package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/servusdei2018/fython/pkg/dynload"
	"github.com/servusdei2018/fython/pkg/val"
)

var (
	libcHandleOnce sync.Once
	libcHandle     uintptr
	libRegistry    = make(map[uintptr]bool)
	libMu          sync.Mutex
)

func dlopenForCTypes(name string) (uintptr, error) {
	if name == "" {
		h, err := libcHandleOrOpen()
		if err != nil {
			return 0, err
		}
		return h, nil
	}
	h, err := dynload.Open(name)
	if err != nil {
		return 0, fmt.Errorf("cannot load library %s: %v", name, err)
	}
	return h, nil
}

func libcHandleOrOpen() (uintptr, error) {
	libcHandleOnce.Do(func() {
		for _, cand := range []string{"libc.so.6", "libc.so", "/lib/libc.so", "libSystem.dylib"} {
			h, err := dynload.Open(cand)
			if err == nil {
				libcHandle = h
				return
			}
		}
		h, err := purego.Dlopen("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			libcHandle = h
		}
	})
	if libcHandle == 0 {
		return 0, errors.New("cannot find libc")
	}
	return libcHandle, nil
}

func registerCTypesLib(h uintptr) {
	libMu.Lock()
	libRegistry[h] = true
	libMu.Unlock()
}

func libcFuncAddr(name string) uintptr {
	h, err := libcHandleOrOpen()
	if err != nil {
		return 0
	}
	addr, err := dynload.Sym(h, name)
	if err == nil {
		return addr
	}
	addr2, err2 := purego.Dlsym(h, name)
	if err2 == nil {
		return addr2
	}
	return 0
}

func dlsymIn(handle uintptr, name string) (uintptr, error) {
	if handle == 0 {
		h, _ := libcHandleOrOpen()
		handle = h
	}
	addr, err := dynload.Sym(handle, name)
	if err == nil {
		return addr, nil
	}
	return purego.Dlsym(handle, name)
}

func simpleBufPtr(buf []byte) unsafe.Pointer {
	if len(buf) == 0 {
		return nil
	}
	return unsafe.Pointer(&buf[0])
}

func addressOfValue(v val.Value) int64 {
	switch {
	case v.IsBytes():
		if len(v.AsBytes().Data) == 0 {
			return 0
		}
		return int64(uintptr(unsafe.Pointer(&v.AsBytes().Data[0])))
	case v.IsByteArray():
		if len(v.AsByteArray().Data) == 0 {
			return 0
		}
		return int64(uintptr(unsafe.Pointer(&v.AsByteArray().Data[0])))
	case v.IsMemoryView():
		if len(v.AsMemoryView().Buf) == 0 {
			return 0
		}
		return int64(uintptr(unsafe.Pointer(&v.AsMemoryView().Buf[0])))
	case v.HeaderPtr() != nil && v.HeaderPtr().Kind == val.KindNative:
		// ctypes objects
		if isSimpleCData(v) {
			sc := asSimpleCData(v)
			return int64(uintptr(simpleBufPtr(sc.Buf)))
		}
		if isCStruct(v) {
			return int64(uintptr(simpleBufPtr(asCStruct(v).Buf)))
		}
		if isCArray(v) {
			return int64(uintptr(simpleBufPtr(asCArray(v).Buf)))
		}
		if isCPointer(v) {
			return int64(asCPointer(v).PointerAddr())
		}
		if isCFuncPtr(v) {
			return int64(asCFuncPtr(v).Addr)
		}
	}
	return 0
}

func makeByRefValue(v val.Value) val.Value {
	addr := addressOfValue(v)
	// byref returns a c_void_p-like object that resolves to address when passed to C.
	// Use a simple c_void_p wrapper holding the address.
	t := getCType("c_void_p")
	if t == nil {
		// Fallback to direct int (should not happen after ctypes imported).
		return val.Int64Val(addr)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(addr))
	obj := &pySimpleCData{Type: t, Buf: buf}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = t
	return val.ObjectVal(&obj.Header)
}

