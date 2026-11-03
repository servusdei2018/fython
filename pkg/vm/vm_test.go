package vm

import (
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
