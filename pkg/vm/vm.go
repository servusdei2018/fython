package vm

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"unsafe"

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
	return vm.runLoopWithGlobals(&vm.rootCO, 0, numLocals, 0, nil, vm.globals, val.NoneVal(), vm.stack, nil, nil)
}

// RunCode executes a *CodeObject on the VM.
func (vm *VM) RunCode(co *CodeObject) val.Value {
	return vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, vm.globals, val.NoneVal(), vm.stack, nil, nil)
}

// RunCodeWithGlobals executes a *CodeObject using a custom module globals dictionary.
func (vm *VM) RunCodeWithGlobals(co *CodeObject, globals *val.PyDict) val.Value {
	return vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, globals, val.NoneVal(), vm.stack, nil, nil)
}

func (vm *VM) runClassBody(co *CodeObject, classDict *val.PyDict) {
	vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, nil, vm.globals, val.DictVal(classDict), vm.stack, nil, nil)
}

func (vm *VM) runClassBodyWithLocals(co *CodeObject, globals *val.PyDict, namespace val.Value, cells []val.Value) {
	vm.runLoopWithGlobals(co, 0, co.NumLocals, 0, cells, globals, namespace, vm.stack, nil, nil)
}

func (vm *VM) buildClass(classBody, nameVal val.Value, origBases []val.Value, kwdsVal val.Value, globals *val.PyDict) val.Value {
	// Step 1: Base Resolution via __mro_entries__ (PEP 560)
	var resolvedBases []val.Value
	hasMROEntries := false
	for _, b := range origBases {
		if !b.IsType() {
			bType := b.Type()
			if mroEntryFn, _, ok := bType.LookupMRO(val.DunderMroEntries); ok {
				hasMROEntries = true
				tupleVal := val.NewTupleVal(origBases...)
				entries := vm.Vectorcall(mroEntryFn, []val.Value{b, tupleVal}, nil)
				if !entries.IsTuple() {
					panic("TypeError: __mro_entries__ must return a tuple")
				}
				resolvedBases = append(resolvedBases, entries.AsTuple().Items...)
				continue
			}
		}
		resolvedBases = append(resolvedBases, b)
	}

	// Step 2: Determine the Most Derived Metaclass
	var explicitMeta val.Value
	var kwdsDict *val.PyDict
	if kwdsVal.IsDict() {
		kwdsDict = kwdsVal.AsDict()
		if metaV, ok := kwdsDict.Get(val.InternString("metaclass")); ok {
			explicitMeta = metaV
			kwdsDict.Delete(val.InternString("metaclass"))
		}
	}

	var meta *val.PyType
	var metaCallable val.Value

	if !explicitMeta.IsNone() && explicitMeta.HeaderPtr() != nil {
		if explicitMeta.IsType() {
			meta = explicitMeta.AsType()
		} else {
			metaCallable = explicitMeta
		}
	}

	if meta == nil && metaCallable.HeaderPtr() == nil {
		meta = val.TypeType
	}

	if meta != nil {
		for _, b := range resolvedBases {
			if b.IsType() {
				bMeta := b.AsType().Header.Type
				if bMeta == nil {
					bMeta = val.TypeType
				}
				if val.IsSubclass(bMeta, meta) {
					meta = bMeta
				} else if val.IsSubclass(meta, bMeta) {
					// meta is already a subclass of bMeta, keep meta
				} else {
					panic("TypeError: metaclass conflict: the metaclass of a derived class must be a (non-strict) subclass of the metaclasses of all its bases")
				}
			}
		}
	}

	// Step 3: __prepare__ Hook
	var namespace val.Value
	if meta != nil {
		deferCatch := func() (v val.Value, ok bool) {
			defer func() {
				if r := recover(); r != nil {
					ok = false
				}
			}()
			return val.GetAttrCtx(&vm.execCtx, val.TypeVal(meta), val.DunderPrepare), true
		}
		if prepCallable, ok := deferCatch(); ok && !prepCallable.IsNone() && prepCallable.HeaderPtr() != nil {
			prepArgs := []val.Value{nameVal, val.NewTupleVal(resolvedBases...)}
			var prepKWNames []val.Value
			if kwdsDict != nil {
				for _, item := range kwdsDict.Items() {
					prepArgs = append(prepArgs, item.Value)
					prepKWNames = append(prepKWNames, item.Key)
				}
			}
			namespace = vm.Vectorcall(prepCallable, prepArgs, prepKWNames)
		}
	}
	if namespace.IsNone() || namespace.HeaderPtr() == nil {
		namespace = val.NewDictVal(16)
	}

	// Step 4: Class Body Execution & Zero-Arg super() (__class__ Cell)
	var co *CodeObject
	if classBody.Kind() == val.KindCode {
		co = AsCodeObject(classBody)
	} else if classBody.IsFunction() {
		co = AsCodeObject(classBody.AsFunction().Code)
	} else {
		panic("TypeError: class body must be code object or function")
	}

	var bodyCells []val.Value
	var classCell val.Value
	classCellIdx := -1

	if len(co.CellVars) > 0 {
		bodyCells = make([]val.Value, len(co.CellVars))
		for i, cv := range co.CellVars {
			c := val.NewCellVal(val.UnboundVal())
			bodyCells[i] = c
			if cv.AsString() == "__class__" {
				classCell = c
				classCellIdx = i
			}
		}
	}

	if classCellIdx >= 0 {
		if namespace.IsDict() {
			namespace.AsDict().Set(val.InternString("__classcell__"), classCell)
		}
	}

	if globals == nil {
		globals = vm.globals
	}
	vm.runClassBodyWithLocals(co, globals, namespace, bodyCells)

	// Step 5: Class Creation via Metaclass
	createArgs := []val.Value{nameVal, val.NewTupleVal(resolvedBases...), namespace}
	var createKWNames []val.Value
	if kwdsDict != nil {
		for _, item := range kwdsDict.Items() {
			createArgs = append(createArgs, item.Value)
			createKWNames = append(createKWNames, item.Key)
		}
	}

	if metaCallable.HeaderPtr() == nil && meta != nil {
		metaCallable = val.TypeVal(meta)
	}
	newClsVal := vm.Vectorcall(metaCallable, createArgs, createKWNames)

	// Step 6: Bind __class__ Closure Cell
	if classCellIdx >= 0 {
		bodyCells[classCellIdx].AsCell().Value = newClsVal
	}
	if namespace.IsDict() {
		if cc, ok := namespace.AsDict().Get(val.InternString("__classcell__")); ok {
			if cc.IsCell() {
				cc.AsCell().Value = newClsVal
			}
		}
	}

	// Step 7: Descriptor Notification (__set_name__)
	if newClsVal.IsType() {
		newCls := newClsVal.AsType()
		if newCls.Dict != nil {
			for _, item := range newCls.Dict.Items() {
				attrVal := item.Value
				if !attrVal.IsNone() && attrVal.HeaderPtr() != nil {
					if setNameFn, _, ok := attrVal.Type().LookupMRO(val.DunderSetName); ok {
						vm.Vectorcall(setNameFn, []val.Value{attrVal, newClsVal, item.Key}, nil)
					}
				}
			}
		}

		// Step 8: Subclass Notification (__init_subclass__)
		if len(newCls.Bases) > 0 {
			if initSubclassFn, _, ok := newCls.LookupMROAfter(newCls, val.DunderInitSubclass); ok {
				initArgs := []val.Value{newClsVal}
				var initKWNames []val.Value
				if kwdsDict != nil {
					for _, item := range kwdsDict.Items() {
						initArgs = append(initArgs, item.Value)
						initKWNames = append(initKWNames, item.Key)
					}
				}
				vm.Vectorcall(initSubclassFn, initArgs, initKWNames)
			}
		}

		if hasMROEntries {
			newCls.Dict.Set(val.DunderOrigBases, val.NewTupleVal(origBases...))
		}
	}

	return newClsVal
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
		res := callable.AsBuiltinFunc().Fn(ctx, callable, ctx.Stack[argBase:argBase+nargs], kwnames)
		ctx.SP = savedSP
		return res

	case val.KindFunction:
		fn := callable.AsFunction()
		co := AsCodeObject(fn.Code)
		fnGlobals := fn.Globals
		if fnGlobals == nil {
			fnGlobals = vm.globals
		}
		if co.IsGenerator {
			return vm.instantiateGenerator(co, callable, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames, fnGlobals)
		}
		if co.IsCoroutine {
			return vm.instantiateCoroutine(co, callable, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames, fnGlobals)
		}
		spAfterBind := vm.bindFunctionArgs(co, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames)
		savedSP := ctx.SP
		cells := vm.setupCalleeCells(co, callable, ctx.Stack, argBase, nargs)
		res := vm.runLoopWithGlobals(co, argBase, spAfterBind, vm.activeFrameIdx, cells, fnGlobals, val.NoneVal(), ctx.Stack, nil, nil)
		ctx.SP = savedSP
		return res

	case val.KindCode:
		co := AsCodeObject(callable)
		if co.IsGenerator {
			return vm.instantiateGenerator(co, callable, nil, nil, ctx.Stack, argBase, nargs, kwnames, vm.globals)
		}
		if co.IsCoroutine {
			return vm.instantiateCoroutine(co, callable, nil, nil, ctx.Stack, argBase, nargs, kwnames, vm.globals)
		}
		spAfterBind := vm.bindFunctionArgs(co, nil, nil, ctx.Stack, argBase, nargs, kwnames)
		savedSP := ctx.SP
		cells := vm.setupCalleeCells(co, callable, ctx.Stack, argBase, nargs)
		res := vm.runLoopWithGlobals(co, argBase, spAfterBind, vm.activeFrameIdx, cells, vm.globals, val.NoneVal(), ctx.Stack, nil, nil)
		ctx.SP = savedSP
		return res

	case val.KindBoundMethod:
		bm := callable.AsBoundMethod()
		copy(ctx.Stack[argBase+1:argBase+1+nargs], ctx.Stack[argBase:argBase+nargs])
		ctx.Stack[argBase] = bm.Self
		return vm.callFromHook(ctx, bm.Func, argBase, nargs+1, kwnames)

	default:
		if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
			savedSP := ctx.SP
			ctx.SP = argBase + nargs
			res := tpCall(ctx, callable, ctx.Stack[argBase:argBase+nargs], kwnames)
			ctx.SP = savedSP
			return res
		}
		panic("vm: TypeError: '" + callable.Type().Name + "' object is not callable")
	}
}