func getCType(name string) *val.PyType {
	// Types are defined in ctypes module (Python). Lookup via global registry:
	// ctypes.c_int etc. are Python-created heap types with MRO [_SimpleCData].
	// Find by scanning known ctypes module? Instead, keep a map filled at install time.
	if t, ok := cTypeByName[name]; ok {
		return t
	}
	return nil
}

var cTypeByName = make(map[string]*val.PyType)

func newSimpleCDataValue(t *val.PyType, buf []byte, refObj val.Value) val.Value {
	if len(buf) == 0 {
		sz := sizeofTypeCode(typeCodeOf(val.TypeVal(t)))
		buf = make([]byte, sz)
	}
	obj := &pySimpleCData{Type: t, Buf: buf, RefObj: refObj}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = t
	return val.ObjectVal(&obj.Header)
}

func newCStructValue(t *val.PyType, layout *structLayout) val.Value {
	sz := 0
	if layout != nil {
		sz = layout.Size
	}
	if sz == 0 {
		sz = sizeofOfValue(val.TypeVal(t))
	}
	obj := &pyCStruct{Type: t, Buf: make([]byte, sz)}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = t
	return val.ObjectVal(&obj.Header)
}

func newCArrayValue(t *val.PyType, count int, itemType val.Value) val.Value {
	itemSize := sizeofOfValue(itemType)
	sz := itemSize * count
	obj := &pyCArray{Type: t, Buf: make([]byte, sz), Count: count, ItemType: itemType}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = t
	return val.ObjectVal(&obj.Header)
}

func newPointerValue(t *val.PyType, target val.Value, addr uintptr) val.Value {
	obj := &pyCPointer{Type: t, Target: target, Addr: addr}
	obj.Header.Kind = val.KindNative
	obj.Header.Type = t
	if target.HeaderPtr() != nil {
		obj.Addr = uintptr(addressOfValue(target))
	}
	return val.ObjectVal(&obj.Header)
}

func (p *pyCPointer) PointerAddr() uintptr {
	if p.Addr != 0 {
		return p.Addr
	}
	if p.Target.HeaderPtr() != nil {
		return uintptr(addressOfValue(p.Target))
	}
	return 0
}

// encode/decode helpers for _SimpleCData.value

func encodeSimpleValue(code string, v val.Value, buf []byte) {
	if len(buf) == 0 {
		return
	}
	switch code {
	case "?", "b", "B", "c":
		switch {
		case v.IsBool():
			if v.AsBool() {
				buf[0] = 1
			} else {
				buf[0] = 0
			}
		case v.IsAnyString() || v.IsBytes():
			// c_char: single byte.
			s := v.AsString()
			if len(s) > 0 {
				buf[0] = s[0]
			}
		default:
			buf[0] = byte(v.AsInt64())
		}
	case "h", "H", "i", "I", "l", "L", "q", "Q":
		// Put low bytes (little-endian, field-sized).
		n := len(buf)
		u := uint64(v.AsInt64())
		for i := range n {
			buf[i] = byte(u >> (8 * i))
		}
	case "f":
		f := float32(v.AsFloat64())
		bits := math.Float32bits(f)
		binary.LittleEndian.PutUint32(buf, bits)
	case "d":
		bits := math.Float64bits(v.AsFloat64())
		binary.LittleEndian.PutUint64(buf, bits)
	case "P", "z", "O", "Z", "u":
		// pointer / char_p / wchar_p: value is address (int) or bytes/str.
		switch {
		case v.IsNone() || v.HeaderPtr() == nil:
			for i := range buf {
				buf[i] = 0
			}
		case v.IsInt64() || v.IsBool():
			binary.LittleEndian.PutUint64(buf, uint64(v.AsInt64()))
		case v.IsBytes():
			// Store address of bytes data? For c_char_p, value property is bytes; from_param must keep ref.
			// Store pointer to a pinned copy here as well (fallback).
			binary.LittleEndian.PutUint64(buf, uint64(uintptr(unsafe.Pointer(&v.AsBytes().Data[0]))))
		case v.IsAnyString():
			s := v.AsString()
			if len(s) > 0 {
				// NUL-terminated copy address is handled via RefObj path; store 0 here (caller sets RefObj).
				for i := range buf {
					buf[i] = 0
				}
			}
		default:
			// Pointer-like (byref, c_void_p instance, etc.)
			addr := addressOfValue(v)
			binary.LittleEndian.PutUint64(buf, uint64(addr))
		}
	default:
		if len(buf) > 0 {
			buf[0] = byte(v.AsInt64())
		}
	}
}

