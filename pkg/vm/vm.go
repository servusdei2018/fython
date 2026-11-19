package vm

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// Run executes the top-level bytecode slice with the given constant pool and local variable count.
// Hot loop state (code, consts, ip, baseOffset, sp, stack) is kept in local variables so the Go
// compiler allocates them to CPU registers across loop iterations.
func (vm *VM) Run(code []uint32, consts []val.Value, numLocals int) (res val.Value) {
	vm.unhandledException = val.NoneVal()
	defer func() {
		if r := recover(); r != nil {
			if re, ok := r.(runtime.Error); ok {
				panic(re)
			}
			if r == ErrTimeoutExceeded || r == ErrStepLimitExceeded || r == ErrMaxStackDepthExceeded {
				panic(r)
			}
			vm.unhandledException = panicToPyException(r)
			res = val.NoneVal()
		}
	}()
	// The root code object is reused across calls, so that the Tier-2 state
	// of an unchanged instruction stream survives. A different stream resets
	// it, because the published instruction slice would not match.
	if !sameCodeStream(vm.rootCO.Code, code) || !sameConstStream(vm.rootCO.Consts, consts) {
		vm.rootCO.Code = code
		vm.rootCO.Consts = consts
		vm.rootCO.NumLocals = numLocals
		vm.rootCO.MaxStackSize = numLocals + 64
		vm.rootCO.IsTier2Optimized = 0
		vm.rootCO.Hotness = 0
		vm.rootCO.stream = nil
		vm.rootCO.ParamShapeComputed = false
		vm.rootCO.computeParamShape()
		vm.rootCache.active = nil
		vm.rootCache.codePtr = nil
		vm.rootCache.codeLen = 0
		vm.rootCache.privateActive = false
		if len(vm.rootCache.ic) > 0 {
			clear(vm.rootCache.ic)
			clear(vm.rootCache.methodPIC)
			clear(vm.rootCache.attrPIC)
			clear(vm.rootCache.kwPIC)
		}
	}
	vm.rootCO.initStream()
	return vm.runFrame(&vm.rootCO, 0, numLocals, 0, nil, vm.globals, val.NoneVal(), vm.stack, nil, nil)
}

// sameCodeStream reports whether two instruction slices are the same buffer.
func sameCodeStream(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// sameConstStream reports whether two constant pools are the same buffer.
func sameConstStream(a, b []val.Value) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// RunCode executes a *CodeObject on the VM.
func (vm *VM) RunCode(co *CodeObject) (res val.Value) {
	vm.unhandledException = val.NoneVal()
	defer func() {
		if r := recover(); r != nil {
			if re, ok := r.(runtime.Error); ok {
				panic(re)
			}
			if r == ErrTimeoutExceeded || r == ErrStepLimitExceeded || r == ErrMaxStackDepthExceeded {
				panic(r)
			}
			vm.unhandledException = panicToPyException(r)
			res = val.NoneVal()
		}
	}()
	return vm.runFrame(co, 0, co.NumLocals, 0, nil, vm.globals, val.NoneVal(), vm.stack, nil, nil)
}

// RunCodeWithGlobals executes a *CodeObject using a custom module globals dictionary.
func (vm *VM) RunCodeWithGlobals(co *CodeObject, globals *val.PyDict) val.Value {
	return vm.runNestedCode(co, globals, val.NoneVal(), nil)
}

func (vm *VM) vmStackTop() int {
	top := 0
	limit := min(vm.activeFrameIdx, len(vm.frames))
	for i := range limit {
		f := &vm.frames[i]
		if f.co != nil && f.stackIsVM {
			end := f.baseOffset + 256
			if f.co.NumLocals+f.co.MaxStackSize > 256 {
				end = f.baseOffset + f.co.NumLocals + f.co.MaxStackSize
			}
			if end > top {
				top = end
			}
			if f.retSP > top {
				top = f.retSP
			}
		}
	}
	if vm.activeCO != nil && len(vm.execCtx.Stack) > 0 && len(vm.stack) > 0 && &vm.execCtx.Stack[0] == &vm.stack[0] {
		end := vm.activeBaseOffset + 256
		if vm.activeCO.NumLocals+vm.activeCO.MaxStackSize > 256 {
			end = vm.activeBaseOffset + vm.activeCO.NumLocals + vm.activeCO.MaxStackSize
		}
		if end > top {
			top = end
		}
		if vm.execCtx.SP > top {
			top = vm.execCtx.SP
		}
	}
	return top
}

func (vm *VM) nextNestedStackAndFrame(extraSlots int) (int, int) {
	if vm.activeCO == nil {
		need := extraSlots + 256
		if need > len(vm.stack) {
			wasVM := len(vm.execCtx.Stack) > 0 && len(vm.stack) > 0 && &vm.execCtx.Stack[0] == &vm.stack[0]
			vm.stack = ensureStack(vm.stack, need)
			if wasVM {
				vm.execCtx.Stack = vm.stack
			}
		}
		return 0, 0
	}
	base := vm.vmStackTop()
	nextFrame := vm.activeFrameIdx + 1
	vm.ensureFrameCapacity(nextFrame + 4)
	need := base + extraSlots + 256
	if need > len(vm.stack) {
		wasVM := len(vm.execCtx.Stack) > 0 && len(vm.stack) > 0 && &vm.execCtx.Stack[0] == &vm.stack[0]
		vm.stack = ensureStack(vm.stack, need)
		if wasVM {
			vm.execCtx.Stack = vm.stack
		}
	}
	return base, nextFrame
}

func (vm *VM) ensureFrameCapacity(minFrames int) {
	if minFrames <= len(vm.frames) {
		return
	}
	newCap := len(vm.frames) * 2
	if newCap < minFrames {
		newCap = minFrames + 32
	}
	oldFrames := vm.frames
	newFrames := vm.engine.acquireFrameSlab(newCap)
	copy(newFrames, oldFrames)
	vm.frames = newFrames
	vm.engine.releaseFrameSlab(oldFrames)
}

func (vm *VM) runNestedCode(co *CodeObject, globals *val.PyDict, locals val.Value, cells []val.Value) val.Value {
	extra := co.NumLocals + max(co.MaxStackSize, len(co.Code)) + 64
	base, nextFrame := vm.nextNestedStackAndFrame(extra)
	savedSP := vm.execCtx.SP
	savedFrameIdx := vm.activeFrameIdx
	vm.unhandledException = val.NoneVal()
	res := vm.runFrame(co, base, base+co.NumLocals, nextFrame, cells, globals, locals, vm.stack, nil, nil)
	vm.execCtx.SP = savedSP
	vm.activeFrameIdx = savedFrameIdx
	return res
}

func (vm *VM) currentGlobals() *val.PyDict {
	if vm.activeState != nil && vm.activeFrameIdx == vm.activeState.frameIdx && vm.activeState.globals != nil {
		return vm.activeState.globals
	}
	if vm.activeCO != nil && vm.activeFrameIdx >= 0 && vm.activeFrameIdx < len(vm.frames) {
		if g := vm.frames[vm.activeFrameIdx].globals; g != nil {
			return g
		}
	}
	return vm.globals
}

func (vm *VM) currentLocalsVal() val.Value {
	vm.flushActiveFrame()
	if vm.activeCO != nil && vm.activeFrameIdx >= 0 && vm.activeFrameIdx < len(vm.frames) {
		f := &vm.frames[vm.activeFrameIdx]
		if !f.locals.IsNone() && f.locals.HeaderPtr() != nil {
			return f.locals
		}
		if f.co != nil && f.co != &vm.rootCO && len(f.co.VarNames) > 0 {
			frameVal := vm.materializeFrame(vm.activeFrameIdx)
			if frameVal.IsFrame() {
				fr := frameVal.AsFrame()
				if !fr.LocalsProxy.IsNone() && fr.LocalsProxy.HeaderPtr() != nil {
					return fr.LocalsProxy
				}
				proxy := &val.PyLocalsProxy{Frame: fr}
				fr.LocalsProxy = val.NewLocalsProxyVal(proxy)
				return fr.LocalsProxy
			}
		}
	}
	return val.NoneVal()
}

func (vm *VM) runClassBodyWithLocals(co *CodeObject, globals *val.PyDict, namespace val.Value, cells []val.Value) {
	vm.runNestedCode(co, globals, namespace, cells)
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		panic(exc)
	}
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
			// Instance-level __mro_entries__ (e.g. assigned on a function
			// object, as typing.NamedTuple does).
			if fnDict := instanceAttrDict(b); fnDict != nil {
				if entryFn, ok := fnDict.Get(val.DunderMroEntries); ok && !entryFn.IsNone() {
					hasMROEntries = true
					entries := vm.Vectorcall(entryFn, []val.Value{val.NewTupleVal(origBases...)}, nil)
					if !entries.IsTuple() {
						panic("TypeError: __mro_entries__ must return a tuple")
					}
					resolvedBases = append(resolvedBases, entries.AsTuple().Items...)
					continue
				}
			}
		}
		resolvedBases = append(resolvedBases, b)
	}

	// Step 2: Determine the Most Derived Metaclass
	var explicitMeta val.Value
	var kwdsDict *val.PyDict
	if kwdsVal.IsDict() {
		kwdsDict = kwdsVal.AsDict()
		if metaV, ok := kwdsDict.Get(nameMetaclass); ok {
			explicitMeta = metaV
			kwdsDict.Delete(nameMetaclass)
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
				switch {
				case val.IsSubclass(bMeta, meta):
					meta = bMeta
				case val.IsSubclass(meta, bMeta):
					// meta is already a subclass of bMeta, keep meta
				default:
					panic("TypeError: metaclass conflict: the metaclass of a derived class must be a (non-strict) subclass of the metaclasses of all its bases")
				}
			}
		}
	}

	// Step 3: __prepare__ Hook
	var namespace val.Value
	if meta != nil {
		if prepCallable, ok := vmGetAttrSafe(&vm.execCtx, val.TypeVal(meta), "__prepare__"); ok && !prepCallable.IsNone() && prepCallable.HeaderPtr() != nil {
			prepArgs := []val.Value{nameVal, val.NewTupleVal(resolvedBases...)}
			var prepKWNames []val.Value
			if kwdsDict != nil {
				for _, item := range kwdsDict.Items() {
					prepArgs = append(prepArgs, item.Value)
					prepKWNames = append(prepKWNames, item.Key)
				}
			}
			namespace = vm.Vectorcall(prepCallable, prepArgs, prepKWNames)
			if vm.execCtx.HasException() {
				return val.NoneVal()
			}
		}
	}
	if namespace.IsNone() || namespace.HeaderPtr() == nil {
		namespace = val.NewDictVal(16)
	}

	// Step 4: Class Body Execution & Zero-Arg super() (__class__ Cell)
	var co *CodeObject
	var closure []val.Value
	switch {
	case classBody.Kind() == val.KindCode:
		co = AsCodeObject(classBody)
	case classBody.IsFunction():
		fn := classBody.AsFunction()
		co = AsCodeObject(fn.Code)
		closure = fn.Closure
	default:
		panic("TypeError: class body must be code object or function")
	}

	var bodyCells []val.Value
	var classCell val.Value
	classCellIdx := -1

	nCells := len(co.CellVars)
	nFree := len(co.FreeVars)
	if nCells+nFree > 0 {
		bodyCells = make([]val.Value, nCells+nFree)
		for i := range nCells {
			c := val.NewCellVal(val.UnboundVal())
			bodyCells[i] = c
			if co.CellVars[i].StringEquals("__class__") {
				classCell = c
				classCellIdx = i
			}
		}
		for j := range nFree {
			if j < len(closure) && closure[j].HeaderPtr() != nil {
				bodyCells[nCells+j] = closure[j]
			} else {
				bodyCells[nCells+j] = val.NewCellVal(val.UnboundVal())
			}
		}
	}

	if classCellIdx >= 0 {
		if namespace.IsDict() {
			namespace.AsDict().Set(nameClassCell, classCell)
		}
	}

	if globals == nil {
		globals = vm.globals
	}
	if globals != nil {
		if modName, ok := globals.Get(nameDunderName); ok {
			if namespace.IsDict() {
				namespace.AsDict().Set(nameModule, modName)
			} else {
				val.SetItemCtx(&vm.execCtx, namespace, nameModule, modName)
			}
		}
	}
	qualName := co.QualName
	if qualName == "" {
		qualName = co.Name
	}
	if qualName != "" {
		if namespace.IsDict() {
			namespace.AsDict().Set(nameQualname, val.InternString(qualName))
		} else {
			val.SetItemCtx(&vm.execCtx, namespace, nameQualname, val.InternString(qualName))
		}
	}
	vm.runClassBodyWithLocals(co, globals, namespace, bodyCells)

	// PEP 487: __init_subclass__ and __class_getitem__ defined in a class body
	// are implicit classmethods.
	if namespace.IsDict() {
		wrapImplicitClassMethods(namespace.AsDict())
	}

	// PEP 560: __orig_bases__ must be visible to __init_subclass__ (typing.Generic).
	if hasMROEntries && namespace.IsDict() {
		namespace.AsDict().Set(val.DunderOrigBases, val.NewTupleVal(origBases...))
	}

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
		if cc, ok := namespace.AsDict().Get(nameClassCell); ok {
			if cc.IsCell() {
				cc.AsCell().Value = newClsVal
			}
		}
	}

	if newClsVal.IsType() && hasMROEntries {
		newClsVal.AsType().Dict.Set(val.DunderOrigBases, val.NewTupleVal(origBases...))
	}

	return newClsVal
}

func wrapImplicitClassMethods(ns *val.PyDict) {
	for _, name := range []string{"__init_subclass__", "__class_getitem__"} {
		key := val.InternString(name)
		v, ok := ns.Get(key)
		if !ok || v.IsClassMethod() || v.IsStaticMethod() {
			continue
		}
		if v.IsFunction() || v.IsBuiltinFunc() {
			ns.Set(key, val.NewClassMethodVal(v))
		}
	}
}

// Vectorcall invokes a Python or native callable using a contiguous argument slice and optional keyword names.
func (vm *VM) Vectorcall(callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	base, _ := vm.nextNestedStackAndFrame(len(args) + 64)
	savedStack := vm.execCtx.Stack
	onAltStack := len(savedStack) > 0 && len(vm.stack) > 0 && &savedStack[0] != &vm.stack[0]
	if onAltStack {
		vm.execCtx.Stack = vm.stack
		defer func() {
			vm.execCtx.Stack = savedStack
		}()
	}
	copy(vm.stack[base:base+len(args)], args)
	return vm.callFromHook(&vm.execCtx, callable, base, len(args), kwnames)
}

// Call invokes a callable with positional arguments.
func (vm *VM) Call(callable val.Value, args ...val.Value) val.Value {
	return vm.Vectorcall(callable, args, nil)
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
			return vm.instantiateGenerator(co, callable, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames, fnGlobals, false)
		}
		if co.IsCoroutine {
			return vm.instantiateCoroutine(co, callable, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames, fnGlobals)
		}
		need := argBase + co.NumLocals + co.MaxStackSize + 256
		if need > len(ctx.Stack) {
			if len(ctx.Stack) > 0 && len(vm.stack) > 0 && &ctx.Stack[0] == &vm.stack[0] {
				vm.stack = ensureStack(vm.stack, need)
				ctx.Stack = vm.stack
			} else {
				ctx.Stack = ensureStack(ctx.Stack, need)
			}
		}
		spAfterBind := vm.bindFunctionArgs(co, fn.Defaults, fn.KwDefaults, ctx.Stack, argBase, nargs, kwnames)
		if ctx.HasException() {
			return val.NoneVal()
		}
		savedSP := ctx.SP
		savedFrameIdx := vm.activeFrameIdx
		nextFrame := 0
		if vm.activeCO != nil {
			nextFrame = vm.activeFrameIdx + 1
		}
		vm.ensureFrameCapacity(nextFrame + 4)
		cells := vm.setupCalleeCells(co, callable, ctx.Stack, argBase, nargs)
		vm.unhandledException = val.NoneVal()
		res := vm.runFrame(co, argBase, spAfterBind, nextFrame, cells, fnGlobals, val.NoneVal(), ctx.Stack, nil, nil)
		ctx.SP = savedSP
		vm.activeFrameIdx = savedFrameIdx
		if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
			exc := vm.unhandledException
			vm.unhandledException = val.NoneVal()
			ctx.Raise(exc)
			return val.NoneVal()
		}
		return res

	case val.KindCode:
		co := AsCodeObject(callable)
		if co.IsGenerator {
			return vm.instantiateGenerator(co, callable, nil, nil, ctx.Stack, argBase, nargs, kwnames, vm.globals, false)
		}
		if co.IsCoroutine {
			return vm.instantiateCoroutine(co, callable, nil, nil, ctx.Stack, argBase, nargs, kwnames, vm.globals)
		}
		need := argBase + co.NumLocals + co.MaxStackSize + 256
		if need > len(ctx.Stack) {
			if len(ctx.Stack) > 0 && len(vm.stack) > 0 && &ctx.Stack[0] == &vm.stack[0] {
				vm.stack = ensureStack(vm.stack, need)
				ctx.Stack = vm.stack
			} else {
				ctx.Stack = ensureStack(ctx.Stack, need)
			}
		}
		spAfterBind := vm.bindFunctionArgs(co, nil, nil, ctx.Stack, argBase, nargs, kwnames)
		if ctx.HasException() {
			return val.NoneVal()
		}
		savedSP := ctx.SP
		savedFrameIdx := vm.activeFrameIdx
		nextFrame := 0
		if vm.activeCO != nil {
			nextFrame = vm.activeFrameIdx + 1
		}
		vm.ensureFrameCapacity(nextFrame + 4)
		cells := vm.setupCalleeCells(co, callable, ctx.Stack, argBase, nargs)
		vm.unhandledException = val.NoneVal()
		res := vm.runFrame(co, argBase, spAfterBind, nextFrame, cells, vm.globals, val.NoneVal(), ctx.Stack, nil, nil)
		ctx.SP = savedSP
		vm.activeFrameIdx = savedFrameIdx
		if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
			exc := vm.unhandledException
			vm.unhandledException = val.NoneVal()
			ctx.Raise(exc)
			return val.NoneVal()
		}
		return res

	case val.KindBoundMethod:
		bm := callable.AsBoundMethod()
		if argBase+1+nargs >= len(ctx.Stack) {
			if len(ctx.Stack) > 0 && len(vm.stack) > 0 && &ctx.Stack[0] == &vm.stack[0] {
				vm.stack = ensureStack(vm.stack, argBase+1+nargs+256)
				ctx.Stack = vm.stack
			} else {
				ctx.Stack = ensureStack(ctx.Stack, argBase+1+nargs+256)
			}
		}
		copy(ctx.Stack[argBase+1:argBase+1+nargs], ctx.Stack[argBase:argBase+nargs])
		ctx.Stack[argBase] = bm.Self
		return vm.callFromHook(ctx, bm.Func, argBase, nargs+1, kwnames)

	case val.KindStaticMethod:
		sm := callable.AsStaticMethod()
		return vm.callFromHook(ctx, sm.Callable, argBase, nargs, kwnames)

	default:
		// Instance __call__ is distinct from the type's constructor slot.
		// Builtin types such as partial set Slots.TpCall to build instances
		// and put the instance call in the type dict.
		if callable.IsType() {
			if tpCall := callable.Type().Slots.TpCall; tpCall != nil {
				savedSP := ctx.SP
				ctx.SP = argBase + nargs
				res := tpCall(ctx, callable, ctx.Stack[argBase:argBase+nargs], kwnames)
				ctx.SP = savedSP
				return res
			}
		} else {
			if fn, _, ok := callable.Type().LookupMRO(val.DunderCall); ok && fn.HeaderPtr() != nil && !fn.IsUnbound() {
				if argBase+1+nargs >= len(ctx.Stack) {
					if len(ctx.Stack) > 0 && len(vm.stack) > 0 && &ctx.Stack[0] == &vm.stack[0] {
						vm.stack = ensureStack(vm.stack, argBase+1+nargs+256)
						ctx.Stack = vm.stack
					} else {
						ctx.Stack = ensureStack(ctx.Stack, argBase+1+nargs+256)
					}
				}
				copy(ctx.Stack[argBase+1:argBase+1+nargs], ctx.Stack[argBase:argBase+nargs])
				ctx.Stack[argBase] = callable
				return vm.callFromHook(ctx, fn, argBase, nargs+1, kwnames)
			}
		}
		vm.flushActiveFrame()
		name := ""
		file := ""
		line := 0
		if vm.activeCO != nil {
			name = vm.activeCO.Name
			file = vm.activeCO.FileName
			if vm.activeFrameIdx >= 0 && vm.activeFrameIdx < len(vm.frames) {
				line = vm.activeCO.LineAt(vm.frames[vm.activeFrameIdx].ip)
			}
		}
		ctx.Raise(val.NewException(val.TypeErrorType, fmt.Sprintf("'%s' object is not callable in %s (%s:%d)", callable.Type().Name, name, file, line)))
		return val.NoneVal()
	}
}

