package vm

import (
	"runtime"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestVMArithmeticAndLocals(t *testing.T) {
	// Compute:
	// a = 10
	// b = 25
	// c = (a + b) * 2 - 5  => 65
	// return c
	consts := []val.Value{
		val.Int64Val(10),
		val.Int64Val(25),
		val.Int64Val(2),
		val.Int64Val(5),
	}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 0), // push 10
		Encode(OP_STORE_FAST, 0, 0), // local[0] = 10
		Encode(OP_LOAD_CONST, 0, 1), // push 25
		Encode(OP_STORE_FAST, 0, 1), // local[1] = 25
		Encode(OP_LOAD_FAST, 0, 0),  // push local[0]
		Encode(OP_LOAD_FAST, 0, 1),  // push local[1]
		Encode(OP_BINARY_ADD, 0, 0), // 10 + 25 = 35
		Encode(OP_LOAD_CONST, 0, 2), // push 2
		Encode(OP_BINARY_MUL, 0, 0), // 35 * 2 = 70
		Encode(OP_LOAD_CONST, 0, 3), // push 5
		Encode(OP_BINARY_SUB, 0, 0), // 70 - 5 = 65
		Encode(OP_STORE_FAST, 0, 2), // local[2] = 65
		Encode(OP_LOAD_FAST, 0, 2),  // push local[2]
		Encode(OP_RETURN, 0, 0),     // return 65
	}

	v := NewVM()
	res := v.Run(code, consts, 3)
	if !res.IsInt64() || res.AsInt64() != 65 {
		t.Fatalf("VM.Run result = %v (kind %v), want 65", res.AsInt64(), res.Kind())
	}
}

func TestVMQuickeningAndDeopt(t *testing.T) {
	// Function bytecode:
	// return local[0] + local[1]
	addCode := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_LOAD_FAST, 0, 1),
		Encode(OP_BINARY_ADD, 0, 0), // index 2: will quicken and deoptimize
		Encode(OP_RETURN, 0, 0),
	}

	v := NewVM()

	// 1. Initial opcode is OP_BINARY_ADD
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD {
		t.Fatalf("initial opcode = %v, want OP_BINARY_ADD", got)
	}

	// 2. Execute with int64 + int64 -> should quicken in-place to OP_BINARY_ADD_INT
	v.stack[0] = val.Int64Val(100)
	v.stack[1] = val.Int64Val(200)
	res1 := v.Run(addCode, nil, 2)
	if !res1.IsInt64() || res1.AsInt64() != 300 {
		t.Fatalf("int64 + int64 result = %v, want 300", res1.AsInt64())
	}
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD_INT {
		t.Fatalf("opcode after int64 addition = %v, want OP_BINARY_ADD_INT", got)
	}

	// 3. Execute again with int64 + int64 on quickened OP_BINARY_ADD_INT
	v.stack[0] = val.Int64Val(50)
	v.stack[1] = val.Int64Val(75)
	res2 := v.Run(addCode, nil, 2)
	if !res2.IsInt64() || res2.AsInt64() != 125 {
		t.Fatalf("second int64 + int64 result = %v, want 125", res2.AsInt64())
	}
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD_INT {
		t.Fatalf("opcode after second int64 addition = %v, want OP_BINARY_ADD_INT", got)
	}

	// 4. Break type stability with float64 + float64 -> must deoptimize back to OP_BINARY_ADD
	v.stack[0] = val.Float64Val(1.5)
	v.stack[1] = val.Float64Val(2.25)
	res3 := v.Run(addCode, nil, 2)
	if !res3.IsFloat64() || res3.AsFloat64() != 3.75 {
		t.Fatalf("float64 + float64 result = %v, want 3.75", res3.AsFloat64())
	}
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD {
		t.Fatalf("opcode after float64 deopt = %v, want OP_BINARY_ADD", got)
	}

	// 5. Next float64 + float64 execution quickens OP_BINARY_ADD -> OP_BINARY_ADD_FLOAT
	v.stack[0] = val.Float64Val(10.0)
	v.stack[1] = val.Float64Val(20.5)
	res4 := v.Run(addCode, nil, 2)
	if !res4.IsFloat64() || res4.AsFloat64() != 30.5 {
		t.Fatalf("second float64 + float64 result = %v, want 30.5", res4.AsFloat64())
	}
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD_FLOAT {
		t.Fatalf("opcode after float64 quicken = %v, want OP_BINARY_ADD_FLOAT", got)
	}

	// 6. Break type stability with str + str -> must deoptimize back to OP_BINARY_ADD
	v.stack[0] = val.NewStringVal("py")
	v.stack[1] = val.NewStringVal("thon")
	res5 := v.Run(addCode, nil, 2)
	if !res5.IsInlineStr() || res5.AsString() != "python" {
		t.Fatalf("str + str result = %q, want \"python\"", res5.AsString())
	}
	if got := DecodeOpcode(addCode[2]); got != OP_BINARY_ADD {
		t.Fatalf("opcode after str deopt = %v, want OP_BINARY_ADD", got)
	}
}