// bindFunctionArgs binds positional, keyword, and default arguments in-place on stack[argBase:]
// with 0 heap allocations, returning the initial sp for the callee frame.
func (vm *VM) bindFunctionArgs(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, stack []val.Value, argBase int, nargs int, kwnames []val.Value) int {
	numParams := co.NumParams
	numLocals := co.NumLocals
	if numLocals < numParams {
		numLocals = numParams
	}

	paramNames := co.ParamNames
	varKWIdx := -1
	varArgsIdx := -1
	posParamCount := 0
	for p := 0; p < len(paramNames); p++ {
		pnStr := paramNames[p].AsString()
		if strings.HasPrefix(pnStr, "**") {
			varKWIdx = p
		} else if strings.HasPrefix(pnStr, "*") {
			if pnStr != "*" {
				varArgsIdx = p
			}
		} else {
			posParamCount++
		}
	}

	if len(kwnames) == 0 {
		if varArgsIdx >= 0 {
			if nargs > posParamCount {
				extra := make([]val.Value, nargs-posParamCount)
				copy(extra, stack[argBase+posParamCount:argBase+nargs])
				stack[argBase+varArgsIdx] = val.NewTupleVal(extra...)
			} else {
				stack[argBase+varArgsIdx] = val.NewTupleVal()
			}
		}
		if varKWIdx >= 0 {
			stack[argBase+varKWIdx] = val.NewDictVal(4)
		}
		if nargs < posParamCount && len(defaults) > 0 {
			defStart := posParamCount - len(defaults)
			for i := nargs; i < posParamCount; i++ {
				if i >= defStart {
					stack[argBase+i] = defaults[i-defStart]
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
	copy(vm.kwScratch[:kwCount], stack[argBase+posCount:argBase+nargs])
	for i := 0; i < numParams; i++ {
		vm.kwSet[i] = false
	}
	for i := 0; i < posCount && i < numParams; i++ {
		vm.kwSet[i] = true
	}

	var kwDict *val.PyDict
	for i := 0; i < kwCount; i++ {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := 0; p < len(paramNames); p++ {
			if p == varArgsIdx || p == varKWIdx {
				continue
			}
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		if foundIdx >= 0 {
			if foundIdx < posCount || vm.kwSet[foundIdx] {
				panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
			}
			stack[argBase+foundIdx] = kwVal
			vm.kwSet[foundIdx] = true
		} else if varKWIdx >= 0 {
			if kwDict == nil {
				kwDict = val.NewDictVal(4).AsDict()
			}
			kwDict.Set(kwName, kwVal)
		} else {
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
	}

	if varKWIdx >= 0 {
		if kwDict == nil {
			stack[argBase+varKWIdx] = val.NewDictVal(4)
		} else {
			stack[argBase+varKWIdx] = val.DictVal(kwDict)
		}
		vm.kwSet[varKWIdx] = true
	}

	if varArgsIdx >= 0 {
		if posCount > posParamCount {
			extra := make([]val.Value, posCount-posParamCount)
			copy(extra, stack[argBase+posParamCount:argBase+posCount])
			stack[argBase+varArgsIdx] = val.NewTupleVal(extra...)
		} else {
			stack[argBase+varArgsIdx] = val.NewTupleVal()
		}
		vm.kwSet[varArgsIdx] = true
	}

	defStart := posParamCount - len(defaults)
	for i := 0; i < len(paramNames); i++ {
		if i == varArgsIdx || i == varKWIdx {
			continue
		}
		if !vm.kwSet[i] {
			if i >= defStart && i < posParamCount && len(defaults) > 0 {
				stack[argBase+i] = defaults[i-defStart]
			} else if kwDefaults != nil && i < len(paramNames) {
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					stack[argBase+i] = v
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

func (vm *VM) setupCalleeCells(callee *CodeObject, target val.Value, stack []val.Value, newBase int, argc int) []val.Value {
	nCells := len(callee.CellVars)
	nFree := len(callee.FreeVars)
	if nCells == 0 && nFree == 0 {
		return nil
	}
	if nCells == 0 && target.IsFunction() {
		return target.AsFunction().Closure
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

func (vm *VM) initBuiltins() {
	rangeVal := val.NewBuiltinFuncVal("range", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
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
	})
	vm.builtinRange = rangeVal
	vm.builtins.Set(val.InternString("range"), rangeVal)

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
		if a.IsBytes() {
			return val.Int64Val(int64(len(a.AsBytes().Data)))
		}
		if a.IsByteArray() {
			return val.Int64Val(int64(len(a.AsByteArray().Data)))
		}
		if a.IsMemoryView() {
			return val.Int64Val(int64(len(a.AsMemoryView().Buf)))
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
	vm.builtins.Set(val.InternString("bytes"), val.TypeVal(val.BytesType))
	vm.builtins.Set(val.InternString("bytearray"), val.TypeVal(val.ByteArrayType))
	vm.builtins.Set(val.InternString("memoryview"), val.TypeVal(val.MemoryViewType))
	vm.builtins.Set(val.InternString("slice"), val.TypeVal(val.SliceType))
	vm.builtins.Set(val.InternString("type"), val.TypeVal(val.TypeType))
	vm.builtins.Set(val.InternString("classmethod"), val.TypeVal(val.ClassMethodType))
	vm.builtins.Set(val.InternString("staticmethod"), val.TypeVal(val.StaticMethodType))
	vm.builtins.Set(val.InternString("NotImplemented"), val.NotImplementedVal())

	vm.builtins.Set(val.InternString("super"), val.NewBuiltinFuncVal("super", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 2 {
			if !args[0].IsType() {
				panic("TypeError: super() argument 1 must be type, not " + args[0].Type().Name)
			}
			return val.NewSuperVal(args[0].AsType(), args[1])
		}
		if len(args) == 1 {
			if !args[0].IsType() {
				panic("TypeError: super() argument 1 must be type, not " + args[0].Type().Name)
			}
			return val.NewSuperVal(args[0].AsType(), val.NoneVal())
		}
		if len(args) == 0 {
			vmInstance := ctx.VM.(*VM)
			co := vmInstance.activeCO
			if co == nil {
				panic("RuntimeError: super(): no active code object")
			}
			base := vmInstance.activeBaseOffset
			activeCells := vmInstance.activeCells

			if base >= len(vmInstance.stack) {
				panic("RuntimeError: super(): no arguments")
			}
			self := vmInstance.stack[base]
			if self.IsNone() && self.HeaderPtr() == nil {
				panic("RuntimeError: super(): no arguments")
			}

			var thisClass *val.PyType
			found := false

			for i, fv := range co.FreeVars {
				if fv.AsString() == "__class__" {
					idx := len(co.CellVars) + i
					if idx < len(activeCells) {
						c := activeCells[idx]
						if c.IsCell() && !c.AsCell().Value.IsUnbound() && c.AsCell().Value.IsType() {
							thisClass = c.AsCell().Value.AsType()
							found = true
							break
						}
					}
				}
			}
			if !found {
				for i, cv := range co.CellVars {
					if cv.AsString() == "__class__" {
						if i < len(activeCells) {
							c := activeCells[i]
							if c.IsCell() && !c.AsCell().Value.IsUnbound() && c.AsCell().Value.IsType() {
								thisClass = c.AsCell().Value.AsType()
								found = true
								break
							}
						}
					}
				}
			}

			if !found || thisClass == nil {
				panic("RuntimeError: super(): __class__ cell not found")
			}

			return val.NewSuperVal(thisClass, self)
		}
		panic(fmt.Sprintf("TypeError: super() takes at most 2 arguments (%d given)", len(args)))
	}))

	vm.builtins.Set(val.InternString("__build_class__"), val.NewBuiltinFuncVal("__build_class__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __build_class__: not enough arguments")
		}
		classBody := args[0]
		nameVal := args[1]
		var bases []val.Value
		var kwdsDict *val.PyDict
		if len(kwnames) > 0 {
			kwdsDict = val.NewDictVal(len(kwnames)).AsDict()
			kwStart := len(args) - len(kwnames)
			if kwStart > 2 {
				bases = args[2:kwStart]
			}
			for i, kn := range kwnames {
				kwdsDict.Set(kn, args[kwStart+i])
			}
		} else if len(args) > 2 {
			bases = args[2:]
		}
		vmInstance := ctx.VM.(*VM)
		return vmInstance.buildClass(classBody, nameVal, bases, val.DictVal(kwdsDict), vmInstance.globals)
	}))

	vm.builtins.Set(val.InternString("dir"), val.NewBuiltinFuncVal("dir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			vmInstance := ctx.VM.(*VM)
			namesMap := make(map[string]bool)
			co := vmInstance.activeCO
			if co != nil {
				for _, p := range co.ParamNames {
					namesMap[p.AsString()] = true
				}
				for _, c := range co.CellVars {
					namesMap[c.AsString()] = true
				}
				for _, f := range co.FreeVars {
					namesMap[f.AsString()] = true
				}
			}
			var sortedNames []string
			for k := range namesMap {
				sortedNames = append(sortedNames, k)
			}
			sort.Strings(sortedNames)
			var listItems []val.Value
			for _, k := range sortedNames {
				listItems = append(listItems, val.InternString(k))
			}
			return val.NewListVal(listItems...)
		}

		obj := args[0]
		tp := obj.Type()
		if dirFn, _, ok := tp.LookupMRO(val.DunderDir); ok {
			return ctx.Invoke1(dirFn, obj)
		}

		namesMap := make(map[string]bool)
		for _, base := range tp.MRO {
			if base.Dict != nil {
				for _, item := range base.Dict.Items() {
					if item.Key.IsAnyString() {
						namesMap[item.Key.AsString()] = true
					}
				}
			}
		}
		if obj.IsInstance() {
			inst := obj.AsInstance()
			for cur := inst.Shape; cur != nil; cur = cur.Parent {
				if !cur.AttrName.IsNone() && cur.AttrName.AsString() != "" {
					namesMap[cur.AttrName.AsString()] = true
				}
			}
		} else if obj.IsType() {
			t := obj.AsType()
			for _, base := range t.MRO {
				if base.Dict != nil {
					for _, item := range base.Dict.Items() {
						if item.Key.IsAnyString() {
							namesMap[item.Key.AsString()] = true
						}
					}
				}
			}
		} else if obj.IsModule() {
			mod := obj.AsModule()
			if mod.Dict != nil {
				for _, item := range mod.Dict.Items() {
					if item.Key.IsAnyString() {
						namesMap[item.Key.AsString()] = true
					}
				}
			}
		}
		var sortedNames []string
		for k := range namesMap {
			sortedNames = append(sortedNames, k)
		}
		sort.Strings(sortedNames)
		var listItems []val.Value
		for _, k := range sortedNames {
			listItems = append(listItems, val.InternString(k))
		}
		return val.NewListVal(listItems...)
	}))
	vm.builtins.Set(val.InternString("open"), val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: open() takes at least 1 argument")
		}
		file := args[0].AsString()
		mode := "r"
		if len(args) >= 2 && args[1].IsAnyString() {
			mode = args[1].AsString()
		}
		return openFileBuiltin(file, mode)
	}))

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
	vm.builtins.Set(val.InternString("StopAsyncIteration"), val.TypeVal(val.StopAsyncIterationType))
	vm.builtins.Set(val.InternString("GeneratorExit"), val.TypeVal(val.GeneratorExitType))
	vm.builtins.Set(val.InternString("BufferError"), val.TypeVal(val.BufferErrorType))
	vm.builtins.Set(val.InternString("AssertionError"), val.TypeVal(val.AssertionErrorType))
	vm.builtins.Set(val.InternString("BaseExceptionGroup"), val.TypeVal(val.BaseExceptionGroupType))
	vm.builtins.Set(val.InternString("ExceptionGroup"), val.TypeVal(val.ExceptionGroupType))
	vm.builtins.Set(val.InternString("set"), val.NewBuiltinFuncVal("set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.NewSetVal()
		}
		arg := args[0]
		if arg.IsSet() {
			return arg
		}
		if arg.IsList() {
			return val.NewSetVal(arg.AsList().Items...)
		}
		if arg.IsTuple() {
			return val.NewSetVal(arg.AsTuple().Items...)
		}
		var items []val.Value
		if arg.IsIter() {
			it := arg.AsIter()
			for {
				v, ok := it.Next()
				if !ok {
					break
				}
				items = append(items, v)
			}
		}
		return val.NewSetVal(items...)
	}))
	vm.builtins.Set(val.InternString("TypeVar"), val.TypeVal(val.TypeVarType))
	vm.builtins.Set(val.InternString("TypeVarTuple"), val.TypeVal(val.TypeVarTupleType))
	vm.builtins.Set(val.InternString("ParamSpec"), val.TypeVal(val.ParamSpecType))
	vm.builtins.Set(val.InternString("TypeAliasType"), val.TypeVal(val.TypeAliasTypeType))

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

func (vm *VM) instantiateGenerator(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) val.Value {
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
	spAfterBind := vm.bindFunctionArgsToSlice(co, defaults, kwDefaults, gen.Stack, callerStack[argBase:argBase+nargs], kwnames)
	gen.SP = spAfterBind
	gen.Cells = vm.setupCalleeCellsSlice(co, callable, gen.Stack, 0, nargs)
	gen.ResumeHook = vm.resumeGenerator
	return genVal
}

func (vm *VM) acquireCoroutine(name string, codeVal val.Value, maxStack int, globals *val.PyDict) (*val.PyCoroutine, val.Value) {
	if maxStack < 8 {
		maxStack = 8
	}
	if len(vm.coroFreeList) > 0 {
		n := len(vm.coroFreeList) - 1
		coro := vm.coroFreeList[n]
		vm.coroFreeList = vm.coroFreeList[:n]
		if cap(coro.Stack) < maxStack {
			coro.Stack = make([]val.Value, maxStack)
		} else {
			coro.Stack = coro.Stack[:maxStack]
		}
		for i := range coro.Stack {
			coro.Stack[i] = val.NoneVal()
		}
		coro.Name = name
		coro.Code = codeVal
		coro.State = val.CoroCreated
		coro.SP = 0
		coro.Base = 0
		coro.IP = 0
		coro.Cells = nil
		coro.Globals = globals
		coro.YieldVal = val.NoneVal()
		coro.RetVal = val.NoneVal()
		coro.LastSent = val.NoneVal()
		return coro, val.ObjectVal(&coro.Header)
	}

	coroVal := val.NewCoroutineVal(name, codeVal, maxStack, nil, globals)
	coro := coroVal.AsCoroutine()
	coro.ResumeHook = vm.resumeCoroutine
	return coro, coroVal
}

func (vm *VM) releaseCoroutine(coro *val.PyCoroutine) {
	if coro == nil {
		return
	}
	coro.Globals = nil
	coro.Cells = nil
	coro.YieldVal = val.NoneVal()
	coro.RetVal = val.NoneVal()
	coro.LastSent = val.NoneVal()
	if len(vm.coroFreeList) < 128 {
		vm.coroFreeList = append(vm.coroFreeList, coro)
	}
}

func (vm *VM) instantiateCoroutine(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) val.Value {
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
	coro, coroVal := vm.acquireCoroutine(co.Name, codeVal, maxStack, globals)
	spAfterBind := vm.bindFunctionArgsToSlice(co, defaults, kwDefaults, coro.Stack, callerStack[argBase:argBase+nargs], kwnames)
	coro.SP = spAfterBind
	coro.Cells = vm.setupCalleeCellsSlice(co, callable, coro.Stack, 0, nargs)
	return coroVal
}

func (vm *VM) bindFunctionArgsToSlice(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, dst []val.Value, src []val.Value, kwnames []val.Value) int {
	numParams := co.NumParams
	numLocals := co.NumLocals
	if numLocals < numParams {
		numLocals = numParams
	}
	nargs := len(src)

	paramNames := co.ParamNames
	varKWIdx := -1
	varArgsIdx := -1
	posParamCount := 0
	for p := 0; p < len(paramNames); p++ {
		pnStr := paramNames[p].AsString()
		if strings.HasPrefix(pnStr, "**") {
			varKWIdx = p
		} else if strings.HasPrefix(pnStr, "*") {
			if pnStr != "*" {
				varArgsIdx = p
			}
		} else {
			posParamCount++
		}
	}

	if len(kwnames) == 0 {
		copy(dst[:nargs], src)
		if varArgsIdx >= 0 {
			if nargs > posParamCount {
				extra := make([]val.Value, nargs-posParamCount)
				copy(extra, src[posParamCount:nargs])
				dst[varArgsIdx] = val.NewTupleVal(extra...)
			} else {
				dst[varArgsIdx] = val.NewTupleVal()
			}
		}
		if varKWIdx >= 0 {
			dst[varKWIdx] = val.NewDictVal(4)
		}
		if nargs < posParamCount && len(defaults) > 0 {
			defStart := posParamCount - len(defaults)
			for i := nargs; i < posParamCount; i++ {
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
	for i := 0; i < numParams; i++ {
		vm.kwSet[i] = false
	}
	for i := 0; i < posCount && i < numParams; i++ {
		vm.kwSet[i] = true
	}

	var kwDict *val.PyDict
	for i := 0; i < kwCount; i++ {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := 0; p < len(paramNames); p++ {
			if p == varArgsIdx || p == varKWIdx {
				continue
			}
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		if foundIdx >= 0 {
			if foundIdx < posCount || vm.kwSet[foundIdx] {
				panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
			}
			dst[foundIdx] = kwVal
			vm.kwSet[foundIdx] = true
		} else if varKWIdx >= 0 {
			if kwDict == nil {
				kwDict = val.NewDictVal(4).AsDict()
			}
			kwDict.Set(kwName, kwVal)
		} else {
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
	}

	if varKWIdx >= 0 {
		if kwDict == nil {
			dst[varKWIdx] = val.NewDictVal(4)
		} else {
			dst[varKWIdx] = val.DictVal(kwDict)
		}
		vm.kwSet[varKWIdx] = true
	}

	if varArgsIdx >= 0 {
		if posCount > posParamCount {
			extra := make([]val.Value, posCount-posParamCount)
			copy(extra, src[posParamCount:posCount])
			dst[varArgsIdx] = val.NewTupleVal(extra...)
		} else {
			dst[varArgsIdx] = val.NewTupleVal()
		}
		vm.kwSet[varArgsIdx] = true
	}

	defStart := posParamCount - len(defaults)
	for i := 0; i < len(paramNames); i++ {
		if i == varArgsIdx || i == varKWIdx {
			continue
		}
		if !vm.kwSet[i] {
			if i >= defStart && i < posParamCount && len(defaults) > 0 {
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
	if nCells == 0 && target.IsFunction() {
		return target.AsFunction().Closure
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
	res := vm.runLoopWithGlobals(co, gen.Base, gen.SP, 0, gen.Cells, gen.Globals, val.NoneVal(), gen.Stack, gen, nil)
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

func (vm *VM) resumeCoroutine(ctx *val.ExecContext, coro *val.PyCoroutine, sentVal val.Value) (val.Value, val.IterStatus) {
	if coro.State == val.CoroClosed {
		return coro.RetVal, val.IterExhausted
	}
	if coro.State == val.CoroRunning {
		if ctx != nil {
			ctx.Raise(val.NewValueError("coroutine already executing"))
		}
		return val.NoneVal(), val.IterError
	}
	if coro.State == val.CoroCreated {
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("can't send non-None value to a just-started coroutine"))
			}
			return val.NoneVal(), val.IterError
		}
	} else if coro.State == val.CoroSuspended {
		coro.LastSent = sentVal
		co := AsCodeObject(coro.Code)
		if coro.IP < len(co.Code) && Opcode(co.Code[coro.IP]>>24) != OP_AWAIT && Opcode(co.Code[coro.IP]>>24) != OP_YIELD_FROM {
			coro.Stack[coro.SP] = sentVal
			coro.SP++
		}
	}

	coro.State = val.CoroRunning
	co := AsCodeObject(coro.Code)
	res := vm.runLoopWithGlobals(co, coro.Base, coro.SP, 0, coro.Cells, coro.Globals, val.NoneVal(), coro.Stack, nil, coro)
	if coro.State == val.CoroSuspended {
		return res, val.IterYielded
	}
	if coro.State == val.CoroClosed {
		return coro.RetVal, val.IterExhausted
	}
	if !vm.lastException.IsNone() && vm.lastException.HeaderPtr() != nil {
		if ctx != nil {
			ctx.Raise(vm.lastException)
		}
		return val.NoneVal(), val.IterError
	}
	return res, val.IterExhausted
}

func (vm *VM) unwindException(exc val.Value, curCO **CodeObject, ip *int, frameIdx *int, startFrameIdx int, baseOffset *int, sp *int, code *[]uint32, consts *[]val.Value, cells *[]val.Value, globals **val.PyDict, curLocals *val.Value, stack []val.Value, curIC *[]ICSlot, curMethodPIC *[][4]val.MethodPICEntry, curAttrPIC *[][4]AttrPICEntry) bool {
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
				cache := vm.getCodeCache(co)
				*curIC = cache.ic
				*curMethodPIC = cache.methodPIC
				*curAttrPIC = cache.attrPIC
				return true
			}
		}

		if *frameIdx > startFrameIdx {
			(*frameIdx)--
			caller := &vm.frames[*frameIdx]
			*curCO = caller.co
			*code = caller.code
			*consts = caller.consts
			*baseOffset = caller.baseOffset
			*cells = caller.cells
			*globals = caller.globals
			if curLocals != nil {
				*curLocals = caller.locals
			}
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
	return vm.runLoopWithGlobals(&vm.rootCO, startBase, startSP, startFrameIdx, nil, vm.globals, val.NoneVal(), vm.stack, nil, nil)
}

func ensureStack(stack []val.Value, minCap int) []val.Value {
	if minCap <= len(stack) {
		return stack
	}
	newCap := len(stack) * 2
	if newCap < minCap {
		newCap = minCap + 256
	}
	newStack := make([]val.Value, newCap)
	copy(newStack, stack)
	return newStack
}

func (vm *VM) runLoopWithGlobals(co *CodeObject, startBase int, startSP int, startFrameIdx int, startCells []val.Value, startGlobals *val.PyDict, startLocals val.Value, stack []val.Value, activeGen *val.PyGenerator, activeCoro *val.PyCoroutine) val.Value {
	savedCtxStack := vm.execCtx.Stack
	vm.execCtx.Stack = stack
	savedActiveCO := vm.activeCO
	savedActiveBaseOffset := vm.activeBaseOffset
	savedActiveCells := vm.activeCells
	defer func() {
		vm.execCtx.Stack = savedCtxStack
		vm.activeCO = savedActiveCO
		vm.activeBaseOffset = savedActiveBaseOffset
		vm.activeCells = savedActiveCells
	}()

	curCO := co
	code := curCO.Code
	consts := curCO.Consts
	frameIdx := startFrameIdx
	baseOffset := startBase
	sp := startSP
	ip := 0
	if activeGen != nil && activeGen.IP > 0 {
		ip = activeGen.IP
	} else if activeCoro != nil && activeCoro.IP > 0 {
		ip = activeCoro.IP
	}
	cells := startCells
	vm.activeCO = curCO
	vm.activeBaseOffset = baseOffset
	vm.activeCells = cells
	globals := startGlobals
	if globals == nil {
		globals = vm.globals
	}
	curLocals := startLocals

	if baseOffset+256 > len(stack) {
		stack = ensureStack(stack, baseOffset+256)
		vm.execCtx.Stack = stack
		if activeCoro != nil {
			activeCoro.Stack = stack
		} else if activeGen != nil {
			activeGen.Stack = stack
		} else {
			vm.stack = stack
		}
	}
	locals := stack[baseOffset : baseOffset+256 : baseOffset+256]
	_ = locals[255]

	curCodeCache := vm.getCodeCache(curCO)
	curIC := curCodeCache.ic
	curMethodPIC := curCodeCache.methodPIC
	curAttrPIC := curCodeCache.attrPIC

	maybeOptimizeCodeObject(curCO)

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
			bx := uint16(inst)
			if bx < 256 {
				stack[sp] = locals[uint8(bx)]
			} else {
				stack[sp] = stack[baseOffset+int(bx)]
			}
			sp++
			ip++

		case OP_STORE_FAST:
			bx := uint16(inst)
			sp--
			valToStore := stack[sp]
			if valToStore.IsString() {
				valToStore.AsStringObj().Buf = nil
			}
			if bx < 256 {
				locals[uint8(bx)] = valToStore
			} else {
				stack[baseOffset+int(bx)] = valToStore
			}
			ip++

		case OP_BINARY_ADD:
			b := stack[sp-1]
			a := stack[sp-2]
			if a.IsInt64() && b.IsInt64() {
				// Quickening: specialize in-place to OP_BINARY_ADD_INT
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_ADD_INT)<<24))
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_ADD_FLOAT)<<24))
				stack[sp-2] = val.Float64Val(a.AsFloat64() + b.AsFloat64())
			} else if a.IsAnyString() && b.IsAnyString() {
				if a.IsString() {
					psA := a.AsStringObj()
					bLen := b.StringLen()
					if psA.Buf != nil && cap(psA.Buf) >= len(psA.Buf)+bLen {
						psA.Buf, _ = val.AppendValueBytes(psA.Buf, b)
						psA.Data = unsafe.String(unsafe.SliceData(psA.Buf), len(psA.Buf))
						psA.Header.Hash = 0
						sp--
						ip++
						continue
					}
				}
				stack[sp-2] = val.ConcatStrings(a, b)
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_ADD)<<24))
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_ADD)<<24))
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_SUB_INT)<<24))
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_BINARY_SUB)<<24))
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
				lst := obj.AsList()
				k := key.AsInt64()
				if k >= 0 && k < int64(len(lst.Items)) {
					stack[sp-2] = lst.Items[k]
					sp--
					ip++
					continue
				}
				val, ok := lst.Get(int(k))
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
			key := stack[sp-1]
			obj := stack[sp-2]
			valToStore := stack[sp-3]
			if obj.IsList() && key.IsInt64() {
				lst := obj.AsList()
				k := key.AsInt64()
				if k >= 0 && k < int64(len(lst.Items)) {
					lst.Items[k] = valToStore
					sp -= 3
					ip++
					continue
				}
				if !lst.Set(int(k), valToStore) {
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_COMPARE_LT_INT)<<24))
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
				atomic.StoreUint32(&code[ip], (inst&0x00FFFFFF)|(uint32(OP_COMPARE_LT)<<24))
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
			target := int(uint16(inst))
			if target <= ip {
				maybeOptimizeCodeObject(curCO)
			}
			ip = target

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

			// Builtin range fast-path: 0 allocations
			if target == vm.builtinRange && argc == 1 && stack[newBase].IsInt64() {
				stop := stack[newBase].AsInt64()
				if stop >= 0 && stop <= 0x7FFFFFFF {
					stack[retSP] = val.RangeVal(0, int32(stop))
					sp = retSP + 1
					ip++
					continue
				}
			}

			// Builtin str fast-path: 0 allocations for int64 and strings
			if target.IsType() && target.AsType() == val.StrType && argc == 1 {
				arg := stack[newBase]
				if arg.IsInt64() {
					stack[retSP] = val.IntToStringVal(arg.AsInt64())
					sp = retSP + 1
					ip++
					continue
				}
				if arg.IsAnyString() {
					stack[retSP] = arg
					sp = retSP + 1
					ip++
					continue
				}
			}

			switch target.Kind() {
			case val.KindCode:
				callee := AsCodeObject(target)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, nil, nil, stack, newBase, argc, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, target, nil, nil, stack, newBase, argc, nil, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				maybeOptimizeCodeObject(callee)
				cells = vm.setupCalleeCells(callee, target, stack, newBase, argc)
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < argc {
					numLocalsCallee = argc
				}
				sp = newBase + numLocalsCallee
				curCodeCache = vm.getCodeCache(curCO)
				curIC = curCodeCache.ic
				curMethodPIC = curCodeCache.methodPIC
				curAttrPIC = curCodeCache.attrPIC
				ip = 0
				continue

			case val.KindFunction:
				fn := target.AsFunction()
				callee := AsCodeObject(fn.Code)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				if argc == callee.NumParams && len(fn.Defaults) == 0 {
					numLocalsCallee := callee.NumLocals
					if numLocalsCallee < argc {
						numLocalsCallee = argc
					}
					sp = newBase + numLocalsCallee
				} else {
					sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, stack, newBase, argc, nil)
				}
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				if len(callee.CellVars) == 0 && fn.Closure != nil {
					cells = fn.Closure
				} else if len(callee.CellVars) == 0 && len(callee.FreeVars) == 0 {
					cells = nil
				} else {
					cells = vm.setupCalleeCells(callee, target, stack, newBase, argc)
				}
				if fn.Globals != nil {
					globals = fn.Globals
				}
				curCodeCache = vm.getCodeCache(curCO)
				curIC = curCodeCache.ic
				curMethodPIC = curCodeCache.methodPIC
				curAttrPIC = curCodeCache.attrPIC
				ip = 0
				continue

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
					genVal := vm.instantiateGenerator(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, target, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				maybeOptimizeCodeObject(callee)
				cells = vm.setupCalleeCells(callee, target, stack, newBase, totalArgs)
				if fn.Globals != nil {
					globals = fn.Globals
				}
				ip = 0

			case val.KindCode:
				callee := AsCodeObject(target)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, target, nil, nil, stack, newBase, totalArgs, kwnames, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				sp = vm.bindFunctionArgs(callee, nil, nil, stack, newBase, totalArgs, kwnames)
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				maybeOptimizeCodeObject(callee)
				cells = vm.setupCalleeCells(callee, target, stack, newBase, totalArgs)
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
				instShape := instObj.Shape
				instType := instObj.Header.Type
				pic := &curMethodPIC[ip]
				if pic[0].Shape == instShape && instType.VersionTag == pic[0].TypeVersion {
					stack[sp] = pic[0].TargetVal
					sp++
					ip++
					continue
				}
				if pic[1].Shape == instShape && instType.VersionTag == pic[1].TypeVersion {
					stack[sp] = pic[1].TargetVal
					sp++
					ip++
					continue
				}
				if pic[2].Shape == instShape && instType.VersionTag == pic[2].TypeVersion {
					stack[sp] = pic[2].TargetVal
					sp++
					ip++
					continue
				}
				if pic[3].Shape == instShape && instType.VersionTag == pic[3].TypeVersion {
					stack[sp] = pic[3].TargetVal
					sp++
					ip++
					continue
				}
				entry := curIC[ip]
				if instShape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
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
			} else if obj.IsModule() {
				mod := obj.AsModule()
				entry := &curIC[ip]
				modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
				if entry.Kind == ICKindModuleAttr && entry.ExpectedGlobalsVer == modVer {
					stack[sp-1] = val.UnboundVal()
					stack[sp] = entry.CachedVal
					sp++
					ip++
					continue
				}
				attrName := consts[uint16(inst)]
				if v, slot, ok := mod.Dict.GetWithSlotHint(attrName, entry.SlotIndex); ok {
					entry.Kind = ICKindModuleAttr
					entry.ExpectedGlobalsVer = modVer
					entry.CachedVal = v
					entry.SlotIndex = slot
					stack[sp-1] = val.UnboundVal()
					stack[sp] = v
					sp++
					ip++
					continue
				}
			}
			vm.loadMethodSlow(obj, consts[uint16(inst)], ip, sp, frameIdx, stack, curIC, curMethodPIC)
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
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, callable, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, callable, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				if totalArgs == callee.NumParams && len(fn.Defaults) == 0 {
					numLocalsCallee := callee.NumLocals
					if numLocalsCallee < totalArgs {
						numLocalsCallee = totalArgs
					}
					sp = newBase + numLocalsCallee
				} else {
					sp = vm.bindFunctionArgs(callee, fn.Defaults, fn.KwDefaults, stack, newBase, totalArgs, nil)
				}
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				if len(callee.CellVars) == 0 && fn.Closure != nil {
					cells = fn.Closure
				} else if len(callee.CellVars) == 0 && len(callee.FreeVars) == 0 {
					cells = nil
				} else {
					cells = vm.setupCalleeCells(callee, callable, stack, newBase, totalArgs)
				}
				if fn.Globals != nil {
					globals = fn.Globals
				}
				curCodeCache = vm.getCodeCache(curCO)
				curIC = curCodeCache.ic
				curMethodPIC = curCodeCache.methodPIC
				curAttrPIC = curCodeCache.attrPIC
				ip = 0
				continue

			case val.KindCode:
				callee := AsCodeObject(callable)
				if callee.IsGenerator {
					genVal := vm.instantiateGenerator(callee, callable, nil, nil, stack, newBase, totalArgs, nil, globals)
					stack[retSP] = genVal
					sp = retSP + 1
					ip++
					continue
				}
				if callee.IsCoroutine {
					coroVal := vm.instantiateCoroutine(callee, callable, nil, nil, stack, newBase, totalArgs, nil, globals)
					stack[retSP] = coroVal
					sp = retSP + 1
					ip++
					continue
				}
				saved := &vm.frames[frameIdx]
				saved.co = curCO
				saved.code = code
				saved.consts = consts
				saved.ip = ip + 1
				saved.baseOffset = baseOffset
				saved.retSP = retSP
				saved.cells = cells
				saved.globals = globals
				saved.locals = curLocals
				saved.cache = curCodeCache
				curLocals = val.NoneVal()
				frameIdx++
				curCO = callee
				code = callee.Code
				consts = callee.Consts
				baseOffset = newBase
				if baseOffset+256 > len(stack) {
					stack = ensureStack(stack, baseOffset+256)
					vm.execCtx.Stack = stack
					if activeCoro != nil {
						activeCoro.Stack = stack
					} else if activeGen != nil {
						activeGen.Stack = stack
					} else {
						vm.stack = stack
					}
				}
				locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
				_ = locals[255]
				maybeOptimizeCodeObject(callee)
				cells = vm.setupCalleeCells(callee, callable, stack, newBase, totalArgs)
				numLocalsCallee := callee.NumLocals
				if numLocalsCallee < totalArgs {
					numLocalsCallee = totalArgs
				}
				sp = newBase + numLocalsCallee
				curCodeCache = vm.getCodeCache(curCO)
				curIC = curCodeCache.ic
				curMethodPIC = curCodeCache.methodPIC
				curAttrPIC = curCodeCache.attrPIC
				ip = 0
				continue

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

		case OP_CALL_METHOD_PIC:
			argc := int(uint8(inst >> 16))
			methIdx := uint16(inst)
			newBase := sp - argc - 1
			receiver := stack[newBase]
			retSP := newBase
			totalArgs := argc + 1

			if receiver.IsInstance() {
				instObj := receiver.AsInstance()
				instShape := instObj.Shape
				instType := instObj.Header.Type
				pic := &curMethodPIC[ip]
				var targetFn *val.PyFunction
				var nativeFn val.VectorcallFunc
				var targetVal val.Value
				if pic[0].Shape == instShape && instType.VersionTag == pic[0].TypeVersion {
					targetFn = pic[0].TargetFn
					nativeFn = pic[0].NativeFn
					targetVal = pic[0].TargetVal
				} else if pic[1].Shape == instShape && instType.VersionTag == pic[1].TypeVersion {
					targetFn = pic[1].TargetFn
					nativeFn = pic[1].NativeFn
					targetVal = pic[1].TargetVal
				} else if pic[2].Shape == instShape && instType.VersionTag == pic[2].TypeVersion {
					targetFn = pic[2].TargetFn
					nativeFn = pic[2].NativeFn
					targetVal = pic[2].TargetVal
				} else if pic[3].Shape == instShape && instType.VersionTag == pic[3].TypeVersion {
					targetFn = pic[3].TargetFn
					nativeFn = pic[3].NativeFn
					targetVal = pic[3].TargetVal
				}

				if targetFn != nil {
					callee := AsCodeObject(targetFn.Code)
					if !callee.IsGenerator && !callee.IsCoroutine {
						saved := &vm.frames[frameIdx]
						saved.co = curCO
						saved.code = code
						saved.consts = consts
						saved.ip = ip + 1
						saved.baseOffset = baseOffset
						saved.retSP = retSP
						saved.cells = cells
						saved.globals = globals
						saved.locals = curLocals
						saved.cache = curCodeCache
						curLocals = val.NoneVal()
						frameIdx++
						curCO = callee
						if totalArgs == callee.NumParams && len(targetFn.Defaults) == 0 {
							numLocals := callee.NumLocals
							if numLocals < totalArgs {
								numLocals = totalArgs
							}
							sp = newBase + numLocals
						} else {
							sp = vm.bindFunctionArgs(callee, targetFn.Defaults, targetFn.KwDefaults, stack, newBase, totalArgs, nil)
						}
						code = callee.Code
						consts = callee.Consts
						baseOffset = newBase
						if baseOffset+256 > len(stack) {
							stack = ensureStack(stack, baseOffset+256)
							vm.execCtx.Stack = stack
							if activeCoro != nil {
								activeCoro.Stack = stack
							} else if activeGen != nil {
								activeGen.Stack = stack
							} else {
								vm.stack = stack
							}
						}
						locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
						_ = locals[255]
						if len(callee.CellVars) == 0 && targetFn.Closure != nil {
							cells = targetFn.Closure
						} else if len(callee.CellVars) == 0 && len(callee.FreeVars) == 0 {
							cells = nil
						} else {
							cells = vm.setupCalleeCells(callee, targetVal, stack, newBase, totalArgs)
						}
						if targetFn.Globals != nil {
							globals = targetFn.Globals
						}
						curCodeCache = vm.getCodeCache(curCO)
						curIC = curCodeCache.ic
						curMethodPIC = curCodeCache.methodPIC
						curAttrPIC = curCodeCache.attrPIC
						ip = 0
						continue
					}
				} else if nativeFn != nil {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					res := nativeFn(&vm.execCtx, targetVal, stack[newBase:newBase+totalArgs], nil)
					stack[retSP] = res
					sp = retSP + 1
					ip++
					continue
				}
			}

			vm.callMethodPICSlow(receiver, consts[methIdx], ip, newBase, argc, frameIdx, stack, curMethodPIC)
			sp = retSP + 1
			ip++

		case OP_RETURN:
			sp--
			retVal := stack[sp]
			if activeGen != nil && frameIdx == startFrameIdx {
				activeGen.RetVal = retVal
				activeGen.State = val.GenClosed
				activeGen.SP = sp
				return retVal
			}
			if activeCoro != nil && frameIdx == startFrameIdx {
				activeCoro.RetVal = retVal
				activeCoro.State = val.CoroClosed
				activeCoro.SP = sp
				return retVal
			}
			if frameIdx == startFrameIdx {
				return retVal
			}
			frameIdx--
			caller := &vm.frames[frameIdx]
			curCO = caller.co
			code = caller.code
			consts = caller.consts
			ip = caller.ip
			baseOffset = caller.baseOffset
			locals = stack[baseOffset : baseOffset+256 : baseOffset+256]
			_ = locals[255]
			cells = caller.cells
			globals = caller.globals
			curLocals = caller.locals
			vm.activeCO = curCO
			vm.activeBaseOffset = baseOffset
			vm.activeCells = cells
			stack[caller.retSP] = retVal
			sp = caller.retSP + 1
			if caller.cache != nil {
				curCodeCache = caller.cache
			} else {
				curCodeCache = vm.getCodeCache(curCO)
			}
			curIC = curCodeCache.ic
			curMethodPIC = curCodeCache.methodPIC
			curAttrPIC = curCodeCache.attrPIC
			continue

		case OP_LOAD_ATTR:
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instType := instObj.Header.Type
				if instType.Flags&val.FlagCustomGetAttribute != 0 {
					vm.execCtx.SP = sp
					vm.activeFrameIdx = frameIdx
					vm.activeCO = curCO
					vm.activeBaseOffset = baseOffset
					vm.activeCells = cells
					stack[sp-1] = val.GetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)])
					ip++
					continue
				}
				instShape := instObj.Shape
				pic := &curAttrPIC[ip]
				if pic[0].Shape == instShape {
					v := instObj.Slots[pic[0].SlotIndex]
					if !v.IsUnbound() {
						stack[sp-1] = v
						ip++
						continue
					}
				}
				if pic[1].Shape == instShape {
					v := instObj.Slots[pic[1].SlotIndex]
					if !v.IsUnbound() {
						stack[sp-1] = v
						ip++
						continue
					}
				}
				if pic[2].Shape == instShape {
					v := instObj.Slots[pic[2].SlotIndex]
					if !v.IsUnbound() {
						stack[sp-1] = v
						ip++
						continue
					}
				}
				if pic[3].Shape == instShape {
					v := instObj.Slots[pic[3].SlotIndex]
					if !v.IsUnbound() {
						stack[sp-1] = v
						ip++
						continue
					}
				}
				entry := curIC[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
					if entry.Kind == ICKindInstanceSlot {
						if entry.SlotIndex >= 0 && entry.SlotIndex < len(instObj.Slots) {
							v := instObj.Slots[entry.SlotIndex]
							if !v.IsUnbound() {
								stack[sp-1] = v
								ip++
								continue
							}
						}
					}
					if entry.Kind == ICKindDataDescriptor {
						vm.execCtx.SP = sp
						vm.activeFrameIdx = frameIdx
						vm.activeCO = curCO
						vm.activeBaseOffset = baseOffset
						vm.activeCells = cells
						stack[sp-1] = entry.CachedVal.Type().Slots.TpDescrGet(&vm.execCtx, entry.CachedVal, obj, instType)
						ip++
						continue
					}
				}
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				vm.activeCO = curCO
				vm.activeBaseOffset = baseOffset
				vm.activeCells = cells
				stack[sp-1] = vm.loadAttrInstanceSlow(instObj, obj, consts[uint16(inst)], ip, sp, frameIdx, curIC, curAttrPIC)
			} else if obj.IsModule() {
				mod := obj.AsModule()
				entry := &curIC[ip]
				modVer := atomic.LoadUint64(&mod.Dict.VersionTag)
				if entry.Kind == ICKindModuleAttr && entry.ExpectedGlobalsVer == modVer {
					stack[sp-1] = entry.CachedVal
					ip++
					continue
				}
				attrName := consts[uint16(inst)]
				if v, slot, ok := mod.Dict.GetWithSlotHint(attrName, entry.SlotIndex); ok {
					entry.Kind = ICKindModuleAttr
					entry.ExpectedGlobalsVer = modVer
					entry.CachedVal = v
					entry.SlotIndex = slot
					stack[sp-1] = v
					ip++
					continue
				}
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-1] = val.GetAttrCtx(&vm.execCtx, obj, attrName)
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				stack[sp-1] = val.GetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)])
			}
			ip++

		case OP_STORE_ATTR:
			v := stack[sp-2]
			obj := stack[sp-1]
			if obj.IsInstance() {
				instObj := obj.AsInstance()
				instShape := instObj.Shape
				pic := &curAttrPIC[ip]
				if pic[0].Shape == instShape {
					instObj.Slots[pic[0].SlotIndex] = v
					sp -= 2
					ip++
					continue
				}
				if pic[1].Shape == instShape {
					instObj.Slots[pic[1].SlotIndex] = v
					sp -= 2
					ip++
					continue
				}
				if pic[2].Shape == instShape {
					instObj.Slots[pic[2].SlotIndex] = v
					sp -= 2
					ip++
					continue
				}
				if pic[3].Shape == instShape {
					instObj.Slots[pic[3].SlotIndex] = v
					sp -= 2
					ip++
					continue
				}
				instType := instObj.Header.Type
				entry := curIC[ip]
				if instObj.Shape.ID == entry.ExpectedShapeID && instType.VersionTag == entry.ExpectedTypeVer {
					if entry.Kind == ICKindInstanceSlot {
						if entry.SlotIndex >= 0 && entry.SlotIndex < len(instObj.Slots) {
							instObj.Slots[entry.SlotIndex] = v
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
				vm.storeAttrInstanceSlow(instObj, obj, consts[uint16(inst)], v, ip, sp, frameIdx, curIC, curAttrPIC)
			} else {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				val.SetAttrCtx(&vm.execCtx, obj, consts[uint16(inst)], v)
			}
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
					if activeGen != nil {
						activeGen.State = val.GenClosed
					}
					return val.NoneVal()
				}
				continue
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
			entry := &curIC[ip]
			if entry.ExpectedGlobalsVer == globVer {
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
			if v, slot, ok := globals.GetWithSlotHint(nameVal, entry.SlotIndex); ok {
				entry.Kind = ICKindGlobal
				entry.ExpectedGlobalsVer = globVer
				entry.CachedVal = v
				entry.SlotIndex = slot
				stack[sp] = v
				sp++
				ip++
				continue
			}
			if v, ok := vm.builtins.Get(nameVal); ok {
				entry.Kind = ICKindBuiltin
				entry.ExpectedGlobalsVer = globVer
				entry.ExpectedBuiltinsVer = atomic.LoadUint64(&vm.builtins.VersionTag)
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
			valToStore := stack[sp]
			if valToStore.IsString() {
				valToStore.AsStringObj().Buf = nil
			}
			globals.Set(nameVal, valToStore)
			ip++

		case OP_LOAD_NAME:
			bx := uint16(inst)
			nameVal := consts[bx]
			found := false
			var res val.Value

			if !curLocals.IsNone() && curLocals.HeaderPtr() != nil {
				if curLocals.Type() == val.DictType {
					if v, ok := curLocals.AsDict().Get(nameVal); ok {
						res = v
						found = true
					}
				} else {
					vm.execCtx.SP = sp
					vm.activeFrameIdx = frameIdx
					vm.activeCO = curCO
					vm.activeBaseOffset = baseOffset
					vm.activeCells = cells
					deferCatch := func() (v val.Value, ok bool) {
						defer func() {
							if r := recover(); r != nil {
								ok = false
							}
						}()
						return val.GetItemCtx(&vm.execCtx, curLocals, nameVal), true
					}
					if v, ok := deferCatch(); ok {
						res = v
						found = true
					}
				}
			}

			if !found && globals != nil {
				if v, ok := globals.Get(nameVal); ok {
					res = v
					found = true
				}
			}

			if !found && vm.builtins != nil {
				if v, ok := vm.builtins.Get(nameVal); ok {
					res = v
					found = true
				}
			}

			if !found {
				panic("NameError: name '" + nameVal.AsString() + "' is not defined")
			}

			stack[sp] = res
			sp++
			ip++

		case OP_STORE_NAME:
			bx := uint16(inst)
			nameVal := consts[bx]
			sp--
			valToStore := stack[sp]

			if !curLocals.IsNone() && curLocals.HeaderPtr() != nil {
				if curLocals.Type() == val.DictType {
					curLocals.AsDict().Set(nameVal, valToStore)
				} else {
					vm.execCtx.SP = sp
					vm.activeFrameIdx = frameIdx
					vm.activeCO = curCO
					vm.activeBaseOffset = baseOffset
					vm.activeCells = cells
					val.SetItemCtx(&vm.execCtx, curLocals, nameVal, valToStore)
				}
			} else if globals != nil {
				globals.Set(nameVal, valToStore)
			}
			ip++

		case OP_LOAD_DEREF:
			bx := int(uint16(inst))
			cell := (*val.PyCell)(unsafe.Pointer(cells[bx].HeaderPtr()))
			stack[sp] = cell.Value
			sp++
			ip++

		case OP_STORE_DEREF:
			bx := int(uint16(inst))
			sp--
			valToStore := stack[sp]
			if valToStore.IsString() {
				valToStore.AsStringObj().Buf = nil
			}
			cell := (*val.PyCell)(unsafe.Pointer(cells[bx].HeaderPtr()))
			cell.Value = valToStore
			ip++

		case OP_INC_DEREF_INT:
			bx := int(uint16(inst))
			cell := (*val.PyCell)(unsafe.Pointer(cells[bx].HeaderPtr()))
			if cell.Value.IsInt64() {
				cell.Value = val.Int64Val(cell.Value.AsInt64() + 1)
				ip += 4
				continue
			}
			cell.Value = val.Add(cell.Value, val.Int64Val(1))
			ip += 4

		case OP_LOAD_CLOSURE:
			bx := int(uint16(inst))
			stack[sp] = cells[bx]
			sp++
			ip++

		case OP_IMPORT_NAME:
			bx := uint16(inst)
			nameVal := consts[bx]
			argA := uint8(inst >> 16)
			level := int(argA & 0x7F)
			returnLeaf := (argA & 0x80) != 0

			modVal, err := vm.ImportModule(nameVal.AsString(), level, returnLeaf, globals)
			if err != nil {
				panic("vm: " + err.Error())
			}
			stack[sp] = modVal
			sp++
			ip++

		case OP_IMPORT_FROM:
			bx := uint16(inst)
			symVal := consts[bx]
			modVal := stack[sp-1]

			v, err := vm.ImportFrom(modVal, symVal.AsString(), globals)
			if err != nil {
				panic("vm: " + err.Error())
			}
			stack[sp] = v
			sp++
			ip++

		case OP_IMPORT_STAR:
			sp--
			modVal := stack[sp]
			if err := vm.ImportStar(modVal, globals); err != nil {
				panic("vm: " + err.Error())
			}
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
			var annotateVal val.Value
			if (flags & 0x04) != 0 {
				sp--
				annotateVal = stack[sp]
			}
			callee := AsCodeObject(codeVal)
			fnVal := val.NewFunctionValWithGlobalsAndClosure(callee.Name, codeVal, defaultsSlice, closureSlice, globals)
			if (flags & 0x04) != 0 {
				fn := fnVal.AsFunction()
				if fn.Dict == nil {
					fn.Dict = val.NewDictVal(4).AsDict()
				}
				fn.Dict.Set(val.InternString("__annotate__"), annotateVal)
			}
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
				stack[sp-1] = val.NewIterVal(top)
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
					instNext := atomic.LoadUint32(&code[ip+1])
					if DecodeOpcode(instNext) == OP_STORE_FAST {
						reg := DecodeArgBx(instNext)
						if reg < 256 {
							locals[uint8(reg)] = val.Int64Val(int64(cur))
						} else {
							stack[baseOffset+int(reg)] = val.Int64Val(int64(cur))
						}
						ip += 2
						continue
					}
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
					if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
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

		case OP_BUILD_SLICE:
			count := int(uint16(inst))
			if count == 2 {
				stop := stack[sp-1]
				start := stack[sp-2]
				stack[sp-2] = val.NewSliceVal(start, stop, val.NoneVal())
				sp--
			} else if count == 3 {
				step := stack[sp-1]
				stop := stack[sp-2]
				start := stack[sp-3]
				stack[sp-3] = val.NewSliceVal(start, stop, step)
				sp -= 2
			}
			ip++

		case OP_BUILD_CLASS:
			argA := uint8(inst >> 16)
			var kwdsVal val.Value
			var basesVal val.Value
			var nameVal val.Value
			var classBody val.Value

			if argA == 1 {
				kwdsVal = stack[sp-1]
				basesVal = stack[sp-2]
				nameVal = stack[sp-3]
				classBody = stack[sp-4]
				sp -= 4
			} else {
				kwdsVal = val.NoneVal()
				basesVal = stack[sp-1]
				nameVal = stack[sp-2]
				classBody = stack[sp-3]
				sp -= 3
			}

			var origBases []val.Value
			if basesVal.IsTuple() {
				origBases = basesVal.AsTuple().Items
			} else if basesVal.IsList() {
				origBases = basesVal.AsList().Items
			} else if !basesVal.IsNone() {
				origBases = []val.Value{basesVal}
			}

			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			vm.activeCO = curCO
			vm.activeBaseOffset = baseOffset
			vm.activeCells = cells

			newClsVal := vm.buildClass(classBody, nameVal, origBases, kwdsVal, globals)
			stack[sp] = newClsVal
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
			if a.IsInt64() && b.IsInt64() {
				ai := a.AsInt64()
				bi := b.AsInt64()
				switch cmpOp {
				case CMP_EQ:
					stack[sp-2] = val.BoolVal(ai == bi)
				case CMP_NEQ:
					stack[sp-2] = val.BoolVal(ai != bi)
				case CMP_LT:
					stack[sp-2] = val.BoolVal(ai < bi)
				case CMP_LTE:
					stack[sp-2] = val.BoolVal(ai <= bi)
				case CMP_GT:
					stack[sp-2] = val.BoolVal(ai > bi)
				case CMP_GTE:
					stack[sp-2] = val.BoolVal(ai >= bi)
				default:
					stack[sp-2] = val.BoolVal(false)
				}
				sp--
				ip++
				continue
			}
			var res val.Value
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			vm.activeCO = curCO
			vm.activeBaseOffset = baseOffset
			vm.activeCells = cells
			switch cmpOp {
			case CMP_LT, CMP_LTE, CMP_EQ, CMP_NEQ, CMP_GT, CMP_GTE:
				res = val.RichCompare(&vm.execCtx, a, b, cmpOp)
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
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
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
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
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
				if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
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
			if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
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
			if !subiter.IsIter() && !subiter.IsGenerator() && !subiter.IsCoroutine() {
				if tpIter := subiter.Type().Slots.TpIter; tpIter != nil {
					subiter = tpIter(&vm.execCtx, subiter)
					stack[sp-1] = subiter
				}
			}
			sentVal := val.NoneVal()
			if activeGen != nil {
				sentVal = activeGen.LastSent
				activeGen.LastSent = val.NoneVal()
			} else if activeCoro != nil {
				sentVal = activeCoro.LastSent
				activeCoro.LastSent = val.NoneVal()
			}
			var nextVal val.Value
			var status val.IterStatus
			if subiter.IsGenerator() {
				nextVal, status = subiter.AsGenerator().Send(&vm.execCtx, sentVal)
			} else if subiter.IsCoroutine() {
				nextVal, status = vm.resumeCoroutine(&vm.execCtx, subiter.AsCoroutine(), sentVal)
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
				if activeCoro != nil {
					activeCoro.YieldVal = nextVal
					activeCoro.State = val.CoroSuspended
					activeCoro.SP = sp
					activeCoro.IP = ip
					return nextVal
				}
			} else if status == val.IterExhausted {
				retVal := val.NoneVal()
				if subiter.IsGenerator() {
					retVal = subiter.AsGenerator().RetVal
				} else if subiter.IsCoroutine() {
					retVal = subiter.AsCoroutine().RetVal
					vm.releaseCoroutine(subiter.AsCoroutine())
				}
				stack[sp-1] = retVal
				ip++
			} else {
				if vm.execCtx.HasException() {
					exc := vm.execCtx.ActiveException
					vm.execCtx.ClearException()
					if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
						if activeGen != nil {
							activeGen.State = val.GenClosed
						}
						if activeCoro != nil {
							activeCoro.State = val.CoroClosed
						}
						return val.NoneVal()
					}
					continue
				}
			}

		case OP_GET_AWAITABLE:
			top := stack[sp-1]
			if top.IsCoroutine() || top.IsFuture() || top.IsTask() {
				ip++
				continue
			}
			if top.IsInstance() {
				inst := top.AsInstance()
				if m, ok := inst.GetAttr(val.InternString("__await__")); ok {
					stack[sp-1] = vm.Vectorcall(m, nil, nil)
					ip++
					continue
				}
			}
			if tpIter := top.Type().Slots.TpIter; tpIter != nil {
				stack[sp-1] = tpIter(&vm.execCtx, top)
				ip++
				continue
			}
			exc := val.NewTypeError(fmt.Sprintf("object %s can't be used in 'await' expression", top.Type().Name))
			if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
				if activeCoro != nil {
					activeCoro.State = val.CoroClosed
				}
				return val.NoneVal()
			}
			continue

		case OP_AWAIT:
			aw := stack[sp-1]
			if aw.IsFuture() || aw.IsTask() {
				fut := aw.AsFuture()
				if aw.IsTask() {
					fut = &aw.AsTask().Future
				}
				if fut.IsDone() {
					res, exc := fut.Result()
					if !exc.IsNone() && exc.HeaderPtr() != nil {
						if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
							if activeCoro != nil {
								activeCoro.State = val.CoroClosed
							}
							return val.NoneVal()
						}
						continue
					}
					stack[sp-1] = res
					ip++
					continue
				}
				if activeCoro != nil {
					activeCoro.YieldVal = aw
					activeCoro.State = val.CoroSuspended
					activeCoro.SP = sp
					activeCoro.IP = ip
					return aw
				}
			} else if aw.IsCoroutine() {
				childCoro := aw.AsCoroutine()
				sentVal := val.NoneVal()
				if activeCoro != nil {
					sentVal = activeCoro.LastSent
					activeCoro.LastSent = val.NoneVal()
				}
				_, status := vm.resumeCoroutine(&vm.execCtx, childCoro, sentVal)
				if status == val.IterExhausted {
					stack[sp-1] = childCoro.RetVal
					vm.releaseCoroutine(childCoro)
					ip++
					continue
				} else if status == val.IterYielded {
					if activeCoro != nil {
						activeCoro.YieldVal = childCoro.YieldVal
						activeCoro.State = val.CoroSuspended
						activeCoro.SP = sp
						activeCoro.IP = ip
						return childCoro.YieldVal
					}
				} else {
					if vm.execCtx.HasException() {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
							if activeCoro != nil {
								activeCoro.State = val.CoroClosed
							}
							return val.NoneVal()
						}
						continue
					}
				}
			} else if aw.IsGenerator() {
				subiter := aw.AsGenerator()
				sentVal := val.NoneVal()
				if activeCoro != nil {
					sentVal = activeCoro.LastSent
					activeCoro.LastSent = val.NoneVal()
				}
				nextVal, status := subiter.Send(&vm.execCtx, sentVal)
				if status == val.IterExhausted {
					stack[sp-1] = subiter.RetVal
					ip++
					continue
				} else if status == val.IterYielded {
					if activeCoro != nil {
						activeCoro.YieldVal = nextVal
						activeCoro.State = val.CoroSuspended
						activeCoro.SP = sp
						activeCoro.IP = ip
						return nextVal
					}
				} else {
					if vm.execCtx.HasException() {
						exc := vm.execCtx.ActiveException
						vm.execCtx.ClearException()
						if !vm.unwindException(exc, &curCO, &ip, &frameIdx, startFrameIdx, &baseOffset, &sp, &code, &consts, &cells, &globals, &curLocals, stack, &curIC, &curMethodPIC, &curAttrPIC) {
							if activeCoro != nil {
								activeCoro.State = val.CoroClosed
							}
							return val.NoneVal()
						}
						continue
					}
				}
			} else if tpIterNext := aw.Type().Slots.TpIterNext; tpIterNext != nil {
				nextVal, status := tpIterNext(&vm.execCtx, aw)
				if status == val.IterExhausted {
					stack[sp-1] = nextVal
					ip++
					continue
				} else if status == val.IterYielded {
					if activeCoro != nil {
						activeCoro.YieldVal = nextVal
						activeCoro.State = val.CoroSuspended
						activeCoro.SP = sp
						activeCoro.IP = ip
						return nextVal
					}
				}
			} else {
				ip++
				continue
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

		case OP_NOP:
			ip++

		case OP_FAST_ADD_INT_LOCAL_TO_LOCAL:
			_ = locals[255]
			dstReg := uint8(inst >> 16)
			srcRegA := uint8(inst >> 8)
			srcRegB := uint8(inst)
			va := locals[srcRegA]
			vb := locals[srcRegB]
			if va.IsInt64() && vb.IsInt64() {
				ai := va.AsInt64()
				bi := vb.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					locals[dstReg] = val.Int64Val(sum)
					ip += 4
					continue
				}
				locals[dstReg] = val.Add(va, vb)
				ip += 4
				continue
			}
			DeoptimizeFastAdd(code, ip, dstReg, srcRegA, srcRegB)
			continue

		case OP_FAST_INC_INT_LOCAL_CONST:
			_ = locals[255]
			reg := uint8(inst >> 16)
			constIdx := uint16(inst)
			v := locals[reg]
			c := consts[constIdx]
			if v.IsInt64() && c.IsInt64() {
				vi := v.AsInt64()
				ci := c.AsInt64()
				sum := vi + ci
				if ((vi ^ sum) & (ci ^ sum)) >= 0 {
					locals[reg] = val.Int64Val(sum)
					ip += 4
					continue
				}
				locals[reg] = val.Add(v, c)
				ip += 4
				continue
			}
			DeoptimizeFastInc(code, ip, reg, constIdx)
			continue

		case OP_FAST_LOOP_CMP_LT_CONST_JUMP:
			_ = locals[255]
			reg := uint8(inst >> 16)
			exitTarget := uint16(inst)
			constIdx := uint16(atomic.LoadUint32(&code[ip+1]))
			v := locals[reg]
			c := consts[constIdx]
			if !v.IsInt64() || !c.IsInt64() {
				DeoptimizeFastLoop(code, ip, reg, constIdx, exitTarget)
				continue
			}
			limit := c.AsInt64()
			cur := v.AsInt64()

			// Hot-Loop Register Optimizer:
			// If followed by OP_FAST_INC_INT_LOCAL_CONST (same reg) and back-edge OP_JUMP back to this instruction
			if ip+8 < len(code) {
				inst4 := atomic.LoadUint32(&code[ip+4])
				inst8 := atomic.LoadUint32(&code[ip+8])
				if DecodeOpcode(inst4) == OP_FAST_INC_INT_LOCAL_CONST &&
					DecodeArgA(inst4) == reg &&
					DecodeOpcode(inst8) == OP_JUMP &&
					int(DecodeArgBx(inst8)) == ip {

					stepConstIdx := DecodeArgBx(inst4)
					stepVal := consts[stepConstIdx]
					if stepVal.IsInt64() {
						step := stepVal.AsInt64()
						if step > 0 && cur < limit {
							for cur < limit {
								next := cur + step
								if next < cur {
									break
								}
								cur = next
							}
							locals[reg] = val.Int64Val(cur)
							if cur >= limit {
								ip = int(exitTarget)
								continue
							}
						}
					}
				}
			}

			if cur < limit {
				ip += 4
			} else {
				ip = int(exitTarget)
			}

		case OP_FAST_SUB_INT_LOCAL_TO_LOCAL:
			_ = locals[255]
			dstReg := uint8(inst >> 16)
			srcRegA := uint8(inst >> 8)
			srcRegB := uint8(inst)
			va := locals[srcRegA]
			vb := locals[srcRegB]
			if va.IsInt64() && vb.IsInt64() {
				ai := va.AsInt64()
				bi := vb.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					locals[dstReg] = val.Int64Val(diff)
					ip += 4
					continue
				}
				locals[dstReg] = val.Sub(va, vb)
				ip += 4
				continue
			}
			DeoptimizeFastSub(code, ip, dstReg, srcRegA, srcRegB)
			continue

		case OP_FAST_CMP_EQ_JUMP_IF_FALSE:
			_ = locals[255]
			srcA := uint8(inst >> 16)
			exitTarget := uint16(inst)
			srcB := uint8(atomic.LoadUint32(&code[ip+1]) >> 16)
			va := locals[srcA]
			vb := locals[srcB]
			if va.IsInt64() && vb.IsInt64() {
				if va.AsInt64() != vb.AsInt64() {
					ip = int(exitTarget)
					continue
				}
				ip += 4
				continue
			}
			DeoptimizeFastCmpEq(code, ip, srcA, srcB, exitTarget)
			continue

		case OP_FAST_SUBSCR_DEREF_LIST_LOCAL:
			_ = locals[255]
			dstReg := uint8(inst >> 16)
			cellIdx := uint8(inst >> 8)
			idxReg := uint8(inst)
			if int(cellIdx) < len(cells) {
				cell := (*val.PyCell)(unsafe.Pointer(cells[cellIdx].HeaderPtr()))
				cVal := cell.Value
				if cVal.IsList() {
					iv := locals[idxReg]
					if iv.IsInt64() {
						idx := iv.AsInt64()
						items := cVal.AsList().Items
						if idx >= 0 && idx < int64(len(items)) {
							locals[dstReg] = items[idx]
							ip += 4
							continue
						}
					}
				}
			}
			DeoptimizeFastSubscr(code, ip, dstReg, cellIdx, idxReg)
			continue

		case OP_FAST_NEG_INT_LOCAL:
			_ = locals[255]
			reg := uint8(inst >> 16)
			v := locals[reg]
			if v.IsInt64() {
				locals[reg] = val.Int64Val(-v.AsInt64())
				ip += 3
				continue
			}
			DeoptimizeFastNeg(code, ip, reg)
			continue

		case OP_FORMAT_VALUE:
			conversion := uint8(inst >> 16)
			hasSpec := uint16(inst) != 0
			var specStr string
			if hasSpec {
				specVal := stack[sp-1]
				sp--
				specStr = specVal.AsString()
			}
			valToFormat := stack[sp-1]
			stack[sp-1] = val.FormatValue(valToFormat, specStr, conversion)
			ip++

		case OP_BUILD_STRING:
			count := int(uint16(inst))
			sp -= count
			if count == 0 {
				stack[sp] = val.NewStringVal("")
				sp++
				ip++
				continue
			}
			var b strings.Builder
			for i := 0; i < count; i++ {
				b.WriteString(stack[sp+i].AsString())
			}
			stack[sp] = val.NewStringVal(b.String())
			sp++
			ip++

		case OP_MATCH_SEQUENCE:
			subject := stack[sp-1]
			isSeq := (subject.IsList() || subject.IsTuple()) && !subject.IsString() && !subject.IsBytes()
			stack[sp] = val.BoolVal(isSeq)
			sp++
			ip++

		case OP_MATCH_MAPPING:
			subject := stack[sp-1]
			isMap := subject.IsDict()
			stack[sp] = val.BoolVal(isMap)
			sp++
			ip++

		case OP_MATCH_KEYS:
			keysTuple := stack[sp-1]
			subject := stack[sp-2]
			sp--
			if subject.IsDict() && keysTuple.IsTuple() {
				d := subject.AsDict()
				keys := keysTuple.AsTuple().Items
				var vals []val.Value
				allFound := true
				for _, k := range keys {
					if v, ok := d.Get(k); ok {
						vals = append(vals, v)
					} else {
						allFound = false
						break
					}
				}
				if allFound {
					stack[sp-1] = val.NewTupleVal(vals...)
					stack[sp] = val.BoolVal(true)
					sp++
				} else {
					stack[sp-1] = val.NoneVal()
					stack[sp] = val.BoolVal(false)
					sp++
				}
			} else {
				stack[sp-1] = val.NoneVal()
				stack[sp] = val.BoolVal(false)
				sp++
			}
			ip++

		case OP_MATCH_CLASS:
			nargs := int(uint8(inst >> 16))
			kwTuple := stack[sp-1]
			clsType := stack[sp-2]
			subject := stack[sp-3]
			sp -= 2
			isInst := subject.IsInstance() && clsType.IsType() && val.IsSubclass(subject.Type(), clsType.AsType())
			if isInst {
				instObj := subject.AsInstance()
				var vals []val.Value
				ok := true
				if nargs > 0 {
					if matchArgs, hasMA := clsType.AsType().Dict.Get(val.InternString("__match_args__")); hasMA && matchArgs.IsTuple() {
						maItems := matchArgs.AsTuple().Items
						for i := 0; i < nargs; i++ {
							if i < len(maItems) {
								attrName := maItems[i]
								if v, found := instObj.GetAttr(attrName); found {
									vals = append(vals, v)
								} else {
									ok = false
									break
								}
							}
						}
					}
				}
				if ok && kwTuple.IsTuple() {
					for _, kw := range kwTuple.AsTuple().Items {
						if v, found := instObj.GetAttr(kw); found {
							vals = append(vals, v)
						} else {
							ok = false
							break
						}
					}
				}
				if ok {
					stack[sp-1] = val.NewTupleVal(vals...)
					stack[sp] = val.BoolVal(true)
					sp++
				} else {
					stack[sp-1] = val.NoneVal()
					stack[sp] = val.BoolVal(false)
					sp++
				}
			} else {
				stack[sp-1] = val.NoneVal()
				stack[sp] = val.BoolVal(false)
				sp++
			}
			ip++

		case OP_UNPACK_EX:
			beforeCount := int(uint8(inst >> 16))
			afterCount := int(uint16(inst))
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
			minNeeded := beforeCount + afterCount
			if len(items) < minNeeded {
				panic("vm: ValueError: not enough values to unpack")
			}
			midLen := len(items) - beforeCount - afterCount
			midItems := make([]val.Value, midLen)
			copy(midItems, items[beforeCount:beforeCount+midLen])
			midList := val.NewListVal(midItems...)

			for i := len(items) - 1; i >= len(items)-afterCount; i-- {
				stack[sp] = items[i]
				sp++
			}
			stack[sp] = midList
			sp++
			for i := beforeCount - 1; i >= 0; i-- {
				stack[sp] = items[i]
				sp++
			}
			ip++

		case OP_BINARY_AND:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.BitAnd(&vm.execCtx, a, b)
			ip++

		case OP_BINARY_OR:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.BitOr(&vm.execCtx, a, b)
			ip++

		case OP_BINARY_XOR:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.BitXor(&vm.execCtx, a, b)
			ip++

		case OP_BINARY_LSHIFT:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.LShift(&vm.execCtx, a, b)
			ip++

		case OP_BINARY_RSHIFT:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.RShift(&vm.execCtx, a, b)
			ip++

		case OP_BINARY_MATMUL:
			b := stack[sp-1]
			a := stack[sp-2]
			sp--
			stack[sp-1] = val.MatMul(&vm.execCtx, a, b)
			ip++

		case OP_UNARY_INVERT:
			a := stack[sp-1]
			stack[sp-1] = val.Invert(&vm.execCtx, a)
			ip++

		case OP_BUILD_SET:
			count := int(uint16(inst))
			sp -= count
			setVal := val.NewSetVal(stack[sp : sp+count]...)
			stack[sp] = setVal
			sp++
			ip++

		case OP_SET_ADD:
			offset := int(uint16(inst))
			item := stack[sp-1]
			sp--
			setVal := stack[sp-offset]
			setVal.AsSet().Add(item)
			ip++

		case OP_DELETE_FAST:
			reg := uint16(inst)
			if reg < 256 {
				locals[uint8(reg)] = val.UnboundVal()
			} else {
				stack[baseOffset+int(reg)] = val.UnboundVal()
			}
			ip++

		case OP_DELETE_GLOBAL:
			bx := uint16(inst)
			nameVal := consts[bx]
			globals.Delete(nameVal)
			ip++

		case OP_DELETE_NAME:
			bx := uint16(inst)
			nameVal := consts[bx]
			deleted := false
			if !curLocals.IsNone() && curLocals.HeaderPtr() != nil {
				if curLocals.IsDict() {
					deleted = curLocals.AsDict().Delete(nameVal)
				} else {
					vm.execCtx.SP = sp
					vm.activeFrameIdx = frameIdx
					vm.activeCO = curCO
					vm.activeBaseOffset = baseOffset
					vm.activeCells = cells
					if delItemFn, _, ok := curLocals.Type().LookupMRO(val.InternString("__delitem__")); ok {
						vm.Vectorcall(delItemFn, []val.Value{curLocals, nameVal}, nil)
						deleted = true
					}
				}
			} else if globals != nil {
				deleted = globals.Delete(nameVal)
			}
			if !deleted {
				panic("NameError: name '" + nameVal.AsString() + "' is not defined")
			}
			ip++

		case OP_DELETE_ATTR:
			bx := uint16(inst)
			attrName := consts[bx]
			sp--
			obj := stack[sp]
			if obj.IsInstance() {
				obj.AsInstance().DeleteAttr(attrName)
			}
			ip++

		case OP_DELETE_SUBSCR:
			key := stack[sp-1]
			obj := stack[sp-2]
			sp -= 2
			if obj.IsDict() {
				obj.AsDict().Delete(key)
			}
			ip++

		case OP_CHECK_EXC_MATCH_STAR:
			targetType := stack[sp-1]
			exc := stack[sp-2]
			if exc.IsExceptionGroup() {
				eg := exc.AsExceptionGroup()
				var targetCls *val.PyType
				if targetType.IsType() {
					targetCls = targetType.AsType()
				}
				matched, rest := eg.Split(targetCls)
				if !matched.IsNone() {
					stack[sp-2] = rest
					stack[sp-1] = matched
					stack[sp] = val.BoolVal(true)
					sp++
				} else {
					stack[sp-1] = val.BoolVal(false)
				}
			} else {
				matches := false
				if targetType.IsType() {
					matches = val.IsSubclass(exc.Type(), targetType.AsType())
				}
				if matches {
					stack[sp-2] = val.NoneVal()
					stack[sp-1] = exc
					stack[sp] = val.BoolVal(true)
					sp++
				} else {
					stack[sp-1] = val.BoolVal(false)
				}
			}
			ip++

		case OP_BUILD_MAP_UNPACK:
			count := int(uint16(inst))
			sp -= count
			resVal := val.NewDictVal(8)
			resDict := resVal.AsDict()
			for i := 0; i < count; i++ {
				d := stack[sp+i]
				if d.IsDict() {
					pyD := d.AsDict()
					for _, k := range pyD.Keys() {
						if v, ok := pyD.Get(k); ok {
							resDict.Set(k, v)
						}
					}
				}
			}
			stack[sp] = resVal
			sp++
			ip++

		case OP_LIST_EXTEND:
			offset := int(uint16(inst))
			iterVal := stack[sp-1]
			sp--
			listVal := stack[sp-offset]
			if listVal.IsList() {
				lst := listVal.AsList()
				if iterVal.IsList() {
					lst.Items = append(lst.Items, iterVal.AsList().Items...)
				} else if iterVal.IsTuple() {
					lst.Items = append(lst.Items, iterVal.AsTuple().Items...)
				} else if iterVal.IsSet() {
					lst.Items = append(lst.Items, iterVal.AsSet().Items()...)
				}
			}
			ip++

		case OP_CALL_FUNCTION_EX:
			hasKW := (inst>>16)&0x01 != 0
			var kwDictVal val.Value
			var argsVal val.Value
			var callable val.Value
			if hasKW {
				kwDictVal = stack[sp-1]
				argsVal = stack[sp-2]
				callable = stack[sp-3]
				sp -= 3
			} else {
				argsVal = stack[sp-1]
				callable = stack[sp-2]
				sp -= 2
			}
			var argsSlice []val.Value
			if argsVal.IsTuple() {
				argsSlice = append(argsSlice, argsVal.AsTuple().Items...)
			} else if argsVal.IsList() {
				argsSlice = append(argsSlice, argsVal.AsList().Items...)
			}
			var kwnames []val.Value
			if hasKW && kwDictVal.IsDict() {
				pyD := kwDictVal.AsDict()
				for _, k := range pyD.Keys() {
					if v, ok := pyD.Get(k); ok {
						kwnames = append(kwnames, k)
						argsSlice = append(argsSlice, v)
					}
				}
			}
			res := vm.Vectorcall(callable, argsSlice, kwnames)
			stack[sp] = res
			sp++
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
func (vm *VM) loadMethodSlow(obj, attrName val.Value, ip, sp, frameIdx int, stack []val.Value, curIC []ICSlot, curMethodPIC [][4]val.MethodPICEntry) {
	attrName = val.InternValue(attrName)

	if obj.IsModule() {
		mod := obj.AsModule()
		if fnVal, ok := mod.Dict.Get(attrName); ok {
			stack[sp-1] = val.UnboundVal()
			stack[sp] = fnVal
			return
		}
	}

	if obj.IsSuper() {
		sup := obj.AsSuper()
		selfType := sup.Self.Type()
		descr, _, ok := selfType.LookupMROAfter(sup.ThisClass, attrName)
		if !ok {
			panic("vm: AttributeError: 'super' object has no attribute '" + attrName.AsString() + "'")
		}
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			stack[sp-1] = sup.Self
			stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, sup.Self, selfType)
			stack[sp-1] = val.UnboundVal()
			stack[sp] = res
			return
		}
		stack[sp-1] = val.UnboundVal()
		stack[sp] = descr
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
				stack[sp-1] = val.UnboundVal()
				stack[sp] = res
				return
			}
		}

		if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindInstanceSlot,
				SlotIndex:       slotIdx,
				ExpectedTypeVer: instType.VersionTag,
			}
			stack[sp-1] = val.UnboundVal()
			stack[sp] = instObj.Slots[slotIdx]
			return
		}

		if foundInClass {
			if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
				pic := &curMethodPIC[ip]
				var targetFn *val.PyFunction
				var nativeFn val.VectorcallFunc
				if descr.IsFunction() {
					targetFn = descr.AsFunction()
				} else if descr.IsBuiltinFunc() {
					nativeFn = descr.AsBuiltinFunc().Fn
				}
				newEntry := val.MethodPICEntry{
					Shape:       instObj.Shape,
					TypeVersion: instType.VersionTag,
					TargetFn:    targetFn,
					NativeFn:    nativeFn,
					TargetVal:   descr,
				}
				inserted := false
				for k := 0; k < 4; k++ {
					if pic[k].Shape == nil || pic[k].Shape == instObj.Shape {
						pic[k] = newEntry
						inserted = true
						break
					}
				}
				if !inserted {
					pic[0] = pic[1]
					pic[1] = pic[2]
					pic[2] = pic[3]
					pic[3] = newEntry
				}
				curIC[ip] = ICSlot{
					ExpectedShapeID: instObj.Shape.ID,
					Kind:            ICKindClassMethod,
					ExpectedTypeVer: instType.VersionTag,
					CachedVal:       descr,
				}
				stack[sp-1] = obj
				stack[sp] = descr
				return
			}
			if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrGet(&vm.execCtx, descr, obj, instType)
				stack[sp-1] = val.UnboundVal()
				stack[sp] = res
				return
			}
			stack[sp-1] = val.UnboundVal()
			stack[sp] = descr
			return
		}

		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		stack[sp-1] = val.UnboundVal()
		stack[sp] = res
		return
	}

	if obj.IsType() {
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		stack[sp-1] = val.UnboundVal()
		stack[sp] = res
		return
	}

	// Primitive or built-in heap type method lookup.
	tp := obj.Type()
	if descr, _, ok := tp.LookupMRO(attrName); ok {
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			stack[sp-1] = obj
			stack[sp] = descr
			return
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, obj, tp)
			stack[sp-1] = val.UnboundVal()
			stack[sp] = res
			return
		}
		stack[sp-1] = val.UnboundVal()
		stack[sp] = descr
		return
	}

	panic("vm: AttributeError: '" + tp.Name + "' object has no attribute '" + attrName.AsString() + "'")
}

