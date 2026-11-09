package val

import (
	"bytes"
	"testing"
	"unsafe"
)

func TestBytesZeroCopySlice(t *testing.T) {
	orig := []byte("Hello, world! This is a test for zero-copy slicing.")
	valBytes := NewBytesVal(orig)

	// Slice bytes[7:12] => "world"
	sl := NewSliceVal(Int64Val(7), Int64Val(12), NoneVal())
	slicedVal := GetItemCtx(nil, valBytes, sl)

	if !slicedVal.IsBytes() {
		t.Fatalf("expected KindBytes, got %v", slicedVal.Kind())
	}
	sliced := slicedVal.AsBytes()
	if string(sliced.Data) != "world" {
		t.Fatalf("expected 'world', got %q", string(sliced.Data))
	}

	// Invariant: underlying slice MUST share the original array backing pointer
	origPtr := unsafe.Pointer(&orig[7])
	slicedPtr := unsafe.Pointer(&sliced.Data[0])
	if origPtr != slicedPtr {
		t.Fatalf("expected zero-copy pointer sharing: origPtr %p != slicedPtr %p", origPtr, slicedPtr)
	}

	// Invariant: 3-index slicing cap must match len to prevent capacity aliasing
	if cap(sliced.Data) != len(sliced.Data) {
		t.Fatalf("expected cap == len (5), got cap = %d, len = %d", cap(sliced.Data), len(sliced.Data))
	}
}

func TestMemoryViewByteArrayMutation(t *testing.T) {
	raw := []byte("abcdefghijklmnop")
	ba := NewByteArrayVal(raw)

	// Wrap in memoryview
	mv := NewMemoryViewVal(ba)
	if !mv.IsMemoryView() {
		t.Fatalf("expected KindMemoryView, got %v", mv.Kind())
	}

	// Slice mv[2:6] => "cdef"
	sl := NewSliceVal(Int64Val(2), Int64Val(6), NoneVal())
	subView := GetItemCtx(nil, mv, sl)
	if !subView.IsMemoryView() {
		t.Fatalf("expected KindMemoryView subview, got %v", subView.Kind())
	}

	// Check underlying pointer shares backing memory
	if unsafe.Pointer(&raw[2]) != unsafe.Pointer(&subView.AsMemoryView().Buf[0]) {
		t.Fatalf("subview does not share backing memory pointer")
	}

	// In-place mutate subView[0] = 'Z' (90)
	SetItemCtx(nil, subView, Int64Val(0), Int64Val(90))

	// Invariant: raw and ba MUST reflect in-place mutation
	if raw[2] != 'Z' {
		t.Fatalf("expected raw[2] to be 'Z', got %c", raw[2])
	}
	if ba.AsByteArray().Data[2] != 'Z' {
		t.Fatalf("expected ba.Data[2] to be 'Z', got %c", ba.AsByteArray().Data[2])
	}

	// Slicing subview again subView[1:3] => "de"
	sl2 := NewSliceVal(Int64Val(1), Int64Val(3), NoneVal())
	subSubView := GetItemCtx(nil, subView, sl2)
	SetItemCtx(nil, subSubView, Int64Val(0), Int64Val(88)) // 'X'

	if raw[3] != 'X' {
		t.Fatalf("expected raw[3] to be 'X', got %c", raw[3])
	}
}

func TestMemoryViewReadOnlyBytes(t *testing.T) {
	orig := []byte("immutable bytes")
	valBytes := NewBytesVal(orig)
	mv := NewMemoryViewVal(valBytes)

	if !mv.AsMemoryView().ReadOnly {
		t.Fatalf("expected memoryview of bytes to be ReadOnly")
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on mutating read-only memoryview")
		}
	}()

	// Mutating read-only memoryview should panic
	SetItemCtx(nil, mv, Int64Val(0), Int64Val(65))
}

func TestTupleZeroCopySlice(t *testing.T) {
	items := make([]Value, 100)
	for i := range 100 {
		items[i] = Int64Val(int64(i * 10))
	}
	tup := NewTupleVal(items...)

	// Slice tup[10:30]
	sl := NewSliceVal(Int64Val(10), Int64Val(30), NoneVal())
	slicedVal := GetItemCtx(nil, tup, sl)

	if !slicedVal.IsTuple() {
		t.Fatalf("expected KindTuple, got %v", slicedVal.Kind())
	}
	slicedTup := slicedVal.AsTuple()
	if len(slicedTup.Items) != 20 {
		t.Fatalf("expected 20 items, got %d", len(slicedTup.Items))
	}

	// Invariant: underlying Items slice MUST share the parent array pointer
	origPtr := unsafe.Pointer(&tup.AsTuple().Items[10])
	slicedPtr := unsafe.Pointer(&slicedTup.Items[0])
	if origPtr != slicedPtr {
		t.Fatalf("expected zero-copy tuple slicing: %p != %p", origPtr, slicedPtr)
	}

	// Invariant: capacity must be clamped to prevent capacity aliasing
	if cap(slicedTup.Items) != len(slicedTup.Items) {
		t.Fatalf("expected cap == len (20), got cap = %d", cap(slicedTup.Items))
	}

	// Verify items match
	for i := range 20 {
		if slicedTup.Items[i].AsInt64() != int64((i+10)*10) {
			t.Fatalf("item %d mismatch: got %d, want %d", i, slicedTup.Items[i].AsInt64(), (i+10)*10)
		}
	}
}

