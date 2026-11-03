package vm

import (
	"fmt"
	"sync/atomic"

	"github.com/servusdei2018/fython/pkg/val"
)

// Run executes the top-level bytecode slice with the given constant pool and local variable count.
// Hot loop state (code, consts, ip, baseOffset, sp, stack) is kept in local variables so the Go
// compiler allocates them to CPU registers across loop iterations.
func (vm *VM) Run(code []uint32, consts []val.Value, numLocals int) val.Value {
	vm.rootCO = CodeObject{
		Code:         code,
		Consts:       consts,
		NumLocals:    numLocals,
		MaxStackSize: numLocals + 64,
	}
	return vm.runLoopWithGlobals(&vm.rootCO, 0, numLocals, 0, nil, vm.globals, vm.stack, nil)
}

// RunCode executes a *CodeObject on the VM.
func (vm *VM) RunCode(co *CodeObject) val.Value {
	return vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, vm.globals, vm.stack, nil)
}

// RunCodeWithGlobals executes a *CodeObject using a custom module globals dictionary.
func (vm *VM) RunCodeWithGlobals(co *CodeObject, globals *val.PyDict) val.Value {
	return vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, globals, vm.stack, nil)
}

func (vm *VM) runClassBody(co *CodeObject, classDict *val.PyDict) {
	vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, classDict, vm.stack, nil)
}

// Vectorcall invokes a Python or native callable using a contiguous argument slice and optional keyword names.
func (vm *VM) Vectorcall(callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	base := vm.execCtx.SP
	copy(vm.stack[base:base+len(args)], args)
	return vm.callFromHook(&vm.execCtx, callable, base, len(args), kwnames)
}

// callFromHook executes a callable whose arguments are already placed on vm.stack[argBase : argBase+nargs].
func (vm *VM) callFromHook(ctx *val.ExecContext, callable val.Value, argBase int, nargs int, kwnames []val.Value) val.Value {
	switch callable.Kind() {
	case val.KindBuiltinFunc:
		savedSP := ctx.SP
		ctx.SP = argBase + nargs
		res := callable.AsBuiltinFunc().Fn(ctx, callable, vm.stack[argBase:argBase+nargs], kwnames)
		ctx.SP = savedSP
		return res

	case val.KindFunction:
		fn := callable.AsFunction()
		co := AsCodeObject(fn.Code)
		if co.IsGenerator {
			return vm.instantiateGenerator(co, callable, fn.Defaults, fn.KwDefaults, argBase, nargs, kwnames, vm.globals)
		}
		spAfterBind := vm.bindFunctionArgs(co, fn.Defaults, fn.KwDefaults, argBase, nargs, kwnames)
		savedSP := ctx.SP
		cells := vm.setupCalleeCells(co, callable, argBase, nargs)
		res := vm.runLoopWithGlobals(co, argBase, spAfterBind, vm.activeFrameIdx, cells, vm.globals, vm.stack, nil)
		ctx.SP = savedSP
		return res

	case val.KindCode:
		co := AsCodeObject(callable)
		if co.IsGenerator {
			return vm.instantiateGenerator(co, callable, nil, nil, argBase, nargs, kwnames, vm.globals)
		}
		spAfterBind := vm.bindFunctionArgs(co, nil, nil, argBase, nargs, kwnames)
		savedSP := ctx.SP
		cells := vm.setupCalleeCells(co, callable, argBase, nargs)
		res := vm.runLoopWithGlobals(co, argBase, spAfterBind, vm.activeFrameIdx, cells, vm.globals, vm.stack, nil)
		ctx.SP = savedSP
		return res

	case val.KindBoundMethod:
		bm := callable.AsBoundMethod()
		copy(vm.stack[argBase+1:argBase+1+nargs], vm.stack[argBase:argBase+nargs])
		vm.stack[argBase] = bm.Self
		return vm.callFromHook(ctx, bm.Func, argBase, nargs+1, kwnames)

	default:
		if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
			savedSP := ctx.SP
			ctx.SP = argBase + nargs
			res := tpCall(ctx, callable, vm.stack[argBase:argBase+nargs], kwnames)
			ctx.SP = savedSP
			return res
		}
		panic("vm: TypeError: '" + callable.Type().Name + "' object is not callable")
	}
}