func TestVMCallFrameSlabZeroAlloc(t *testing.T) {
	// Helper function add(x, y): return x + y
	addFn := &CodeObject{
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}

	// Caller: return add(12, 30)
	callerConsts := []val.Value{
		val.Int64Val(12),
		val.Int64Val(30),
		NewCodeVal(addFn),
	}
	callerCode := []uint32{
		Encode(OP_LOAD_CONST, 0, 0), // push 12
		Encode(OP_LOAD_CONST, 0, 1), // push 30
		Encode(OP_CALL, 2, 2),       // call consts[2] with 2 args
		Encode(OP_RETURN, 0, 0),     // return result
	}

	v := NewVM()
	allocs := testing.AllocsPerRun(100, func() {
		res := v.Run(callerCode, callerConsts, 0)
		if !res.IsInt64() || res.AsInt64() != 42 {
			t.Fatalf("unexpected call result: %v", res)
		}
	})
	if allocs != 0 {
		t.Fatalf("OP_CALL / OP_RETURN allocated %v times, want 0", allocs)
	}
}

func BenchmarkVMLoop100M(b *testing.B) {
	// Bytecode for:
	// x = 0
	// while x < 100_000_000:
	//     x = x + 1
	// return x
	const limit = 100_000_000
	consts := []val.Value{
		val.Int64Val(0),
		val.Int64Val(limit),
		val.Int64Val(1),
	}
	code := []uint32{
		/* 00 */ Encode(OP_LOAD_CONST, 0, 0), // push 0
		/* 01 */ Encode(OP_STORE_FAST, 0, 0), // x = 0
		/* 02 */ Encode(OP_LOAD_FAST, 0, 0), // loop: push x
		/* 03 */ Encode(OP_LOAD_CONST, 0, 1), // push 100_000_000
		/* 04 */ Encode(OP_COMPARE_LT, 0, 0), // x < 100_000_000
		/* 05 */ Encode(OP_JUMP_IF_FALSE, 0, 11), // if false jump to 11
		/* 06 */ Encode(OP_LOAD_FAST, 0, 0), // push x
		/* 07 */ Encode(OP_LOAD_CONST, 0, 2), // push 1
		/* 08 */ Encode(OP_BINARY_ADD, 0, 0), // x + 1
		/* 09 */ Encode(OP_STORE_FAST, 0, 0), // x = x + 1
		/* 10 */ Encode(OP_JUMP, 0, 2), // jump to 2
		/* 11 */ Encode(OP_LOAD_FAST, 0, 0), // push x
		/* 12 */ Encode(OP_RETURN, 0, 0), // return x
	}

	v := NewVM()
	runtime.GC()
	b.ReportAllocs()
	start := time.Now()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		res := v.Run(code, consts, 1)
		if !res.IsInt64() || res.AsInt64() != limit {
			b.Fatalf("res = %v, want %d", res.AsInt64(), limit)
		}
	}

	elapsed := time.Since(start)
	b.ReportMetric(float64(elapsed.Milliseconds())/float64(b.N), "ms/100M_iters")
}

func TestVMInstanceAttrInlineCache(t *testing.T) {
	v := NewVM()
	attrCounter := v.Intern("counter_value")

	objVal := val.NewInstanceVal(val.NewRootShape())
	obj := objVal.AsInstance()
	obj.SetAttr(attrCounter, val.Int64Val(0))

	// Bytecode for:
	// obj.counter_value = obj.counter_value + 1
	// return obj.counter_value
	consts := []val.Value{
		attrCounter,     // 0: "counter_value"
		val.Int64Val(1), // 1: 1
	}
	code := []uint32{
		/* 00 */ Encode(OP_LOAD_FAST, 0, 0), // push obj (for store target)
		/* 01 */ Encode(OP_LOAD_FAST, 0, 0), // push obj (for load source)
		/* 02 */ Encode(OP_LOAD_ATTR, 0, 0), // pop obj, push obj.counter_value
		/* 03 */ Encode(OP_LOAD_CONST, 0, 1), // push 1
		/* 04 */ Encode(OP_BINARY_ADD, 0, 0), // obj.counter_value + 1
		/* 05 */ Encode(OP_STORE_ATTR, 0, 0), // obj.counter_value = result
		/* 06 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 07 */ Encode(OP_LOAD_ATTR, 0, 0), // push obj.counter_value
		/* 08 */ Encode(OP_RETURN, 0, 0), // return obj.counter_value
	}

	v.stack[0] = objVal
	res := v.Run(code, consts, 1)
	if !res.IsInt64() || res.AsInt64() != 1 {
		t.Fatalf("first run result = %v, want 1", res)
	}

	// Verify inline cache slots at ip=2, ip=5, ip=7 are populated with obj.Shape.ID
	if v.ic[2].ExpectedShapeID != obj.Shape.ID || v.ic[5].ExpectedShapeID != obj.Shape.ID || v.ic[7].ExpectedShapeID != obj.Shape.ID {
		t.Fatalf("inline cache not populated with expected shape ID %d: ic[2]=%+v ic[5]=%+v ic[7]=%+v",
			obj.Shape.ID, v.ic[2], v.ic[5], v.ic[7])
	}

	// Subsequent runs must hit the Shape inline cache with 0 allocations
	allocs := testing.AllocsPerRun(100, func() {
		v.stack[0] = objVal
		r := v.Run(code, consts, 1)
		if !r.IsInt64() {
			t.Fatal("expected int64")
		}
	})
	if allocs != 0 {
		t.Fatalf("inline-cached OP_LOAD_ATTR / OP_STORE_ATTR allocated %v times, want 0", allocs)
	}
	if got := obj.Slots[0].AsInt64(); got != 102 {
		t.Fatalf("obj.counter_value after AllocsPerRun(100) = %d, want 102", got)
	}
}

