package compiler

import (
	"fmt"
	"sort"
	"strings"
)

// StorageClass represents the lexical variable storage category.
type StorageClass uint8

const (
	StorageUnresolved StorageClass = iota
	StorageFastLocal
	StorageCell
	StorageFree
	StorageGlobal
)

// String returns the name of the storage class.
func (s StorageClass) String() string {
	switch s {
	case StorageFastLocal:
		return "FastLocal"
	case StorageCell:
		return "Cell"
	case StorageFree:
		return "Free"
	case StorageGlobal:
		return "Global"
	default:
		return "Unresolved"
	}
}

// ScopeKind defines the lexical scope category.
type ScopeKind uint8

const (
	ScopeModule ScopeKind = iota
	ScopeFunction
	ScopeClass
	ScopeComprehension
)

// Symbol holds classification and offset metadata for an identifier in a scope.
type Symbol struct {
	Name    string
	Storage StorageClass
	Offset  int // stack frame offset for FastLocal, cell/free index for Cell/Free
}

// Scope represents a lexical environment in the symbol table tree.
type Scope struct {
	Kind           ScopeKind
	Name           string
	Parent         *Scope
	Children       []*Scope
	Symbols        map[string]*Symbol
	Bound          map[string]bool
	ExplicitG      map[string]bool // global x
	ExplicitNL     map[string]bool // nonlocal x
	Used           map[string]bool
	IsGenerator    bool
	IsCoroutine    bool
	HasAnnotations bool

	// PEP 649: AnnScope is the synthetic __annotate__(format) function scope
	// owning every annotation expression of this module or class body.
	AnnScope *Scope
	// AnnOwner is the module/class scope an __annotate__ scope belongs to.
	AnnOwner *Scope
	// CanSeeClass marks a class-level __annotate__ scope: names that are not
	// local resolve through the implicit __classdict__ cell first.
	CanSeeClass bool
	// AnnEntries lists the annotated simple names of this body in source order.
	AnnEntries []*AnnEntry
	// AnnByStmt maps an annotated statement to its entry.
	AnnByStmt map[StmtID]*AnnEntry
	// AnnHasCond reports that some annotation sits inside conditional control
	// flow and is tracked through __conditional_annotations__.
	AnnHasCond bool

	ParamNames []string
	CellVars   []string
	FreeVars   []string
	FastLocals []string
	// ClosureSlots is the cell index used by OP_LOAD_CLOSURE.
	// Class scopes keep this even when a name is also a namespace binding
	// (Symbols stays StorageGlobal for load/store).
	ClosureSlots map[string]int
	// NumSlots is the total number of fast-local frame slots (params + locals).
	NumSlots int
	// CompID identifies an inlined comprehension (its arena index) so the
	// compiler can find the hidden slots that preserve shadowed locals.
	CompID uint32
}

// AnnEntry is one `name: annotation` statement in a module or class body.
type AnnEntry struct {
	Stmt StmtID
	Name string
	Expr ExprID
	Idx  int
	Cond bool
}

// Implicit names of the PEP 649 machinery.
const (
	annFormatParam     = ".format"
	annClassDictName   = "__classdict__"
	annCondAnnotations = "__conditional_annotations__"
	annModuleAnnotate  = "__annotate__"
	annClassAnnotate   = "__annotate_func__"
)

// compSaveName is the hidden fast-local that holds the enclosing function's
// value of name while comprehension compID rebinds it (PEP 709).
func compSaveName(compID uint32, name string) string {
	return fmt.Sprintf(".save%d.%s", compID, name)
}

// NewScope creates a new Scope node in the symbol table tree.
func NewScope(kind ScopeKind, name string, parent *Scope) *Scope {
	s := &Scope{
		Kind:       kind,
		Name:       name,
		Parent:     parent,
		Symbols:    make(map[string]*Symbol),
		Bound:      make(map[string]bool),
		ExplicitG:  make(map[string]bool),
		ExplicitNL: make(map[string]bool),
		Used:       make(map[string]bool),
	}
	if parent != nil {
		parent.Children = append(parent.Children, s)
	}
	return s
}

// SymbolTable performs two-pass lexical scope resolution on an AST.
type SymbolTable struct {
	src    []byte
	arena  *ASTArena
	top    *Scope
	cur    *Scope
	Scopes []*Scope
	// condDepth is >0 while visiting statements nested in control flow
	// (if/for/while/try/with/match) of the current scope.
	condDepth int
}

// BuildSymbolTable analyzes the AST and returns the resolved top-level Scope.
func BuildSymbolTable(src []byte, arena *ASTArena, topStmts []StmtID) (*Scope, error) {
	st := &SymbolTable{
		src:   src,
		arena: arena,
		top:   NewScope(ScopeModule, "<module>", nil),
	}
	st.cur = st.top
	st.Scopes = append(st.Scopes, st.top)

	// Pass 1: Traversal to discover bindings, declarations, and usages
	for _, stmtID := range topStmts {
		if err := st.visitStmt(stmtID); err != nil {
			return nil, err
		}
	}

	// Pass 2: Upvalue and closure propagation & storage class assignment
	if err := st.resolveScope(st.top); err != nil {
		return nil, err
	}

	return st.top, nil
}