func TestNormalizeSliceIndices(t *testing.T) {
	// [1:5:1] on len 10
	start, stop, step, sliceLen, err := NormalizeSliceIndices(Int64Val(1), Int64Val(5), Int64Val(1), 10)
	if err != nil || start != 1 || stop != 5 || step != 1 || sliceLen != 4 {
		t.Fatalf("case 1 failed: start=%d, stop=%d, step=%d, len=%d, err=%v", start, stop, step, sliceLen, err)
	}

	// [::-1] on len 5 => start=4, stop=-1, step=-1, len=5
	start, stop, step, sliceLen, err = NormalizeSliceIndices(NoneVal(), NoneVal(), Int64Val(-1), 5)
	if err != nil || start != 4 || stop != -1 || step != -1 || sliceLen != 5 {
		t.Fatalf("case 2 failed: start=%d, stop=%d, step=%d, len=%d, err=%v", start, stop, step, sliceLen, err)
	}

	// [-3::1] on len 10 => start=7, stop=10, step=1, len=3
	start, stop, step, sliceLen, err = NormalizeSliceIndices(Int64Val(-3), NoneVal(), NoneVal(), 10)
	if err != nil || start != 7 || stop != 10 || step != 1 || sliceLen != 3 {
		t.Fatalf("case 3 failed: start=%d, stop=%d, step=%d, len=%d, err=%v", start, stop, step, sliceLen, err)
	}
}

func TestSmallStringFromBytesAndInternBytes(t *testing.T) {
	// <= 7 bytes: SmallStringFromBytes
	shortKey := []byte("name")
	v1 := SmallStringFromBytes(shortKey)
	if !v1.IsInlineStr() {
		t.Fatalf("expected InlineStr for <= 7 bytes, got %v", v1.Kind())
	}
	if v1.AsString() != "name" {
		t.Fatalf("expected 'name', got %q", v1.AsString())
	}

	// InternPool.InternBytes
	pool := NewInternPool(64)
	v2 := pool.InternBytes(shortKey)
	if !v2.IsInlineStr() {
		t.Fatalf("expected InlineStr from pool for <= 7 bytes")
	}

	// > 7 bytes: heap interned string
	longKey := []byte("description_field")
	v3 := pool.InternBytes(longKey)
	if !v3.IsString() || (v3.ptr.Flags&FlagInterned) == 0 {
		t.Fatalf("expected FlagInterned on longKey")
	}

	// Deduplication check
	v4 := pool.InternBytes(longKey)
	if v3.ptr != v4.ptr {
		t.Fatalf("expected identical pointer for interned string: %p != %p", v3.ptr, v4.ptr)
	}
}

func TestBytesMethods(t *testing.T) {
	b := NewBytesVal([]byte("hello world"))

	// decode()
	decFn, ok := BytesType.Dict.Get(InternString("decode"))
	if !ok {
		t.Fatalf("decode not found in BytesType.Dict")
	}
	s := decFn.AsBuiltinFunc().Fn(nil, decFn, []Value{b}, nil)
	if s.AsString() != "hello world" {
		t.Fatalf("decode failed: %q", s.AsString())
	}

	// hex()
	hexFn, _ := BytesType.Dict.Get(InternString("hex"))
	h := hexFn.AsBuiltinFunc().Fn(nil, hexFn, []Value{b}, nil)
	if h.AsString() != "68656c6c6f20776f726c64" {
		t.Fatalf("hex failed: %q", h.AsString())
	}

	// startswith / endswith
	swFn, _ := BytesType.Dict.Get(InternString("startswith"))
	res := swFn.AsBuiltinFunc().Fn(nil, swFn, []Value{b, NewBytesVal([]byte("hello"))}, nil)
	if !res.AsBool() {
		t.Fatalf("startswith failed")
	}

	ewFn, _ := BytesType.Dict.Get(InternString("endswith"))
	res = ewFn.AsBuiltinFunc().Fn(nil, ewFn, []Value{b, NewBytesVal([]byte("world"))}, nil)
	if !res.AsBool() {
		t.Fatalf("endswith failed")
	}
}

func TestByteArrayMethods(t *testing.T) {
	ba := NewByteArrayVal([]byte("foo"))

	// append(98) => 'b'
	appFn, _ := ByteArrayType.Dict.Get(InternString("append"))
	appFn.AsBuiltinFunc().Fn(nil, appFn, []Value{ba, Int64Val(98)}, nil)
	if string(ba.AsByteArray().Data) != "foob" {
		t.Fatalf("append failed: %q", string(ba.AsByteArray().Data))
	}

	// extend(b"ar")
	extFn, _ := ByteArrayType.Dict.Get(InternString("extend"))
	extFn.AsBuiltinFunc().Fn(nil, extFn, []Value{ba, NewBytesVal([]byte("ar"))}, nil)
	if string(ba.AsByteArray().Data) != "foobar" {
		t.Fatalf("extend failed: %q", string(ba.AsByteArray().Data))
	}
}

func TestMemoryViewMethods(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5}
	mv := NewMemoryViewVal(NewBytesVal(raw))

	// tobytes()
	tbFn, _ := MemoryViewType.Dict.Get(InternString("tobytes"))
	bVal := tbFn.AsBuiltinFunc().Fn(nil, tbFn, []Value{mv}, nil)
	if !bytes.Equal(bVal.AsBytes().Data, raw) {
		t.Fatalf("tobytes mismatch")
	}

	// tolist()
	tlFn, _ := MemoryViewType.Dict.Get(InternString("tolist"))
	lVal := tlFn.AsBuiltinFunc().Fn(nil, tlFn, []Value{mv}, nil)
	if lVal.AsList().Len() != 5 {
		t.Fatalf("tolist len mismatch")
	}

	// release()
	relFn, _ := MemoryViewType.Dict.Get(InternString("release"))
	relFn.AsBuiltinFunc().Fn(nil, relFn, []Value{mv}, nil)
	if !mv.AsMemoryView().released {
		t.Fatalf("expected released to be true")
	}
}