// bindFunctionArgs binds positional, keyword, and default arguments in-place on vm.stack[argBase:]
// with 0 heap allocations, returning the initial sp for the callee frame.
func (vm *VM) bindFunctionArgs(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, argBase int, nargs int, kwnames []val.Value) int {
	numParams := co.NumParams
	numLocals := co.NumLocals
	if numLocals < numParams {
		numLocals = numParams
	}

	if len(kwnames) == 0 {
		if nargs < numParams && len(defaults) > 0 {
			defStart := numParams - len(defaults)
			for i := nargs; i < numParams; i++ {
				if i >= defStart {
					vm.stack[argBase+i] = defaults[i-defStart]
				}
			}
		}
		if numLocals < nargs {
			numLocals = nargs
		}
		return argBase + numLocals
	}

	kwCount := len(kwnames)
	posCount := nargs - kwCount
	if posCount < 0 {
		panic("vm: TypeError: invalid keyword argument count")
	}

	// Copy keyword argument values into VM scratch buffer before rearranging in-place.
	copy(vm.kwScratch[:kwCount], vm.stack[argBase+posCount:argBase+nargs])
	for i := posCount; i < numParams; i++ {
		vm.kwSet[i] = false
	}

	paramNames := co.ParamNames
	for i := 0; i < kwCount; i++ {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := 0; p < len(paramNames); p++ {
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		if foundIdx < 0 {
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
		if foundIdx < posCount || vm.kwSet[foundIdx] {
			panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
		}
		vm.stack[argBase+foundIdx] = kwVal
		vm.kwSet[foundIdx] = true
	}

	defStart := numParams - len(defaults)
	for i := posCount; i < numParams; i++ {
		if !vm.kwSet[i] {
			if i >= defStart && len(defaults) > 0 {
				vm.stack[argBase+i] = defaults[i-defStart]
			} else if kwDefaults != nil && i < len(paramNames) {
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					vm.stack[argBase+i] = v
				} else {
					panic("vm: TypeError: missing required argument")
				}
			} else {
				panic("vm: TypeError: missing required argument")
			}
		}
	}

	return argBase + numLocals
}

func (vm *VM) setupCalleeCells(callee *CodeObject, target val.Value, newBase int, argc int) []val.Value {
	nCells := len(callee.CellVars)
	nFree := len(callee.FreeVars)
	if nCells == 0 && nFree == 0 {
		return nil
	}
	res := make([]val.Value, nCells+nFree)
	for i := 0; i < nCells; i++ {
		name := callee.CellVars[i]
		initVal := val.NoneVal()
		for p := 0; p < len(callee.ParamNames); p++ {
			if val.StringEquals(callee.ParamNames[p], name) && p < argc {
				initVal = vm.stack[newBase+p]
				break
			}
		}
		res[i] = val.NewCellVal(initVal)
	}
	if target.IsFunction() {
		closure := target.AsFunction().Closure
		for j := 0; j < nFree && j < len(closure); j++ {
			res[nCells+j] = closure[j]
		}
	}
	return res
}

func (vm *VM) initBuiltins() {
	vm.builtins.Set(val.InternString("range"), val.NewBuiltinFuncVal("range", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		var start, stop, step int64
		step = 1
		if len(args) == 1 {
			stop = args[0].AsInt64()
		} else if len(args) == 2 {
			start = args[0].AsInt64()
			stop = args[1].AsInt64()
		} else if len(args) >= 3 {
			start = args[0].AsInt64()
			stop = args[1].AsInt64()
			step = args[2].AsInt64()
		}
		if step == 1 && start >= -0x80000000 && start <= 0x7FFFFFFF && stop >= -0x80000000 && stop <= 0x7FFFFFFF {
			return val.RangeVal(int32(start), int32(stop))
		}
		return val.NewRangeIterVal(start, stop, step)
	}))

	vm.builtins.Set(val.InternString("len"), val.NewBuiltinFuncVal("len", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("vm: TypeError: len() takes 1 argument")
		}
		a := args[0]
		if a.IsList() {
			return val.Int64Val(int64(len(a.AsList().Items)))
		}
		if a.IsTuple() {
			return val.Int64Val(int64(len(a.AsTuple().Items)))
		}
		if a.IsDict() {
			return val.Int64Val(int64(a.AsDict().Len()))
		}
		if a.IsAnyString() {
			return val.Int64Val(int64(a.StringLen()))
		}
		if sqLen := a.Type().Slots.SqLen; sqLen != nil {
			return val.Int64Val(sqLen(ctx, a))
		}
		panic("vm: TypeError: object of type '" + a.Type().Name + "' has no len()")
	}))

	vm.builtins.Set(val.InternString("print"), val.NewBuiltinFuncVal("print", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		for i, arg := range args {
			if i > 0 {
				fmt.Print(" ")
			}
			fmt.Print(arg.String())
		}
		fmt.Println()
		return val.NoneVal()
	}))

	vm.builtins.Set(val.InternString("int"), val.TypeVal(val.IntType))
	vm.builtins.Set(val.InternString("float"), val.TypeVal(val.FloatType))
	vm.builtins.Set(val.InternString("str"), val.TypeVal(val.StrType))
	vm.builtins.Set(val.InternString("list"), val.TypeVal(val.ListType))
	vm.builtins.Set(val.InternString("dict"), val.TypeVal(val.DictType))
	vm.builtins.Set(val.InternString("bool"), val.TypeVal(val.BoolType))
	vm.builtins.Set(val.InternString("tuple"), val.TypeVal(val.TupleType))
	vm.builtins.Set(val.InternString("object"), val.TypeVal(val.ObjectType))

	// Built-in exception types
	vm.builtins.Set(val.InternString("BaseException"), val.TypeVal(val.BaseExceptionType))
	vm.builtins.Set(val.InternString("Exception"), val.TypeVal(val.ExceptionType))
	vm.builtins.Set(val.InternString("TypeError"), val.TypeVal(val.TypeErrorType))
	vm.builtins.Set(val.InternString("ValueError"), val.TypeVal(val.ValueErrorType))
	vm.builtins.Set(val.InternString("KeyError"), val.TypeVal(val.KeyErrorType))
	vm.builtins.Set(val.InternString("IndexError"), val.TypeVal(val.IndexErrorType))
	vm.builtins.Set(val.InternString("ZeroDivisionError"), val.TypeVal(val.ZeroDivisionErrorType))
	vm.builtins.Set(val.InternString("NameError"), val.TypeVal(val.NameErrorType))
	vm.builtins.Set(val.InternString("AttributeError"), val.TypeVal(val.AttributeErrorType))
	vm.builtins.Set(val.InternString("RuntimeError"), val.TypeVal(val.RuntimeErrorType))
	vm.builtins.Set(val.InternString("StopIteration"), val.TypeVal(val.StopIterationType))
	vm.builtins.Set(val.InternString("GeneratorExit"), val.TypeVal(val.GeneratorExitType))

	vm.builtins.Set(val.InternString("isinstance"), val.NewBuiltinFuncVal("isinstance", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("isinstance() takes 2 arguments")
		}
		obj := args[0]
		target := args[1]
		if target.IsType() {
			return val.BoolVal(val.IsSubclass(obj.Type(), target.AsType()))
		}
		if target.IsTuple() {
			for _, item := range target.AsTuple().Items {
				if item.IsType() && val.IsSubclass(obj.Type(), item.AsType()) {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		}
		return val.BoolVal(false)
	}))

	vm.builtins.Set(val.InternString("issubclass"), val.NewBuiltinFuncVal("issubclass", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("issubclass() takes 2 arguments")
		}
		cls := args[0]
		target := args[1]
		if !cls.IsType() {
			panic("issubclass() arg 1 must be a class")
		}
		if target.IsType() {
			return val.BoolVal(val.IsSubclass(cls.AsType(), target.AsType()))
		}
		if target.IsTuple() {
			for _, item := range target.AsTuple().Items {
				if item.IsType() && val.IsSubclass(cls.AsType(), item.AsType()) {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		}
		return val.BoolVal(false)
	}))

	vm.builtins.Set(val.InternString("next"), val.NewBuiltinFuncVal("next", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			panic("next() takes at least 1 argument")
		}
		it := args[0]
		var defaultVal val.Value
		hasDefault := len(args) > 1
		if hasDefault {
			defaultVal = args[1]
		}
		if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
			v, status := tpIterNext(ctx, it)
			if status == val.IterYielded {
				return v
			}
			if status == val.IterExhausted {
				if hasDefault {
					return defaultVal
				}
				if ctx != nil {
					ctx.Raise(val.NewStopIteration(val.NoneVal()))
				}
				return val.NoneVal()
			}
			return val.NoneVal()
		}
		if it.IsIter() {
			if v, ok := it.AsIter().Next(); ok {
				return v
			}
			if hasDefault {
				return defaultVal
			}
			if ctx != nil {
				ctx.Raise(val.NewStopIteration(val.NoneVal()))
			}
			return val.NoneVal()
		}
		panic("next() object is not an iterator")
	}))

	vm.builtins.Set(val.InternString("go"), val.NewBuiltinFuncVal("go", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: go() takes at least 1 argument")
		}
		fn := args[0]
		var taskArgs []val.Value
		if len(args) > 1 {
			taskArgs = make([]val.Value, len(args)-1)
			copy(taskArgs, args[1:])
		}
		var eng *Engine
		var globals *val.PyDict
		var builtins *val.PyDict

		if ctx != nil && ctx.VM != nil {
			activeVM := ctx.VM.(*VM)
			eng = activeVM.engine
			globals = activeVM.globals
			builtins = activeVM.builtins
		}
		if eng == nil {
			eng = DefaultEngine()
		}
		if globals == nil {
			globals = val.NewDictVal(16).AsDict()
		}
		if builtins == nil {
			builtins = eng.builtins
		}

		go func() {
			workerVM := eng.AcquireVM()
			defer eng.ReleaseVM(workerVM)
			workerVM.globals = globals
			workerVM.builtins = builtins
			defer func() {
				if r := recover(); r != nil {
					// Catch unhandled panics inside goroutine
				}
			}()
			workerVM.Vectorcall(fn, taskArgs, nil)
		}()

		return val.NoneVal()
	}))

	vm.builtins.Set(val.InternString("chan"), val.NewBuiltinFuncVal("chan", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		capacity := 0
		if len(args) > 0 && args[0].IsInt64() {
			capacity = int(args[0].AsInt64())
			if capacity < 0 {
				capacity = 0
			}
		}
		return val.NewChanVal(capacity)
	}))
}

