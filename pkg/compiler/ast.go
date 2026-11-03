package compiler

// ExprID is a lightweight unboxed 32-bit handle referencing an ASTExpr inside ASTArena.
type ExprID uint32

// StmtID is a lightweight unboxed 32-bit handle referencing an ASTStmt inside ASTArena.
type StmtID uint32

const (
	ExprNone ExprID = 0
	StmtNone StmtID = 0
)

// ExprKind discriminates the variant of an AST expression.
type ExprKind uint8

const (
	ExprKindNone ExprKind = iota
	ExprKindInt
	ExprKindFloat
	ExprKindString
	ExprKindBool
	ExprKindNoneLit
	ExprKindName
	ExprKindUnary
	ExprKindBinary
	ExprKindCompare
	ExprKindCall
	ExprKindAttribute
	ExprKindSubscript
	ExprKindList
	ExprKindTuple
	ExprKindDict
	ExprKindListComp
	ExprKindDictComp
	ExprKindIfExp
	ExprKindYield
	ExprKindYieldFrom
	ExprKindSlice
)

// ASTExpr represents a flat, unboxed expression node stored in contiguous arena slices.
// Never heap-allocated individually or boxed behind Go interfaces.
type ASTExpr struct {
	Kind  ExprKind
	Op    uint8 // Operator or comparison code
	_     uint16
	Start uint32
	End   uint32
	Left  ExprID // Callee, target, lhs, operand
	Right ExprID // Value, rhs, arg/index
	Extra uint32 // Index into arena auxiliary tables
}

// StmtKind discriminates the variant of an AST statement.
type StmtKind uint8

const (
	StmtKindNone StmtKind = iota
	StmtKindExpr
	StmtKindAssign
	StmtKindAugAssign
	StmtKindReturn
	StmtKindIf
	StmtKindWhile
	StmtKindFor
	StmtKindBreak
	StmtKindContinue
	StmtKindPass
	StmtKindFuncDef
	StmtKindClassDef
	StmtKindGlobal
	StmtKindNonlocal
	StmtKindTry
	StmtKindRaise
	StmtKindWith
	StmtKindImport
	StmtKindImportFrom
)

// ASTStmt represents a flat, unboxed statement node stored in contiguous arena slices.
type ASTStmt struct {
	Kind  StmtKind
	Op    uint8 // Operator for AugAssign (+, -, etc.)
	_     uint16
	Start uint32
	End   uint32
	Expr1 ExprID // Expression, condition, return value, or target
	Expr2 ExprID // Assigned value, iterable, etc.
	Body  uint32 // Index into arena.StmtSlices
	Else  uint32 // Index into arena.StmtSlices
	Extra uint32 // Index into FuncDefs, ClassDefs, Elifs, or IdentSlices
}

// StmtSlice references a contiguous sequence of StmtIDs inside ASTArena.StmtIDs.
type StmtSlice struct {
	Start uint32
	Count uint32
}

// ExprSlice references a contiguous sequence of ExprIDs inside ASTArena.ExprIDs.
type ExprSlice struct {
	Start uint32
	Count uint32
}

// IdentSlice references a contiguous sequence of string identifiers inside ASTArena.Idents.
type IdentSlice struct {
	Start uint32
	Count uint32
}

// ASTCall stores detailed call argument metadata.
type ASTCall struct {
	Func    ExprID
	PosArgs ExprSlice
	KWNames IdentSlice
	KWArgs  ExprSlice
}

// ASTComp stores list/dict comprehension parameters.
type ASTComp struct {
	Target  ExprID
	Iter    ExprID
	IfConds ExprSlice
	IsDict  bool
}

// ASTDictPair stores a single {key: value} mapping in a dict literal.
type ASTDictPair struct {
	Key   ExprID
	Value ExprID
}

// ASTFuncDef stores function definition metadata.
type ASTFuncDef struct {
	Name       string
	ParamNames IdentSlice
	Defaults   ExprSlice
	Body       StmtSlice
}

// ASTClassDef stores class definition metadata.
type ASTClassDef struct {
	Name  string
	Bases ExprSlice
	Body  StmtSlice
}

// ASTElif stores an elif condition and body.
type ASTElif struct {
	Cond ExprID
	Body StmtSlice
}

// ASTExceptHandler stores an except clause.
type ASTExceptHandler struct {
	Type ExprID // Exception type expression, or ExprNone for bare except:
	Name string // as name, or empty string
	Body StmtSlice
}

