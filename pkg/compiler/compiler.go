package compiler

import (
	"fmt"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func init() {
	vm.DefaultEngine().SetCompileHook(CompileSource)
}

// LoopContext tracks jump patch locations for break and continue statements.
type LoopContext struct {
	LoopStart int
	Breaks    []int
	Continues []int
	Parent    *LoopContext
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
	err            error
	nextChildIdx   int
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
		src:       src,
		arena:     arena,
		scope:     topScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 64),
	}

	for _, stmtID := range topStmts {
		if err := c.compileStmt(stmtID); err != nil {
			return nil, err
		}
	}

	// Implicit module return None
	c.emitReturnNone()

	c.peephole()

	co := &vm.CodeObject{
		Name:           "<module>",
		Code:           c.code,
		Consts:         c.constPool.Slice(),
		NumLocals:      len(c.scope.FastLocals),
		NumParams:      0,
		MaxStackSize:   c.maxStackDepth + 16,
		ExceptionTable: c.exceptionTable,
	}
	return co, nil
}

// CompileSource compiles Python source bytes using a fresh ASTArena.
func CompileSource(src []byte) (*vm.CodeObject, error) {
	arena := NewASTArena(256)
	return Compile(src, arena)
}

// CompileString compiles a Python source string.
func CompileString(src string) (*vm.CodeObject, error) {
	return CompileSource([]byte(src))
}

