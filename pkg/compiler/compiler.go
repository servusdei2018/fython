package compiler

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

var defaultOptimizeLevel atomic.Int32

// SetDefaultOptimizeLevel sets the process-wide compiler optimization level (0, 1 for -O, 2 for -OO).
func SetDefaultOptimizeLevel(level int) {
	if level < 0 {
		level = 0
	}
	defaultOptimizeLevel.Store(int32(level))
}

// DefaultOptimizeLevel returns the active default compiler optimization level.
func DefaultOptimizeLevel() int {
	return int(defaultOptimizeLevel.Load())
}

func init() {
	vm.SetDefaultCompileHook(CompileSource)
	vm.SetDefaultCompileWithFilenameHook(CompileSourceWithFilename)
	vm.DefaultEngine().SetCompileHook(CompileSource)
	vm.DefaultEngine().SetCompileWithFilenameHook(CompileSourceWithFilename)
	hook := func(src []byte, filename string, mode int) (*vm.CodeObject, error) {
		return CompileSourceMode(src, filename, CompileMode(mode))
	}
	optHook := func(src []byte, filename string, mode, optimize int) (*vm.CodeObject, error) {
		return CompileSourceModeWithOptimize(src, filename, CompileMode(mode), optimize)
	}
	vm.SetDynamicCompileHook(hook)
	vm.SetDynamicCompileOptimizeHook(optHook)
	vm.DefaultEngine().SetDynamicCompileHook(hook)
	vm.DefaultEngine().SetDynamicCompileOptimizeHook(optHook)
}

type CleanupKind int

const (
	CleanupWith CleanupKind = iota
	CleanupFinally
	CleanupExcept
)

type CleanupBlock struct {
	Kind     CleanupKind
	Parent   *CleanupBlock
	TempSlot int
	Finally  StmtSlice
}

// LoopContext tracks jump patch locations for break and continue statements.
type LoopContext struct {
	LoopStart int
	Breaks    []int
	Continues []int
	Parent    *LoopContext
	Cleanup   *CleanupBlock
	IsFor     bool
}

// Compiler compiles an ASTArena + SymbolTable into 32-bit VM bytecode.
type Compiler struct {
	src            []byte
	arena          *ASTArena
	scope          *Scope
	constPool      *ConstPool
	code           []uint32
	exceptionTable []vm.ExceptionEntry
	stackDepth     int
	maxStackDepth  int
	activeLoop     *LoopContext
	activeCleanup  *CleanupBlock
	err            error
	nextChildIdx   int
	lineStarts     []int
	lineSpans      []vm.LineSpanEntry
	curLine        uint32
	curCol         uint32
	filename       string
	qualnamePrefix string
	isExec         bool
	optimize       int
}

func buildLineStarts(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func (c *Compiler) offsetToLineCol(pos uint32) (uint32, uint32) {
	if len(c.lineStarts) == 0 {
		c.lineStarts = buildLineStarts(c.src)
	}
	ipos := int(pos)
	if ipos > len(c.src) {
		ipos = len(c.src)
	}
	idx := sort.Search(len(c.lineStarts), func(i int) bool {
		return c.lineStarts[i] > ipos
	}) - 1
	if idx < 0 {
		idx = 0
	}
	line := uint32(idx + 1)
	col := uint32(ipos - c.lineStarts[idx] + 1)
	return line, col
}

func (c *Compiler) setSourcePos(pos uint32) {
	if int(pos) <= len(c.src) {
		c.curLine, c.curCol = c.offsetToLineCol(pos)
	}
}

func (c *Compiler) nextChildScope(kind ScopeKind, name string) *Scope {
	for i := c.nextChildIdx; i < len(c.scope.Children); i++ {
		child := c.scope.Children[i]
		if child.Kind == kind && (name == "" || child.Name == name) {
			c.nextChildIdx = i + 1
			return child
		}
	}
	for _, child := range c.scope.Children {
		if child.Kind == kind && (name == "" || child.Name == name) {
			return child
		}
	}
	return nil
}

func (c *Compiler) addExceptionRange(startIP, endIP, targetIP, targetDepth int, pushLasti bool) {
	c.exceptionTable = append(c.exceptionTable, vm.ExceptionEntry{
		StartIP:          startIP,
		EndIP:            endIP,
		TargetIP:         targetIP,
		TargetStackDepth: targetDepth,
		PushLasti:        pushLasti,
	})
}

// Compile compiles Python source bytes into an executable *vm.CodeObject using the provided arena.
func Compile(src []byte, arena *ASTArena) (*vm.CodeObject, error) {
	return CompileWithFilename(src, arena, "<string>")
}

// CompileWithFilename compiles Python source bytes into an executable *vm.CodeObject with source filename.
func CompileWithFilename(src []byte, arena *ASTArena, filename string) (*vm.CodeObject, error) {
	return CompileWithFilenameAndOptimize(src, arena, filename, DefaultOptimizeLevel())
}

// CompileWithFilenameAndOptimize compiles Python source bytes with source filename and explicit optimization level.
func CompileWithFilenameAndOptimize(src []byte, arena *ASTArena, filename string, optimize int) (*vm.CodeObject, error) {
	if optimize < 0 {
		optimize = DefaultOptimizeLevel()
	}
	parser := NewParser(src, arena)
	topStmts, err := parser.ParseModule()
	if err != nil {
		return nil, err
	}

	topScope, err := BuildSymbolTable(src, arena, topStmts)
	if err != nil {
		return nil, err
	}

	if filename == "" {
		filename = "<string>"
	}

	c := &Compiler{
		src:            src,
		arena:          arena,
		scope:          topScope,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 64),
		filename:       filename,
		qualnamePrefix: "",
		curLine:        1,
		curCol:         1,
		optimize:       optimize,
	}

	if topScope.HasAnnotations {
		if err := c.emitModuleAnnotatePrologue(); err != nil {
			return nil, err
		}
	}

	modDoc := val.NoneVal()
	if docStr, hasDoc := c.extractDocstringFromSlice(topStmts); hasDoc {
		docVal := val.NewStringVal(docStr)
		modDoc = docVal
		docIdx := c.constPool.Add(docVal)
		c.emitOp(vm.OP_LOAD_CONST, 0, docIdx)
		if err := c.compileNameStore("__doc__"); err != nil {
			return nil, err
		}
	}

	for _, stmtID := range topStmts {
		if err := c.compileStmt(stmtID); err != nil {
			return nil, err
		}
	}

	// Implicit module return None
	c.emitReturnNone()

	c.peephole()

	varNames := make([]val.Value, len(c.scope.FastLocals))
	for i, fl := range c.scope.FastLocals {
		varNames[i] = val.InternString(fl)
	}

	co := &vm.CodeObject{
		Name:           "<module>",
		QualName:       "<module>",
		FileName:       filename,
		FirstLineNo:    1,
		Code:           c.code,
		Consts:         c.constPool.Slice(),
		VarNames:       varNames,
		NumLocals:      frameLocalCount(c.scope),
		NumParams:      0,
		MaxStackSize:   c.maxStackDepth + 16,
		Doc:            modDoc,
		ExceptionTable: c.exceptionTable,
		LineSpans:      c.lineSpans,
	}
	return co.Init(), nil
}

// CompileSource compiles Python source bytes using a fresh ASTArena.
func CompileSource(src []byte) (*vm.CodeObject, error) {
	arena := NewASTArena(256)
	return Compile(src, arena)
}

// CompileSourceWithFilename compiles Python source bytes with filename using a fresh ASTArena.
func CompileSourceWithFilename(src []byte, filename string) (*vm.CodeObject, error) {
	arena := NewASTArena(256)
	return CompileWithFilename(src, arena, filename)
}

// CompileString compiles a Python source string.
func CompileString(src string) (*vm.CodeObject, error) {
	return CompileSource([]byte(src))
}

// CompileMode represents the compilation mode for code objects.
type CompileMode int

const (
	ModeExec CompileMode = iota
	ModeEval
	ModeSingle
)

// CompileSourceMode compiles Python source bytes with the specified mode.
func CompileSourceMode(src []byte, filename string, mode CompileMode) (*vm.CodeObject, error) {
	return CompileSourceModeWithOptimize(src, filename, mode, DefaultOptimizeLevel())
}

// CompileSourceModeWithOptimize compiles Python source bytes with the specified mode and optimization level.
func CompileSourceModeWithOptimize(src []byte, filename string, mode CompileMode, optimize int) (*vm.CodeObject, error) {
	if optimize < 0 {
		optimize = DefaultOptimizeLevel()
	}
	arena := NewASTArena(256)
	if filename == "" {
		filename = "<string>"
	}
	if mode == ModeEval {
		parser := NewParser(src, arena)
		exprID, err := parser.parseExpr()
		if err != nil {
			return nil, err
		}
		topScope, err := BuildSymbolTableForExpr(src, arena, exprID)
		if err != nil {
			return nil, err
		}
		c := &Compiler{
			src:            src,
			arena:          arena,
			scope:          topScope,
			constPool:      NewConstPool(),
			code:           make([]uint32, 0, 64),
			filename:       filename,
			qualnamePrefix: "",
			isExec:         true,
			curLine:        1,
			curCol:         1,
			optimize:       optimize,
		}
		if err := c.compileExpr(exprID); err != nil {
			return nil, err
		}
		c.emitOp(vm.OP_RETURN, 0, 0)
		c.peephole()
		varNames := make([]val.Value, len(c.scope.FastLocals))
		for i, fl := range c.scope.FastLocals {
			varNames[i] = val.InternString(fl)
		}
		co := &vm.CodeObject{
			Name:           "<module>",
			QualName:       "<module>",
			FileName:       filename,
			FirstLineNo:    1,
			Code:           c.code,
			Consts:         c.constPool.Slice(),
			VarNames:       varNames,
			NumLocals:      frameLocalCount(c.scope),
			NumParams:      0,
			MaxStackSize:   c.maxStackDepth + 16,
			ExceptionTable: c.exceptionTable,
			LineSpans:      c.lineSpans,
		}
		return co.Init(), nil
	}

	parser := NewParser(src, arena)
	topStmts, err := parser.ParseModule()
	if err != nil {
		return nil, err
	}
	topScope, err := BuildSymbolTable(src, arena, topStmts)
	if err != nil {
		return nil, err
	}
	c := &Compiler{
		src:            src,
		arena:          arena,
		scope:          topScope,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 64),
		filename:       filename,
		qualnamePrefix: "",
		isExec:         true,
		curLine:        1,
		curCol:         1,
		optimize:       optimize,
	}
	if topScope.HasAnnotations {
		if err := c.emitModuleAnnotatePrologue(); err != nil {
			return nil, err
		}
	}
	modDoc := val.NoneVal()
	if docStr, hasDoc := c.extractDocstringFromSlice(topStmts); hasDoc {
		docVal := val.NewStringVal(docStr)
		modDoc = docVal
		docIdx := c.constPool.Add(docVal)
		c.emitOp(vm.OP_LOAD_CONST, 0, docIdx)
		if err := c.compileNameStore("__doc__"); err != nil {
			return nil, err
		}
	}
	for _, stmtID := range topStmts {
		if err := c.compileStmt(stmtID); err != nil {
			return nil, err
		}
	}
	c.emitReturnNone()
	c.peephole()
	varNames := make([]val.Value, len(c.scope.FastLocals))
	for i, fl := range c.scope.FastLocals {
		varNames[i] = val.InternString(fl)
	}
	co := &vm.CodeObject{
		Name:           "<module>",
		QualName:       "<module>",
		FileName:       filename,
		FirstLineNo:    1,
		Code:           c.code,
		Consts:         c.constPool.Slice(),
		VarNames:       varNames,
		NumLocals:      frameLocalCount(c.scope),
		NumParams:      0,
		MaxStackSize:   c.maxStackDepth + 16,
		Doc:            modDoc,
		ExceptionTable: c.exceptionTable,
		LineSpans:      c.lineSpans,
	}
	return co.Init(), nil
}

func (c *Compiler) extractDocstringFromSlice(stmts []StmtID) (string, bool) {
	if c.optimize >= 2 || len(stmts) == 0 {
		return "", false
	}
	firstStmt := c.arena.GetStmt(stmts[0])
	if firstStmt == nil || firstStmt.Kind != StmtKindExpr || firstStmt.Expr1 == ExprNone {
		return "", false
	}
	expr := c.arena.GetExpr(firstStmt.Expr1)
	if expr == nil || expr.Kind != ExprKindString {
		return "", false
	}
	rawStr := string(c.src[expr.Start:expr.End])
	lowerPrefix := strings.ToLower(rawStr)
	if strings.HasPrefix(lowerPrefix, "b") || strings.HasPrefix(lowerPrefix, "rb") || strings.HasPrefix(lowerPrefix, "br") {
		return "", false
	}
	return parseStringLiteral(rawStr), true
}

func (c *Compiler) emit(inst uint32) int {
	idx := len(c.code)
	c.code = append(c.code, inst)
	line := c.curLine
	col := c.curCol
	if line == 0 {
		line = 1
	}
	if col == 0 {
		col = 1
	}
	n := len(c.lineSpans)
	if n > 0 && c.lineSpans[n-1].Line == line && c.lineSpans[n-1].Col == col && c.lineSpans[n-1].EndIP == uint32(idx) {
		c.lineSpans[n-1].EndIP = uint32(idx + 1)
	} else {
		c.lineSpans = append(c.lineSpans, vm.LineSpanEntry{
			StartIP: uint32(idx),
			EndIP:   uint32(idx + 1),
			Line:    line,
			Col:     col,
		})
	}
	return idx
}

func (c *Compiler) emitOp(op vm.Opcode, argA uint8, argBx uint16) int {
	return c.emit(vm.Encode(op, argA, argBx))
}

func (c *Compiler) patchJump(jumpIdx int, target int) {
	inst := c.code[jumpIdx]
	op := vm.DecodeOpcode(inst)
	argA := vm.DecodeArgA(inst)
	c.code[jumpIdx] = vm.Encode(op, argA, uint16(target))
}

func (c *Compiler) emitReturnNone() {
	noneIdx := c.constPool.Add(val.NoneVal())
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_RETURN, 0, 0)
}

func (c *Compiler) runCleanupsTo(target *CleanupBlock) error {
	for cl := c.activeCleanup; cl != target && cl != nil; cl = cl.Parent {
		if cl.Kind == CleanupWith {
			_, exitIdx := c.constPool.InternName("__exit__")
			noneIdx := c.constPool.Add(val.NoneVal())
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(cl.TempSlot))
			c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
			c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
			c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
			c.emitOp(vm.OP_CALL_METHOD_PIC, 3, exitIdx)
			c.emitOp(vm.OP_POP_TOP, 0, 0)
		} else if cl.Kind == CleanupFinally {
			savedCleanup := c.activeCleanup
			c.activeCleanup = cl.Parent
			for _, sID := range c.arena.StmtIDs[cl.Finally.Start : cl.Finally.Start+cl.Finally.Count] {
				if err := c.compileStmt(sID); err != nil {
					c.activeCleanup = savedCleanup
					return err
				}
			}
			c.activeCleanup = savedCleanup
		} else if cl.Kind == CleanupExcept {
			c.emitOp(vm.OP_POP_EXCEPT, 0, 0)
		}
	}
	return nil
}