func (vm *VM) instantiateGenerator(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) val.Value {
	maxStack := co.MaxStackSize
	if maxStack < co.NumLocals+16 {
		maxStack = co.NumLocals + 16
	}
	var codeVal val.Value
	if callable.IsFunction() {
		codeVal = callable.AsFunction().Code
	} else {
		codeVal = callable
	}
	genVal := val.NewGeneratorVal(co.Name, codeVal, maxStack, nil, globals)
	gen := genVal.AsGenerator()
	spAfterBind := vm.bindFunctionArgsToSlice(co, defaults, kwDefaults, gen.Stack, vm.stack[argBase:argBase+nargs], kwnames)
	gen.SP = spAfterBind
	gen.Cells = vm.setupCalleeCellsSlice(co, callable, gen.Stack, 0, nargs)
	gen.ResumeHook = vm.resumeGenerator
	return genVal
}

func (vm *VM) bindFunctionArgsToSlice(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, dst []val.Value, src []val.Value, kwnames []val.Value) int {
	numParams := co.NumParams
	numLocals := co.NumLocals
	if numLocals < numParams {
		numLocals = numParams
	}
	nargs := len(src)

	if len(kwnames) == 0 {
		copy(dst[:nargs], src)
		if nargs < numParams && len(defaults) > 0 {
			defStart := numParams - len(defaults)
			for i := nargs; i < numParams; i++ {
				if i >= defStart {
					dst[i] = defaults[i-defStart]
				}
			}
		}
		if numLocals < nargs {
			numLocals = nargs
		}
		return numLocals
	}

	kwCount := len(kwnames)
	posCount := nargs - kwCount
	if posCount < 0 {
		panic("vm: TypeError: invalid keyword argument count")
	}

	copy(dst[:posCount], src[:posCount])
	copy(vm.kwScratch[:kwCount], src[posCount:nargs])
	for i := posCount; i < numParams; i++ {
		vm.kwSet[i] = false
	}

	paramNames := co.ParamNames
	for i := 0; i < kwCount; i++ {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := 0; p < len(paramNames); p++ {
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		if foundIdx < 0 {
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
		if foundIdx < posCount || vm.kwSet[foundIdx] {
			panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
		}
		dst[foundIdx] = kwVal
		vm.kwSet[foundIdx] = true
	}

	defStart := numParams - len(defaults)
	for i := posCount; i < numParams; i++ {
		if !vm.kwSet[i] {
			if i >= defStart && len(defaults) > 0 {
				dst[i] = defaults[i-defStart]
			} else if kwDefaults != nil && i < len(paramNames) {
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					dst[i] = v
				} else {
					panic("vm: TypeError: missing required argument")
				}
			} else {
				panic("vm: TypeError: missing required argument")
			}
		}
	}

	return numLocals
}

func (vm *VM) setupCalleeCellsSlice(callee *CodeObject, target val.Value, stack []val.Value, newBase int, argc int) []val.Value {
	nCells := len(callee.CellVars)
	nFree := len(callee.FreeVars)
	if nCells == 0 && nFree == 0 {
		return nil
	}
	res := make([]val.Value, nCells+nFree)
	for i := 0; i < nCells; i++ {
		name := callee.CellVars[i]
		initVal := val.NoneVal()
		for p := 0; p < len(callee.ParamNames); p++ {
			if val.StringEquals(callee.ParamNames[p], name) && p < argc {
				initVal = stack[newBase+p]
				break
			}
		}
		res[i] = val.NewCellVal(initVal)
	}
	if target.IsFunction() {
		closure := target.AsFunction().Closure
		for j := 0; j < nFree && j < len(closure); j++ {
			res[nCells+j] = closure[j]
		}
	}
	return res
}

func (vm *VM) resumeGenerator(ctx *val.ExecContext, gen *val.PyGenerator, sentVal val.Value) (val.Value, val.IterStatus) {
	if gen.State == val.GenClosed {
		return val.NoneVal(), val.IterExhausted
	}
	if gen.State == val.GenRunning {
		if ctx != nil {
			ctx.Raise(val.NewValueError("generator already executing"))
		}
		return val.NoneVal(), val.IterError
	}
	if gen.State == val.GenCreated {
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("can't send non-None value to a just-started generator"))
			}
			return val.NoneVal(), val.IterError
		}
	} else if gen.State == val.GenSuspended {
		gen.LastSent = sentVal
		co := AsCodeObject(gen.Code)
		if gen.IP < len(co.Code) && Opcode(co.Code[gen.IP]>>24) != OP_YIELD_FROM {
			gen.Stack[gen.SP] = sentVal
			gen.SP++
		}
	}

	gen.State = val.GenRunning
	co := AsCodeObject(gen.Code)
	res := vm.runLoopWithGlobals(co, gen.Base, gen.SP, 0, gen.Cells, gen.Globals, gen.Stack, gen)
	if gen.State == val.GenSuspended {
		return res, val.IterYielded
	}
	if gen.State == val.GenClosed {
		return gen.RetVal, val.IterExhausted
	}
	if !vm.lastException.IsNone() && vm.lastException.HeaderPtr() != nil {
		if ctx != nil {
			ctx.Raise(vm.lastException)
		}
		return val.NoneVal(), val.IterError
	}
	return res, val.IterExhausted
}