func decodeSimpleValue(code string, buf []byte) val.Value {
	if len(buf) == 0 {
		return val.NoneVal()
	}
	switch code {
	case "?":
		return val.BoolVal(buf[0] != 0)
	case "b":
		return val.Int64Val(int64(int8(buf[0])))
	case "B", "c":
		if code == "c" {
			return val.NewBytesVal([]byte{buf[0]})
		}
		return val.Int64Val(int64(buf[0]))
	case "h":
		return val.Int64Val(int64(int16(binary.LittleEndian.Uint16(buf))))
	case "H":
		return val.Int64Val(int64(binary.LittleEndian.Uint16(buf)))
	case "i", "l":
		// l is 4 or 8 depending on _calcsize("l"); use 4 if buf len 4 else 8.
		if len(buf) == 8 {
			return val.Int64Val(int64(binary.LittleEndian.Uint64(buf)))
		}
		return val.Int64Val(int64(int32(binary.LittleEndian.Uint32(buf))))
	case "I", "L":
		if len(buf) == 8 {
			return val.Int64Val(int64(binary.LittleEndian.Uint64(buf)))
		}
		return val.Int64Val(int64(binary.LittleEndian.Uint32(buf)))
	case "q":
		return val.Int64Val(int64(binary.LittleEndian.Uint64(buf)))
	case "Q":
		return val.Int64Val(int64(binary.LittleEndian.Uint64(buf)))
	case "f":
		return val.Float64Val(float64(math.Float32frombits(binary.LittleEndian.Uint32(buf))))
	case "d":
		return val.Float64Val(math.Float64frombits(binary.LittleEndian.Uint64(buf)))
	case "P", "z", "O":
		addr := binary.LittleEndian.Uint64(buf)
		if addr == 0 {
			if code == "z" {
				return val.NoneVal()
			}
			return val.NoneVal()
		}
		if code == "z" {
			// Read C string.
			ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
			var out []byte
			for i := range 1 << 20 {
				b := *(*byte)(unsafe.Add(ptr, i))
				if b == 0 {
					break
				}
				out = append(out, b)
			}
			return val.NewBytesVal(out)
		}
		return val.Int64Val(int64(addr))
	default:
		return val.Int64Val(int64(buf[0]))
	}
}