func (c *Compiler) emit(inst uint32) int {
	idx := len(c.code)
	c.code = append(c.code, inst)
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

func (c *Compiler) compileStmt(stmtID StmtID) error {
	stmt := c.arena.GetStmt(stmtID)
	if stmt == nil {
		return nil
	}

	switch stmt.Kind {
	case StmtKindExpr:
		if err := c.compileExpr(stmt.Expr1); err != nil {
			return err
		}
		c.emitOp(vm.OP_POP_TOP, 0, 0)

	case StmtKindReturn:
		if stmt.Expr1 != ExprNone {
			if err := c.compileExpr(stmt.Expr1); err != nil {
				return err
			}
		} else {
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
			return c.compileStoreTarget(stmt.Expr1)
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
		jumpIdx := c.emitOp(vm.OP_JUMP, 0, 0)
		c.activeLoop.Breaks = append(c.activeLoop.Breaks, jumpIdx)

	case StmtKindContinue:
		if c.activeLoop == nil {
			return fmt.Errorf("SyntaxError: 'continue' outside loop")
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
		c.emitOp(vm.OP_DUP_TOP, 0, 0)
		attrName := string(c.src[expr.Start:expr.End])
		_, constIdx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_LOAD_ATTR, 0, constIdx)
	case ExprKindSubscript:
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

func (c *Compiler) compileNameLoad(name string) error {
	sym := c.scope.Symbols[name]
	if sym == nil {
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
		return nil
	}
	switch sym.Storage {
	case StorageFastLocal:
		c.emitOp(vm.OP_LOAD_FAST, 0, uint16(sym.Offset))
	case StorageCell, StorageFree:
		c.emitOp(vm.OP_LOAD_DEREF, 0, uint16(sym.Offset))
	case StorageGlobal:
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
	default:
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
	}
	return nil
}

func (c *Compiler) compileNameStore(name string) error {
	sym := c.scope.Symbols[name]
	if sym == nil {
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
		return nil
	}
	switch sym.Storage {
	case StorageFastLocal:
		c.emitOp(vm.OP_STORE_FAST, 0, uint16(sym.Offset))
	case StorageCell, StorageFree:
		c.emitOp(vm.OP_STORE_DEREF, 0, uint16(sym.Offset))
	case StorageGlobal:
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
	default:
		_, idx := c.constPool.InternName(name)
		c.emitOp(vm.OP_STORE_GLOBAL, 0, idx)
	}
	return nil
}

func (c *Compiler) compileIfStmt(stmt *ASTStmt) error {
	if err := c.compileExpr(stmt.Expr1); err != nil {
		return err
	}
	jumpFalse := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)

	// Then body
	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}
	jumpEnd := c.emitOp(vm.OP_JUMP, 0, 0)

	var exitJumps []int
	exitJumps = append(exitJumps, jumpEnd)

	c.patchJump(jumpFalse, len(c.code))

	// Elifs
	elifsStart := stmt.Extra >> 16
	elifsCount := stmt.Extra & 0xFFFF
	for i := uint32(0); i < elifsCount; i++ {
		elif := &c.arena.Elifs[elifsStart+i]
		if err := c.compileExpr(elif.Cond); err != nil {
			return err
		}
		elifFalse := c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
		for _, sID := range c.arena.StmtIDs[elif.Body.Start : elif.Body.Start+elif.Body.Count] {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
		exitJumps = append(exitJumps, c.emitOp(vm.OP_JUMP, 0, 0))
		c.patchJump(elifFalse, len(c.code))
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

func (c *Compiler) compileForStmt(stmt *ASTStmt) error {
	if err := c.compileExpr(stmt.Expr2); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)

	loopStart := len(c.code)
	loopCtx := &LoopContext{
		LoopStart: loopStart,
		Parent:    c.activeLoop,
	}
	c.activeLoop = loopCtx

	forIterJump := c.emitOp(vm.OP_FOR_ITER, 0, 0)

	if err := c.compileStoreTarget(stmt.Expr1); err != nil {
		return err
	}

	for _, sID := range c.arena.GetStmtSlice(stmt.Body) {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}

	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	endTarget := len(c.code)
	c.patchJump(forIterJump, endTarget)

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

func (c *Compiler) allocTempLocal() int {
	offset := len(c.scope.FastLocals)
	name := fmt.Sprintf(".temp_%d", offset)
	c.scope.FastLocals = append(c.scope.FastLocals, name)
	c.scope.Symbols[name] = &Symbol{
		Name:    name,
		Storage: StorageFastLocal,
		Offset:  offset,
	}
	return offset
}

func (c *Compiler) compileTryStmt(stmt *ASTStmt) error {
	tryStmt := &c.arena.TryStmts[stmt.Extra]

	tryStart := len(c.code)
	depth := c.stackDepth

	for _, sID := range c.arena.StmtIDs[tryStmt.Body.Start : tryStmt.Body.Start+tryStmt.Body.Count] {
		if err := c.compileStmt(sID); err != nil {
			return err
		}
	}
	tryEnd := len(c.code)

	var normalEndJumps []int
	if tryStmt.Else.Count > 0 {
		for _, sID := range c.arena.StmtIDs[tryStmt.Else.Start : tryStmt.Else.Start+tryStmt.Else.Count] {
			if err := c.compileStmt(sID); err != nil {
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
					return err
				}
				c.emitOp(vm.OP_CHECK_EXC_MATCH_STAR, 0, 0)
				nextHandlerJump = c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
			}
			if h.Name != "" {
				if err := c.compileNameStore(h.Name); err != nil {
					return err
				}
			} else {
				c.emitOp(vm.OP_POP_TOP, 0, 0)
			}
			c.emitOp(vm.OP_POP_TOP, 0, 0) // pop rest

			for _, sID := range c.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count] {
				if err := c.compileStmt(sID); err != nil {
					return err
				}
			}
			hEndJump := c.emitOp(vm.OP_JUMP, 0, 0)
			handlerEndJumps = append(handlerEndJumps, hEndJump)

			if h.Type != ExprNone {
				c.patchJump(nextHandlerJump, len(c.code))
			}
		} else {
			if h.Type != ExprNone {
				c.emitOp(vm.OP_DUP_TOP, 0, 0)
				if err := c.compileExpr(h.Type); err != nil {
					return err
				}
				c.emitOp(vm.OP_CHECK_EXC_MATCH, 0, 0)
				nextHandlerJump = c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0)
			}

			if h.Name != "" {
				c.emitOp(vm.OP_DUP_TOP, 0, 0)
				if err := c.compileNameStore(h.Name); err != nil {
					return err
				}
			}
			c.emitOp(vm.OP_POP_TOP, 0, 0)

			for _, sID := range c.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count] {
				if err := c.compileStmt(sID); err != nil {
					return err
				}
			}
			hEndJump := c.emitOp(vm.OP_JUMP, 0, 0)
			handlerEndJumps = append(handlerEndJumps, hEndJump)

			if h.Type != ExprNone {
				c.patchJump(nextHandlerJump, len(c.code))
			}
		}
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
		for _, sID := range c.arena.StmtIDs[tryStmt.Finally.Start : tryStmt.Finally.Start+tryStmt.Finally.Count] {
			if err := c.compileStmt(sID); err != nil {
				return err
			}
		}
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
	if err := c.compileWithItemRecursive(items[1:], body); err != nil {
		return err
	}
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

	// Exception handler: call __exit__(exc, exc, None) and check suppression
	excTarget := len(c.code)
	c.addExceptionRange(withStart, withEnd, excTarget, depth, false)

	// At excTarget, exc is at top of stack.
	tempSlot2 := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot2))

	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
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

	// Exception handler: call __aexit__(exc, exc, None) and check suppression
	excTarget := len(c.code)
	c.addExceptionRange(withStart, withEnd, excTarget, depth, false)

	// At excTarget, exc is at top of stack.
	tempSlot2 := c.allocTempLocal()
	c.emitOp(vm.OP_STORE_FAST, 0, uint16(tempSlot2))

	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot))
	c.emitOp(vm.OP_LOAD_METHOD, 0, exitIdx)
	c.emitOp(vm.OP_LOAD_FAST, 0, uint16(tempSlot2))
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

	// Compile function code object
	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     fnScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 64),
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

	numLocals := len(fnScope.FastLocals)
	if numLocals < len(paramNames) {
		numLocals = len(paramNames)
	}

	maxStack := numLocals + subCompiler.maxStackDepth + 16
	var coFlags uint32
	if fnScope.IsGenerator {
		coFlags |= vm.FlagGenerator
	}
	if fnScope.IsCoroutine {
		coFlags |= vm.FlagCoroutine
	}
	fnCodeObj := &vm.CodeObject{
		Name:           fnDef.Name,
		Code:           subCompiler.code,
		Consts:         subCompiler.constPool.Slice(),
		ParamNames:     paramValSlice,
		CellVars:       cellValSlice,
		FreeVars:       freeValSlice,
		NumLocals:      numLocals,
		NumParams:      len(paramNames),
		MaxStackSize:   maxStack,
		IsGenerator:    fnScope.IsGenerator,
		IsCoroutine:    fnScope.IsCoroutine,
		Flags:          coFlags,
		ExceptionTable: subCompiler.exceptionTable,
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
		}

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
			NumLocals:    len(annScope.FastLocals),
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

	// Closure cells (if FreeVars > 0)
	if len(fnScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range fnScope.FreeVars {
			// Load cell from current scope
			sym := c.scope.Symbols[fv]
			c.emitOp(vm.OP_LOAD_CLOSURE, 0, uint16(sym.Offset))
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

	// Compile class body code object
	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     clsScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 64),
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

	for _, sID := range c.arena.StmtIDs[clsDef.Body.Start : clsDef.Body.Start+clsDef.Body.Count] {
		if err := subCompiler.compileStmt(sID); err != nil {
			return err
		}
	}
	subCompiler.emitReturnNone()
	subCompiler.peephole()

	clsCodeObj := &vm.CodeObject{
		Name:      clsDef.Name,
		Code:      subCompiler.code,
		Consts:    subCompiler.constPool.Slice(),
		NumLocals: 0,
		NumParams: 0,
	}

	// Push class body code object
	coVal := vm.NewCodeVal(clsCodeObj)
	coIdx := c.constPool.Add(coVal)
	c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)

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

	// Build class
	c.emitOp(vm.OP_BUILD_CLASS, 0, 0)

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

	switch expr.Kind {
	case ExprKindInt:
		v := c.arena.IntConsts[expr.Extra]
		idx := c.constPool.Add(val.Int64Val(v))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindFloat:
		v := c.arena.FloatConsts[expr.Extra]
		idx := c.constPool.Add(val.Float64Val(v))
		c.emitOp(vm.OP_LOAD_CONST, 0, idx)

	case ExprKindString:
		rawStr := string(c.src[expr.Start:expr.End])
		if strings.HasPrefix(rawStr, "b") || strings.HasPrefix(rawStr, "B") {
			parsedBytes := parseBytesLiteral(rawStr)
			idx := c.constPool.Add(val.NewBytesVal(parsedBytes))
			c.emitOp(vm.OP_LOAD_CONST, 0, idx)
			return nil
		}
		parsedStr := parseStringLiteral(rawStr)
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
		if err := c.compileExpr(expr.Left); err != nil {
			return err
		}
		if err := c.compileExpr(expr.Right); err != nil {
			return err
		}
		c.emitOp(vm.OP_BINARY_SUBSCR, 0, 0)

	case ExprKindList:
		slice := c.arena.ExprSlices[expr.Extra]
		for _, elem := range c.arena.GetExprSlice(slice) {
			if err := c.compileExpr(elem); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_LIST, 0, uint16(slice.Count))

	case ExprKindTuple:
		slice := c.arena.ExprSlices[expr.Extra]
		for _, elem := range c.arena.GetExprSlice(slice) {
			if err := c.compileExpr(elem); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(slice.Count))

	case ExprKindDict:
		start := expr.Extra
		count := uint32(expr.Op)
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
		for _, elem := range c.arena.GetExprSlice(slice) {
			if err := c.compileExpr(elem); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_BUILD_SET, 0, uint16(slice.Count))

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

func (c *Compiler) compileListComp(expr *ASTExpr) error {
	comp := &c.arena.Comps[expr.Extra]

	// 1. Build initial empty list
	c.emitOp(vm.OP_BUILD_LIST, 0, 0)

	// 2. Evaluate iter
	if err := c.compileExpr(comp.Iter); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)

	// 3. Loop header
	loopStart := len(c.code)
	jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

	// 4. Store target
	if err := c.compileStoreTarget(comp.Target); err != nil {
		return err
	}

	// 5. If conditions
	var ifSkips []int
	for _, ifCond := range c.arena.GetExprSlice(comp.IfConds) {
		if err := c.compileExpr(ifCond); err != nil {
			return err
		}
		ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
	}

	// 6. Evaluate element & append to list
	// Stack: [list, iter, elt]
	if err := c.compileExpr(expr.Left); err != nil {
		return err
	}
	c.emitOp(vm.OP_LIST_APPEND, 0, 2)

	// 7. Loop back
	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	// 8. Patch jumps
	for _, skip := range ifSkips {
		c.patchJump(skip, loopStart) // continue to next iteration
	}
	c.patchJump(jumpEnd, len(c.code))

	return nil
}

