package compiler

import (
	"fmt"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

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
		if err := c.compileExpr(stmt.Expr2); err != nil {
			return err
		}
		return c.compileStoreTarget(stmt.Expr1)

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

	case StmtKindFuncDef:
		return c.compileFuncDef(stmt)

	case StmtKindClassDef:
		return c.compileClassDef(stmt)

	case StmtKindGlobal, StmtKindNonlocal:
		// Handled in symbol table pass
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

	case ExprKindTuple, ExprKindList:
		slice := c.arena.ExprSlices[expr.Extra]
		count := slice.Count
		c.emitOp(vm.OP_UNPACK_SEQUENCE, 0, uint16(count))
		for i := uint32(0); i < count; i++ {
			elemID := c.arena.ExprIDs[slice.Start+i]
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
	c.emitOp(vm.OP_LOAD_METHOD, 0, enterIdx)
	c.emitOp(vm.OP_CALL_METHOD, 0, 0)

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
	c.emitOp(vm.OP_LOAD_METHOD, 0, exitIdx)
	noneIdx := c.constPool.Add(val.NoneVal())
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_LOAD_CONST, 0, noneIdx)
	c.emitOp(vm.OP_CALL_METHOD, 3, 0)
	c.emitOp(vm.OP_POP_TOP, 0, 0)
	jumpAfterWith := c.emitOp(vm.OP_JUMP, 0, 0)

	// Exception handler: call __exit__(exc, exc, None) and check suppression
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

func (c *Compiler) compileFuncDef(stmt *ASTStmt) error {
	fnDef := &c.arena.FuncDefs[stmt.Extra]

	// Find child scope matching function name
	var fnScope *Scope
	for _, child := range c.scope.Children {
		if child.Kind == ScopeFunction && child.Name == fnDef.Name {
			fnScope = child
			break
		}
	}

	// Compile function code object
	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     fnScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 64),
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
		ExceptionTable: subCompiler.exceptionTable,
	}

	// Closure cells (if FreeVars > 0)
	var flags uint8
	if len(fnScope.FreeVars) > 0 {
		flags |= 0x01
		for _, fv := range fnScope.FreeVars {
			// Load cell from current scope
			sym := c.scope.Symbols[fv]
			c.emitOp(vm.OP_LOAD_CLOSURE, 0, uint16(sym.Offset))
		}
		c.emitOp(vm.OP_BUILD_TUPLE, 0, uint16(len(fnScope.FreeVars)))
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

	coVal := vm.NewCodeVal(fnCodeObj)
	coIdx := c.constPool.Add(coVal)
	c.emitOp(vm.OP_LOAD_CONST, 0, coIdx)
	c.emitOp(vm.OP_MAKE_FUNCTION, flags, 0)

	return c.compileNameStore(fnDef.Name)
}

func (c *Compiler) compileClassDef(stmt *ASTStmt) error {
	clsDef := &c.arena.ClassDefs[stmt.Extra]

	var clsScope *Scope
	for _, child := range c.scope.Children {
		if child.Kind == ScopeClass && child.Name == clsDef.Name {
			clsScope = child
			break
		}
	}

	// Compile class body code object
	subCompiler := &Compiler{
		src:       c.src,
		arena:     c.arena,
		scope:     clsScope,
		constPool: NewConstPool(),
		code:      make([]uint32, 0, 64),
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

	case ExprKindListComp:
		return c.compileListComp(expr)

	case ExprKindDictComp:
		return c.compileDictComp(expr)

	case ExprKindYield:
		return c.compileYieldExpr(expr)

	case ExprKindYieldFrom:
		return c.compileYieldFromExpr(expr)
	}

	return nil
}

func (c *Compiler) compileCall(expr *ASTExpr) error {
	call := &c.arena.Calls[expr.Extra]
	posArgs := c.arena.GetExprSlice(call.PosArgs)
	kwNames := c.arena.GetIdentSlice(call.KWNames)
	kwArgs := c.arena.GetExprSlice(call.KWArgs)

	calleeExpr := c.arena.GetExpr(call.Func)
	isMethod := calleeExpr != nil && calleeExpr.Kind == ExprKindAttribute && len(kwNames) == 0

	if isMethod {
		if err := c.compileExpr(calleeExpr.Left); err != nil {
			return err
		}
		attrName := string(c.src[calleeExpr.Start:calleeExpr.End])
		_, idx := c.constPool.InternName(attrName)
		c.emitOp(vm.OP_LOAD_METHOD, 0, idx)
		for _, arg := range posArgs {
			if err := c.compileExpr(arg); err != nil {
				return err
			}
		}
		c.emitOp(vm.OP_CALL_METHOD, uint8(len(posArgs)), 0)
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
				code[i] = vm.Encode(vm.OP_POP_TOP, 0, 0) // or NOP equivalent
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