func (vm *VM) callMethodPICSlow(receiver, attrName val.Value, ip, newBase, argc, frameIdx int, stack []val.Value, curMethodPIC [][4]val.MethodPICEntry) {
	attrName = val.InternValue(attrName)
	totalArgs := argc + 1

	if receiver.IsInstance() {
		instObj := receiver.AsInstance()
		instType := instObj.Header.Type

		if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
			callable := instObj.Slots[slotIdx]
			vm.execCtx.SP = newBase + totalArgs
			vm.activeFrameIdx = frameIdx
			res := vm.callFromHook(&vm.execCtx, callable, newBase+1, argc, nil)
			stack[newBase] = res
			return
		}

		descr, _, foundInClass := instType.LookupMRO(attrName)
		if foundInClass {
			if descr.IsFunction() || descr.IsBuiltinFunc() {
				pic := &curMethodPIC[ip]
				var targetFn *val.PyFunction
				var nativeFn val.VectorcallFunc
				if descr.IsFunction() {
					targetFn = descr.AsFunction()
				} else if descr.IsBuiltinFunc() {
					nativeFn = descr.AsBuiltinFunc().Fn
				}
				newEntry := val.MethodPICEntry{
					Shape:       instObj.Shape,
					TypeVersion: instType.VersionTag,
					TargetFn:    targetFn,
					NativeFn:    nativeFn,
					TargetVal:   descr,
				}
				inserted := false
				for k := 0; k < 4; k++ {
					if pic[k].Shape == nil || pic[k].Shape == instObj.Shape {
						pic[k] = newEntry
						inserted = true
						break
					}
				}
				if !inserted {
					pic[0] = pic[1]
					pic[1] = pic[2]
					pic[2] = pic[3]
					pic[3] = newEntry
				}

				if descr.IsBuiltinFunc() {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					res := descr.AsBuiltinFunc().Fn(&vm.execCtx, descr, stack[newBase:newBase+totalArgs], nil)
					stack[newBase] = res
					return
				}
				if descr.IsFunction() {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					res := vm.callFromHook(&vm.execCtx, descr, newBase, totalArgs, nil)
					stack[newBase] = res
					return
				}
			}
		}

		vm.execCtx.SP = newBase + totalArgs
		vm.activeFrameIdx = frameIdx
		callable := val.GetAttrCtx(&vm.execCtx, receiver, attrName)
		res := vm.callFromHook(&vm.execCtx, callable, newBase+1, argc, nil)
		stack[newBase] = res
		return
	}

	vm.execCtx.SP = newBase + totalArgs
	vm.activeFrameIdx = frameIdx
	callable := val.GetAttrCtx(&vm.execCtx, receiver, attrName)
	if callable.IsBoundMethod() {
		bm := callable.AsBoundMethod()
		stack[newBase] = bm.Self
		res := vm.callFromHook(&vm.execCtx, bm.Func, newBase, totalArgs, nil)
		stack[newBase] = res
		return
	}
	res := vm.callFromHook(&vm.execCtx, callable, newBase+1, argc, nil)
	stack[newBase] = res
}