func (c *Compiler) compileStmt(stmtID StmtID) error {
	stmt := c.arena.GetStmt(stmtID)
	if stmt == nil {
		return nil
	}
	c.setSourcePos(stmt.Start)

	switch stmt.Kind {
	case StmtKindExpr:
		if err := c.compileExpr(stmt.Expr1); err != nil {
			return err
		}
		c.emitOp(vm.OP_POP_TOP, 0, 0)

	case StmtKindReturn:
		var retSlot int = -1
		if stmt.Expr1 != ExprNone {
			if err := c.compileExpr(stmt.Expr1); err != nil {
				return err
			}
			if c.activeCleanup != nil {
				retSlot = c.allocTempLocal()
				c.emitOp(vm.OP_STORE_FAST, 0, uint16(retSlot))
			}
		} else {
			if c.activeCleanup != nil {
				noneIdx := c.constPool.Add(val.NoneVal())
				c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
				retSlot = c.allocTempLocal()
				c.emitOp(vm.OP_STORE_FAST, 0, uint16(retSlot))
			}
		}

		if err := c.runCleanupsTo(nil); err != nil {
			return err
		}

		if retSlot >= 0 {
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(retSlot))
		} else if stmt.Expr1 == ExprNone {
			noneIdx := c.constPool.Add(val.NoneVal())
			c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
		}
		c.emitOp(vm.OP_RETURN, 0, 0)

	case StmtKindAssign:
		count := stmt.Extra >> 16
		if count > 1 {
			if err := c.compileExpr(stmt.Expr2); err != nil {
				return err
			}
			start := stmt.Extra & 0xFFFF
			elemIDs := c.arena.ExprIDs[start : start+count]
			for i, targetID := range elemIDs {
				if i < len(elemIDs)-1 {
					c.emitOp(vm.OP_DUP_TOP, 0, 0)
				}
				if err := c.compileStoreTarget(targetID); err != nil {
					return err
				}
			}
			return nil
		}
		if err := c.compileExpr(stmt.Expr2); err != nil {
			return err
		}
		return c.compileStoreTarget(stmt.Expr1)

	case StmtKindAnnAssign:
		if stmt.Expr2 != ExprNone {
			if err := c.compileExpr(stmt.Expr2); err != nil {
				return err
			}
			if err := c.compileStoreTarget(stmt.Expr1); err != nil {
				return err
			}
		}
		// PEP 649: the annotation itself is evaluated lazily by the
		// synthesized __annotate__. Annotations nested in control flow only
		// record that they were reached.
		if entry := c.scope.AnnByStmt[stmtID]; entry != nil && entry.Cond {
			if err := c.compileNameLoad(annCondAnnotations); err != nil {
				return err
			}
			c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.Int64Val(int64(entry.Idx))))
			c.emitOp(vm.OP_SET_ADD, 0, 1)
			c.emitOp(vm.OP_POP_TOP, 0, 0)
		}
		return nil

	case StmtKindMatch:
		return c.compileMatchStmt(stmt)

	case StmtKindTypeAlias:
		return c.compileTypeAlias(stmt)

	case StmtKindAssert:
		return c.compileAssertStmt(stmt)

	case StmtKindDelete:
		return c.compileDeleteStmt(stmt)

	case StmtKindAugAssign:
		// Target load
		if err := c.compileTargetLoad(stmt.Expr1); err != nil {
			return err
		}
		// Expr2
		if err := c.compileExpr(stmt.Expr2); err != nil {
			return err
		}
		// Binary op
		c.emitAugAssignOp(TokenType(stmt.Op))
		// Target store
		return c.compileStoreTarget(stmt.Expr1)

	case StmtKindIf:
		return c.compileIfStmt(stmt)

	case StmtKindWhile:
		return c.compileWhileStmt(stmt)

	case StmtKindFor:
		return c.compileForStmt(stmt)

	case StmtKindTry:
		return c.compileTryStmt(stmt)

	case StmtKindRaise:
		return c.compileRaiseStmt(stmt)

	case StmtKindWith:
		return c.compileWithStmt(stmt)

	case StmtKindBreak:
		if c.activeLoop == nil {
			return fmt.Errorf("SyntaxError: 'break' outside loop")
		}
		if err := c.runCleanupsTo(c.activeLoop.Cleanup); err != nil {
			return err
		}
		if c.activeLoop.IsFor {
			c.emitOp(vm.OP_POP_TOP, 0, 0)
		}
		jumpIdx := c.emitOp(vm.OP_JUMP, 0, 0)
		c.activeLoop.Breaks = append(c.activeLoop.Breaks, jumpIdx)

	case StmtKindContinue:
		if c.activeLoop == nil {
			return fmt.Errorf("SyntaxError: 'continue' outside loop")
		}
		if err := c.runCleanupsTo(c.activeLoop.Cleanup); err != nil {
			return err
		}
		jumpIdx := c.emitOp(vm.OP_JUMP, 0, 0)
		c.activeLoop.Continues = append(c.activeLoop.Continues, jumpIdx)

	case StmtKindPass:
		// No-op

	case StmtKindFuncDef, StmtKindAsyncFuncDef:
		return c.compileFuncDef(stmt)

	case StmtKindAsyncFor:
		return c.compileAsyncForStmt(stmt)

	case StmtKindAsyncWith:
		return c.compileAsyncWithStmt(stmt)

	case StmtKindClassDef:
		return c.compileClassDef(stmt)

	case StmtKindGlobal, StmtKindNonlocal:
		// Handled in symbol table pass

	case StmtKindImport:
		imp := c.arena.GetImport(stmt.Extra)
		if imp != nil {
			if imp.IsLazy {
				for _, alias := range c.arena.GetAliasSlice(imp.Aliases) {
					if err := c.compileNameLoad("__build_lazy_import__"); err != nil {
						return err
					}
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.NewStringVal(alias.Name)))
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.NewStringVal("")))
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.Int64Val(0)))
					c.emitOp(vm.OP_CALL, 3, 0)
					targetName := alias.AsName
					if targetName == "" {
						targetName = strings.Split(alias.Name, ".")[0]
					}
					if err := c.compileNameStore(targetName); err != nil {
						return err
					}
				}
				break
			}
			for _, alias := range c.arena.GetAliasSlice(imp.Aliases) {
				_, constIdx := c.constPool.InternName(alias.Name)
				flagLeaf := uint8(0)
				if alias.AsName != "" {
					flagLeaf = 0x80
				}
				c.emitOp(vm.OP_IMPORT_NAME, flagLeaf, constIdx)
				targetName := alias.AsName
				if targetName == "" {
					targetName = strings.Split(alias.Name, ".")[0]
				}
				if err := c.compileNameStore(targetName); err != nil {
					return err
				}
			}
		}

	case StmtKindImportFrom:
		impFrom := c.arena.GetImportFrom(stmt.Extra)
		if impFrom != nil {
			if impFrom.IsLazy {
				for _, alias := range c.arena.GetAliasSlice(impFrom.Aliases) {
					if err := c.compileNameLoad("__build_lazy_import__"); err != nil {
						return err
					}
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.NewStringVal(impFrom.Module)))
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.NewStringVal(alias.Name)))
					c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.Int64Val(int64(impFrom.Level))))
					c.emitOp(vm.OP_CALL, 3, 0)
					targetName := alias.Name
					if alias.AsName != "" {
						targetName = alias.AsName
					}
					if err := c.compileNameStore(targetName); err != nil {
						return err
					}
				}
				break
			}
			_, constIdx := c.constPool.InternName(impFrom.Module)
			c.emitOp(vm.OP_IMPORT_NAME, 0x80|uint8(impFrom.Level&0x7F), constIdx)
			if impFrom.IsStar {
				c.emitOp(vm.OP_IMPORT_STAR, 0, 0)
			} else {
				for _, alias := range c.arena.GetAliasSlice(impFrom.Aliases) {
					_, symIdx := c.constPool.InternName(alias.Name)
					c.emitOp(vm.OP_IMPORT_FROM, 0, symIdx)
					targetName := alias.Name
					if alias.AsName != "" {
						targetName = alias.AsName
					}
					if err := c.compileNameStore(targetName); err != nil {
						return err
					}
				}
				c.emitOp(vm.OP_POP_TOP, 0, 0)
			}
		}
	}

	return nil
}

func (c *Compiler) emitAugAssignOp(t TokenType) {
	switch t {
	case TOKEN_PLUSEQ:
		c.emitOp(vm.OP_BINARY_ADD, 0, 0)
	case TOKEN_MINUSEQ:
		c.emitOp(vm.OP_BINARY_SUB, 0, 0)
	case TOKEN_STAREQ:
		c.emitOp(vm.OP_BINARY_MUL, 0, 0)
	case TOKEN_SLASHEQ:
		c.emitOp(vm.OP_BINARY_TRUE_DIV, 0, 0)
	case TOKEN_SLASHSLASHEQ:
		c.emitOp(vm.OP_BINARY_FLOOR_DIV, 0, 0)
	case TOKEN_PERCENTEQ:
		c.emitOp(vm.OP_BINARY_MOD, 0, 0)
	case TOKEN_STARSTAREQ:
		c.emitOp(vm.OP_BINARY_POW, 0, 0)
	case TOKEN_AMPEQ:
		c.emitOp(vm.OP_BINARY_AND, 0, 0)
	case TOKEN_PIPEEQ:
		c.emitOp(vm.OP_BINARY_OR, 0, 0)
	case TOKEN_CARETEQ:
		c.emitOp(vm.OP_BINARY_XOR, 0, 0)
	case TOKEN_LSHIFTEQ:
		c.emitOp(vm.OP_BINARY_LSHIFT, 0, 0)
	case TOKEN_RSHIFTEQ:
		c.emitOp(vm.OP_BINARY_RSHIFT, 0, 0)
	case TOKEN_ATEQ:
		c.emitOp(vm.OP_BINARY_MATMUL, 0, 0)
	}
}

func (c *Compiler) compileTargetLoad(targetID ExprID) error {
	expr := c.arena.GetExpr(targetID)
	if expr == nil {
		return nil
	}
	switch expr.Kind {
	case ExprKindName:
		name := string(c.src[expr.Start:expr.End])
		return c.compileNameLoad(name)
	case ExprKindAttribute:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		attrName := string(c.src[expr.Start:expr.End])
		_, constIdx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_LOAD_ATTR, 0, constIdx)
	case ExprKindSubscript:
		rightExpr := c.arena.GetExpr(expr.Right)
		if rightExpr != nil && rightExpr.Kind == ExprKindSlice && rightExpr.Extra == 0 {
			// Zero-allocation binary slice: container[start:stop]
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			if rightExpr.Left != ExprNone {
				if err := c.compileExpr(rightExpr.Left); err != nil {
					return err
				}
			} else {
				idx := c.constPool.Add(val.NoneVal())
				c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			}
			if rightExpr.Right != ExprNone {
				if err := c.compileExpr(rightExpr.Right); err != nil {
					return err
				}
			} else {
				idx := c.constPool.Add(val.NoneVal())
				c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			}
			c.emitOp(vm.OP_BINARY_SLICE, 0, 0)
			return nil
		}
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
	}
	return nil
}

func (c *Compiler) compileStoreTarget(targetID ExprID) error {
	expr := c.arena.GetExpr(targetID)
	if expr == nil {
		return nil
	}
	switch expr.Kind {
	case ExprKindName:
		name := string(c.src[expr.Start:expr.End])
		return c.compileNameStore(name)

	case ExprKindStarred:
		return c.compileStoreTarget(expr.Left)

	case ExprKindTuple, ExprKindList:
		slice := c.arena.ExprSlices[expr.Extra]
		count := slice.Count
		elemIDs := c.arena.ExprIDs[slice.Start : slice.Start+count]

		starIdx := -1
		for i, id := range elemIDs {
			subExpr := c.arena.GetExpr(id)
			if subExpr != nil && subExpr.Kind == ExprKindStarred {
				if starIdx >= 0 {
					return fmt.Errorf("SyntaxError: multiple starred expressions in assignment")
				}
				starIdx = i
			}
		}

		if starIdx >= 0 {
			beforeCount := uint8(starIdx)
			afterCount := uint16(len(elemIDs) - 1 - starIdx)
			c.emitOp(vm.OP_UNPACK_EX, beforeCount, afterCount)
		} else {
			c.emitOp(vm.OP_UNPACK_SEQUENCE, 0, uint16(count))
		}

		for _, elemID := range elemIDs {
			if err := c.compileStoreTarget(elemID); err != nil {
				return err
			}
		}

	case ExprKindAttribute:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		attrName := string(c.src[expr.Start:expr.End])
		_, constIdx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_STORE_ATTR, 0, constIdx)

	case ExprKindSubscript:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_STORE_SUBSCR, 0, 0)
	}
	return nil
}

// closureSlot is the cell index of name in the current scope.
// Class scopes consult ClosureSlots first so a namespace binding and a
// free-var passthrough of the same name stay distinct.
func (c *Compiler) closureSlot(name string) (uint16, error) {
	if c.scope == nil {
		return 0, fmt.Errorf("compile error: missing closure cell for '%s'", name)
	}
	if c.scope.ClosureSlots != nil {
		if off, ok := c.scope.ClosureSlots[name]; ok {
			return uint16(off), nil
		}
	}
	sym := c.scope.Symbols[name]
	if sym != nil && (sym.Storage == StorageCell || sym.Storage == StorageFree) {
		return uint16(sym.Offset), nil
	}
	return 0, fmt.Errorf("compile error: missing closure cell for '%s'", name)
}

func (c *Compiler) emitLoadClosure(name string) error {
	off, err := c.closureSlot(name)
	if err != nil {
		return err
	}
	c.emitOp(vm.OP_LOAD_CLOSURE, 0, off)
	return nil
}

func (c *Compiler) compileNameLoad(name string) error {
	if name == "__debug__" {
		idx := c.constPool.Add(val.BoolVal(c.optimize == 0))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)
		return nil
	}
	sym := c.scope.Symbols[name]
	if c.scope.CanSeeClass && name != annClassDictName && name != annCondAnnotations &&
		(sym == nil || sym.Storage == StorageGlobal || sym.Storage == StorageFree) {
		// PEP 649: names in a class-level __annotate__ resolve through the
		// class namespace first, then closure/globals.
		cdSym := c.scope.Symbols[annClassDictName]
		if cdSym != nil {
			_, idx := c.constPool.InternName(name)
			c.emitOp(vm.OP_LOAD_DEREF, 0, uint16(cdSym.Offset))
			if sym != nil && sym.Storage == StorageFree {
				if sym.Offset > 0xFF {
					return fmt.Errorf("SyntaxError: too many closure variables in annotation scope")
				}
				c.emitOp(vm.OP_LOAD_FROM_DICT_OR_DEREF, uint8(sym.Offset), idx)
			} else {
				c.emitOp(vm.OP_LOAD_FROM_DICT_OR_GLOBALS, 0, idx)
			}
			return nil
		}
	}
	if sym == nil {
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_LOAD_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
		}
		return nil
	}
	switch sym.Storage {
	case StorageFastLocal:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(sym.Offset))
	case StorageCell, StorageFree:
		c.emitOp(vm.OP_LOAD_DEREF, 0, uint16(sym.Offset))
	case StorageGlobal:
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_LOAD_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
		}
	default:
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_LOAD_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
		}
	}
	return nil
}

func (c *Compiler) compileNameStore(name string) error {
	if name == "__debug__" {
		return fmt.Errorf("SyntaxError: cannot assign to __debug__")
	}
	sym := c.scope.Symbols[name]
	if sym == nil {
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.isExec && c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_STORE_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
		}
		return nil
	}
	switch sym.Storage {
	case StorageFastLocal:
		c.emitOp(vm.OP_STORE_FAST, 0, uint16(sym.Offset))
	case StorageCell, StorageFree:
		c.emitOp(vm.OP_STORE_DEREF, 0, uint16(sym.Offset))
	case StorageGlobal:
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.isExec && c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_STORE_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
		}
	default:
		_, idx := c.constPool.InternName(name)
		if c.scope.Kind == ScopeClass || (c.isExec && c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			c.emitOp(vm.OP_STORE_NAME, 0, idx)
		} else {
			c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
		}
	}
	return nil
}

func (c *Compiler) compileCond(exprID ExprID, falseJumps *[]int) error {
	expr := c.arena.GetExpr(exprID)
	if expr == nil {
		return nil
	}
	if expr.Kind == ExprKindBinary && expr.Op == uint8(TOKEN_AND) {
		if err := c.compileCond(expr.Left, falseJumps); err != nil {
			return err
		}
		return c.compileCond(expr.Right, falseJumps)
	}
	if expr.Kind == ExprKindUnary && expr.Op == uint8(TOKEN_NOT) {
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		*falseJumps = append(*falseJumps, c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0))
		return nil
	}
	if err := c.compileExpr(exprID); err != nil {
		return err
	}
	*falseJumps = append(*falseJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
	return nil
}

func (c *Compiler) compileIfStmt(stmt *ASTStmt) error {
	var falseJumps []int
	if err := c.compileCond(stmt.Expr1, &falseJumps); err != nil {
		return err
	}

	// Then body
	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}
	jumpEnd := c.emitOp(vm.OP_JUMP, 0, 0)

	var exitJumps []int
	exitJumps = append(exitJumps, jumpEnd)

	for _, j := range falseJumps {
		c.patchJump(j, len(c.code))
	}

	// Elifs
	elifsStart := stmt.Extra >> 16
	elifsCount := stmt.Extra & 0xFFFF
	for i := uint32(0); i < elifsCount; i++ {
		elif := &c.arena.Elifs[elifsStart+i]
		var elifFalseJumps []int
		if err := c.compileCond(elif.Cond, &elifFalseJumps); err != nil {
			return err
		}
		for _, sID := range c.arena.StmtIDs[elif.Body.Start : elif.Body.Start+elif.Body.Count] {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
		exitJumps = append(exitJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		for _, j := range elifFalseJumps {
			c.patchJump(j, len(c.code))
		}
	}

	// Else
	if stmt.Else != 0 {
		for _, sID := range c.arena.GetStmtSlice(stmt.Else) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
	}

	// Patch all exit jumps to end of if statement
	endTarget := len(c.code)
	for _, j := range exitJumps {
		c.patchJump(j, endTarget)
	}

	return nil
}

func (c *Compiler) compileWhileStmt(stmt *ASTStmt) error {
	loopStart := len(c.code)
	loopCtx := &LoopContext{
		LoopStart: loopStart,
		Parent:    c.activeLoop,
		Cleanup:   c.activeCleanup,
	}
	c.activeLoop = loopCtx

	if err := c.compileExpr(stmt.Expr1); err != nil {
		return err
	}
	jumpExit := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}

	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	endTarget := len(c.code)
	c.patchJump(jumpExit, endTarget)

	for _, cont := range loopCtx.Continues {
		c.patchJump(cont, loopStart)
	}

	c.activeLoop = loopCtx.Parent

	if stmt.Else != 0 {
		for _, sID := range c.arena.GetStmtSlice(stmt.Else) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
	}

	afterElseTarget := len(c.code)
	for _, b := range loopCtx.Breaks {
		c.patchJump(b, afterElseTarget)
	}

	return nil
}