func (c *Compiler) compileDictComp(expr *ASTExpr) error {
	comp := &c.arena.Comps[expr.Extra]

	// 1. Build initial empty dict
	c.emitOp(vm.OP_BUILD_MAP, 0, 0)

	// 2. Evaluate iter
	if err := c.compileExpr(comp.Iter); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)

	// 3. Loop header
	loopStart := len(c.code)
	jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

	// 4. Store target
	if err := c.compileStoreTarget(comp.Target); err != nil {
		return err
	}

	// 5. If conditions
	var ifSkips []int
	for _, ifCond := range c.arena.GetExprSlice(comp.IfConds) {
		if err := c.compileExpr(ifCond); err != nil {
			return err
		}
		ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
	}

	// 6. Evaluate key and value & add to dict
	// Stack: [dict, iter, key, val]
	if err := c.compileExpr(expr.Left); err != nil {
		return err
	}
	if err := c.compileExpr(expr.Right); err != nil {
		return err
	}
	c.emitOp(vm.OP_MAP_ADD, 0, 3)

	// 7. Loop back
	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	// 8. Patch jumps
	for _, skip := range ifSkips {
		c.patchJump(skip, loopStart)
	}
	c.patchJump(jumpEnd, len(c.code))

	return nil
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

func parseStringLiteral(s string) string {
	// Strip quotes
	if strings.HasPrefix(s, "'''") || strings.HasPrefix(s, `"""`) {
		if len(s) >= 6 {
			s = s[3 : len(s)-3]
		}
	} else if (strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) ||
		(strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) {
		if len(s) >= 2 {
			s = s[1 : len(s)-1]
		}
	}

	// Basic escape processing
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '\'':
				b.WriteByte('\'')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func parseBytesLiteral(s string) []byte {
	if strings.HasPrefix(s, "b") || strings.HasPrefix(s, "B") {
		s = s[1:]
	}
	// Strip quotes
	if strings.HasPrefix(s, "'''") || strings.HasPrefix(s, `"""`) {
		if len(s) >= 6 {
			s = s[3 : len(s)-3]
		}
	} else if (strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) ||
		(strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`)) {
		if len(s) >= 2 {
			s = s[1 : len(s)-1]
		}
	}

	var res []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				res = append(res, '\n')
			case 't':
				res = append(res, '\t')
			case 'r':
				res = append(res, '\r')
			case '\\':
				res = append(res, '\\')
			case '\'':
				res = append(res, '\'')
			case '"':
				res = append(res, '"')
			case 'x':
				if i+2 < len(s) {
					hexStr := s[i+1 : i+3]
					var b byte
					if _, err := fmt.Sscanf(hexStr, "%x", &b); err == nil {
						res = append(res, b)
						i += 2
						continue
					}
				}
				res = append(res, '\\', s[i])
			default:
				res = append(res, '\\', s[i])
			}
		} else {
			res = append(res, s[i])
		}
	}
	return res
}

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
	comp := &c.arena.Comps[expr.Extra]

	// 1. Build initial empty set
	c.emitOp(vm.OP_BUILD_SET, 0, 0)

	// 2. Evaluate iter
	if err := c.compileExpr(comp.Iter); err != nil {
		return err
	}
	c.emitOp(vm.OP_GET_ITER, 0, 0)

	// 3. Loop header
	loopStart := len(c.code)
	jumpEnd := c.emitOp(vm.OP_FOR_ITER, 0, 0)

	// 4. Store target
	if err := c.compileStoreTarget(comp.Target); err != nil {
		return err
	}

	// 5. If conditions
	var ifSkips []int
	for _, ifCond := range c.arena.GetExprSlice(comp.IfConds) {
		if err := c.compileExpr(ifCond); err != nil {
			return err
		}
		ifSkips = append(ifSkips, c.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
	}

	// 6. Evaluate element & add to set
	// Stack: [set, iter, elt]
	if err := c.compileExpr(expr.Left); err != nil {
		return err
	}
	c.emitOp(vm.OP_SET_ADD, 0, 2)

	// 7. Loop back
	c.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	// 8. Patch jumps
	for _, skip := range ifSkips {
		c.patchJump(skip, loopStart)
	}
	c.patchJump(jumpEnd, len(c.code))

	return nil
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
	}

	// Load parameter 0 (.0)
	subCompiler.emitOp(vm.OP_LOAD_FAST, 0, 0)
	loopStart := len(subCompiler.code)
	jumpEnd := subCompiler.emitOp(vm.OP_FOR_ITER, 0, 0)

	if err := subCompiler.compileStoreTarget(comp.Target); err != nil {
		return err
	}

	var ifSkips []int
	for _, ifCond := range c.arena.GetExprSlice(comp.IfConds) {
		if err := subCompiler.compileExpr(ifCond); err != nil {
			return err
		}
		ifSkips = append(ifSkips, subCompiler.emitOp(vm.OP_JUMP_IF_FALSE, 0, 0))
	}

	if err := subCompiler.compileExpr(expr.Left); err != nil {
		return err
	}
	subCompiler.emitOp(vm.OP_YIELD_VALUE, 0, 0)
	subCompiler.emitOp(vm.OP_POP_TOP, 0, 0)
	subCompiler.emitOp(vm.OP_JUMP, 0, uint16(loopStart))

	for _, skip := range ifSkips {
		subCompiler.patchJump(skip, loopStart)
	}
	subCompiler.patchJump(jumpEnd, len(subCompiler.code))
	subCompiler.emitReturnNone()
	subCompiler.peephole()

	paramNames := []val.Value{val.InternString(".0")}
	numLocals := len(genScope.FastLocals)
	if numLocals < 1 {
		numLocals = 1
	}

	genCodeObj := &vm.CodeObject{
		Name:         "<genexpr>",
		Code:         subCompiler.code,
		Consts:       subCompiler.constPool.Slice(),
		ParamNames:   paramNames,
		NumLocals:    numLocals,
		NumParams:    1,
		MaxStackSize: numLocals + subCompiler.maxStackDepth + 16,
		IsGenerator:  true,
		Flags:        vm.FlagGenerator,
	}

	codeIdx := c.constPool.Add(vm.NewCodeVal(genCodeObj))
	c.emitOp(vm.OP_LOAD_CONST, 0, codeIdx)
	c.emitOp(vm.OP_MAKE_FUNCTION, 0, 0)
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

	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     lambdaScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 16),
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

	numLocals := len(lambdaScope.FastLocals)
	if numLocals < len(paramNames) {
		numLocals = len(paramNames)
	}

	lambdaCodeObj := &vm.CodeObject{
		Name:         "<lambda>",
		Code:         subCompiler.code,
		Consts:       subCompiler.constPool.Slice(),
		ParamNames:   paramValSlice,
		CellVars:     cellValSlice,
		FreeVars:     freeValSlice,
		NumLocals:    numLocals,
		NumParams:    len(paramNames),
		MaxStackSize: numLocals + subCompiler.maxStackDepth + 16,
	}

	if len(lambdaScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range lambdaScope.FreeVars {
			sym := c.scope.Symbols[fv]
			if sym != nil && (sym.Storage == StorageCell || sym.Storage == StorageFree) {
				c.emitOp(vm.OP_LOAD_CLOSURE, 0, uint16(sym.Offset))
			} else {
				_, idx := c.constPool.InternName(fv)
				c.emitOp(vm.OP_LOAD_GLOBAL, 0, idx)
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