// bindFunctionArgs binds positional, keyword, and default arguments in-place on stack[argBase:]
// with 0 heap allocations, returning the initial sp for the callee frame.
func (vm *VM) bindFunctionArgs(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, stack []val.Value, argBase int, nargs int, kwnames []val.Value) int {
	numParams := co.NumParams
	if len(kwnames) == 0 && co.SimplePositionalOnly && nargs == co.PositionalParamCount &&
		len(defaults) == 0 && kwDefaults == nil {
		numLocals := max(co.NumLocals, numParams)
		for i := nargs; i < numLocals; i++ {
			stack[argBase+i] = val.NoneVal()
		}
		return argBase + numLocals
	}
	if !co.ParamShapeComputed {
		co.computeParamShape()
	}
	numLocals := max(co.NumLocals, numParams)

	paramNames := co.ParamNames
	varKWIdx := co.VarKWIdx
	varArgsIdx := co.VarArgsIdx
	posParamCount := co.PosParamCount
	kwOnlyCount := co.KWOnlyArgCount

	if len(kwnames) == 0 && kwOnlyCount == 0 && kwDefaults == nil {
		minPos := posParamCount - len(defaults)
		if co.Name != "" && nargs < minPos {
			panic(fmt.Sprintf("vm: TypeError: %s() missing %d required positional argument(s)", co.Name, minPos-nargs))
		}
		if varArgsIdx >= 0 {
			if nargs > posParamCount {
				stack[argBase+varArgsIdx] = val.NewTupleFromSlice(stack[argBase+posParamCount : argBase+nargs])
			} else {
				stack[argBase+varArgsIdx] = val.EmptyTupleVal()
			}
		} else if co.Name != "" && nargs > posParamCount {
			panic(fmt.Sprintf("vm: TypeError: %s() takes %d positional arguments but %d were given", co.Name, posParamCount, nargs))
		}
		if varKWIdx >= 0 {
			stack[argBase+varKWIdx] = val.NewDictVal(0)
		}
		if nargs < posParamCount && len(defaults) > 0 {
			defStart := posParamCount - len(defaults)
			for i := nargs; i < posParamCount; i++ {
				if i >= defStart {
					stack[argBase+i] = defaults[i-defStart]
				}
			}
		}
		if varArgsIdx < 0 && numLocals < nargs {
			numLocals = nargs
		}
		return argBase + numLocals
	}

	kwCount := len(kwnames)
	posCount := nargs - kwCount
	if posCount < 0 {
		panic("vm: TypeError: invalid keyword argument count")
	}
	if varArgsIdx < 0 && co.Name != "" && posCount > posParamCount {
		panic(fmt.Sprintf("vm: TypeError: %s() takes %d positional arguments but %d were given", co.Name, posParamCount, posCount))
	}

	// Copy keyword argument values into VM scratch buffer before rearranging in-place.
	copy(vm.kwScratch[:kwCount], stack[argBase+posCount:argBase+nargs])
	// Positionals past the named parameters belong to *args. Take them now:
	// keyword-only parameters sit in the slots those extras occupy.
	posBound := min(posCount, posParamCount)
	var extraPos []val.Value
	if varArgsIdx >= 0 && posCount > posParamCount {
		extraPos = make([]val.Value, posCount-posParamCount)
		copy(extraPos, stack[argBase+posParamCount:argBase+posCount])
	}
	for i := range numParams {
		vm.kwSet[i] = false
	}
	for i := 0; i < posBound && i < numParams; i++ {
		vm.kwSet[i] = true
	}

	var kwDict *val.PyDict
	for i := range kwCount {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := range paramNames {
			if p == varArgsIdx || p == varKWIdx {
				continue
			}
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		switch {
		case foundIdx >= 0:
			if foundIdx < posBound || vm.kwSet[foundIdx] {
				panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
			}
			stack[argBase+foundIdx] = kwVal
			vm.kwSet[foundIdx] = true
		case varKWIdx >= 0:
			if kwDict == nil {
				kwDict = val.NewDictVal(4).AsDict()
			}
			kwDict.Set(kwName, kwVal)
		default:
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
	}

	if varKWIdx >= 0 {
		if kwDict == nil {
			stack[argBase+varKWIdx] = val.NewDictVal(0)
		} else {
			stack[argBase+varKWIdx] = val.DictVal(kwDict)
		}
		vm.kwSet[varKWIdx] = true
	}

	if varArgsIdx >= 0 {
		if extraPos != nil {
			stack[argBase+varArgsIdx] = val.NewTupleVal(extraPos...)
		} else {
			stack[argBase+varArgsIdx] = val.NewTupleVal()
		}
		vm.kwSet[varArgsIdx] = true
	}

	defStart := posParamCount - len(defaults)
	for i := range paramNames {
		if i == varArgsIdx || i == varKWIdx {
			continue
		}
		if !vm.kwSet[i] {
			switch {
			case i >= defStart && i < posParamCount && len(defaults) > 0:
				stack[argBase+i] = defaults[i-defStart]
			case kwDefaults != nil && i < len(paramNames):
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					stack[argBase+i] = v
				} else {
					panic("vm: TypeError: missing required argument '" + paramNames[i].AsString() + "' in " + co.Name)
				}
			default:
				panic("vm: TypeError: missing required argument '" + paramNames[i].AsString() + "' in " + co.Name)
			}
		}
	}

	return argBase + numLocals
}

func (vm *VM) setupCalleeCellsWithPool(callee *CodeObject, target val.Value, stack []val.Value, newBase int, argc int, usePool bool) ([]val.Value, []*val.PyCell) {
	nCells := len(callee.CellVars)
	nFree := len(callee.FreeVars)
	if nCells == 0 && nFree == 0 {
		return nil, nil
	}
	if nCells == 0 && target.IsFunction() {
		fn := target.AsFunction()
		return fn.Closure, fn.CellPtrs
	}
	var res []val.Value
	var ptrs []*val.PyCell
	if usePool {
		res, ptrs = vm.acquireGenCellSlices(nCells + nFree)
	} else {
		res = make([]val.Value, nCells+nFree)
		ptrs = make([]*val.PyCell, nCells+nFree)
	}
	if len(callee.CellParamIndices) != nCells {
		callee.InitCellParamIndices()
	}
	boundLimit := len(callee.ParamNames)
	if !target.IsFunction() && argc < boundLimit {
		boundLimit = argc
	}
	for i := range nCells {
		initVal := val.NoneVal()
		if p := callee.CellParamIndices[i]; p >= 0 && p < boundLimit {
			initVal = stack[newBase+p]
		}
		cVal := val.NewCellVal(initVal)
		res[i] = cVal
		ptrs[i] = cVal.AsCell()
	}
	if target.IsFunction() {
		fn := target.AsFunction()
		closure := fn.Closure
		closurePtrs := fn.CellPtrs
		for j := 0; j < nFree && j < len(closure); j++ {
			res[nCells+j] = closure[j]
			if j < len(closurePtrs) && closurePtrs[j] != nil {
				ptrs[nCells+j] = closurePtrs[j]
			} else {
				ptrs[nCells+j] = closure[j].AsCell()
			}
		}
	}
	return res, ptrs
}

func (vm *VM) setupCalleeCellsAndPtrs(callee *CodeObject, target val.Value, stack []val.Value, newBase int, argc int) ([]val.Value, []*val.PyCell) {
	return vm.setupCalleeCellsWithPool(callee, target, stack, newBase, argc, false)
}

func (vm *VM) setupCalleeCellsForFn(callee *CodeObject, fn *val.PyFunction, stack []val.Value, newBase int) ([]val.Value, []*val.PyCell) {
	var target val.Value
	if fn != nil {
		target = val.ObjectVal(&fn.Header)
	}
	return vm.setupCalleeCellsWithPool(callee, target, stack, newBase, len(callee.ParamNames), false)
}

func (vm *VM) setupCalleeCells(callee *CodeObject, target val.Value, stack []val.Value, newBase int, argc int) []val.Value {
	cells, _ := vm.setupCalleeCellsAndPtrs(callee, target, stack, newBase, argc)
	return cells
}

// reclaimViewSlot returns the memory view a slot held to the free list, once
// the slot is overwritten. A view that is still referenced somewhere else is
// kept alive.
func (vm *VM) reclaimViewSlot(old, incoming val.Value) {
	if old.Kind() != val.KindMemoryView || old == incoming {
		return
	}
	mv := old.AsMemoryView()
	if mv.Escaped {
		return
	}
	vm.recycleMemoryView(mv)
}

// reusableViewFor returns the memory view that the slot of the store
// instruction right after the slice already holds, when that view is not
// shared with anybody else. Reusing it keeps the slice loop free of
// allocations and of pointer stores the write barrier would have to record.
//
// The instruction after the slice is inspected, so the reuse only happens
// when the slice result is stored into a local slot right away.
func reusableViewFor(codePtr unsafe.Pointer, ip int, locals []val.Value) *val.PyMemoryView {
	next := *(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4))
	nextOp := DecodeOpcode(next)
	if nextOp != OP_STORE_FAST && nextOp != OP_STORE_FAST_VIEW {
		return nil
	}
	slot := int(DecodeArgBx(next))
	if slot >= len(locals) {
		return nil
	}
	old := locals[slot]
	if old.Kind() != val.KindMemoryView {
		return nil
	}
	mv := old.AsMemoryView()
	if mv.Escaped {
		return nil
	}
	return mv
}

// viewSlotKey identifies the frame slot that owns a memory view.
func viewSlotKey(frameIdx int, slot int) uint32 {
	return uint32(frameIdx)<<16 | uint32(uint16(slot))
}

// markViewEscaped marks a memory view as shared, so that it is never
// recycled while another reference to it is still live.
func markViewEscaped(v val.Value) {
	if v.Kind() == val.KindMemoryView {
		v.AsMemoryView().Escaped = true
	}
}

// markViewsEscaped marks every memory view in vals as shared.
func markViewsEscaped(vals []val.Value) {
	for i := range vals {
		markViewEscaped(vals[i])
	}
}

// allocMemoryView allocates a fresh memoryview on the Go heap.
//
// Memoryviews used to be handed out from a fixed 1024-entry ring, which
// silently overwrote live views once more than 1024 of them existed. Every
// view is now an ordinary heap object, so all of them stay valid for as long
// as they are referenced.
func (vm *VM) allocMemoryView() *val.PyMemoryView {
	if n := len(vm.mvFree); n > 0 {
		mv := vm.mvFree[n-1]
		vm.mvFree = vm.mvFree[:n-1]
		return mv
	}
	return new(val.PyMemoryView)
}

// recycleMemoryView returns an unreferenced memory view to the per-VM free
// list, so that the next slice can reuse it without allocating.
func (vm *VM) recycleMemoryView(mv *val.PyMemoryView) {
	// The pointer fields are left alone: the slice that reuses the view
	// overwrites all of them, and clearing them here would add a write
	// barrier to every recycled view.
	mv.Suboffsets = nil
	mv.NDim = 0
	mv.Dim0 = 0
	mv.Stride0 = 0
	mv.ItemSize = 0
	mv.ReadOnly = false
	mv.Escaped = false
	mv.OwnerKey = 0
	vm.mvFree = append(vm.mvFree, mv)
}

func (vm *VM) initBuiltins() {
	vm.populateAllBuiltins(vm.builtins)
	if isInstVal, ok := vm.builtins.Get(nameIsInstance); ok {
		vm.builtinIsInstance = isInstVal
	}
	if nextVal, ok := vm.builtins.Get(nameBuiltinNext); ok {
		vm.builtinNext = nextVal
	}

	rangeVal := val.TypeVal(val.RangeType)
	vm.builtinRange = rangeVal
	vm.builtins.Set(val.InternString("range"), rangeVal)

	lenVal := val.NewBuiltinFuncVal("len", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			ctx.Raise(val.NewException(val.TypeErrorType, "len() takes 1 argument"))
			return val.NoneVal()
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
			mv := a.AsMemoryView()
			if mv.NDim > 0 {
				return val.Int64Val(int64(mv.Dim(0)))
			}
			if mv.ItemSize > 1 {
				return val.Int64Val(int64(len(mv.Buf) / mv.ItemSize))
			}
			return val.Int64Val(int64(len(mv.Buf)))
		}
		if sqLen := a.Type().Slots.SqLen; sqLen != nil {
			return val.Int64Val(sqLen(ctx, a))
		}
		ctx.Raise(val.NewException(val.TypeErrorType, "object of type '"+a.Type().Name+"' has no len()"))
		return val.NoneVal()
	})
	vm.builtinLen = lenVal
	vm.builtins.Set(val.InternString("len"), lenVal)
	if vm.strScratch == nil {
		vm.strScratch = make([]byte, 0, 1024)
	}

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
				ctx.Raise(val.NewException(val.TypeErrorType, "super() argument 1 must be type, not"+args[0].Type().Name))
				return val.NoneVal()
			}
			return val.NewSuperVal(args[0].AsType(), args[1])
		}
		if len(args) == 1 {
			if !args[0].IsType() {
				ctx.Raise(val.NewException(val.TypeErrorType, "super() argument 1 must be type, not"+args[0].Type().Name))
				return val.NoneVal()
			}
			return val.NewSuperVal(args[0].AsType(), val.NoneVal())
		}
		if len(args) == 0 {
			vmInstance := ctx.VM.(*VM)
			co := vmInstance.activeCO
			if co == nil {
				ctx.Raise(val.NewException(val.RuntimeErrorType, "super(): no active code object"))
				return val.NoneVal()
			}
			base := vmInstance.activeBaseOffset
			activeCells := vmInstance.activeCells

			stack := ctx.Stack
			if len(stack) == 0 {
				stack = vmInstance.stack
			}
			if base >= len(stack) {
				ctx.Raise(val.NewException(val.RuntimeErrorType, "super(): no arguments"))
				return val.NoneVal()
			}
			self := stack[base]
			if self.IsNone() && self.HeaderPtr() == nil {
				ctx.Raise(val.NewException(val.RuntimeErrorType, "super(): no arguments"))
				return val.NoneVal()
			}

			var thisClass *val.PyType
			found := false

			for i, fv := range co.FreeVars {
				if fv.StringEquals("__class__") {
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
					if cv.StringEquals("__class__") {
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
				ctx.Raise(val.NewException(val.RuntimeErrorType, "super(): __class__ cell not found"))
				return val.NoneVal()
			}

			return val.NewSuperVal(thisClass, self)
		}
		ctx.Raise(val.NewException(val.TypeErrorType, fmt.Sprintf("super() takes at most 2 arguments (%d given)", len(args))))
		return val.NoneVal()
	}))

	vm.builtins.Set(val.InternString("__build_class__"), val.NewBuiltinFuncVal("__build_class__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "__build_class__: not enough arguments"))
			return val.NoneVal()
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

	vm.builtins.Set(val.InternString("__build_lazy_import__"), val.NewBuiltinFuncVal("__build_lazy_import__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			ctx.Raise(val.NewException(val.TypeErrorType, "__build_lazy_import__ requires 3 arguments"))
			return val.NoneVal()
		}
		modName := args[0].AsString()
		symName := args[1].AsString()
		level := int(args[2].AsInt64())
		pkgName := ""
		if ctx != nil && ctx.VM != nil {
			vmInstance := ctx.VM.(*VM)
			var activeGlobals *val.PyDict
			if vmInstance.activeState != nil && vmInstance.activeFrameIdx == vmInstance.activeState.frameIdx {
				activeGlobals = vmInstance.activeState.globals
			}
			if activeGlobals == nil && vmInstance.activeFrameIdx >= 0 && vmInstance.activeFrameIdx < len(vmInstance.frames) {
				activeGlobals = vmInstance.frames[vmInstance.activeFrameIdx].globals
			}
			if activeGlobals == nil {
				activeGlobals = vmInstance.globals
			}
			if activeGlobals != nil {
				if v, ok := activeGlobals.Get(namePackage); ok && v.IsAnyString() {
					pkgName = v.AsString()
				} else if v, ok := activeGlobals.Get(nameDunderName); ok && v.IsAnyString() {
					pkgName = v.AsString()
				}
			}
		}
		return val.NewLazyImportVal(modName, symName, level, pkgName)
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
		if dirFn, owner, ok := tp.LookupMRO(val.DunderDir); ok && owner != val.ObjectType {
			res := ctx.Invoke1(dirFn, obj)
			if res.IsList() {
				items := append([]val.Value(nil), res.AsList().Items...)
				sort.SliceStable(items, func(i, j int) bool {
					return items[i].IsAnyString() && items[j].IsAnyString() && items[i].AsString() < items[j].AsString()
				})
				return val.NewListVal(items...)
			}
			return res
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
		switch {
		case obj.IsInstance():
			inst := obj.AsInstance()
			for cur := inst.Shape; cur != nil; cur = cur.Parent {
				if cur.AttrName.IsAnyString() && cur.AttrName.AsString() != "" {
					namesMap[cur.AttrName.AsString()] = true
				}
			}
		case obj.IsType():
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
		case obj.IsModule():
			mod := obj.AsModule()
			if modDirFn, ok := mod.Dict.Get(val.DunderDir); ok && !modDirFn.IsNone() && !modDirFn.IsUnbound() {
				res := ctx.Invoke0(modDirFn)
				items := val.ExtractIterableItems(ctx, res)
				sort.SliceStable(items, func(i, j int) bool {
					return items[i].IsAnyString() && items[j].IsAnyString() && items[i].AsString() < items[j].AsString()
				})
				return val.NewListVal(items...)
			}
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

	vm.builtins.Set(val.InternString("locals"), val.NewBuiltinFuncVal("locals", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		frameIdx := vmInstance.activeFrameIdx
		if frameIdx < 0 || frameIdx >= len(vmInstance.frames) {
			return val.DictVal(vmInstance.globals)
		}
		fVal := vmInstance.materializeFrame(frameIdx)
		if fVal.IsFrame() {
			f := fVal.AsFrame()
			if !f.LocalsProxy.IsNone() {
				return f.LocalsProxy
			}
			proxy := &val.PyLocalsProxy{Frame: f}
			f.LocalsProxy = val.NewLocalsProxyVal(proxy)
			return f.LocalsProxy
		}
		return val.DictVal(vmInstance.globals)
	}))

	vm.builtins.Set(val.InternString("globals"), val.NewBuiltinFuncVal("globals", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		vmInstance := ctx.VM.(*VM)
		if vmInstance.activeState != nil && vmInstance.activeFrameIdx == vmInstance.activeState.frameIdx && vmInstance.activeState.globals != nil {
			return val.DictVal(vmInstance.activeState.globals)
		}
		frameIdx := vmInstance.activeFrameIdx
		if frameIdx >= 0 && frameIdx < len(vmInstance.frames) && vmInstance.frames[frameIdx].globals != nil {
			return val.DictVal(vmInstance.frames[frameIdx].globals)
		}
		return val.DictVal(vmInstance.globals)
	}))

	vm.builtins.Set(val.InternString("open"), val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(kwnames) == 0 && len(args) == 2 && args[0].IsAnyString() && args[1].IsAnyString() {
			return openFileBuiltinCtx(ctx, args[0].AsString(), args[1].AsString())
		}
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			if v, ok := kwArg(args, kwnames, "file"); ok {
				pa = []val.Value{v}
			} else {
				ctx.Raise(val.NewException(val.TypeErrorType, "open() missing required argument 'file' (pos 1)"))
				return val.NoneVal()
			}
		}
		file := pathArg(ctx, pa[0])
		mode := "r"
		if len(pa) >= 2 && pa[1].IsAnyString() {
			mode = pa[1].AsString()
		} else if v, ok := kwArg(args, kwnames, "mode"); ok && v.IsAnyString() {
			mode = v.AsString()
		}
		return openFileBuiltinCtx(ctx, file, mode)
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
	vm.builtins.Set(val.InternString("ReferenceError"), val.TypeVal(val.ReferenceErrorType))
	vm.builtins.Set(val.InternString("AssertionError"), val.TypeVal(val.AssertionErrorType))
	vm.builtins.Set(val.InternString("BaseExceptionGroup"), val.TypeVal(val.BaseExceptionGroupType))
	vm.builtins.Set(val.InternString("ExceptionGroup"), val.TypeVal(val.ExceptionGroupType))
	vm.builtins.Set(val.InternString("TypeVar"), val.TypeVal(val.TypeVarType))
	vm.builtins.Set(val.InternString("TypeVarTuple"), val.TypeVal(val.TypeVarTupleType))
	vm.builtins.Set(val.InternString("ParamSpec"), val.TypeVal(val.ParamSpecType))
	vm.builtins.Set(val.InternString("TypeAliasType"), val.TypeVal(val.TypeAliasTypeType))

	vm.builtins.Set(val.InternString("go"), val.NewBuiltinFuncVal("go", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			ctx.Raise(val.NewException(val.TypeErrorType, "go() takes at least 1 argument"))
			return val.NoneVal()
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
		val.SetMultiThreaded(true)

		go func() {
			workerVM := eng.AcquireVM()
			defer eng.ReleaseVM(workerVM)
			workerVM.globals = globals
			workerVM.builtins = builtins
			defer func() {
				_ = recover()
			}()
			workerVM.Vectorcall(fn, taskArgs, nil)
		}()

		return val.NoneVal()
	}))

	vm.builtins.Set(val.InternString("chan"), val.NewBuiltinFuncVal("chan", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		capacity := 0
		if len(args) > 0 && args[0].IsInt64() {
			capacity = max(int(args[0].AsInt64()), 0)
		}
		return val.NewChanVal(capacity)
	}))
}

