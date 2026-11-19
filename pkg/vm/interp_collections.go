package vm

import (
	"fmt"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

//go:noinline
func (vm *VM) opStoreSubscr(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	key := stack[sp-1]
	obj := stack[sp-2]
	valToStore := stack[sp-3]
	val.ClearEphemeral(valToStore)
	val.DetachStringBuf(valToStore)
	markViewEscaped(valToStore)
	if obj.Kind() == val.KindMemoryView && key.IsInt64() && valToStore.IsInt64() && obj.AsMemoryView().ItemSize <= 1 && obj.AsMemoryView().NDim <= 1 {
		mv := obj.AsMemoryView()
		if !mv.ReadOnly {
			buf := mv.Buf
			k := key.AsInt64()
			if k < 0 {
				k += int64(len(buf))
			}
			if k >= 0 && k < int64(len(buf)) {
				buf[k] = byte(valToStore.AsInt64())
				sp -= 3
				ip++
				return ip, sp, ctlReload
			}
		}
	}
	switch {
	case obj.IsList() && key.IsInt64() && obj.Type() == val.ListType:
		lst := obj.AsList()
		k := key.AsInt64()
		if k >= 0 && k < int64(len(lst.Items)) {
			lst.Items[k] = valToStore
			sp -= 3
			ip++
			return ip, sp, ctlReload
		}
		if !lst.Set(int(k), valToStore) {
			exc := val.NewIndexError("list assignment index out of range")
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
	case obj.IsDict() && obj.Type() == val.DictType:
		if key.IsInt64() || key.IsAnyString() {
			obj.AsDict().Set(key, valToStore)
		} else {
			vm.dictSetSafe(obj.AsDict(), key, valToStore)
			st.syncStackFromVM(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
		}
	default:
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = sp
		vm.setItemSafe(obj, key, valToStore)
		st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
	}
	sp -= 3
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opMapAdd(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	depth := int(uint16(inst))
	dictVal := stack[sp-depth-1]
	v := stack[sp-1]
	k := stack[sp-2]
	sp -= 2
	val.DetachStringBuf(k)
	val.DetachStringBuf(v)
	markViewEscaped(v)
	dictVal.AsDict().Set(k, v)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildSlice(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	switch count {
	case 2:
		stop := stack[sp-1]
		start := stack[sp-2]
		stack[sp-2] = val.NewSliceVal(start, stop, val.NoneVal())
		sp--
	case 3:
		step := stack[sp-1]
		stop := stack[sp-2]
		start := stack[sp-3]
		stack[sp-3] = val.NewSliceVal(start, stop, step)
		sp -= 2
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBinarySlice(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := (*[256]val.Value)(unsafe.Pointer(&stack[st.base]))
	stopVal := stack[sp-1]
	startVal := stack[sp-2]
	container := stack[sp-3]

	if container.Kind() == val.KindMemoryView {
		vm.hasTrackedViews = true
		mv := container.AsMemoryView()
		// The sub-view shares the payload of its parent, so the parent
		// must never be recycled while the sub-view exists.
		mv.Escaped = true
		// ND/typed: slice in element space (shape[0]); byte 1D is a special case.
		mvLen := len(mv.Buf)
		if mv.NDim > 0 {
			mvLen = mv.Dim(0)
		} else if mv.ItemSize > 1 {
			mvLen = len(mv.Buf) / mv.ItemSize
		}
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), mvLen)
		if err != nil {
			exc := val.NewTypeError(err.Error())
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
		// A slice that is stored into a local slot right away reuses
		// the view that slot already holds, so the loop allocates no
		// view at all and touches no pointer field it does not have
		// to change.
		newMv := reusableViewFor(st.codePtr, ip, locals[:])
		if newMv == nil {
			newMv = vm.allocMemoryView()
			newMv.Header = val.ObjectHeader{Kind: val.KindMemoryView, Type: val.MemoryViewType}
			newMv.Obj = mv.Obj
		}
		// Convert element indices to byte offset via stride[0].
		stride0 := mv.Stride(0)
		if stride0 == 0 {
			stride0 = mv.ItemSize
		}
		if stride0 == 0 {
			stride0 = 1
		}
		byteStart := start * stride0
		byteStop := stop * stride0
		if byteStart < 0 {
			byteStart = 0
		}
		if byteStop < byteStart {
			byteStop = byteStart
		}
		if byteStart >= len(mv.Buf) {
			newMv.Buf = mv.Buf[len(mv.Buf):len(mv.Buf):len(mv.Buf)]
		} else {
			if byteStop > len(mv.Buf) {
				byteStop = len(mv.Buf)
			}
			newMv.Buf = mv.Buf[byteStart:byteStop:byteStop]
		}
		newMv.ReadOnly = mv.ReadOnly
		newMv.ItemSize = mv.ItemSize
		if newMv.ItemSize == 0 {
			newMv.ItemSize = 1
		}
		if newMv.Format != mv.Format {
			newMv.Format = mv.Format
		}
		if newMv.Format == "" {
			newMv.Format = "B"
		}
		newMv.NDim = mv.NDim
		if newMv.NDim == 0 {
			newMv.NDim = 1
		}
		if newMv.NDim == 1 {
			// 1-D slices keep their extent and stride inline.
			newMv.Dim0 = sliceLen
			newMv.Stride0 = stride0
			if newMv.Shape != nil {
				newMv.Shape = nil
			}
			if newMv.Strides != nil {
				newMv.Strides = nil
			}
		} else {
			newShape := append([]int(nil), mv.Shape...)
			if len(newShape) == 0 {
				newShape = []int{sliceLen}
			} else {
				newShape[0] = sliceLen
			}
			newStrides := append([]int(nil), mv.Strides...)
			if len(newStrides) == 0 {
				newStrides = val.CContiguousStrides(newShape, newMv.ItemSize)
			}
			newMv.SetShape(newShape, newStrides)
		}
		stack[sp-3] = val.ObjectVal(&newMv.Header)
		sp -= 2
		ip++
		return ip, sp, ctlReload
	}

	if container.Kind() == val.KindBytes {
		data := container.AsBytes().Data
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(data))
		if err == nil {
			if sliceLen == 0 {
				stack[sp-3] = val.NewBytesVal(nil)
			} else {
				stack[sp-3] = val.NewBytesVal(data[start:stop])
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	if container.Kind() == val.KindByteArray {
		data := container.AsByteArray().Data
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(data))
		if err == nil {
			if sliceLen == 0 {
				stack[sp-3] = val.NewByteArrayVal(nil)
			} else {
				sub := make([]byte, sliceLen)
				copy(sub, data[start:stop])
				stack[sp-3] = val.NewByteArrayVal(sub)
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	if container.Kind() == val.KindList && container.Type() == val.ListType {
		items := container.AsList().Items
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(items))
		if err == nil {
			if sliceLen == 0 {
				stack[sp-3] = val.NewListVal()
			} else {
				sub := make([]val.Value, sliceLen)
				copy(sub, items[start:stop])
				stack[sp-3] = val.NewListVal(sub...)
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	if container.Kind() == val.KindTuple {
		items := container.AsTuple().Items
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(items))
		if err == nil {
			switch {
			case sliceLen == 0:
				stack[sp-3] = val.EmptyTupleVal()
			case start == 0 && stop == len(items) && container.Type() == val.TupleType:
				stack[sp-3] = container
			case sliceLen > 2 && (len(items) <= 32 || sliceLen*2 >= len(items)):
				val.ClearEphemeral(container)
				stack[sp-3] = val.NewTupleView(items[start:stop:stop])
			default:
				stack[sp-3] = val.NewTupleFromSlice(items[start:stop])
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	if val.IsInlineStrASCII(container) {
		n := container.StringByteLen()
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), n)
		if err == nil {
			if sliceLen == 0 {
				stack[sp-3] = val.SmallStringVal("")
			} else {
				stack[sp-3] = val.SliceInlineStrASCII(container, start, stop)
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	if container.Kind() == val.KindString || container.Kind() == val.KindInlineStr {
		s := container.AsString()
		start, stop, _, sliceLen, err := val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(s))
		if err == nil {
			if sliceLen == 0 {
				stack[sp-3] = val.NewStringVal("")
			} else {
				stack[sp-3] = val.NewStringVal(s[start:stop])
			}
			sp -= 2
			ip++
			return ip, sp, ctlReload
		}
	}

	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	sl := val.NewSliceVal(startVal, stopVal, val.NoneVal())
	res := vm.getItemSafe(container, sl)
	stack = st.syncStackFromVM(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-3] = res
	sp -= 2
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opUnpackSequence(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	sp--
	seq := stack[sp]
	var items []val.Value
	switch {
	case seq.IsTuple():
		items = seq.AsTuple().Items
	case seq.IsList():
		items = seq.AsList().Items
	case seq.IsAnyString():
		r := []rune(seq.AsString())
		items = make([]val.Value, len(r))
		for i := range r {
			items[i] = val.NewStringVal(string(r[i]))
		}
	default:
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = sp
		items = vm.extractIterableSafe(seq)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
	}
	if len(items) != count {
		exc := val.NewValueError(fmt.Sprintf("not enough or too many values to unpack (expected %d, got %d)", count, len(items)))
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	if sp+count+16 > len(stack) {
		stack = vm.growStack(st, stack, sp+count+64)
	}
	for i := count - 1; i >= 0; i-- {
		stack[sp] = items[i]
		sp++
	}
	if seq.IsTuple() {
		vm.releaseEphemeralTupleVal(seq)
	}
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opFormatValue(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	conversion := uint8(inst >> 16)
	hasSpec := uint16(inst) != 0
	var specStr string
	if hasSpec {
		specVal := stack[sp-1]
		sp--
		specStr = specVal.AsString()
	}
	valToFormat := stack[sp-1]
	if conversion == 0 && (valToFormat.IsInt64() || valToFormat.IsAnyString() || valToFormat.IsFloat64()) {
		stack[sp-1] = val.FormatValue(valToFormat, specStr, 0)
		ip++
		return ip, sp, ctlReload
	}
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	fres := vm.formatValueSafe(valToFormat, specStr, conversion)
	stack = st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp-1] = fres
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opBuildString(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	sp -= count
	if count == 0 {
		stack[sp] = val.NewStringVal("")
		sp++
		ip++
		return ip, sp, ctlNext
	}
	vm.strScratch = vm.strScratch[:0]
	for i := range count {
		var ok bool
		vm.strScratch, ok = val.AppendValueBytes(vm.strScratch, stack[sp+i])
		if !ok {
			s := stack[sp+i].AsString()
			view := unsafe.Slice(unsafe.StringData(s), len(s))
			vm.strScratch = append(vm.strScratch, view...)
		}
	}
	stack[sp] = val.NewStringVal(string(vm.strScratch))
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildTemplate(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	n := int(uint16(inst))
	count := 5*n + 1
	if sp < count {
		panic("vm: stack underflow in OP_BUILD_TEMPLATE")
	}
	sp -= count
	base := sp
	strs := make([]val.Value, n+1)
	interps := make([]val.Value, n)
	strs[0] = stack[base]
	for i := range n {
		off := base + 1 + i*5
		interps[i] = val.MakeInterpolation(stack[off], stack[off+1], stack[off+2], stack[off+3])
		strs[i+1] = stack[off+4]
	}
	stack[sp] = val.MakeTemplate(strs, interps)
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opUnpackEx(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	beforeCount := int(uint8(inst >> 16))
	afterCount := int(uint16(inst))
	sp--
	seq := stack[sp]
	var items []val.Value
	switch {
	case seq.IsTuple():
		items = seq.AsTuple().Items
	case seq.IsList():
		items = seq.AsList().Items
	default:
		st.stack = stack
		st.ip = ip
		vm.execCtx.SP = sp
		items = vm.extractIterableSafe(seq)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			exc := vm.execCtx.ActiveException
			vm.execCtx.ClearException()
			st.raiseExc = exc
			return ip, sp, ctlRaise
		}
	}
	minNeeded := beforeCount + afterCount
	if len(items) < minNeeded {
		exc := val.NewValueError(fmt.Sprintf("not enough values to unpack (expected at least %d, got %d)", minNeeded, len(items)))
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	midLen := len(items) - beforeCount - afterCount
	midItems := make([]val.Value, midLen)
	copy(midItems, items[beforeCount:beforeCount+midLen])
	midList := val.NewListVal(midItems...)

	if sp+beforeCount+afterCount+16 > len(stack) {
		stack = vm.growStack(st, stack, sp+beforeCount+afterCount+64)
	}
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
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opBuildSet(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	sp -= count
	for i := range count {
		val.ClearEphemeral(stack[sp+i])
		val.DetachStringBuf(stack[sp+i])
	}
	setVal := val.NewSetVal(stack[sp : sp+count]...)
	stack[sp] = setVal
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opSetAdd(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	offset := int(uint16(inst))
	item := stack[sp-1]
	sp--
	val.ClearEphemeral(item)
	val.DetachStringBuf(item)
	markViewEscaped(item)
	setVal := stack[sp-offset]
	setVal.AsSet().Add(item)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opDeleteSubscr(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	key := stack[sp-1]
	obj := stack[sp-2]
	sp -= 2
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	vm.delSubscrSafe(obj, key)
	st.syncStackFromExecCtx(vm, stack)
	if vm.execCtx.HasException() {
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	ip++
	return ip, sp, ctlReload
}

func isEphemeralMapSite(st *loopState, ip int) bool {
	limit := min(st.codeLen, ip+16)
	depth := 1
	for k := ip + 1; k < limit; k++ {
		wk := *(*uint32)(unsafe.Add(st.codePtr, uintptr(k)*4))
		opk := DecodeOpcode(wk)
		if depth == 1 && opk == OP_CALL_FUNCTION_EX && DecodeArgA(wk) == 1 {
			return true
		}
		if opk == OP_BUILD_MAP_UNPACK && depth <= int(DecodeArgBx(wk)) {
			return k+1 < st.codeLen &&
				DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(k+1)*4))) == OP_CALL_FUNCTION_EX &&
				DecodeArgA(*(*uint32)(unsafe.Add(st.codePtr, uintptr(k+1)*4))) == 1
		}
		switch opk {
		case OP_LOAD_FAST, OP_LOAD_CONST, OP_LOAD_GLOBAL, OP_LOAD_DEREF:
			depth++
		case OP_BUILD_MAP:
			depth = depth - int(DecodeArgBx(wk))*2 + 1
			if depth <= 1 {
				return false
			}
		default:
			return false
		}
	}
	return false
}

//go:noinline
func (vm *VM) opBuildMapUnpack(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	sp -= count
	isCallEx := ip+1 < st.codeLen &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_CALL_FUNCTION_EX &&
		DecodeArgA(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == 1
	if isCallEx {
		allEmpty := true
		for i := range count {
			d := stack[sp+i]
			if !d.IsDict() || d.AsDict().Live != nil || d.AsDict().Len() != 0 {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			for i := range count {
				vm.releaseSmallDictVal(stack[sp+i])
			}
			stack[sp] = val.NoneVal()
			return ip + 1, sp + 1, ctlNext
		}
	}
	resVal := vm.acquireSmallDict(4, isCallEx)
	resDict := resVal.AsDict()
	st.stack = stack
	st.ip = ip
	vm.execCtx.SP = sp
	for i := range count {
		d := stack[sp+i]
		vm.unpackMapIntoSafe(resDict, d)
		vm.releaseSmallDictVal(d)
		stack = st.syncStackFromVM(vm, stack)
		if vm.execCtx.HasException() {
			break
		}
	}
	if vm.execCtx.HasException() {
		vm.releaseSmallDictVal(resVal)
		exc := vm.execCtx.ActiveException
		vm.execCtx.ClearException()
		st.raiseExc = exc
		return ip, sp, ctlRaise
	}
	stack[sp] = resVal
	sp++
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opListExtend(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	offset := int(uint16(inst))
	iterVal := stack[sp-1]
	sp--
	listVal := stack[sp-offset]
	if listVal.IsList() {
		lst := listVal.AsList()
		var added int
		switch {
		case iterVal.IsList():
			added = len(iterVal.AsList().Items)
			lst.Items = append(lst.Items, iterVal.AsList().Items...)
		case iterVal.IsTuple():
			added = len(iterVal.AsTuple().Items)
			lst.Items = append(lst.Items, iterVal.AsTuple().Items...)
		case iterVal.IsSet():
			added = len(iterVal.AsSet().Items())
			lst.Items = append(lst.Items, iterVal.AsSet().Items()...)
		default:
			st.stack = stack
			st.ip = ip
			vm.execCtx.SP = sp
			items := vm.extractIterableSafe(iterVal)
			st.syncStackFromVM(vm, stack)
			if vm.execCtx.HasException() {
				exc := vm.execCtx.ActiveException
				vm.execCtx.ClearException()
				st.raiseExc = exc
				return ip, sp, ctlRaise
			}
			added = len(items)
			lst.Items = append(lst.Items, items...)
		}
		for _, it := range lst.Items[len(lst.Items)-added:] {
			val.DetachStringBuf(it)
		}
	}
	ip++
	return ip, sp, ctlReload
}

//go:noinline
func (vm *VM) opListToTuple(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	listVal := stack[sp-1]
	if listVal.IsList() {
		stack[sp-1] = val.NewTupleFromSlice(listVal.AsList().Items)
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opListToSet(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	listVal := stack[sp-1]
	if listVal.IsList() {
		stack[sp-1] = val.NewSetVal(listVal.AsList().Items...)
	}
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildList(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	items := make([]val.Value, count)
	copy(items, stack[sp-count:sp])
	for i := range items {
		val.ClearEphemeral(items[i])
		val.DetachStringBuf(items[i])
		markViewEscaped(items[i])
	}
	sp -= count
	stack[sp] = val.NewListVal(items...)
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opListAppend(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	depth := int(uint16(inst))
	listVal := stack[sp-depth-1]
	item := stack[sp-1]
	sp--
	val.ClearEphemeral(item)
	val.DetachStringBuf(item)
	markViewEscaped(item)
	listVal.AsList().Append(item)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildMap(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	var dictVal val.Value
	if count <= 5 && isEphemeralMapSite(st, ip) {
		dictVal = vm.acquireSmallDict(count, true)
	} else {
		dictVal = val.NewDictVal(count)
	}
	d := dictVal.AsDict()
	for i := sp - count*2; i < sp; i += 2 {
		val.ClearEphemeral(stack[i])
		val.ClearEphemeral(stack[i+1])
		val.DetachStringBuf(stack[i])
		val.DetachStringBuf(stack[i+1])
		d.Set(stack[i], stack[i+1])
	}
	sp -= count * 2
	stack[sp] = dictVal
	sp++
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildTuple(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	if count == 0 {
		stack[sp] = val.EmptyTupleVal()
		return ip + 1, sp + 1, ctlNext
	}
	base := sp - count
	for i := base; i < sp; i++ {
		val.ClearEphemeral(stack[i])
		val.DetachStringBuf(stack[i])
		markViewEscaped(stack[i])
	}
	if count <= 2 && ip+1 < st.codeLen && !val.MultiThreadedFlag.Load() {
		opNext := DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4)))
		if opNext == OP_CALL_FUNCTION_EX || opNext == OP_UNPACK_SEQUENCE {
			stack[base] = vm.acquireEphemeralTuple(stack[base:sp])
			return ip + 1, base + 1, ctlNext
		}
	}
	stack[base] = val.NewTupleFromSlice(stack[base:sp])
	return ip + 1, base + 1, ctlNext
}
