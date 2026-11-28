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
	d := dictVal.AsDict()
	if compKind := DecodeArgA(inst); compKind != 0 && d.Len() == 0 {
		if n, ok := listAppendHint(stack, sp, depth-1); ok {
			// OP_FOR_ITER has already consumed the first element before the
			// first OP_MAP_ADD executes, so the total count includes it.
			total := n + 1
			if compKind == 2 {
				total = max(total/2, 1)
				if total > 5 {
					d.Reserve(total)
				}
			} else if total > 5 {
				d.ReserveExact(total)
			}
		}
	}
	d.Set(k, v)
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

// normalizeSimpleSlice handles the common step==1 slice bounds where start and
// stop are each either None or Int64 without entering val.NormalizeSliceIndices.
func normalizeSimpleSlice(startVal, stopVal val.Value, length int) (int, int, int, bool) {
	var start int
	switch {
	case startVal.IsNone():
		start = 0
	case startVal.IsInt64():
		start = int(startVal.AsInt64())
		if start < 0 {
			start += length
			if start < 0 {
				start = 0
			}
		} else if start > length {
			start = length
		}
	default:
		return 0, 0, 0, false
	}
	var stop int
	switch {
	case stopVal.IsNone():
		stop = length
	case stopVal.IsInt64():
		stop = int(stopVal.AsInt64())
		if stop < 0 {
			stop += length
			if stop < 0 {
				stop = 0
			}
		} else if stop > length {
			stop = length
		}
	default:
		return 0, 0, 0, false
	}
	if stop <= start {
		return start, start, 0, true
	}
	return start, stop, stop - start, true
}

