package val

import (
	"math"
	"testing"
	"unsafe"
)

func TestValueSizeAndHeaderOffsets(t *testing.T) {
	if got := unsafe.Sizeof(Value{}); got != 16 {
		t.Fatalf("unsafe.Sizeof(Value{}) = %d, want 16", got)
	}
	if off := unsafe.Offsetof(PyBigInt{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PyBigInt{}.Header) = %d, want 0", off)
	}
	if off := unsafe.Offsetof(PyString{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PyString{}.Header) = %d, want 0", off)
	}
	if off := unsafe.Offsetof(PyList{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PyList{}.Header) = %d, want 0", off)
	}
	if off := unsafe.Offsetof(PyDict{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PyDict{}.Header) = %d, want 0", off)
	}
	if off := unsafe.Offsetof(PyInstance{}.Header); off != 0 {
		t.Fatalf("unsafe.Offsetof(PyInstance{}.Header) = %d, want 0", off)
	}
}

func TestPrimitivesAndSentinels(t *testing.T) {
	n := NoneVal()
	if !n.IsNone() || n.Kind() != KindNone || n.IsTruthy() {
		t.Fatalf("unexpected NoneVal state: kind=%v truthy=%v", n.Kind(), n.IsTruthy())
	}

	bTrue := BoolVal(true)
	bFalse := BoolVal(false)
	if !bTrue.IsBool() || bTrue.Kind() != KindBool || !bTrue.AsBool() || !bTrue.IsTruthy() {
		t.Fatalf("unexpected BoolVal(true) state")
	}
	if !bFalse.IsBool() || bFalse.Kind() != KindBool || bFalse.AsBool() || bFalse.IsTruthy() {
		t.Fatalf("unexpected BoolVal(false) state")
	}

	i := Int64Val(-42)
	if !i.IsInt64() || i.Kind() != KindInt64 || i.AsInt64() != -42 || !i.IsTruthy() {
		t.Fatalf("unexpected Int64Val(-42) state")
	}
	if Int64Val(0).IsTruthy() {
		t.Fatalf("Int64Val(0) should not be truthy")
	}

	f := Float64Val(3.14159)
	if !f.IsFloat64() || f.Kind() != KindFloat64 || f.AsFloat64() != 3.14159 || !f.IsTruthy() {
		t.Fatalf("unexpected Float64Val(3.14159) state")
	}
	if Float64Val(0.0).IsTruthy() {
		t.Fatalf("Float64Val(0.0) should not be truthy")
	}
}

func TestInt64BoundaryOverflowPromotion(t *testing.T) {
	maxInt := Int64Val(math.MaxInt64)
	one := Int64Val(1)

	// math.MaxInt64 + 1 must promote cleanly to KindBigInt and equal 9223372036854775808
	res := Add(maxInt, one)
	if !res.IsBigInt() || res.Kind() != KindBigInt {
		t.Fatalf("Add(MaxInt64, 1) Kind = %v, want KindBigInt", res.Kind())
	}
	gotStr := res.AsBigInt().Val.String()
	wantStr := "9223372036854775808"
	if gotStr != wantStr {
		t.Fatalf("Add(MaxInt64, 1) = %s, want %s", gotStr, wantStr)
	}

	// math.MinInt64 - 1 must promote cleanly to KindBigInt and equal -9223372036854775809
	minInt := Int64Val(math.MinInt64)
	under := Sub(minInt, one)
	if !under.IsBigInt() || under.Kind() != KindBigInt {
		t.Fatalf("Sub(MinInt64, 1) Kind = %v, want KindBigInt", under.Kind())
	}
	if got := under.AsBigInt().Val.String(); got != "-9223372036854775809" {
		t.Fatalf("Sub(MinInt64, 1) = %s, want -9223372036854775809", got)
	}

	// math.MaxInt64 * 2 must promote cleanly to KindBigInt and equal 18446744073709551614
	mulRes := Mul(maxInt, Int64Val(2))
	if !mulRes.IsBigInt() || mulRes.Kind() != KindBigInt {
		t.Fatalf("Mul(MaxInt64, 2) Kind = %v, want KindBigInt", mulRes.Kind())
	}
	if got := mulRes.AsBigInt().Val.String(); got != "18446744073709551614" {
		t.Fatalf("Mul(MaxInt64, 2) = %s, want 18446744073709551614", got)
	}

	// Non-overflowing arithmetic stays KindInt64 with 0 allocations
	allocs := testing.AllocsPerRun(100, func() {
		a := Int64Val(1_000_000)
		b := Int64Val(2_000_000)
		c := Add(a, b)
		d := Sub(c, a)
		e := Mul(d, Int64Val(3))
		if e.AsInt64() != 6_000_000 {
			t.Fatal("unexpected arithmetic result")
		}
	})
	if allocs != 0 {
		t.Fatalf("non-overflowing int64 arithmetic allocated %v times, want 0", allocs)
	}
}