func (c *Compiler) compileForStmt(stmt *ASTStmt) error {
	iterExpr := c.arena.GetExpr(stmt.Expr2)
	targetExpr := c.arena.GetExpr(stmt.Expr1)

	var isFusedRange bool
	var rangeSlot uint8
	var rangeArgCount int

	if iterExpr != nil && iterExpr.Kind == ExprKindCall && targetExpr != nil && targetExpr.Kind == ExprKindName {
		call := &c.arena.Calls[iterExpr.Extra]
		calleeExpr := c.arena.GetExpr(call.Func)
		if calleeExpr != nil && calleeExpr.Kind == ExprKindName {
			calleeName := string(c.src[calleeExpr.Start:calleeExpr.End])
			if calleeName == "range" {
				rangeSym := c.scope.Symbols["range"]
				if rangeSym == nil || rangeSym.Storage == StorageGlobal {
					targetName := string(c.src[targetExpr.Start:targetExpr.End])
					targetSym := c.scope.Symbols[targetName]
					if targetSym != nil && targetSym.Storage == StorageFastLocal && targetSym.Offset < 256 {
						posArgs := c.arena.GetExprSlice(call.PosArgs)
						kwNames := c.arena.GetIdentSlice(call.KWNames)
						if len(kwNames) == 0 && (len(posArgs) == 1 || len(posArgs) == 2) {
							isFusedRange = true
							rangeSlot = uint8(targetSym.Offset)
							rangeArgCount = len(posArgs)
						}
					}
				}
			}
		}
	}

	if isFusedRange {
		posArgs := c.arena.GetExprSlice(c.arena.Calls[iterExpr.Extra].PosArgs)
		if rangeArgCount == 1 {
			if err := c.compileExpr(posArgs[0]); err != nil {
				return err
			}
			c.emitOp(vm.OP_SETUP_RANGE, 0, 1)
		} else {
			if err := c.compileExpr(posArgs[0]); err != nil {
				return err
			}
			if err := c.compileExpr(posArgs[1]); err != nil {
				return err
			}
			c.emitOp(vm.OP_SETUP_RANGE, 0, 2)
		}

		loopStart := len(c.code)
		loopCtx := &LoopContext{
			LoopStart: loopStart,
			Parent:    c.activeLoop,
			Cleanup:   c.activeCleanup,
			IsFor:     true,
		}
		c.activeLoop = loopCtx

		forIterJump := c.emitOp(vm.OP_FOR_ITER_RANGE, rangeSlot, 0)

		for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}

		c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

		endTarget := len(c.code)
		c.patchJump(forIterJump, endTarget)

		for _, cont := range loopCtx.Continues {
			c.patchJump(cont, loopStart)
		}

		c.activeLoop = loopCtx.Parent

		if stmt.Else != 0 {
			for _, sID := range c.arena.GetStmtSlice(stmt.Else) {
				if err := c.compileStmt(sID); err != nil {
					return err
				}
			}
		}

		afterElseTarget := len(c.code)
		for _, b := range loopCtx.Breaks {
			c.patchJump(b, afterElseTarget)
		}

		return nil
	}

	if err := c.compileExpr(stmt.Expr2); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)

	loopStart := len(c.code)
	loopCtx := &LoopContext{
		LoopStart: loopStart,
		Parent:    c.activeLoop,
		Cleanup:   c.activeCleanup,
		IsFor:     true,
	}
	c.activeLoop = loopCtx

	forIterJump := c.emitOp(vm.OP_FOR_ITER, 0, 0)

	c.stackDepth++
	if c.stackDepth > c.maxStackDepth {
		c.maxStackDepth = c.stackDepth
	}

	if err := c.compileStoreTarget(stmt.Expr1); err != nil {
		c.stackDepth--
		return err
	}

	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			c.stackDepth--
			return err
		}
	}

	c.stackDepth--
	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	endTarget := len(c.code)
	c.patchJump(forIterJump, endTarget)

	for _, cont := range loopCtx.Continues {
		c.patchJump(cont, loopStart)
	}

	c.activeLoop = loopCtx.Parent

	if stmt.Else != 0 {
		for _, sID := range c.arena.GetStmtSlice(stmt.Else) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
	}

	afterElseTarget := len(c.code)
	for _, b := range loopCtx.Breaks {
		c.patchJump(b, afterElseTarget)
	}

	return nil
}

func (c *Compiler) compileAsyncForStmt(stmt *ASTStmt) error {
	if err := c.compileExpr(stmt.Expr2); err != nil {
		return err
	}
	// Call __aiter__()
	_, aiterIdx := c.constPool.InternName("__aiter__")
	c.emitOp(vm.OP_LOAD_METHOD, 0, aiterIdx)
	c.emitOp(vm.OP_CALL_METHOD, 0, 0)

	iterSlot := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(iterSlot))

	loopStart := len(c.code)
	loopCtx := &LoopContext{
		LoopStart: loopStart,
		Parent:    c.activeLoop,
		Cleanup:   c.activeCleanup,
	}
	c.activeLoop = loopCtx

	// Load iter and call __anext__()
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(iterSlot))
	_, anextIdx := c.constPool.InternName("__anext__")
	c.emitOp(vm.OP_LOAD_METHOD, 0, anextIdx)
	c.emitOp(vm.OP_CALL_METHOD, 0, 0)
	c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)

	// Await anext, catching StopAsyncIteration
	anextDepth := c.stackDepth
	anextStart := len(c.code)
	c.emitOp(vm.OP_AWAIT, 0, 0)
	anextEnd := len(c.code)

	// Store target
	if err := c.compileStoreTarget(stmt.Expr1); err != nil {
		return err
	}

	// Body statements
	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}

	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	// Handler for StopAsyncIteration
	excHandlerIP := len(c.code)
	c.addExceptionRange(anextStart, anextEnd, excHandlerIP, anextDepth, false)

	// At excHandlerIP: exception is at top of stack.
	c.emitOp(vm.OP_DUP_TOP, 0, 0)
	if err := c.compileNameLoad("StopAsyncIteration"); err != nil {
		return err
	}
	c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
	jumpNotMatch := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

	// Matched StopAsyncIteration: pop exception and jump out of loop
	c.emitOp(vm.OP_POP_TOP, 0, 0)
	jumpAfterLoop := c.emitOp(vm.OP_JUMP, 0, 0)

	// Not StopAsyncIteration: re-raise
	c.patchJump(jumpNotMatch, len(c.code))
	c.emitOp(vm.OP_RAISE, 1, 0)

	c.patchJump(jumpAfterLoop, len(c.code))
	endTarget := len(c.code)

	for _, b := range loopCtx.Breaks {
		c.patchJump(b, endTarget)
	}
	for _, cont := range loopCtx.Continues {
		c.patchJump(cont, loopStart)
	}

	c.activeLoop = loopCtx.Parent

	if stmt.Else != 0 {
		for _, sID := range c.arena.GetStmtSlice(stmt.Else) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
	}

	return nil
}

// frameLocalCount is the frame size required by this scope. Cell parameters
// reserve slots without being listed in FastLocals, and temps may sit past
// both, so the slice length alone is not the slot count.
func frameLocalCount(s *Scope) int {
	if s == nil {
		return 0
	}
	n := len(s.FastLocals)
	if s.NumSlots > n {
		n = s.NumSlots
	}
	return n
}

func (c *Compiler) allocTempLocal() int {
	// len(FastLocals) is not a free slot. Captured parameters are omitted
	// from that slice but still consume frame slots, so indexing by length
	// overwrites the last real local. Which name sorts last follows map
	// iteration order and changes from compile to compile.
	offset := 0
	if c.scope.NumSlots > offset {
		offset = c.scope.NumSlots
	}
	for _, name := range c.scope.FastLocals {
		sym := c.scope.Symbols[name]
		if sym != nil && sym.Storage == StorageFastLocal && sym.Offset >= offset {
			offset = sym.Offset + 1
		}
	}
	name := fmt.Sprintf(".temp_%d", offset)
	c.scope.FastLocals = append(c.scope.FastLocals, name)
	c.scope.Symbols[name] = &Symbol{
		Name:    name,
		Storage: StorageFastLocal,
		Offset:  offset,
	}
	if offset >= c.scope.NumSlots {
		c.scope.NumSlots = offset + 1
	}
	return offset
}

func (c *Compiler) compileTryStmt(stmt *ASTStmt) error {
	tryStmt := &c.arena.TryStmts[stmt.Extra]

	var finallyCleanup *CleanupBlock
	if tryStmt.Finally.Count > 0 {
		finallyCleanup = &CleanupBlock{
			Kind:    CleanupFinally,
			Parent:  c.activeCleanup,
			Finally: tryStmt.Finally,
		}
		c.activeCleanup = finallyCleanup
	}

	tryStart := len(c.code)
	depth := c.stackDepth

	for _, sID := range c.arena.StmtIDs[tryStmt.Body.Start : tryStmt.Body.Start+tryStmt.Body.Count] {
		if err := c.compileStmt(sID); err != nil {
			if finallyCleanup != nil {
				c.activeCleanup = finallyCleanup.Parent
			}
			return err
		}
	}
	tryEnd := len(c.code)

	var normalEndJumps []int
	if tryStmt.Else.Count > 0 {
		for _, sID := range c.arena.StmtIDs[tryStmt.Else.Start : tryStmt.Else.Start+tryStmt.Else.Count] {
			if err := c.compileStmt(sID); err != nil {
				if finallyCleanup != nil {
					c.activeCleanup = finallyCleanup.Parent
				}
				return err
			}
		}
	}
	jumpEnd := c.emitOp(vm.OP_JUMP, 0, 0)
	normalEndJumps = append(normalEndJumps, jumpEnd)

	handlers := c.arena.GetExceptSlice(tryStmt.Handlers)
	var handlerEndJumps []int

	if len(handlers) > 0 {
		c.addExceptionRange(tryStart, tryEnd, len(c.code), depth, false)
	}

	for _, h := range handlers {
		var nextHandlerJump int
		if h.IsStar {
			if h.Type != ExprNone {
				if err := c.compileExpr(h.Type); err != nil {
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
				c.emitOp(vm.OP_CHECK_EXC_MATCH_STAR, 0, 0)
				nextHandlerJump = c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
			}
			c.emitOp(vm.OP_PUSH_EXC_INFO, 0, 0)
			if h.Name != "" {
				if err := c.compileNameStore(h.Name); err != nil {
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
			} else {
				c.emitOp(vm.OP_POP_TOP, 0, 0)
			}
			c.emitOp(vm.OP_POP_TOP, 0, 0) // pop rest

			cleanup := &CleanupBlock{Kind: CleanupExcept, Parent: c.activeCleanup}
			c.activeCleanup = cleanup
			for _, sID := range c.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count] {
				if err := c.compileStmt(sID); err != nil {
					c.activeCleanup = cleanup.Parent
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
			}
			c.activeCleanup = cleanup.Parent
			c.emitOp(vm.OP_POP_EXCEPT, 0, 0)
			hEndJump := c.emitOp(vm.OP_JUMP, 0, 0)
			handlerEndJumps = append(handlerEndJumps, hEndJump)

			if h.Type != ExprNone {
				c.patchJump(nextHandlerJump, len(c.code))
			}
		} else {
			if h.Type != ExprNone {
				c.emitOp(vm.OP_DUP_TOP, 0, 0)
				if err := c.compileExpr(h.Type); err != nil {
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
				c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
				nextHandlerJump = c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
			}

			c.emitOp(vm.OP_PUSH_EXC_INFO, 0, 0)
			if h.Name != "" {
				c.emitOp(vm.OP_DUP_TOP, 0, 0)
				if err := c.compileNameStore(h.Name); err != nil {
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
			}
			c.emitOp(vm.OP_POP_TOP, 0, 0)

			cleanup := &CleanupBlock{Kind: CleanupExcept, Parent: c.activeCleanup}
			c.activeCleanup = cleanup
			for _, sID := range c.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count] {
				if err := c.compileStmt(sID); err != nil {
					c.activeCleanup = cleanup.Parent
					if finallyCleanup != nil {
						c.activeCleanup = finallyCleanup.Parent
					}
					return err
				}
			}
			c.activeCleanup = cleanup.Parent
			c.emitOp(vm.OP_POP_EXCEPT, 0, 0)
			hEndJump := c.emitOp(vm.OP_JUMP, 0, 0)
			handlerEndJumps = append(handlerEndJumps, hEndJump)

			if h.Type != ExprNone {
				c.patchJump(nextHandlerJump, len(c.code))
			}
		}
	}

	if finallyCleanup != nil {
		c.activeCleanup = finallyCleanup.Parent
	}

	if len(handlers) > 0 {
		c.emitOp(vm.OP_RAISE, 1, 0)
	}

	handlersEnd := len(c.code)
	for _, j := range normalEndJumps {
		c.patchJump(j, handlersEnd)
	}
	for _, j := range handlerEndJumps {
		c.patchJump(j, handlersEnd)
	}

	if tryStmt.Finally.Count > 0 {
		// Normal path:
		for _, sID := range c.arena.StmtIDs[tryStmt.Finally.Start : tryStmt.Finally.Start+tryStmt.Finally.Count] {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
		afterFin := c.emitOp(vm.OP_JUMP, 0, 0)

		// Exception path for finally: covers try body and all handlers
		finExcTarget := len(c.code)
		c.addExceptionRange(tryStart, handlersEnd, finExcTarget, depth, false)

		// Run finally body on exception, then re-raise
		c.stackDepth++
		if c.stackDepth > c.maxStackDepth {
			c.maxStackDepth = c.stackDepth
		}
		for _, sID := range c.arena.StmtIDs[tryStmt.Finally.Start : tryStmt.Finally.Start+tryStmt.Finally.Count] {
			if err := c.compileStmt(sID); err != nil {
				c.stackDepth--
				return err
			}
		}
		c.stackDepth--
		c.emitOp(vm.OP_RAISE, 1, 0)
		c.patchJump(afterFin, len(c.code))
	}

	return nil
}

func (c *Compiler) compileRaiseStmt(stmt *ASTStmt) error {
	if stmt.Expr1 == ExprNone {
		c.emitOp(vm.OP_RAISE, 0, 0)
		return nil
	}
	if err := c.compileExpr(stmt.Expr1); err != nil {
		return err
	}
	if stmt.Expr2 != ExprNone {
		if err := c.compileExpr(stmt.Expr2); err != nil {
			return err
		}
		c.emitOp(vm.OP_RAISE, 2, 0)
	} else {
		c.emitOp(vm.OP_RAISE, 1, 0)
	}
	return nil
}

func (c *Compiler) compileWithStmt(stmt *ASTStmt) error {
	sliceStart := stmt.Extra & 0xFFFF
	sliceCount := stmt.Extra >> 16
	items := c.arena.WithItems[sliceStart : sliceStart+sliceCount]
	return c.compileWithItemRecursive(items, stmt.Body)
}

func (c *Compiler) compileWithItemRecursive(items []ASTWithItem, body uint32) error {
	if len(items) == 0 {
		for _, sID := range c.arena.GetStmtSlice(body) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
		return nil
	}

	item := items[0]
	if err := c.compileExpr(item.Expr); err != nil {
		return err
	}
	tempSlot := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot))

	// Call __enter__
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	_, enterIdx := c.constPool.InternName("__enter__")
	c.emitOp(vm.OP_CALL_METHOD_PIC, 0, enterIdx)

	if item.Target != ExprNone {
		if err := c.compileStoreTarget(item.Target); err != nil {
			return err
		}
	} else {
		c.emitOp(vm.OP_POP_TOP, 0, 0)
	}

	depth := c.stackDepth
	withStart := len(c.code)
	cleanup := &CleanupBlock{
		Kind:     CleanupWith,
		Parent:   c.activeCleanup,
		TempSlot: tempSlot,
	}
	c.activeCleanup = cleanup
	if err := c.compileWithItemRecursive(items[1:], body); err != nil {
		c.activeCleanup = cleanup.Parent
		return err
	}
	c.activeCleanup = cleanup.Parent
	withEnd := len(c.code)

	// Normal exit: call __exit__(None, None, None)
	_, exitIdx := c.constPool.InternName("__exit__")
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	noneIdx := c.constPool.Add(val.NoneVal())
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_CALL_METHOD_PIC, 3, exitIdx)
	c.emitOp(vm.OP_POP_TOP, 0, 0)
	jumpAfterWith := c.emitOp(vm.OP_JUMP, 0, 0)

	// Exception handler: call __exit__(type(exc), exc, None) and check suppression
	excTarget := len(c.code)
	c.addExceptionRange(withStart, withEnd, excTarget, depth, false)

	// At excTarget, exc is at top of stack.
	tempSlot2 := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot2))

	_, classIdx := c.constPool.InternName("__class__")
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_LOAD_ATTR, 0, classIdx)
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_CALL_METHOD_PIC, 3, exitIdx)
	jumpSuppress := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)

	// Not suppressed: reload exc and re-raise
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_RAISE, 1, 0)

	// Suppressed: continue
	c.patchJump(jumpSuppress, len(c.code))
	c.patchJump(jumpAfterWith, len(c.code))
	return nil
}