func (vm *VM) acquireGenerator(name string, codeVal val.Value, stack []val.Value, poolCls uint8, globals *val.PyDict, ephemeral bool) (*val.PyGenerator, val.Value) {
	var flags uint8
	if ephemeral {
		flags = val.FlagEphemeralGen
	}
	if len(vm.genFreeList) > 0 {
		n := len(vm.genFreeList) - 1
		gen := vm.genFreeList[n]
		vm.genFreeList[n] = nil
		vm.genFreeList = vm.genFreeList[:n]
		gen.Header = val.ObjectHeader{Kind: val.KindGenerator, Flags: flags, Type: val.GeneratorType}
		gen.Name = name
		gen.Code = codeVal
		gen.State = val.GenCreated
		gen.Stack = stack
		gen.SP = 0
		gen.Base = 0
		gen.IP = 0
		gen.MaxSP = 0
		gen.PoolClass = poolCls
		gen.OwnCells = false
		gen.Cells = nil
		gen.CellPtrs = nil
		gen.Globals = globals
		gen.YieldVal = val.NoneVal()
		gen.RetVal = val.NoneVal()
		gen.LastSent = val.NoneVal()
		return gen, val.ObjectVal(&gen.Header)
	}
	genVal := val.NewGeneratorWithStack(name, codeVal, stack, poolCls, nil, globals)
	gen := genVal.AsGenerator()
	gen.Header.Flags = flags
	return gen, genVal
}

func (vm *VM) releaseGenerator(gen *val.PyGenerator) {
	if gen == nil || (gen.Header.Flags&val.FlagEphemeralGen) == 0 || val.MultiThreadedFlag.Load() {
		return
	}
	vm.releaseGeneratorStack(gen)
	gen.Header.Flags &^= val.FlagEphemeralGen
	gen.Name = ""
	gen.Code = val.Value{}
	gen.State = val.GenClosed
	gen.SP = 0
	gen.Base = 0
	gen.IP = 0
	gen.MaxSP = 0
	gen.OwnCells = false
	gen.Cells = nil
	gen.CellPtrs = nil
	gen.Globals = nil
	gen.YieldVal = val.Value{}
	gen.RetVal = val.Value{}
	gen.LastSent = val.Value{}
	if len(vm.genFreeList) < 128 {
		vm.genFreeList = append(vm.genFreeList, gen)
	}
}

func (vm *VM) instantiateGenerator(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict, ephemeral bool) val.Value {
	need := max(256, co.NumLocals+co.MaxStackSize+64)
	if hint := int(co.GenStackHint.Load()); hint > need {
		need = hint
	}
	genStack, poolCls := vm.acquireGeneratorStack(need)
	var codeVal val.Value
	if callable.IsFunction() {
		codeVal = callable.AsFunction().Code
	} else {
		codeVal = callable
	}
	gen, genVal := vm.acquireGenerator(co.Name, codeVal, genStack, poolCls, globals, ephemeral)
	spAfterBind := vm.bindFunctionArgsToSlice(co, defaults, kwDefaults, gen.Stack, callerStack[argBase:argBase+nargs], kwnames)
	gen.SP = spAfterBind
	if len(co.CellVars) > 0 || len(co.FreeVars) > 0 {
		gen.Cells, gen.CellPtrs = vm.setupCalleeCellsWithPool(co, callable, gen.Stack, 0, nargs, true)
		if len(co.CellVars) > 0 || !callable.IsFunction() {
			gen.OwnCells = true
		}
	}
	if vm.genResumeHook != nil {
		gen.ResumeHook = vm.genResumeHook
	} else {
		gen.ResumeHook = vm.resumeGenerator
	}
	if vm.genReleaseHook != nil {
		gen.ReleaseHook = vm.genReleaseHook
	} else {
		gen.ReleaseHook = vm.releaseGeneratorStack
	}
	return genVal
}

