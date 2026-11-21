//go:build amd64 && linux

package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestJITArithmeticLoopAndOverflowBailout(t *testing.T) {
	// x = 0; while x < n: x += step; return x
	co := &CodeObject{
		Name: "jit_loop",
		Consts: []val.Value{
			val.Int64Val(0),
		},
		Code: []uint32{
			Encode(OP_LOAD_CONST, 0, 0),     // 0: 0
			Encode(OP_STORE_FAST, 0, 2),     // 1: x = 0
			Encode(OP_LOAD_FAST, 0, 2),      // 2: x
			Encode(OP_LOAD_FAST, 0, 0),      // 3: n
			Encode(OP_COMPARE_LT, 0, 0),     // 4: x < n
			Encode(OP_JUMP_IF_FALSE, 0, 10), // 5: -> 10
			Encode(OP_LOAD_FAST, 0, 2),      // 6: x
			Encode(OP_LOAD_FAST, 0, 1),      // 7: step
			Encode(OP_BINARY_ADD, 0, 0),     // 8: x + step
			Encode(OP_STORE_FAST, 0, 2),     // 9: x = x + step
			Encode(OP_JUMP, 0, 2),           // 10? Wait: jump back to 2 (see below)
		},
		NumLocals:    3,
		NumParams:    2,
		ParamNames:   []val.Value{val.InternString("n"), val.InternString("step")},
		MaxStackSize: 8,
	}
	co.Code = []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 2),
		/* 2 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 3 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 4 */ Encode(OP_COMPARE_LT, 0, 0),
		/* 5 */ Encode(OP_JUMP_IF_FALSE, 0, 11),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 7 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 8 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 9 */ Encode(OP_STORE_FAST, 0, 2),
		/* 10 */ Encode(OP_JUMP, 0, 2),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 12 */ Encode(OP_RETURN, 0, 0),
	}
	for range 12 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))

	e := NewEngine()
	defer e.Close()
	co.Init()
	OptimizeCodeObject(co)
	rc := co.RegCode()
	if rc == nil {
		t.Fatalf("expected RegCode")
	}
	jc := jitCompileRegCode(e, rc, co.Consts)
	if jc == nil {
		t.Fatalf("expected jitCode")
	}
	rc.jit.Store(jc)

	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	// 1. Normal loop exceeding 256 backward ticks (tests backward tick bailout + resume).
	v.stack[0] = val.Int64Val(1000)
	v.stack[1] = val.Int64Val(1)
	res := v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unexpected exception: %s", v.FormatException(exc))
	}
	if !res.IsInt64() || res.AsInt64() != 1000 {
		t.Fatalf("got %v, want 1000", res)
	}

	// 2. Non-int64 bailout (float step) + overflow bailout (near MaxInt64).
	v.stack[0] = val.Float64Val(3.5)
	v.stack[1] = val.Float64Val(1.5)
	resF := v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unexpected float exception: %s", v.FormatException(exc))
	}
	if !resF.IsFloat64() || resF.AsFloat64() != 4.5 {
		t.Fatalf("got %v, want 4.5", resF)
	}
}