func TestMixedIntFloatCoercion(t *testing.T) {
	i := Int64Val(10)
	f := Float64Val(2.5)

	sum := Add(i, f)
	if !sum.IsFloat64() || sum.AsFloat64() != 12.5 {
		t.Fatalf("Add(10, 2.5) = %v, want 12.5", sum.AsFloat64())
	}

	diff := Sub(f, i)
	if !diff.IsFloat64() || diff.AsFloat64() != -7.5 {
		t.Fatalf("Sub(2.5, 10) = %v, want -7.5", diff.AsFloat64())
	}

	prod := Mul(i, f)
	if !prod.IsFloat64() || prod.AsFloat64() != 25.0 {
		t.Fatalf("Mul(10, 2.5) = %v, want 25.0", prod.AsFloat64())
	}

	lt := LessThan(f, i)
	if !lt.IsBool() || !lt.AsBool() {
		t.Fatalf("LessThan(2.5, 10) = false, want true")
	}
}

func TestSSOBoundary(t *testing.T) {
	// 7-byte string "__len__" must use KindInlineStr and 0 allocations
	var s7 Value
	allocs7 := testing.AllocsPerRun(100, func() {
		s7 = NewStringVal("__len__")
	})
	if allocs7 != 0 {
		t.Fatalf("NewStringVal(\"__len__\") allocs = %v, want 0", allocs7)
	}
	if !s7.IsInlineStr() || s7.Kind() != KindInlineStr {
		t.Fatalf("NewStringVal(\"__len__\") Kind = %v, want KindInlineStr", s7.Kind())
	}
	if s7.StringLen() != 7 || s7.AsString() != "__len__" {
		t.Fatalf("s7 = %q (len %d), want \"__len__\" (len 7)", s7.AsString(), s7.StringLen())
	}
	if InlineStrHash(s7) != HashString("__len__") {
		t.Fatalf("InlineStrHash mismatch: got %d, want %d", InlineStrHash(s7), HashString("__len__"))
	}

	// 8-byte string "__init__" must allocate on the heap as KindString
	var s8 Value
	allocs8 := testing.AllocsPerRun(100, func() {
		s8 = NewStringVal("__init__")
	})
	if allocs8 == 0 {
		t.Fatalf("NewStringVal(\"__init__\") allocs = 0, want > 0 (heap allocated)")
	}
	if !s8.IsString() || s8.Kind() != KindString {
		t.Fatalf("NewStringVal(\"__init__\") Kind = %v, want KindString", s8.Kind())
	}
	if s8.StringLen() != 8 || s8.AsString() != "__init__" {
		t.Fatalf("s8 = %q (len %d), want \"__init__\" (len 8)", s8.AsString(), s8.StringLen())
	}

	// Equality checks across inline and heap strings
	s7Copy := NewStringVal("__len__")
	s7Other := NewStringVal("__str__")
	s8Copy := NewStringVal("__init__")
	s8Other := NewStringVal("__repr__")

	if !StringEquals(s7, s7Copy) || !Equals(s7, s7Copy) {
		t.Fatalf("expected s7 == s7Copy")
	}
	if StringEquals(s7, s7Other) || Equals(s7, s7Other) {
		t.Fatalf("expected s7 != s7Other")
	}
	if !StringEquals(s8, s8Copy) || !Equals(s8, s8Copy) {
		t.Fatalf("expected s8 == s8Copy")
	}
	if StringEquals(s8, s8Other) || Equals(s8, s8Other) {
		t.Fatalf("expected s8 != s8Other")
	}
	if StringEquals(s7, s8) || Equals(s7, s8) {
		t.Fatalf("expected s7 != s8")
	}

	// Verify manually constructed heap PyString of length 7 equals inline 7-byte string
	manualHeap7 := Value{
		num: 0,
		ptr: &(&PyString{Header: ObjectHeader{Kind: KindString, Hash: HashString("__len__")}, Data: "__len__"}).Header,
	}
	if !StringEquals(s7, manualHeap7) || !StringEquals(manualHeap7, s7) {
		t.Fatalf("expected cross-representation StringEquals to succeed for identical 7-byte string")
	}
}