func (vm *VM) acquireCoroutine(name string, codeVal val.Value, maxStack int, globals *val.PyDict) (*val.PyCoroutine, val.Value) {
	if maxStack < 256 {
		maxStack = 256
	}
	if len(vm.coroFreeList) > 0 {
		n := len(vm.coroFreeList) - 1
		coro := vm.coroFreeList[n]
		vm.coroFreeList = vm.coroFreeList[:n]
		if cap(coro.Stack) < maxStack {
			coro.Stack = make([]val.Value, maxStack)
		} else {
			coro.Stack = coro.Stack[:cap(coro.Stack)]
		}
		for i := range maxStack {
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
	if vm.coroResumeHook != nil {
		coro.ResumeHook = vm.coroResumeHook
	} else {
		coro.ResumeHook = vm.resumeCoroutine
	}
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

// ReleaseCoroutine returns a closed coroutine to the VM's free list.
func (vm *VM) ReleaseCoroutine(coro *val.PyCoroutine) {
	vm.releaseCoroutine(coro)
}

// StepCoroutine advances a coroutine on this VM's execution context.
func (vm *VM) StepCoroutine(coro *val.PyCoroutine, sent val.Value) (val.Value, val.IterStatus) {
	return vm.resumeCoroutine(&vm.execCtx, coro, sent)
}

// ExecContext returns the execution context for this VM.
func (vm *VM) ExecContext() *val.ExecContext {
	return &vm.execCtx
}

// CallUserExcepthook invokes a user-installed sys.excepthook for an unhandled
// exception.
//
// It reports whether a user override was found and invoked. When it returns
// false the caller is responsible for rendering the traceback itself.
func (vm *VM) CallUserExcepthook(exc val.Value) bool {
	if vm.engine == nil || vm.modules == nil {
		return false
	}
	sysVal, ok := vm.modules.Get(nameSys)
	if !ok || !sysVal.IsModule() {
		return false
	}
	hook, ok := sysVal.AsModule().Dict.Get(nameExcepthook)
	if !ok || hook.IsNone() || hook.HeaderPtr() == nil {
		return false
	}
	// The default hook is also published as sys.__excepthook__, so a user
	// override can be detected by comparing the two.
	if def, ok := sysVal.AsModule().Dict.Get(nameDunderExcepthook); ok &&
		def.HeaderPtr() != nil && hook.HeaderPtr() == def.HeaderPtr() {
		return false
	}
	tb := val.NoneVal()
	if exc.IsInstance() {
		tb, _ = exc.AsInstance().GetAttr(nameTraceback)
	}
	vm.execCtx.SP = 0
	vm.Vectorcall(hook, []val.Value{val.TypeVal(exc.Type()), exc, tb}, nil)
	vm.execCtx.ClearException()
	return true
}

func (vm *VM) instantiateCoroutine(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) val.Value {
	maxStack := max(co.MaxStackSize, co.NumLocals+16)
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
	numLocals := max(co.NumLocals, numParams)
	nargs := len(src)
	if len(kwnames) == 0 && co.SimplePositionalOnly && nargs == co.PositionalParamCount &&
		len(defaults) == 0 && kwDefaults == nil {
		if nargs <= len(dst) {
			copy(dst[:nargs], src)
		} else {
			copy(dst, src[:len(dst)])
		}
		return numLocals
	}
	if !co.ParamShapeComputed {
		co.computeParamShape()
	}

	paramNames := co.ParamNames
	varKWIdx := co.VarKWIdx
	varArgsIdx := co.VarArgsIdx
	posParamCount := co.PosParamCount
	kwOnlyCount := co.KWOnlyArgCount

	if len(kwnames) == 0 && kwOnlyCount == 0 && kwDefaults == nil {
		minPos := posParamCount - len(defaults)
		if co.Name != "" && nargs < minPos {
			panic(fmt.Sprintf("vm: TypeError: %s() missing %d required positional argument(s)", co.Name, minPos-nargs))
		}
		if varArgsIdx < 0 && co.Name != "" && nargs > posParamCount {
			panic(fmt.Sprintf("vm: TypeError: %s() takes %d positional arguments but %d were given", co.Name, posParamCount, nargs))
		}
		if nargs <= len(dst) {
			copy(dst[:nargs], src)
		} else {
			copy(dst, src[:len(dst)])
		}
		if varArgsIdx >= 0 {
			if nargs > posParamCount {
				dst[varArgsIdx] = val.NewTupleFromSlice(src[posParamCount:nargs])
			} else {
				dst[varArgsIdx] = val.EmptyTupleVal()
			}
		}
		if varKWIdx >= 0 {
			dst[varKWIdx] = val.NewDictVal(0)
		}
		if nargs < posParamCount && len(defaults) > 0 {
			defStart := posParamCount - len(defaults)
			for i := nargs; i < posParamCount; i++ {
				if i >= defStart {
					dst[i] = defaults[i-defStart]
				}
			}
		}
		if varArgsIdx < 0 && numLocals < nargs {
			numLocals = nargs
		}
		return numLocals
	}

	kwCount := len(kwnames)
	posCount := nargs - kwCount
	if posCount < 0 {
		panic("vm: TypeError: invalid keyword argument count")
	}
	if varArgsIdx < 0 && co.Name != "" && posCount > posParamCount {
		panic(fmt.Sprintf("vm: TypeError: %s() takes %d positional arguments but %d were given", co.Name, posParamCount, posCount))
	}

	posBound := min(posCount, posParamCount)
	copy(dst[:posBound], src[:posBound])
	copy(vm.kwScratch[:kwCount], src[posCount:nargs])
	for i := range numParams {
		vm.kwSet[i] = false
	}
	for i := 0; i < posBound && i < numParams; i++ {
		vm.kwSet[i] = true
	}

	var kwDict *val.PyDict
	for i := range kwCount {
		kwName := kwnames[i]
		kwVal := vm.kwScratch[i]
		foundIdx := -1
		for p := range paramNames {
			if p == varArgsIdx || p == varKWIdx {
				continue
			}
			pn := paramNames[p]
			if (pn.HeaderPtr() == kwName.HeaderPtr() && pn == kwName) || val.StringEquals(pn, kwName) {
				foundIdx = p
				break
			}
		}
		switch {
		case foundIdx >= 0:
			if foundIdx < posBound || vm.kwSet[foundIdx] {
				panic("vm: TypeError: multiple values for argument '" + kwName.AsString() + "'")
			}
			dst[foundIdx] = kwVal
			vm.kwSet[foundIdx] = true
		case varKWIdx >= 0:
			if kwDict == nil {
				kwDict = val.NewDictVal(4).AsDict()
			}
			kwDict.Set(kwName, kwVal)
		default:
			panic("vm: TypeError: unexpected keyword argument '" + kwName.AsString() + "'")
		}
	}

	if varKWIdx >= 0 {
		if kwDict == nil {
			dst[varKWIdx] = val.NewDictVal(0)
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
	for i := range paramNames {
		if i == varArgsIdx || i == varKWIdx {
			continue
		}
		if !vm.kwSet[i] {
			switch {
			case i >= defStart && i < posParamCount && len(defaults) > 0:
				dst[i] = defaults[i-defStart]
			case kwDefaults != nil && i < len(paramNames):
				if v, ok := kwDefaults.Get(paramNames[i]); ok {
					dst[i] = v
				} else {
					panic("vm: TypeError: missing required argument")
				}
			default:
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
	boundLimit := len(callee.ParamNames)
	if !target.IsFunction() && argc < boundLimit {
		boundLimit = argc
	}
	for i := range nCells {
		name := callee.CellVars[i]
		nameStr := ""
		initVal := val.NoneVal()
		for p := range boundLimit {
			pn := callee.ParamNames[p]
			if val.StringEquals(pn, name) {
				initVal = stack[newBase+p]
				break
			}
			if pn.StringHasBytePrefix('*') {
				pnStr := pn.AsString()
				if len(pnStr) > 1 {
					clean := pnStr[1:]
					if len(clean) > 0 && clean[0] == '*' {
						clean = clean[1:]
					}
					if nameStr == "" {
						nameStr = name.AsString()
					}
					if clean == nameStr {
						initVal = stack[newBase+p]
						break
					}
				}
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
	var thrownExc val.Value
	if ctx != nil && ctx.HasException() {
		thrownExc = ctx.ActiveException
		ctx.ClearException()
		if gen.State == val.GenCreated {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
			ctx.Raise(thrownExc)
			return val.NoneVal(), val.IterError
		}
	}
	if thrownExc.IsNone() || thrownExc.HeaderPtr() == nil {
		switch gen.State {
		case val.GenCreated:
			if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
				if ctx != nil {
					ctx.Raise(val.NewTypeError("can't send non-None value to a just-started generator"))
				}
				return val.NoneVal(), val.IterError
			}
		case val.GenSuspended:
			gen.LastSent = sentVal
			co := AsCodeObject(gen.Code)
			if gen.IP < len(co.Code) && Opcode(co.Code[gen.IP]>>24) != OP_YIELD_FROM {
				gen.Stack[gen.SP] = sentVal
				gen.SP++
			}
		}
	}

	gen.State = val.GenRunning
	co := AsCodeObject(gen.Code)
	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	vm.ensureFrameCapacity(nextFrame + 4)
	vm.unhandledException = val.NoneVal()
	if !thrownExc.IsNone() && thrownExc.HeaderPtr() != nil {
		vm.execCtx.Raise(thrownExc)
	}
	res := vm.runFrame(co, gen.Base, gen.SP, nextFrame, gen.Cells, gen.Globals, val.NoneVal(), gen.Stack, gen, nil)
	if gen.State == val.GenSuspended {
		return res, val.IterYielded
	}
	if gen.State == val.GenClosed && (vm.unhandledException.IsNone() || vm.unhandledException.HeaderPtr() == nil) {
		vm.releaseGeneratorStack(gen)
		return gen.RetVal, val.IterExhausted
	}
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		vm.releaseGeneratorStack(gen)
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		if ctx != nil {
			ctx.Raise(exc)
		} else {
			panic(exc)
		}
		return val.NoneVal(), val.IterError
	}
	return res, val.IterExhausted
}

func (vm *VM) appendJoinItem(v val.Value) {
	idx := vm.joinCount
	vm.joinCount++
	if !v.IsAnyString() {
		if vm.joinErrMsg == "" {
			vm.joinErrMsg = fmt.Sprintf("sequence item %d: expected str instance, %s found", idx, v.Type().Name)
		}
		return
	}
	if vm.joinErrMsg != "" {
		return
	}
	if !vm.joinFirst {
		vm.joinSink.WriteString(vm.joinSep)
	}
	vm.joinFirst = false
	v.WriteToBuilder(vm.joinSink)
}

// joinGenerator drains gen in a single runLoopWithGlobals invocation,
// appending each yielded string into a separator-joined result. Plain
// yields and OP_YIELD_FROM delegation never leave the loop: they append
// straight into the sink. It returns the joined string and true on success.
// On failure it returns false with the exception set on ctx.
func (vm *VM) joinGenerator(ctx *val.ExecContext, sep string, gen *val.PyGenerator) (val.Value, bool) {
	if gen.State == val.GenClosed {
		return val.NewStringVal(""), true
	}
	if gen.State == val.GenRunning {
		if ctx != nil {
			ctx.Raise(val.NewValueError("generator already executing"))
		}
		return val.NoneVal(), false
	}
	if vm.activeJoinGen != nil {
		return val.Value{}, false
	}
	var thrownExc val.Value
	if ctx != nil && ctx.HasException() {
		thrownExc = ctx.ActiveException
		ctx.ClearException()
		if gen.State == val.GenCreated {
			gen.State = val.GenClosed
			vm.releaseGeneratorStack(gen)
			ctx.Raise(thrownExc)
			return val.NoneVal(), false
		}
	}
	vm.joinBuf.Reset()
	if vm.joinBuf.Cap() < 8192 {
		vm.joinBuf.Grow(8192)
	}
	savedGen, savedSink, savedSep, savedFirst, savedCount, savedErr := vm.activeJoinGen, vm.joinSink, vm.joinSep, vm.joinFirst, vm.joinCount, vm.joinErrMsg
	vm.activeJoinGen = gen
	vm.joinSink = &vm.joinBuf
	vm.joinSep = sep
	vm.joinFirst = true
	vm.joinCount = 0
	vm.joinErrMsg = ""
	restoreJoin := func() {
		vm.activeJoinGen, vm.joinSink, vm.joinSep, vm.joinFirst, vm.joinCount, vm.joinErrMsg = savedGen, savedSink, savedSep, savedFirst, savedCount, savedErr
	}
	co := AsCodeObject(gen.Code)
	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	vm.ensureFrameCapacity(nextFrame + 4)
	vm.unhandledException = val.NoneVal()
	switch {
	case !thrownExc.IsNone() && thrownExc.HeaderPtr() != nil:
		vm.execCtx.Raise(thrownExc)
	case gen.State == val.GenSuspended:
		if gen.IP < len(co.Code) && Opcode(co.Code[gen.IP]>>24) != OP_YIELD_FROM {
			gen.Stack[gen.SP] = val.NoneVal()
			gen.SP++
		}
	case gen.State != val.GenCreated:
		restoreJoin()
		return val.Value{}, false
	}
	gen.State = val.GenRunning
	res := vm.runFrame(co, gen.Base, gen.SP, nextFrame, gen.Cells, gen.Globals, val.NoneVal(), gen.Stack, gen, nil)
	_ = res
	if gen.State == val.GenSuspended {
		if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
			exc := vm.unhandledException
			vm.unhandledException = val.NoneVal()
			if ctx != nil {
				ctx.Raise(exc)
			} else {
				panic(exc)
			}
			restoreJoin()
			return val.NoneVal(), false
		}
		gen.State = val.GenClosed
		vm.releaseGeneratorStack(gen)
		if ctx != nil {
			ctx.Raise(val.NewRuntimeError("join left generator suspended"))
		}
		restoreJoin()
		return val.NoneVal(), false
	}
	if gen.State == val.GenClosed && (vm.unhandledException.IsNone() || vm.unhandledException.HeaderPtr() == nil) {
		vm.releaseGeneratorStack(gen)
		if vm.joinErrMsg != "" {
			errMsg := vm.joinErrMsg
			restoreJoin()
			if ctx != nil {
				ctx.Raise(val.NewTypeError(errMsg))
			} else {
				vm.execCtx.Raise(val.NewTypeError(errMsg))
			}
			return val.NoneVal(), false
		}
		out := val.NewStringVal(vm.joinBuf.String())
		restoreJoin()
		return out, true
	}
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		if ctx != nil {
			ctx.Raise(exc)
		} else {
			panic(exc)
		}
		restoreJoin()
		return val.NoneVal(), false
	}
	out := val.NewStringVal(vm.joinBuf.String())
	restoreJoin()
	return out, true
}

// inlineDunderCompare pushes an in-loop frame for dunder, a simple
// two-argument Python function whose receiver and argument already occupy
// stack[newBase:newBase+2]. Pending-comparison state is recorded on the
// caller frame so OP_RETURN can fall back to the generic comparison when
// the dunder answers NotImplemented.
//
//go:noinline
func (vm *VM) inlineDunderCompare(dunder val.Value, newBase, retSP int, cmpLeft, cmpRight val.Value, cmpOp uint8, st *loopState, ip int) (int, int) {
	dfn := dunder.AsFunction()
	dcallee := AsCodeObject(dfn.Code)
	curStack := st.stack
	minCap := max(newBase+256, newBase+dcallee.NumLocals+64)
	if minCap > len(curStack) {
		curStack = vm.growStack(st, curStack, minCap)
	}
	if st.frameIdx+2 > len(vm.frames) {
		vm.ensureFrameCapacity(st.frameIdx + 2)
	}
	saved := &vm.frames[st.frameIdx]
	saved.co = st.curCO
	saved.codePtr = st.codePtr
	saved.codeLen = st.codeLen
	saved.consts = st.consts
	saved.ip = ip + 1
	saved.baseOffset = st.base
	saved.retSP = retSP
	saved.cells = st.cells
	saved.cellPtrs = st.cellPtrs
	saved.globals = st.globals
	saved.locals = st.curLocals
	saved.cache = st.cache
	saved.excStackLen = len(vm.excStack)
	saved.stackIsVM = st.stackIsVM
	saved.stack = st.stack
	saved.activeGen = st.activeGen
	saved.genFrameIdx = st.genFrameIdx
	saved.genMode = genModeNone
	saved.genDefault = val.Value{}
	saved.initInstance = val.Value{}
	saved.cmpLeft = cmpLeft
	saved.cmpRight = cmpRight
	saved.cmpOp = cmpOp
	saved.cmpActive = true
	st.curLocals = val.NoneVal()
	st.frameIdx++
	st.curCO = dcallee
	st.cache = vm.cacheFor(dcallee)
	st.codePtr, st.codeLen = vm.loadWindow(dcallee, st.cache)
	st.consts = dcallee.Consts
	st.base = newBase
	switch {
	case len(dcallee.CellVars) == 0 && dfn.Closure != nil:
		st.cells = dfn.Closure
		st.cellPtrs = dfn.CellPtrs
	case len(dcallee.CellVars) == 0 && len(dcallee.FreeVars) == 0:
		st.cells = nil
		st.cellPtrs = nil
	default:
		st.cells, st.cellPtrs = vm.setupCalleeCellsAndPtrs(dcallee, dunder, curStack, newBase, 2)
	}
	if dfn.Globals != nil {
		st.globals = dfn.Globals
	}
	vm.activeCO = st.curCO
	vm.activeBaseOffset = st.base
	if vm.activeCellPtrs != nil || st.cellPtrs != nil {
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
	vm.activeFrameIdx = st.frameIdx
	return 0, newBase + max(dcallee.NumLocals, 2)
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
	switch coro.State {
	case val.CoroCreated:
		if !sentVal.IsNone() && sentVal.HeaderPtr() != nil {
			if ctx != nil {
				ctx.Raise(val.NewTypeError("can't send non-None value to a just-started coroutine"))
			}
			return val.NoneVal(), val.IterError
		}
	case val.CoroSuspended:
		coro.LastSent = sentVal
		co := AsCodeObject(coro.Code)
		if coro.IP < len(co.Code) && Opcode(co.Code[coro.IP]>>24) != OP_AWAIT && Opcode(co.Code[coro.IP]>>24) != OP_YIELD_FROM {
			coro.Stack[coro.SP] = sentVal
			coro.SP++
		}
	}

	coro.State = val.CoroRunning
	co := AsCodeObject(coro.Code)
	nextFrame := 0
	if vm.activeCO != nil {
		nextFrame = vm.activeFrameIdx + 1
	}
	vm.ensureFrameCapacity(nextFrame + 4)
	vm.unhandledException = val.NoneVal()
	res := vm.runFrame(co, coro.Base, coro.SP, nextFrame, coro.Cells, coro.Globals, val.NoneVal(), coro.Stack, nil, coro)
	if coro.State == val.CoroSuspended {
		return res, val.IterYielded
	}
	if coro.State == val.CoroClosed && (vm.unhandledException.IsNone() || vm.unhandledException.HeaderPtr() == nil) {
		return coro.RetVal, val.IterExhausted
	}
	if !vm.unhandledException.IsNone() && vm.unhandledException.HeaderPtr() != nil {
		exc := vm.unhandledException
		vm.unhandledException = val.NoneVal()
		if ctx != nil {
			ctx.Raise(exc)
		} else {
			panic(exc)
		}
		return val.NoneVal(), val.IterError
	}
	return res, val.IterExhausted
}

//go:noinline
func (vm *VM) unwind(st *loopState, ip int) (int, int, loopCtl) {
	exc := st.raiseExc
	st.raiseExc = val.Value{}

	curTB := val.NoneVal()
	if exc.IsInstance() {
		curTB, _ = exc.AsInstance().GetAttr(nameTraceback)
	}

	faultIP := ip
	st.ip = faultIP
	faultCO := st.curCO
	faultFrameIdx := st.frameIdx

	if faultFrameIdx >= 0 && faultFrameIdx < len(vm.frames) {
		fSlot := &vm.frames[faultFrameIdx]
		fSlot.co = faultCO
		fSlot.codePtr = st.codePtr
		fSlot.codeLen = st.codeLen
		fSlot.consts = st.consts
		fSlot.ip = faultIP
		fSlot.baseOffset = st.base
		fSlot.cells = st.cells
		fSlot.cellPtrs = st.cellPtrs
		fSlot.globals = st.globals
		fSlot.locals = st.curLocals
		fSlot.cache = st.cache
		vm.activeFrameIdx = faultFrameIdx
	}

	for {
		co := st.curCO
		if co != nil && len(co.ExceptionTable) > 0 {
			faultIP := ip
			var matched *ExceptionEntry
			minSpan := int(^uint(0) >> 1)
			for i := range len(co.ExceptionTable) {
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
				sp := st.base + co.NumLocals + matched.TargetStackDepth
				if matched.PushLasti {
					st.stack[sp] = val.Int64Val(int64(faultIP))
					sp++
				}
				st.stack[sp] = exc
				sp++
				if st.cache == nil {
					st.cache = vm.cacheFor(co)
				}
				vm.activeCO = co
				vm.activeBaseOffset = st.base
				vm.activeCells = st.cells
				vm.activeCellPtrs = st.cellPtrs
				vm.activeFrameIdx = st.frameIdx
				vm.frames[st.frameIdx].initInstance = val.Value{}
				vm.frames[st.frameIdx].cmpActive = false
				return matched.TargetIP, sp, ctlReload
			}
		}

		if curTB.IsNone() && faultCO != nil {
			line := faultCO.LineAt(faultIP)
			frameVal := vm.materializeFrame(faultFrameIdx)
			curTB = val.NewTracebackVal(frameVal, line, faultIP, val.NoneVal())
			if exc.IsInstance() {
				exc.AsInstance().SetAttr(nameTraceback, curTB)
			}
		}

		if st.activeGen != nil && st.frameIdx == st.genFrameIdx {
			st.activeGen.State = val.GenClosed
			vm.releaseGeneratorStack(st.activeGen)
		}

		if st.frameIdx > st.startFrameIdx {
			if curShadow := vm.frames[st.frameIdx].shadowFrame; curShadow != nil {
				curShadow.Detach()
				vm.frames[st.frameIdx].shadowFrame = nil
			}

			st.frameIdx--
			caller := &vm.frames[st.frameIdx]
			caller.initInstance = val.Value{}
			if caller.cmpActive {
				caller.cmpActive = false
				caller.cmpLeft = val.Value{}
				caller.cmpRight = val.Value{}
			}
			if len(vm.excStack) > caller.excStackLen {
				vm.excStack = vm.excStack[:caller.excStackLen]
			}
			callerIP := caller.ip - 1
			if caller.genMode == genModeYieldFrom {
				callerIP = caller.ip
			}
			if caller.genMode != genModeNone {
				st.stack = caller.stack
				st.stackIsVM = caller.stackIsVM
				vm.execCtx.Stack = caller.stack
				st.activeGen = caller.activeGen
				st.genFrameIdx = caller.genFrameIdx
				caller.genMode = genModeNone
				caller.genDefault = val.Value{}
				caller.stack = nil
				caller.activeGen = nil
			}
			st.curCO = caller.co
			st.codePtr = caller.codePtr
			st.codeLen = caller.codeLen
			st.consts = caller.consts
			st.base = caller.baseOffset
			st.cells = caller.cells
			st.cellPtrs = caller.cellPtrs
			vm.activeCells = caller.cells
			vm.activeCellPtrs = caller.cellPtrs
			st.globals = caller.globals
			st.curLocals = caller.locals
			st.cache = caller.cache
			ip = callerIP
			st.ip = callerIP

			if caller.co != nil {
				line := caller.co.LineAt(callerIP)
				frameVal := vm.materializeFrame(st.frameIdx)
				curTB = val.NewTracebackVal(frameVal, line, callerIP, curTB)
				if exc.IsInstance() {
					exc.AsInstance().SetAttr(nameTraceback, curTB)
				}
			}
			continue
		}

		if curShadow := vm.frames[st.frameIdx].shadowFrame; curShadow != nil {
			curShadow.Detach()
			vm.frames[st.frameIdx].shadowFrame = nil
		}

		vm.unhandledException = exc
		vm.lastException = exc
		if st.activeGen != nil {
			st.activeGen.State = val.GenClosed
			vm.releaseGeneratorStack(st.activeGen)
		}
		if st.activeCoro != nil {
			st.activeCoro.State = val.CoroClosed
		}
		st.result = val.NoneVal()
		return 0, 0, ctlExit
	}
}

func ensureStack(stack []val.Value, minCap int) []val.Value {
	if minCap <= len(stack) {
		return stack
	}
	if minCap <= cap(stack) {
		return stack[:cap(stack)]
	}
	newCap := len(stack) * 2
	if newCap < minCap {
		newCap = minCap + 256
	}
	newStack := make([]val.Value, newCap)
	copy(newStack, stack)
	return newStack
}

// interpreterSafepoint is a safepoint for the Go runtime.
//
// The interpreter executes long stretches of bytecode without calling into the
// Go runtime, so the garbage collector could not preempt it and its mark
// phase stalled until the bytecode ran out. This function is called from the
// dispatch loop, which lets the collector finish a cycle and lets the
// scheduler run the other goroutines.
//
//go:noinline
func interpreterSafepoint() {
}

// The method names that the call fast paths compare against. They are
// interned once, so the comparison is a word compare instead of a call into
// the value package: the dispatch loop is too large for the Go inliner, so
// every call there is a real call.
var (
	nameStringIO = val.InternString("StringIO")
	nameEnter    = val.InternString("__enter__")
	nameGetValue = val.InternString("getvalue")
	nameRead     = val.InternString("read")
	nameWrite    = val.InternString("write")
	nameExists   = val.InternString("exists")
	nameRemove   = val.InternString("remove")
	nameUnlink   = val.InternString("unlink")
)

// prepareStoreAttr prepares a value for an attribute store and resolves the
// instance the store targets. The interpreter loop used to make three
// separate calls for this, which cost more in call overhead than the checks
// themselves.
func (vm *VM) prepareStoreAttr(obj, v val.Value) (*val.PyInstance, *val.PyType, bool) {
	if v.IsString() {
		val.DetachStringBuf(v)
	}
	if v.Kind() == val.KindMemoryView {
		v.AsMemoryView().Escaped = true
	}
	if !obj.IsInstance() {
		return nil, nil, false
	}
	inst := obj.AsInstance()
	typ := inst.Header.Type
	if inst.Shape == nil || typ == nil {
		return nil, nil, false
	}
	return inst, typ, true
}

// prepareReturnValue releases the spare capacity of a returned string and
// marks a returned memory view as shared.
func (vm *VM) prepareReturnValue(v val.Value) {
	if v.IsString() {
		val.DetachStringBuf(v)
	}
	if v.Kind() == val.KindMemoryView {
		v.AsMemoryView().Escaped = true
	}
}

// storeFastValue publishes a value into a local slot. It merges the three
// separate calls the interpreter loop used to make.
func (vm *VM) storeFastValue(old, incoming val.Value, key uint32) {
	if incoming.IsString() {
		val.DetachStringBuf(incoming)
	}
	if old.Kind() == val.KindMemoryView && old != incoming {
		mv := old.AsMemoryView()
		if !mv.Escaped {
			vm.recycleMemoryView(mv)
		}
	}
	if incoming.Kind() == val.KindMemoryView {
		mv := incoming.AsMemoryView()
		switch {
		case mv.OwnerKey == 0:
			mv.OwnerKey = key
		case mv.OwnerKey != key:
			mv.Escaped = true
		}
	}
}

func (vm *VM) runFrame(co *CodeObject, startBase int, startSP int, startFrameIdx int, startCells []val.Value, startGlobals *val.PyDict, startLocals val.Value, stack []val.Value, activeGen *val.PyGenerator, activeCoro *val.PyCoroutine) val.Value {
	savedActiveState := vm.activeState
	if savedActiveState != nil {
		vm.flushActiveFrame()
	}
	savedCtxStack := vm.execCtx.Stack
	savedCtxSP := vm.execCtx.SP
	wasVMStack := len(savedCtxStack) > 0 && len(vm.stack) > 0 && &savedCtxStack[0] == &vm.stack[0]
	vm.execCtx.Stack = stack
	vm.execCtx.SP = startSP
	savedActiveCO := vm.activeCO
	savedActiveBaseOffset := vm.activeBaseOffset
	savedActiveCells := vm.activeCells
	savedActiveCellPtrs := vm.activeCellPtrs
	savedActiveFrameIdx := vm.activeFrameIdx
	st := vm.acquireLoopState()
	var cellPtrs []*val.PyCell
	ownCellPtrs := false
	if activeGen != nil && len(activeGen.CellPtrs) == len(startCells) {
		cellPtrs = activeGen.CellPtrs
	} else if len(startCells) > 0 {
		cellPtrs = vm.acquireGenCellPtrs(len(startCells))
		for i, c := range startCells {
			cellPtrs[i] = c.AsCell()
		}
		if activeGen != nil {
			activeGen.CellPtrs = cellPtrs
		} else {
			ownCellPtrs = true
		}
	}
	defer func() {
		if st.frameIdx > vm.maxFrameIdx {
			vm.maxFrameIdx = st.frameIdx
		}
		if st.stackIsVM {
			hw := st.base + 256
			if st.curCO != nil && st.curCO.NumLocals+st.curCO.MaxStackSize > 256 {
				hw = st.base + st.curCO.NumLocals + st.curCO.MaxStackSize
			}
			if hw > vm.maxSP {
				vm.maxSP = hw
			}
		}
		if wasVMStack {
			vm.execCtx.Stack = vm.stack
		} else {
			vm.execCtx.Stack = savedCtxStack
		}
		vm.execCtx.SP = savedCtxSP
		vm.activeCO = savedActiveCO
		vm.activeBaseOffset = savedActiveBaseOffset
		vm.activeCells = savedActiveCells
		vm.activeCellPtrs = savedActiveCellPtrs
		vm.activeFrameIdx = savedActiveFrameIdx
		vm.activeState = savedActiveState
		st.cellPtrs = nil
		if ownCellPtrs {
			vm.releaseCellPtrSlice(cellPtrs)
		}
		vm.releaseLoopState(st)
	}()

	ip := 0
	if activeGen != nil && activeGen.IP > 0 {
		ip = activeGen.IP
	} else if activeCoro != nil && activeCoro.IP > 0 {
		ip = activeCoro.IP
	}
	globals := startGlobals
	if globals == nil {
		globals = vm.globals
	}

	st.curCO = co
	st.consts = co.Consts
	st.frameIdx = startFrameIdx
	st.startFrameIdx = startFrameIdx
	st.genFrameIdx = -1
	if activeGen != nil {
		st.genFrameIdx = startFrameIdx
	}
	if startFrameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = startFrameIdx
	}
	st.base = startBase
	st.cells = startCells
	st.cellPtrs = cellPtrs
	st.globals = globals
	st.curLocals = startLocals
	st.activeGen = activeGen
	st.activeCoro = activeCoro
	st.ip = ip
	st.sp = startSP
	st.periodicTick = 0

	vm.activeCO = co
	vm.activeBaseOffset = startBase
	vm.activeCells = startCells
	vm.activeCellPtrs = cellPtrs
	vm.activeFrameIdx = startFrameIdx

	minStack := startBase + max(256, co.NumLocals+max(co.MaxStackSize, len(co.Code))+64)
	if minStack > len(stack) {
		stack = vm.growStack(st, stack, minStack)
	}
	st.stack = stack
	st.stackIsVM = activeGen == nil && activeCoro == nil && len(vm.stack) > 0 && &stack[0] == &vm.stack[0]
	if st.stackIsVM && minStack > vm.maxSP {
		vm.maxSP = minStack
	}

	st.cache = vm.cacheFor(co)
	st.codePtr, st.codeLen = vm.loadWindow(co, st.cache)
	vm.activeState = st

	if atomic.LoadInt32(&co.IsTier2Optimized) == 0 {
		maybeOptimizeCodeObject(vm, co, st.cache)
	}

	if (activeGen != nil || activeCoro != nil) && vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		unwindIP := ip
		if unwindIP > 0 && (unwindIP >= st.codeLen || Opcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(unwindIP-1)*4))>>24) == OP_YIELD_VALUE) {
			unwindIP = ip - 1
		}
		st.raiseExc = exc
		newIP, newSP, ctl := vm.unwind(st, unwindIP)
		if ctl == ctlExit {
			return st.result
		}
		st.ip = newIP
		st.sp = newSP
	}

	return vm.runLoopWithGlobals(st)
}

func (vm *VM) runLoopWithGlobals(st *loopState) val.Value {
	var (
		ip            = st.ip
		sp            = st.sp
		stackPtr      = unsafe.Pointer(unsafe.SliceData(st.stack))
		locals        = (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
		constsPtr     = unsafe.Pointer(unsafe.SliceData(st.consts))
		codePtr       = st.codePtr
		ctl           loopCtl
		pushFn        *val.PyFunction
		pushCache     *VMCodeCache
		pushTotalArgs int
	)

	for {
		inst := *(*uint32)(unsafe.Add(codePtr, uintptr(ip)*4))
		op := Opcode(inst >> 24)
		recordOpcode(op)

		switch op {
		case OP_LOAD_CONST:
			bx := uint16(inst)
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = *(*val.Value)(unsafe.Add(constsPtr, uintptr(bx)*16))
			sp++
			ip++
			continue

		case OP_LOAD_FAST:
			bx := uint16(inst)
			if bx < 256 {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = locals[uint8(bx)]
				sp++
				ip++
				continue
			}
			ip, sp, ctl = vm.opLoadFastWide(st, inst, ip, sp)
		case OP_STORE_FAST:
			bx := uint16(inst)
			if bx < 256 {
				sp--
				locals[uint8(bx)] = *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16))
				ip++
				continue
			}
			ip, sp, ctl = vm.opStoreFastWide(st, inst, ip, sp)
		case OP_STORE_FAST_VIEW:
			ip, sp, ctl = vm.opStoreFastView(st, inst, ip, sp)
		case OP_BINARY_ADD:
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if a.IsInt64() && b.IsInt64() {
				ai, bi := a.AsInt64(), b.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(sum)
					sp--
					ip++
					continue
				}
			} else if a.IsFloat64() && b.IsFloat64() {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Float64Val(a.AsFloat64() + b.AsFloat64())
				sp--
				ip++
				continue
			}
			ip, sp, ctl = vm.opBinarySlow(st, OP_BINARY_ADD, ip, sp)
		case OP_BINARY_SUB:
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if a.IsInt64() && b.IsInt64() {
				ai, bi := a.AsInt64(), b.AsInt64()
				diff := ai - bi
				if ((ai ^ bi) & (ai ^ diff)) >= 0 {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(diff)
					sp--
					ip++
					continue
				}
			}
			ip, sp, ctl = vm.opBinarySlow(st, OP_BINARY_SUB, ip, sp)
		case OP_BINARY_SUBSCR:
			key := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			obj := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			switch {
			case obj.IsList() && key.IsInt64() && obj.AsList().Header.Type == val.ListType:
				items := obj.AsList().Items
				k := key.AsInt64()
				if uint64(k) < uint64(len(items)) {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = items[k]
					sp--
					ip++
					continue
				}
			case obj.IsDict() && obj.AsDict().Header.Type == val.DictType &&
				(key.IsInt64() || key.IsInlineStr() || (key.IsString() && key.AsStringObj().Header.Type == val.StrType)):
				if v, ok := obj.AsDict().Get(key); ok {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = v
					sp--
					ip++
					continue
				}
			case obj.Kind() == val.KindMemoryView && key.IsInt64():
				mv := obj.AsMemoryView()
				if mv.ItemSize <= 1 && mv.NDim <= 1 {
					buf := mv.Buf
					k := key.AsInt64()
					if k < 0 {
						k += int64(len(buf))
					}
					if uint64(k) < uint64(len(buf)) {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(int64(buf[k]))
						sp--
						ip++
						continue
					}
				}
			}
			ip, sp, ctl = vm.opBinarySubscrSlow(st, ip, sp)
		case OP_BINARY_SLICE:
			ip, sp, ctl = vm.opBinarySlice(st, ip, sp)
		case OP_JUMP_IF_FALSE:
			sp--
			cond := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16))
			if !cond.IsTruthy() {
				ip = int(uint16(inst))
			} else {
				ip++
			}
			continue

		case OP_JUMP:
			target := int(uint16(inst))
			if target <= ip {
				st.periodicTick++
				if (st.periodicTick&0xFF) == 0 ||
					atomic.LoadInt32(&st.curCO.IsTier2Optimized) == 0 ||
					atomic.LoadInt32(&st.curCO.StreamGen) != st.cache.streamGen ||
					vm.signalsPending() {
					ip, sp, ctl = vm.opJumpSlow(st, target, ip, sp)
					break
				}
			}
			ip = target
			continue
		case OP_CALL:
			ip, sp, ctl = vm.opCallSlow(st, inst, ip, sp)
		case OP_CALL_PY_EXACT_ARGS:
			argc := int(uint8(inst >> 16))
			newBase := sp - argc
			retSP := newBase - 1
			target := *(*val.Value)(unsafe.Add(stackPtr, uintptr(retSP)*16))
			entry := &st.cache.ic[ip]
			if target == entry.CachedVal && !vm.hasTrackedViews {
				fn := target.AsFunction()
				cc := entry.CalleeCache
				if cc != nil && cc.owner == vm && cc.co != nil &&
					fn.Code.HeaderPtr() == &cc.co.Header &&
					len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
					atomic.LoadInt32(&cc.co.StreamGen) == cc.streamGen {
					callee := cc.co
					st.periodicTick++
					if (st.periodicTick&0xFF) != 0 &&
						st.frameIdx+2 <= len(vm.frames) &&
						newBase+max(256, callee.NumLocals+64) <= len(st.stack) {
						pushFn = fn
						pushCache = cc
						// Negative bitwise-complement encodes retSP = newBase - 1 (popping callable slot).
						pushTotalArgs = ^argc
						goto pushFrame
					}
				}
			}
			ip, sp, ctl = vm.opCallSlow(st, inst, ip, sp)
		case OP_CALL_LEN:
			target := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if target == vm.builtinLen {
				arg := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
				var n int
				ok := true
				switch arg.Kind() {
				case val.KindString, val.KindInlineStr:
					n = arg.StringLen()
				case val.KindList:
					n = len(arg.AsList().Items)
				case val.KindTuple:
					n = len(arg.AsTuple().Items)
				case val.KindDict:
					n = arg.AsDict().Len()
				case val.KindSet:
					n = arg.AsSet().Len()
				case val.KindBytes:
					n = len(arg.AsBytes().Data)
				default:
					ok = false
				}
				if ok {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(int64(n))
					sp--
					ip++
					continue
				}
			}
			ip, sp, ctl = vm.opCallLenSlow(st, inst, ip, sp)
		case OP_CALL_ISINSTANCE:
			target := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-3)*16))
			if target == vm.builtinIsInstance {
				cls := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
				if cls.IsType() {
					tp := cls.AsType()
					meta := tp.Header.Type
					if meta == nil || meta == val.TypeType {
						obj := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
						if obj.Type() == tp {
							*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-3)*16)) = val.BoolVal(true)
							sp -= 2
							ip++
							continue
						}
					}
				}
			}
			ip, sp, ctl = vm.opCallIsInstanceSlow(st, inst, ip, sp)
		case OP_CALL_BUILTIN_FAST:
			argc := int(uint8(inst >> 16))
			newBase := sp - argc
			retSP := newBase - 1
			target := *(*val.Value)(unsafe.Add(stackPtr, uintptr(retSP)*16))
			if target == vm.builtinRange && argc == 1 {
				arg0 := *(*val.Value)(unsafe.Add(stackPtr, uintptr(newBase)*16))
				if arg0.IsInt64() {
					stop := arg0.AsInt64()
					if stop >= 0 && stop <= 0x7FFFFFFF {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(retSP)*16)) = val.RangeVal(0, int32(stop))
						sp = retSP + 1
						ip++
						continue
					}
				}
			}
			ip, sp, ctl = vm.opCallBuiltinFast(st, inst, ip, sp)
		case OP_CALL_METHOD_DESCR:
			ip, sp, ctl = vm.opCallMethodDescr(st, inst, ip, sp)
		case OP_CALL_TYPE_INIT:
			ip, sp, ctl = vm.opCallTypeInit(st, inst, ip, sp)
		case OP_CALL_KW:
			ip, sp, ctl = vm.opCallKw(st, inst, ip, sp)
		case OP_CALL_FUNCTION_EX:
			ip, sp, ctl = vm.opCallFunctionEx(st, inst, ip, sp)
		case OP_CALL_METHOD_PIC:
			argc := int(uint8(inst >> 16))
			newBase := sp - argc - 1
			receiver := *(*val.Value)(unsafe.Add(stackPtr, uintptr(newBase)*16))
			totalArgs := argc + 1
			if !vm.hasTrackedViews && receiver.IsInstance() && uint(ip) < uint(len(st.cache.methodPIC)) {
				instObj := receiver.AsInstance()
				instShape := instObj.Shape
				instType := instObj.Header.Type
				pic := &st.cache.methodPIC[ip]
				var targetFn *val.PyFunction
				switch {
				case pic[0].Shape == instShape && instType.Version() == pic[0].TypeVersion:
					targetFn = pic[0].TargetFn
				case pic[1].Shape == instShape && instType.Version() == pic[1].TypeVersion:
					targetFn = pic[1].TargetFn
				case pic[2].Shape == instShape && instType.Version() == pic[2].TypeVersion:
					targetFn = pic[2].TargetFn
				case pic[3].Shape == instShape && instType.Version() == pic[3].TypeVersion:
					targetFn = pic[3].TargetFn
				}
				if targetFn != nil {
					callee := AsCodeObject(targetFn.Code)
					if !callee.IsGenerator && !callee.IsCoroutine &&
						callee.SimplePositionalOnly && totalArgs == callee.PositionalParamCount &&
						len(callee.CellVars) == 0 && (len(callee.FreeVars) == 0 || targetFn.Closure != nil) &&
						st.frameIdx+2 <= len(vm.frames) &&
						newBase+max(256, callee.NumLocals+64) <= len(st.stack) &&
						(len(callee.ExceptionTable) > 0 || atomic.LoadInt32(&callee.IsTier2Optimized) != 0) {
						pc := st.cache
						if callee != st.curCO {
							pc = (*VMCodeCache)(atomic.LoadPointer(&callee.primary))
						}
						if pc != nil && pc.owner == vm && atomic.LoadInt32(&callee.StreamGen) == pc.streamGen {
							pushFn = targetFn
							pushCache = pc
							pushTotalArgs = totalArgs
							goto pushFrame
						}
					}
				}
			}
			ip, sp, ctl = vm.opCallMethodPICSlow(st, inst, ip, sp)
		case OP_RETURN:
			sp--
			retVal := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16))
			if retVal.IsString() || retVal.Kind() == val.KindMemoryView {
				vm.prepareReturnValue(retVal)
			}
			if st.frameIdx > st.startFrameIdx && st.frameIdx != st.genFrameIdx && vm.frames[st.frameIdx].shadowFrame == nil {
				caller := &vm.frames[st.frameIdx-1]
				if caller.initInstance.HeaderPtr() == nil &&
					(!caller.cmpActive || (caller.cmpOp <= CMP_GTE && !retVal.IsNotImplemented())) &&
					len(vm.excStack) <= caller.excStackLen && caller.cache != nil {
					if caller.cmpActive {
						caller.cmpActive = false
						caller.cmpLeft = val.Value{}
						caller.cmpRight = val.Value{}
					}
					st.frameIdx--
					if caller.co == st.curCO {
						ip = caller.ip
						st.base = caller.baseOffset
						locals = (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
						vm.activeBaseOffset = st.base
						vm.activeFrameIdx = st.frameIdx
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(caller.retSP)*16)) = retVal
						sp = caller.retSP + 1
						continue
					}
					st.curCO = caller.co
					codePtr = caller.codePtr
					st.codePtr = codePtr
					st.codeLen = caller.codeLen
					st.consts = caller.consts
					constsPtr = unsafe.Pointer(unsafe.SliceData(caller.consts))
					ip = caller.ip
					st.base = caller.baseOffset
					locals = (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
					st.cells = caller.cells
					st.cellPtrs = caller.cellPtrs
					st.globals = caller.globals
					st.curLocals = caller.locals
					vm.activeCO = st.curCO
					vm.activeBaseOffset = st.base
					if vm.activeCellPtrs != nil || st.cellPtrs != nil {
						vm.activeCells = st.cells
						vm.activeCellPtrs = st.cellPtrs
					}
					vm.activeFrameIdx = st.frameIdx
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(caller.retSP)*16)) = retVal
					sp = caller.retSP + 1
					st.cache = caller.cache
					continue
				}
			}
			ip, sp, ctl = vm.opReturnSlow(st, ip, sp, retVal)
		case OP_LOAD_FAST_ATTR:
			reg := uint8(inst >> 16)
			obj := locals[reg]
			if instObj := obj.AsInstanceChecked(); instObj != nil {
				instShape := instObj.Shape
				instType := instObj.Header.Type
				if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() && uint(ip+1) < uint(len(st.cache.attrPIC)) {
					pic := &st.cache.attrPIC[ip+1]
					if pic[0].Shape == instShape && pic[0].TypeVer == uint32(instType.Version()) {
						slotIdx := pic[0].SlotIndex
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							v := instObj.Slots[slotIdx]
							if !v.IsUnbound() {
								*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = v
								sp++
								ip += 2
								continue
							}
						}
					}
				}
			}
			ip, sp, ctl = vm.opFastAttrSlow(st, inst, ip, sp)

		case OP_LOAD_ATTR:
			obj := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			var attrRes val.Value
			instObj := obj.AsInstanceChecked()
			switch {
			case instObj != nil:
				instShape := instObj.Shape
				instType := instObj.Header.Type
				if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
					typeVer := instType.Version()
					entry := &st.cache.ic[ip]
					if entry.ExpectedTypeVer == typeVer {
						switch entry.Kind {
						case ICKindSlotMember:
							slotIdx := entry.SlotIndex
							if uint(slotIdx) < uint(len(instObj.Slots)) {
								v := instObj.Slots[slotIdx]
								if !v.IsUnbound() {
									recordICSlowHit(ICKindSlotMember)
									attrRes = v
									goto loadAttrHit
								}
							}
						case ICKindProperty:
							goto loadAttrProp
						case ICKindClassAttr:
							if instShape.ID == entry.ExpectedShapeID {
								recordICSlowHit(ICKindClassAttr)
								attrRes = entry.CachedVal
								goto loadAttrHit
							}
						}
					}
					if uint(ip) < uint(len(st.cache.attrPIC)) {
						pic := &st.cache.attrPIC[ip]
						for k := range 4 {
							if pic[k].Shape == instShape && pic[k].TypeVer == uint32(typeVer) {
								slotIdx := pic[k].SlotIndex
								if uint(slotIdx) < uint(len(instObj.Slots)) {
									v := instObj.Slots[slotIdx]
									if !v.IsUnbound() {
										attrRes = v
										goto loadAttrHit
									}
								}
							}
						}
					}
					if instShape.ID == entry.ExpectedShapeID && typeVer == entry.ExpectedTypeVer && entry.Kind == ICKindInstanceSlot {
						slotIdx := entry.SlotIndex
						if uint(slotIdx) < uint(len(instObj.Slots)) {
							v := instObj.Slots[slotIdx]
							if !v.IsUnbound() {
								attrRes = v
								goto loadAttrHit
							}
						}
					}
				}
			case obj.IsTuple():
				tp := obj.HeaderPtr().Type
				if tp != nil && (tp.Flags&val.FlagCustomGetAttribute) == 0 && !val.MultiThreadedFlag.Load() {
					entry := &st.cache.ic[ip]
					if entry.ExpectedTypeVer == tp.Version() {
						switch entry.Kind {
						case ICKindTupleGetter:
							t := obj.AsTuple()
							if uint(entry.SlotIndex) < uint(len(t.Items)) {
								recordICSlowHit(ICKindTupleGetter)
								attrRes = t.Items[entry.SlotIndex]
								goto loadAttrHit
							}
						case ICKindProperty:
							goto loadAttrProp
						}
					}
				}
			case obj.IsType():
				cls := obj.AsType()
				meta := cls.Header.Type
				if meta == nil {
					meta = val.TypeType
				}
				entry := &st.cache.ic[ip]
				if entry.Kind == ICKindTypeAttr && entry.ExpectedTypeVer == cls.Version() && entry.ExpectedAuxVer == meta.Version() {
					recordICSlowHit(ICKindTypeAttr)
					attrRes = entry.CachedVal
					goto loadAttrHit
				}
			case obj.IsModule() && obj.AsModule().Header.Type == val.ModuleType:
				mod := obj.AsModule()
				entry := &st.cache.ic[ip]
				if entry.Kind == ICKindModuleAttr && entry.ExpectedTypeVer == atomic.LoadUint64(&mod.Dict.VersionTag) {
					attrRes = entry.CachedVal
					goto loadAttrHit
				}
			}
			ip, sp, ctl = vm.opLoadAttrSlow(st, inst, ip, sp)
			break
		loadAttrProp:
			{
				entry := &st.cache.ic[ip]
				fget := entry.CachedVal
				if fget.IsFunction() && !vm.hasTrackedViews {
					fn := fget.AsFunction()
					cc := entry.CalleeCache
					if cc != nil && cc.owner == vm && cc.co != nil &&
						fn.Code.HeaderPtr() == &cc.co.Header &&
						cc.co.SimplePositionalOnly && cc.co.NumParams == 1 &&
						len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
						len(cc.co.CellVars) == 0 &&
						atomic.LoadInt32(&cc.co.StreamGen) == cc.streamGen {
						callee := cc.co
						newBase := sp - 1
						st.periodicTick++
						if (st.periodicTick&0xFF) != 0 &&
							st.frameIdx+2 <= len(vm.frames) &&
							newBase+max(256, callee.NumLocals+64) <= len(st.stack) {
							recordLoadAttrInlinePropHit()
							pushFn = fn
							pushCache = cc
							pushTotalArgs = 1
							goto pushFrame
						}
					}
				}
			}
			ip, sp, ctl = vm.opLoadAttrSlow(st, inst, ip, sp)
			break
		loadAttrHit:
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = attrRes
			ip++
			continue
		case OP_STORE_ATTR:
			v := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			obj := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			if !v.IsString() && v.Kind() != val.KindMemoryView && !val.MultiThreadedFlag.Load() {
				if instObj := obj.AsInstanceChecked(); instObj != nil {
					instShape := instObj.Shape
					instType := instObj.Header.Type
					if instShape != nil && instType != nil && (instType.Flags&val.FlagCustomSetAttr) == 0 {
						typeVer := instType.Version()
						if uint(ip) < uint(len(st.cache.attrPIC)) {
							pic := &st.cache.attrPIC[ip]
							for k := range 4 {
								if pic[k].Shape == instShape && pic[k].TypeVer == uint32(typeVer) {
									slotIdx := pic[k].SlotIndex
									if uint(slotIdx) < uint(len(instObj.Slots)) {
										instObj.Slots[slotIdx] = v
										sp -= 2
										ip++
										goto storeAttrHit
									}
								}
							}
						}
						entry := &st.cache.ic[ip]
						if instShape.ID == entry.ExpectedShapeID && typeVer == entry.ExpectedTypeVer {
							if entry.Kind == ICKindInstanceSlot {
								slotIdx := entry.SlotIndex
								if uint(slotIdx) < uint(len(instObj.Slots)) {
									instObj.Slots[slotIdx] = v
									sp -= 2
									ip++
									continue
								}
							} else if entry.Kind == ICKindInstanceTransition && uint(ip) < uint(len(st.cache.transitionIC)) {
								tc := &st.cache.transitionIC[ip]
								if tc.OldShape == instShape && tc.NewShape != nil && instObj.ExtendBootstrapSlot(int(tc.SlotIdx), v) {
									instObj.Shape = tc.NewShape
									sp -= 2
									ip++
									continue
								}
							}
						}
					}
				}
			}
			ip, sp, ctl = vm.opStoreAttrSlow(st, inst, ip, sp)
			break
		storeAttrHit:
			continue
		case OP_POP_TOP:
			sp--
			if vm.hasTrackedViews {
				vm.opReclaimPopTop(*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)))
			}
			ip++
			continue

		case OP_DUP_TOP:
			// The duplicated value is now reachable from two stack slots, so it
			// must not be extendable in place any more.
			top := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			if top.IsString() || top.Kind() == val.KindMemoryView {
				vm.opDetachDupTop(top)
			} else if top.IsIter() || top.IsGenerator() {
				top.HeaderPtr().Flags &^= val.FlagEphemeralIter | val.FlagEphemeralGen
			}
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = top
			sp++
			ip++
			continue

		case OP_LOAD_GLOBAL:
			if st.globals.Header.Type == val.DictType || st.globals.Header.Type == nil {
				globVer := atomic.LoadUint64(&st.globals.VersionTag)
				entry := &st.cache.ic[ip]
				if entry.ExpectedTypeVer == globVer {
					if entry.Kind == ICKindGlobal {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = entry.CachedVal
						sp++
						ip++
						continue
					}
					if entry.Kind == ICKindBuiltin && entry.ExpectedAuxVer == atomic.LoadUint64(&vm.builtins.VersionTag) {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = entry.CachedVal
						sp++
						ip++
						continue
					}
				}
			}
			ip, sp, ctl = vm.opLoadGlobalSlow(st, inst, ip, sp)
		case OP_LOAD_DEREF:
			bx := int(uint16(inst))
			activePtrs := vm.activeCellPtrs
			if uint(bx) < uint(len(activePtrs)) && activePtrs[bx] != nil {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = activePtrs[bx].Value
			} else {
				cell := (*val.PyCell)(unsafe.Pointer(st.cells[bx].HeaderPtr()))
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = cell.Value
			}
			sp++
			ip++
			continue

		case OP_GET_ITER:
			top := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			if top.IsRange() || top.IsIter() || top.IsGenerator() {
				ip++
				continue
			}
			if top.IsList() || top.IsTuple() || top.IsDict() {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = vm.acquireForIter(top)
				ip++
				continue
			}
			ip, sp, ctl = vm.opGetIterSlow(st, ip, sp)
		case OP_FOR_ITER:
			jumpTarget := int(uint16(inst))
			it := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			if it.IsRange() {
				cur := it.RangeStart()
				stop := it.RangeStop()
				if cur < stop {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.RangeVal(cur+1, stop)
					instNext := *(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4))
					if DecodeOpcode(instNext) == OP_STORE_FAST {
						reg := DecodeArgBx(instNext)
						if reg < 256 {
							locals[uint8(reg)] = val.Int64Val(int64(cur))
						} else {
							*(*val.Value)(unsafe.Add(stackPtr, uintptr(st.base+int(reg))*16)) = val.Int64Val(int64(cur))
						}
						ip += 2
						continue
					}
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(int64(cur))
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
				if pyIter.Kind == val.IterKindList {
					items := pyIter.Obj.AsList().Items
					if uint(pyIter.Idx) < uint(len(items)) {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = items[pyIter.Idx]
						pyIter.Idx++
						sp++
						ip++
						continue
					}
					vm.releaseForIter(pyIter)
					sp--
					ip = jumpTarget
					continue
				}
				if pyIter.Kind == val.IterKindTuple {
					items := pyIter.Obj.AsTuple().Items
					if uint(pyIter.Idx) < uint(len(items)) {
						*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = items[pyIter.Idx]
						pyIter.Idx++
						sp++
						ip++
						continue
					}
					vm.releaseForIter(pyIter)
					sp--
					ip = jumpTarget
					continue
				}
			}
			ip, sp, ctl = vm.opForIterSlow(st, inst, ip, sp)
		case OP_BUILD_LIST:
			ip, sp, ctl = vm.opBuildList(st, inst, ip, sp)
		case OP_BUILD_MAP:
			ip, sp, ctl = vm.opBuildMap(st, inst, ip, sp)
		case OP_BUILD_TUPLE:
			ip, sp, ctl = vm.opBuildTuple(st, inst, ip, sp)

		case OP_UNPACK_SEQUENCE:
			ip, sp, ctl = vm.opUnpackSequence(st, inst, ip, sp)
		case OP_COMPARE_OP:
			cmpOp := uint8(inst >> 16)
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			var cmpRes bool
			if a.IsInt64() && b.IsInt64() && cmpOp <= CMP_GTE {
				ai, bi := a.AsInt64(), b.AsInt64()
				switch cmpOp {
				case CMP_EQ:
					cmpRes = ai == bi
				case CMP_NEQ:
					cmpRes = ai != bi
				case CMP_LT:
					cmpRes = ai < bi
				case CMP_LTE:
					cmpRes = ai <= bi
				case CMP_GT:
					cmpRes = ai > bi
				case CMP_GTE:
					cmpRes = ai >= bi
				}
				goto cmpBoolHit
			}
			if a.IsFloat64() && b.IsFloat64() && cmpOp <= CMP_GTE {
				af, bf := a.AsFloat64(), b.AsFloat64()
				switch cmpOp {
				case CMP_EQ:
					cmpRes = af == bf
				case CMP_NEQ:
					cmpRes = af != bf
				case CMP_LT:
					cmpRes = af < bf
				case CMP_LTE:
					cmpRes = af <= bf
				case CMP_GT:
					cmpRes = af > bf
				case CMP_GTE:
					cmpRes = af >= bf
				}
				goto cmpBoolHit
			}
			if cmpOp == CMP_IS {
				cmpRes = a == b
				goto cmpBoolHit
			}
			if cmpOp == CMP_IS_NOT {
				cmpRes = a != b
				goto cmpBoolHit
			}
			if cmpOp <= CMP_GTE &&
				(a.IsInlineStr() || (a.IsString() && a.AsStringObj().Header.Type == val.StrType)) &&
				(b.IsInlineStr() || (b.IsString() && b.AsStringObj().Header.Type == val.StrType)) {
				switch cmpOp {
				case CMP_EQ:
					cmpRes = val.StringEquals(a, b)
				case CMP_NEQ:
					cmpRes = !val.StringEquals(a, b)
				case CMP_LT:
					cmpRes = val.StringCompare(a, b) < 0
				case CMP_LTE:
					cmpRes = val.StringCompare(a, b) <= 0
				case CMP_GT:
					cmpRes = val.StringCompare(a, b) > 0
				case CMP_GTE:
					cmpRes = val.StringCompare(a, b) >= 0
				}
				goto cmpBoolHit
			}
			if cmpOp <= CMP_GTE && a.IsTuple() && b.IsTuple() &&
				a.HeaderPtr().Type == val.TupleType && b.HeaderPtr().Type == val.TupleType {
				st.ip = ip
				vm.execCtx.SP = sp
				res := val.CompareTuplesFast(&vm.execCtx, a, b, cmpOp)
				if !vm.execCtx.HasException() && unsafe.Pointer(unsafe.SliceData(vm.execCtx.Stack)) == stackPtr &&
					(!st.stackIsVM || unsafe.Pointer(unsafe.SliceData(vm.stack)) == stackPtr) {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = res
					sp--
					ip++
					continue
				}
				ip, sp, ctl = vm.finishCompareTupleSlow(st, ip, sp, res)
				break
			}
			if (cmpOp == CMP_IN || cmpOp == CMP_NOT_IN) &&
				(a.IsInlineStr() || (a.IsString() && a.AsStringObj().Header.Type == val.StrType) || a.IsInt64()) {
				if b.IsDict() && b.HeaderPtr().Type == val.DictType {
					_, ok := b.AsDict().Get(a)
					cmpRes = ok == (cmpOp == CMP_IN)
					goto cmpBoolHit
				}
				if b.IsSet() && b.HeaderPtr().Type == val.SetType {
					ok := b.AsSet().Contains(a)
					cmpRes = ok == (cmpOp == CMP_IN)
					goto cmpBoolHit
				}
			}
			ip, sp, ctl = vm.opCompareSlow(st, inst, ip, sp)
			break
		cmpBoolHit:
			*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.BoolVal(cmpRes)
			sp--
			ip++
			continue
		case OP_COMPARE_DUNDER:
			cmpOp := uint8(inst >> 16)
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			entry := &st.cache.ic[ip]
			if entry.Kind == ICKindCompareDunder && !vm.hasTrackedViews &&
				b.Type().Version() == entry.ExpectedAuxVer &&
				(entry.SlotIndex == 2 || a.Type().Version() == entry.ExpectedTypeVer) {
				fn := entry.CachedVal.AsFunction()
				cc := entry.CalleeCache
				if cc != nil && cc.owner == vm && cc.co != nil &&
					fn.Code.HeaderPtr() == &cc.co.Header &&
					len(fn.Defaults) == 0 && fn.KwDefaults == nil &&
					len(cc.co.CellVars) == 0 && (!cc.co.HasCells || fn.Closure != nil) &&
					atomic.LoadInt32(&cc.co.StreamGen) == cc.streamGen {
					callee := cc.co
					newBase := sp - 2
					st.periodicTick++
					if (st.periodicTick&0xFF) != 0 &&
						st.frameIdx+2 <= len(vm.frames) &&
						newBase+max(256, callee.NumLocals+64) <= len(st.stack) {
						if entry.SlotIndex != 0 {
							*(*val.Value)(unsafe.Add(stackPtr, uintptr(newBase)*16)) = b
							*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = a
						}
						saved := &vm.frames[st.frameIdx]
						saved.cmpLeft = a
						saved.cmpRight = b
						saved.cmpOp = cmpOp
						saved.cmpActive = true
						pushFn = fn
						pushCache = cc
						pushTotalArgs = 2
						goto pushFrame
					}
				}
			}
			ip, sp, ctl = vm.opCompareSlow(st, inst, ip, sp)
		case OP_JUMP_IF_TRUE:
			sp--
			cond := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16))
			if cond.IsTruthy() {
				ip = int(uint16(inst))
			} else {
				ip++
			}
			continue

		case OP_JUMP_IF_FALSE_OR_POP:
			if !(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)).IsTruthy() {
				ip = int(uint16(inst))
			} else {
				sp--
				ip++
			}
			continue

		case OP_JUMP_IF_TRUE_OR_POP:
			if (*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)).IsTruthy() {
				ip = int(uint16(inst))
			} else {
				sp--
				ip++
			}
			continue

		case OP_YIELD_VALUE:
			ip, sp, ctl = vm.opYieldValue(st, ip, sp)
		case OP_NOP:
			ip++
			continue

		case OP_FAST_ADD_INT_LOCAL_TO_LOCAL:
			va := locals[uint8(inst>>8)]
			vb := locals[uint8(inst)]
			if va.IsInt64() && vb.IsInt64() {
				ai, bi := va.AsInt64(), vb.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					locals[uint8(inst>>16)] = val.Int64Val(sum)
					ip += 4
					continue
				}
			}
			codePtr = vm.deoptInst(st, ip, inst)
			continue
		case OP_FOR_ITER_RANGE:
			targetReg := uint8(inst >> 16)
			jumpTarget := int(uint16(inst))
			it := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			if it.IsRange() {
				cur := it.RangeStart()
				stop := it.RangeStop()
				if cur < stop {
					if ip+5 < st.codeLen && DecodeOpcode(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+5)*4))) == OP_JUMP && int(DecodeArgBx(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+5)*4)))) == ip {
						op1 := DecodeOpcode(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
						if op1 == OP_FAST_STREAM_WRITE_LOCAL_CONST ||
							(op1 == OP_LOAD_FAST && (DecodeOpcode(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+3)*4))) == OP_CALL_METHOD_PIC || DecodeOpcode(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+3)*4))) == OP_CALL_METHOD)) {
							ip, sp, ctl = vm.opForIterRangeWriteLoop(st, inst, ip, sp)
							break
						}
					}
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.RangeVal(cur+1, stop)
					locals[targetReg] = val.Int64Val(int64(cur))
					ip++
					continue
				}
			}
			sp--
			ip = jumpTarget
			continue
		case OP_FAST_JUMP_IF_DEREF_LIST_SUBSCR_TRUE:
			cellIdx := uint8(inst >> 16)
			exitTarget := int(uint16(inst))
			localIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
			if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
				cVal := st.cellPtrs[cellIdx].Value
				if cVal.IsList() && localIdx < 256 {
					kVal := locals[uint8(localIdx)]
					if kVal.IsInt64() {
						k := kVal.AsInt64()
						items := cVal.AsList().Items
						if uint64(k) < uint64(len(items)) {
							if items[k].AsBool() {
								ip = exitTarget
								continue
							}
							ip += 4
							continue
						}
					}
				}
			}
			ip, sp, ctl = vm.opFastJumpSubscrSlow(st, inst, ip, sp)
		case OP_FAST_STORE_DEREF_LIST_LOCAL_CONST:
			cellIdx := uint8(inst >> 16)
			localIdx := uint16(inst)
			constIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
			if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
				cVal := st.cellPtrs[cellIdx].Value
				if cVal.IsList() && localIdx < 256 {
					kVal := locals[uint8(localIdx)]
					if kVal.IsInt64() {
						k := kVal.AsInt64()
						items := cVal.AsList().Items
						if uint64(k) < uint64(len(items)) && int(constIdx) < len(st.consts) {
							items[k] = *(*val.Value)(unsafe.Add(constsPtr, uintptr(constIdx)*16))
							ip += 4
							continue
						}
					}
				}
			}
			ip, sp, ctl = vm.opFastStoreSubscrSlow(st, inst, ip, sp)
		case OP_FAST_SUB_LOCAL_ADD_DEREF_TO_LOCAL:
			cellIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
			if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
				cVal := st.cellPtrs[cellIdx].Value
				vA := locals[uint8(inst>>8)]
				vB := locals[uint8(inst)]
				if vA.IsInt64() && vB.IsInt64() && cVal.IsInt64() {
					locals[uint8(inst>>16)] = val.Int64Val(vA.AsInt64() - vB.AsInt64() + cVal.AsInt64())
					ip += 6
					continue
				}
			}
			ip, sp, ctl = vm.opFastSubAddDerefSlow(st, inst, ip, sp)
		case OP_BINARY_MUL:
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if a.IsInt64() && b.IsInt64() {
				ai, bi := a.AsInt64(), b.AsInt64()
				if int64(int32(ai)) == ai && int64(int32(bi)) == bi {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(ai * bi)
					sp--
					ip++
					continue
				}
			}
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		case OP_BINARY_MOD:
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if a.IsInt64() && b.IsInt64() {
				ai, bi := a.AsInt64(), b.AsInt64()
				if bi != 0 {
					rem := ai % bi
					if rem != 0 && (rem < 0) != (bi < 0) {
						rem += bi
					}
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.Int64Val(rem)
					sp--
					ip++
					continue
				}
			}
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		case OP_COMPARE_LT:
			b := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
			a := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
			if a.IsInt64() && b.IsInt64() {
				*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.BoolVal(a.AsInt64() < b.AsInt64())
				sp--
				ip++
				continue
			}
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		case OP_FAST_ADD_INT_LOCALS:
			va := locals[uint8(inst>>16)]
			vb := locals[uint8(inst)]
			if va.IsInt64() && vb.IsInt64() {
				ai, bi := va.AsInt64(), vb.AsInt64()
				sum := ai + bi
				if ((ai ^ sum) & (bi ^ sum)) >= 0 {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp)*16)) = val.Int64Val(sum)
					sp++
					ip += 3
					continue
				}
			}
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		case OP_SETUP_RANGE:
			count := int(uint16(inst))
			if count == 1 {
				stopVal := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
				if stopVal.IsInt64() {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16)) = val.RangeVal(0, int32(stopVal.AsInt64()))
					ip++
					continue
				}
			} else if count == 2 {
				stopVal := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-1)*16))
				startVal := *(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16))
				if startVal.IsInt64() && stopVal.IsInt64() {
					*(*val.Value)(unsafe.Add(stackPtr, uintptr(sp-2)*16)) = val.RangeVal(int32(startVal.AsInt64()), int32(stopVal.AsInt64()))
					sp--
					ip++
					continue
				}
			}
			ip++
			continue
		case OP_FAST_CMP_EQ_LOCAL_DEREF_JUMP_IF_FALSE:
			cellIdx := uint16(*(*uint32)(unsafe.Add(codePtr, uintptr(ip+1)*4)))
			if int(cellIdx) < len(st.cellPtrs) && st.cellPtrs[cellIdx] != nil {
				cVal := st.cellPtrs[cellIdx].Value
				lVal := locals[uint8(inst>>16)]
				if lVal.IsInt64() && cVal.IsInt64() {
					if lVal.AsInt64() != cVal.AsInt64() {
						ip = int(uint16(inst))
						continue
					}
					ip += 4
					continue
				}
			}
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		default:
			ip, sp, ctl = vm.dispatchColdOpcode(st, op, inst, ip, sp)
		}
		switch ctl {
		case ctlNext:
			continue
		case ctlRaise:
			ip, sp, ctl = vm.unwind(st, ip)
			if ctl == ctlExit {
				return st.result
			}
		case ctlExit:
			return st.result
		}
		stackPtr = unsafe.Pointer(unsafe.SliceData(st.stack))
		constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
		codePtr = st.codePtr
		locals = (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
		continue
	pushFrame:
		ip, sp = vm.pushFrameFast(st, pushFn, pushCache, sp, pushTotalArgs, ip)
		stackPtr = unsafe.Pointer(unsafe.SliceData(st.stack))
		constsPtr = unsafe.Pointer(unsafe.SliceData(st.consts))
		codePtr = st.codePtr
		locals = (*[256]val.Value)(unsafe.Add(stackPtr, uintptr(st.base)*16))
	}
}