func BenchmarkInstanceAttrAccess(b *testing.B) {
	v := NewVM()
	attrCounter := v.Intern("counter")

	objVal := val.NewInstanceVal(val.NewRootShape())
	objVal.AsInstance().SetAttr(attrCounter, val.Int64Val(0))

	// Bytecode loop executing `obj.counter += 1` inside the VM:
	// obj.counter = 0
	// while obj.counter < 1000:
	//     obj.counter = obj.counter + 1
	// return obj.counter
	const loopIters = 1000
	consts := []val.Value{
		attrCounter,             // 0: "counter"
		val.Int64Val(0),         // 1: 0
		val.Int64Val(loopIters), // 2: 1000
		val.Int64Val(1),         // 3: 1
	}
	code := []uint32{
		/* 00 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 01 */ Encode(OP_LOAD_CONST, 0, 1), // push 0
		/* 02 */ Encode(OP_STORE_ATTR, 0, 0), // obj.counter = 0
		/* 03 */ Encode(OP_LOAD_FAST, 0, 0), // loop: push obj
		/* 04 */ Encode(OP_LOAD_ATTR, 0, 0), // push obj.counter
		/* 05 */ Encode(OP_LOAD_CONST, 0, 2), // push 1000
		/* 06 */ Encode(OP_COMPARE_LT, 0, 0), // obj.counter < 1000
		/* 07 */ Encode(OP_JUMP_IF_FALSE, 0, 15), // if false jump to 15
		/* 08 */ Encode(OP_LOAD_FAST, 0, 0), // push obj (for store)
		/* 09 */ Encode(OP_LOAD_FAST, 0, 0), // push obj (for load)
		/* 10 */ Encode(OP_LOAD_ATTR, 0, 0), // push obj.counter
		/* 11 */ Encode(OP_LOAD_CONST, 0, 3), // push 1
		/* 12 */ Encode(OP_BINARY_ADD, 0, 0), // obj.counter + 1
		/* 13 */ Encode(OP_STORE_ATTR, 0, 0), // obj.counter = obj.counter + 1
		/* 14 */ Encode(OP_JUMP, 0, 3), // jump to 3
		/* 15 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 16 */ Encode(OP_LOAD_ATTR, 0, 0), // push obj.counter
		/* 17 */ Encode(OP_RETURN, 0, 0), // return obj.counter
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		v.stack[0] = objVal
		res := v.Run(code, consts, 1)
		if res.AsInt64() != loopIters {
			b.Fatalf("res = %v, want %d", res.AsInt64(), loopIters)
		}
	}
}