//go:noinline
func (vm *VM) opBinarySlice(st *loopState, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	locals := stack[st.base:]
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
		newMv := reusableViewFor(st.codePtr, ip, locals)
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
		return ip, sp, ctlNext
	}

	if container.Kind() == val.KindBytes {
		data := container.AsBytes().Data
		start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(data))
		var err error
		if !ok {
			start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(data))
		}
		if err == nil {
			switch {
			case start == 0 && stop == len(data) && container.Type() == val.BytesType:
				stack[sp-3] = container
			case sliceLen == 0:
				stack[sp-3] = val.NewBytesVal(nil)
			default:
				stack[sp-3] = val.NewBytesVal(data[start:stop])
			}
			sp -= 2
			ip++
			return ip, sp, ctlNext
		}
	}

	if container.Kind() == val.KindByteArray {
		data := container.AsByteArray().Data
		start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(data))
		var err error
		if !ok {
			start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(data))
		}
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
			return ip, sp, ctlNext
		}
	}

	if container.Kind() == val.KindList && container.Type() == val.ListType {
		items := container.AsList().Items
		start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(items))
		var err error
		if !ok {
			start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(items))
		}
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
			return ip, sp, ctlNext
		}
	}

	if container.Kind() == val.KindTuple {
		items := container.AsTuple().Items
		start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(items))
		var err error
		if !ok {
			start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(items))
		}
		if err == nil {
			switch {
			case sliceLen == 0:
				stack[sp-3] = val.EmptyTupleVal()
			case start == 0 && stop == len(items) && container.Type() == val.TupleType:
				stack[sp-3] = container
			case sliceLen <= 8 && container.Type() == val.TupleType && ip+1 < st.codeLen && !val.MultiThreadedFlag.Load() &&
				isCachedEphemeralTupleSite(st, ip):
				stack[sp-3] = vm.acquireEphemeralTuple(items[start:stop])
			case sliceLen >= 4:
				// Tuples are immutable so sharing the backing slice is
				// safe and avoids the Packaging copy path.
				val.ClearEphemeral(container)
				stack[sp-3] = val.NewTupleView(items[start:stop])
			case sliceLen > 2 && (len(items) <= 32 || sliceLen*2 >= len(items)):
				val.ClearEphemeral(container)
				stack[sp-3] = val.NewTupleView(items[start:stop:stop])
			default:
				stack[sp-3] = val.NewTupleFromSlice(items[start:stop])
			}
			sp -= 2
			ip++
			return ip, sp, ctlNext
		}
	}

	if val.IsInlineStrASCII(container) {
		n := container.StringByteLen()
		start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, n)
		var err error
		if !ok {
			start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), n)
		}
		if err == nil {
			switch {
			case start == 0 && stop == n:
				stack[sp-3] = container
			case sliceLen == 0:
				stack[sp-3] = val.SmallStringVal("")
			default:
				stack[sp-3] = val.SliceInlineStrASCII(container, start, stop)
			}
			sp -= 2
			ip++
			return ip, sp, ctlNext
		}
	}

	if container.Kind() == val.KindString || container.Kind() == val.KindInlineStr {
		var ascii bool
		if container.Kind() == val.KindString {
			ascii = container.AsStringObj().IsASCII()
		} else {
			ascii = val.IsInlineStrASCII(container)
		}
		if ascii {
			s := container.AsString()
			start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(s))
			var err error
			if !ok {
				start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(s))
			}
			if err == nil {
				switch {
				case start == 0 && stop == len(s) && (container.Kind() == val.KindInlineStr || container.Type() == val.StrType):
					stack[sp-3] = container
				case sliceLen == 0:
					stack[sp-3] = val.SmallStringVal("")
				case sliceLen <= 7:
					stack[sp-3] = val.SmallStringVal(s[start:stop])
				default:
					stack[sp-3] = val.NewStringVal(s[start:stop])
				}
				sp -= 2
				ip++
				return ip, sp, ctlReload
			}
		} else {
			runes := val.StringValRunes(container)
			start, stop, sliceLen, ok := normalizeSimpleSlice(startVal, stopVal, len(runes))
			var err error
			if !ok {
				start, stop, _, sliceLen, err = val.NormalizeSliceIndices(startVal, stopVal, val.NoneVal(), len(runes))
			}
			if err == nil {
				switch {
				case start == 0 && stop == len(runes) && (container.Kind() == val.KindInlineStr || container.Type() == val.StrType):
					stack[sp-3] = container
				case sliceLen == 0:
					stack[sp-3] = val.SmallStringVal("")
				default:
					stack[sp-3] = val.NewStringVal(string(runes[start:stop]))
				}
				sp -= 2
				ip++
				return ip, sp, ctlReload
			}
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
		r := val.StringValRunes(seq)
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
func (vm *VM) opUnpackStoreFastSlow(st *loopState, op Opcode, inst uint32, ip, sp int) (int, int, loopCtl) {
	count := 2
	step := 3
	if op == OP_UNPACK_3_FAST {
		count = 3
		step = 4
	}
	newIP, newSP, ctl := vm.opUnpackSequence(st, Encode(OP_UNPACK_SEQUENCE, 0, uint16(count)), ip, sp)
	if ctl == ctlRaise || ctl == ctlExit {
		return newIP, newSP, ctl
	}
	stack := st.stack
	locals := stack[st.base:]
	if count == 2 {
		v0 := stack[newSP-1]
		v1 := stack[newSP-2]
		if vm.hasTrackedViews {
			vm.reclaimViewSlot(locals[uint8(inst>>16)], v0)
			vm.reclaimViewSlot(locals[uint8(inst>>8)], v1)
		}
		locals[uint8(inst>>16)] = v0
		locals[uint8(inst>>8)] = v1
	} else {
		v0 := stack[newSP-1]
		v1 := stack[newSP-2]
		v2 := stack[newSP-3]
		if vm.hasTrackedViews {
			vm.reclaimViewSlot(locals[uint8(inst>>16)], v0)
			vm.reclaimViewSlot(locals[uint8(inst>>8)], v1)
			vm.reclaimViewSlot(locals[uint8(inst)], v2)
		}
		locals[uint8(inst>>16)] = v0
		locals[uint8(inst>>8)] = v1
		locals[uint8(inst)] = v2
	}
	return ip + step, newSP - count, ctl
}

//go:noinline
func (vm *VM) opFormatValue(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	conversion := uint8(inst >> 16)
	hasSpec := uint16(inst) != 0
	var specStr string
	var specBuf [7]byte
	var specBytes []byte
	if hasSpec {
		specVal := stack[sp-1]
		sp--
		if sb := specVal.AsBytesScratch(&specBuf); sb != nil {
			specBytes = sb
		} else {
			specStr = specVal.AsString()
		}
	}
	valToFormat := stack[sp-1]
	if conversion == 0 && (valToFormat.IsInt64() || valToFormat.IsAnyString() || valToFormat.IsFloat64()) {
		if specBytes != nil {
			stack[sp-1] = val.FormatValueBytes(valToFormat, specBytes, 0)
		} else {
			stack[sp-1] = val.FormatValue(valToFormat, specStr, 0)
		}
		ip++
		return ip, sp, ctlReload
	}
	if specBytes != nil {
		specStr = string(specBytes)
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
		stack[sp] = val.SmallStringVal("")
		sp++
		ip++
		return ip, sp, ctlNext
	}
	if count == 1 {
		v := stack[sp]
		if v.IsInlineStr() || (v.IsString() && v.AsStringObj().Header.Type == val.StrType) {
			sp++
			ip++
			return ip, sp, ctlNext
		}
	}
	buf := vm.strBuildBuf[:0]
	if count >= 3 {
		totalLen := 0
		for i := range count {
			totalLen += stack[sp+i].StringByteLen()
		}
		if cap(buf) < totalLen {
			buf = make([]byte, 0, totalLen)
		}
	}
	for i := range count {
		var ok bool
		buf, ok = val.AppendValueBytes(buf, stack[sp+i])
		if !ok {
			var sbuf [7]byte
			if sb := stack[sp+i].AsBytesScratch(&sbuf); sb != nil {
				buf = append(buf, sb...)
			} else {
				s := stack[sp+i].AsString()
				view := unsafe.Slice(unsafe.StringData(s), len(s))
				buf = append(buf, view...)
			}
		}
	}
	stack[sp] = val.NewStringFromBytes(buf)
	if cap(buf) <= 65536 {
		vm.strBuildBuf = buf[:0]
	} else {
		vm.strBuildBuf = make([]byte, 0, 1024)
	}
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
		if badType, bad := val.IsUnhashableValue(stack[sp+i]); bad {
			st.raiseExc = val.NewTypeError("unhashable type: '" + badType + "'")
			return ip, sp, ctlRaise
		}
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
	s := setVal.AsSet()
	if compKind := DecodeArgA(inst); compKind != 0 && s.Len() == 0 {
		if n, ok := listAppendHint(stack, sp, offset); ok {
			if compKind == 2 {
				n = max(n/2, 1)
			}
			if n > 0 {
				s.Reserve(n)
			}
		}
	}
	s.Add(item)
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
	limit := min(st.codeLen, ip+32)
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
		case OP_BUILD_CONST_KEY_MAP:
			// Const-key literal consumes count values + keys tuple and
			// produces one dict. Mirror BUILD_MAP accounting so nested
			// 11-16 key literals for the same CALL_FUNCTION_EX stay
			// eligible while dicts merged into an outer literal fail closed.
			depth -= int(DecodeArgBx(wk))
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
		if count == 1 {
			dVal := stack[sp]
			if dVal.IsDict() {
				d := dVal.AsDict()
				if d.Live == nil && !d.Shared && !val.MultiThreadedFlag.Load() {
					if d.Len() == 0 {
						vm.releaseSmallDictVal(dVal)
						stack[sp] = val.NoneVal()
					}
					return ip + 1, sp + 1, ctlNext
				}
			}
		}
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
			lst.Items = appendListGrown(lst.Items, added)
			lst.Items = append(lst.Items, iterVal.AsList().Items...)
		case iterVal.IsTuple():
			added = len(iterVal.AsTuple().Items)
			lst.Items = appendListGrown(lst.Items, added)
			lst.Items = append(lst.Items, iterVal.AsTuple().Items...)
		case iterVal.IsSet():
			added = len(iterVal.AsSet().Items())
			lst.Items = appendListGrown(lst.Items, added)
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
			lst.Items = appendListGrown(lst.Items, added)
			lst.Items = append(lst.Items, items...)
		}
		for _, it := range lst.Items[len(lst.Items)-added:] {
			val.DetachStringBuf(it)
		}
	}
	ip++
	return ip, sp, ctlReload
}