func (c *Compiler) compileAsyncWithStmt(stmt *ASTStmt) error {
	sliceStart := stmt.Extra & 0xFFFF
	sliceCount := stmt.Extra >> 16
	items := c.arena.WithItems[sliceStart : sliceStart+sliceCount]
	return c.compileAsyncWithItemRecursive(items, stmt.Body)
}

func (c *Compiler) compileAsyncWithItemRecursive(items []ASTWithItem, body uint32) error {
	if len(items) == 0 {
		for _, sID := range c.arena.GetStmtSlice(body) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
		return nil
	}

	item := items[0]
	if err := c.compileExpr(item.Expr); err != nil {
		return err
	}
	tempSlot := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot))

	// Call __aenter__ and await
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	_, enterIdx := c.constPool.InternName("__aenter__")
	c.emitOp(vm.OP_LOAD_METHOD, 0, enterIdx)
	c.emitOp(vm.OP_CALL_METHOD, 0, 0)
	c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)
	c.emitOp(vm.OP_AWAIT, 0, 0)

	if item.Target != ExprNone {
		if err := c.compileStoreTarget(item.Target); err != nil {
			return err
		}
	} else {
		c.emitOp(vm.OP_POP_TOP, 0, 0)
	}

	depth := c.stackDepth
	withStart := len(c.code)
	if err := c.compileAsyncWithItemRecursive(items[1:], body); err != nil {
		return err
	}
	withEnd := len(c.code)

	// Normal exit: call __aexit__(None, None, None) and await
	_, exitIdx := c.constPool.InternName("__aexit__")
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	c.emitOp(vm.OP_LOAD_METHOD, 0, exitIdx)
	noneIdx := c.constPool.Add(val.NoneVal())
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_CALL_METHOD, 3, 0)
	c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)
	c.emitOp(vm.OP_AWAIT, 0, 0)
	c.emitOp(vm.OP_POP_TOP, 0, 0)
	jumpAfterWith := c.emitOp(vm.OP_JUMP, 0, 0)

	// Exception handler: call __aexit__(type(exc), exc, None) and check suppression
	excTarget := len(c.code)
	c.addExceptionRange(withStart, withEnd, excTarget, depth, false)

	// At excTarget, exc is at top of stack.
	tempSlot2 := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot2))

	_, classIdx := c.constPool.InternName("__class__")
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	c.emitOp(vm.OP_LOAD_METHOD, 0, exitIdx)
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_LOAD_ATTR, 0, classIdx)
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_CALL_METHOD, 3, 0)
	c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)
	c.emitOp(vm.OP_AWAIT, 0, 0)
	jumpSuppress := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)

	// Not suppressed: reload exc and re-raise
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
	c.emitOp(vm.OP_RAISE, 1, 0)

	// Suppressed: continue
	c.patchJump(jumpSuppress, len(c.code))
	c.patchJump(jumpAfterWith, len(c.code))
	return nil
}

func (c *Compiler) compileYieldExpr(expr *ASTExpr) error {
	if expr.Left != ExprNone {
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
	} else {
		noneIdx := c.constPool.Add(val.NoneVal())
		c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	}
	c.emitOp(vm.OP_YIELD_VALUE, 0, 0)
	return nil
}

func (c *Compiler) compileYieldFromExpr(expr *ASTExpr) error {
	if err := c.compileExpr(expr.Left); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)
	c.emitOp(vm.OP_YIELD_FROM, 0, 0)
	return nil
}

func (c *Compiler) compileAwaitExpr(expr *ASTExpr) error {
	if expr.Left != ExprNone {
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
	} else {
		idx := c.constPool.Add(val.NoneVal())
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)
	}
	c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)
	c.emitOp(vm.OP_AWAIT, 0, 0)
	return nil
}

func (c *Compiler) compileFuncDef(stmt *ASTStmt) error {
	fnDef := &c.arena.FuncDefs[stmt.Extra]

	// 1. Evaluate decorators in enclosing scope
	decorators := c.arena.GetExprSlice(fnDef.Decorators)
	for _, dec := range decorators {
		if err := c.compileExpr(dec); err != nil {
			return err
		}
	}

	// Find child scope matching function name
	fnScope := c.nextChildScope(ScopeFunction, fnDef.Name)

	firstLine, _ := c.offsetToLineCol(stmt.Start)
	qualName := c.qualnamePrefix + fnDef.Name

	// Compile function code object
	subCompiler := &Compiler{
		src:            c.src,
		arena:          c.arena,
		scope:          fnScope,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 64),
		filename:       c.filename,
		qualnamePrefix: qualName + ".<locals>.",
		lineStarts:     c.lineStarts,
		curLine:        firstLine,
		curCol:         1,
		optimize:       c.optimize,
	}

	tpCount := uint16(fnDef.TypeParams.Count)
	if tpCount > 0 {
		tps := c.arena.TypeParams[fnDef.TypeParams.Start : fnDef.TypeParams.Start+uint32(tpCount)]
		for _, tp := range tps {
			typeName := "TypeVar"
			if tp.Kind == TypeParamTypeVarTuple {
				typeName = "TypeVarTuple"
			} else if tp.Kind == TypeParamParamSpec {
				typeName = "ParamSpec"
			}
			_, clsIdx := subCompiler.constPool.InternName(typeName)
			subCompiler.emitOp(vm.OP_LOAD_GLOBAL, 0, clsIdx)
			pNameIdx := subCompiler.constPool.Add(val.InternString(tp.Name))
			subCompiler.emitOp(vm.OP_LOAD_CONST, 0, pNameIdx)
			subCompiler.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
			if err := subCompiler.compileNameStore(tp.Name); err != nil {
				return err
			}
		}
	}

	for _, sID := range c.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count] {
		if err := subCompiler.compileStmt(sID); err != nil {
			return err
		}
	}

	subCompiler.emitReturnNone()
	subCompiler.peephole()

	// Param names as interned values
	paramNames := fnScope.ParamNames
	paramValSlice := make([]val.Value, len(paramNames))
	for i, p := range paramNames {
		paramValSlice[i] = val.InternString(p)
	}

	cellValSlice := make([]val.Value, len(fnScope.CellVars))
	for i, cv := range fnScope.CellVars {
		cellValSlice[i] = val.InternString(cv)
	}

	freeValSlice := make([]val.Value, len(fnScope.FreeVars))
	for i, fv := range fnScope.FreeVars {
		freeValSlice[i] = val.InternString(fv)
	}

	numLocals := frameLocalCount(fnScope)
	if numLocals < len(paramNames) {
		numLocals = len(paramNames)
	}

	maxStack := numLocals + subCompiler.maxStackDepth + 16
	var coFlags uint32
	if fnScope.IsGenerator {
		coFlags |= vm.FlagGenerator | vm.CO_GENERATOR
	}
	if fnScope.IsCoroutine {
		coFlags |= vm.FlagCoroutine | vm.CO_COROUTINE
	}
	coFlags |= vm.CO_OPTIMIZED | vm.CO_NEWLOCALS
	if len(fnScope.FreeVars) == 0 && len(fnScope.CellVars) == 0 {
		coFlags |= vm.CO_NOFREE
	}
	if c.qualnamePrefix != "" {
		coFlags |= vm.CO_NESTED
	}

	varNames := make([]val.Value, 0, len(paramNames)+len(fnScope.FastLocals))
	seenNames := make(map[string]bool)
	for _, p := range paramNames {
		clean := p
		if strings.HasPrefix(clean, "**") {
			clean = clean[2:]
			coFlags |= vm.CO_VARKEYWORDS
		} else if strings.HasPrefix(clean, "*") {
			clean = clean[1:]
			if clean != "" {
				coFlags |= vm.CO_VARARGS
			}
		}
		if clean != "" && !seenNames[clean] {
			seenNames[clean] = true
			varNames = append(varNames, val.InternString(clean))
		}
	}
	for _, fl := range fnScope.FastLocals {
		if !seenNames[fl] {
			seenNames[fl] = true
			varNames = append(varNames, val.InternString(fl))
		}
	}

	fnDoc := val.NoneVal()
	if fnDef.Body.Count > 0 {
		bodySlice := c.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count]
		if docStr, hasDoc := c.extractDocstringFromSlice(bodySlice); hasDoc {
			fnDoc = val.NewStringVal(docStr)
		}
	}

	var canSuspend bool
	if fnScope.IsCoroutine {
		for _, inst := range subCompiler.code {
			op := vm.DecodeOpcode(inst)
			if op == vm.OP_AWAIT || op == vm.OP_YIELD_VALUE || op == vm.OP_YIELD_FROM {
				canSuspend = true
				break
			}
		}
	}

	fnCodeObj := &vm.CodeObject{
		Name:            fnDef.Name,
		QualName:        qualName,
		FileName:        c.filename,
		FirstLineNo:     int(firstLine),
		PosOnlyArgCount: int(fnDef.PosOnlyCount),
		KWOnlyArgCount:  int(fnDef.KWOnlyCount),
		Code:            subCompiler.code,
		Consts:          subCompiler.constPool.Slice(),
		ParamNames:      paramValSlice,
		VarNames:        varNames,
		CellVars:        cellValSlice,
		FreeVars:        freeValSlice,
		NumLocals:       numLocals,
		NumParams:       len(paramNames),
		MaxStackSize:    maxStack,
		IsGenerator:     fnScope.IsGenerator,
		IsCoroutine:     fnScope.IsCoroutine,
		CanSuspend:      canSuspend,
		Flags:           coFlags,
		Doc:             fnDoc,
		ExceptionTable:  subCompiler.exceptionTable,
		LineSpans:       subCompiler.lineSpans,
	}

	var flags uint8

	// PEP 649 / 749: Deferred __annotate__(format)
	if fnDef.Returns != ExprNone || fnDef.ParamTypes.Count > 0 {
		annScope := NewScope(ScopeFunction, "__annotate__", c.scope)
		annScope.ParamNames = []string{"format"}
		annScope.FastLocals = []string{"format"}
		annScope.Symbols["format"] = &Symbol{Name: "format", Storage: StorageFastLocal, Offset: 0}

		if fnDef.TypeParams.Count > 0 {
			tps := c.arena.TypeParams[fnDef.TypeParams.Start : fnDef.TypeParams.Start+uint32(fnDef.TypeParams.Count)]
			for _, tp := range tps {
				offset := len(annScope.FastLocals)
				annScope.FastLocals = append(annScope.FastLocals, tp.Name)
				annScope.Symbols[tp.Name] = &Symbol{Name: tp.Name, Storage: StorageFastLocal, Offset: offset}
				annScope.Bound[tp.Name] = true
			}
		}

		annCompiler := &Compiler{
			src:       c.src,
			arena:     c.arena,
			scope:     annScope,
			constPool: NewConstPool(),
			code:      make([]uint32, 0, 32),
			optimize:  c.optimize,
		}

		// PEP 749: only VALUE (1) and VALUE_WITH_FAKE_GLOBALS (2) are
		// supported; anything else raises NotImplementedError so that
		// annotationlib can emulate STRING/FORWARDREF.
		annCompiler.emitOp(vm.OP_LOAD_FAST, 0, 0)
		annCompiler.emitOp(vm.OP_LOAD_CONST, 0, annCompiler.constPool.Add(val.Int64Val(2)))
		annCompiler.emitOp(vm.OP_COMPARE_OP, vm.CMP_GT, 0)
		annSkip := annCompiler.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
		_, niIdx := annCompiler.constPool.InternName("NotImplementedError")
		annCompiler.emitOp(vm.OP_LOAD_GLOBAL, 0, niIdx)
		annCompiler.emitOp(vm.OP_RAISE, 1, 0)
		annCompiler.patchJump(annSkip, len(annCompiler.code))

		if fnDef.TypeParams.Count > 0 {
			tps := c.arena.TypeParams[fnDef.TypeParams.Start : fnDef.TypeParams.Start+uint32(fnDef.TypeParams.Count)]
			for _, tp := range tps {
				typeName := "TypeVar"
				if tp.Kind == TypeParamTypeVarTuple {
					typeName = "TypeVarTuple"
				} else if tp.Kind == TypeParamParamSpec {
					typeName = "ParamSpec"
				}
				_, clsIdx := annCompiler.constPool.InternName(typeName)
				annCompiler.emitOp(vm.OP_LOAD_GLOBAL, 0, clsIdx)
				pNameIdx := annCompiler.constPool.Add(val.InternString(tp.Name))
				annCompiler.emitOp(vm.OP_LOAD_CONST, 0, pNameIdx)
				annCompiler.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
				if err := annCompiler.compileNameStore(tp.Name); err != nil {
					return err
				}
			}
		}

		pNames := c.arena.GetIdentSlice(fnDef.ParamNames)
		pTypes := c.arena.GetExprSlice(fnDef.ParamTypes)
		pairCount := uint16(0)
		for i, pName := range pNames {
			if i < len(pTypes) && pTypes[i] != ExprNone {
				pName = strings.TrimLeft(pName, "*")
				_, pNameIdx := annCompiler.constPool.InternName(pName)
				annCompiler.emitOp(vm.OP_LOAD_CONST, 0, pNameIdx)
				if err := annCompiler.compileExpr(pTypes[i]); err != nil {
					return err
				}
				pairCount++
			}
		}
		if fnDef.Returns != ExprNone {
			_, retNameIdx := annCompiler.constPool.InternName("return")
			annCompiler.emitOp(vm.OP_LOAD_CONST, 0, retNameIdx)
			if err := annCompiler.compileExpr(fnDef.Returns); err != nil {
				return err
			}
			pairCount++
		}
		annCompiler.emitOp(vm.OP_BUILD_MAP, 0, pairCount)
		annCompiler.emitOp(vm.OP_RETURN, 0, 0)
		annCompiler.peephole()

		annCodeObj := &vm.CodeObject{
			Name:         "__annotate__",
			Code:         annCompiler.code,
			Consts:       annCompiler.constPool.Slice(),
			NumParams:    1,
			NumLocals:    frameLocalCount(annScope),
			MaxStackSize: 16,
		}
		annVal := vm.NewCodeVal(annCodeObj)
		annIdx := c.constPool.Add(annVal)

		c.emitOp(vm.OP_LOAD_CONST, 0, annIdx)
		c.emitOp(vm.OP_MAKE_FUNCTION, 0, 0)
		flags |= 0x04
	}

	// Defaults (if any)
	defaults := c.arena.GetExprSlice(fnDef.Defaults)
	if len(defaults) > 0 {
		flags |= 0x02
		for _, dExpr := range defaults {
			if err := c.compileExpr(dExpr); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(defaults)))
	}

	// Keyword-only defaults (if any)
	kwDefaults := c.arena.GetExprSlice(fnDef.KWDefaults)
	kwDefNames := c.arena.GetIdentSlice(fnDef.KWDefaultNames)
	if len(kwDefaults) > 0 {
		flags |= 0x08
		for i, dExpr := range kwDefaults {
			_, nameIdx := c.constPool.InternName(kwDefNames[i])
			c.emitOp(vm.OP_LOAD_CONST, 0, nameIdx)
			if err := c.compileExpr(dExpr); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_MAP, 0, uint16(len(kwDefaults)))
	}

	// Closure cells (if FreeVars > 0)
	if len(fnScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range fnScope.FreeVars {
			if err := c.emitLoadClosure(fv); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(fnScope.FreeVars)))
	}

	coVal := vm.NewCodeVal(fnCodeObj)
	coIdx := c.constPool.Add(coVal)
	c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
	c.emitOp(vm.OP_MAKE_FUNCTION, flags, 0)

	// Apply decorators in reverse order
	for i := len(decorators) - 1; i >= 0; i-- {
		c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
	}

	return c.compileNameStore(fnDef.Name)
}