// ExceptSlice references a contiguous sequence of ASTExceptHandler handles in ASTArena.ExceptHandlers.
type ExceptSlice struct {
	Start uint32
	Count uint32
}

// ASTWithItem stores a single item in a with statement.
type ASTWithItem struct {
	Expr   ExprID // Context manager expression
	Target ExprID // Optional target (or ExprNone)
}

// WithSlice references a contiguous sequence of ASTWithItems in ASTArena.WithItems.
type WithSlice struct {
	Start uint32
	Count uint32
}

// ASTTry stores try/except/else/finally statement metadata.
type ASTTry struct {
	Body     StmtSlice
	Handlers ExceptSlice
	Else     StmtSlice
	Finally  StmtSlice
}

// ASTAlias stores an imported name and its optional alias.
type ASTAlias struct {
	Name   string
	AsName string
}

// AliasSlice references a contiguous sequence of ASTAlias inside ASTArena.Aliases.
type AliasSlice struct {
	Start uint32
	Count uint32
}

// ASTImport stores metadata for an `import ...` statement.
type ASTImport struct {
	Aliases AliasSlice
}

// ASTImportFrom stores metadata for a `from ... import ...` statement.
type ASTImportFrom struct {
	Module  string
	Level   int
	Aliases AliasSlice
	IsStar  bool
}

// ASTArena provides flat, contiguous, typed storage for all AST nodes and auxiliary metadata.
// Enables zero per-node heap allocations and rapid GC-free reuse via Reset().
type ASTArena struct {
	Exprs          []ASTExpr
	Stmts          []ASTStmt
	ExprIDs        []ExprID
	StmtIDs        []StmtID
	StmtSlices     []StmtSlice
	ExprSlices     []ExprSlice
	IdentSlices    []IdentSlice
	Idents         []string
	IntConsts      []int64
	FloatConsts    []float64
	Calls          []ASTCall
	Comps          []ASTComp
	DictPairs      []ASTDictPair
	FuncDefs       []ASTFuncDef
	ClassDefs      []ASTClassDef
	Elifs          []ASTElif
	ExceptHandlers []ASTExceptHandler
	WithItems      []ASTWithItem
	TryStmts       []ASTTry
	Aliases        []ASTAlias
	AliasSlices    []AliasSlice
	Imports        []ASTImport
	ImportFroms    []ASTImportFrom
}

// NewASTArena constructs an ASTArena with pre-allocated capacity for zero-allocation parsing.
func NewASTArena(initialCap int) *ASTArena {
	if initialCap <= 0 {
		initialCap = 256
	}
	return &ASTArena{
		Exprs:          make([]ASTExpr, 0, initialCap*2),
		Stmts:          make([]ASTStmt, 0, initialCap),
		ExprIDs:        make([]ExprID, 0, initialCap*2),
		StmtIDs:        make([]StmtID, 0, initialCap*2),
		StmtSlices:     make([]StmtSlice, 0, initialCap/2),
		ExprSlices:     make([]ExprSlice, 0, initialCap/2),
		IdentSlices:    make([]IdentSlice, 0, initialCap/4),
		Idents:         make([]string, 0, initialCap),
		IntConsts:      make([]int64, 0, initialCap/2),
		FloatConsts:    make([]float64, 0, initialCap/4),
		Calls:          make([]ASTCall, 0, initialCap/4),
		Comps:          make([]ASTComp, 0, initialCap/8),
		DictPairs:      make([]ASTDictPair, 0, initialCap/2),
		FuncDefs:       make([]ASTFuncDef, 0, initialCap/8),
		ClassDefs:      make([]ASTClassDef, 0, initialCap/16),
		Elifs:          make([]ASTElif, 0, initialCap/8),
		ExceptHandlers: make([]ASTExceptHandler, 0, initialCap/8),
		WithItems:      make([]ASTWithItem, 0, initialCap/8),
		TryStmts:       make([]ASTTry, 0, initialCap/8),
		Aliases:        make([]ASTAlias, 0, initialCap/4),
		AliasSlices:    make([]AliasSlice, 0, initialCap/8),
		Imports:        make([]ASTImport, 0, initialCap/8),
		ImportFroms:    make([]ASTImportFrom, 0, initialCap/8),
	}
}