//go:noinline
func (vm *VM) pushFrameFast(
	st *loopState,
	pushFn *val.PyFunction,
	pushCache *VMCodeCache,
	sp, encodedTotalArgs, ip int,
) (int, int) {
	pushCallee := pushCache.co
	pushTotalArgs := encodedTotalArgs
	popCallable := 0
	if pushTotalArgs < 0 {
		pushTotalArgs = ^pushTotalArgs
		popCallable = 1
	}
	pushNewBase := sp - pushTotalArgs
	pushRetSP := pushNewBase - popCallable
	saved := &vm.frames[st.frameIdx]
	saved.co = st.curCO
	saved.codePtr = st.codePtr
	saved.codeLen = st.codeLen
	saved.consts = st.consts
	saved.ip = ip + 1
	saved.baseOffset = st.base
	saved.retSP = pushRetSP
	if saved.cells != nil || st.cells != nil {
		saved.cells = st.cells
	}
	if saved.cellPtrs != nil || st.cellPtrs != nil {
		saved.cellPtrs = st.cellPtrs
	}
	saved.globals = st.globals
	if saved.locals != st.curLocals {
		saved.locals = st.curLocals
	}
	saved.cache = st.cache
	saved.excStackLen = len(vm.excStack)
	saved.stackIsVM = st.stackIsVM
	if !st.curLocals.IsNone() {
		st.curLocals = val.NoneVal()
	}
	st.frameIdx++
	if st.frameIdx > vm.maxFrameIdx {
		vm.maxFrameIdx = st.frameIdx
	}
	if st.stackIsVM {
		if hw := pushNewBase + pushCallee.FrameStackNeed; hw > vm.maxSP {
			vm.maxSP = hw
		}
	}
	st.curCO = pushCallee
	st.cache = pushCache
	st.codePtr = pushCache.codePtr
	st.codeLen = pushCache.codeLen
	st.consts = pushCallee.Consts
	st.base = pushNewBase
	if !pushCallee.HasCells {
		if st.cells != nil || st.cellPtrs != nil {
			st.cells = nil
			st.cellPtrs = nil
		}
	} else {
		st.cells = pushFn.Closure
		st.cellPtrs = pushFn.CellPtrs
	}
	if pushFn.Globals != nil {
		st.globals = pushFn.Globals
	}
	vm.activeCO = pushCallee
	vm.activeBaseOffset = pushNewBase
	if vm.activeCellPtrs != nil || st.cellPtrs != nil {
		vm.activeCells = st.cells
		vm.activeCellPtrs = st.cellPtrs
	}
	vm.activeFrameIdx = st.frameIdx
	return 0, pushNewBase + max(pushCallee.NumLocals, pushTotalArgs)
}