func (c *Compiler) compileClassDef(stmt *ASTStmt) error {
	clsDef := &c.arena.ClassDefs[stmt.Extra]

	// 1. Evaluate decorators in enclosing scope
	decorators := c.arena.GetExprSlice(clsDef.Decorators)
	for _, dec := range decorators {
		if err := c.compileExpr(dec); err != nil {
			return err
		}
	}

	clsScope := c.nextChildScope(ScopeClass, clsDef.Name)

	firstLine, _ := c.offsetToLineCol(stmt.Start)
	clsQualName := c.qualnamePrefix + clsDef.Name

	// Compile class body code object
	subCompiler := &Compiler{
		src:            c.src,
		arena:          c.arena,
		scope:          clsScope,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 64),
		filename:       c.filename,
		qualnamePrefix: clsQualName + ".",
		lineStarts:     c.lineStarts,
		curLine:        firstLine,
		curCol:         1,
		optimize:       c.optimize,
	}

	clsTPCount := uint16(clsDef.TypeParams.Count)
	if clsTPCount > 0 {
		tps := c.arena.TypeParams[clsDef.TypeParams.Start : clsDef.TypeParams.Start+uint32(clsTPCount)]
		for _, tp := range tps {
			typeName := "TypeVar"
			if tp.Kind == TypeParamTypeVarTuple {
				typeName = "TypeVarTuple"
			} else if tp.Kind == TypeParamParamSpec {
				typeName = "ParamSpec"
			}
			_, clsIdx := subCompiler.constPool.InternName(typeName)
			subCompiler.emitOp(vm.OP_LOAD_GLOBAL, 0, clsIdx)
			pNameIdx := subCompiler.constPool.Add(val.InternString(tp.Name))
			subCompiler.emitOp(vm.OP_LOAD_CONST, 0, pNameIdx)
			subCompiler.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
			if err := subCompiler.compileNameStore(tp.Name); err != nil {
				return err
			}
		}
	}

	if clsScope != nil && clsScope.HasAnnotations {
		// PEP 649: capture the class namespace for the deferred __annotate__.
		subCompiler.emitOp(vm.OP_LOAD_LOCALS, 0, 0)
		if err := subCompiler.compileNameStore(annClassDictName); err != nil {
			return err
		}
		if clsScope.AnnHasCond {
			subCompiler.emitOp(vm.OP_BUILD_SET, 0, 0)
			if err := subCompiler.compileNameStore(annCondAnnotations); err != nil {
				return err
			}
		}
	}

	clsBodySlice := c.arena.StmtIDs[clsDef.Body.Start : clsDef.Body.Start+clsDef.Body.Count]
	if docStr, hasDoc := c.extractDocstringFromSlice(clsBodySlice); hasDoc {
		docIdx := subCompiler.constPool.Add(val.NewStringVal(docStr))
		subCompiler.emitOp(vm.OP_LOAD_CONST, 0, docIdx)
		if err := subCompiler.compileNameStore("__doc__"); err != nil {
			return err
		}
	}

	for _, sID := range clsBodySlice {
		if err := subCompiler.compileStmt(sID); err != nil {
			return err
		}
	}
	if clsScope != nil && clsScope.HasAnnotations {
		if err := subCompiler.emitAnnotateFunction(); err != nil {
			return err
		}
	}
	subCompiler.emitReturnNone()
	subCompiler.peephole()

	clsCodeObj := &vm.CodeObject{
		Name:         clsDef.Name,
		QualName:     clsQualName,
		FileName:     c.filename,
		FirstLineNo:  int(firstLine),
		Code:         subCompiler.code,
		Consts:       subCompiler.constPool.Slice(),
		NumLocals:    frameLocalCount(subCompiler.scope),
		NumParams:    0,
		MaxStackSize: subCompiler.maxStackDepth + 16,
		LineSpans:    subCompiler.lineSpans,
	}
	if clsScope != nil && len(clsScope.CellVars) > 0 {
		clsCodeObj.CellVars = make([]val.Value, len(clsScope.CellVars))
		for i, cv := range clsScope.CellVars {
			clsCodeObj.CellVars[i] = val.InternString(cv)
		}
	}
	if clsScope != nil && len(clsScope.FreeVars) > 0 {
		clsCodeObj.FreeVars = make([]val.Value, len(clsScope.FreeVars))
		for i, fv := range clsScope.FreeVars {
			clsCodeObj.FreeVars[i] = val.InternString(fv)
		}
	}

	// Push class body. Free vars arrive as a function closure so the
	// class body can pass enclosing cells into methods.
	coVal := vm.NewCodeVal(clsCodeObj)
	coIdx := c.constPool.Add(coVal)
	if clsScope != nil && len(clsScope.FreeVars) > 0 {
		for _, fv := range clsScope.FreeVars {
			if err := c.emitLoadClosure(fv); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(clsScope.FreeVars)))
		c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
		c.emitOp(vm.OP_MAKE_FUNCTION, 0x01, 0)
	} else {
		c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
	}

	// Push class name string
	nameVal, nameIdx := c.constPool.InternName(clsDef.Name)
	_ = nameVal
	c.emitOp(vm.OP_LOAD_CONST, 0, nameIdx)

	// Push bases tuple
	bases := c.arena.GetExprSlice(clsDef.Bases)
	for _, bExpr := range bases {
		if err := c.compileExpr(bExpr); err != nil {
			return err
		}
	}
	c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(bases)))

	// Check keywords (e.g. metaclass=Meta or kw=val)
	kwNames := c.arena.GetIdentSlice(clsDef.KWNames)
	kwArgs := c.arena.GetExprSlice(clsDef.KWArgs)
	hasKW := len(kwArgs) > 0
	if hasKW {
		hasUnpack := false
		for _, name := range kwNames {
			if name == "" {
				hasUnpack = true
				break
			}
		}
		if hasUnpack {
			numDicts := uint16(0)
			currentKeys := 0
			for i, name := range kwNames {
				if name == "" {
					if currentKeys > 0 {
						c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
						numDicts++
						currentKeys = 0
					}
					if err := c.compileExpr(kwArgs[i]); err != nil {
						return err
					}
					numDicts++
				} else {
					nameConst := c.constPool.Add(val.InternString(name))
					c.emitOp(vm.OP_LOAD_CONST, 0, nameConst)
					if err := c.compileExpr(kwArgs[i]); err != nil {
						return err
					}
					currentKeys++
				}
			}
			if currentKeys > 0 {
				c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
				numDicts++
			}
			c.emitOp(vm.OP_BUILD_MAP_UNPACK, 0, numDicts)
		} else {
			for i, name := range kwNames {
				nameConst := c.constPool.Add(val.InternString(name))
				c.emitOp(vm.OP_LOAD_CONST, 0, nameConst)
				if err := c.compileExpr(kwArgs[i]); err != nil {
					return err
				}
			}
			c.emitOp(vm.OP_BUILD_MAP, 0, uint16(len(kwArgs)))
		}
		c.emitOp(vm.OP_BUILD_CLASS, 1, 0)
	} else {
		c.emitOp(vm.OP_BUILD_CLASS, 0, 0)
	}

	// Apply decorators in reverse order
	for i := len(decorators) - 1; i >= 0; i-- {
		c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
	}

	// Store into class name
	return c.compileNameStore(clsDef.Name)
}

func (c *Compiler) compileExpr(exprID ExprID) error {
	expr := c.arena.GetExpr(exprID)
	if expr == nil {
		noneIdx := c.constPool.Add(val.NoneVal())
		c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
		return nil
	}
	c.setSourcePos(expr.Start)

	switch expr.Kind {
	case ExprKindInt:
		v := c.arena.IntConsts[expr.Extra]
		idx := c.constPool.Add(val.Int64Val(v))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindFloat:
		v := c.arena.FloatConsts[expr.Extra]
		idx := c.constPool.Add(val.Float64Val(v))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindBigInt:
		idx := c.constPool.Add(val.NewBigIntVal(c.arena.BigConsts[expr.Extra]))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindImag:
		v := c.arena.FloatConsts[expr.Extra]
		idx := c.constPool.Add(val.NewComplexVal(0, v))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindString:
		rawStr := string(c.src[expr.Start:expr.End])
		if expr.Op&1 == 0 && isBytesToken(rawStr) {
			parsedBytes := parseBytesLiteral(rawStr)
			idx := c.constPool.Add(val.NewBytesVal(parsedBytes))
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			return nil
		}
		var parsedStr string
		if expr.Op&1 != 0 {
			parsedStr = decodeFStringPart(rawStr, expr.Op&2 != 0)
		} else {
			parsedStr = parseStringLiteral(rawStr)
		}
		idx := c.constPool.Add(val.NewStringVal(parsedStr))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindBool:
		idx := c.constPool.Add(val.BoolVal(expr.Op != 0))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindNoneLit:
		idx := c.constPool.Add(val.NoneVal())
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindName:
		name := string(c.src[expr.Start:expr.End])
		return c.compileNameLoad(name)

	case ExprKindUnary:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if expr.Op == uint8(TOKEN_MINUS) {
			c.emitOp(vm.OP_UNARY_NEGATIVE, 0, 0)
		} else if expr.Op == uint8(TOKEN_PLUS) {
			c.emitOp(vm.OP_UNARY_POSITIVE, 0, 0)
		} else if expr.Op == uint8(TOKEN_NOT) {
			c.emitOp(vm.OP_UNARY_NOT, 0, 0)
		} else if expr.Op == uint8(TOKEN_TILDE) {
			c.emitOp(vm.OP_UNARY_INVERT, 0, 0)
		}

	case ExprKindBinary:
		// Check for logical and / or short-circuiting
		if expr.Op == uint8(TOKEN_AND) {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			jumpEnd := c.emitOp(vm.OP_JUMP_IF_FALSE_OR_POP, 0, 0)
			if err := c.compileExpr(expr.Right); err != nil {
				return err
			}
			c.patchJump(jumpEnd, len(c.code))
			return nil
		}
		if expr.Op == uint8(TOKEN_OR) {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			jumpEnd := c.emitOp(vm.OP_JUMP_IF_TRUE_OR_POP, 0, 0)
			if err := c.compileExpr(expr.Right); err != nil {
				return err
			}
			c.patchJump(jumpEnd, len(c.code))
			return nil
		}

		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		switch TokenType(expr.Op) {
		case TOKEN_PLUS:
			c.emitOp(vm.OP_BINARY_ADD, 0, 0)
		case TOKEN_MINUS:
			c.emitOp(vm.OP_BINARY_SUB, 0, 0)
		case TOKEN_STAR:
			c.emitOp(vm.OP_BINARY_MUL, 0, 0)
		case TOKEN_SLASH:
			c.emitOp(vm.OP_BINARY_TRUE_DIV, 0, 0)
		case TOKEN_SLASHSLASH:
			c.emitOp(vm.OP_BINARY_FLOOR_DIV, 0, 0)
		case TOKEN_PERCENT:
			c.emitOp(vm.OP_BINARY_MOD, 0, 0)
		case TOKEN_STARSTAR:
			c.emitOp(vm.OP_BINARY_POW, 0, 0)
		case TOKEN_AMP:
			c.emitOp(vm.OP_BINARY_AND, 0, 0)
		case TOKEN_PIPE:
			c.emitOp(vm.OP_BINARY_OR, 0, 0)
		case TOKEN_CARET:
			c.emitOp(vm.OP_BINARY_XOR, 0, 0)
		case TOKEN_LSHIFT:
			c.emitOp(vm.OP_BINARY_LSHIFT, 0, 0)
		case TOKEN_RSHIFT:
			c.emitOp(vm.OP_BINARY_RSHIFT, 0, 0)
		case TOKEN_AT:
			c.emitOp(vm.OP_BINARY_MATMUL, 0, 0)
		}

	case ExprKindCompare:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		if expr.Op == vm.CMP_LT {
			c.emitOp(vm.OP_COMPARE_LT, 0, 0)
		} else {
			c.emitOp(vm.OP_COMPARE_OP, expr.Op, 0)
		}

	case ExprKindIfExp:
		cond := ExprID(expr.Extra)
		if err := c.compileExpr(cond); err != nil {
			return err
		}
		jumpFalse := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		jumpEnd := c.emitOp(vm.OP_JUMP, 0, 0)
		c.patchJump(jumpFalse, len(c.code))
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.patchJump(jumpEnd, len(c.code))

	case ExprKindCall:
		return c.compileCall(expr)

	case ExprKindAttribute:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		attrName := string(c.src[expr.Start:expr.End])
		_, idx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_LOAD_ATTR, 0, idx)

	case ExprKindSubscript:
		rightExpr := c.arena.GetExpr(expr.Right)
		if rightExpr != nil && rightExpr.Kind == ExprKindSlice && rightExpr.Extra == 0 {
			// Zero-allocation binary slice: container[start:stop]
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			if rightExpr.Left != ExprNone {
				if err := c.compileExpr(rightExpr.Left); err != nil {
					return err
				}
			} else {
				idx := c.constPool.Add(val.NoneVal())
				c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			}
			if rightExpr.Right != ExprNone {
				if err := c.compileExpr(rightExpr.Right); err != nil {
					return err
				}
			} else {
				idx := c.constPool.Add(val.NoneVal())
				c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			}
			c.emitOp(vm.OP_BINARY_SLICE, 0, 0)
			return nil
		}
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)

	case ExprKindList:
		slice := c.arena.ExprSlices[expr.Extra]
		return c.compileSequenceDisplay(c.arena.GetExprSlice(slice), vm.OP_BUILD_LIST)

	case ExprKindTuple:
		slice := c.arena.ExprSlices[expr.Extra]
		return c.compileSequenceDisplay(c.arena.GetExprSlice(slice), vm.OP_BUILD_TUPLE)

	case ExprKindDict:
		start := expr.Extra
		count := uint32(expr.Right)
		hasUnpack := false
		for i := uint32(0); i < count; i++ {
			if c.arena.DictPairs[start+i].Key == ExprNone {
				hasUnpack = true
				break
			}
		}
		if !hasUnpack {
			for i := uint32(0); i < count; i++ {
				pair := &c.arena.DictPairs[start+i]
				if err := c.compileExpr(pair.Key); err != nil {
					return err
				}
				if err := c.compileExpr(pair.Value); err != nil {
					return err
				}
			}
			c.emitOp(vm.OP_BUILD_MAP, 0, uint16(count))
		} else {
			numDicts := uint16(0)
			currentKeys := 0
			for i := uint32(0); i < count; i++ {
				pair := &c.arena.DictPairs[start+i]
				if pair.Key == ExprNone {
					if currentKeys > 0 {
						c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
						numDicts++
						currentKeys = 0
					}
					if err := c.compileExpr(pair.Value); err != nil {
						return err
					}
					numDicts++
				} else {
					if err := c.compileExpr(pair.Key); err != nil {
						return err
					}
					if err := c.compileExpr(pair.Value); err != nil {
						return err
					}
					currentKeys++
				}
			}
			if currentKeys > 0 {
				c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
				numDicts++
			}
			c.emitOp(vm.OP_BUILD_MAP_UNPACK, 0, numDicts)
		}

	case ExprKindListComp:
		return c.compileListComp(expr)

	case ExprKindDictComp:
		return c.compileDictComp(expr)

	case ExprKindYield:
		return c.compileYieldExpr(expr)

	case ExprKindYieldFrom:
		return c.compileYieldFromExpr(expr)

	case ExprKindAwait:
		return c.compileAwaitExpr(expr)

	case ExprKindSlice:
		if expr.Left != ExprNone {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
		} else {
			idx := c.constPool.Add(val.NoneVal())
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
		}
		if expr.Right != ExprNone {
			if err := c.compileExpr(expr.Right); err != nil {
				return err
			}
		} else {
			idx := c.constPool.Add(val.NoneVal())
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
		}
		if expr.Extra != 0 {
			if err := c.compileExpr(ExprID(expr.Extra)); err != nil {
				return err
			}
			c.emitOp(vm.OP_BUILD_SLICE, 0, 3)
		} else {
			c.emitOp(vm.OP_BUILD_SLICE, 0, 2)
		}

	case ExprKindJoinedStr:
		fs := c.arena.GetFString(expr.Extra)
		if fs == nil {
			idx := c.constPool.Add(val.NewStringVal(""))
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			return nil
		}
		pieces := c.arena.GetExprSlice(fs.Pieces)
		for _, pID := range pieces {
			if err := c.compileExpr(pID); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_STRING, 0, uint16(len(pieces)))

	case ExprKindTemplateStr:
		fs := c.arena.GetFString(expr.Extra)
		if fs == nil {
			idx := c.constPool.Add(val.NewStringVal(""))
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			c.emitOp(vm.OP_BUILD_TEMPLATE, 0, 0)
			return nil
		}
		return c.compileTemplate(fs)

	case ExprKindFormattedValue:
		fv := c.arena.GetFormattedValue(expr.Extra)
		if fv == nil {
			return nil
		}
		if fv.DebugText != "" {
			dbgIdx := c.constPool.Add(val.NewStringVal(fv.DebugText))
			c.emitOp(vm.OP_LOAD_CONST, 0, dbgIdx)
			if err := c.compileExpr(fv.Value); err != nil {
				return err
			}
			if fv.FormatSpec != ExprNone {
				if err := c.compileExpr(fv.FormatSpec); err != nil {
					return err
				}
				c.emitOp(vm.OP_FORMAT_VALUE, fv.Conversion, 1)
			} else {
				conv := fv.Conversion
				if conv == 0 {
					conv = 'r'
				}
				c.emitOp(vm.OP_FORMAT_VALUE, conv, 0)
			}
			c.emitOp(vm.OP_BUILD_STRING, 0, 2)
		} else {
			if err := c.compileExpr(fv.Value); err != nil {
				return err
			}
			if fv.FormatSpec != ExprNone {
				if err := c.compileExpr(fv.FormatSpec); err != nil {
					return err
				}
				c.emitOp(vm.OP_FORMAT_VALUE, fv.Conversion, 1)
			} else {
				c.emitOp(vm.OP_FORMAT_VALUE, fv.Conversion, 0)
			}
		}

	case ExprKindSet:
		slice := c.arena.ExprSlices[expr.Extra]
		return c.compileSequenceDisplay(c.arena.GetExprSlice(slice), vm.OP_BUILD_SET)

	case ExprKindSetComp:
		return c.compileSetComp(expr)

	case ExprKindGeneratorExp:
		return c.compileGenExp(expr)

	case ExprKindLambda:
		return c.compileLambda(expr)

	case ExprKindStarred:
		return c.compileExpr(expr.Left)

	case ExprKindNamedExpr:
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_DUP_TOP, 0, 0)
		return c.compileStoreTarget(expr.Left)

	case ExprKindEllipsis:
		_, idx := c.constPool.InternName("Ellipsis")
		c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
	}

	return nil
}

