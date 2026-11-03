package compiler

import (
	"fmt"
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
	Kind       ScopeKind
	Name       string
	Parent     *Scope
	Children   []*Scope
	Symbols    map[string]*Symbol
	Bound      map[string]bool
	ExplicitG  map[string]bool // global x
	ExplicitNL map[string]bool // nonlocal x
	Used       map[string]bool

	ParamNames []string
	CellVars   []string
	FreeVars   []string
	FastLocals []string
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

func (st *SymbolTable) visitStmt(stmtID StmtID) error {
	stmt := st.arena.GetStmt(stmtID)
	if stmt == nil {
		return nil
	}

	switch stmt.Kind {
	case StmtKindFuncDef:
		fnDef := &st.arena.FuncDefs[stmt.Extra]
		st.cur.Bound[fnDef.Name] = true

		// Evaluate defaults in enclosing scope
		for _, defExpr := range st.arena.GetExprSlice(fnDef.Defaults) {
			if err := st.visitExpr(defExpr); err != nil {
				return err
			}
		}

		// Function scope
		fnScope := NewScope(ScopeFunction, fnDef.Name, st.cur)
		st.Scopes = append(st.Scopes, fnScope)
		params := st.arena.GetIdentSlice(fnDef.ParamNames)
		fnScope.ParamNames = append(fnScope.ParamNames, params...)
		for _, p := range params {
			fnScope.Bound[p] = true
		}

		saved := st.cur
		st.cur = fnScope
		bodyStmts := st.arena.GetStmtSlice(fnDef.Body.Start) // Body.Start in StmtSlice
		_ = bodyStmts
		for _, sID := range st.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		st.cur = saved

	case StmtKindClassDef:
		clsDef := &st.arena.ClassDefs[stmt.Extra]
		st.cur.Bound[clsDef.Name] = true

		// Evaluate bases in enclosing scope
		for _, bExpr := range st.arena.GetExprSlice(clsDef.Bases) {
			if err := st.visitExpr(bExpr); err != nil {
				return err
			}
		}

		// Class scope
		clsScope := NewScope(ScopeClass, clsDef.Name, st.cur)
		st.Scopes = append(st.Scopes, clsScope)

		saved := st.cur
		st.cur = clsScope
		for _, sID := range st.arena.StmtIDs[clsDef.Body.Start : clsDef.Body.Start+clsDef.Body.Count] {
			if err := st.visitStmt(sID); err != nil {
				return err
			}
		}
		st.cur = saved

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
		st.bindTarget(stmt.Expr1)

	case StmtKindAugAssign:
		if err := st.visitExpr(stmt.Expr2); err != nil {
			return err
		}
		if err := st.visitExpr(stmt.Expr1); err != nil {
			return err
		}
		st.bindTarget(stmt.Expr1)

	case StmtKindFor:
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
	}

	return nil
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
		count := uint32(expr.Op)
		for i := uint32(0); i < count; i++ {
			pair := &st.arena.DictPairs[start+i]
			if err := st.visitExpr(pair.Key); err != nil {
				return err
			}
			if err := st.visitExpr(pair.Value); err != nil {
				return err
			}
		}

	case ExprKindListComp, ExprKindDictComp:
		comp := &st.arena.Comps[expr.Extra]
		if err := st.visitExpr(comp.Iter); err != nil {
			return err
		}
		// Comprehension introduces a nested scope
		compScope := NewScope(ScopeComprehension, "<comprehension>", st.cur)
		st.Scopes = append(st.Scopes, compScope)

		saved := st.cur
		st.cur = compScope
		st.bindTarget(comp.Target)
		for _, ifCond := range st.arena.GetExprSlice(comp.IfConds) {
			if err := st.visitExpr(ifCond); err != nil {
				return err
			}
		}
		if expr.Kind == ExprKindListComp {
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
		for name := range s.Bound {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
		for name := range s.Used {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageGlobal}
		}
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
		if s.ExplicitG[param] || s.ExplicitNL[param] {
			continue
		}
		if st.isCapturedByDescendants(s, param) {
			s.Symbols[param] = &Symbol{Name: param, Storage: StorageCell}
			s.CellVars = append(s.CellVars, param)
		} else {
			s.Symbols[param] = &Symbol{Name: param, Storage: StorageFastLocal}
			s.FastLocals = append(s.FastLocals, param)
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
		if st.isCapturedByDescendants(s, name) {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageCell}
			s.CellVars = append(s.CellVars, name)
		} else {
			s.Symbols[name] = &Symbol{Name: name, Storage: StorageFastLocal}
			s.FastLocals = append(s.FastLocals, name)
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
		sym := s.Symbols[p]
		if sym != nil && sym.Storage == StorageFastLocal {
			sym.Offset = offset
			offset++
		}
	}
	for _, fl := range s.FastLocals {
		sym := s.Symbols[fl]
		if sym != nil && sym.Offset == 0 && !isParam(s.ParamNames, fl) {
			sym.Offset = offset
			offset++
		}
	}

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
		if p.Kind == ScopeModule || p.Kind == ScopeClass {
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
		if p.Kind == ScopeFunction || p.Kind == ScopeComprehension {
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
			if child.Kind == ScopeFunction || child.Kind == ScopeComprehension {
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
		if p == name {
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