// appendListGrown grows items once so the following append of added elements
// never triggers repeated slice doubling.
func appendListGrown(items []val.Value, added int) []val.Value {
	if added <= 0 {
		return items
	}
	if need := len(items) + added; need > cap(items) {
		grown := make([]val.Value, len(items), need)
		copy(grown, items)
		return grown
	}
	return items
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
	if count == 0 {
		// Leave Items nil: the first append allocates the 8-slot floor,
		// or the hinted capacity directly when the source length is known.
		stack[sp] = val.NewListVal()
		sp++
		ip++
		return ip, sp, ctlNext
	}
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

// listAppendHint scans the stack window between a comprehension target list
// and the appended item for a known-length iterable (post-GET_ITER iterator
// or range) and reports its length for one-shot pre-sizing.
func listAppendHint(stack []val.Value, sp, depth int) (int, bool) {
	lo := max(sp-depth+1, 0)
	hi := sp
	if hi < lo {
		return 0, false
	}
	if hi-lo > 8 {
		lo = hi - 8
	}
	for i := lo; i < hi; i++ {
		if n, ok := val.ListLenHint(stack[i]); ok && n >= 0 && n <= 1<<20 {
			return n, true
		}
	}
	return 0, false
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
	lst := listVal.AsList()
	if len(lst.Items) == 0 {
		if n, ok := listAppendHint(stack, sp, depth); ok && n > cap(lst.Items) {
			lst.EnsureCap(n)
		}
	}
	lst.Append(item)
	ip++
	return ip, sp, ctlNext
}

//go:noinline
func (vm *VM) opBuildMap(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	var dictVal val.Value
	switch {
	case count == 0 && DecodeArgA(inst) == 1:
		dictVal = val.DictVal(val.NewSmallDictDirect(0))
	case count <= 16 && isEphemeralMapSite(st, ip):
		dictVal = vm.acquireSmallDict(count, true)
	case count <= 16 && st.curCO != nil && ip < len(st.curCO.MapTempSafe) && st.curCO.MapTempSafe[ip]:
		dictVal = vm.acquireSmallDict(count, true)
	default:
		dictVal = val.NewDictVal(count)
	}
	d := dictVal.AsDict()
	for i := sp - count*2; i < sp; i += 2 {
		val.ClearEphemeral(stack[i])
		val.ClearEphemeral(stack[i+1])
		val.DetachStringBuf(stack[i])
		val.DetachStringBuf(stack[i+1])
		if badType, bad := val.IsUnhashableValue(stack[i]); bad {
			st.raiseExc = val.NewTypeError("unhashable type: '" + badType + "'")
			return ip, sp - count*2, ctlRaise
		}
		d.Set(stack[i], stack[i+1])
	}
	sp -= count * 2
	stack[sp] = dictVal
	sp++
	ip++
	return ip, sp, ctlNext
}

func constKeyIsTemplateable(k val.Value) bool {
	if k.IsInlineStr() {
		return true
	}
	if k.IsString() && k.AsStringObj().Header.Type == val.StrType && k.IsInterned() {
		return true
	}
	return false
}

func constKeyMapsFor(cache *VMCodeCache, n int) []ConstKeyMapTemplate {
	if cache == nil {
		return nil
	}
	return cache.ensureConstKeyMaps(n)
}

//go:noinline
func (vm *VM) opBuildConstKeyMap(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	if count < 2 || count > 16 {
		return vm.opBuildConstKeyMapGeneric(st, inst, ip, sp)
	}
	if sp < count+1 {
		st.raiseExc = val.NewException(val.SystemErrorType, "bad const key map stack")
		return ip, sp, ctlRaise
	}
	keysVal := stack[sp-1]
	base := sp - 1 - count
	cache := st.cache
	var tmpl *ConstKeyMapTemplate
	if cache != nil && !val.MultiThreadedFlag.Load() {
		maps := constKeyMapsFor(cache, st.codeLen)
		if ip >= 0 && ip < len(maps) {
			tmpl = &maps[ip]
		}
	}
	// O(1) hot path: constant tuple identity guarantees keys are unchanged,
	// templateable, and duplicate-free without re-scanning.
	if tmpl != nil && tmpl.Valid && tmpl.KeysTuple == keysVal && tmpl.Count == count {
		if !tmpl.IsEphemeral {
			if sk := tmpl.SplitKeys; sk != nil {
				if !sk.DisableSplit.Load() {
					for i := base; i < base+count; i++ {
						val.ClearEphemeral(stack[i])
						val.DetachStringBuf(stack[i])
					}
					d := val.NewSplitDict(sk, stack[base:base+count], 0)
					stack[base] = val.DictVal(d)
					return ip + 1, base + 1, ctlNext
				}
				// tmpl lives on a VM-private VMCodeCache (gated by !MultiThreadedFlag),
				// so clearing SplitKeys avoids even the atomic load on future hits.
				tmpl.SplitKeys = nil
			}
		}
		d := vm.acquireConstKeyDictEphemeral(count, tmpl.IsEphemeral)
		if d != nil {
			if vm.fillConstKeyDict(d, tmpl, stack[base:base+count]) {
				for i := base; i < base+count; i++ {
					val.ClearEphemeral(stack[i])
					val.DetachStringBuf(stack[i])
				}
				stack[base] = val.DictVal(d)
				return ip + 1, base + 1, ctlNext
			}
			vm.releaseSmallDictFlag(d, val.FlagEphemeralDict)
		}
	}
	if !keysVal.IsTuple() {
		return vm.opBuildConstKeyMapGeneric(st, inst, ip, sp)
	}
	keys := keysVal.AsTuple().Items
	if len(keys) != count {
		return vm.opBuildConstKeyMapGeneric(st, inst, ip, sp)
	}
	if tmpl != nil {
		if vm.buildConstKeyTemplate(tmpl, keysVal, keys, count, st, ip) {
			if ip >= 0 && ip < len(st.cache.ic) {
				st.cache.ic[ip].Kind = ICKindConstKeyMap
			}
			if !tmpl.IsEphemeral && tmpl.SplitKeys != nil {
				for i := base; i < base+count; i++ {
					val.ClearEphemeral(stack[i])
					val.DetachStringBuf(stack[i])
				}
				d := val.NewSplitDict(tmpl.SplitKeys, stack[base:base+count], 0)
				stack[base] = val.DictVal(d)
				return ip + 1, base + 1, ctlNext
			}
			d := vm.acquireConstKeyDictEphemeral(count, tmpl.IsEphemeral)
			if d != nil && vm.fillConstKeyDict(d, tmpl, stack[base:base+count]) {
				for i := base; i < base+count; i++ {
					val.ClearEphemeral(stack[i])
					val.DetachStringBuf(stack[i])
				}
				stack[base] = val.DictVal(d)
				return ip + 1, base + 1, ctlNext
			}
			if d != nil {
				vm.releaseSmallDictFlag(d, val.FlagEphemeralDict)
			}
		} else {
			tmpl.Valid = false
		}
	}
	return vm.opBuildConstKeyMapGeneric(st, inst, ip, sp)
}

//go:noinline
func (vm *VM) opBuildConstKeyMapGeneric(st *loopState, inst uint32, ip, sp int) (int, int, loopCtl) {
	stack := st.stack
	count := int(uint16(inst))
	keysVal := stack[sp-1]
	var keys []val.Value
	if keysVal.IsTuple() {
		keys = keysVal.AsTuple().Items
	}
	base := sp - 1 - count
	var dictVal val.Value
	switch {
	case count <= 16 && isEphemeralMapSite(st, ip):
		dictVal = vm.acquireSmallDict(count, true)
	case count <= 16 && st.curCO != nil && ip < len(st.curCO.MapTempSafe) && st.curCO.MapTempSafe[ip]:
		dictVal = vm.acquireSmallDict(count, true)
	default:
		dictVal = val.NewDictVal(count)
	}
	d := dictVal.AsDict()
	if len(keys) == count {
		for i := range count {
			k := keys[i]
			v := stack[base+i]
			val.ClearEphemeral(k)
			val.ClearEphemeral(v)
			val.DetachStringBuf(k)
			val.DetachStringBuf(v)
			d.Set(k, v)
		}
	} else {
		for i := range count {
			v := stack[base+i]
			val.ClearEphemeral(v)
			val.DetachStringBuf(v)
			d.Set(val.NoneVal(), v)
		}
	}
	sp = base
	stack[sp] = dictVal
	sp++
	ip++
	return ip, sp, ctlNext
}

func (vm *VM) acquireConstKeyDictEphemeral(count int, ephemeral bool) *val.PyDict {
	var flags uint8
	if ephemeral {
		flags = val.FlagEphemeralDict
	}
	if !val.MultiThreadedFlag.Load() {
		switch {
		case count <= 5:
			if n := len(vm.dictFreeList); n > 0 {
				d := vm.dictFreeList[n-1]
				vm.dictFreeList[n-1] = nil
				vm.dictFreeList = vm.dictFreeList[:n-1]
				d.Header = val.ObjectHeader{Kind: val.KindDict, Flags: flags, Type: val.DictType}
				d.InitSmallInlineTable()
				return d
			}
		case count <= 10:
			if n := len(vm.dict16FreeList); n > 0 {
				d := vm.dict16FreeList[n-1]
				vm.dict16FreeList[n-1] = nil
				vm.dict16FreeList = vm.dict16FreeList[:n-1]
				d.Header = val.ObjectHeader{Kind: val.KindDict, Flags: flags, Type: val.DictType}
				d.InitDict16Table()
				return d
			}
		case count <= 16:
			if n := len(vm.dict32FreeList); n > 0 {
				d := vm.dict32FreeList[n-1]
				vm.dict32FreeList[n-1] = nil
				vm.dict32FreeList = vm.dict32FreeList[:n-1]
				d.Header = val.ObjectHeader{Kind: val.KindDict, Flags: flags, Type: val.DictType}
				d.InitDict32Table()
				return d
			}
		}
	}
	if count <= 5 {
		return val.NewSmallDictDirect(flags)
	}
	d := val.NewDictVal(count).AsDict()
	if flags != 0 {
		d.Header.Flags |= flags
	}
	return d
}

func (vm *VM) buildConstKeyTemplate(tmpl *ConstKeyMapTemplate, keysVal val.Value, keys []val.Value, count int, st *loopState, ip int) bool {
	if count < 2 || count > 16 {
		return false
	}
	for _, k := range keys {
		if !constKeyIsTemplateable(k) {
			return false
		}
	}
	for i := range count {
		for j := i + 1; j < count; j++ {
			if keys[i] == keys[j] {
				return false
			}
		}
	}
	var hashes [16]uint64
	for i := range count {
		h := uint64(val.HashValue(keys[i]))
		hashes[i] = h
	}
	var idx8 [8]byte
	var idx16 [16]byte
	var idx32 [32]byte
	switch {
	case count <= 5:
		for i := range count {
			slot := hashes[i] & 7
			for idx8[slot] != 0 {
				slot = (slot + 1) & 7
			}
			idx8[slot] = byte(i + 2)
		}
	case count <= 10:
		for i := range count {
			slot := hashes[i] & 15
			for idx16[slot] != 0 {
				slot = (slot + 1) & 15
			}
			idx16[slot] = byte(i + 2)
		}
	default:
		for i := range count {
			slot := hashes[i] & 31
			for idx32[slot] != 0 {
				slot = (slot + 1) & 31
			}
			idx32[slot] = byte(i + 2)
		}
	}
	for i := range count {
		tmpl.Keys[i] = keys[i]
		tmpl.Hashes[i] = hashes[i]
	}
	tmpl.KeysTuple = keysVal
	tmpl.Indices8 = idx8
	tmpl.Indices16 = idx16
	tmpl.Indices32 = idx32
	sk := &val.SplitDictKeys{
		Count: count,
	}
	for i := range count {
		sk.Keys[i] = keys[i]
		sk.Hashes[i] = uint32(hashes[i])
	}
	switch {
	case count <= 5:
		copy(sk.Indices[:8], idx8[:])
		sk.Mask = 7
	case count <= 10:
		copy(sk.Indices[:16], idx16[:])
		sk.Mask = 15
	default:
		copy(sk.Indices[:32], idx32[:])
		sk.Mask = 31
	}
	tmpl.SplitKeys = sk
	ephemeral := isEphemeralMapSite(st, ip)
	if !ephemeral && st.curCO != nil && ip < len(st.curCO.MapTempSafe) && st.curCO.MapTempSafe[ip] {
		ephemeral = true
	}
	tmpl.IsEphemeral = ephemeral
	tmpl.Count = count
	tmpl.Valid = true
	return true
}

func (vm *VM) fillConstKeyDict(d *val.PyDict, tmpl *ConstKeyMapTemplate, values []val.Value) bool {
	count := tmpl.Count
	if count < 2 || count > 16 || len(values) < count || len(d.Entries) != 0 || cap(d.Entries) < count {
		return false
	}
	entries := d.Entries[:count]
	_ = entries[count-1]
	_ = values[count-1]
	hashes := tmpl.Hashes[:count]
	keys := tmpl.Keys[:count]
	_ = hashes[count-1]
	_ = keys[count-1]
	for i := range count {
		entries[i] = val.DictEntry{
			Hash:   uint32(hashes[i]),
			Active: true,
			Key:    keys[i],
			Value:  values[i],
		}
	}
	d.Entries = entries
	switch {
	case count <= 5:
		copy(d.Indices(), tmpl.Indices8[:])
	case count <= 10:
		copy(d.Indices(), tmpl.Indices16[:])
	default:
		copy(d.Indices(), tmpl.Indices32[:])
	}
	d.SetUsedFilled(count, count)
	d.BumpVersion()
	return true
}

func isEphemeralTupleNextInst(nextWord uint32) bool {
	switch DecodeOpcode(nextWord) {
	case OP_CALL_FUNCTION_EX, OP_UNPACK_SEQUENCE, OP_UNPACK_2_FAST, OP_UNPACK_3_FAST, OP_COMPARE_LT:
		return true
	case OP_COMPARE_OP, OP_COMPARE_JUMP_IF_FALSE, OP_COMPARE_JUMP_IF_TRUE,
		OP_CMP_CONST_JUMP_IF_FALSE, OP_CMP_CONST_JUMP_IF_TRUE:
		return DecodeArgA(nextWord) <= CMP_NOT_IN
	}
	return false
}

// isEphemeralTupleLHSCmp reports whether a tuple built at ip is the LHS operand
// of a non-identity comparison whose RHS is built purely from non-escaping
// loads and tuple builds (e.g. (a, b) < (c, d) in @dataclass(order=True)).
func isEphemeralTupleLHSCmp(st *loopState, ip int) bool {
	if st.codePtr == nil || ip+2 >= st.codeLen {
		return false
	}
	code := unsafe.Slice((*uint32)(st.codePtr), st.codeLen)
	depth := 1
	limit := min(st.codeLen, ip+20)
	for k := ip + 1; k < limit; k++ {
		wk := code[k]
		opk := DecodeOpcode(wk)
		if depth == 2 {
			if opk == OP_COMPARE_LT {
				return true
			}
			if opk == OP_COMPARE_OP || opk == OP_COMPARE_JUMP_IF_FALSE || opk == OP_COMPARE_JUMP_IF_TRUE {
				return DecodeArgA(wk) <= CMP_NOT_IN
			}
		}
		switch opk {
		case OP_NOP:
			// Trailing NOP emitted after fused OP_LOAD_FAST_ATTR / OP_LOAD_FAST_2.
		case OP_LOAD_FAST, OP_LOAD_CONST, OP_LOAD_FAST_ATTR:
			depth++
		case OP_LOAD_FAST_2, OP_LOAD_FAST_CONST:
			depth += 2
		case OP_BUILD_TUPLE:
			cnt := int(DecodeArgBx(wk))
			if depth-cnt < 1 {
				return false
			}
			depth = depth - cnt + 1
		default:
			return false
		}
	}
	return false
}

// isCachedEphemeralTupleSite caches whether a tuple produced at ip (by
// OP_BUILD_TUPLE or OP_BINARY_SLICE) is consumed immediately by a non-escaping
// operation, keyed by VMCodeCache.streamGen.
func isCachedEphemeralTupleSite(st *loopState, ip int) bool {
	cache := st.cache
	var state uint8
	if cache != nil && cache.buildTupleGen == cache.streamGen && uint(ip) < uint(len(cache.buildTupleEphemeral)) {
		state = cache.buildTupleEphemeral[ip]
	}
	if state == 0 {
		nextWord := *(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))
		if isEphemeralTupleNextInst(nextWord) || isEphemeralTupleLHSCmp(st, ip) {
			state = 2
		} else {
			state = 1
		}
		if cache != nil && uint(ip) < uint(len(cache.ic)) {
			if cache.buildTupleGen != cache.streamGen || len(cache.buildTupleEphemeral) < len(cache.ic) {
				cache.buildTupleEphemeral = make([]uint8, len(cache.ic))
				cache.buildTupleGen = cache.streamGen
			}
			cache.buildTupleEphemeral[ip] = state
		}
	}
	return state == 2
}