func TestDiamondMROAndSuper(t *testing.T) {
	v := NewVM()
	computeName := v.Intern("compute")

	// Diamond hierarchy:
	//   A
	//  / \
	// B   C
	//  \ /
	//   D(B, C)
	clsA := val.MustNewType("A", nil, nil)
	clsB := val.MustNewType("B", []*val.PyType{clsA}, nil)
	clsC := val.MustNewType("C", []*val.PyType{clsA}, nil)
	clsD := val.MustNewType("D", []*val.PyType{clsB, clsC}, nil)

	// super(thisClass, self) built-in helper for testing cooperative super() calls
	superBuiltin := val.NewBuiltinFuncVal("super", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewSuperVal(args[0].AsType(), args[1])
	})

	// A.compute(self, acc): return acc * 10 + 1
	fnA := val.NewFunctionVal("A.compute", NewCodeVal(&CodeObject{
		Name: "A.compute",
		Consts: []val.Value{
			val.Int64Val(10),
			val.Int64Val(1),
		},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 1),  // acc
			Encode(OP_LOAD_CONST, 0, 0), // 10
			Encode(OP_BINARY_MUL, 0, 0), // acc * 10
			Encode(OP_LOAD_CONST, 0, 1), // 1
			Encode(OP_BINARY_ADD, 0, 0), // acc * 10 + 1
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}))
	clsA.SetAttr(computeName, fnA)

	makeSuperStepMethod := func(cls *val.PyType, digit int64) val.Value {
		// def compute(self, acc):
		//     return super(cls, self).compute(acc * 10 + digit)
		return val.NewFunctionVal(cls.Name+".compute", NewCodeVal(&CodeObject{
			Name: cls.Name + ".compute",
			Consts: []val.Value{
				superBuiltin,        // 0: super
				val.TypeVal(cls),    // 1: cls
				computeName,         // 2: "compute"
				val.Int64Val(10),    // 3: 10
				val.Int64Val(digit), // 4: digit
			},
			Code: []uint32{
				Encode(OP_LOAD_CONST, 0, 1),  // push cls
				Encode(OP_LOAD_FAST, 0, 0),   // push self
				Encode(OP_CALL, 2, 0),        // super(cls, self)
				Encode(OP_LOAD_METHOD, 0, 2), // load method "compute" on super proxy
				Encode(OP_LOAD_FAST, 0, 1),   // push acc
				Encode(OP_LOAD_CONST, 0, 3),  // push 10
				Encode(OP_BINARY_MUL, 0, 0),  // acc * 10
				Encode(OP_LOAD_CONST, 0, 4),  // push digit
				Encode(OP_BINARY_ADD, 0, 0),  // acc * 10 + digit
				Encode(OP_CALL_METHOD, 1, 0), // call super().compute(acc * 10 + digit)
				Encode(OP_RETURN, 0, 0),
			},
			NumLocals: 2,
			NumParams: 2,
		}))
	}

	clsC.SetAttr(computeName, makeSuperStepMethod(clsC, 2))
	clsB.SetAttr(computeName, makeSuperStepMethod(clsB, 3))
	clsD.SetAttr(computeName, makeSuperStepMethod(clsD, 4))

	instD := val.NewInstanceOfType(clsD)

	// Main bytecode: return instD.compute(0)
	// MRO is [D, B, C, A, object], so traversal order is D(4) -> B(3) -> C(2) -> A(1) => 4321
	mainConsts := []val.Value{
		instD,
		computeName,
		val.Int64Val(0),
	}
	mainCode := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),  // push instD
		Encode(OP_LOAD_METHOD, 0, 1), // load method "compute"
		Encode(OP_LOAD_CONST, 0, 2),  // push 0
		Encode(OP_CALL_METHOD, 1, 0), // instD.compute(0)
		Encode(OP_RETURN, 0, 0),
	}

	res := v.Run(mainCode, mainConsts, 0)
	if !res.IsInt64() || res.AsInt64() != 4321 {
		t.Fatalf("diamond super() traversal result = %v, want 4321", res)
	}
}

func TestMROVersionTagInvalidation(t *testing.T) {
	v := NewVM()
	methodName := v.Intern("value")

	clsBase := val.MustNewType("Base", nil, nil)
	clsSub := val.MustNewType("Sub", []*val.PyType{clsBase}, nil)

	fn1 := val.NewFunctionVal("value_v1", NewCodeVal(&CodeObject{
		Consts:    []val.Value{val.Int64Val(10)},
		Code:      []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)},
		NumLocals: 1,
		NumParams: 1,
	}))
	fn2 := val.NewFunctionVal("value_v2", NewCodeVal(&CodeObject{
		Consts:    []val.Value{val.Int64Val(20)},
		Code:      []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)},
		NumLocals: 1,
		NumParams: 1,
	}))

	clsBase.SetAttr(methodName, fn1)
	inst := val.NewInstanceOfType(clsSub)

	consts := []val.Value{inst, methodName}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_LOAD_METHOD, 0, 1),
		Encode(OP_CALL_METHOD, 0, 0),
		Encode(OP_RETURN, 0, 0),
	}

	// Warm up inline cache at ip=1
	res1 := v.Run(code, consts, 0)
	if !res1.IsInt64() || res1.AsInt64() != 10 {
		t.Fatalf("initial method call = %v, want 10", res1)
	}

	verBefore := clsSub.VersionTag
	// Mutate Base class method after warmup -> must invalidate Sub's VersionTag and IC
	clsBase.SetAttr(methodName, fn2)
	if clsSub.VersionTag <= verBefore {
		t.Fatalf("expected clsSub.VersionTag to increment after Base mutation")
	}

	res2 := v.Run(code, consts, 0)
	if !res2.IsInt64() || res2.AsInt64() != 20 {
		t.Fatalf("method call after Base mutation = %v, want 20", res2)
	}
}

func TestPropertyDescriptorPrecedence(t *testing.T) {
	v := NewVM()
	rawAttr := v.Intern("_x")
	propAttr := v.Intern("x")

	cls := val.MustNewType("PropBox", nil, nil)

	// fget(self): return self._x * 10
	fget := val.NewFunctionVal("fget", NewCodeVal(&CodeObject{
		Consts: []val.Value{rawAttr, val.Int64Val(10)},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_ATTR, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BINARY_MUL, 0, 0),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 1,
		NumParams: 1,
	}))

	// fset(self, val): self._x = val + 1
	fset := val.NewFunctionVal("fset", NewCodeVal(&CodeObject{
		Consts: []val.Value{rawAttr, val.Int64Val(1)},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_STORE_ATTR, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}))

	prop := val.NewPropertyVal(fget, fset, val.NoneVal())
	cls.SetAttr(propAttr, prop)

	inst := val.NewInstanceOfType(cls)
	// Even if an instance slot named "x" is manually set on the instance Shape,
	// the @property data descriptor on cls MUST override it!
	inst.AsInstance().SetAttr(propAttr, val.Int64Val(999))
	inst.AsInstance().SetAttr(rawAttr, val.Int64Val(0))

	// Bytecode:
	// inst.x = 4   (fset sets inst._x = 4 + 1 = 5)
	// return inst.x (fget returns inst._x * 10 = 50)
	consts := []val.Value{
		inst,            // 0
		propAttr,        // 1: "x"
		val.Int64Val(4), // 2: 4
	}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_LOAD_CONST, 0, 2),
		Encode(OP_STORE_ATTR, 0, 1), // inst.x = 4
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_LOAD_ATTR, 0, 1), // push inst.x
		Encode(OP_RETURN, 0, 0),
	}

	res := v.Run(code, consts, 0)
	if !res.IsInt64() || res.AsInt64() != 50 {
		t.Fatalf("@property getter/setter result = %v, want 50", res)
	}

	// Verify 0 allocations on subsequent runs
	allocs := testing.AllocsPerRun(100, func() {
		r := v.Run(code, consts, 0)
		if r.AsInt64() != 50 {
			t.Fatal("unexpected property result")
		}
	})
	if allocs != 0 {
		t.Fatalf("@property get/set allocated %v times, want 0", allocs)
	}
}