func TestHeapListAndDictDowncast(t *testing.T) {
	listVal := NewListVal(Int64Val(10), NewStringVal("abc"))
	if !listVal.IsList() || listVal.Kind() != KindList {
		t.Fatalf("expected KindList, got %v", listVal.Kind())
	}
	listObj := listVal.AsList()
	if len(listObj.Items) != 2 || listObj.Items[0].AsInt64() != 10 || listObj.Items[1].AsString() != "abc" {
		t.Fatalf("unexpected PyList contents: %+v", listObj.Items)
	}

	dictVal := NewDictVal(4)
	if !dictVal.IsDict() || dictVal.Kind() != KindDict {
		t.Fatalf("expected KindDict, got %v", dictVal.Kind())
	}
	dictObj := dictVal.AsDict()
	key := NewStringVal("key")
	dictObj.Set(key, Int64Val(99))
	got, ok := dictObj.Get(NewStringVal("key"))
	if !ok || !got.IsInt64() || got.AsInt64() != 99 {
		t.Fatalf("PyDict.Get(\"key\") = (%v, %v), want (99, true)", got, ok)
	}
}

var benchSinkVal Value
var benchSinkBool bool

func BenchmarkIntAddFastPath(b *testing.B) {
	x := Int64Val(42)
	y := Int64Val(100)
	b.ReportAllocs()

	var acc Value = x
	for b.Loop() {
		acc = Add(x, y)
	}
	benchSinkVal = acc
}

func BenchmarkSmallStringCompare(b *testing.B) {
	s1 := NewStringVal("__len__")
	s2 := NewStringVal("__len__")
	b.ReportAllocs()

	var res bool
	for b.Loop() {
		res = StringEquals(s1, s2)
	}
	benchSinkBool = res
}

func TestDictNumericEquivalence(t *testing.T) {
	// Acceptance test: d[1] = "a", d[1.0] = "b", d[True] = "c"
	// must result in a dictionary of length 1 with key 1 and value "c".
	dVal := NewDictVal(8)
	d := dVal.AsDict()

	kInt := Int64Val(1)
	kFloat := Float64Val(1.0)
	kBool := BoolVal(true)

	if HashValue(kInt) != HashValue(kFloat) || HashValue(kInt) != HashValue(kBool) {
		t.Fatalf("numeric hash mismatch: hash(1)=%d, hash(1.0)=%d, hash(True)=%d",
			HashValue(kInt), HashValue(kFloat), HashValue(kBool))
	}

	d.Set(kInt, NewStringVal("a"))
	d.Set(kFloat, NewStringVal("b"))
	d.Set(kBool, NewStringVal("c"))

	if d.Len() != 1 {
		t.Fatalf("d.Len() = %d, want 1", d.Len())
	}
	if len(d.Entries) != 1 || !d.Entries[0].Active {
		t.Fatalf("expected 1 active DictEntry, got %+v", d.Entries)
	}
	// First-inserted key (1, KindInt64) must be preserved
	if !d.Entries[0].Key.IsInt64() || d.Entries[0].Key.AsInt64() != 1 {
		t.Fatalf("preserved key = %v (kind %v), want Int64Val(1)", d.Entries[0].Key, d.Entries[0].Key.Kind())
	}

	for _, probe := range []Value{kInt, kFloat, kBool} {
		got, ok := d.Get(probe)
		if !ok || got.AsString() != "c" {
			t.Fatalf("d.Get(%v) = (%q, %v), want (\"c\", true)", probe.Kind(), got.AsString(), ok)
		}
	}

	// Verify 0 == 0.0 == -0.0 == False equivalence as well
	negZero := Float64Val(math.Copysign(0, -1))
	if HashValue(Int64Val(0)) != HashValue(Float64Val(0.0)) ||
		HashValue(Int64Val(0)) != HashValue(negZero) ||
		HashValue(Int64Val(0)) != HashValue(BoolVal(false)) {
		t.Fatalf("zero hash equivalence failed")
	}
}