// compileSequenceDisplay compiles a list, tuple, or set display.
// Starred elements are unpacked (PEP 448). Displays without stars keep
// the direct BUILD_* opcodes.
func (c *Compiler) compileSequenceDisplay(elemIDs []ExprID, finish vm.Opcode) error {
	hasStar := false
	for _, id := range elemIDs {
		if e := c.arena.GetExpr(id); e != nil && e.Kind == ExprKindStarred {
			hasStar = true
			break
		}
	}
	if !hasStar {
		for _, id := range elemIDs {
			if err := c.compileExpr(id); err != nil {
				return err
			}
		}
		c.emitOp(finish, 0, uint16(len(elemIDs)))
		return nil
	}
	c.emitOp(vm.OP_BUILD_LIST, 0, 0)
	for _, id := range elemIDs {
		e := c.arena.GetExpr(id)
		if e != nil && e.Kind == ExprKindStarred {
			if err := c.compileExpr(e.Left); err != nil {
				return err
			}
			c.emitOp(vm.OP_LIST_EXTEND, 0, 1)
			continue
		}
		if err := c.compileExpr(id); err != nil {
			return err
		}
		c.emitOp(vm.OP_LIST_APPEND, 0, 1)
	}
	switch finish {
	case vm.OP_BUILD_TUPLE:
		c.emitOp(vm.OP_LIST_TO_TUPLE, 0, 0)
	case vm.OP_BUILD_SET:
		c.emitOp(vm.OP_LIST_TO_SET, 0, 0)
	}
	return nil
}

func (c *Compiler) compileCall(expr *ASTExpr) error {
	call := &c.arena.Calls[expr.Extra]
	posArgs := c.arena.GetExprSlice(call.PosArgs)
	kwNames := c.arena.GetIdentSlice(call.KWNames)
	kwArgs := c.arena.GetExprSlice(call.KWArgs)

	hasStarPos := false
	for _, a := range posArgs {
		argExpr := c.arena.GetExpr(a)
		if argExpr != nil && argExpr.Kind == ExprKindStarred {
			hasStarPos = true
			break
		}
	}
	hasStarKW := false
	for _, name := range kwNames {
		if name == "" {
			hasStarKW = true
			break
		}
	}

	calleeExpr := c.arena.GetExpr(call.Func)
	isMethod := !hasStarPos && !hasStarKW && calleeExpr != nil && calleeExpr.Kind == ExprKindAttribute && len(kwNames) == 0

	if isMethod {
		if err := c.compileExpr(calleeExpr.Left); err != nil {
			return err
		}
		for _, arg := range posArgs {
			if err := c.compileExpr(arg); err != nil {
				return err
			}
		}
		attrName := string(c.src[calleeExpr.Start:calleeExpr.End])
		_, idx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_CALL_METHOD_PIC, uint8(len(posArgs)), idx)
		return nil
	}

	if hasStarPos || hasStarKW {
		// Call using OP_CALL_FUNCTION_EX
		if err := c.compileExpr(call.Func); err != nil {
			return err
		}

		// Build positional args tuple / list
		if hasStarPos {
			c.emitOp(vm.OP_BUILD_LIST, 0, 0)
			for _, arg := range posArgs {
				argExpr := c.arena.GetExpr(arg)
				if argExpr != nil && argExpr.Kind == ExprKindStarred {
					if err := c.compileExpr(argExpr.Left); err != nil {
						return err
					}
					c.emitOp(vm.OP_LIST_EXTEND, 0, 1)
				} else {
					if err := c.compileExpr(arg); err != nil {
						return err
					}
					c.emitOp(vm.OP_LIST_APPEND, 0, 1)
				}
			}
		} else {
			for _, arg := range posArgs {
				if err := c.compileExpr(arg); err != nil {
					return err
				}
			}
			c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(posArgs)))
		}

		// Build keyword args dict (if any)
		if len(kwArgs) > 0 {
			if hasStarKW {
				numDicts := uint16(0)
				currentKeys := 0
				for i, name := range kwNames {
					if name == "" {
						if currentKeys > 0 {
							c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
							numDicts++
							currentKeys = 0
						}
						if err := c.compileExpr(kwArgs[i]); err != nil {
							return err
						}
						numDicts++
					} else {
						nameConst := c.constPool.Add(val.InternString(name))
						c.emitOp(vm.OP_LOAD_CONST, 0, nameConst)
						if err := c.compileExpr(kwArgs[i]); err != nil {
							return err
						}
						currentKeys++
					}
				}
				if currentKeys > 0 {
					c.emitOp(vm.OP_BUILD_MAP, 0, uint16(currentKeys))
					numDicts++
				}
				c.emitOp(vm.OP_BUILD_MAP_UNPACK, 0, numDicts)
			} else {
				for i, name := range kwNames {
					nameConst := c.constPool.Add(val.InternString(name))
					c.emitOp(vm.OP_LOAD_CONST, 0, nameConst)
					if err := c.compileExpr(kwArgs[i]); err != nil {
						return err
					}
				}
				c.emitOp(vm.OP_BUILD_MAP, 0, uint16(len(kwArgs)))
			}
			c.emitOp(vm.OP_CALL_FUNCTION_EX, 1, 0)
		} else {
			c.emitOp(vm.OP_CALL_FUNCTION_EX, 0, 0)
		}
		return nil
	}

	// Normal call
	if err := c.compileExpr(call.Func); err != nil {
		return err
	}
	for _, arg := range posArgs {
		if err := c.compileExpr(arg); err != nil {
			return err
		}
	}
	for _, kwArg := range kwArgs {
		if err := c.compileExpr(kwArg); err != nil {
			return err
		}
	}

	if len(kwNames) > 0 {
		var kwTupleItems []val.Value
		for _, name := range kwNames {
			kwTupleItems = append(kwTupleItems, val.InternString(name))
		}
		kwTupleVal := val.NewTupleVal(kwTupleItems...)
		idx := c.constPool.Add(kwTupleVal)
		totalArgs := len(posArgs) + len(kwArgs)
		c.emitOp(vm.OP_CALL_KW, uint8(totalArgs), idx)
	} else {
		c.emitOp(vm.OP_CALL, uint8(len(posArgs)), vm.CallFromStack)
	}

	return nil
}

// compTargetNames lists the identifiers a comprehension's clauses bind.
func (c *Compiler) compTargetNames(idx uint32) []string {
	var names []string
	seen := map[string]bool{}
	var walk func(id ExprID)
	walk = func(id ExprID) {
		e := c.arena.GetExpr(id)
		if e == nil {
			return
		}
		switch e.Kind {
		case ExprKindName:
			n := string(c.src[e.Start:e.End])
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		case ExprKindStarred:
			walk(e.Left)
		case ExprKindTuple, ExprKindList:
			slice := c.arena.ExprSlices[e.Extra]
			for _, sub := range c.arena.ExprIDs[slice.Start : slice.Start+slice.Count] {
				walk(sub)
			}
		}
	}
	for {
		comp := &c.arena.Comps[idx]
		walk(comp.Target)
		if comp.Next == 0 {
			break
		}
		idx = comp.Next - 1
	}
	return names
}

// compSaveLocals copies the enclosing function's value of every local the
// comprehension is about to rebind into its hidden slot. The copy is raw, so
// an unbound local stays unbound. It returns the matching restore pairs.
func (c *Compiler) compSaveLocals(idx uint32) (restore func()) {
	if c.scope == nil || c.scope.Kind != ScopeFunction {
		return func() {}
	}
	type pair struct{ from, to uint16 }
	var pairs []pair
	for _, name := range c.compTargetNames(idx) {
		sym := c.scope.Symbols[name]
		hidden := c.scope.Symbols[compSaveName(idx, name)]
		if sym == nil || hidden == nil || sym.Storage != StorageFastLocal || hidden.Storage != StorageFastLocal {
			continue
		}
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(sym.Offset))
		c.emitOp(vm.OP_STORE_FAST, 0, uint16(hidden.Offset))
		pairs = append(pairs, pair{uint16(hidden.Offset), uint16(sym.Offset)})
	}
	return func() {
		for _, p := range pairs {
			c.emitOp(vm.OP_LOAD_FAST, 0, p.from)
			c.emitOp(vm.OP_STORE_FAST, 0, p.to)
		}
	}
}

func (c *Compiler) compileListComp(expr *ASTExpr) error {
	restore := c.compSaveLocals(expr.Extra)
	defer restore()
	// 1. Build initial empty list
	c.emitOp(vm.OP_BUILD_LIST, 0, 0)

	var compileClause func(currIdx uint32, depth int) error
	compileClause = func(currIdx uint32, depth int) error {
		curr := &c.arena.Comps[currIdx]
		if curr.IsAsync {
			if err := c.compileExpr(curr.Iter); err != nil {
				return err
			}
			_, aiterIdx := c.constPool.InternName("__aiter__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, aiterIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)

			iterSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(iterSlot))

			loopStart := len(c.code)

			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(iterSlot))
			_, anextIdx := c.constPool.InternName("__anext__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, anextIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)
			c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)

			anextDepth := c.stackDepth
			anextStart := len(c.code)
			c.emitOp(vm.OP_AWAIT, 0, 0)
			anextEnd := len(c.code)

			if err := c.compileStoreTarget(curr.Target); err != nil {
				return err
			}

			var ifSkips []int
			for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
				if err := c.compileExpr(ifCond); err != nil {
					return err
				}
				ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
			}

			if curr.Next != 0 {
				if err := compileClause(curr.Next-1, depth+1); err != nil {
					return err
				}
			} else {
				if err := c.compileExpr(expr.Left); err != nil {
					return err
				}
				c.emitOp(vm.OP_LIST_APPEND, 0, uint16(depth+1))
			}

			c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

			excHandlerIP := len(c.code)
			c.addExceptionRange(anextStart, anextEnd, excHandlerIP, anextDepth, false)

			c.emitOp(vm.OP_DUP_TOP, 0, 0)
			if err := c.compileNameLoad("StopAsyncIteration"); err != nil {
				return err
			}
			c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
			jumpNotMatch := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

			c.emitOp(vm.OP_POP_TOP, 0, 0)
			jumpAfterLoop := c.emitOp(vm.OP_JUMP, 0, 0)

			c.patchJump(jumpNotMatch, len(c.code))
			c.emitOp(vm.OP_RAISE, 1, 0)

			c.patchJump(jumpAfterLoop, len(c.code))
			for _, skip := range ifSkips {
				c.patchJump(skip, loopStart)
			}
			return nil
		}

		if err := c.compileExpr(curr.Iter); err != nil {
			return err
		}
		c.emitOp(vm.OP_GET_ITER, 0, 0)

		loopStart := len(c.code)
		jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

		if err := c.compileStoreTarget(curr.Target); err != nil {
			return err
		}

		var ifSkips []int
		for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
			if err := c.compileExpr(ifCond); err != nil {
				return err
			}
			ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		if curr.Next != 0 {
			if err := compileClause(curr.Next-1, depth+1); err != nil {
				return err
			}
		} else {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			c.emitOp(vm.OP_LIST_APPEND, 0, uint16(depth+1))
		}

		c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))
		for _, skip := range ifSkips {
			c.patchJump(skip, loopStart)
		}
		c.patchJump(jumpEnd, len(c.code))
		return nil
	}

	return compileClause(expr.Extra, 1)
}

func (c *Compiler) compileDictComp(expr *ASTExpr) error {
	restore := c.compSaveLocals(expr.Extra)
	defer restore()
	// 1. Build initial empty dict
	c.emitOp(vm.OP_BUILD_MAP, 0, 0)

	var compileClause func(currIdx uint32, depth int) error
	compileClause = func(currIdx uint32, depth int) error {
		curr := &c.arena.Comps[currIdx]
		if curr.IsAsync {
			if err := c.compileExpr(curr.Iter); err != nil {
				return err
			}
			_, aiterIdx := c.constPool.InternName("__aiter__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, aiterIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)

			iterSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(iterSlot))

			loopStart := len(c.code)

			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(iterSlot))
			_, anextIdx := c.constPool.InternName("__anext__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, anextIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)
			c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)

			anextDepth := c.stackDepth
			anextStart := len(c.code)
			c.emitOp(vm.OP_AWAIT, 0, 0)
			anextEnd := len(c.code)

			if err := c.compileStoreTarget(curr.Target); err != nil {
				return err
			}

			var ifSkips []int
			for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
				if err := c.compileExpr(ifCond); err != nil {
					return err
				}
				ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
			}

			if curr.Next != 0 {
				if err := compileClause(curr.Next-1, depth+1); err != nil {
					return err
				}
			} else {
				if err := c.compileExpr(expr.Left); err != nil {
					return err
				}
				c.emitOp(vm.OP_LIST_APPEND, 0, uint16(depth+1))
			}

			c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

			excHandlerIP := len(c.code)
			c.addExceptionRange(anextStart, anextEnd, excHandlerIP, anextDepth, false)

			c.emitOp(vm.OP_DUP_TOP, 0, 0)
			if err := c.compileNameLoad("StopAsyncIteration"); err != nil {
				return err
			}
			c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
			jumpNotMatch := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

			c.emitOp(vm.OP_POP_TOP, 0, 0)
			jumpAfterLoop := c.emitOp(vm.OP_JUMP, 0, 0)

			c.patchJump(jumpNotMatch, len(c.code))
			c.emitOp(vm.OP_RAISE, 1, 0)

			c.patchJump(jumpAfterLoop, len(c.code))
			for _, skip := range ifSkips {
				c.patchJump(skip, loopStart)
			}
			return nil
		}

		if err := c.compileExpr(curr.Iter); err != nil {
			return err
		}
		c.emitOp(vm.OP_GET_ITER, 0, 0)

		loopStart := len(c.code)
		jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

		if err := c.compileStoreTarget(curr.Target); err != nil {
			return err
		}

		var ifSkips []int
		for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
			if err := c.compileExpr(ifCond); err != nil {
				return err
			}
			ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		if curr.Next != 0 {
			if err := compileClause(curr.Next-1, depth+1); err != nil {
				return err
			}
		} else {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			if err := c.compileExpr(expr.Right); err != nil {
				return err
			}
			c.emitOp(vm.OP_MAP_ADD, 0, uint16(depth+2))
		}

		c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))
		for _, skip := range ifSkips {
			c.patchJump(skip, loopStart)
		}
		c.patchJump(jumpEnd, len(c.code))
		return nil
	}

	return compileClause(expr.Extra, 1)
}

// peephole optimizes bytecode by eliminating redundant jumps.
func (c *Compiler) peephole() {
	code := c.code
	for i := 0; i < len(code); i++ {
		op := vm.DecodeOpcode(code[i])
		if op == vm.OP_JUMP {
			target := int(vm.DecodeArgBx(code[i]))
			if target == i+1 {
				// JUMP to next instruction is a no-op
				code[i] = vm.Encode(vm.OP_NOP, 0, 0)
			} else if target < len(code) && vm.DecodeOpcode(code[target]) == vm.OP_JUMP {
				// Jump to jump chaining
				nextTarget := vm.DecodeArgBx(code[target])
				code[i] = vm.Encode(op, 0, nextTarget)
			}
		}
	}
}