func TestCustomDunderTypeSlots(t *testing.T) {
	v := NewVM()
	valAttr := v.Intern("val")

	cls := val.MustNewType("CustomNum", nil, nil)

	// __add__(self, other): return self.val + other.val
	dunderAddFn := val.NewFunctionVal("__add__", NewCodeVal(&CodeObject{
		Consts: []val.Value{valAttr},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_ATTR, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_LOAD_ATTR, 0, 0),
			Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}))

	// __getitem__(self, idx): return self.val * idx
	dunderGetItemFn := val.NewFunctionVal("__getitem__", NewCodeVal(&CodeObject{
		Consts: []val.Value{valAttr},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_ATTR, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_MUL, 0, 0),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}))

	cls.SetAttr(val.DunderAdd, dunderAddFn)
	cls.SetAttr(val.DunderGetItem, dunderGetItemFn)

	if cls.Slots.NbAdd == nil || cls.Slots.MpSubscript == nil {
		t.Fatalf("expected NbAdd and MpSubscript slots to be populated on CustomNum")
	}

	a := val.NewInstanceOfType(cls)
	a.AsInstance().SetAttr(valAttr, val.Int64Val(15))
	b := val.NewInstanceOfType(cls)
	b.AsInstance().SetAttr(valAttr, val.Int64Val(27))

	// Bytecode: return (a + b) + a[3] => (15 + 27) + (15 * 3) = 42 + 45 = 87
	consts := []val.Value{
		a,
		b,
		val.Int64Val(3),
	}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),    // a
		Encode(OP_LOAD_CONST, 0, 1),    // b
		Encode(OP_BINARY_ADD, 0, 0),    // a + b => 42
		Encode(OP_LOAD_CONST, 0, 0),    // a
		Encode(OP_LOAD_CONST, 0, 2),    // 3
		Encode(OP_BINARY_SUBSCR, 0, 0), // a[3] => 45
		Encode(OP_BINARY_ADD, 0, 0),    // 42 + 45 => 87
		Encode(OP_RETURN, 0, 0),
	}

	res := v.Run(code, consts, 0)
	if !res.IsInt64() || res.AsInt64() != 87 {
		t.Fatalf("custom __add__ and __getitem__ result = %v, want 87", res)
	}

	allocs := testing.AllocsPerRun(100, func() {
		r := v.Run(code, consts, 0)
		if r.AsInt64() != 87 {
			t.Fatal("unexpected result")
		}
	})
	if allocs != 0 {
		t.Fatalf("custom dunder slots allocated %v times, want 0", allocs)
	}
}

func TestBoundMethodMaterializationVsZeroAllocCallMethod(t *testing.T) {
	v := NewVM()
	incName := v.Intern("inc")
	countName := v.Intern("count")

	cls := val.MustNewType("Counter", nil, nil)
	incFn := val.NewFunctionVal("inc", NewCodeVal(&CodeObject{
		Consts: []val.Value{countName},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_ATTR, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 2,
		NumParams: 2,
	}))
	cls.SetAttr(incName, incFn)

	inst := val.NewInstanceOfType(cls)
	inst.AsInstance().SetAttr(countName, val.Int64Val(100))

	// 1. Accessing `m = inst.inc` via OP_LOAD_ATTR must allocate a first-class BoundMethod on the heap.
	loadAttrConsts := []val.Value{inst, incName}
	loadAttrCode := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_LOAD_ATTR, 0, 1),
		Encode(OP_RETURN, 0, 0),
	}
	boundVal := v.Run(loadAttrCode, loadAttrConsts, 0)
	if !boundVal.IsBoundMethod() {
		t.Fatalf("OP_LOAD_ATTR on method returned %v, want KindBoundMethod", boundVal.Kind())
	}

	// Calling the materialized BoundMethod via OP_CALL: return m(5) => 105
	callBoundConsts := []val.Value{boundVal, val.Int64Val(5)}
	callBoundCode := []uint32{
		Encode(OP_LOAD_CONST, 0, 0), // push m
		Encode(OP_LOAD_CONST, 0, 1), // push 5
		Encode(OP_CALL, 1, 99),      // call stack callable m(5)
		Encode(OP_RETURN, 0, 0),
	}
	resBound := v.Run(callBoundCode, callBoundConsts, 0)
	if !resBound.IsInt64() || resBound.AsInt64() != 105 {
		t.Fatalf("calling BoundMethod returned %v, want 105", resBound)
	}

	// 2. Calling `inst.inc(5)` via OP_LOAD_METHOD + OP_CALL_METHOD must execute with 0 allocations.
	callMethodConsts := []val.Value{inst, incName, val.Int64Val(5)}
	callMethodCode := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_LOAD_METHOD, 0, 1),
		Encode(OP_LOAD_CONST, 0, 2),
		Encode(OP_CALL_METHOD, 1, 0),
		Encode(OP_RETURN, 0, 0),
	}
	// Warmup IC
	_ = v.Run(callMethodCode, callMethodConsts, 0)

	allocs := testing.AllocsPerRun(100, func() {
		r := v.Run(callMethodCode, callMethodConsts, 0)
		if r.AsInt64() != 105 {
			t.Fatalf("got %v, want 105", r)
		}
	})
	if allocs != 0 {
		t.Fatalf("OP_LOAD_METHOD + OP_CALL_METHOD allocated %v times, want 0", allocs)
	}
}