func isCallableConst(v val.Value) bool {
	k := v.Kind()
	return k == val.KindCode || k == val.KindFunction || k == val.KindBuiltinFunc
}

//go:noinline
func (vm *VM) loadMethodSlow(obj, attrName val.Value, ip, sp, frameIdx int, curIC []ICSlot, curMethodPIC [][4]val.MethodPICEntry) (val.Value, val.Value) {
	attrName = val.InternValue(attrName)

	if obj.IsModule() {
		mod := obj.AsModule()
		if obj.Type() == val.ModuleType {
			if fnVal, ok := mod.Dict.Get(attrName); ok && !fnVal.IsLazyImport() {
				return val.UnboundVal(), fnVal
			}
		}
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		res := val.GetAttrCtx(&vm.execCtx, obj, attrName)
		return val.UnboundVal(), res
	}

	if obj.IsSuper() {
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		return val.LoadMethodNoBind(&vm.execCtx, obj, attrName)
	}

	if obj.IsInstance() {
		instObj := obj.AsInstance()
		instType := instObj.Header.Type

		if instType.Flags&val.FlagCustomGetAttribute != 0 {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return val.LoadMethodNoBind(&vm.execCtx, obj, attrName)
		}

		descr, _, foundInClass := instType.LookupMRO(attrName)
		if foundInClass {
			descrType := descr.Type()
			if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
				return val.UnboundVal(), res
			}
		}

		if instObj.Shape != nil {
			if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
				slotVal, okSlot := instObj.GetSlot(slotIdx)
				if okSlot && !slotVal.IsUnbound() {
					curIC[ip] = ICSlot{
						ExpectedShapeID: instObj.Shape.ID,
						Kind:            ICKindInstanceSlot,
						SlotIndex:       int32(slotIdx),
						ExpectedTypeVer: instType.Version(),
					}
					return val.UnboundVal(), slotVal
				}
			}
		}

		if foundInClass {
			if descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind {
				return val.UnboundVal(), descr
			}
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
					TypeVersion: instType.Version(),
					TargetFn:    targetFn,
					NativeFn:    nativeFn,
					TargetVal:   descr,
				}
				inserted := false
				for k := range 4 {
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
					ExpectedTypeVer: instType.Version(),
					CachedVal:       descr,
				}
				return obj, descr
			}
			if descr.Kind() == val.KindStaticMethod {
				return val.UnboundVal(), descr.AsStaticMethod().Callable
			}
			if descr.Kind() == val.KindClassMethod {
				return val.TypeVal(instType), descr.AsClassMethod().Callable
			}
			if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				res := descrGet(&vm.execCtx, descr, obj, instType)
				return val.UnboundVal(), res
			}
			return val.UnboundVal(), descr
		}

		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		return val.LoadMethodNoBind(&vm.execCtx, obj, attrName)
	}

	if obj.IsType() {
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		return val.LoadMethodNoBind(&vm.execCtx, obj, attrName)
	}

	// Primitive or built-in heap type method lookup.
	tp := obj.Type()
	if descr, _, ok := tp.LookupMRO(attrName); ok {
		if descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind {
			return val.UnboundVal(), descr
		}
		if descr.IsFunction() || descr.IsBuiltinFunc() || descr.Kind() == val.KindCode {
			return obj, descr
		}
		if descr.Kind() == val.KindStaticMethod {
			return val.UnboundVal(), descr.AsStaticMethod().Callable
		}
		if descr.Kind() == val.KindClassMethod {
			return val.TypeVal(tp), descr.AsClassMethod().Callable
		}
		if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			res := descrGet(&vm.execCtx, descr, obj, tp)
			return val.UnboundVal(), res
		}
		return val.UnboundVal(), descr
	}

	vm.execCtx.Raise(val.NewLazyAttributeError(tp, attrName))
	return val.UnboundVal(), val.NoneVal()
}