//go:noinline
func (vm *VM) loadAttrInstanceSlow(instObj *val.PyInstance, obj, attrName val.Value, ip, sp, frameIdx int, curIC []ICSlot, curAttrPIC [][4]AttrPICEntry) val.Value {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	descr, _, foundInClass := instType.LookupMRO(attrName)
	if foundInClass {
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		pic := &curAttrPIC[ip]
		inserted := false
		for k := 0; k < 4; k++ {
			if pic[k].Shape == nil || pic[k].Shape == instObj.Shape {
				pic[k] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
				inserted = true
				break
			}
		}
		if !inserted {
			pic[0] = pic[1]
			pic[1] = pic[2]
			pic[2] = pic[3]
			pic[3] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
		}
		curIC[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
		}
		val, _ := instObj.GetSlot(slotIdx)
		if val.IsUnbound() {
			panic("AttributeError: '" + instType.Name + "' object has no attribute '" + attrName.AsString() + "'")
		}
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
func (vm *VM) storeAttrInstanceSlow(instObj *val.PyInstance, obj, attrName, v val.Value, ip, sp, frameIdx int, curIC []ICSlot, curAttrPIC [][4]AttrPICEntry) {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type

	if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindDataDescriptor,
				ExpectedTypeVer: instType.VersionTag,
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
		pic := &curAttrPIC[ip]
		inserted := false
		for k := 0; k < 4; k++ {
			if pic[k].Shape == nil || pic[k].Shape == instObj.Shape {
				pic[k] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
				inserted = true
				break
			}
		}
		if !inserted {
			pic[0] = pic[1]
			pic[1] = pic[2]
			pic[2] = pic[3]
			pic[3] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
		}
		curIC[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       slotIdx,
			ExpectedTypeVer: instType.VersionTag,
		}
		return
	}

	if instType.Flags&val.FlagForbidDynamicAttrs != 0 {
		vm.execCtx.Raise(val.NewAttributeError("'" + instType.Name + "' object has no attribute '" + attrName.AsString() + "'"))
		return
	}

	slotIdx := instObj.SetAttr(attrName, v)
	pic := &curAttrPIC[ip]
	inserted := false
	for k := 0; k < 4; k++ {
		if pic[k].Shape == nil || pic[k].Shape == instObj.Shape {
			pic[k] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
			inserted = true
			break
		}
	}
	if !inserted {
		pic[0] = pic[1]
		pic[1] = pic[2]
		pic[2] = pic[3]
		pic[3] = AttrPICEntry{Shape: instObj.Shape, SlotIndex: slotIdx}
	}
	curIC[ip] = ICSlot{
		ExpectedShapeID: instObj.Shape.ID,
		Kind:            ICKindInstanceSlot,
		SlotIndex:       slotIdx,
		ExpectedTypeVer: instType.VersionTag,
	}
}