func argToCArg(ctx *val.ExecContext, arg val.Value, argType val.Value) uintptr {
	if argType.IsNone() || argType.HeaderPtr() == nil {
		// No declared type: pass through (int -> uintptr, pointer-like -> address).
		if arg.IsInt64() || arg.IsBool() {
			return uintptr(arg.AsInt64())
		}
		if arg.IsFloat64() {
			// Floats need special handling (SyscallN does not support mixed). Caller handles float separately.
			return uintptr(math.Float64bits(arg.AsFloat64()))
		}
		if addr := addressOfValue(arg); addr != 0 {
			return uintptr(addr)
		}
		return 0
	}
	code := typeCodeOf(argType)
	// argType may be _Pointer or CFunctionPtr type, etc. — pointer types have _type_ "P".
	// c_char_p from bytes/str: allocate NUL-terminated copy, keep alive via conversion cache (leaked for call duration).
	if code == "z" {
		var data []byte
		switch {
		case arg.IsBytes():
			data = arg.AsBytes().Data
		case arg.IsByteArray():
			data = arg.AsByteArray().Data
		case arg.IsAnyString():
			data = []byte(arg.AsString())
		case arg.IsNone():
			return 0
		case arg.IsInt64():
			return uintptr(arg.AsInt64())
		default:
			return uintptr(addressOfValue(arg))
		}
		// NUL-terminated copy (kept alive by appending to a global keepalive list to survive call).
		// For MVP, leak a copy (caller keeps reference via RefObj equivalent).
		cbuf := make([]byte, len(data)+1)
		copy(cbuf, data)
		keepCStr(cbuf)
		return uintptr(unsafe.Pointer(&cbuf[0]))
	}
	if code == "P" {
		if arg.IsNone() {
			return 0
		}
		if arg.IsInt64() || arg.IsBool() {
			return uintptr(arg.AsInt64())
		}
		// c_void_p instance: return stored pointer value, NOT address of its buffer.
		if isSimpleCData(arg) {
			sc := asSimpleCData(arg)
			if len(sc.Buf) >= 8 {
				return uintptr(binary.LittleEndian.Uint64(sc.Buf))
			}
		}
		if isCPointer(arg) {
			return asCPointer(arg).PointerAddr()
		}
		if isCFuncPtr(arg) {
			return asCFuncPtr(arg).Callback
		}
		if addr := addressOfValue(arg); addr != 0 {
			return uintptr(addr)
		}
		return 0
	}
	// Simple numeric: decode ctypes instance value if needed, else plain int.
	intVal := func() int64 {
		if isSimpleCData(arg) {
			v := decodeSimpleValue(code, asSimpleCData(arg).Buf)
			if v.IsFloat64() {
				return 0
			}
			return v.AsInt64()
		}
		if arg.IsFloat64() {
			return 0
		}
		return arg.AsInt64()
	}
	switch code {
	case "?", "b", "B", "c":
		return uintptr(uint8(intVal()))
	case "h", "H":
		return uintptr(uint64(intVal()))
	case "i", "I", "l", "L", "q", "Q", "n", "N":
		return uintptr(uint64(intVal()))
	case "f", "d":
		// Float args must go via RegisterFunc (SyscallN mangles floats). Handled by caller via float path.
		return 0
	default:
		if arg.IsInt64() {
			return uintptr(uint64(arg.AsInt64()))
		}
		return uintptr(addressOfValue(arg))
	}
}

var keepMu sync.Mutex
var keepCSlices [][]byte

func keepCStr(b []byte) {
	keepMu.Lock()
	keepCSlices = append(keepCSlices, b)
	keepMu.Unlock()
}

func convertReturn(raw uintptr, restType val.Value) val.Value {
	if restType.IsNone() || restType.HeaderPtr() == nil {
		return val.NoneVal()
	}
	code := typeCodeOf(restType)
	switch code {
	case "?", "b", "B", "c", "h", "H", "i", "I", "l", "L", "q", "Q":
		// Sign-extend per size.
		switch code {
		case "?":
			return val.BoolVal(raw != 0)
		case "b":
			return val.Int64Val(int64(int8(byte(raw))))
		case "B":
			return val.Int64Val(int64(uint8(raw)))
		case "c":
			return val.NewBytesVal([]byte{byte(raw)})
		case "h":
			return val.Int64Val(int64(int16(uint16(raw))))
		case "H":
			return val.Int64Val(int64(uint16(raw)))
		case "i", "l":
			// Distinguish by sizeof: if 32-bit, sign-extend 32.
			// Use buf size proxy: restype sizeof.
			if sizeofOfValue(restType) == 4 {
				return val.Int64Val(int64(int32(uint32(raw))))
			}
			return val.Int64Val(int64(raw))
		default:
			return val.Int64Val(int64(raw))
		}
	case "f":
		return val.Float64Val(float64(math.Float32frombits(uint32(raw))))
	case "d":
		return val.Float64Val(math.Float64frombits(uint64(raw)))
	case "P", "z":
		if raw == 0 {
			return val.NoneVal()
		}
		if code == "z" {
			ptr := *(*unsafe.Pointer)(unsafe.Pointer(&raw))
			var out []byte
			for i := range 1 << 20 {
				b := *(*byte)(unsafe.Add(ptr, i))
				if b == 0 {
					break
				}
				out = append(out, b)
			}
			return val.NewBytesVal(out)
		}
		// c_void_p return: as int (or None if null? CPython returns int or None? Actually c_void_p returns int/None.)
		return val.Int64Val(int64(raw))
	default:
		return val.Int64Val(int64(raw))
	}
}