func (vm *VM) callMethodPICSlow(receiver, attrName val.Value, ip, newBase, argc, frameIdx int, curMethodPIC [][4]val.MethodPICEntry) val.Value {
	attrName = val.InternValue(attrName)
	totalArgs := argc + 1

	if receiver.IsType() || receiver.IsSuper() || receiver.IsModule() {
		vm.execCtx.SP = newBase + totalArgs
		vm.activeFrameIdx = frameIdx
		rec, fn := val.LoadMethodNoBind(&vm.execCtx, receiver, attrName)
		if vm.execCtx.HasException() {
			return val.NoneVal()
		}
		if !rec.IsUnbound() {
			vm.execCtx.Stack[newBase] = rec
			return vm.callFromHook(&vm.execCtx, fn, newBase, totalArgs, nil)
		}
		return vm.callFromHook(&vm.execCtx, fn, newBase+1, argc, nil)
	}

	recType := receiver.Type()
	if receiver.IsInstance() {
		instObj := receiver.AsInstance()
		if instType := instObj.Header.Type; instType.Flags&val.FlagCustomGetAttribute != 0 {
			vm.execCtx.SP = newBase + totalArgs
			vm.activeFrameIdx = frameIdx
			rec, fn := val.LoadMethodNoBind(&vm.execCtx, receiver, attrName)
			if vm.execCtx.HasException() {
				return val.NoneVal()
			}
			if !rec.IsUnbound() {
				vm.execCtx.Stack[newBase] = rec
				return vm.callFromHook(&vm.execCtx, fn, newBase, totalArgs, nil)
			}
			return vm.callFromHook(&vm.execCtx, fn, newBase+1, argc, nil)
		}
		if instObj.Shape != nil {
			if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
				callable, okSlot := instObj.GetSlot(slotIdx)
				if okSlot && !callable.IsUnbound() {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					return vm.callFromHook(&vm.execCtx, callable, newBase+1, argc, nil)
				}
			}
		}
	}

	if recType != nil {
		descr, _, foundInClass := recType.LookupMRO(attrName)
		if foundInClass {
			if descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind) {
				if receiver.IsInstance() {
					instObj := receiver.AsInstance()
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
						TypeVersion: recType.Version(),
						TargetFn:    targetFn,
						NativeFn:    nativeFn,
						TargetVal:   descr,
					}
					inserted := false
					for k := range 4 {
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
				}

				if descr.IsBuiltinFunc() {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					return descr.AsBuiltinFunc().Fn(&vm.execCtx, descr, vm.execCtx.Stack[newBase:newBase+totalArgs], nil)
				}
				if descr.IsFunction() {
					vm.execCtx.SP = newBase + totalArgs
					vm.activeFrameIdx = frameIdx
					return vm.callFromHook(&vm.execCtx, descr, newBase, totalArgs, nil)
				}
			}
		}
	}

	vm.execCtx.SP = newBase + totalArgs
	vm.activeFrameIdx = frameIdx
	rec, fn := val.LoadMethodNoBind(&vm.execCtx, receiver, attrName)
	if vm.execCtx.HasException() {
		return val.NoneVal()
	}
	if !rec.IsUnbound() {
		vm.execCtx.Stack[newBase] = rec
		return vm.callFromHook(&vm.execCtx, fn, newBase, totalArgs, nil)
	}
	return vm.callFromHook(&vm.execCtx, fn, newBase+1, argc, nil)
}

func insertAttrPIC(pic *[4]AttrPICEntry, shape *val.Shape, slotIdx int, typeVer uint64) {
	entry := AttrPICEntry{
		Shape:     shape,
		SlotIndex: int32(slotIdx),
		TypeVer:   uint32(typeVer),
	}
	for k := range 4 {
		if pic[k].Shape == nil || pic[k].Shape == shape {
			pic[k] = entry
			return
		}
	}
	pic[0] = pic[1]
	pic[1] = pic[2]
	pic[2] = pic[3]
	pic[3] = entry
}