// callerForIterUnpackCount returns the exact unpack arity (1..3) when callerIP
// (the instruction immediately following OP_FOR_ITER or OP_FOR_ITER_GEN) is
// OP_UNPACK_SEQUENCE(1..3) or OP_UNPACK_2_FAST, or 0 otherwise. Results are
// cached per IP on the caller's VMCodeCache keyed by streamGen.
func callerForIterUnpackCount(cache *VMCodeCache, callerIP int) int {
	if cache == nil || callerIP < 0 || callerIP >= len(cache.active) {
		return 0
	}
	var state uint8
	if cache.forIterUnpackGen == cache.streamGen && callerIP < len(cache.forIterUnpack) {
		state = cache.forIterUnpack[callerIP]
	}
	if state == 0 {
		w := cache.active[callerIP]
		switch DecodeOpcode(w) {
		case OP_UNPACK_2_FAST:
			state = 3 // count 2 + 1
		case OP_UNPACK_SEQUENCE:
			if c := int(DecodeArgBx(w)); c >= 1 && c <= 3 {
				state = uint8(c + 1)
			} else {
				state = 1
			}
		default:
			state = 1
		}
		if cache.forIterUnpackGen != cache.streamGen || len(cache.forIterUnpack) < len(cache.active) {
			cache.forIterUnpack = make([]uint8, len(cache.active))
			cache.forIterUnpackGen = cache.streamGen
		}
		cache.forIterUnpack[callerIP] = state
	}
	if state >= 2 {
		return int(state - 1)
	}
	return 0
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
	if count <= 3 && ip+1 < st.codeLen && !val.MultiThreadedFlag.Load() &&
		st.activeGen != nil && st.frameIdx == st.genFrameIdx && st.frameIdx > st.startFrameIdx &&
		DecodeOpcode(*(*uint32)(unsafe.Add(st.codePtr, uintptr(ip+1)*4))) == OP_YIELD_VALUE {
		callerIdx := st.frameIdx - 1
		if callerIdx < len(vm.frameAux) && vm.frameAux[callerIdx].genMode == genModeForIter {
			caller := &vm.frames[callerIdx]
			if (caller.flags&frameFlagReg) == 0 && callerForIterUnpackCount(caller.cache, caller.ip) == count {
				gen := st.activeGen
				t := gen.YieldTuple
				if t == nil {
					t = &val.PyTuple{
						Header: val.ObjectHeader{Kind: val.KindTuple, Type: val.TupleType},
					}
					gen.YieldTuple = t
				} else {
					t.Header.Hash = 0
					t.Header.Flags = 0
				}
				t.SetInline(stack[base:sp])
				stack[base] = val.ObjectVal(&t.Header)
				return ip + 1, base + 1, ctlNext
			}
		}
	}
	if count <= 8 && ip+1 < st.codeLen && !val.MultiThreadedFlag.Load() && isCachedEphemeralTupleSite(st, ip) {
		stack[base] = vm.acquireEphemeralTuple(stack[base:sp])
		return ip + 1, base + 1, ctlNext
	}
	stack[base] = val.NewTupleFromSlice(stack[base:sp])
	return ip + 1, base + 1, ctlNext
}