func TestDictInsertionOrderAndResize(t *testing.T) {
	dVal := NewDictVal(4)
	d := dVal.AsDict()

	// Insert 300 keys to trigger uint8 -> uint16 sparse index table growth
	const N = 300
	for i := 0; i < N; i++ {
		d.Set(Int64Val(int64(i)), Int64Val(int64(i*10)))
	}
	if d.Len() != N {
		t.Fatalf("d.Len() = %d, want %d", d.Len(), N)
	}
	for i := 0; i < N; i++ {
		v, ok := d.Get(Int64Val(int64(i)))
		if !ok || v.AsInt64() != int64(i*10) {
			t.Fatalf("d.Get(%d) = (%v, %v), want (%d, true)", i, v, ok, i*10)
		}
		if d.Entries[i].Key.AsInt64() != int64(i) {
			t.Fatalf("insertion order broken at index %d: got %d", i, d.Entries[i].Key.AsInt64())
		}
	}
}

func TestStringInterningFastPath(t *testing.T) {
	// >= 8 byte strings must be heap-allocated with FlagInterned set and share identical pointers
	id1 := InternString("__attribute_name__")
	id2 := InternString(string([]byte("__attribute_name__")))
	id3 := InternString("__different_name__")

	if !id1.IsString() || !id1.IsInterned() || (id1.HeaderPtr().Flags&FlagInterned) == 0 {
		t.Fatalf("expected id1 to be interned KindString")
	}
	if id1.HeaderPtr() != id2.HeaderPtr() {
		t.Fatalf("interned strings with identical content must share identical *ObjectHeader pointers")
	}
	if !StringEquals(id1, id2) || !Equals(id1, id2) {
		t.Fatalf("expected id1 == id2")
	}
	if StringEquals(id1, id3) || Equals(id1, id3) {
		t.Fatalf("expected id1 != id3")
	}

	// Comparing two interned strings must allocate 0 bytes
	allocs := testing.AllocsPerRun(100, func() {
		if !StringEquals(id1, id2) || StringEquals(id1, id3) {
			t.Fatal("unexpected interned comparison result")
		}
	})
	if allocs != 0 {
		t.Fatalf("interned StringEquals allocated %v times, want 0", allocs)
	}
}

func TestHiddenClassShapesAndInstances(t *testing.T) {
	root := NewRootShape()
	inst1Val := NewInstanceVal(root)
	inst2Val := NewInstanceVal(root)

	inst1 := inst1Val.AsInstance()
	inst2 := inst2Val.AsInstance()

	attrX := InternString("x_coordinate")
	attrY := InternString("y_coordinate")

	inst1.SetAttr(attrX, Int64Val(10))
	inst1.SetAttr(attrY, Int64Val(20))

	inst2.SetAttr(attrX, Int64Val(30))
	inst2.SetAttr(attrY, Int64Val(40))

	// Both instances added attributes in the same order from the same root shape,
	// so they MUST converge to the exact same immutable Shape pointer and ID.
	if inst1.Shape != inst2.Shape || inst1.Shape.ID != inst2.Shape.ID {
		t.Fatalf("instances with identical attribute transition order did not share Shape: %p vs %p", inst1.Shape, inst2.Shape)
	}

	v1x, ok1 := inst1.GetAttr(attrX)
	v2y, ok2 := inst2.GetAttr(attrY)
	if !ok1 || v1x.AsInt64() != 10 || !ok2 || v2y.AsInt64() != 40 {
		t.Fatalf("unexpected slot values: v1x=%v, v2y=%v", v1x, v2y)
	}
}

func BenchmarkDictLookupInt(b *testing.B) {
	dVal := NewDictVal(64)
	d := dVal.AsDict()
	for i := int64(0); i < 32; i++ {
		d.Set(Int64Val(i), Int64Val(i*100))
	}
	probeKey := Int64Val(17)
	b.ReportAllocs()
	b.ResetTimer()

	var v Value
	var ok bool
	for b.Loop() {
		v, ok = d.Get(probeKey)
	}
	if !ok {
		b.Fatal("expected key to be found")
	}
	benchSinkVal = v
	benchSinkBool = ok
}

func BenchmarkDictLookupInternedStr(b *testing.B) {
	dVal := NewDictVal(64)
	d := dVal.AsDict()
	keys := []string{
		"__init__", "__repr__", "__dict__", "__class__",
		"attribute_alpha", "attribute_beta", "attribute_gamma", "attribute_delta",
	}
	for i, k := range keys {
		d.Set(InternString(k), Int64Val(int64(i)))
	}
	probeKey := InternString("attribute_gamma")
	b.ReportAllocs()
	b.ResetTimer()

	var v Value
	var ok bool
	for b.Loop() {
		v, ok = d.Get(probeKey)
	}
	if !ok {
		b.Fatal("expected interned key to be found")
	}
	benchSinkVal = v
	benchSinkBool = ok
}
