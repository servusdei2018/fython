package val

import (
	"math"
	"math/big"
	"sync"
	"testing"
)

func makePrimSubInstance(tp *PyType, payload Value) Value {
	v := NewInstanceOfType(tp)
	inst := v.AsInstance()
	if len(inst.Slots) == 0 {
		inst.Slots = []Value{payload}
	} else {
		inst.Slots[0] = payload
	}
	return v
}

func TestPrimitiveSubclassDictKeyEquality(t *testing.T) {
	myIntType := MustNewType("MyInt", []*PyType{IntType}, nil)
	if (myIntType.Flags & FlagIsIntSub) == 0 {
		t.Fatalf("expected FlagIsIntSub on MyInt")
	}
	if (myIntType.Flags & FlagDefaultIntEq) == 0 {
		t.Fatalf("expected FlagDefaultIntEq on MyInt")
	}

	myStrType := MustNewType("MyStr", []*PyType{StrType}, nil)
	if (myStrType.Flags & FlagIsStrSub) == 0 {
		t.Fatalf("expected FlagIsStrSub on MyStr")
	}
	if (myStrType.Flags & FlagDefaultStrEq) == 0 {
		t.Fatalf("expected FlagDefaultStrEq on MyStr")
	}

	one := makePrimSubInstance(myIntType, Int64Val(1))
	oneDup := makePrimSubInstance(myIntType, Int64Val(1))
	two := makePrimSubInstance(myIntType, Int64Val(2))
	zero := makePrimSubInstance(myIntType, Int64Val(0))

	// Hash parity with base primitives.
	if HashValue(one) != HashValue(Int64Val(1)) {
		t.Fatalf("MyInt(1) hash mismatch: got %d, want %d", HashValue(one), HashValue(Int64Val(1)))
	}
	if HashValue(one) != HashValue(BoolVal(true)) {
		t.Fatalf("MyInt(1) vs True hash mismatch")
	}

	// Equality parity: int64, bool, float64, NaN, BigInt, complex, non-numeric.
	if !Equals(one, oneDup) || Equals(one, two) {
		t.Fatalf("MyInt vs MyInt equality failed")
	}
	if !Equals(one, Int64Val(1)) || !Equals(Int64Val(1), one) {
		t.Fatalf("MyInt(1) == 1 failed")
	}
	if !Equals(one, BoolVal(true)) || !Equals(BoolVal(true), one) || !Equals(zero, BoolVal(false)) {
		t.Fatalf("MyInt vs bool equality failed")
	}
	if !Equals(one, Float64Val(1.0)) || !Equals(Float64Val(1.0), one) {
		t.Fatalf("MyInt(1) == 1.0 failed")
	}
	if Equals(one, Float64Val(math.NaN())) || Equals(Float64Val(math.NaN()), one) {
		t.Fatalf("MyInt(1) == NaN must be false")
	}

	// BigInt parity: MyInt(10**30) == 10**30.
	bigPow := new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)
	bigValA := NewBigIntVal(new(big.Int).Set(bigPow))
	bigValB := NewBigIntVal(new(big.Int).Set(bigPow))
	myBigInt := makePrimSubInstance(myIntType, bigValA)
	if !Equals(myBigInt, bigValB) || !Equals(bigValB, myBigInt) {
		t.Fatalf("MyInt(10**30) == 10**30 failed")
	}
	if Equals(myBigInt, one) {
		t.Fatalf("MyInt(10**30) == MyInt(1) must be false")
	}

	// Complex parity: MyInt(1) == 1+0j, MyInt(1) != 1+1j.
	cOne := NewComplexVal(1, 0)
	cImag := NewComplexVal(1, 1)
	if !Equals(one, cOne) || !Equals(cOne, one) {
		t.Fatalf("MyInt(1) == 1+0j failed")
	}
	if Equals(one, cImag) || Equals(cImag, one) {
		t.Fatalf("MyInt(1) == 1+1j must be false")
	}

	// Dict lookup parity for int subclass keys.
	d := NewDictVal(4).AsDict()
	d.Set(one, InternString("one"))
	d.Set(two, InternString("two"))
	if v, ok := d.Get(oneDup); !ok || v.AsString() != "one" {
		t.Fatalf("dict lookup by MyInt(1) failed: %v %v", v, ok)
	}
	if v, ok := d.Get(Int64Val(1)); !ok || v.AsString() != "one" {
		t.Fatalf("dict lookup by plain int 1 failed: %v %v", v, ok)
	}
	if v, ok := d.Get(BoolVal(true)); !ok || v.AsString() != "one" {
		t.Fatalf("dict lookup by True failed: %v %v", v, ok)
	}
	if v, ok := d.Get(Float64Val(2.0)); !ok || v.AsString() != "two" {
		t.Fatalf("dict lookup by 2.0 failed: %v %v", v, ok)
	}

	// Float subclass with custom __eq__ on the RHS must be invoked.
	floatSubCalls := 0
	floatSubDict := NewDictVal(2).AsDict()
	floatSubDict.Set(DunderEq, NewBuiltinFuncVal("FloatSub.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		floatSubCalls++
		return BoolVal(true)
	}))
	floatSubType := MustNewType("CustomFloat", []*PyType{FloatType}, floatSubDict)
	customFloat := makePrimSubInstance(floatSubType, Float64Val(99.5))
	if !Equals(one, customFloat) || floatSubCalls != 1 {
		t.Fatalf("expected RHS CustomFloat.__eq__ to be invoked once and return true; calls=%d", floatSubCalls)
	}

	// Str subclass parity (inline and heap strings).
	sShort := makePrimSubInstance(myStrType, NewStringVal("hi"))
	sShortDup := makePrimSubInstance(myStrType, NewStringVal("hi"))
	sLong := makePrimSubInstance(myStrType, NewStringVal("a_longer_heap_string_value"))
	sLongDup := makePrimSubInstance(myStrType, NewStringVal("a_longer_heap_string_value"))

	if HashValue(sLong) != HashValue(NewStringVal("a_longer_heap_string_value")) {
		t.Fatalf("MyStr hash mismatch")
	}
	if !Equals(sShort, sShortDup) || !Equals(sShort, NewStringVal("hi")) || !Equals(NewStringVal("hi"), sShort) {
		t.Fatalf("MyStr inline equality failed")
	}
	if !Equals(sLong, sLongDup) || !Equals(sLong, NewStringVal("a_longer_heap_string_value")) {
		t.Fatalf("MyStr heap string equality failed")
	}
	if Equals(sShort, one) || Equals(one, sShort) || Equals(sShort, NoneVal()) {
		t.Fatalf("MyStr vs non-str must be false")
	}

	sd := NewDictVal(4).AsDict()
	sd.Set(sShort, Int64Val(10))
	sd.Set(sLong, Int64Val(20))
	if v, ok := sd.Get(NewStringVal("hi")); !ok || v.AsInt64() != 10 {
		t.Fatalf("dict lookup MyStr short failed")
	}
	if v, ok := sd.Get(sLongDup); !ok || v.AsInt64() != 20 {
		t.Fatalf("dict lookup MyStr long failed")
	}

	// Monkey-patching MyInt.__eq__ must immediately clear FlagDefaultIntEq and invoke custom __eq__.
	customCalls := 0
	myIntType.SetAttr(DunderEq, NewBuiltinFuncVal("MyInt.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		customCalls++
		return BoolVal(false)
	}))
	if (myIntType.Flags & FlagDefaultIntEq) != 0 {
		t.Fatalf("expected FlagDefaultIntEq to be cleared after monkey-patching __eq__")
	}
	if Equals(one, oneDup) || customCalls != 1 {
		t.Fatalf("expected monkey-patched MyInt.__eq__ to run and return false; calls=%d", customCalls)
	}

	// Deleting MyInt.__eq__ restores FlagDefaultIntEq.
	myIntType.Dict.Delete(DunderEq)
	myIntType.InvalidateVersion()
	if (myIntType.Flags & FlagDefaultIntEq) == 0 {
		t.Fatalf("expected FlagDefaultIntEq to be restored after deleting custom __eq__")
	}
	if !Equals(one, oneDup) {
		t.Fatalf("expected restored default int.__eq__ to return true")
	}

	// Strict subclass overriding __eq__ on RHS must be invoked even when parent has FlagDefaultIntEq.
	subCalls := 0
	subDict := NewDictVal(2).AsDict()
	subDict.Set(DunderEq, NewBuiltinFuncVal("SubMyInt.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		subCalls++
		return BoolVal(false)
	}))
	subMyIntType := MustNewType("SubMyInt", []*PyType{myIntType}, subDict)
	if (subMyIntType.Flags & FlagDefaultIntEq) != 0 {
		t.Fatalf("expected SubMyInt with custom __eq__ not to have FlagDefaultIntEq")
	}
	subOne := makePrimSubInstance(subMyIntType, Int64Val(1))
	if Equals(one, subOne) || subCalls != 1 {
		t.Fatalf("expected SubMyInt.__eq__ to be invoked on RHS; calls=%d", subCalls)
	}

	// Pathological int subclass whose Slots[0] is non-int must fall back safely.
	badPayload := makePrimSubInstance(myIntType, NewStringVal("not-an-int"))
	if Equals(badPayload, Int64Val(1)) {
		t.Fatalf("pathological payload should not equal 1")
	}
}