func (vm *VM) unwindException(exc val.Value, curCO **CodeObject, ip *int, frameIdx *int, startFrameIdx int, baseOffset *int, sp *int, code *[]uint32, consts *[]val.Value, cells *[]val.Value, globals **val.PyDict, stack []val.Value) bool {
	vm.lastException = exc
	for {
		co := *curCO
		if co != nil && len(co.ExceptionTable) > 0 {
			faultIP := *ip
			var matched *ExceptionEntry
			minSpan := int(^uint(0) >> 1)
			for i := 0; i < len(co.ExceptionTable); i++ {
				entry := &co.ExceptionTable[i]
				if faultIP >= entry.StartIP && faultIP < entry.EndIP {
					span := entry.EndIP - entry.StartIP
					if span < minSpan {
						minSpan = span
						matched = entry
					}
				}
			}
			if matched != nil {
				*sp = *baseOffset + co.NumLocals + matched.TargetStackDepth
				if matched.PushLasti {
					stack[*sp] = val.Int64Val(int64(faultIP))
					(*sp)++
				}
				stack[*sp] = exc
				(*sp)++
				*ip = matched.TargetIP
				return true
			}
		}

		if *frameIdx > startFrameIdx {
			(*frameIdx)--
			caller := vm.frames[*frameIdx]
			*curCO = caller.co
			*code = caller.code
			*consts = caller.consts
			*baseOffset = caller.baseOffset
			*cells = caller.cells
			*globals = caller.globals
			*ip = caller.ip - 1
			continue
		}

		return false
	}
}

func (vm *VM) runLoop(code []uint32, consts []val.Value, startBase int, startSP int, startFrameIdx int) val.Value {
	vm.rootCO = CodeObject{
		Code:      code,
		Consts:    consts,
		NumLocals: startSP - startBase,
	}
	return vm.runLoopWithGlobals(&vm.rootCO, startBase, startSP, startFrameIdx, nil, vm.globals, vm.stack, nil)
}