func TestVectorcallKeywordArgsZeroAlloc(t *testing.T) {
	v := NewVM()
	paramA := v.Intern("a")
	paramB := v.Intern("b")
	paramC := v.Intern("c")

	// def calc(a, b=10, c=100):
	//     return a + b * 2 + c * 3
	calcCode := NewCodeVal(&CodeObject{
		Name:       "calc",
		ParamNames: []val.Value{paramA, paramB, paramC},
		Consts: []val.Value{
			val.Int64Val(2),
			val.Int64Val(3),
		},
		Code: []uint32{
			Encode(OP_LOAD_FAST, 0, 0),  // a
			Encode(OP_LOAD_FAST, 0, 1),  // b
			Encode(OP_LOAD_CONST, 0, 0), // 2
			Encode(OP_BINARY_MUL, 0, 0), // b * 2
			Encode(OP_BINARY_ADD, 0, 0), // a + b * 2
			Encode(OP_LOAD_FAST, 0, 2),  // c
			Encode(OP_LOAD_CONST, 0, 1), // 3
			Encode(OP_BINARY_MUL, 0, 0), // c * 3
			Encode(OP_BINARY_ADD, 0, 0), // a + b * 2 + c * 3
			Encode(OP_RETURN, 0, 0),
		},
		NumLocals: 3,
		NumParams: 3,
	})
	calcFn := val.NewFunctionVal("calc", calcCode, val.Int64Val(10), val.Int64Val(100))

	// Call: calc(1, c=20) -> 1 + 10*2 + 20*3 = 81
	kwTuple := val.NewTupleVal(paramC)
	consts := []val.Value{
		calcFn,           // 0
		val.Int64Val(1),  // 1: positional arg a=1
		val.Int64Val(20), // 2: keyword arg c=20
		kwTuple,          // 3: kwnames = ("c",)
	}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 0), // push calcFn
		Encode(OP_LOAD_CONST, 0, 1), // push 1
		Encode(OP_LOAD_CONST, 0, 2), // push 20
		Encode(OP_CALL_KW, 2, 3),    // call with 2 total args, kwnames at consts[3]
		Encode(OP_RETURN, 0, 0),
	}

	res := v.Run(code, consts, 0)
	if !res.IsInt64() || res.AsInt64() != 81 {
		t.Fatalf("OP_CALL_KW result = %v, want 81", res)
	}

	allocs := testing.AllocsPerRun(100, func() {
		r := v.Run(code, consts, 0)
		if r.AsInt64() != 81 {
			t.Fatal("unexpected OP_CALL_KW result")
		}
	})
	if allocs != 0 {
		t.Fatalf("OP_CALL_KW allocated %v times, want 0", allocs)
	}
}