func callCFuncPtr(ctx *val.ExecContext, fp val.Value, args []val.Value, kwnames []val.Value) val.Value {
	cptr := asCFuncPtr(fp)
	// Python callback path: CFUNCTYPE wrapping a Python callable.
	if cptr.Callable.HeaderPtr() != nil && !cptr.Callable.IsNone() {
		// Convert C args -> Python args (for callback invoked from C, this path is not used;
		// this is for direct Python call? Actually CFUNCTYPE instances are not called from Python;
		// they are passed as function pointers to C. Direct call from Python (rare) converts Python->Python.
		// Fall through to C-call using stored callback address.
		if cptr.Callback != 0 {
			// Call the Go callback via purego trampoline is complex; instead call Python callable directly
			// with converted args (for testing).
			pyArgs := make([]val.Value, len(args))
			copy(pyArgs, args)
			return ctx.Call(cptr.Callable, pyArgs, nil)
		}
	}
	if cptr.Addr == 0 {
		exc := val.NewException(argumentErrorTyp, "NULL function pointer")
		panic(exc)
	}
	// Intercept sentinel addresses for _string_at etc. handled in Python layer via CFUNCTYPE:
	// If addr is -2 etc., Python already defines string_at via CFUNCTYPE; the _cast machinery will fail.
	// Our CFUNCTYPE for those sentinels is created with Addr = -2 (negative). Convert to actual Go helpers via cptr.Name? Instead, handle in Python layer:
	// string_at uses _string_at_addr = -2, then CFUNCTYPE(py_object, c_void_p, c_int)(-2) should create a funcptr that when called does string_at.
	// We handle that by checking Addr as int64 sentinel.
	if int64(cptr.Addr) == -2 {
		// string_at(ptr, size)
		ptr := uintptr(0)
		size := int64(-1)
		if len(args) >= 1 {
			ptr = argToCArg(ctx, args[0], val.NoneVal())
		}
		if len(args) >= 2 {
			size = args[1].AsInt64()
		}
		out := readCStringAt(ptr, size)
		return val.NewBytesVal(out)
	}
	if int64(cptr.Addr) == -3 {
		// cast(obj, typ): args are [void_p_addr, py_obj, typ] (per PYFUNCTYPE(py_object, c_void_p, py_object, py_object)).
		if len(args) < 3 {
			return args[0]
		}
		typVal := args[2]
		addr := argToCArg(ctx, args[0], val.NoneVal())
		if typVal.IsType() {
			pt := typVal.AsType()
			if val.IsSubclass(pt, pointerTyp) {
				// Pointer cast: wrap address.
				return newPointerValue(pt, val.Value{}, addr)
			}
			if val.IsSubclass(pt, simpleCDataTyp) {
				// c_void_p / pointer target: wrap address, do NOT dereference.
				if typeCodeOf(typVal) == "P" {
					buf := make([]byte, 8)
					binary.LittleEndian.PutUint64(buf, uint64(addr))
					return newSimpleCDataValue(pt, buf, val.Value{})
				}
				// Value cast: copy sizeof bytes from addr into new simple.
				sz := sizeofOfValue(typVal)
				buf := make([]byte, sz)
				if addr != 0 && sz > 0 {
					ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
					for i := range sz {
						buf[i] = *(*byte)(unsafe.Add(ptr, i))
					}
				}
				return newSimpleCDataValue(pt, buf, val.Value{})
			}
			if val.IsSubclass(pt, structTyp) || val.IsSubclass(pt, unionTyp) {
				layout, _ := computeStructLayout(pt, false)
				buf := make([]byte, layout.Size)
				if addr != 0 {
					ptr := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
					for i := range layout.Size {
						buf[i] = *(*byte)(unsafe.Add(ptr, i))
					}
				}
				obj := &pyCStruct{Type: pt, Buf: buf}
				obj.Header.Kind = val.KindNative
				obj.Header.Type = pt
				return val.ObjectVal(&obj.Header)
			}
		}
		return args[0]
	}
	if int64(cptr.Addr) == -4 {
		// memoryview_at(ptr, size, readonly) -> memoryview
		// Create a memoryview over Go slice copied from C memory.
		ptr := uintptr(0)
		// args already converted? arg0 is c_void_p addr, arg1 size, arg2 bool.
		if len(args) >= 1 {
			ptr = argToCArg(ctx, args[0], val.NoneVal())
		}
		sz := int64(0)
		if len(args) >= 2 {
			sz = args[1].AsInt64()
		}
		ro := false
		if len(args) >= 3 {
			ro = args[2].IsTruthy()
		}
		data := make([]byte, sz)
		for i := range sz {
			data[i] = *(*byte)(unsafe.Add(*(*unsafe.Pointer)(unsafe.Pointer(&ptr)), int(i)))
		}
		ba := val.NewByteArrayVal(data)
		mv := val.NewMemoryViewVal(ba)
		if ro {
			mv.AsMemoryView().ReadOnly = true
		}
		return mv
	}
	// General C call via SyscallN (integer/pointer args only; floats go via RegisterFunc fallback).
	hasFloat := false
	for _, at := range cptr.ArgTypes {
		code := typeCodeOf(at)
		if code == "f" || code == "d" {
			hasFloat = true
			break
		}
	}
	if restTypeFloat := typeCodeOf(cptr.RestType); restTypeFloat == "f" || restTypeFloat == "d" {
		hasFloat = true
	}
	if hasFloat {
		// Use RegisterFunc with float-aware wrappers (limited signatures).
		return callCFuncFloat(ctx, cptr, args)
	}
	cArgs := make([]uintptr, len(args))
	for i, a := range args {
		var at val.Value
		if i < len(cptr.ArgTypes) {
			at = cptr.ArgTypes[i]
		}
		// If arg is already a CFuncPtr (e.g., qsort comparator), its address is the callback pointer.
		if a.HeaderPtr() != nil && a.HeaderPtr().Kind == val.KindNative {
			if isCFuncPtr(a) {
				cArgs[i] = asCFuncPtr(a).Callback
				if cArgs[i] == 0 {
					cArgs[i] = asCFuncPtr(a).Addr
				}
				continue
			}
		}
		cArgs[i] = argToCArg(ctx, a, at)
	}
	// GIL is held by caller (callFromHook holds it?).
	r1, _, _ := purego.SyscallN(cptr.Addr, cArgs...)
	return convertReturn(r1, cptr.RestType)
}

func readCStringAt(ptr uintptr, size int64) []byte {
	if ptr == 0 {
		return nil
	}
	p := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	if size >= 0 {
		out := make([]byte, size)
		for i := range size {
			out[i] = *(*byte)(unsafe.Add(p, int(i)))
		}
		return out
	}
	var out []byte
	for i := range 1 << 20 {
		b := *(*byte)(unsafe.Add(p, i))
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return out
}

func callCFuncFloat(ctx *val.ExecContext, cptr *pyCFuncPtr, args []val.Value) val.Value {
	// Limited float support: handle up to 8 args via RegisterFunc with a generic
	// all-float/integer mixed signature is not supported by purego's fixed types.
	// Fallback: panic with clear error (tests do not use floats).
	exc := val.NewException(argumentErrorTyp, "ctypes: floating-point arguments not supported in this build")
	panic(exc)
}

// _SimpleCData helpers exported for val? No, VM-only.