func (vm *VM) runLoopWithGlobals(co *CodeObject, startBase int, startSP int, startFrameIdx int, startCells []val.Value, startGlobals *val.PyDict, stack []val.Value, activeGen *val.PyGenerator) val.Value {
	curCO := co
	code := curCO.Code
	consts := curCO.Consts
	frameIdx := startFrameIdx
	baseOffset := startBase
	sp := startSP
	ip := 0
	if activeGen != nil && activeGen.IP > 0 {
		ip = activeGen.IP
	}
	cells := startCells
	globals := startGlobals
	if globals == nil {
		globals = vm.globals
	}

	for {
		inst := atomic.LoadUint32(&code[ip])
		op := Opcode(inst >> 24)

		switch op {
		case OP_LOAD_CONST:
			bx := uint16(inst)
			stack[sp] = consts[bx]
			sp++
			ip++

		case OP_LOAD_FAST:
			bx := int(uint16(inst))
			stack[sp] = stack[baseOffset+bx]
			sp++
			ip++

		case OP_STORE_FAST:
			bx := int(uint16(inst))
			sp--
			stack[baseOffset+bx] = stack[sp]
			ip++

		case OP_BINARY_ADD:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_BINARY_ADD_INT
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD_INT) << 24))
				ai := a.AsInt64()
				bi := b.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					stack[sp-2] = val.Int64Val(sum)
				} else {
					stack[sp-2] = val.Add(a, b)
				}
			} else if a.IsFloat64() && b.IsFloat64() {
				// Quickening: specialize in-place to OP_BINARY_ADD_FLOAT
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD_FLOAT) << 24))
				stack[sp-2] = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_ADD_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				ai := a.AsInt64()
				bi := b.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					stack[sp-2] = val.Int64Val(sum)
				} else {
					stack[sp-2] = val.Add(a, b)
				}
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_ADD
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD) << 24))
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_ADD_FLOAT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsFloat64() && b.IsFloat64() {
				stack[sp-2] = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_ADD
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_ADD) << 24))
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.AddCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_SUB:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_BINARY_SUB_INT
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_SUB_INT) << 24))
				ai := a.AsInt64()
				bi := b.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					stack[sp-2] = val.Int64Val(diff)
				} else {
					stack[sp-2] = val.Sub(a, b)
				}
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.SubCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_SUB_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				ai := a.AsInt64()
				bi := b.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					stack[sp-2] = val.Int64Val(diff)
				} else {
					stack[sp-2] = val.Sub(a, b)
				}
			} else {
				// Type stability broken: deoptimize in-place back to OP_BINARY_SUB
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_BINARY_SUB) << 24))
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.SubCtx(&vm.execCtx, a, b)
			}
			sp--
			ip++

		case OP_BINARY_MUL:
			b := stack[sp-1]
			a := stack[sp-2]
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			stack[sp-2] = val.MulCtx(&vm.execCtx, a, b)
			sp--
			ip++

		case OP_BINARY_SUBSCR:
			key := stack[sp-1]
			obj := stack[sp-2]
			if obj.IsList() && key.IsInt64() {
				val, ok := obj.AsList().Get(int(key.AsInt64()))
				if !ok {
					panic("vm: IndexError: list index out of range")
				}
				stack[sp-2] = val
			} else if obj.IsDict() {
				v, ok := obj.AsDict().Get(key)
				if !ok {
					panic("vm: KeyError")
				}
				stack[sp-2] = v
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-2] = val.GetItemCtx(&vm.execCtx, obj, key)
			}
			sp--
			ip++

		case OP_STORE_SUBSCR:
			valToStore := stack[sp-1]
			key := stack[sp-2]
			obj := stack[sp-3]
			if obj.IsList() && key.IsInt64() {
				if !obj.AsList().Set(int(key.AsInt64()), valToStore) {
					panic("vm: IndexError: list assignment index out of range")
				}
			} else if obj.IsDict() {
				obj.AsDict().Set(key, valToStore)
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				val.SetItemCtx(&vm.execCtx, obj, key, valToStore)
			}
			sp -= 3
			ip++

		case OP_COMPARE_LT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_COMPARE_LT_INT
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_COMPARE_LT_INT) << 24))
				stack[sp-2] = val.BoolVal(a.AsInt64() < b.AsInt64())
			} else {
				stack[sp-2] = val.LessThan(a, b)
			}
			sp--
			ip++

		case OP_COMPARE_LT_INT:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				stack[sp-2] = val.BoolVal(a.AsInt64() < b.AsInt64())
			} else {
				// Type stability broken: deoptimize in-place back to OP_COMPARE_LT
				atomic.StoreUint32(&code[ip], (inst & 0x00FFFFFF) | (uint32(OP_COMPARE_LT) << 24))
				stack[sp-2] = val.LessThan(a, b)
			}
			sp--
			ip++

		case OP_JUMP_IF_FALSE:
			sp--
			cond := stack[sp]
			if !cond.AsBool() {
				ip = int(uint16(inst))
			} else {
				ip++
			}

		case OP_JUMP:
			ip = int(uint16(inst))

		case OP_CALL:
			argc := int(uint8(inst >> 16))
			bx := int(uint16(inst))

			var target val.Value
			var newBase int
			var retSP int

			if bx < len(consts) && isCallableConst(consts[bx]) {
				target = consts[bx]
				newBase = sp - argc
				retSP = newBase
			} else {
				target = stack[sp-argc-1]
				newBase = sp - argc
				retSP = sp - argc - 1
			}

			switch target.Kind() {
			case val.KindCode:
				callee := AsCodeObject(target)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, nil, nil, newBase, argc, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, target, newBase, argc)
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < argc {
					numLocalsCallee = argc
				}
				sp = newBase + numLocalsCallee
				ip = 0

			case val.KindFunction:
				fn := target.AsFunction()
				callee := AsCodeObject(fn.Code)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, fn.Defaults, fn.KwDefaults, newBase, argc, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, argc, nil)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, target, newBase, argc)
				ip = 0

			case val.KindBuiltinFunc:
				builtin := target.AsBuiltinFunc()
				vm.execCtx.SP = newBase + argc
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, target, stack[newBase:newBase+argc], nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + argc
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, target, newBase, argc, nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_CALL_KW:
			totalArgs := int(uint8(inst >> 16))
			bx := int(uint16(inst))
			kwnames := consts[bx].AsTuple().Items

			target := stack[sp-totalArgs-1]
			newBase := sp - totalArgs
			retSP := sp - totalArgs - 1

			switch target.Kind() {
			case val.KindFunction:
				fn := target.AsFunction()
				callee := AsCodeObject(fn.Code)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, fn.Defaults, fn.KwDefaults, newBase, totalArgs, kwnames, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, target, newBase, totalArgs)
				ip = 0

			case val.KindCode:
				callee := AsCodeObject(target)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, nil, nil, newBase, totalArgs, kwnames, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				sp = vm.bindFunctionArgs(callee, nil, nil, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, target, newBase, totalArgs)
				ip = 0

			case val.KindBuiltinFunc:
				builtin := target.AsBuiltinFunc()
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, target, stack[newBase:newBase+totalArgs], kwnames)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, target, newBase, totalArgs, kwnames)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_LOAD_METHOD:
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindClassMethod {
						// stack[sp-1] is already obj (Receiver); place raw Callable at stack[sp] with 0 allocs!
						stack[sp] = entry.CachedVal
						sp++
						ip++
						continue
					}
					if entry.Kind == ICKindInstanceSlot {
						stack[sp-1] = val.UnboundVal()
						stack[sp] = instObj.Slots[entry.SlotIndex]
						sp++
						ip++
						continue
					}
				}
			}
			vm.loadMethodSlow(obj, consts[uint16(inst)], ip, sp, frameIdx, &code[0])
			sp++
			ip++

		case OP_CALL_METHOD:
			argc := int(uint8(inst >> 16))
			retSP := sp - argc - 2
			receiver := stack[retSP]
			callable := stack[retSP+1]

			var newBase int
			var totalArgs int

			if !receiver.IsUnbound() {
				// Bound method call fast path: Receiver is already at stack[retSP].
				// Shift positional arguments down by 1 slot over Callable so [Receiver, arg1, ..., argN] are contiguous.
				newBase = retSP
				totalArgs = argc + 1
				if argc == 1 {
					stack[retSP+1] = stack[retSP+2]
				} else if argc > 1 {
					copy(stack[retSP+1:retSP+1+argc], stack[retSP+2:retSP+2+argc])
				}
			} else {
				newBase = retSP + 2
				totalArgs = argc
			}

			switch callable.Kind() {
			case val.KindFunction:
				fn := callable.AsFunction()
				callee := AsCodeObject(fn.Code)
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				if totalArgs < callee.NumParams && len(fn.Defaults) > 0 {
					sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, newBase, totalArgs, nil)
				} else {
					numLocalsCallee := callee.NumLocals
					if numLocalsCallee < totalArgs {
						numLocalsCallee = totalArgs
					}
					sp = newBase + numLocalsCallee
				}
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, callable, newBase, totalArgs)
				ip = 0

			case val.KindCode:
				callee := AsCodeObject(callable)
				vm.frames[frameIdx] = CallFrame{
					co:         curCO,
					code:       code,
					consts:     consts,
					ip:         ip + 1,
					baseOffset: baseOffset,
					retSP:      retSP,
					cells:      cells,
					globals:    globals,
				}
				frameIdx++
				curCO = callee
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				cells = vm.setupCalleeCells(callee, callable, newBase, totalArgs)
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < totalArgs {
					numLocalsCallee = totalArgs
				}
				sp = newBase + numLocalsCallee
				ip = 0

			case val.KindBuiltinFunc:
				builtin := callable.AsBuiltinFunc()
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := builtin.Fn(&vm.execCtx, callable, stack[newBase:newBase+totalArgs], nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++

			default:
				vm.execCtx.SP = newBase + totalArgs
				vm.activeFrameIdx = frameIdx
				res := vm.callFromHook(&vm.execCtx, callable, newBase, totalArgs, nil)
				stack[retSP] = res
				sp = retSP + 1
				ip++
			}

		case OP_RETURN:
			sp--
			retVal := stack[sp]
			if activeGen != nil && frameIdx == startFrameIdx {
				activeGen.RetVal = retVal
				activeGen.State = val.GenClosed
				activeGen.SP = sp
				return retVal
			}
			if frameIdx == startFrameIdx {
				return retVal
			}
			frameIdx--
			caller := vm.frames[frameIdx]
			curCO = caller.co
			code = caller.code
			consts = caller.consts
			ip = caller.ip
			baseOffset = caller.baseOffset
			cells = caller.cells
			globals = caller.globals
			stack[caller.retSP] = retVal
			sp = caller.retSP + 1

		case OP_LOAD_ATTR:
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindInstanceSlot {
						val, ok := instObj.GetSlot(entry.SlotIndex)
						if ok {
							stack[sp-1] = val
							ip++
							continue
						}
					}
					if entry.Kind == ICKindDataDescriptor {
						vm.execCtx.SP = sp
						vm.activeFrameIdx = frameIdx
						stack[sp-1] = entry.CachedVal.Type().Slots.TpDescrGet(&vm.execCtx, entry.CachedVal, obj, instType)
						ip++
						continue
					}
				}
				stack[sp-1] = vm.loadAttrInstanceSlow(instObj, obj, consts[uint16(inst)], ip, sp, frameIdx, &code[0])
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-1] = val.GetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)])
			}
			ip++

		case OP_STORE_ATTR:
			a := stack[sp-2]
			b := stack[sp-1]
			var obj val.Value
			var v val.Value
			if a.IsInstance() || a.IsType() || a.IsSuper() {
				obj = a
				v = b
			} else {
				obj = b
				v = a
			}
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				entry := vm.ic[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer && entry.CodePtr == &code[0] {
					if entry.Kind == ICKindInstanceSlot {
						if instObj.SetSlot(entry.SlotIndex, v) {
							sp -= 2
							ip++
							continue
						}
					}
					if entry.Kind == ICKindDataDescriptor {
						vm.execCtx.SP = sp
						vm.activeFrameIdx = frameIdx
						entry.CachedVal.Type().Slots.TpDescrSet(&vm.execCtx, entry.CachedVal, obj, v)
						sp -= 2
						ip++
						continue
					}
				}
				vm.storeAttrInstanceSlow(instObj, obj, consts[uint16(inst)], v, ip, sp, frameIdx, &code[0])
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				val.SetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)], v)
			}
			sp -= 2
			ip++

		case OP_POP_TOP:
			sp--
			ip++

		case OP_DUP_TOP:
			stack[sp] = stack[sp-1]
			sp++
			ip++

		case OP_LOAD_GLOBAL:
			bx := uint16(inst)
			nameVal := consts[bx]
			globVer := atomic.LoadUint64(&globals.VersionTag)
			entry := &vm.ic[ip]
			if entry.CodePtr == &code[0] && entry.ExpectedGlobalsVer == globVer {
				if entry.Kind == ICKindGlobal {
					stack[sp] = entry.CachedVal
					sp++
					ip++
					continue
				}
				if entry.Kind == ICKindBuiltin && entry.ExpectedBuiltinsVer == atomic.LoadUint64(&vm.builtins.VersionTag) {
					stack[sp] = entry.CachedVal
					sp++
					ip++
					continue
				}
			}
			if v, ok := globals.Get(nameVal); ok {
				entry.Kind = ICKindGlobal
				entry.ExpectedGlobalsVer = globVer
				entry.CodePtr = &code[0]
				entry.CachedVal = v
				stack[sp] = v
				sp++
				ip++
				continue
			}
			if v, ok := vm.builtins.Get(nameVal); ok {
				entry.Kind = ICKindBuiltin
				entry.ExpectedGlobalsVer = globVer
				entry.ExpectedBuiltinsVer = atomic.LoadUint64(&vm.builtins.VersionTag)
				entry.CodePtr = &code[0]
				entry.CachedVal = v
				stack[sp] = v
				sp++
				ip++
				continue
			}
			panic("vm: NameError: name '" + nameVal.AsString() + "' is not defined")

		case OP_STORE_GLOBAL:
			bx := uint16(inst)
			nameVal := consts[bx]
			sp--
			globals.Set(nameVal, stack[sp])
			ip++

		case OP_LOAD_DEREF:
			bx := int(uint16(inst))
			stack[sp] = cells[bx].AsCell().Get()
			sp++
			ip++

		case OP_STORE_DEREF:
			bx := int(uint16(inst))
			sp--
			cells[bx].AsCell().Set(stack[sp])
			ip++

		case OP_LOAD_CLOSURE:
			bx := int(uint16(inst))
			stack[sp] = cells[bx]
			sp++
			ip++

		case OP_MAKE_FUNCTION:
			flags := uint8(inst >> 16)
			sp--
			codeVal := stack[sp]
			var closureSlice []val.Value
			if (flags & 0x01) != 0 {
				sp--
				closureVal := stack[sp]
				closureSlice = closureVal.AsTuple().Items
			}
			var defaultsSlice []val.Value
			if (flags & 0x02) != 0 {
				sp--
				defVal := stack[sp]
				defaultsSlice = defVal.AsTuple().Items
			}
			callee := AsCodeObject(codeVal)
			fnVal := val.NewFunctionValWithClosure(callee.Name, codeVal, defaultsSlice, closureSlice)
			stack[sp] = fnVal
			sp++
			ip++

		case OP_GET_ITER:
			top := stack[sp-1]
			if top.IsRange() {
				ip++
				continue
			}
			if top.IsList() || top.IsTuple() || top.IsDict() {
				var it *val.PyIter
				if activeGen != nil {
					it = &activeGen.Iters[(sp-1)&7]
				} else {
					it = &vm.frames[frameIdx].iters[(sp-1)&15]
				}
				it.Header = val.ObjectHeader{Kind: val.KindIter, Type: val.IterType}
				if top.IsList() {
					it.Kind = val.IterKindList
				} else if top.IsTuple() {
					it.Kind = val.IterKindTuple
				} else {
					it.Kind = val.IterKindDict
				}
				it.Obj = top
				it.Idx = 0
				stack[sp-1] = val.ObjectVal(&it.Header)
				ip++
				continue
			}
			if !top.IsIter() && !top.IsGenerator() {
				if tpIter := top.Type().Slots.TpIter; tpIter != nil {
					stack[sp-1] = tpIter(&vm.execCtx, top)
				} else {
					stack[sp-1] = val.NewIterVal(top)
				}
			}
			ip++

		case OP_FOR_ITER:
			jumpTarget := int(uint16(inst))
			it := stack[sp-1]
			if it.IsRange() {
				cur := it.RangeStart()
				stop := it.RangeStop()
				if cur < stop {
					stack[sp-1] = val.RangeVal(cur+1, stop)
					stack[sp] = val.Int64Val(int64(cur))
					sp++
					ip++
					continue
				}
				sp--
				ip = jumpTarget
				continue
			}
			if it.IsIter() {
				pyIter := it.AsIter()
				if nextVal, ok := pyIter.Next(); ok {
					stack[sp] = nextVal
					sp++
					ip++
					continue
				}
				sp--
				ip = jumpTarget
				continue
			}
			if tpIterNext := it.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status := tpIterNext(&vm.execCtx, it)
				if status == val.IterYielded {
					stack[sp] = nextVal
					sp++
					ip++
					continue
				}
				if status == val.IterExhausted {
					sp--
					ip = jumpTarget
					continue
				}
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
						if activeGen != nil {
							activeGen.State = val.GenClosed
						}
						return val.NoneVal()
					}
					continue
				}
			}

		case OP_BUILD_LIST:
			count := int(uint16(inst))
			items := make([]val.Value, count)
			copy(items, stack[sp-count:sp])
			sp -= count
			stack[sp] = val.NewListVal(items...)
			sp++
			ip++

		case OP_LIST_APPEND:
			depth := int(uint16(inst))
			listVal := stack[sp-depth-1]
			item := stack[sp-1]
			sp--
			l := listVal.AsList()
			l.Append(item)
			ip++

		case OP_BUILD_MAP:
			count := int(uint16(inst))
			dictVal := val.NewDictVal(count)
			d := dictVal.AsDict()
			for i := sp - count*2; i < sp; i += 2 {
				d.Set(stack[i], stack[i+1])
			}
			sp -= count * 2
			stack[sp] = dictVal
			sp++
			ip++

		case OP_MAP_ADD:
			depth := int(uint16(inst))
			dictVal := stack[sp-depth-1]
			v := stack[sp-1]
			k := stack[sp-2]
			sp -= 2
			dictVal.AsDict().Set(k, v)
			ip++

		case OP_BUILD_TUPLE:
			count := int(uint16(inst))
			items := make([]val.Value, count)
			copy(items, stack[sp-count:sp])
			sp -= count
			stack[sp] = val.NewTupleVal(items...)
			sp++
			ip++

		case OP_BUILD_CLASS:
			basesVal := stack[sp-1]
			nameVal := stack[sp-2]
			classBody := stack[sp-3]
			sp -= 3

			classDict := val.NewDictVal(8).AsDict()
			var bases []*val.PyType
			if basesVal.IsTuple() {
				for _, b := range basesVal.AsTuple().Items {
					if b.IsType() {
						bases = append(bases, b.AsType())
					}
				}
			} else if basesVal.IsType() {
				bases = []*val.PyType{basesVal.AsType()}
			}

			if classBody.Kind() == val.KindCode {
				vm.runClassBody(AsCodeObject(classBody), classDict)
			} else if classBody.IsFunction() {
				vm.runClassBody(AsCodeObject(classBody.AsFunction().Code), classDict)
			}

			newCls := val.MustNewType(nameVal.AsString(), bases, classDict)
			stack[sp] = val.TypeVal(newCls)
			sp++
			ip++

		case OP_UNPACK_SEQUENCE:
			count := int(uint16(inst))
			sp--
			seq := stack[sp]
			var items []val.Value
			if seq.IsTuple() {
				items = seq.AsTuple().Items
			} else if seq.IsList() {
				items = seq.AsList().Items
			} else {
				panic("vm: TypeError: cannot unpack non-iterable object")
			}
			if len(items) != count {
				panic("vm: ValueError: not enough or too many values to unpack")
			}
			for i := count - 1; i >= 0; i-- {
				stack[sp] = items[i]
				sp++
			}
			ip++

		case OP_COMPARE_OP:
			cmpOp := uint8(inst >> 16)
			b := stack[sp-1]
			a := stack[sp-2]
			var res val.Value
			switch cmpOp {
			case CMP_LT:
				res = val.LessThan(a, b)
			case CMP_LTE:
				res = val.LessOrEqual(a, b)
			case CMP_EQ:
				res = val.BoolVal(val.Equals(a, b))
			case CMP_NEQ:
				res = val.NotEquals(a, b)
			case CMP_GT:
				res = val.GreaterThan(a, b)
			case CMP_GTE:
				res = val.GreaterOrEqual(a, b)
			case CMP_IN:
				res = val.In(a, b)
			case CMP_NOT_IN:
				res = val.NotIn(a, b)
			case CMP_IS:
				res = val.Is(a, b)
			case CMP_IS_NOT:
				res = val.IsNot(a, b)
			default:
				panic("vm: unknown comparison op")
			}
			stack[sp-2] = res
			sp--
			ip++

		case OP_BINARY_TRUE_DIV:
			b := stack[sp-1]
			a := stack[sp-2]
			if (b.IsInt64() && b.AsInt64() == 0) || (b.IsFloat64() && b.AsFloat64() == 0.0) {
				exc := val.NewZeroDivisionError("division by zero")
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
					if activeGen != nil {
						activeGen.State = val.GenClosed
					}
					return val.NoneVal()
				}
				continue
			}
			stack[sp-2] = val.TrueDiv(a, b)
			sp--
			ip++

		case OP_BINARY_FLOOR_DIV:
			b := stack[sp-1]
			a := stack[sp-2]
			if (b.IsInt64() && b.AsInt64() == 0) || (b.IsFloat64() && b.AsFloat64() == 0.0) {
				exc := val.NewZeroDivisionError("integer division or modulo by zero")
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
					if activeGen != nil {
						activeGen.State = val.GenClosed
					}
					return val.NoneVal()
				}
				continue
			}
			stack[sp-2] = val.FloorDiv(a, b)
			sp--
			ip++

		case OP_BINARY_MOD:
			b := stack[sp-1]
			a := stack[sp-2]
			if (b.IsInt64() && b.AsInt64() == 0) || (b.IsFloat64() && b.AsFloat64() == 0.0) {
				exc := val.NewZeroDivisionError("integer modulo by zero")
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
					if activeGen != nil {
						activeGen.State = val.GenClosed
					}
					return val.NoneVal()
				}
				continue
			}
			stack[sp-2] = val.Mod(a, b)
			sp--
			ip++

		case OP_BINARY_POW:
			stack[sp-2] = val.Pow(stack[sp-2], stack[sp-1])
			sp--
			ip++

		case OP_UNARY_NEGATIVE:
			v := stack[sp-1]
			if v.IsInt64() {
				stack[sp-1] = val.Int64Val(-v.AsInt64())
			} else if v.IsFloat64() {
				stack[sp-1] = val.Float64Val(-v.AsFloat64())
			} else {
				stack[sp-1] = val.Sub(val.Int64Val(0), v)
			}
			ip++

		case OP_UNARY_NOT:
			stack[sp-1] = val.BoolVal(!stack[sp-1].IsTruthy())
			ip++

		case OP_JUMP_IF_TRUE:
			sp--
			cond := stack[sp]
			if cond.AsBool() {
				ip = int(uint16(inst))
			} else {
				ip++
			}

		case OP_JUMP_IF_FALSE_OR_POP:
			if !stack[sp-1].IsTruthy() {
				ip = int(uint16(inst))
			} else {
				sp--
				ip++
			}

		case OP_JUMP_IF_TRUE_OR_POP:
			if stack[sp-1].IsTruthy() {
				ip = int(uint16(inst))
			} else {
				sp--
				ip++
			}

		case OP_RAISE:
			argc := DecodeArgA(inst)
			var exc val.Value
			if argc == 0 {
				exc = vm.lastException
				if exc.IsNone() || exc.HeaderPtr() == nil {
					exc = val.NewRuntimeError("No active exception to reraise")
				}
			} else if argc == 1 {
				sp--
				exc = stack[sp]
				if exc.IsType() {
					exc = val.NewInstanceOfType(exc.AsType())
				}
			} else if argc == 2 {
				cause := stack[sp-1]
				exc = stack[sp-2]
				sp -= 2
				if exc.IsType() {
					exc = val.NewInstanceOfType(exc.AsType())
				}
				if exc.IsInstance() {
					exc.AsInstance().SetAttr(val.InternString("__cause__"), cause)
					exc.AsInstance().SetAttr(val.InternString("__suppress_context__"), val.BoolVal(true))
				}
			}
			if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
				if activeGen != nil {
					activeGen.State = val.GenClosed
				}
				return val.NoneVal()
			}
			continue

		case OP_YIELD_VALUE:
			sp--
			yieldVal := stack[sp]
			if activeGen != nil {
				activeGen.YieldVal = yieldVal
				activeGen.State = val.GenSuspended
				activeGen.SP = sp
				activeGen.IP = ip + 1
				return yieldVal
			}
			ip++

		case OP_YIELD_FROM:
			subiter := stack[sp-1]
			if !subiter.IsIter() && !subiter.IsGenerator() {
				if tpIter := subiter.Type().Slots.TpIter; tpIter != nil {
					subiter = tpIter(&vm.execCtx, subiter)
					stack[sp-1] = subiter
				}
			}
			sentVal := val.NoneVal()
			if activeGen != nil {
				sentVal = activeGen.LastSent
				activeGen.LastSent = val.NoneVal()
			}
			var nextVal val.Value
			var status val.IterStatus
			if subiter.IsGenerator() {
				nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, sentVal)
			} else if tpIterNext := subiter.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status = tpIterNext(&vm.execCtx, subiter)
			} else {
				ip++
				continue
			}

			if status == val.IterYielded {
				if activeGen != nil {
					activeGen.YieldVal = nextVal
					activeGen.State = val.GenSuspended
					activeGen.SP = sp
					activeGen.IP = ip
					return nextVal
				}
			} else if status == val.IterExhausted {
				retVal := val.NoneVal()
				if subiter.IsGenerator() {
					retVal = subiter.AsGenerator().RetVal
				}
				stack[sp-1] = retVal
				ip++
			} else {
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, stack) {
						if activeGen != nil {
							activeGen.State = val.GenClosed
						}
						return val.NoneVal()
					}
					continue
				}
			}

		case OP_CHECK_EXC_MATCH:
			target := stack[sp-1]
			exc := stack[sp-2]
			sp--
			match := false
			if target.IsType() {
				match = val.IsSubclass(exc.Type(), target.AsType())
			} else if target.IsTuple() {
				for _, item := range target.AsTuple().Items {
					if item.IsType() && val.IsSubclass(exc.Type(), item.AsType()) {
						match = true
						break
					}
				}
			}
			stack[sp] = val.BoolVal(match)
			sp++
			ip++

		case OP_CLEANUP_EXC:
			sp--
			ip++

		default:
			panic("vm: unknown opcode")
		}
	}
}