func BenchmarkMethodCallLoop(b *testing.B) {
	v := NewVM()
	countAttr := v.Intern("count")
	incMethod := v.Intern("increment")

	// class Counter:
	//     def increment(self, delta):
	//         self.count = self.count + delta
	//         return self.count
	cls := val.MustNewType("Counter", nil, nil)
	incFn := val.NewFunctionVal("increment", NewCodeVal(&CodeObject{
		Name:   "increment",
		Consts: []val.Value{countAttr},
		Code: []uint32{
			/* 00 */ Encode(OP_LOAD_FAST, 0, 0), // push self (store target)
			/* 01 */ Encode(OP_LOAD_FAST, 0, 0), // push self (load source)
			/* 02 */ Encode(OP_LOAD_ATTR, 0, 0), // push self.count
			/* 03 */ Encode(OP_LOAD_FAST, 0, 1), // push delta
			/* 04 */ Encode(OP_BINARY_ADD, 0, 0), // self.count + delta
			/* 05 */ Encode(OP_STORE_ATTR, 0, 0), // self.count = self.count + delta
			/* 06 */ Encode(OP_LOAD_FAST, 0, 0), // push self
			/* 07 */ Encode(OP_LOAD_ATTR, 0, 0), // push self.count
			/* 08 */ Encode(OP_RETURN, 0, 0), // return self.count
		},
		NumLocals: 2,
		NumParams: 2,
	}))
	cls.SetAttr(incMethod, incFn)

	objVal := val.NewInstanceOfType(cls)
	objVal.AsInstance().SetAttr(countAttr, val.Int64Val(0))

	// VM loop calling obj.increment(1) 10,000,000 times:
	// obj.count = 0
	// i = 0
	// while i < 10_000_000:
	//     obj.increment(1)
	//     i = i + 1
	// return obj.count
	const limit = 10_000_000
	consts := []val.Value{
		countAttr,           // 0: "count"
		incMethod,           // 1: "increment"
		val.Int64Val(0),     // 2: 0
		val.Int64Val(limit), // 3: 10_000_000
		val.Int64Val(1),     // 4: 1
	}
	code := []uint32{
		/* 00 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 01 */ Encode(OP_LOAD_CONST, 0, 2), // push 0
		/* 02 */ Encode(OP_STORE_ATTR, 0, 0), // obj.count = 0
		/* 03 */ Encode(OP_LOAD_CONST, 0, 2), // push 0
		/* 04 */ Encode(OP_STORE_FAST, 0, 1), // i = 0
		/* 05 */ Encode(OP_LOAD_FAST, 0, 1), // loop: push i
		/* 06 */ Encode(OP_LOAD_CONST, 0, 3), // push 10_000_000
		/* 07 */ Encode(OP_COMPARE_LT, 0, 0), // i < 10_000_000
		/* 08 */ Encode(OP_JUMP_IF_FALSE, 0, 19), // if false jump to 19
		/* 09 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 10 */ Encode(OP_LOAD_METHOD, 0, 1), // load method obj.increment -> [obj, incFn]
		/* 11 */ Encode(OP_LOAD_CONST, 0, 4), // push 1
		/* 12 */ Encode(OP_CALL_METHOD, 1, 0), // call obj.increment(1)
		/* 13 */ Encode(OP_STORE_FAST, 0, 2), // discard/store return value in local[2]
		/* 14 */ Encode(OP_LOAD_FAST, 0, 1), // push i
		/* 15 */ Encode(OP_LOAD_CONST, 0, 4), // push 1
		/* 16 */ Encode(OP_BINARY_ADD, 0, 0), // i + 1
		/* 17 */ Encode(OP_STORE_FAST, 0, 1), // i = i + 1
		/* 18 */ Encode(OP_JUMP, 0, 5), // jump to 5
		/* 19 */ Encode(OP_LOAD_FAST, 0, 0), // push obj
		/* 20 */ Encode(OP_LOAD_ATTR, 0, 0), // push obj.count
		/* 21 */ Encode(OP_RETURN, 0, 0), // return obj.count
	}

	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		v.stack[0] = objVal
		res := v.Run(code, consts, 3)
		if !res.IsInt64() || res.AsInt64() != limit {
			b.Fatalf("res = %v, want %d", res.AsInt64(), limit)
		}
	}
}

func BenchmarkNativeBuiltinCall(b *testing.B) {
	v := NewVM()
	items := val.NewListVal(val.Int64Val(10), val.Int64Val(20), val.Int64Val(30), val.Int64Val(40))

	// Bytecode calling len(items) + abs(-42) in a VM execution:
	// return len(items) + abs(-42) => 4 + 42 = 46
	consts := []val.Value{
		val.BuiltinLenVal, // 0: len
		val.BuiltinAbsVal, // 1: abs
		items,             // 2: [10, 20, 30, 40]
		val.Int64Val(-42), // 3: -42
	}
	code := []uint32{
		Encode(OP_LOAD_CONST, 0, 2), // push items
		Encode(OP_CALL, 1, 0),       // len(items)
		Encode(OP_LOAD_CONST, 0, 3), // push -42
		Encode(OP_CALL, 1, 1),       // abs(-42)
		Encode(OP_BINARY_ADD, 0, 0), // len(items) + abs(-42)
		Encode(OP_RETURN, 0, 0),
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		res := v.Run(code, consts, 0)
		if !res.IsInt64() || res.AsInt64() != 46 {
			b.Fatalf("res = %v, want 46", res.AsInt64())
		}
	}
}