func decodeTemplateLiteral(s string, raw bool) string {
	if s == "{{" || s == "}}" {
		return s[:1]
	}
	return decodeFStringPart(s, raw)
}

func (c *Compiler) compileTemplate(fs *ASTFString) error {
	pieces := c.arena.GetExprSlice(fs.Pieces)
	n := 0
	for _, id := range pieces {
		if e := c.arena.GetExpr(id); e != nil && e.Kind == ExprKindFormattedValue {
			n++
		}
	}
	i := 0
	emitRun := func() {
		var b strings.Builder
		for i < len(pieces) {
			e := c.arena.GetExpr(pieces[i])
			if e == nil || e.Kind != ExprKindString {
				break
			}
			b.WriteString(decodeTemplateLiteral(string(c.src[e.Start:e.End]), fs.Raw))
			i++
		}
		idx := c.constPool.Add(val.NewStringVal(b.String()))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)
	}
	emitRun()
	for i < len(pieces) {
		e := c.arena.GetExpr(pieces[i])
		if e == nil || e.Kind != ExprKindFormattedValue {
			return fmt.Errorf("compiler: malformed template string")
		}
		i++
		fv := c.arena.GetFormattedValue(e.Extra)
		if fv == nil {
			return fmt.Errorf("compiler: missing template interpolation")
		}
		if err := c.compileExpr(fv.Value); err != nil {
			return err
		}
		exprIdx := c.constPool.Add(val.NewStringVal(fv.Expression))
		c.emitOp(vm.OP_LOAD_CONST, 0, exprIdx)
		if fv.Conversion == 0 {
			noneIdx := c.constPool.Add(val.NoneVal())
			c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
		} else {
			convIdx := c.constPool.Add(val.NewStringVal(string([]byte{fv.Conversion})))
			c.emitOp(vm.OP_LOAD_CONST, 0, convIdx)
		}
		if fv.FormatSpec != ExprNone {
			if err := c.compileExpr(fv.FormatSpec); err != nil {
				return err
			}
		} else {
			specIdx := c.constPool.Add(val.NewStringVal(""))
			c.emitOp(vm.OP_LOAD_CONST, 0, specIdx)
		}
		emitRun()
	}
	c.emitOp(vm.OP_BUILD_TEMPLATE, 0, uint16(n))
	return nil
}

func parseStringLiteral(s string) string { return decodeStringToken(s) }

func parseBytesLiteral(s string) []byte { return decodeBytesToken(s) }

func (c *Compiler) compileMatchStmt(stmt *ASTStmt) error {
	matchStmt := c.arena.GetMatchStmt(stmt.Extra)
	if matchStmt == nil {
		return nil
	}

	if err := c.compileExpr(matchStmt.Subject); err != nil {
		return err
	}
	subjSlot := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(subjSlot))

	cases := c.arena.GetMatchCaseSlice(matchStmt.Cases)
	var endJumps []int

	for _, caseItem := range cases {
		var failJumps []int

		if err := c.compilePatternInSlot(caseItem.Pattern, subjSlot, &failJumps); err != nil {
			return err
		}

		if caseItem.Guard != ExprNone {
			if err := c.compileExpr(caseItem.Guard); err != nil {
				return err
			}
			failJumps = append(failJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		for _, sID := range c.arena.GetStmtSliceDirect(caseItem.Body) {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}

		endJumps = append(endJumps, c.emitOp(vm.OP_JUMP, 0, 0))

		nextCaseTarget := len(c.code)
		for _, fj := range failJumps {
			c.patchJump(fj, nextCaseTarget)
		}
	}

	endTarget := len(c.code)
	for _, ej := range endJumps {
		c.patchJump(ej, endTarget)
	}

	return nil
}

func (c *Compiler) compilePatternInSlot(patID PatternID, subjSlot int, failJumps *[]int) error {
	pat := c.arena.GetPattern(patID)
	if pat == nil {
		return nil
	}

	switch pat.Kind {
	case PatternKindWildcard:
		// _ matches everything

	case PatternKindCapture:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		return c.compileNameStore(pat.Name)

	case PatternKindValue:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		if err := c.compileExpr(pat.Expr1); err != nil {
			return err
		}
		c.emitOp(vm.OP_COMPARE_OP, vm.CMP_EQ, 0)
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))

	case PatternKindSingleton:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		if err := c.compileExpr(pat.Expr1); err != nil {
			return err
		}
		c.emitOp(vm.OP_COMPARE_OP, vm.CMP_IS, 0)
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))

	case PatternKindAs:
		if err := c.compilePatternInSlot(PatternID(pat.Expr1), subjSlot, failJumps); err != nil {
			return err
		}
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		return c.compileNameStore(pat.Name)

	case PatternKindOr:
		subPats := c.arena.GetPatternSlice(PatternSlice{Start: pat.Extra, Count: uint32(pat.Expr1)})
		if len(subPats) == 0 {
			return nil
		}
		var orSuccessJumps []int
		for i, subPatID := range subPats {
			if i < len(subPats)-1 {
				var subFailJumps []int
				if err := c.compilePatternInSlot(subPatID, subjSlot, &subFailJumps); err != nil {
					return err
				}
				orSuccessJumps = append(orSuccessJumps, c.emitOp(vm.OP_JUMP, 0, 0))
				nextSubTarget := len(c.code)
				for _, fj := range subFailJumps {
					c.patchJump(fj, nextSubTarget)
				}
			} else {
				if err := c.compilePatternInSlot(subPatID, subjSlot, failJumps); err != nil {
					return err
				}
			}
		}
		orTarget := len(c.code)
		for _, sj := range orSuccessJumps {
			c.patchJump(sj, orTarget)
		}

	case PatternKindSequence:
		// 1. Check if sequence
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		c.emitOp(vm.OP_MATCH_SEQUENCE, 0, 0)
		jumpIsSeq := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)
		c.emitOp(vm.OP_POP_TOP, 0, 0)
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		c.patchJump(jumpIsSeq, len(c.code))
		c.emitOp(vm.OP_POP_TOP, 0, 0)

		// 2. Length check
		subPats := c.arena.GetPatternSlice(PatternSlice{Start: pat.Extra, Count: uint32(pat.Expr1)})
		_, lenIdx := c.constPool.InternName("len")
		c.emitOp(vm.OP_LOAD_GLOBAL, 0, lenIdx)
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
		if pat.StarIdx < 0 {
			lenConst := c.constPool.Add(val.Int64Val(int64(len(subPats))))
			c.emitOp(vm.OP_LOAD_CONST, 0, lenConst)
			c.emitOp(vm.OP_COMPARE_OP, vm.CMP_EQ, 0)
			*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		} else {
			lenConst := c.constPool.Add(val.Int64Val(int64(len(subPats) - 1)))
			c.emitOp(vm.OP_LOAD_CONST, 0, lenConst)
			c.emitOp(vm.OP_COMPARE_OP, vm.CMP_GTE, 0)
			*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		// 3. Subpatterns
		for i, subPatID := range subPats {
			if int32(i) == pat.StarIdx {
				c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
				startConst := c.constPool.Add(val.Int64Val(int64(i)))
				c.emitOp(vm.OP_LOAD_CONST, 0, startConst)
				afterCount := len(subPats) - 1 - i
				if afterCount > 0 {
					endConst := c.constPool.Add(val.Int64Val(int64(-afterCount)))
					c.emitOp(vm.OP_LOAD_CONST, 0, endConst)
				} else {
					endConst := c.constPool.Add(val.NoneVal())
					c.emitOp(vm.OP_LOAD_CONST, 0, endConst)
				}
				c.emitOp(vm.OP_BUILD_SLICE, 0, 2)
				c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
				subSlot := c.allocTempLocal()
				c.emitOp(vm.OP_STORE_FAST, 0, uint16(subSlot))
				if err := c.compilePatternInSlot(subPatID, subSlot, failJumps); err != nil {
					return err
				}
			} else {
				idx := i
				if pat.StarIdx >= 0 && int32(i) > pat.StarIdx {
					idx = -(len(subPats) - i)
				}
				c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
				idxConst := c.constPool.Add(val.Int64Val(int64(idx)))
				c.emitOp(vm.OP_LOAD_CONST, 0, idxConst)
				c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
				subSlot := c.allocTempLocal()
				c.emitOp(vm.OP_STORE_FAST, 0, uint16(subSlot))
				if err := c.compilePatternInSlot(subPatID, subSlot, failJumps); err != nil {
					return err
				}
			}
		}

	case PatternKindMapping:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		c.emitOp(vm.OP_MATCH_MAPPING, 0, 0)
		jumpIsMap := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)
		c.emitOp(vm.OP_POP_TOP, 0, 0)
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		c.patchJump(jumpIsMap, len(c.code))
		c.emitOp(vm.OP_POP_TOP, 0, 0)

		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		keys := c.arena.GetExprSlice(ExprSlice{Start: uint32(pat.Expr1), Count: uint32(pat.Expr2)})
		for _, k := range keys {
			if err := c.compileExpr(k); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(keys)))
		c.emitOp(vm.OP_MATCH_KEYS, 0, 0)

		jumpSuccess := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)
		c.emitOp(vm.OP_POP_TOP, 0, 0) // pop None
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		c.patchJump(jumpSuccess, len(c.code))

		tupleSlot := c.allocTempLocal()
		c.emitOp(vm.OP_STORE_FAST, 0, uint16(tupleSlot))

		subPats := c.arena.GetPatternSlice(PatternSlice{Start: pat.Extra, Count: uint32(pat.Expr2)})
		for i, subPatID := range subPats {
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tupleSlot))
			idxConst := c.constPool.Add(val.Int64Val(int64(i)))
			c.emitOp(vm.OP_LOAD_CONST, 0, idxConst)
			c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
			subSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(subSlot))
			if err := c.compilePatternInSlot(subPatID, subSlot, failJumps); err != nil {
				return err
			}
		}

		if pat.Name != "" {
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
			if err := c.compileNameStore(pat.Name); err != nil {
				return err
			}
		}

	case PatternKindClass:
		posCount := uint16(uint32(pat.Expr2) >> 16)
		kwCount := uint16(uint32(pat.Expr2) & 0xFFFF)
		posStart := pat.Extra
		kwStart := posStart + uint32(posCount)
		kwNamesStart := uint32(pat.StarIdx)
		kwNames := c.arena.Idents[kwNamesStart : kwNamesStart+uint32(kwCount)]

		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(subjSlot))
		if err := c.compileExpr(pat.Expr1); err != nil {
			return err
		}
		for _, kwName := range kwNames {
			c.emitOp(vm.OP_LOAD_CONST, 0, c.constPool.Add(val.InternString(kwName)))
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, kwCount)
		c.emitOp(vm.OP_MATCH_CLASS, uint8(posCount), 0)

		jumpSuccess := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)
		c.emitOp(vm.OP_POP_TOP, 0, 0) // pop None
		*failJumps = append(*failJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		c.patchJump(jumpSuccess, len(c.code))

		tupleSlot := c.allocTempLocal()
		c.emitOp(vm.OP_STORE_FAST, 0, uint16(tupleSlot))

		posPats := c.arena.PatternIDs[posStart : posStart+uint32(posCount)]
		for i, subPatID := range posPats {
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tupleSlot))
			idxConst := c.constPool.Add(val.Int64Val(int64(i)))
			c.emitOp(vm.OP_LOAD_CONST, 0, idxConst)
			c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
			subSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(subSlot))
			if err := c.compilePatternInSlot(subPatID, subSlot, failJumps); err != nil {
				return err
			}
		}

		kwPats := c.arena.PatternIDs[kwStart : kwStart+uint32(kwCount)]
		for i, subPatID := range kwPats {
			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tupleSlot))
			idxConst := c.constPool.Add(val.Int64Val(int64(int(posCount) + i)))
			c.emitOp(vm.OP_LOAD_CONST, 0, idxConst)
			c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)
			subSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(subSlot))
			if err := c.compilePatternInSlot(subPatID, subSlot, failJumps); err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Compiler) compileTypeAlias(stmt *ASTStmt) error {
	ta := &c.arena.TypeAliases[stmt.Extra]

	tpCount := uint16(ta.TypeParams.Count)
	if tpCount > 0 {
		tps := c.arena.TypeParams[ta.TypeParams.Start : ta.TypeParams.Start+uint32(tpCount)]
		for _, tp := range tps {
			typeName := "TypeVar"
			if tp.Kind == TypeParamTypeVarTuple {
				typeName = "TypeVarTuple"
			} else if tp.Kind == TypeParamParamSpec {
				typeName = "ParamSpec"
			}
			_, clsIdx := c.constPool.InternName(typeName)
			c.emitOp(vm.OP_LOAD_GLOBAL, 0, clsIdx)
			pNameIdx := c.constPool.Add(val.InternString(tp.Name))
			c.emitOp(vm.OP_LOAD_CONST, 0, pNameIdx)
			if tp.Bound != ExprNone {
				if err := c.compileExpr(tp.Bound); err != nil {
					return err
				}
				c.emitOp(vm.OP_CALL, 2, vm.CallFromStack)
			} else {
				c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
			}
			if err := c.compileNameStore(tp.Name); err != nil {
				return err
			}
		}
	}

	// Load TypeAliasType
	_, tatIdx := c.constPool.InternName("TypeAliasType")
	c.emitOp(vm.OP_LOAD_GLOBAL, 0, tatIdx)

	// Arg 1: name
	nameIdx := c.constPool.Add(val.InternString(ta.Name))
	c.emitOp(vm.OP_LOAD_CONST, 0, nameIdx)

	// Arg 2: value
	if err := c.compileExpr(ta.Value); err != nil {
		return err
	}

	// Arg 3: type_params tuple
	if tpCount > 0 {
		tps := c.arena.TypeParams[ta.TypeParams.Start : ta.TypeParams.Start+uint32(tpCount)]
		for _, tp := range tps {
			if err := c.compileNameLoad(tp.Name); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, tpCount)
	} else {
		c.emitOp(vm.OP_BUILD_TUPLE, 0, 0)
	}

	// Call TypeAliasType(name, value, type_params)
	c.emitOp(vm.OP_CALL, 3, vm.CallFromStack)

	// Store into alias name
	return c.compileNameStore(ta.Name)
}

func (c *Compiler) compileAssertStmt(stmt *ASTStmt) error {
	if c.optimize >= 1 {
		return nil
	}
	if err := c.compileExpr(stmt.Expr1); err != nil {
		return err
	}
	jumpPass := c.emitOp(vm.OP_JUMP_IF_TRUE, 0, 0)
	_, errIdx := c.constPool.InternName("AssertionError")
	c.emitOp(vm.OP_LOAD_GLOBAL, 0, errIdx)
	if stmt.Expr2 != ExprNone {
		if err := c.compileExpr(stmt.Expr2); err != nil {
			return err
		}
		c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)
	} else {
		c.emitOp(vm.OP_CALL, 0, vm.CallFromStack)
	}
	c.emitOp(vm.OP_RAISE, 1, 0)
	c.patchJump(jumpPass, len(c.code))
	return nil
}

func (c *Compiler) compileDeleteStmt(stmt *ASTStmt) error {
	return c.compileDeleteTarget(stmt.Expr1)
}