func isCallableConst(v val.Value) bool {
	k := v.Kind()
	return k == val.KindCode || k == val.KindFunction || k == val.KindBuiltinFunc
}

//go:noinline
func (vm *VM) loadMethodSlow(obj, attrName val.Value, ip, sp, frameIdx int, codePtr *uint32) {
	attrName = val.InternValue(attrName)

	if obj.IsSuper() {
		sup := obj.AsSuper()
		selfType := sup.Self.Type()
		descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName)
		if !ok {
			panic("vm: AttributeError: 'super' object has no attribute '" + attrName.AsString() + "'")
		}
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			vm.stack[sp-1] = sup.Self
			vm.stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, sup.Self, selfType)
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = res
			return
		}
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = descr
		return
	}

	if obj.IsInstance() {
		instObj := obj.AsInstance()
		instType := instObj.Header.Type

		descr, _, foundInClass := instType.LookupMRO(attrName)
		if foundInClass {
			descrType := descr.Type()
			if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
				vm.stack[sp-1] = val.UnboundVal()
				vm.stack[sp] = res
				return
			}
		}

		if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindInstanceSlot,
				SlotIndex:       slotIdx,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
			}
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = instObj.Slots[slotIdx]
			return
		}

		if foundInClass {
			if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
				vm.ic[ip] = ICSlot{
					ExpectedShapeID: instObj.Shape.ID,
					Kind:            ICKindClassMethod,
					ExpectedTypeVer: instType.VersionTag,
					CodePtr:         codePtr,
					CachedVal:       descr,
				}
				vm.stack[sp-1] = obj
				vm.stack[sp] = descr
				return
			}
			if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrGet(&vm.execCtx, descr, obj, instType)
				vm.stack[sp-1] = val.UnboundVal()
				vm.stack[sp] = res
				return
			}
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = descr
			return
		}

		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = res
		return
	}

	if obj.IsType() {
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = res
		return
	}

	// Primitive or built-in heap type method lookup.
	tp := obj.Type()
	if descr, _, ok := tp.LookupMRO(attrName); ok {
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			vm.stack[sp-1] = obj
			vm.stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, obj, tp)
			vm.stack[sp-1] = val.UnboundVal()
			vm.stack[sp] = res
			return
		}
		vm.stack[sp-1] = val.UnboundVal()
		vm.stack[sp] = descr
		return
	}

	panic("vm: AttributeError: '" + tp.Name + "' object has no attribute '" + attrName.AsString() + "'")
}