func TestPrimitiveSubclassAndInvoke2SlowZeroAlloc(t *testing.T) {
	myIntType := MustNewType("AllocMyInt", []*PyType{IntType}, nil)
	myStrType := MustNewType("AllocMyStr", []*PyType{StrType}, nil)

	k1 := makePrimSubInstance(myIntType, Int64Val(1))
	k1Lookup := makePrimSubInstance(myIntType, Int64Val(1))
	k2 := makePrimSubInstance(myIntType, Int64Val(2))
	k2Lookup := makePrimSubInstance(myIntType, Int64Val(2))
	dInt := NewDictVal(4).AsDict()
	dInt.Set(k1, Int64Val(100))
	dInt.Set(k2, Int64Val(200))

	if allocs := testing.AllocsPerRun(200, func() {
		v, ok := dInt.Get(k1Lookup)
		if !ok || v.AsInt64() != 100 {
			t.Fatal("unexpected dict miss")
		}
		v, ok = dInt.Get(k2Lookup)
		if !ok || v.AsInt64() != 200 {
			t.Fatal("unexpected dict miss")
		}
	}); allocs != 0 {
		t.Fatalf("expected 0 allocs/op for int-subclass PyDict.Get, got %v", allocs)
	}

	s1 := makePrimSubInstance(myStrType, NewStringVal("alpha_key_value"))
	s1Lookup := makePrimSubInstance(myStrType, NewStringVal("alpha_key_value"))
	dStr := NewDictVal(4).AsDict()
	dStr.Set(s1, Int64Val(42))
	if allocs := testing.AllocsPerRun(200, func() {
		v, ok := dStr.Get(s1Lookup)
		if !ok || v.AsInt64() != 42 {
			t.Fatal("unexpected str dict miss")
		}
	}); allocs != 0 {
		t.Fatalf("expected 0 allocs/op for str-subclass PyDict.Get, got %v", allocs)
	}

	// Custom builtin __eq__ via Invoke2(nil, ...) -> invoke2Slow must allocate 0 bytes after pool warmup.
	customDict := NewDictVal(2).AsDict()
	customDict.Set(DunderEq, NewBuiltinFuncVal("CustomEq.__eq__", func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return BoolVal(args[0].AsInstance().Slots[0] == args[1].AsInstance().Slots[0])
	}))
	customType := MustNewType("CustomEq", []*PyType{ObjectType}, customDict)
	cA := makePrimSubInstance(customType, Int64Val(7))
	cB := makePrimSubInstance(customType, Int64Val(7))

	// Warm up invokeArgBufPool.
	if !Equals(cA, cB) {
		t.Fatal("expected custom __eq__ true")
	}
	if allocs := testing.AllocsPerRun(200, func() {
		if !Equals(cA, cB) {
			t.Fatal("expected custom __eq__ true")
		}
	}); allocs != 0 {
		t.Fatalf("expected 0 allocs/op for custom builtin __eq__ via invoke2Slow, got %v", allocs)
	}

	// LookupMRO and LookupMROAfter must allocate 0 bytes even on continuous cache misses.
	base := MustNewType("MROAllocBase", []*PyType{ObjectType}, nil)
	attrName := InternString("mro_alloc_attr")
	base.SetAttr(attrName, Int64Val(99))
	sub := MustNewType("MROAllocSub", []*PyType{base}, nil)

	if allocs := testing.AllocsPerRun(200, func() {
		// Force a fresh VersionTag every iteration so LookupMRO always misses and writes to cache.
		sub.bumpVersion()
		v, owner, ok := sub.LookupMRO(attrName)
		if !ok || owner != base || v.AsInt64() != 99 {
			t.Fatalf("unexpected LookupMRO result: %v %v %v", v, owner, ok)
		}
		v, owner, ok = sub.LookupMROAfter(sub, attrName)
		if !ok || owner != base || v.AsInt64() != 99 {
			t.Fatalf("unexpected LookupMROAfter result: %v %v %v", v, owner, ok)
		}
	}); allocs != 0 {
		t.Fatalf("expected 0 allocs/op on LookupMRO/LookupMROAfter misses, got %v", allocs)
	}
}