// Reset reslices all contiguous slices to zero length while retaining underlying allocated buffers.
func (a *ASTArena) Reset() {
	a.Exprs = a.Exprs[:0]
	a.Stmts = a.Stmts[:0]
	a.ExprIDs = a.ExprIDs[:0]
	a.StmtIDs = a.StmtIDs[:0]
	a.StmtSlices = a.StmtSlices[:0]
	a.ExprSlices = a.ExprSlices[:0]
	a.IdentSlices = a.IdentSlices[:0]
	a.Idents = a.Idents[:0]
	a.IntConsts = a.IntConsts[:0]
	a.FloatConsts = a.FloatConsts[:0]
	a.Calls = a.Calls[:0]
	a.Comps = a.Comps[:0]
	a.DictPairs = a.DictPairs[:0]
	a.FuncDefs = a.FuncDefs[:0]
	a.ClassDefs = a.ClassDefs[:0]
	a.Elifs = a.Elifs[:0]
	a.ExceptHandlers = a.ExceptHandlers[:0]
	a.WithItems = a.WithItems[:0]
	a.TryStmts = a.TryStmts[:0]
	a.Aliases = a.Aliases[:0]
	a.AliasSlices = a.AliasSlices[:0]
	a.Imports = a.Imports[:0]
	a.ImportFroms = a.ImportFroms[:0]
}

// AllocExpr appends an ASTExpr and returns its 1-based ExprID handle.
func (a *ASTArena) AllocExpr(expr ASTExpr) ExprID {
	id := ExprID(len(a.Exprs) + 1)
	a.Exprs = append(a.Exprs, expr)
	return id
}

// GetExpr retrieves a pointer to the ASTExpr for id.
func (a *ASTArena) GetExpr(id ExprID) *ASTExpr {
	if id == 0 || int(id) > len(a.Exprs) {
		return nil
	}
	return &a.Exprs[id-1]
}

// AllocStmt appends an ASTStmt and returns its 1-based StmtID handle.
func (a *ASTArena) AllocStmt(stmt ASTStmt) StmtID {
	id := StmtID(len(a.Stmts) + 1)
	a.Stmts = append(a.Stmts, stmt)
	return id
}

// GetStmt retrieves a pointer to the ASTStmt for id.
func (a *ASTArena) GetStmt(id StmtID) *ASTStmt {
	if id == 0 || int(id) > len(a.Stmts) {
		return nil
	}
	return &a.Stmts[id-1]
}

// AllocInt appends an int64 constant and returns its index.
func (a *ASTArena) AllocInt(v int64) uint32 {
	idx := uint32(len(a.IntConsts))
	a.IntConsts = append(a.IntConsts, v)
	return idx
}

// AllocFloat appends a float64 constant and returns its index.
func (a *ASTArena) AllocFloat(v float64) uint32 {
	idx := uint32(len(a.FloatConsts))
	a.FloatConsts = append(a.FloatConsts, v)
	return idx
}

// AddStmtSlice records a slice of StmtIDs into the arena and returns the slice descriptor index.
func (a *ASTArena) AddStmtSlice(ids []StmtID) uint32 {
	start := uint32(len(a.StmtIDs))
	a.StmtIDs = append(a.StmtIDs, ids...)
	sliceIdx := uint32(len(a.StmtSlices))
	a.StmtSlices = append(a.StmtSlices, StmtSlice{
		Start: start,
		Count: uint32(len(ids)),
	})
	return sliceIdx
}

// GetStmtSlice retrieves the contiguous StmtIDs for a slice descriptor index.
func (a *ASTArena) GetStmtSlice(sliceIdx uint32) []StmtID {
	if int(sliceIdx) >= len(a.StmtSlices) {
		return nil
	}
	s := a.StmtSlices[sliceIdx]
	return a.StmtIDs[s.Start : s.Start+s.Count]
}

// AddExprSlice records a slice of ExprIDs into the arena and returns the ExprSlice descriptor.
func (a *ASTArena) AddExprSlice(ids []ExprID) ExprSlice {
	start := uint32(len(a.ExprIDs))
	a.ExprIDs = append(a.ExprIDs, ids...)
	s := ExprSlice{
		Start: start,
		Count: uint32(len(ids)),
	}
	a.ExprSlices = append(a.ExprSlices, s)
	return s
}

// GetExprSlice retrieves the contiguous ExprIDs for an ExprSlice descriptor.
func (a *ASTArena) GetExprSlice(s ExprSlice) []ExprID {
	return a.ExprIDs[s.Start : s.Start+s.Count]
}