// BuildSymbolTableForExpr analyzes an expression AST and returns the resolved top-level Scope.
func BuildSymbolTableForExpr(src []byte, arena *ASTArena, exprID ExprID) (*Scope, error) {
	st := &SymbolTable{
		src:   src,
		arena: arena,
		top:   NewScope(ScopeModule, "<module>", nil),
	}
	st.cur = st.top
	st.Scopes = append(st.Scopes, st.top)

	if err := st.visitExpr(exprID); err != nil {
		return nil, err
	}

	if err := st.resolveScope(st.top); err != nil {
		return nil, err
	}

	return st.top, nil
}

func (st *SymbolTable) visitStmt(stmtID StmtID) error {
	stmt := st.arena.GetStmt(stmtID)
	if stmt == nil {
		return nil
	}

	switch stmt.Kind {
	case StmtKindIf, StmtKindFor, StmtKindAsyncFor, StmtKindWhile, StmtKindTry,
		StmtKindWith, StmtKindAsyncWith, StmtKindMatch:
		st.condDepth++
		defer func() { st.condDepth-- }()
	}

	switch stmt.Kind {
	case StmtKindFuncDef, StmtKindAsyncFuncDef:
		fnDef := &st.arena.FuncDefs[stmt.Extra]
		st.cur.Bound[fnDef.Name] = true

		// Evaluate decorators in enclosing scope
		for _, dec := range st.arena.GetExprSlice(fnDef.Decorators) {
			if err := st.visitExpr(dec); err != nil {
				return err
			}
		}

		// Evaluate defaults in enclosing scope
		for _, defExpr := range st.arena.GetExprSlice(fnDef.Defaults) {
			if err := st.visitExpr(defExpr); err != nil {
				return err
			}
		}

		// Function scope
		fnScope := NewScope(ScopeFunction, fnDef.Name, st.cur)
		if stmt.Kind == StmtKindAsyncFuncDef || fnDef.IsAsync {
			fnScope.IsCoroutine = true
		}
		st.Scopes = append(st.Scopes, fnScope)

		// Type parameters
		for _, tp := range st.arena.GetTypeParamSlice(fnDef.TypeParams) {
			fnScope.Bound[tp.Name] = true
			if tp.Bound != ExprNone {
				_ = st.visitExpr(tp.Bound)
			}
		}

		params := st.arena.GetIdentSlice(fnDef.ParamNames)
		fnScope.ParamNames = append(fnScope.ParamNames, params...)
		for _, p := range params {
			clean := p
			if strings.HasPrefix(p, "**") {
				clean = p[2:]
			} else if strings.HasPrefix(p, "*") {
				clean = p[1:]
			}
			if clean != "" {
				fnScope.Bound[clean] = true
			}
			fnScope.Bound[p] = true
		}

		saved, savedCond := st.cur, st.condDepth
		st.cur, st.condDepth = fnScope, 0
		for _, sID := range st.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		st.cur, st.condDepth = saved, savedCond

	case StmtKindClassDef:
		clsDef := &st.arena.ClassDefs[stmt.Extra]
		st.cur.Bound[clsDef.Name] = true

		// Evaluate decorators in enclosing scope
		for _, dec := range st.arena.GetExprSlice(clsDef.Decorators) {
			if err := st.visitExpr(dec); err != nil {
				return err
			}
		}

		// Evaluate bases and keywords in enclosing scope
		for _, bExpr := range st.arena.GetExprSlice(clsDef.Bases) {
			if err := st.visitExpr(bExpr); err != nil {
				return err
			}
		}
		for _, kwExpr := range st.arena.GetExprSlice(clsDef.Keywords) {
			if err := st.visitExpr(kwExpr); err != nil {
				return err
			}
		}

		// Class scope
		clsScope := NewScope(ScopeClass, clsDef.Name, st.cur)
		st.Scopes = append(st.Scopes, clsScope)

		// Type parameters
		for _, tp := range st.arena.GetTypeParamSlice(clsDef.TypeParams) {
			clsScope.Bound[tp.Name] = true
			if tp.Bound != ExprNone {
				_ = st.visitExpr(tp.Bound)
			}
		}

		saved, savedCond := st.cur, st.condDepth
		st.cur, st.condDepth = clsScope, 0
		for _, sID := range st.arena.StmtIDs[clsDef.Body.Start : clsDef.Body.Start+clsDef.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		st.cur, st.condDepth = saved, savedCond

	case StmtKindGlobal:
		slice := st.arena.IdentSlices[stmt.Extra]
		names := st.arena.Idents[slice.Start : slice.Start+slice.Count]
		for _, name := range names {
			st.cur.ExplicitG[name] = true
		}

	case StmtKindNonlocal:
		slice := st.arena.IdentSlices[stmt.Extra]
		names := st.arena.Idents[slice.Start : slice.Start+slice.Count]
		for _, name := range names {
			st.cur.ExplicitNL[name] = true
		}

	case StmtKindAssign:
		if err := st.visitExpr(stmt.Expr2); err != nil {
			return err
		}
		count := stmt.Extra >> 16
		if count > 1 {
			start := stmt.Extra & 0xFFFF
			targets := st.arena.ExprIDs[start : start+count]
			for _, t := range targets {
				st.bindTarget(t)
			}
		} else {
			st.bindTarget(stmt.Expr1)
		}

	case StmtKindAugAssign:
		if err := st.visitExpr(stmt.Expr2); err != nil {
			return err
		}
		if err := st.visitExpr(stmt.Expr1); err != nil {
			return err
		}
		st.bindTarget(stmt.Expr1)

	case StmtKindFor, StmtKindAsyncFor:
		if err := st.visitExpr(stmt.Expr2); err != nil {
			return err
		}
		st.bindTarget(stmt.Expr1)
		for _, sID := range st.arena.GetStmtSlice(stmt.Body) {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		if stmt.Else != 0 {
			for _, sID := range st.arena.GetStmtSlice(stmt.Else) {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}

	case StmtKindWhile:
		if err := st.visitExpr(stmt.Expr1); err != nil {
			return err
		}
		for _, sID := range st.arena.GetStmtSlice(stmt.Body) {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		if stmt.Else != 0 {
			for _, sID := range st.arena.GetStmtSlice(stmt.Else) {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}

	case StmtKindIf:
		if err := st.visitExpr(stmt.Expr1); err != nil {
			return err
		}
		for _, sID := range st.arena.GetStmtSlice(stmt.Body) {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		elifsStart := stmt.Extra >> 16
		elifsCount := stmt.Extra & 0xFFFF
		for i := uint32(0); i < elifsCount; i++ {
			elif := &st.arena.Elifs[elifsStart+i]
			if err := st.visitExpr(elif.Cond); err != nil {
				return err
			}
			for _, sID := range st.arena.StmtIDs[elif.Body.Start : elif.Body.Start+elif.Body.Count] {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}
		if stmt.Else != 0 {
			for _, sID := range st.arena.GetStmtSlice(stmt.Else) {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}

	case StmtKindReturn, StmtKindExpr:
		if stmt.Expr1 != ExprNone {
			return st.visitExpr(stmt.Expr1)
		}

	case StmtKindTry:
		tryStmt := &st.arena.TryStmts[stmt.Extra]
		for _, sID := range st.arena.StmtIDs[tryStmt.Body.Start : tryStmt.Body.Start+tryStmt.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		for _, h := range st.arena.GetExceptSlice(tryStmt.Handlers) {
			if h.Type != ExprNone {
				if err := st.visitExpr(h.Type); err != nil {
					return err
				}
			}
			if h.Name != "" {
				st.cur.Bound[h.Name] = true
			}
			for _, sID := range st.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count] {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}
		if tryStmt.Else.Count > 0 {
			for _, sID := range st.arena.StmtIDs[tryStmt.Else.Start : tryStmt.Else.Start+tryStmt.Else.Count] {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}
		if tryStmt.Finally.Count > 0 {
			for _, sID := range st.arena.StmtIDs[tryStmt.Finally.Start : tryStmt.Finally.Start+tryStmt.Finally.Count] {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}

	case StmtKindRaise:
		if stmt.Expr1 != ExprNone {
			if err := st.visitExpr(stmt.Expr1); err != nil {
				return err
			}
		}
		if stmt.Expr2 != ExprNone {
			if err := st.visitExpr(stmt.Expr2); err != nil {
				return err
			}
		}

	case StmtKindWith, StmtKindAsyncWith:
		sliceStart := stmt.Extra & 0xFFFF
		sliceCount := stmt.Extra >> 16
		items := st.arena.WithItems[sliceStart : sliceStart+sliceCount]
		for _, item := range items {
			if err := st.visitExpr(item.Expr); err != nil {
				return err
			}
			if item.Target != ExprNone {
				st.bindTarget(item.Target)
			}
		}
		for _, sID := range st.arena.GetStmtSlice(stmt.Body) {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}

	case StmtKindImport:
		imp := st.arena.GetImport(stmt.Extra)
		if imp != nil {
			for _, alias := range st.arena.GetAliasSlice(imp.Aliases) {
				if alias.AsName != "" {
					st.cur.Bound[alias.AsName] = true
				} else {
					parts := strings.Split(alias.Name, ".")
					st.cur.Bound[parts[0]] = true
				}
			}
		}

	case StmtKindImportFrom:
		impFrom := st.arena.GetImportFrom(stmt.Extra)
		if impFrom != nil && !impFrom.IsStar {
			for _, alias := range st.arena.GetAliasSlice(impFrom.Aliases) {
				target := alias.Name
				if alias.AsName != "" {
					target = alias.AsName
				}
				st.cur.Bound[target] = true
			}
		}

	case StmtKindMatch:
		matchStmt := &st.arena.MatchStmts[stmt.Extra]
		if err := st.visitExpr(matchStmt.Subject); err != nil {
			return err
		}
		for _, matchCase := range st.arena.GetMatchCaseSlice(matchStmt.Cases) {
			if err := st.bindAndVisitPattern(matchCase.Pattern); err != nil {
				return err
			}
			if matchCase.Guard != ExprNone {
				if err := st.visitExpr(matchCase.Guard); err != nil {
					return err
				}
			}
			for _, sID := range st.arena.GetStmtSliceDirect(matchCase.Body) {
				if err := st.visitStmt(sID); err != nil {
					return err
				}
			}
		}

	case StmtKindTypeAlias:
		alias := &st.arena.TypeAliases[stmt.Extra]
		st.cur.Bound[alias.Name] = true
		for _, tp := range st.arena.GetTypeParamSlice(alias.TypeParams) {
			st.cur.Bound[tp.Name] = true
			if tp.Bound != ExprNone {
				_ = st.visitExpr(tp.Bound)
			}
		}
		if err := st.visitExpr(alias.Value); err != nil {
			return err
		}

	case StmtKindAssert:
		if err := st.visitExpr(stmt.Expr1); err != nil {
			return err
		}
		if stmt.Expr2 != ExprNone {
			return st.visitExpr(stmt.Expr2)
		}

	case StmtKindDelete:
		st.visitDelete(stmt.Expr1)

	case StmtKindAnnAssign:
		if stmt.Expr2 != ExprNone {
			if err := st.visitExpr(stmt.Expr2); err != nil {
				return err
			}
			st.bindTarget(stmt.Expr1)
		} else if st.cur.Kind == ScopeFunction {
			st.bindTarget(stmt.Expr1)
		}
		annExprID := ExprID(stmt.Extra)
		if annExprID != ExprNone && (st.cur.Kind == ScopeClass || st.cur.Kind == ScopeModule) {
			targetExpr := st.arena.GetExpr(stmt.Expr1)
			if targetExpr != nil && targetExpr.Kind == ExprKindName {
				owner := st.cur
				owner.HasAnnotations = true
				if owner.AnnScope == nil {
					as := NewScope(ScopeFunction, "__annotate__", owner)
					as.ParamNames = []string{annFormatParam}
					as.Bound[annFormatParam] = true
					as.AnnOwner = owner
					as.CanSeeClass = owner.Kind == ScopeClass
					owner.AnnScope = as
					owner.AnnByStmt = make(map[StmtID]*AnnEntry)
					st.Scopes = append(st.Scopes, as)
				}
				entry := &AnnEntry{
					Stmt: stmtID,
					Name: string(st.src[targetExpr.Start:targetExpr.End]),
					Expr: annExprID,
					Idx:  len(owner.AnnEntries),
					Cond: st.condDepth > 0,
				}
				if entry.Cond {
					owner.AnnHasCond = true
				}
				owner.AnnEntries = append(owner.AnnEntries, entry)
				owner.AnnByStmt[stmtID] = entry
				savedCur := st.cur
				st.cur = owner.AnnScope
				err := st.visitExpr(annExprID)
				st.cur = savedCur
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (st *SymbolTable) bindAndVisitPattern(patID PatternID) error {
	pat := st.arena.GetPattern(patID)
	if pat == nil {
		return nil
	}
	switch pat.Kind {
	case PatternKindValue:
		if pat.Expr1 != ExprNone {
			return st.visitExpr(pat.Expr1)
		}
	case PatternKindSingleton:
		if pat.Expr1 != ExprNone {
			return st.visitExpr(pat.Expr1)
		}
	case PatternKindCapture:
		if pat.Name != "" && pat.Name != "_" {
			st.cur.Bound[pat.Name] = true
		}
	case PatternKindWildcard:
		// _ does not bind
	case PatternKindAs:
		if pat.Expr1 != ExprNone {
			if err := st.bindAndVisitPattern(PatternID(pat.Expr1)); err != nil {
				return err
			}
		}
		if pat.Name != "" && pat.Name != "_" {
			st.cur.Bound[pat.Name] = true
		}
	case PatternKindOr:
		count := uint32(pat.Expr1)
		for i := uint32(0); i < count; i++ {
			subID := st.arena.PatternIDs[pat.Extra+i]
			if err := st.bindAndVisitPattern(subID); err != nil {
				return err
			}
		}
	case PatternKindSequence:
		count := uint32(pat.Expr1)
		for i := uint32(0); i < count; i++ {
			subID := st.arena.PatternIDs[pat.Extra+i]
			if err := st.bindAndVisitPattern(subID); err != nil {
				return err
			}
		}
	case PatternKindMapping:
		count := uint32(pat.Expr2)
		for i := uint32(0); i < count; i++ {
			subID := st.arena.PatternIDs[pat.Extra+i]
			if err := st.bindAndVisitPattern(subID); err != nil {
				return err
			}
		}
		if pat.Name != "" && pat.Name != "_" {
			st.cur.Bound[pat.Name] = true
		}
	case PatternKindClass:
		if pat.Expr1 != ExprNone {
			if err := st.visitExpr(pat.Expr1); err != nil {
				return err
			}
		}
		posCount := uint32(pat.Expr2) >> 16
		kwCount := uint32(pat.Expr2) & 0xFFFF
		totalCount := posCount + kwCount
		for i := uint32(0); i < totalCount; i++ {
			subID := st.arena.PatternIDs[pat.Extra+i]
			if err := st.bindAndVisitPattern(subID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (st *SymbolTable) visitDelete(exprID ExprID) {
	expr := st.arena.GetExpr(exprID)
	if expr == nil {
		return
	}
	switch expr.Kind {
	case ExprKindName:
		name := string(st.src[expr.Start:expr.End])
		st.cur.Bound[name] = true
	case ExprKindTuple, ExprKindList:
		slice := st.arena.ExprSlices[expr.Extra]
		for _, elem := range st.arena.ExprIDs[slice.Start : slice.Start+slice.Count] {
			st.visitDelete(elem)
		}
	case ExprKindAttribute:
		_ = st.visitExpr(expr.Left)
	case ExprKindSubscript:
		_ = st.visitExpr(expr.Left)
		_ = st.visitExpr(expr.Right)
	}
}

func (st *SymbolTable) bindTarget(targetID ExprID) {
	expr := st.arena.GetExpr(targetID)
	if expr == nil {
		return
	}
	switch expr.Kind {
	case ExprKindName:
		name := string(st.src[expr.Start:expr.End])
		st.cur.Bound[name] = true
	case ExprKindTuple, ExprKindList:
		slice := st.arena.ExprSlices[expr.Extra]
		for _, elemID := range st.arena.ExprIDs[slice.Start : slice.Start+slice.Count] {
			st.bindTarget(elemID)
		}
	case ExprKindStarred:
		st.bindTarget(expr.Left)
	case ExprKindAttribute:
		_ = st.visitExpr(expr.Left)
	case ExprKindSubscript:
		_ = st.visitExpr(expr.Left)
		_ = st.visitExpr(expr.Right)
	}
}

func (st *SymbolTable) visitExpr(exprID ExprID) error {
	expr := st.arena.GetExpr(exprID)
	if expr == nil {
		return nil
	}

	switch expr.Kind {
	case ExprKindName:
		name := string(st.src[expr.Start:expr.End])
		st.cur.Used[name] = true

	case ExprKindUnary:
		return st.visitExpr(expr.Left)

	case ExprKindBinary, ExprKindCompare:
		if err := st.visitExpr(expr.Left); err != nil {
			return err
		}
		return st.visitExpr(expr.Right)

	case ExprKindIfExp:
		if err := st.visitExpr(expr.Left); err != nil {
			return err
		}
		if err := st.visitExpr(ExprID(expr.Extra)); err != nil {
			return err
		}
		return st.visitExpr(expr.Right)

	case ExprKindCall:
		call := &st.arena.Calls[expr.Extra]
		if err := st.visitExpr(call.Func); err != nil {
			return err
		}
		// Zero-arg super() implicitly references __class__
		if call.PosArgs.Count == 0 && call.KWArgs.Count == 0 {
			fExpr := st.arena.GetExpr(call.Func)
			if fExpr != nil && fExpr.Kind == ExprKindName {
				fName := string(st.src[fExpr.Start:fExpr.End])
				if fName == "super" {
					st.cur.Used["__class__"] = true
				}
			}
		}
		for _, arg := range st.arena.GetExprSlice(call.PosArgs) {
			if err := st.visitExpr(arg); err != nil {
				return err
			}
		}
		for _, kwArg := range st.arena.GetExprSlice(call.KWArgs) {
			if err := st.visitExpr(kwArg); err != nil {
				return err
			}
		}

	case ExprKindAttribute:
		return st.visitExpr(expr.Left)

	case ExprKindSubscript:
		if err := st.visitExpr(expr.Left); err != nil {
			return err
		}
		return st.visitExpr(expr.Right)

	case ExprKindList, ExprKindTuple:
		slice := st.arena.ExprSlices[expr.Extra]
		for _, elem := range st.arena.ExprIDs[slice.Start : slice.Start+slice.Count] {
			if err := st.visitExpr(elem); err != nil {
				return err
			}
		}

	case ExprKindDict:
		start := expr.Extra
		count := uint32(expr.Right)
		for i := uint32(0); i < count; i++ {
			pair := &st.arena.DictPairs[start+i]
			if err := st.visitExpr(pair.Key); err != nil {
				return err
			}
			if err := st.visitExpr(pair.Value); err != nil {
				return err
			}
		}

	case ExprKindListComp, ExprKindDictComp, ExprKindSetComp:
		comp := &st.arena.Comps[expr.Extra]
		if err := st.visitExpr(comp.Iter); err != nil {
			return err
		}
		compScope := NewScope(ScopeComprehension, "<comprehension>", st.cur)
		compScope.CompID = expr.Extra
		st.Scopes = append(st.Scopes, compScope)

		saved := st.cur
		st.cur = compScope
		currComp := comp
		for {
			st.bindTarget(currComp.Target)
			for _, ifCond := range st.arena.GetExprSlice(currComp.IfConds) {
				if err := st.visitExpr(ifCond); err != nil {
					return err
				}
			}
			if currComp.Next == 0 {
				break
			}
			currComp = &st.arena.Comps[currComp.Next-1]
			if err := st.visitExpr(currComp.Iter); err != nil {
				return err
			}
		}
		if expr.Kind == ExprKindListComp || expr.Kind == ExprKindSetComp {
			if err := st.visitExpr(expr.Left); err != nil {
				return err
			}
		} else {
			if err := st.visitExpr(expr.Left); err != nil {
				return err
			}
			if err := st.visitExpr(expr.Right); err != nil {
				return err
			}
		}
		st.cur = saved

	case ExprKindGeneratorExp:
		comp := &st.arena.Comps[expr.Extra]
		if err := st.visitExpr(comp.Iter); err != nil {
			return err
		}
		genScope := NewScope(ScopeFunction, "<genexpr>", st.cur)
		genScope.IsGenerator = true
		genScope.ParamNames = []string{".0"}
		genScope.Bound[".0"] = true
		st.Scopes = append(st.Scopes, genScope)

		saved := st.cur
		st.cur = genScope
		currGenComp := comp
		for {
			st.bindTarget(currGenComp.Target)
			for _, ifCond := range st.arena.GetExprSlice(currGenComp.IfConds) {
				if err := st.visitExpr(ifCond); err != nil {
					return err
				}
			}
			if currGenComp.Next == 0 {
				break
			}
			currGenComp = &st.arena.Comps[currGenComp.Next-1]
			if err := st.visitExpr(currGenComp.Iter); err != nil {
				return err
			}
		}
		if err := st.visitExpr(expr.Left); err != nil {
			return err
		}
		st.cur = saved

	case ExprKindLambda:
		fnDef := &st.arena.FuncDefs[expr.Extra]
		for _, defExpr := range st.arena.GetExprSlice(fnDef.Defaults) {
			if err := st.visitExpr(defExpr); err != nil {
				return err
			}
		}
		lambdaScope := NewScope(ScopeFunction, "<lambda>", st.cur)
		st.Scopes = append(st.Scopes, lambdaScope)
		params := st.arena.GetIdentSlice(fnDef.ParamNames)
		lambdaScope.ParamNames = append(lambdaScope.ParamNames, params...)
		for _, p := range params {
			clean := p
			if strings.HasPrefix(p, "**") {
				clean = p[2:]
			} else if strings.HasPrefix(p, "*") {
				clean = p[1:]
			}
			if clean != "" {
				lambdaScope.Bound[clean] = true
			}
			lambdaScope.Bound[p] = true
		}
		saved := st.cur
		st.cur = lambdaScope
		// The parser stores a lambda body as a synthesized `return <expr>`
		// statement; Left is unused.
		for _, sID := range st.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				st.cur = saved
				return err
			}
		}
		if expr.Left != ExprNone {
			if err := st.visitExpr(expr.Left); err != nil {
				st.cur = saved
				return err
			}
		}
		st.cur = saved

	case ExprKindStarred:
		return st.visitExpr(expr.Left)

	case ExprKindNamedExpr:
		if err := st.visitExpr(expr.Right); err != nil {
			return err
		}
		targetScope := st.cur
		for targetScope != nil && targetScope.Kind == ScopeComprehension {
			targetScope = targetScope.Parent
		}
		if targetScope != nil {
			targetExpr := st.arena.GetExpr(expr.Left)
			if targetExpr != nil && targetExpr.Kind == ExprKindName {
				name := string(st.src[targetExpr.Start:targetExpr.End])
				targetScope.Bound[name] = true
			}
		}
		return st.visitExpr(expr.Left)

	case ExprKindJoinedStr, ExprKindTemplateStr:
		fs := &st.arena.FStrings[expr.Extra]
		for _, pExpr := range st.arena.GetExprSlice(fs.Pieces) {
			if err := st.visitExpr(pExpr); err != nil {
				return err
			}
		}

	case ExprKindFormattedValue:
		fv := &st.arena.FormattedValues[expr.Extra]
		if err := st.visitExpr(fv.Value); err != nil {
			return err
		}
		if fv.FormatSpec != ExprNone {
			return st.visitExpr(fv.FormatSpec)
		}

	case ExprKindSet:
		slice := st.arena.ExprSlices[expr.Extra]
		for _, elem := range st.arena.ExprIDs[slice.Start : slice.Start+slice.Count] {
			if err := st.visitExpr(elem); err != nil {
				return err
			}
		}

	case ExprKindSlice:
		if expr.Left != ExprNone {
			if err := st.visitExpr(expr.Left); err != nil {
				return err
			}
		}
		if expr.Right != ExprNone {
			if err := st.visitExpr(expr.Right); err != nil {
				return err
			}
		}
		if expr.Extra != 0 {
			if err := st.visitExpr(ExprID(expr.Extra - 1)); err != nil {
				return err
			}
		}

	case ExprKindEllipsis:
		return nil

	case ExprKindYield:
		st.cur.IsGenerator = true
		if expr.Left != ExprNone {
			return st.visitExpr(expr.Left)
		}

	case ExprKindYieldFrom:
		st.cur.IsGenerator = true
		if expr.Left != ExprNone {
			return st.visitExpr(expr.Left)
		}

	case ExprKindAwait:
		if expr.Left != ExprNone {
			return st.visitExpr(expr.Left)
		}
	}

	return nil
}

// Pass 2: Upvalue resolution and storage classification
func (st *SymbolTable) resolveScope(s *Scope) error {
	// First recursively resolve children so upvalues propagate outward
	for _, child := range s.Children {
		if err := st.resolveScope(child); err != nil {
			return err
		}
	}

	// Nonlocal checks
	for name := range s.ExplicitNL {
		found := false
		for p := s.Parent; p != nil; p = p.Parent {
			if p.Kind == ScopeFunction || p.Kind == ScopeComprehension {
				if p.Bound[name] {
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("SyntaxError: no binding for nonlocal '%s' found", name)
		}
	}

	if s.Kind == ScopeModule {
		for name := range s.Bound {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
		for name := range s.Used {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
		return nil
	}

	if s.Kind == ScopeClass {
		if s.ClosureSlots == nil {
			s.ClosureSlots = make(map[string]int)
		}
		for idx, cv := range s.CellVars {
			s.Symbols[cv] = &Symbol{Name: cv, Storage: StorageCell, Offset: idx}
			s.ClosureSlots[cv] = idx
		}
		// Namespace bindings win for load/store. A nested method can still
		// close over an enclosing function local of the same name.
		for name := range s.Bound {
			if sym := s.Symbols[name]; sym != nil && sym.Storage == StorageCell {
				continue
			}
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
		for name := range s.Used {
			if s.Bound[name] || containsString(s.FreeVars, name) {
				continue
			}
			if sym := s.Symbols[name]; sym != nil && sym.Storage == StorageCell {
				continue
			}
			if st.isBoundInFunctionAncestors(s.Parent, name) {
				s.FreeVars = append(s.FreeVars, name)
				st.markAncestorCell(s.Parent, name)
			}
		}
		for idx, fv := range s.FreeVars {
			off := len(s.CellVars) + idx
			if sym := s.Symbols[fv]; sym != nil && sym.Storage == StorageCell {
				s.ClosureSlots[fv] = sym.Offset
				continue
			}
			s.ClosureSlots[fv] = off
			if s.Bound[fv] {
				continue
			}
			s.Symbols[fv] = &Symbol{Name: fv, Storage: StorageFree, Offset: off}
		}
		for name := range s.Used {
			if s.Symbols[name] == nil {
				s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
			}
		}
		return nil
	}

	if s.Kind == ScopeComprehension {
		boundNames := make([]string, 0, len(s.Bound))
		for name := range s.Bound {
			boundNames = append(boundNames, name)
		}
		sort.Strings(boundNames)
		for _, name := range boundNames {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageFastLocal}
			s.FastLocals = append(s.FastLocals, name)
			fn := s.Parent
			for fn != nil && fn.Kind == ScopeComprehension {
				fn = fn.Parent
			}
			if fn != nil && fn.Kind == ScopeFunction && (fn.Bound[name] || isParam(fn.ParamNames, name)) {
				// The comprehension shares this slot with a local of the
				// enclosing function. Reserve a hidden slot to restore it.
				hidden := compSaveName(s.CompID, name)
				if fn.Symbols[hidden] == nil {
					fn.Symbols[hidden] = &Symbol{Name: hidden, Storage: StorageFastLocal}
					fn.FastLocals = append(fn.FastLocals, hidden)
				}
			}
			if s.Parent != nil && s.Parent.Kind == ScopeFunction {
				if !containsString(s.Parent.FastLocals, name) && !s.Parent.Bound[name] {
					s.Parent.FastLocals = append(s.Parent.FastLocals, name)
					s.Parent.Symbols[name] = &Symbol{
						Name:    name,
						Storage: StorageFastLocal,
						Offset:  len(s.Parent.FastLocals) - 1,
					}
				}
			}
		}
		for name := range s.Used {
			if s.Symbols[name] != nil {
				continue
			}
			if s.Parent != nil && s.Parent.Kind == ScopeFunction && s.Parent.Bound[name] {
				s.Symbols[name] = &Symbol{Name: name, Storage: StorageFastLocal}
			} else if st.isBoundInFunctionAncestors(s.Parent, name) {
				s.Symbols[name] = &Symbol{Name: name, Storage: StorageFree}
				s.FreeVars = append(s.FreeVars, name)
				st.markAncestorCell(s.Parent, name)
			} else {
				s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
			}
		}
		offset := 0
		if s.Parent != nil && s.Parent.Kind == ScopeFunction {
			offset = len(s.Parent.FastLocals)
		}
		for _, fl := range s.FastLocals {
			sym := s.Symbols[fl]
			if sym != nil {
				sym.Offset = offset
				offset++
			}
		}
		s.NumSlots = offset
		return nil
	}

	// Function and Comprehension Scopes:
	// 1. Process variables that are explicitly nonlocal
	for name := range s.ExplicitNL {
		s.Symbols[name] = &Symbol{Name: name, Storage: StorageFree}
		s.FreeVars = append(s.FreeVars, name)
		st.markAncestorCell(s.Parent, name)
	}

	// 2. Process variables bound in this function
	for _, param := range s.ParamNames {
		clean := param
		if strings.HasPrefix(param, "**") {
			clean = param[2:]
		} else if strings.HasPrefix(param, "*") {
			clean = param[1:]
		}
		if clean == "" {
			continue
		}
		if s.ExplicitG[clean] || s.ExplicitNL[clean] {
			continue
		}
		if st.isCapturedByDescendants(s, clean) || (s.Symbols[clean] != nil && s.Symbols[clean].Storage == StorageCell) {
			if s.Symbols[clean] == nil || s.Symbols[clean].Storage != StorageCell {
				s.Symbols[clean] = &Symbol{Name: clean, Storage: StorageCell}
			}
			if !containsString(s.CellVars, clean) {
				s.CellVars = append(s.CellVars, clean)
			}
		} else {
			s.Symbols[clean] = &Symbol{Name: clean, Storage: StorageFastLocal}
			s.FastLocals = append(s.FastLocals, clean)
		}
	}

	for name := range s.Bound {
		if s.ExplicitG[name] {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
			continue
		}
		if s.ExplicitNL[name] {
			continue
		}
		if isParam(s.ParamNames, name) {
			continue
		}
		if st.isCapturedByDescendants(s, name) || (s.Symbols[name] != nil && s.Symbols[name].Storage == StorageCell) {
			if s.Symbols[name] == nil || s.Symbols[name].Storage != StorageCell {
				s.Symbols[name] = &Symbol{Name: name, Storage: StorageCell}
			}
			if !containsString(s.CellVars, name) {
				s.CellVars = append(s.CellVars, name)
			}
		} else {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageFastLocal}
			s.FastLocals = append(s.FastLocals, name)
		}
	}

	// PEP 649: a class-level __annotate__ closes over the class namespace
	// (and the conditional-annotation set) through implicit class cells.
	if s.CanSeeClass && s.AnnOwner != nil {
		implicit := []string{annClassDictName}
		if s.AnnOwner.AnnHasCond {
			implicit = append(implicit, annCondAnnotations)
		}
		for _, name := range implicit {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageFree}
			s.FreeVars = append(s.FreeVars, name)
			st.markAncestorCell(s.Parent, name)
		}
	}

	// 3. Process variables used but not bound locally
	for name := range s.Used {
		if s.Symbols[name] != nil {
			continue
		}
		if s.ExplicitG[name] {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
			continue
		}
		// Look up in lexical ancestor functions
		if st.isBoundInFunctionAncestors(s.Parent, name) {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageFree}
			s.FreeVars = append(s.FreeVars, name)
			st.markAncestorCell(s.Parent, name)
		} else {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
	}

	// Assign static offsets:
	// Parameters and FastLocals: 0 .. NumLocals-1
	offset := 0
	for _, p := range s.ParamNames {
		clean := p
		if strings.HasPrefix(p, "**") {
			clean = p[2:]
		} else if strings.HasPrefix(p, "*") {
			clean = p[1:]
		}
		if clean == "" {
			continue
		}
		// Every parameter occupies the frame slot matching its ParamNames index,
		// including cell parameters (their initial value is copied into the cell).
		sym := s.Symbols[clean]
		if sym != nil && sym.Storage == StorageFastLocal {
			sym.Offset = offset
		}
		offset++
	}
	// Assign every non-parameter fast local a fresh slot. Offset 0 is a real
	// slot (the first parameter), so it cannot mean "unassigned". A symbol
	// that already carries an offset — for example a comprehension target
	// injected before this scope is resolved — must still be renumbered,
	// or it collides with whichever local is visited last.
	seen := make(map[*Symbol]struct{}, len(s.FastLocals))
	for _, fl := range s.FastLocals {
		sym := s.Symbols[fl]
		if sym == nil || sym.Storage != StorageFastLocal || isParam(s.ParamNames, fl) {
			continue
		}
		if _, ok := seen[sym]; ok {
			continue
		}
		seen[sym] = struct{}{}
		sym.Offset = offset
		offset++
	}
	s.NumSlots = offset

	// Cell offsets: 0 .. len(CellVars)-1
	for idx, cv := range s.CellVars {
		if sym := s.Symbols[cv]; sym != nil {
			sym.Offset = idx
		}
	}
	// Free offsets: len(CellVars) .. len(CellVars)+len(FreeVars)-1
	for idx, fv := range s.FreeVars {
		if sym := s.Symbols[fv]; sym != nil {
			sym.Offset = len(s.CellVars) + idx
		}
	}

	return nil
}

func (st *SymbolTable) markAncestorCell(start *Scope, name string) {
	for p := start; p != nil; p = p.Parent {
		if p.Kind == ScopeClass && (name == "__class__" || name == annClassDictName || name == annCondAnnotations) {
			sym := p.Symbols[name]
			if sym == nil {
				sym = &Symbol{Name: name}
				p.Symbols[name] = sym
			}
			sym.Storage = StorageCell
			if !containsString(p.CellVars, name) {
				p.CellVars = append(p.CellVars, name)
			}
			return
		}
		if p.Kind == ScopeModule {
			continue
		}
		if p.Kind == ScopeClass {
			// Class bodies are not an enclosing scope, but they must carry
			// the cell so methods created inside the class can close over it.
			if !containsString(p.FreeVars, name) {
				p.FreeVars = append(p.FreeVars, name)
			}
			continue
		}
		if p.Bound[name] {
			// Found defining scope: mark as Cell
			sym := p.Symbols[name]
			if sym == nil {
				sym = &Symbol{Name: name}
				p.Symbols[name] = sym
			}
			if sym.Storage != StorageCell {
				sym.Storage = StorageCell
				if !containsString(p.CellVars, name) {
					p.CellVars = append(p.CellVars, name)
				}
				// Remove from FastLocals if was there
				p.FastLocals = removeString(p.FastLocals, name)
			}
			return
		}
		// Intermediate scope: mark as Free so it passes through
		sym := p.Symbols[name]
		if sym == nil {
			sym = &Symbol{Name: name}
			p.Symbols[name] = sym
		}
		if sym.Storage != StorageFree && sym.Storage != StorageCell {
			sym.Storage = StorageFree
			if !containsString(p.FreeVars, name) {
				p.FreeVars = append(p.FreeVars, name)
			}
		}
	}
}

func (st *SymbolTable) isBoundInFunctionAncestors(start *Scope, name string) bool {
	for p := start; p != nil; p = p.Parent {
		if p.Kind == ScopeModule {
			return false
		}
		if p.Kind == ScopeClass {
			if name == "__class__" {
				return true
			}
			continue
		}
		if p.Kind == ScopeFunction {
			if p.Bound[name] {
				return true
			}
		}
	}
	return false
}

func (st *SymbolTable) isCapturedByDescendants(s *Scope, name string) bool {
	var check func(cur *Scope) bool
	check = func(cur *Scope) bool {
		for _, child := range cur.Children {
			if child.Kind == ScopeFunction {
				if child.ExplicitG[name] {
					continue
				}
				if child.Used[name] && !child.Bound[name] {
					return true
				}
				if child.ExplicitNL[name] {
					return true
				}
			}
			if child.Kind == ScopeClass && containsString(child.FreeVars, name) {
				return true
			}
			if check(child) {
				return true
			}
		}
		return false
	}
	return check(s)
}

func isParam(params []string, name string) bool {
	for _, p := range params {
		if p == name || p == "*"+name || p == "**"+name {
			return true
		}
	}
	return false
}

func containsString(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	var res []string
	for _, x := range slice {
		if x != s {
			res = append(res, x)
		}
	}
	return res
}