func TestJITOverflowAndSlotAttrAndEngineClose(t *testing.T) {
	// def sum_attr(obj, n):
	//     acc = 0
	//     i = 0
	//     while i < n:
	//         acc = acc + obj.val
	//         i = i + 1
	//     return acc
	valName := val.InternString("val")
	tp := val.MustNewType("JITSlotBox", []*val.PyType{val.ObjectType}, nil)
	tp.Flags |= val.FlagForbidDynamicAttrs
	tp.RootShape = val.DefaultRootShape().Transition(valName)
	descr := val.NewMemberDescr(tp, valName, 0)
	tp.Dict.Set(valName, descr)

	co := &CodeObject{
		Name: "sum_attr",
		Consts: []val.Value{
			val.Int64Val(0), // 0
			val.Int64Val(1), // 1
			valName,         // 2
		},
		Code: []uint32{
			/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
			/* 1 */ Encode(OP_STORE_FAST, 0, 2), // acc = 0
			/* 2 */ Encode(OP_LOAD_CONST, 0, 0),
			/* 3 */ Encode(OP_STORE_FAST, 0, 3), // i = 0
			/* 4 */ Encode(OP_LOAD_FAST, 0, 3),
			/* 5 */ Encode(OP_LOAD_FAST, 0, 1),
			/* 6 */ Encode(OP_COMPARE_LT, 0, 0),
			/* 7 */ Encode(OP_JUMP_IF_FALSE, 0, 17),
			/* 8 */ Encode(OP_LOAD_FAST, 0, 2),
			/* 9 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 10 */ Encode(OP_LOAD_ATTR, 0, 2), // obj.val
			/* 11 */ Encode(OP_BINARY_ADD, 0, 0),
			/* 12 */ Encode(OP_STORE_FAST, 0, 2),
			/* 13 */ Encode(OP_LOAD_FAST, 0, 3),
			/* 14 */ Encode(OP_LOAD_CONST, 0, 1),
			/* 15 */ Encode(OP_BINARY_ADD, 0, 0),
			/* 16 */ Encode(OP_STORE_FAST, 0, 3),
			/* 17 */ Encode(OP_JUMP, 0, 4),
			/* 18 */ Encode(OP_LOAD_FAST, 0, 2),
			/* 19 */ Encode(OP_RETURN, 0, 0),
		},
		NumLocals:    4,
		NumParams:    2,
		ParamNames:   []val.Value{val.InternString("obj"), val.InternString("n")},
		MaxStackSize: 8,
	}
	// Fix jump target: OP_JUMP_IF_FALSE should jump to 18.
	co.Code[7] = Encode(OP_JUMP_IF_FALSE, 0, 18)
	for range 5 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))

	e := NewEngine()
	co.Init()
	inst := val.NewInstanceOfType(tp)
	inst.AsInstance().SetSlot(0, val.Int64Val(7))

	v := e.AcquireVM()
	// Warm up IC at sip=10 to ICKindSlotMember via Tier-1 before publishing JIT.
	v.stack[0] = inst
	v.stack[1] = val.Int64Val(5)
	res1 := v.RunCode(co)
	if !res1.IsInt64() || res1.AsInt64() != 35 {
		t.Fatalf("warmup got %v, want 35", res1)
	}

	OptimizeCodeObject(co)
	rc := co.RegCode()
	if rc == nil {
		t.Fatalf("expected RegCode")
	}
	jc := jitCompileRegCode(e, rc, co.Consts)
	if jc == nil {
		t.Fatalf("expected jitCode")
	}
	rc.jit.Store(jc)

	// Second run hits inline ICKindSlotMember in JIT!
	v.stack[0] = inst
	v.stack[1] = val.Int64Val(300)
	res2 := v.RunCode(co)
	if !res2.IsInt64() || res2.AsInt64() != 2100 {
		t.Fatalf("hot slot attr got %v, want 2100", res2)
	}

	// Overflow test: (1<<62) + (1<<62) promotes cleanly via bailout to BigInt.
	inst.AsInstance().SetSlot(0, val.Int64Val(1<<62))
	v.stack[0] = inst
	v.stack[1] = val.Int64Val(2)
	resBig := v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("overflow raised: %s", v.FormatException(exc))
	}
	if resBig.Kind() != val.KindBigInt {
		t.Fatalf("expected BigInt on int64 overflow bailout, got %v", resBig.Kind())
	}

	e.ReleaseVM(v)
	e.Close()
	if rc.jit.Load() != nil {
		t.Fatalf("expected Engine.Close() to clear rc.jit")
	}
	if !jc.buf.closed.Load() {
		t.Fatalf("expected Engine.Close() to munmap jit buffer")
	}
}