// AddIdentSlice records a slice of identifier strings and returns an IdentSlice descriptor.
func (a *ASTArena) AddIdentSlice(names []string) IdentSlice {
	start := uint32(len(a.Idents))
	a.Idents = append(a.Idents, names...)
	s := IdentSlice{
		Start: start,
		Count: uint32(len(names)),
	}
	a.IdentSlices = append(a.IdentSlices, s)
	return s
}

// GetIdentSlice retrieves the contiguous identifier strings for an IdentSlice descriptor.
func (a *ASTArena) GetIdentSlice(s IdentSlice) []string {
	return a.Idents[s.Start : s.Start+s.Count]
}

// AllocTry records an ASTTry and returns its index.
func (a *ASTArena) AllocTry(t ASTTry) uint32 {
	idx := uint32(len(a.TryStmts))
	a.TryStmts = append(a.TryStmts, t)
	return idx
}

// AllocExceptHandler records an ASTExceptHandler and returns its index.
func (a *ASTArena) AllocExceptHandler(h ASTExceptHandler) uint32 {
	idx := uint32(len(a.ExceptHandlers))
	a.ExceptHandlers = append(a.ExceptHandlers, h)
	return idx
}

// AddExceptSlice records a contiguous sequence of ASTExceptHandlers and returns an ExceptSlice descriptor.
func (a *ASTArena) AddExceptSlice(handlers []ASTExceptHandler) ExceptSlice {
	start := uint32(len(a.ExceptHandlers))
	a.ExceptHandlers = append(a.ExceptHandlers, handlers...)
	return ExceptSlice{
		Start: start,
		Count: uint32(len(handlers)),
	}
}

// GetExceptSlice retrieves the contiguous ASTExceptHandlers for an ExceptSlice descriptor.
func (a *ASTArena) GetExceptSlice(s ExceptSlice) []ASTExceptHandler {
	return a.ExceptHandlers[s.Start : s.Start+s.Count]
}

// AllocWithItem records an ASTWithItem and returns its index.
func (a *ASTArena) AllocWithItem(item ASTWithItem) uint32 {
	idx := uint32(len(a.WithItems))
	a.WithItems = append(a.WithItems, item)
	return idx
}

// AddWithSlice records a contiguous sequence of ASTWithItems and returns a WithSlice descriptor.
func (a *ASTArena) AddWithSlice(items []ASTWithItem) WithSlice {
	start := uint32(len(a.WithItems))
	a.WithItems = append(a.WithItems, items...)
	return WithSlice{
		Start: start,
		Count: uint32(len(items)),
	}
}

// GetWithSlice retrieves the contiguous ASTWithItems for a WithSlice descriptor.
func (a *ASTArena) GetWithSlice(s WithSlice) []ASTWithItem {
	return a.WithItems[s.Start : s.Start+s.Count]
}

// AddAliasSlice records a contiguous sequence of ASTAlias and returns an AliasSlice descriptor.
func (a *ASTArena) AddAliasSlice(aliases []ASTAlias) AliasSlice {
	start := uint32(len(a.Aliases))
	a.Aliases = append(a.Aliases, aliases...)
	return AliasSlice{
		Start: start,
		Count: uint32(len(aliases)),
	}
}

// GetAliasSlice retrieves the contiguous ASTAliases for an AliasSlice descriptor.
func (a *ASTArena) GetAliasSlice(s AliasSlice) []ASTAlias {
	return a.Aliases[s.Start : s.Start+s.Count]
}

// AllocImport records an ASTImport and returns its index.
func (a *ASTArena) AllocImport(imp ASTImport) uint32 {
	idx := uint32(len(a.Imports))
	a.Imports = append(a.Imports, imp)
	return idx
}

// GetImport retrieves an ASTImport by index.
func (a *ASTArena) GetImport(idx uint32) *ASTImport {
	if int(idx) >= len(a.Imports) {
		return nil
	}
	return &a.Imports[idx]
}

// AllocImportFrom records an ASTImportFrom and returns its index.
func (a *ASTArena) AllocImportFrom(imp ASTImportFrom) uint32 {
	idx := uint32(len(a.ImportFroms))
	a.ImportFroms = append(a.ImportFroms, imp)
	return idx
}

// GetImportFrom retrieves an ASTImportFrom by index.
func (a *ASTArena) GetImportFrom(idx uint32) *ASTImportFrom {
	if int(idx) >= len(a.ImportFroms) {
		return nil
	}
	return &a.ImportFroms[idx]
}