func TestMROCacheZeroVersionAndConcurrentStress(t *testing.T) {
	// Guard: a type with VersionTag == 0 must never match a zeroed cache slot.
	FlushTypeLookupCache()
	zeroVerAttr := InternString("zero_ver_attr")
	zeroDict := NewDictVal(2).AsDict()
	zeroDict.Set(zeroVerAttr, Int64Val(123))
	zeroType := &PyType{
		Header:     ObjectHeader{Kind: KindType, Type: TypeType},
		Name:       "ZeroVerType",
		Dict:       zeroDict,
		VersionTag: 0,
	}
	zeroType.MRO = []*PyType{zeroType}
	v, owner, ok := zeroType.LookupMRO(zeroVerAttr)
	if !ok || owner != zeroType || v.AsInt64() != 123 {
		t.Fatalf("zero-version LookupMRO failed: %v %v %v", v, owner, ok)
	}
	if _, _, ok := zeroType.LookupMRO(InternString("absent_zero_ver_attr")); ok {
		t.Fatalf("zero-version LookupMRO unexpectedly matched absent attr")
	}

	// Concurrent -race stress test with MultiThreadedFlag == false (raw Go goroutines).
	// Each worker creates and mutates its own classes while all goroutines concurrently
	// hammer the shared global typeLookupCache, superLookupCache, and FlushTypeLookupCache.
	prevMT := MultiThreadedFlag.Swap(false)
	defer MultiThreadedFlag.Store(prevMT)

	attrA := InternString("stress_attr_a")
	attrB := InternString("stress_attr_b")
	base := MustNewType("StressBase", []*PyType{ObjectType}, nil)
	base.SetAttr(attrA, Int64Val(1))
	sharedSub := MustNewType("StressSub", []*PyType{base}, nil)

	var wg sync.WaitGroup
	const workers = 6
	const iters = 300

	// Concurrent readers + LookupMROAfter readers + cache flushes.
	for range workers {
		wg.Go(func() {
			for i := range iters {
				v, _, ok := sharedSub.LookupMRO(attrA)
				if !ok || v.AsInt64() != 1 {
					t.Errorf("sharedSub.LookupMRO(attrA) failed: %v %v", v, ok)
					return
				}
				_, _, _ = sharedSub.LookupMRO(attrB)
				v, _, ok = sharedSub.LookupMROAfter(sharedSub, attrA)
				if !ok || v.AsInt64() != 1 {
					t.Errorf("sharedSub.LookupMROAfter(attrA) failed: %v %v", v, ok)
					return
				}
				if i%50 == 0 {
					FlushTypeLookupCache()
				}
			}
		})
	}

	// Concurrent class creators + per-class mutators sharing the global MRO caches.
	for w := range 4 {
		id := w
		wg.Go(func() {
			for i := range iters {
				cls, err := NewType("EphemeralStress", []*PyType{base}, nil)
				if err != nil {
					t.Errorf("NewType failed: %v", err)
					return
				}
				cls.SetAttr(attrB, Int64Val(int64(id*1000+i)))
				v, _, ok := cls.LookupMRO(attrB)
				if !ok || v.AsInt64() != int64(id*1000+i) {
					t.Errorf("LookupMRO mismatch on fresh class: got %v (%v)", v, ok)
					return
				}
				cls.SetAttr(attrB, Int64Val(int64(id*1000+i+1)))
				v, _, ok = cls.LookupMRO(attrB)
				if !ok || v.AsInt64() != int64(id*1000+i+1) {
					t.Errorf("LookupMRO mismatch after mutation: got %v (%v)", v, ok)
					return
				}
			}
		})
	}

	wg.Wait()
}