//go:noinline
func (vm *VM) loadAttrInstanceSlow(instObj *val.PyInstance, obj, attrName val.Value, ip, sp, frameIdx int, curIC []ICSlot, curAttrPIC [][4]AttrPICEntry) val.Value {
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type
	typeVer := instType.Version()

	descr, _, foundInClass := instType.LookupMRO(attrName)
	if foundInClass {
		if descr.IsMemberDescr() {
			md := descr.AsMemberDescr()
			if md.SlotIndex >= 0 && md.SlotIndex < len(instObj.Slots) {
				curIC[ip] = ICSlot{
					Kind:            ICKindSlotMember,
					ExpectedTypeVer: typeVer,
					SlotIndex:       int32(md.SlotIndex),
				}
				v, okSlot := instObj.GetSlot(md.SlotIndex)
				if okSlot && !v.IsUnbound() {
					return v
				}
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			return descr.Type().Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
		}
		if descr.IsProperty() {
			prop := descr.AsProperty()
			if prop.FGet.IsFunction() || prop.FGet.IsBuiltinFunc() {
				curIC[ip] = ICSlot{
					Kind:            ICKindProperty,
					ExpectedTypeVer: typeVer,
					CachedVal:       prop.FGet,
					CalleeCache:     vm.cacheForPropertyFGet(prop.FGet),
				}
				vm.execCtx.SP = sp
				vm.activeFrameIdx = frameIdx
				if prop.FGet.IsBuiltinFunc() {
					return prop.FGet.AsBuiltinFunc().Fn(&vm.execCtx, prop.FGet, vm.execCtx.Stack[sp-1:sp], nil)
				}
				if descrGet := descr.Type().Slots.TpDescrGet; descrGet != nil {
					return descrGet(&vm.execCtx, descr, obj, instType)
				}
				return val.GetAttrCtx(&vm.execCtx, obj, attrName)
			}
		}
		descrType := descr.Type()
		if descr.IsDataDescriptor() || (descrType.Slots.TpDescrGet != nil && descrType.Slots.TpDescrSet != nil) {
			kind := ICKindDataDescriptor
			if descrType != nil && descrType.Name == "struct_field_descriptor" {
				kind = ICKindNativeDescr
			}
			curIC[ip] = ICSlot{
				Kind:            kind,
				ExpectedTypeVer: typeVer,
				CachedVal:       descr,
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			if descrType.Slots.TpDescrGet != nil {
				return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
			}
			return descr
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		insertAttrPIC(&curAttrPIC[ip], instObj.Shape, slotIdx, typeVer)
		curIC[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       int32(slotIdx),
			ExpectedTypeVer: typeVer,
		}
		v, _ := instObj.GetSlot(slotIdx)
		if !v.IsUnbound() {
			return v
		}
		if !foundInClass {
			vm.execCtx.Raise(val.NewLazyAttributeError(instType, attrName))
			return val.NoneVal()
		}
	}

	if foundInClass {
		descrType := descr.Type()
		if descr.IsFunction() || (descr.IsBuiltinFunc() && !descr.AsBuiltinFunc().NoBind) {
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindClassFunc,
				ExpectedTypeVer: typeVer,
				CachedVal:       descr,
			}
			return val.NewBoundMethodVal(obj, descr)
		}
		if descr.Kind() == val.KindStaticMethod {
			smVal := descr.AsStaticMethod().Callable
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindClassAttr,
				ExpectedTypeVer: typeVer,
				CachedVal:       smVal,
			}
			return smVal
		}
		if descrType.Slots.TpDescrGet == nil || (descr.IsBuiltinFunc() && descr.AsBuiltinFunc().NoBind) {
			curIC[ip] = ICSlot{
				ExpectedShapeID: instObj.Shape.ID,
				Kind:            ICKindClassAttr,
				ExpectedTypeVer: typeVer,
				CachedVal:       descr,
			}
			return descr
		}
		vm.execCtx.SP = sp
		vm.activeFrameIdx = frameIdx
		return descrType.Slots.TpDescrGet(&vm.execCtx, descr, obj, instType)
	}

	vm.execCtx.SP = sp
	vm.activeFrameIdx = frameIdx
	return val.GetAttrCtx(&vm.execCtx, obj, attrName)
}

//go:noinline
func (vm *VM) storeAttrInstanceSlow(instObj *val.PyInstance, obj, attrName, v val.Value, ip, sp, frameIdx int, cache *VMCodeCache) {
	curIC := cache.ic
	curAttrPIC := cache.ensureAttrPIC()
	attrName = val.InternValue(attrName)
	instType := instObj.Header.Type
	typeVer := instType.Version()

	if attrName == nameDunderDict && instType.Flags&val.FlagForbidDynamicAttrs == 0 {
		val.ReplaceInstanceDict(&vm.execCtx, instObj, v)
		return
	}
	if attrName == nameClass {
		val.AssignInstanceClass(obj, v)
		return
	}

	if descr, _, foundInClass := instType.LookupMRO(attrName); foundInClass {
		if descr.IsMemberDescr() {
			md := descr.AsMemberDescr()
			if md.SlotIndex >= 0 && md.SlotIndex < len(instObj.Slots) && instObj.SetSlot(md.SlotIndex, v) {
				curIC[ip] = ICSlot{
					Kind:            ICKindSlotMember,
					ExpectedTypeVer: typeVer,
					SlotIndex:       int32(md.SlotIndex),
				}
				return
			}
		}
		if descr.IsProperty() {
			prop := descr.AsProperty()
			if prop.FSet.IsFunction() || prop.FSet.IsBuiltinFunc() {
				curIC[ip] = ICSlot{
					Kind:            ICKindPropertySet,
					ExpectedTypeVer: typeVer,
					CachedVal:       prop.FSet,
				}
			}
		}
		descrType := descr.Type()
		if descrType.Slots.TpDescrSet != nil {
			if !descr.IsProperty() || (descr.AsProperty().FSet.IsNone() || descr.AsProperty().FSet.IsUnbound()) {
				kind := ICKindDataDescriptor
				if descrType.Name == "struct_field_descriptor" {
					kind = ICKindNativeDescr
				}
				curIC[ip] = ICSlot{
					Kind:            kind,
					ExpectedTypeVer: typeVer,
					CachedVal:       descr,
				}
			}
			vm.execCtx.SP = sp
			vm.activeFrameIdx = frameIdx
			descrType.Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
			return
		}
	}

	if slotIdx, ok := instObj.Shape.Lookup(attrName); ok {
		instObj.SetSlot(slotIdx, v)
		insertAttrPIC(&curAttrPIC[ip], instObj.Shape, slotIdx, typeVer)
		curIC[ip] = ICSlot{
			ExpectedShapeID: instObj.Shape.ID,
			Kind:            ICKindInstanceSlot,
			SlotIndex:       int32(slotIdx),
			ExpectedTypeVer: typeVer,
		}
		return
	}

	if instType.Flags&val.FlagForbidDynamicAttrs != 0 {
		vm.execCtx.Raise(val.NewAttributeError("'" + instType.Name + "' object has no attribute '" + attrName.AsString() + "'"))
		return
	}

	oldShape := instObj.Shape
	slotIdx := instObj.SetAttr(attrName, v)
	newShape := instObj.Shape
	insertAttrPIC(&curAttrPIC[ip], newShape, slotIdx, typeVer)
	if oldShape != nil && newShape != nil && newShape != oldShape {
		transIC := cache.ensureTransitionIC()
		transIC[ip] = TransitionICEntry{
			OldShape: oldShape,
			NewShape: newShape,
			TypeVer:  typeVer,
			SlotIdx:  int32(slotIdx),
		}
		curIC[ip] = ICSlot{
			ExpectedShapeID: oldShape.ID,
			Kind:            ICKindInstanceTransition,
			SlotIndex:       int32(slotIdx),
			ExpectedTypeVer: typeVer,
		}
		return
	}
	curIC[ip] = ICSlot{
		ExpectedShapeID: newShape.ID,
		Kind:            ICKindInstanceSlot,
		SlotIndex:       int32(slotIdx),
		ExpectedTypeVer: typeVer,
	}
}

// panicToPyException converts a recovered Go panic payload into a Python exception Value,
// while re-panicking genuine Go runtime errors (nil dereference, bounds out of range, etc.).
func panicToPyException(r any) val.Value {
	if re, ok := r.(runtime.Error); ok {
		panic(re)
	}
	if v, ok := r.(val.Value); ok {
		return v
	}
	var msg string
	switch x := r.(type) {
	case string:
		msg = x
	case error:
		msg = x.Error()
	default:
		msg = fmt.Sprint(r)
	}
	clean := strings.TrimSpace(msg)
loopClean:
	for {
		switch {
		case strings.HasPrefix(clean, "vm: "):
			clean = strings.TrimSpace(clean[4:])
		case strings.HasPrefix(clean, "val: "):
			clean = strings.TrimSpace(clean[5:])
		default:
			break loopClean
		}
	}

	type excPrefix struct {
		prefix string
		cls    *val.PyType
	}
	prefixes := [...]excPrefix{
		{"ModuleNotFoundError:", val.ModuleNotFoundErrorType},
		{"ImportError:", val.ImportErrorType},
		{"AttributeError:", val.AttributeErrorType},
		{"TypeError:", val.TypeErrorType},
		{"ValueError:", val.ValueErrorType},
		{"KeyError:", val.KeyErrorType},
		{"IndexError:", val.IndexErrorType},
		{"UnboundLocalError:", val.UnboundLocalErrorType},
		{"NameError:", val.NameErrorType},
		{"ZeroDivisionError:", val.ZeroDivisionErrorType},
		{"OverflowError:", val.OverflowErrorType},
		{"NotImplementedError:", val.NotImplementedErrorType},
		{"RecursionError:", val.RecursionErrorType},
		{"AssertionError:", val.AssertionErrorType},
		{"FileNotFoundError:", val.FileNotFoundErrorType},
		{"PermissionError:", val.PermissionErrorType},
		{"FileExistsError:", val.FileExistsErrorType},
		{"IsADirectoryError:", val.IsADirectoryErrorType},
		{"NotADirectoryError:", val.NotADirectoryErrorType},
		{"TimeoutError:", val.TimeoutErrorType},
		{"OSError:", val.OSErrorType},
		{"IndentationError:", val.IndentationErrorType},
		{"TabError:", val.TabErrorType},
		{"SyntaxError:", val.SyntaxErrorType},
		{"EOFError:", val.EOFErrorType},
		{"LookupError:", val.LookupErrorType},
		{"ArithmeticError:", val.ArithmeticErrorType},
		{"BufferError:", val.BufferErrorType},
		{"ReferenceError:", val.ReferenceErrorType},
		{"MemoryError:", val.MemoryErrorType},
		{"SystemError:", val.SystemErrorType},
		{"RuntimeError:", val.RuntimeErrorType},
	}
	for _, p := range prefixes {
		if strings.HasPrefix(clean, p.prefix) {
			detail := strings.TrimSpace(clean[len(p.prefix):])
			return val.NewException(p.cls, detail)
		}
	}
	if clean == "StopIteration" || strings.HasPrefix(clean, "StopIteration:") {
		detail := strings.TrimSpace(strings.TrimPrefix(clean, "StopIteration:"))
		if detail == "" {
			return val.NewStopIteration(val.NoneVal())
		}
		return val.NewStopIteration(val.InternString(detail))
	}
	return val.NewRuntimeError(clean)
}

func (vm *VM) dictGetSafe(d *val.PyDict, key val.Value) (val.Value, bool) {
	return d.Get(key)
}

func (vm *VM) dictSetSafe(d *val.PyDict, key, v val.Value) {
	d.Set(key, v)
}

func (vm *VM) getItemSafe(obj, key val.Value) val.Value {
	return val.GetItemCtx(&vm.execCtx, obj, key)
}

func (vm *VM) setItemSafe(obj, key, v val.Value) {
	val.SetItemCtx(&vm.execCtx, obj, key, v)
}

func (vm *VM) instantiateGeneratorSafe(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict, ephemeral bool) (val.Value, bool) {
	res := vm.instantiateGenerator(co, callable, defaults, kwDefaults, callerStack, argBase, nargs, kwnames, globals, ephemeral)
	return res, !vm.execCtx.HasException()
}

func (vm *VM) instantiateCoroutineSafe(co *CodeObject, callable val.Value, defaults []val.Value, kwDefaults *val.PyDict, callerStack []val.Value, argBase int, nargs int, kwnames []val.Value, globals *val.PyDict) (val.Value, bool) {
	res := vm.instantiateCoroutine(co, callable, defaults, kwDefaults, callerStack, argBase, nargs, kwnames, globals)
	return res, !vm.execCtx.HasException()
}

func (vm *VM) bindFunctionArgsSafe(co *CodeObject, defaults []val.Value, kwDefaults *val.PyDict, stack []val.Value, argBase int, nargs int, kwnames []val.Value) (int, bool) {
	nextSP := vm.bindFunctionArgs(co, defaults, kwDefaults, stack, argBase, nargs, kwnames)
	return nextSP, !vm.execCtx.HasException()
}

func (vm *VM) invokeBuiltinSafe(fn val.VectorcallFunc, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
	return fn(&vm.execCtx, callable, args, kwnames)
}

func (vm *VM) callFromHookSafe(callable val.Value, argBase int, nargs int, kwnames []val.Value) val.Value {
	return vm.callFromHook(&vm.execCtx, callable, argBase, nargs, kwnames)
}

func (vm *VM) loadMethodSlowSafe(obj, attrName val.Value, ip, sp, frameIdx int, curIC []ICSlot, curMethodPIC [][4]val.MethodPICEntry) (val.Value, val.Value) {
	return vm.loadMethodSlow(obj, attrName, ip, sp, frameIdx, curIC, curMethodPIC)
}

func (vm *VM) callMethodPICSlowSafe(receiver, attrName val.Value, ip, newBase, argc, frameIdx int, curMethodPIC [][4]val.MethodPICEntry) val.Value {
	return vm.callMethodPICSlow(receiver, attrName, ip, newBase, argc, frameIdx, curMethodPIC)
}

func (vm *VM) getAttrSafe(obj, attrName val.Value) val.Value {
	v, ok := val.LookupAttrOptional(&vm.execCtx, obj, attrName)
	if !ok && !vm.execCtx.HasException() {
		vm.execCtx.Raise(val.NewLazyAttributeError(obj.Type(), val.InternValue(attrName)))
		return val.NoneVal()
	}
	return v
}

func (vm *VM) descrGetSafe(descr, obj val.Value, owner *val.PyType) val.Value {
	if descr.Type() == nil || descr.Type().Slots.TpDescrGet == nil {
		return descr
	}
	return descr.Type().Slots.TpDescrGet(&vm.execCtx, descr, obj, owner)
}

func (vm *VM) loadAttrInstanceSlowSafe(instObj *val.PyInstance, obj, attrName val.Value, ip, sp, frameIdx int, curIC []ICSlot, curAttrPIC [][4]AttrPICEntry) val.Value {
	return vm.loadAttrInstanceSlow(instObj, obj, attrName, ip, sp, frameIdx, curIC, curAttrPIC)
}

func (vm *VM) setAttrSafe(obj, attrName, v val.Value) {
	val.SetAttrCtx(&vm.execCtx, obj, attrName, v)
}

func (vm *VM) descrSetSafe(descr, obj, v val.Value) {
	if descr.Type() != nil && descr.Type().Slots.TpDescrSet != nil {
		descr.Type().Slots.TpDescrSet(&vm.execCtx, descr, obj, v)
	}
}

func (vm *VM) storeAttrInstanceSlowSafe(instObj *val.PyInstance, obj, attrName, v val.Value, ip, sp, frameIdx int, cache *VMCodeCache) {
	vm.storeAttrInstanceSlow(instObj, obj, attrName, v, ip, sp, frameIdx, cache)
}

func (vm *VM) getIterSafe(obj val.Value) val.Value {
	tp := obj.Type()
	if tpIter := tp.Slots.TpIter; tpIter != nil {
		return tpIter(&vm.execCtx, obj)
	}
	return newIterCheckedCtx(&vm.execCtx, obj)
}

// newIterCheckedCtx builds an iterator for obj and signals TypeError via ctx
// when obj supports no iteration protocol (PEP 234).
func newIterCheckedCtx(ctx *val.ExecContext, obj val.Value) val.Value {
	tp := obj.Type()
	it := val.NewIterVal(obj)
	if it.IsIter() && it.AsIter().Kind == val.IterKindInvalid {
		if tp.Slots.TpIterNext == nil && tp.Slots.SqItem == nil {
			if _, _, ok := tp.LookupMRO(nameNext); !ok {
				if _, _, ok := tp.LookupMRO(nameGetitem); !ok {
					return val.RaiseTypeError(ctx, "'"+tp.Name+"' object is not iterable")
				}
			}
		}
	}
	return it
}

func (vm *VM) iterNextSafe(tpIterNext func(*val.ExecContext, val.Value) (val.Value, val.IterStatus), it val.Value) (val.Value, val.IterStatus) {
	return tpIterNext(&vm.execCtx, it)
}

func (vm *VM) buildClassSafe(classBody, nameVal val.Value, origBases []val.Value, kwdsVal val.Value, globals *val.PyDict) val.Value {
	return vm.buildClass(classBody, nameVal, origBases, kwdsVal, globals)
}

func (vm *VM) extractIterableSafe(seq val.Value) []val.Value {
	return val.ExtractIterableItems(&vm.execCtx, seq)
}

func (vm *VM) formatValueSafe(v val.Value, spec string, conversion uint8) val.Value {
	ctx := &vm.execCtx
	switch conversion {
	case 'r':
		v = val.NewStringVal(val.PyRepr(ctx, v))
	case 's':
		v = val.NewStringVal(val.PyStr(ctx, v))
	case 'a':
		v = val.NewStringVal(val.PyASCII(ctx, v))
	}
	return val.NewStringVal(val.PyFormat(ctx, v, spec))
}

func (vm *VM) compareOpSafe(a, b val.Value, cmpOp uint8) val.Value {
	return val.RichCompare(&vm.execCtx, a, b, cmpOp)
}

func (vm *VM) delAttrSafe(obj, attrName val.Value) {
	val.DelAttrCtx(&vm.execCtx, obj, attrName)
}

func (vm *VM) delSubscrSafe(obj, key val.Value) {
	if obj.IsDict() && obj.Type() == val.DictType {
		if !obj.AsDict().Delete(key) {
			vm.execCtx.Raise(val.NewKeyError(key))
		}
		return
	}
	if obj.IsList() && obj.Type() == val.ListType {
		lst := obj.AsList()
		if key.IsInt64() {
			n := len(lst.Items)
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += n
			}
			if idx < 0 || idx >= n {
				vm.execCtx.Raise(val.NewIndexError("list assignment index out of range"))
				return
			}
			copy(lst.Items[idx:], lst.Items[idx+1:])
			lst.Items[n-1] = val.NoneVal()
			lst.Items = lst.Items[:n-1]
			return
		}
		if key.IsSlice() {
			sl := key.AsSlice()
			start, stop, step, sliceLen, err := val.NormalizeSliceIndices(sl.Start, sl.Stop, sl.Step, len(lst.Items))
			if err != nil {
				vm.execCtx.Raise(panicToPyException(err.Error()))
				return
			}
			if sliceLen == 0 {
				return
			}
			if step == 1 {
				copy(lst.Items[start:], lst.Items[stop:])
				newLen := len(lst.Items) - sliceLen
				for i := newLen; i < len(lst.Items); i++ {
					lst.Items[i] = val.NoneVal()
				}
				lst.Items = lst.Items[:newLen]
				return
			}
			delSet := make(map[int]bool, sliceLen)
			cur := start
			for range sliceLen {
				delSet[cur] = true
				cur += step
			}
			dst := 0
			for i, item := range lst.Items {
				if !delSet[i] {
					lst.Items[dst] = item
					dst++
				}
			}
			for i := dst; i < len(lst.Items); i++ {
				lst.Items[i] = val.NoneVal()
			}
			lst.Items = lst.Items[:dst]
			return
		}
		vm.execCtx.Raise(val.NewTypeError("list indices must be integers or slices, not " + key.Type().Name))
		return
	}
	if obj.IsByteArray() && obj.Type() == val.ByteArrayType {
		ba := obj.AsByteArray()
		if key.IsInt64() {
			n := len(ba.Data)
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += n
			}
			if idx < 0 || idx >= n {
				vm.execCtx.Raise(val.NewIndexError("bytearray index out of range"))
				return
			}
			ba.Data = append(ba.Data[:idx], ba.Data[idx+1:]...)
			return
		}
	}
	if delFn, _, ok := obj.Type().LookupMRO(nameDelitem); ok {
		vm.execCtx.Invoke2(delFn, obj, key)
		return
	}
	vm.execCtx.Raise(val.NewTypeError("'" + obj.Type().Name + "' object does not support item deletion"))
}

func (vm *VM) unpackMapIntoSafe(resDict *val.PyDict, mapping val.Value) {
	if mapping.IsDict() {
		for _, item := range mapping.AsDict().Items() {
			resDict.Set(item.Key, item.Value)
		}
		return
	}
	if mapping.IsMappingProxy() {
		mp := mapping.AsMappingProxy()
		if !mp.Mapping.IsNone() && mp.Mapping.HeaderPtr() != nil {
			vm.unpackMapIntoSafe(resDict, mp.Mapping)
		}
		return
	}
	keysMeth, ok := vmGetAttrSafe(&vm.execCtx, mapping, "keys")
	if !ok {
		vm.execCtx.Raise(val.NewTypeError("'" + mapping.Type().Name + "' object is not a mapping"))
		return
	}
	keysIter := vm.execCtx.Invoke0(keysMeth)
	if vm.execCtx.HasException() {
		return
	}
	keys := val.ExtractIterableItems(&vm.execCtx, keysIter)
	if vm.execCtx.HasException() {
		return
	}
	for _, k := range keys {
		v := val.GetItemCtx(&vm.execCtx, mapping, k)
		if vm.execCtx.HasException() {
			return
		}
		resDict.Set(k, v)
	}
}

func (vm *VM) callFunctionExSafe(callable, argsVal, kwDictVal val.Value, hasKW bool) val.Value {
	var posArgs []val.Value
	switch {
	case argsVal.IsTuple():
		posArgs = argsVal.AsTuple().Items
	case argsVal.IsList():
		posArgs = argsVal.AsList().Items
	default:
		posArgs = val.ExtractIterableItems(&vm.execCtx, argsVal)
		if vm.execCtx.HasException() {
			return val.NoneVal()
		}
	}

	if !hasKW || kwDictVal.IsNone() || kwDictVal.HeaderPtr() == nil {
		return vm.Vectorcall(callable, posArgs, nil)
	}

	var argsBuf [16]val.Value
	var argsSlice []val.Value
	var kwnames []val.Value

	if kwDictVal.IsMappingProxy() {
		kwDictVal = kwDictVal.AsMappingProxy().Mapping
	}

	if kwDictVal.IsDict() {
		d := kwDictVal.AsDict()
		kwLen := d.Len()
		if kwLen == 0 {
			return vm.Vectorcall(callable, posArgs, nil)
		}
		total := len(posArgs) + kwLen
		if total <= len(argsBuf) {
			argsSlice = argsBuf[:len(posArgs)]
			copy(argsSlice, posArgs)
		} else {
			argsSlice = make([]val.Value, len(posArgs), total)
			copy(argsSlice, posArgs)
		}
		kwnames = make([]val.Value, 0, kwLen)
		if d.Live == nil && !d.Shared && !val.MultiThreadedFlag.Load() {
			for i := range d.Entries {
				e := &d.Entries[i]
				if !e.Active {
					continue
				}
				if !e.Key.IsAnyString() {
					vm.execCtx.Raise(val.NewTypeError("keywords must be strings"))
					return val.NoneVal()
				}
				kwnames = append(kwnames, val.InternValue(e.Key))
				argsSlice = append(argsSlice, e.Value)
			}
		} else {
			for _, item := range d.Items() {
				if !item.Key.IsAnyString() {
					vm.execCtx.Raise(val.NewTypeError("keywords must be strings"))
					return val.NoneVal()
				}
				kwnames = append(kwnames, val.InternValue(item.Key))
				argsSlice = append(argsSlice, item.Value)
			}
		}
	} else {
		keysMeth, ok := vmGetAttrSafe(&vm.execCtx, kwDictVal, "keys")
		if !ok {
			vm.execCtx.Raise(val.NewTypeError("argument after ** must be a mapping, not " + kwDictVal.Type().Name))
			return val.NoneVal()
		}
		keysIter := vm.execCtx.Invoke0(keysMeth)
		if vm.execCtx.HasException() {
			return val.NoneVal()
		}
		keys := val.ExtractIterableItems(&vm.execCtx, keysIter)
		if vm.execCtx.HasException() {
			return val.NoneVal()
		}
		total := len(posArgs) + len(keys)
		if total <= len(argsBuf) {
			argsSlice = argsBuf[:len(posArgs)]
			copy(argsSlice, posArgs)
		} else {
			argsSlice = make([]val.Value, len(posArgs), total)
			copy(argsSlice, posArgs)
		}
		kwnames = make([]val.Value, 0, len(keys))
		for _, k := range keys {
			v := val.GetItemCtx(&vm.execCtx, kwDictVal, k)
			if vm.execCtx.HasException() {
				return val.NoneVal()
			}
			if !k.IsAnyString() {
				vm.execCtx.Raise(val.NewTypeError("keywords must be strings"))
				return val.NoneVal()
			}
			kwnames = append(kwnames, val.InternValue(k))
			argsSlice = append(argsSlice, v)
		}
	}

	return vm.Vectorcall(callable, argsSlice, kwnames)
}

func opcodeOf(inst uint32) Opcode { return Opcode(inst >> 24) }

// mappingGetOptional looks name up in mapping, treating a KeyError as a miss.
// Any other exception is left pending on the execution context.
//
//go:noinline
func (vm *VM) mappingGetOptional(mapping, name val.Value) (val.Value, bool) {
	if mapping.IsNone() || mapping.HeaderPtr() == nil {
		return val.NoneVal(), false
	}
	if mapping.Type() == val.DictType {
		return mapping.AsDict().Get(name)
	}
	res := vm.getItemSafe(mapping, name)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		if exc.HeaderPtr() != nil && val.IsSubclass(exc.Type(), val.KeyErrorType) {
			vm.execCtx.ClearException()
		}
		return val.NoneVal(), false
	}
	return res, true
}

// globalOrBuiltin resolves name like OP_LOAD_GLOBAL: the globals mapping
// (honouring a dict subclass's __missing__), then builtins.
//
//go:noinline
func (vm *VM) globalOrBuiltin(globals *val.PyDict, name val.Value) (val.Value, bool) {
	if globals != nil {
		if v, ok := globals.Get(name); ok {
			return v, true
		}
		if t := globals.Header.Type; t != val.DictType && t != nil {
			if _, _, has := t.LookupMRO(val.DunderMissing); has {
				res := vm.getItemSafe(val.ObjectVal(&globals.Header), name)
				if vm.execCtx.HasException() {
					return val.NoneVal(), false
				}
				return res, true
			}
		}
	}
	if vm.builtins != nil {
		if v, ok := vm.builtins.Get(name); ok {
			return v, true
		}
	}
	return val.NoneVal(), false
}

// instanceAttrDict returns the attribute dict of a function object, or nil.
func instanceAttrDict(v val.Value) *val.PyDict {
	if v.IsFunction() {
		return v.AsFunction().Dict
	}
	return nil
}