func BenchmarkTryBlockHappyPath(b *testing.B) {
	v := NewVM()

	// 1. Loop outside try:
	constsNoTry := []val.Value{val.Int64Val(0), val.Int64Val(1000), val.Int64Val(1)}
	codeNoTry := []uint32{
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_STORE_FAST, 0, 0),
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_LOAD_CONST, 0, 1),
		Encode(OP_COMPARE_LT, 0, 0),
		Encode(OP_JUMP_IF_FALSE, 0, 11),
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_LOAD_CONST, 0, 2),
		Encode(OP_BINARY_ADD, 0, 0),
		Encode(OP_STORE_FAST, 0, 0),
		Encode(OP_JUMP, 0, 2),
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_RETURN, 0, 0),
	}
	coNoTry := &CodeObject{
		Name:      "no_try",
		Code:      codeNoTry,
		Consts:    constsNoTry,
		NumLocals: 1,
	}

	// 2. Exact same loop inside try...except Exception (which never raises):
	constsWithTry := []val.Value{val.Int64Val(0), val.Int64Val(1000), val.Int64Val(1), val.TypeVal(val.ExceptionType), val.Int64Val(-1)}
	codeWithTry := []uint32{
		/* 00 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 01 */ Encode(OP_STORE_FAST, 0, 0),
		/* 02 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 03 */ Encode(OP_LOAD_CONST, 0, 1),
		/* 04 */ Encode(OP_COMPARE_LT, 0, 0),
		/* 05 */ Encode(OP_JUMP_IF_FALSE, 0, 11),
		/* 06 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 07 */ Encode(OP_LOAD_CONST, 0, 2),
		/* 08 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 09 */ Encode(OP_STORE_FAST, 0, 0),
		/* 10 */ Encode(OP_JUMP, 0, 2),
		/* 11 */ Encode(OP_JUMP, 0, 19), // jump to normal return
		/* 12 */ Encode(OP_DUP_TOP, 0, 0),
		/* 13 */ Encode(OP_LOAD_CONST, 0, 3), // Exception
		/* 14 */ Encode(OP_CHECK_EXC_MATCH, 0, 0),
		/* 15 */ Encode(OP_JUMP_IF_FALSE, 0, 18),
		/* 16 */ Encode(OP_POP_TOP, 0, 0),
		/* 17 */ Encode(OP_LOAD_CONST, 0, 4), // -1
		/* 18 */ Encode(OP_STORE_FAST, 0, 0),
		/* 19 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 20 */ Encode(OP_RETURN, 0, 0),
	}
	coWithTry := &CodeObject{
		Name:      "with_try",
		Code:      codeWithTry,
		Consts:    constsWithTry,
		NumLocals: 1,
		ExceptionTable: []ExceptionEntry{
			{StartIP: 0, EndIP: 11, TargetIP: 12, TargetStackDepth: 0},
		},
	}

	b.Run("WithoutTry", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res := v.RunCode(coNoTry)
			if !res.IsInt64() || res.AsInt64() != 1000 {
				b.Fatalf("res = %v, want 1000", res)
			}
		}
	})

	b.Run("WithTryHappyPath", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res := v.RunCode(coWithTry)
			if !res.IsInt64() || res.AsInt64() != 1000 {
				b.Fatalf("res = %v, want 1000", res)
			}
		}
	})
}

func BenchmarkForLoopRangeAndList(b *testing.B) {
	v := NewVM()

	// 1. for x in range(100_000):
	//        s = s + x
	//    return s
	constsRange := []val.Value{
		val.RangeVal(0, 100_000),
		val.Int64Val(0),
	}
	codeRange := []uint32{
		Encode(OP_LOAD_CONST, 0, 1),
		Encode(OP_STORE_FAST, 0, 1),
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_GET_ITER, 0, 0),
		/* 04 */ Encode(OP_FOR_ITER, 0, 11),
		/* 05 */ Encode(OP_STORE_FAST, 0, 0),
		/* 06 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 07 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 08 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 09 */ Encode(OP_STORE_FAST, 0, 1),
		/* 10 */ Encode(OP_JUMP, 0, 4),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 12 */ Encode(OP_RETURN, 0, 0),
	}
	coRange := &CodeObject{
		Name:      "for_range",
		Code:      codeRange,
		Consts:    constsRange,
		NumLocals: 2,
	}

	// 2. Preallocated list of 100_000 integers:
	listItems := make([]val.Value, 100_000)
	for i := 0; i < 100_000; i++ {
		listItems[i] = val.Int64Val(int64(i))
	}
	preallocList := val.NewListVal(listItems...)

	constsList := []val.Value{
		preallocList,
		val.Int64Val(0),
	}
	codeList := []uint32{
		Encode(OP_LOAD_CONST, 0, 1),
		Encode(OP_STORE_FAST, 0, 1),
		Encode(OP_LOAD_CONST, 0, 0),
		Encode(OP_GET_ITER, 0, 0),
		/* 04 */ Encode(OP_FOR_ITER, 0, 11),
		/* 05 */ Encode(OP_STORE_FAST, 0, 0),
		/* 06 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 07 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 08 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 09 */ Encode(OP_STORE_FAST, 0, 1),
		/* 10 */ Encode(OP_JUMP, 0, 4),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 12 */ Encode(OP_RETURN, 0, 0),
	}
	coList := &CodeObject{
		Name:      "for_list",
		Code:      codeList,
		Consts:    constsList,
		NumLocals: 2,
	}

	b.Run("Range_100k", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res := v.RunCode(coRange)
			if !res.IsInt64() || res.AsInt64() != 4999950000 {
				b.Fatalf("res = %v, want 4999950000", res)
			}
		}
	})

	b.Run("PreallocatedList_100k", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			res := v.RunCode(coList)
			if !res.IsInt64() || res.AsInt64() != 4999950000 {
				b.Fatalf("res = %v, want 4999950000", res)
			}
		}
	})
}