//go:noinline
func (vm *VM) loadAttrInstanceSlow(instObj *val.PyInstance, obj, attrName val.Value, ip, sp, frameIdx int, codePtr *uint32) val.Value {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	descr, _, foundInClass := instType.LookupMRO(attrName)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		vm.ic[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
			CodePtr:         codePtr,
		}
		val, _ := instObj.GetSlot(slotIdx)
		return val
	}

	if foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
		return descr
	}

	vm.execCtx.SP = sp
	vm.activeFrameIdx = frameIdx
	return val.GetAttrCtx(&vm.execCtx, obj, attrName)
}

//go:noinline
func (vm *VM) storeAttrInstanceSlow(instObj *val.PyInstance, obj, attrName, v val.Value, ip, sp, frameIdx int, codePtr *uint32) {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			vm.ic[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
				CodePtr:         codePtr,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			descrType.Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
			return
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		instObj.SetSlot(slotIdx, v)
		vm.ic[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
			CodePtr:         codePtr,
		}
		return
	}

	slotIdx := instObj.SetAttr(attrName, v)
	vm.ic[ip] = ICSlot{
		ExpectedShapeID: instObj.Shape.ID,
		Kind:            ICKindInstanceSlot,
		SlotIndex:       slotIdx,
		ExpectedTypeVer: instType.VersionTag,
		CodePtr:         codePtr,
	}
}