func (c *Compiler) compileDeleteTarget(targetID ExprID) error {
	expr := c.arena.GetExpr(targetID)
	if expr == nil {
		return nil
	}
	switch expr.Kind {
	case ExprKindName:
		name := string(c.src[expr.Start:expr.End])
		sym := c.scope.Symbols[name]
		if sym != nil && sym.Storage == StorageFastLocal {
			c.emitOp(vm.OP_DELETE_FAST, 0, uint16(sym.Offset))
		} else if c.scope.Kind == ScopeClass || (c.scope.Kind == ScopeModule && !c.scope.ExplicitG[name]) {
			_, idx := c.constPool.InternName(name)
			c.emitOp(vm.OP_DELETE_NAME, 0, idx)
		} else {
			_, idx := c.constPool.InternName(name)
			c.emitOp(vm.OP_DELETE_GLOBAL, 0, idx)
		}
	case ExprKindAttribute:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		attrName := string(c.src[expr.Start:expr.End])
		_, idx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_DELETE_ATTR, 0, idx)
	case ExprKindSubscript:
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_DELETE_SUBSCR, 0, 0)
	case ExprKindTuple, ExprKindList:
		slice := c.arena.ExprSlices[expr.Extra]
		for _, elem := range c.arena.GetExprSlice(slice) {
			if err := c.compileDeleteTarget(elem); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Compiler) compileSetComp(expr *ASTExpr) error {
	restore := c.compSaveLocals(expr.Extra)
	defer restore()
	// 1. Build initial empty set
	c.emitOp(vm.OP_BUILD_SET, 0, 0)

	var compileClause func(currIdx uint32, depth int) error
	compileClause = func(currIdx uint32, depth int) error {
		curr := &c.arena.Comps[currIdx]
		if curr.IsAsync {
			if err := c.compileExpr(curr.Iter); err != nil {
				return err
			}
			_, aiterIdx := c.constPool.InternName("__aiter__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, aiterIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)

			iterSlot := c.allocTempLocal()
			c.emitOp(vm.OP_STORE_FAST, 0, uint16(iterSlot))

			loopStart := len(c.code)

			c.emitOp(vm.OP_LOAD_FAST, 0, uint16(iterSlot))
			_, anextIdx := c.constPool.InternName("__anext__")
			c.emitOp(vm.OP_LOAD_METHOD, 0, anextIdx)
			c.emitOp(vm.OP_CALL_METHOD, 0, 0)
			c.emitOp(vm.OP_GET_AWAITABLE, 0, 0)

			anextDepth := c.stackDepth
			anextStart := len(c.code)
			c.emitOp(vm.OP_AWAIT, 0, 0)
			anextEnd := len(c.code)

			if err := c.compileStoreTarget(curr.Target); err != nil {
				return err
			}

			var ifSkips []int
			for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
				if err := c.compileExpr(ifCond); err != nil {
					return err
				}
				ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
			}

			if curr.Next != 0 {
				if err := compileClause(curr.Next-1, depth+1); err != nil {
					return err
				}
			} else {
				if err := c.compileExpr(expr.Left); err != nil {
					return err
				}
				c.emitOp(vm.OP_LIST_APPEND, 0, uint16(depth+1))
			}

			c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

			excHandlerIP := len(c.code)
			c.addExceptionRange(anextStart, anextEnd, excHandlerIP, anextDepth, false)

			c.emitOp(vm.OP_DUP_TOP, 0, 0)
			if err := c.compileNameLoad("StopAsyncIteration"); err != nil {
				return err
			}
			c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
			jumpNotMatch := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

			c.emitOp(vm.OP_POP_TOP, 0, 0)
			jumpAfterLoop := c.emitOp(vm.OP_JUMP, 0, 0)

			c.patchJump(jumpNotMatch, len(c.code))
			c.emitOp(vm.OP_RAISE, 1, 0)

			c.patchJump(jumpAfterLoop, len(c.code))
			for _, skip := range ifSkips {
				c.patchJump(skip, loopStart)
			}
			return nil
		}

		if err := c.compileExpr(curr.Iter); err != nil {
			return err
		}
		c.emitOp(vm.OP_GET_ITER, 0, 0)

		loopStart := len(c.code)
		jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

		if err := c.compileStoreTarget(curr.Target); err != nil {
			return err
		}

		var ifSkips []int
		for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
			if err := c.compileExpr(ifCond); err != nil {
				return err
			}
			ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		if curr.Next != 0 {
			if err := compileClause(curr.Next-1, depth+1); err != nil {
				return err
			}
		} else {
			if err := c.compileExpr(expr.Left); err != nil {
				return err
			}
			c.emitOp(vm.OP_SET_ADD, 0, uint16(depth+1))
		}

		c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))
		for _, skip := range ifSkips {
			c.patchJump(skip, loopStart)
		}
		c.patchJump(jumpEnd, len(c.code))
		return nil
	}

	return compileClause(expr.Extra, 1)
}

func (c *Compiler) compileGenExp(expr *ASTExpr) error {
	comp := &c.arena.Comps[expr.Extra]

	// 1. Evaluate iter in enclosing frame and store to temp local
	if err := c.compileExpr(comp.Iter); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)
	iterSlot := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(iterSlot))

	// 2. Find child scope for genexpr
	genScope := c.nextChildScope(ScopeFunction, "<genexpr>")

	// 3. Subcompiler for genexpr
	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     genScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 32),
		optimize:  c.optimize,
	}

	// Load parameter 0 (.0)
	subCompiler.emitOp(vm.OP_LOAD_FAST, 0, 0)

	var compileClause func(currIdx uint32) error
	compileClause = func(currIdx uint32) error {
		curr := &c.arena.Comps[currIdx]
		loopStart := len(subCompiler.code)
		jumpEnd := subCompiler.emitOp(vm.OP_FOR_ITER, 0, 0)

		if err := subCompiler.compileStoreTarget(curr.Target); err != nil {
			return err
		}

		var ifSkips []int
		for _, ifCond := range c.arena.GetExprSlice(curr.IfConds) {
			if err := subCompiler.compileExpr(ifCond); err != nil {
				return err
			}
			ifSkips = append(ifSkips, subCompiler.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
		}

		if curr.Next != 0 {
			nextIdx := curr.Next - 1
			nextComp := &c.arena.Comps[nextIdx]
			if err := subCompiler.compileExpr(nextComp.Iter); err != nil {
				return err
			}
			subCompiler.emitOp(vm.OP_GET_ITER, 0, 0)
			if err := compileClause(nextIdx); err != nil {
				return err
			}
		} else {
			if err := subCompiler.compileExpr(expr.Left); err != nil {
				return err
			}
			subCompiler.emitOp(vm.OP_YIELD_VALUE, 0, 0)
			subCompiler.emitOp(vm.OP_POP_TOP, 0, 0)
		}

		subCompiler.emitOp(vm.OP_JUMP, 0, uint16(loopStart))
		for _, skip := range ifSkips {
			subCompiler.patchJump(skip, loopStart)
		}
		subCompiler.patchJump(jumpEnd, len(subCompiler.code))
		return nil
	}

	if err := compileClause(expr.Extra); err != nil {
		return err
	}
	subCompiler.emitReturnNone()
	subCompiler.peephole()

	paramNames := []val.Value{val.InternString(".0")}
	numLocals := frameLocalCount(genScope)
	if numLocals < 1 {
		numLocals = 1
	}

	cellValSlice := make([]val.Value, len(genScope.CellVars))
	for i, cv := range genScope.CellVars {
		cellValSlice[i] = val.InternString(cv)
	}

	freeValSlice := make([]val.Value, len(genScope.FreeVars))
	for i, fv := range genScope.FreeVars {
		freeValSlice[i] = val.InternString(fv)
	}

	genCodeObj := &vm.CodeObject{
		Name:         "<genexpr>",
		Code:         subCompiler.code,
		Consts:       subCompiler.constPool.Slice(),
		ParamNames:   paramNames,
		CellVars:     cellValSlice,
		FreeVars:     freeValSlice,
		NumLocals:    numLocals,
		NumParams:    1,
		MaxStackSize: numLocals + subCompiler.maxStackDepth + 16,
		IsGenerator:  true,
		Flags:        vm.FlagGenerator,
	}

	var flags uint8
	if len(genScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range genScope.FreeVars {
			if err := c.emitLoadClosure(fv); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(genScope.FreeVars)))
	}

	codeIdx := c.constPool.Add(vm.NewCodeVal(genCodeObj))
	c.emitOp(vm.OP_LOAD_CONST, 0, codeIdx)
	c.emitOp(vm.OP_MAKE_FUNCTION, flags, 0)
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(iterSlot))
	c.emitOp(vm.OP_CALL, 1, vm.CallFromStack)

	return nil
}

func (c *Compiler) compileLambda(expr *ASTExpr) error {
	fnDef := &c.arena.FuncDefs[expr.Extra]
	defaults := c.arena.GetExprSlice(fnDef.Defaults)
	numDefaults := len(defaults)
	var flags uint8
	if numDefaults > 0 {
		flags |= 0x02
		for _, dExpr := range defaults {
			if err := c.compileExpr(dExpr); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(numDefaults))
	}

	lambdaScope := c.nextChildScope(ScopeFunction, "<lambda>")

	firstLine, _ := c.offsetToLineCol(expr.Start)
	lambdaQualName := c.qualnamePrefix + "<lambda>"

	subCompiler := &Compiler{
		src:            c.src,
		arena:          c.arena,
		scope:          lambdaScope,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 16),
		filename:       c.filename,
		qualnamePrefix: lambdaQualName,
		lineStarts:     c.lineStarts,
		curLine:        firstLine,
		curCol:         1,
		optimize:       c.optimize,
	}

	for _, sID := range c.arena.StmtIDs[fnDef.Body.Start : fnDef.Body.Start+fnDef.Body.Count] {
		if err := subCompiler.compileStmt(sID); err != nil {
			return err
		}
	}
	subCompiler.emitReturnNone()
	subCompiler.peephole()

	paramNames := lambdaScope.ParamNames
	paramValSlice := make([]val.Value, len(paramNames))
	for i, p := range paramNames {
		paramValSlice[i] = val.InternString(p)
	}

	cellValSlice := make([]val.Value, len(lambdaScope.CellVars))
	for i, cv := range lambdaScope.CellVars {
		cellValSlice[i] = val.InternString(cv)
	}

	freeValSlice := make([]val.Value, len(lambdaScope.FreeVars))
	for i, fv := range lambdaScope.FreeVars {
		freeValSlice[i] = val.InternString(fv)
	}

	numLocals := frameLocalCount(lambdaScope)
	if numLocals < len(paramNames) {
		numLocals = len(paramNames)
	}

	var coFlags uint32 = vm.CO_OPTIMIZED | vm.CO_NEWLOCALS
	if len(lambdaScope.FreeVars) == 0 && len(lambdaScope.CellVars) == 0 {
		coFlags |= vm.CO_NOFREE
	}
	if c.qualnamePrefix != "" {
		coFlags |= vm.CO_NESTED
	}

	varNames := make([]val.Value, 0, len(paramNames)+len(lambdaScope.FastLocals))
	seenNames := make(map[string]bool)
	for _, p := range paramNames {
		clean := p
		if strings.HasPrefix(clean, "**") {
			clean = clean[2:]
			coFlags |= vm.CO_VARKEYWORDS
		} else if strings.HasPrefix(clean, "*") {
			clean = clean[1:]
			if clean != "" {
				coFlags |= vm.CO_VARARGS
			}
		}
		if clean != "" && !seenNames[clean] {
			seenNames[clean] = true
			varNames = append(varNames, val.InternString(clean))
		}
	}
	for _, fl := range lambdaScope.FastLocals {
		if !seenNames[fl] {
			seenNames[fl] = true
			varNames = append(varNames, val.InternString(fl))
		}
	}

	lambdaCodeObj := &vm.CodeObject{
		Name:         "<lambda>",
		QualName:     lambdaQualName,
		FileName:     c.filename,
		FirstLineNo:  int(firstLine),
		Code:         subCompiler.code,
		Consts:       subCompiler.constPool.Slice(),
		ParamNames:   paramValSlice,
		VarNames:     varNames,
		CellVars:     cellValSlice,
		FreeVars:     freeValSlice,
		NumLocals:    numLocals,
		NumParams:    len(paramNames),
		MaxStackSize: numLocals + subCompiler.maxStackDepth + 16,
		Flags:        coFlags,
		LineSpans:    subCompiler.lineSpans,
	}

	if len(lambdaScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range lambdaScope.FreeVars {
			if err := c.emitLoadClosure(fv); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(lambdaScope.FreeVars)))
	}

	coVal := vm.NewCodeVal(lambdaCodeObj)
	coIdx := c.constPool.Add(coVal)
	c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
	c.emitOp(vm.OP_MAKE_FUNCTION, flags, 0)

	return nil
}

// emitModuleAnnotatePrologue installs the module's deferred __annotate__ (and
// the __conditional_annotations__ set) before any other module code runs.
func (c *Compiler) emitModuleAnnotatePrologue() error {
	if err := c.emitAnnotateFunction(); err != nil {
		return err
	}
	if c.scope.AnnHasCond {
		c.emitOp(vm.OP_BUILD_SET, 0, 0)
		return c.compileNameStore(annCondAnnotations)
	}
	return nil
}

// emitAnnotateFunction synthesizes `__annotate__(format)` for the module or
// class body being compiled (c.scope) and stores it under __annotate__ (module)
// or __annotate_func__ (class). PEP 649/749: only VALUE (1) and
// VALUE_WITH_FAKE_GLOBALS (2) are supported natively; larger formats raise
// NotImplementedError so annotationlib can emulate STRING and FORWARDREF.
func (c *Compiler) emitAnnotateFunction() error {
	owner := c.scope
	as := owner.AnnScope
	if as == nil || len(owner.AnnEntries) == 0 {
		return nil
	}
	isClass := owner.Kind == ScopeClass

	qualPrefix := c.qualnamePrefix
	sub := &Compiler{
		src:            c.src,
		arena:          c.arena,
		scope:          as,
		constPool:      NewConstPool(),
		code:           make([]uint32, 0, 32),
		filename:       c.filename,
		qualnamePrefix: qualPrefix + "__annotate__.<locals>.",
		lineStarts:     c.lineStarts,
		curLine:        c.curLine,
		curCol:         1,
		optimize:       c.optimize,
	}

	sub.emitOp(vm.OP_LOAD_FAST, 0, 0)
	sub.emitOp(vm.OP_LOAD_CONST, 0, sub.constPool.Add(val.Int64Val(2)))
	sub.emitOp(vm.OP_COMPARE_OP, vm.CMP_GT, 0)
	skip := sub.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
	_, niIdx := sub.constPool.InternName("NotImplementedError")
	sub.emitOp(vm.OP_LOAD_GLOBAL, 0, niIdx)
	sub.emitOp(vm.OP_RAISE, 1, 0)
	sub.patchJump(skip, len(sub.code))

	sub.emitOp(vm.OP_BUILD_MAP, 0, 0)
	for _, e := range owner.AnnEntries {
		sub.setSourcePos(c.arena.GetExpr(e.Expr).Start)
		skipEntry := -1
		if e.Cond {
			sub.emitOp(vm.OP_LOAD_CONST, 0, sub.constPool.Add(val.Int64Val(int64(e.Idx))))
			if err := sub.compileNameLoad(annCondAnnotations); err != nil {
				return err
			}
			sub.emitOp(vm.OP_COMPARE_OP, vm.CMP_IN, 0)
			skipEntry = sub.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
		}
		_, keyIdx := sub.constPool.InternName(e.Name)
		sub.emitOp(vm.OP_LOAD_CONST, 0, keyIdx)
		if err := sub.compileExpr(e.Expr); err != nil {
			return err
		}
		sub.emitOp(vm.OP_MAP_ADD, 0, 2)
		if skipEntry >= 0 {
			sub.patchJump(skipEntry, len(sub.code))
		}
	}
	sub.emitOp(vm.OP_RETURN, 0, 0)
	sub.peephole()

	formatName := val.InternString("format")
	cellVals := make([]val.Value, len(as.CellVars))
	for i, cv := range as.CellVars {
		cellVals[i] = val.InternString(cv)
	}
	freeVals := make([]val.Value, len(as.FreeVars))
	for i, fv := range as.FreeVars {
		freeVals[i] = val.InternString(fv)
	}
	numLocals := frameLocalCount(as)
	if numLocals < 1 {
		numLocals = 1
	}
	flags := uint32(vm.CO_OPTIMIZED | vm.CO_NEWLOCALS)
	if len(as.FreeVars) == 0 && len(as.CellVars) == 0 {
		flags |= vm.CO_NOFREE
	}
	if c.qualnamePrefix != "" {
		flags |= vm.CO_NESTED
	}
	varNames := []val.Value{formatName}
	for _, fl := range as.FastLocals {
		if fl != annFormatParam {
			varNames = append(varNames, val.InternString(fl))
		}
	}
	co := &vm.CodeObject{
		Name:            "__annotate__",
		QualName:        qualPrefix + "__annotate__",
		FileName:        c.filename,
		FirstLineNo:     int(c.curLine),
		PosOnlyArgCount: 1,
		Code:            sub.code,
		Consts:          sub.constPool.Slice(),
		ParamNames:      []val.Value{formatName},
		VarNames:        varNames,
		CellVars:        cellVals,
		FreeVars:        freeVals,
		NumLocals:       numLocals,
		NumParams:       1,
		MaxStackSize:    numLocals + sub.maxStackDepth + 16,
		Flags:           flags,
		ExceptionTable:  sub.exceptionTable,
		LineSpans:       sub.lineSpans,
	}
	coIdx := c.constPool.Add(vm.NewCodeVal(co))

	if len(as.FreeVars) > 0 {
		for _, fv := range as.FreeVars {
			if err := c.emitLoadClosure(fv); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(as.FreeVars)))
		c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
		c.emitOp(vm.OP_MAKE_FUNCTION, 0x01, 0)
	} else {
		c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
		c.emitOp(vm.OP_MAKE_FUNCTION, 0, 0)
	}
	if isClass {
		return c.compileNameStore(annClassAnnotate)
	}
	return c.compileNameStore(annModuleAnnotate)
}
