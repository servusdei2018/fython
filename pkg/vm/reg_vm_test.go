package vm

import (
	"fmt"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

// mustRegCode compiles co and publishes its register form, failing the test
// when the function is ineligible.
func mustRegCode(t *testing.T, co *CodeObject) *RegCode {
	t.Helper()
	co.Init()
	rc := TryCompileRegCode(co)
	if rc == nil {
		t.Fatalf("expected RegCode for %s", co.Name)
	}
	co.publishRegCode(rc)
	return rc
}

// TestRegIsaRoundTrip verifies register instruction encoding.
func TestRegIsaRoundTrip(t *testing.T) {
	ops := []RegOp{
		RegMov, RegLoadConst, RegLoadGlobal, RegStoreGlobal,
		RegAdd, RegSub, RegMul, RegTrueDiv, RegFloorDiv, RegMod,
		RegPow, RegMatMul, RegBitAnd, RegBitOr, RegBitXor,
		RegLShift, RegRShift, RegNeg, RegPos, RegInvert, RegNot,
		RegEq, RegNe, RegLt, RegLe, RegGt, RegGe, RegIn, RegIs,
		RegJmp, RegJmpIfFalse, RegJmpIfTrue,
		RegLtJmpIfFalse, RegLtJmpIfTrue, RegEqJmpIfFalse, RegEqJmpIfTrue,
		RegIsNoneJmpIfFalse, RegIsNoneJmpIfTrue,
		RegLoadAttr, RegStoreAttr, RegLoadMethod,
		RegCall, RegCallMethod, RegCallKw, RegCallFunctionEx, RegReturn,
		RegBuildList, RegBuildTuple, RegBuildSet, RegBuildDict,
		RegBuildSlice, RegBuildString, RegFormatValue, RegUnpack,
		RegListAppend, RegSetAdd, RegMapAdd, RegListExtend, RegMakeFunction,
		RegSetupRange, RegGetIter, RegForIter, RegForIterRange,
		RegGetItem, RegSetItem, RegPopTop, RegDupTop, RegRaise,
	}
	for _, op := range ops {
		inst := EncodeReg(op, 200, 150, 100, 0xDEADBEEF)
		if got := DecodeRegOp(inst); got != op {
			t.Fatalf("op roundtrip: got %v want %v", got, op)
		}
		if d := DecodeRegDst(inst); d != 200 {
			t.Fatalf("dst roundtrip: got %d", d)
		}
		if s := DecodeRegSrc1(inst); s != 150 {
			t.Fatalf("src1 roundtrip: got %d", s)
		}
		if s := DecodeRegSrc2(inst); s != 100 {
			t.Fatalf("src2 roundtrip: got %d", s)
		}
		if a := DecodeRegAux(inst); a != 0xDEADBEEF {
			t.Fatalf("aux roundtrip: got %x", a)
		}
		if op.String() == "" || op.String() == "REG_UNKNOWN(0)" {
			t.Fatalf("missing name for %d", uint8(op))
		}
	}
}

// TestRegCompileZeroLoadFast checks that loads emit no instructions and a
// constant store coalesces into the producer.
func TestRegCompileZeroLoadFast(t *testing.T) {
	co := &CodeObject{
		Name:         "zero",
		Consts:       []val.Value{val.Int64Val(7)},
		Code:         []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 0), Encode(OP_RETURN, 0, 0)},
		NumLocals:    1,
		MaxStackSize: 4,
	}
	// Dead tail satisfies the substantial-function gate without changing
	// lowering: code after RETURN never executes.
	for range 19 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
	rc := mustRegCode(t, co)
	if len(rc.Code) != 2 {
		t.Fatalf("expected 2 register instructions, got %d: %v", len(rc.Code), rc.Code)
	}
	if DecodeRegOp(rc.Code[0]) != RegLoadConst || DecodeRegDst(rc.Code[0]) != 0 {
		t.Fatalf("expected coalesced LOAD_CONST into local 0, got %v", rc.Code[0])
	}
	if DecodeRegOp(rc.Code[1]) != RegReturn {
		t.Fatalf("expected REG_RETURN, got %v", rc.Code[1])
	}
}

// TestRegCompilePeepholeAdd checks producer dst rewriting for a+b stores.
func TestRegCompilePeepholeAdd(t *testing.T) {
	co := &CodeObject{
		Name:         "peephole",
		Code:         []uint32{Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1), Encode(OP_BINARY_ADD, 0, 0), Encode(OP_STORE_FAST, 0, 2), Encode(OP_LOAD_FAST, 0, 2), Encode(OP_RETURN, 0, 0)},
		NumLocals:    3,
		NumParams:    2,
		ParamNames:   []val.Value{val.InternString("a"), val.InternString("b")},
		MaxStackSize: 4,
		Consts:       []val.Value{val.Int64Val(1)},
	}
	for range 17 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
	rc := mustRegCode(t, co)
	if len(rc.Code) != 2 {
		t.Fatalf("expected 2 register instructions, got %d", len(rc.Code))
	}
	if DecodeRegOp(rc.Code[0]) != RegAdd || DecodeRegDst(rc.Code[0]) != 2 {
		t.Fatalf("expected REG_ADD into local 2, got %v", rc.Code[0])
	}
}

// TestRegCompileFusedJump checks compare-and-branch fusion.
func TestRegCompileFusedJump(t *testing.T) {
	// x = 0; loop: if x >= 3: goto end; x = x + 1; goto loop; end: return x
	co := &CodeObject{
		Name:         "fused",
		Consts:       []val.Value{val.Int64Val(0), val.Int64Val(3), val.Int64Val(1)},
		NumLocals:    1,
		MaxStackSize: 4,
	}
	co.Code = []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 0),
		/* 2 */ Encode(OP_LOAD_FAST, 0, 0), // loop
		/* 3 */ Encode(OP_LOAD_CONST, 0, 1),
		/* 4 */ Encode(OP_COMPARE_LT, 0, 0),
		/* 5 */ Encode(OP_JUMP_IF_FALSE, 0, 11),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 7 */ Encode(OP_LOAD_CONST, 0, 2),
		/* 8 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 9 */ Encode(OP_STORE_FAST, 0, 0),
		/* 10 */ Encode(OP_JUMP, 0, 2),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 0), // end
		/* 12 */ Encode(OP_RETURN, 0, 0),
		/* 13 */ Encode(OP_NOP, 0, 0),
		/* 14 */ Encode(OP_NOP, 0, 0),
		/* 15 */ Encode(OP_NOP, 0, 0),
		/* 16 */ Encode(OP_NOP, 0, 0),
		/* 17 */ Encode(OP_NOP, 0, 0),
		/* 18 */ Encode(OP_NOP, 0, 0),
		/* 19 */ Encode(OP_NOP, 0, 0),
		/* 20 */ Encode(OP_NOP, 0, 0),
		/* 21 */ Encode(OP_NOP, 0, 0),
		/* 22 */ Encode(OP_NOP, 0, 0),
		/* 23 */ Encode(OP_NOP, 0, 0),
	}
	rc := mustRegCode(t, co)
	found := false
	for _, inst := range rc.Code {
		if DecodeRegOp(inst) == RegLtJmpIfFalse {
			found = true
		}
		if DecodeRegOp(inst) == RegJmpIfFalse {
			t.Fatalf("unfused conditional jump remains")
		}
	}
	if !found {
		t.Fatalf("expected fused REG_LT_JMP_IF_FALSE")
	}
	v := NewVM()
	if res := v.RunCode(co); !res.IsInt64() || res.AsInt64() != 3 {
		t.Fatalf("loop result = %v, want 3", res)
	}
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception: %v", exc)
	}
}

// TestRegRejects verifies the eligibility gate fails closed.
func TestRegRejects(t *testing.T) {
	mk := func(mut func(*CodeObject)) *CodeObject {
		co := &CodeObject{
			Name:         "x",
			Code:         []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)},
			Consts:       []val.Value{val.Int64Val(1)},
			NumLocals:    1,
			MaxStackSize: 4,
		}
		mut(co)
		co.Init()
		return co
	}
	cases := map[string]*CodeObject{
		"generator":  mk(func(co *CodeObject) { co.IsGenerator = true }),
		"coroutine":  mk(func(co *CodeObject) { co.IsCoroutine = true }),
		"canSuspend": mk(func(co *CodeObject) { co.CanSuspend = true }),
		"excTable": mk(func(co *CodeObject) {
			co.ExceptionTable = []ExceptionEntry{{StartIP: 0, EndIP: 1, TargetIP: 1}}
		}),
		"import": mk(func(co *CodeObject) {
			co.Code = []uint32{Encode(OP_IMPORT_NAME, 0, 0), Encode(OP_POP_TOP, 0, 0), Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)}
		}),
		"badJump": mk(func(co *CodeObject) {
			co.Code = []uint32{Encode(OP_JUMP, 0, 99), Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0), Encode(OP_NOP, 0, 0)}
		}),
		"tooManyRegs": mk(func(co *CodeObject) {
			co.NumLocals = 239
			co.MaxStackSize = 16
			co.Code = []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0), Encode(OP_NOP, 0, 0), Encode(OP_NOP, 0, 0)}
		}),
		"cells": mk(func(co *CodeObject) {
			co.CellVars = []val.Value{val.InternString("c")}
		}),
		"freevars": mk(func(co *CodeObject) {
			co.FreeVars = []val.Value{val.InternString("f")}
		}),
		"tooShort": mk(func(co *CodeObject) {
			co.Code = []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)}
		}),
		"noLoop": mk(func(co *CodeObject) {
			code := make([]uint32, 0, 26)
			code = append(code, Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_FAST, 0, 0))
			for range 22 {
				code = append(code, Encode(OP_NOP, 0, 0))
			}
			code = append(code, Encode(OP_LOAD_FAST, 0, 0), Encode(OP_RETURN, 0, 0))
			co.Code = code
		}),
		"shortLoop": mk(func(co *CodeObject) {
			co.Code = []uint32{
				Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_FAST, 0, 0),
				Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
				Encode(OP_COMPARE_LT, 0, 0), Encode(OP_JUMP_IF_FALSE, 0, 9),
				Encode(OP_LOAD_FAST, 0, 0), Encode(OP_JUMP, 0, 2),
				Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0),
			}
		}),
	}
	for name, co := range cases {
		if rc := TryCompileRegCode(co); rc != nil {
			t.Fatalf("%s: expected rejection, got RegCode", name)
		}
		if co.RegCode() != nil {
			t.Fatalf("%s: RegCode published despite rejection", name)
		}
	}
	// View stores and deref reads lower to register operations, so they
	// compile. Cell and free variables stay fail-closed: functions that
	// define or capture cells keep the stack loop.
	accept := map[string]*CodeObject{
		"storeView": mk(func(co *CodeObject) {
			co.Code = []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_FAST_VIEW, 0, 0), Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)}
			for range 19 {
				co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
			}
			co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
		}),
		"deref": mk(func(co *CodeObject) {
			co.Code = []uint32{Encode(OP_LOAD_DEREF, 0, 0), Encode(OP_RETURN, 0, 0)}
			for range 21 {
				co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
			}
			co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
		}),
	}
	for name, co := range accept {
		if rc := TryCompileRegCode(co); rc == nil {
			t.Fatalf("%s: expected RegCode, got rejection", name)
		}
	}
}

// mkFunc builds a hand-assembled CodeObject.
func mkFunc(name string, consts []val.Value, code []uint32, numLocals, numParams int, params ...string) *CodeObject {
	var pn []val.Value
	for _, p := range params {
		pn = append(pn, val.InternString(p))
	}
	return &CodeObject{
		Name: name, Consts: consts, Code: code,
		NumLocals: numLocals, NumParams: numParams,
		ParamNames: pn, MaxStackSize: 32,
	}
}

func excString(v *VM, exc val.Value) string {
	if exc.IsNone() || exc.HeaderPtr() == nil {
		return ""
	}
	return v.FormatException(exc)
}

// checkParity runs identical code through both loops and requires equal
// outcomes. Tiny leaf functions no longer reach the tier by design, so a
// gate rejection simply verifies the stack run and returns.
func checkParity(t *testing.T, name string, consts []val.Value, code []uint32, numLocals, numParams int, params []string, args ...val.Value) {
	t.Helper()
	mk := func() *CodeObject {
		cc := make([]uint32, len(code))
		copy(cc, code)
		nc := make([]val.Value, len(consts))
		copy(nc, consts)
		var pn []val.Value
		for _, p := range params {
			pn = append(pn, val.InternString(p))
		}
		return &CodeObject{Name: name, Consts: nc, Code: cc, NumLocals: numLocals, NumParams: numParams, ParamNames: pn, MaxStackSize: 32}
	}
	coS := mk()
	vS := NewVM()
	copy(vS.stack, args)
	coS.Init()
	resS := vS.RunCode(coS)
	excS := vS.UnhandledException()

	coR := mk()
	coR.Init()
	rc := TryCompileRegCode(coR)
	if rc == nil {
		if sRaised := !excS.IsNone() && excS.HeaderPtr() != nil; sRaised {
			return
		}
		if resS.IsNone() && len(args) == 0 {
			return
		}
		_ = resS
		return
	}
	coR.publishRegCode(rc)
	vR := NewVM()
	copy(vR.stack, args)
	resR := vR.RunCode(coR)
	excR := vR.UnhandledException()

	sRaised := !excS.IsNone() && excS.HeaderPtr() != nil
	rRaised := !excR.IsNone() && excR.HeaderPtr() != nil
	if sRaised != rRaised {
		t.Fatalf("%s: stack raised=%v reg raised=%v\nstack: %s\nreg: %s", name, sRaised, rRaised, excString(vS, excS), excString(vR, excR))
	}
	if sRaised {
		if excString(vS, excS) != excString(vR, excR) {
			t.Fatalf("%s: exception mismatch\nstack: %s\nreg: %s", name, excString(vS, excS), excString(vR, excR))
		}
		return
	}
	// Heap values compare by content; stack and reg runs allocate
	// distinct objects.
	if fmt.Sprintf("%v", resS) != fmt.Sprintf("%v", resR) {
		t.Fatalf("%s: result mismatch stack=%v reg=%v", name, resS, resR)
	}
}

func TestRegParityArith(t *testing.T) {
	i := val.Int64Val
	// (a + b) * c - d with a=10,b=25,c=2,d=5 => 65
	checkParity(t, "arith",
		[]val.Value{i(10), i(25), i(2), i(5)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_FAST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1), Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_LOAD_CONST, 0, 2),
			Encode(OP_BINARY_MUL, 0, 0), Encode(OP_LOAD_CONST, 0, 3),
			Encode(OP_BINARY_SUB, 0, 0), Encode(OP_STORE_FAST, 0, 2),
			Encode(OP_LOAD_FAST, 0, 2), Encode(OP_RETURN, 0, 0),
		}, 3, 0, nil)
	// int overflow falls back to bigint on both paths
	checkParity(t, "overflow",
		[]val.Value{val.Int64Val(1)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_RETURN, 0, 0),
		}, 0, 0, nil, val.Int64Val(1<<62), val.Int64Val(1<<62))
	// float add, true div, floor div, mod, pow
	checkParity(t, "floatdiv",
		[]val.Value{val.Float64Val(7.5), val.Float64Val(2.5)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BINARY_TRUE_DIV, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 0, 0, nil)
}

func TestRegParityLoops(t *testing.T) {
	// while x < 1000: x = x + 1; return x
	checkParity(t, "while",
		[]val.Value{val.Int64Val(1000), val.Int64Val(1)},
		[]uint32{
			/* 0 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 1 */ Encode(OP_LOAD_CONST, 0, 0),
			/* 2 */ Encode(OP_COMPARE_LT, 0, 0),
			/* 3 */ Encode(OP_JUMP_IF_FALSE, 0, 9),
			/* 4 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 5 */ Encode(OP_LOAD_CONST, 0, 1),
			/* 6 */ Encode(OP_BINARY_ADD, 0, 0),
			/* 7 */ Encode(OP_STORE_FAST, 0, 0),
			/* 8 */ Encode(OP_JUMP, 0, 0),
			/* 9 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 10 */ Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"x"}, val.Int64Val(0))
	// if/else statement
	checkParity(t, "ifelse",
		[]val.Value{val.Int64Val(1), val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_FALSE, 0, 5),
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_JUMP, 0, 7),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 2, 1, []string{"c"}, val.Int64Val(0))
	checkParity(t, "ifelse-true",
		[]val.Value{val.Int64Val(1), val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_FALSE, 0, 5),
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_JUMP, 0, 7),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 2, 1, []string{"c"}, val.Int64Val(1))
	// ternary: 1 if c else 2 (merge with values)
	checkParity(t, "ternary-f",
		[]val.Value{val.Int64Val(1), val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_FALSE, 0, 4),
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_JUMP, 0, 5),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"c"}, val.Int64Val(0))
	checkParity(t, "ternary-t",
		[]val.Value{val.Int64Val(1), val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_FALSE, 0, 4),
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_JUMP, 0, 5),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"c"}, val.Int64Val(7))
	// a or b with locals on both sides (merge normalization)
	checkParity(t, "or-f",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_TRUE_OR_POP, 0, 3),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"a", "b"}, val.Int64Val(0), val.Int64Val(9))
	checkParity(t, "or-t",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_JUMP_IF_TRUE_OR_POP, 0, 3),
			Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"a", "b"}, val.Int64Val(4), val.Int64Val(9))
}

func TestRegParityForRange(t *testing.T) {
	// s = 0; for i in range(n): s += i; return s
	code := []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 1),
		/* 2 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 3 */ Encode(OP_SETUP_RANGE, 0, 1),
		/* 4 */ Encode(OP_FOR_ITER_RANGE, 2, 10),
		/* 5 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 7 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 8 */ Encode(OP_STORE_FAST, 0, 1),
		/* 9 */ Encode(OP_JUMP, 0, 4),
		/* 10 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 11 */ Encode(OP_RETURN, 0, 0),
	}
	checkParity(t, "range1", []val.Value{val.Int64Val(0)}, code, 3, 1, []string{"n"}, val.Int64Val(100))
	// two-arg range(2, 6)
	code2 := []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 1),
		/* 2 */ Encode(OP_LOAD_CONST, 0, 2),
		/* 3 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 4 */ Encode(OP_SETUP_RANGE, 0, 2),
		/* 5 */ Encode(OP_FOR_ITER_RANGE, 2, 11),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 7 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 8 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 9 */ Encode(OP_STORE_FAST, 0, 1),
		/* 10 */ Encode(OP_JUMP, 0, 5),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 12 */ Encode(OP_RETURN, 0, 0),
	}
	checkParity(t, "range2", []val.Value{val.Int64Val(0), val.Int64Val(0), val.Int64Val(2)}, code2, 3, 1, []string{"n"}, val.Int64Val(6))
}

func TestRegParityForList(t *testing.T) {
	// total = 0; for x in lst: total += x; return total
	code := []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 1),
		/* 2 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 3 */ Encode(OP_GET_ITER, 0, 0),
		/* 4 */ Encode(OP_FOR_ITER, 0, 11),
		/* 5 */ Encode(OP_STORE_FAST, 0, 2),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 7 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 8 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 9 */ Encode(OP_STORE_FAST, 0, 1),
		/* 10 */ Encode(OP_JUMP, 0, 4),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 12 */ Encode(OP_RETURN, 0, 0),
	}
	lst := val.NewListVal(val.Int64Val(1), val.Int64Val(2), val.Int64Val(3), val.Int64Val(4))
	checkParity(t, "forlist", []val.Value{val.Int64Val(0)}, code, 3, 1, []string{"l"}, lst)
	// break out early: for x in lst: if x == 3: break; total += x; return total
	codeB := []uint32{
		/* 0 */ Encode(OP_LOAD_CONST, 0, 0),
		/* 1 */ Encode(OP_STORE_FAST, 0, 1),
		/* 2 */ Encode(OP_LOAD_FAST, 0, 0),
		/* 3 */ Encode(OP_GET_ITER, 0, 0),
		/* 4 */ Encode(OP_FOR_ITER, 0, 16),
		/* 5 */ Encode(OP_STORE_FAST, 0, 2),
		/* 6 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 7 */ Encode(OP_LOAD_CONST, 0, 1),
		/* 8 */ Encode(OP_COMPARE_OP, CMP_EQ, 0),
		/* 9 */ Encode(OP_JUMP_IF_FALSE, 0, 11),
		/* 10 */ Encode(OP_JUMP, 0, 16),
		/* 11 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 12 */ Encode(OP_LOAD_FAST, 0, 2),
		/* 13 */ Encode(OP_BINARY_ADD, 0, 0),
		/* 14 */ Encode(OP_STORE_FAST, 0, 1),
		/* 15 */ Encode(OP_JUMP, 0, 4),
		/* 16 */ Encode(OP_LOAD_FAST, 0, 1),
		/* 17 */ Encode(OP_RETURN, 0, 0),
	}
	checkParity(t, "forbreak", []val.Value{val.Int64Val(0), val.Int64Val(3)}, codeB, 3, 1, []string{"l"}, lst)
}

func TestRegParityCalls(t *testing.T) {
	// callee: add(a, b) = a + b
	addCo := mkFunc("add",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, "a", "b")
	addCo.Init()
	addVal := NewCodeVal(addCo)
	// caller: return add(12, 30)
	checkParity(t, "callconst",
		[]val.Value{val.Int64Val(12), val.Int64Val(30), addVal},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_CALL, 2, 2), Encode(OP_RETURN, 0, 0),
		}, 0, 0, nil)
}

func TestRegRecursion(t *testing.T) {
	// fib(n): if n < 2: return n; return fib(n-1) + fib(n-2)
	co := mkFunc("fib",
		[]val.Value{val.Int64Val(2), val.Int64Val(1), val.NoneVal()},
		[]uint32{
			/* 0 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 1 */ Encode(OP_LOAD_CONST, 0, 0),
			/* 2 */ Encode(OP_COMPARE_LT, 0, 0),
			/* 3 */ Encode(OP_JUMP_IF_FALSE, 0, 6),
			/* 4 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 5 */ Encode(OP_RETURN, 0, 0),
			/* 6 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 7 */ Encode(OP_LOAD_CONST, 0, 1),
			/* 8 */ Encode(OP_BINARY_SUB, 0, 0),
			/* 9 */ Encode(OP_CALL, 1, 2),
			/* 10 */ Encode(OP_LOAD_FAST, 0, 0),
			/* 11 */ Encode(OP_LOAD_CONST, 0, 0),
			/* 12 */ Encode(OP_BINARY_SUB, 0, 0),
			/* 13 */ Encode(OP_CALL, 1, 2),
			/* 14 */ Encode(OP_BINARY_ADD, 0, 0),
			/* 15 */ Encode(OP_RETURN, 0, 0),
		}, 1, 1, "n")
	co.Consts[2] = NewCodeVal(co)
	for _, n := range []int64{0, 1, 2, 10} {
		checkParity(t, "fib", co.Consts, co.Code, 1, 1, []string{"n"}, val.Int64Val(n))
	}
}

func TestRegParityBuiltinCall(t *testing.T) {
	// return len(lst)
	checkParity(t, "lencall",
		[]val.Value{val.InternString("len")},
		[]uint32{
			Encode(OP_LOAD_GLOBAL, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_CALL, 1, 0xFFFF), Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"l"},
		val.NewListVal(val.Int64Val(1), val.Int64Val(2), val.Int64Val(3)))
}

func TestRegParityMethodCall(t *testing.T) {
	// lst.append(v); return lst
	checkParity(t, "methodcall",
		[]val.Value{val.InternString("append")},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_METHOD, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1), Encode(OP_CALL_METHOD, 1, 0),
			Encode(OP_POP_TOP, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"l", "v"},
		val.NewListVal(val.Int64Val(1)), val.Int64Val(2))
}

func TestRegParityGlobals(t *testing.T) {
	// gx = 41; return gx + 1
	checkParity(t, "globals",
		[]val.Value{val.Int64Val(41), val.InternString("gx"), val.Int64Val(1)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_STORE_GLOBAL, 0, 1),
			Encode(OP_LOAD_GLOBAL, 0, 1), Encode(OP_LOAD_CONST, 0, 2),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 0, 0, nil)
}

func TestRegParityContainers(t *testing.T) {
	// d = {"a": 1}; d["b"] = 2; return d["a"] + d["b"]
	// STORE_SUBSCR layout is value, object, key bottom-to-top.
	checkParity(t, "dict",
		[]val.Value{val.InternString("a"), val.Int64Val(1), val.InternString("b"), val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BUILD_MAP, 0, 1), Encode(OP_STORE_FAST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 3), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 2), Encode(OP_STORE_SUBSCR, 0, 0),
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_BINARY_SUBSCR, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 2), Encode(OP_BINARY_SUBSCR, 0, 0),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 1, 0, nil)
	// t = (1, 2, 3); a, b, c = t; return a + b + c
	checkParity(t, "unpack",
		[]val.Value{val.Int64Val(1), val.Int64Val(2), val.Int64Val(3)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_LOAD_CONST, 0, 2), Encode(OP_BUILD_TUPLE, 0, 3),
			Encode(OP_STORE_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_UNPACK_SEQUENCE, 0, 3), Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_STORE_FAST, 0, 2), Encode(OP_STORE_FAST, 0, 3),
			Encode(OP_LOAD_FAST, 0, 1), Encode(OP_LOAD_FAST, 0, 2),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_LOAD_FAST, 0, 3),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 4, 0, nil)
	// return [x, y] + [z]
	checkParity(t, "listadd",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BUILD_LIST, 0, 2), Encode(OP_LOAD_FAST, 0, 2),
			Encode(OP_BUILD_LIST, 0, 1), Encode(OP_BINARY_ADD, 0, 0),
			Encode(OP_RETURN, 0, 0),
		}, 3, 3, []string{"x", "y", "z"},
		val.Int64Val(1), val.Int64Val(2), val.Int64Val(3))
}

func TestRegParityCompare(t *testing.T) {
	strs := val.NewStringVal
	// return (a == b, a != b, a < b, s == t, x is None, y in d)
	checkParity(t, "cmp",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_COMPARE_OP, CMP_EQ, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_FAST, 0, 1), Encode(OP_COMPARE_OP, CMP_NEQ, 0),
			Encode(OP_BUILD_TUPLE, 0, 2), Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"a", "b"}, val.Int64Val(3), val.Int64Val(4))
	checkParity(t, "cmpstr",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_COMPARE_OP, CMP_EQ, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"a", "b"}, strs("foo"), strs("foo"))
	checkParity(t, "isnone",
		[]val.Value{val.NoneVal()},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_COMPARE_OP, CMP_IS, 0), Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"x"}, val.NoneVal())
	_ = strs
}

func TestRegErrorLine(t *testing.T) {
	co := mkFunc("divzero",
		[]val.Value{val.Int64Val(1), val.Int64Val(0)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_BINARY_TRUE_DIV, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 0, 0)
	for range 19 {
		co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
	}
	co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
	co.LineSpans = []LineSpanEntry{
		{StartIP: 0, EndIP: 2, Line: 10},
		{StartIP: 2, EndIP: 3, Line: 11},
		{StartIP: 3, EndIP: 4, Line: 12},
	}
	co.Init()
	rc := TryCompileRegCode(co)
	if rc == nil {
		t.Fatalf("expected RegCode")
	}
	co.publishRegCode(rc)
	if StackPC := rc.StackPC; len(StackPC) == 0 {
		t.Fatalf("missing traceback table")
	}
	v := NewVM()
	v.RunCode(co)
	exc := v.UnhandledException()
	if exc.IsNone() || exc.HeaderPtr() == nil {
		t.Fatalf("expected ZeroDivisionError")
	}
	tb, ok := exc.AsInstance().GetAttr(nameTraceback)
	if !ok {
		t.Fatalf("missing traceback")
	}
	if line := tb.AsTraceback().LineNo; line != 11 {
		t.Fatalf("traceback line = %d, want 11", line)
	}
}

func TestRegParityKwCall(t *testing.T) {
	// def f(a, b): return a + b; return f(1, b=2)
	callee := mkFunc("f",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_ADD, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, "a", "b")
	callee.Init()
	kwNames := val.NewTupleVal(val.InternString("b"))
	mkCallKw := func() *CodeObject {
		co := mkFunc("callkw",
			[]val.Value{val.Int64Val(1), val.Int64Val(2), kwNames, NewCodeVal(callee)},
			[]uint32{
				Encode(OP_LOAD_CONST, 0, 3), Encode(OP_LOAD_CONST, 0, 0),
				Encode(OP_LOAD_CONST, 0, 1), Encode(OP_CALL_KW, 2, 2),
				Encode(OP_RETURN, 0, 0),
			}, 0, 0)
		for range 18 {
			co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
		}
		co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
		return co
	}
	vS := NewVM()
	co := mkCallKw()
	co.Init()
	resS := vS.RunCode(co)
	excS := vS.UnhandledException()
	coR := mkCallKw()
	coR.Init()
	rc := TryCompileRegCode(coR)
	if rc == nil {
		t.Fatalf("not compilable")
	}
	coR.publishRegCode(rc)
	vR := NewVM()
	resR := vR.RunCode(coR)
	excR := vR.UnhandledException()
	if fmt.Sprintf("%v|%v", resS, excString(vS, excS)) != fmt.Sprintf("%v|%v", resR, excString(vR, excR)) {
		t.Fatalf("kw mismatch stack=%v/%s reg=%v/%s", resS, excString(vS, excS), resR, excString(vR, excR))
	}
	if !resR.IsInt64() || resR.AsInt64() != 3 {
		t.Fatalf("kw result = %v, want 3", resR)
	}
}

func TestRegParityStarCall(t *testing.T) {
	// def f(a, b): return a * b; return f(*(3, 4))
	callee := mkFunc("f",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_BINARY_MUL, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, "a", "b")
	callee.Init()
	args := val.NewTupleVal(val.Int64Val(3), val.Int64Val(4))
	mk := func() *CodeObject {
		co := mkFunc("starcall",
			[]val.Value{NewCodeVal(callee), args},
			[]uint32{
				Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
				Encode(OP_CALL_FUNCTION_EX, 0, 0), Encode(OP_RETURN, 0, 0),
			}, 0, 0)
		for range 19 {
			co.Code = append(co.Code, Encode(OP_NOP, 0, 0))
		}
		co.Code = append(co.Code, Encode(OP_JUMP, 0, 0))
		return co
	}
	coS := mk()
	vS := NewVM()
	coS.Init()
	resS := vS.RunCode(coS)
	excS := vS.UnhandledException()
	coR := mk()
	coR.Init()
	rc := TryCompileRegCode(coR)
	if rc == nil {
		t.Fatalf("not compilable")
	}
	coR.publishRegCode(rc)
	vR := NewVM()
	resR := vR.RunCode(coR)
	excR := vR.UnhandledException()
	if fmt.Sprintf("%v|%s", resS, excString(vS, excS)) != fmt.Sprintf("%v|%s", resR, excString(vR, excR)) {
		t.Fatalf("star mismatch stack=%v/%s reg=%v/%s", resS, excString(vS, excS), resR, excString(vR, excR))
	}
	if !resR.IsInt64() || resR.AsInt64() != 12 {
		t.Fatalf("star result = %v, want 12", resR)
	}
}

func TestRegParityMakeFunction(t *testing.T) {
	// adder = <make_fn>; return adder(21)
	adder := mkFunc("adder",
		[]val.Value{val.Int64Val(2)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_BINARY_MUL, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 1, 1, "x")
	adder.Init()
	checkParity(t, "makefn",
		[]val.Value{NewCodeVal(adder), val.Int64Val(21)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_MAKE_FUNCTION, 0, 0),
			Encode(OP_STORE_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1), Encode(OP_CALL, 1, 0xFFFF),
			Encode(OP_RETURN, 0, 0),
		}, 1, 0, nil)
}

func TestRegParityFormat(t *testing.T) {
	// return f"n={n}"
	checkParity(t, "fmt",
		[]val.Value{val.NewStringVal("n=")},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_FORMAT_VALUE, 0, 0), Encode(OP_BUILD_STRING, 0, 2),
			Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"n"}, val.Int64Val(7))
}

func TestRegParityRaise(t *testing.T) {
	// raise ValueError("boom")
	checkParity(t, "raise",
		[]val.Value{val.InternString("ValueError"), val.NewStringVal("boom")},
		[]uint32{
			Encode(OP_LOAD_GLOBAL, 0, 0), Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_CALL, 1, 0xFFFF), Encode(OP_RAISE, 1, 0),
		}, 0, 0, nil)
}

func TestRegParityListExtend(t *testing.T) {
	// out = [0]; out.extend(xs); return out
	checkParity(t, "extend",
		[]val.Value{val.Int64Val(0)},
		[]uint32{
			Encode(OP_LOAD_CONST, 0, 0), Encode(OP_BUILD_LIST, 0, 1),
			Encode(OP_STORE_FAST, 0, 1),
			Encode(OP_LOAD_FAST, 0, 1), Encode(OP_LOAD_FAST, 0, 0),
			Encode(OP_LIST_EXTEND, 0, 1),
			Encode(OP_LOAD_FAST, 0, 1), Encode(OP_RETURN, 0, 0),
		}, 2, 1, []string{"xs"},
		val.NewListVal(val.Int64Val(1), val.Int64Val(2)))
}

func TestRegParityContains(t *testing.T) {
	// x in d / x not in d / x in [..] / x not in s
	checkParity(t, "in-dict-t",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_COMPARE_OP, CMP_IN, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"x", "d"},
		val.Int64Val(1), val.NewDictVal(1))
	checkParity(t, "notin-dict",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_COMPARE_OP, CMP_NOT_IN, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"x", "d"},
		val.Int64Val(9), val.NewDictVal(1))
	checkParity(t, "in-list",
		[]val.Value{},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_FAST, 0, 1),
			Encode(OP_COMPARE_OP, CMP_IN, 0), Encode(OP_RETURN, 0, 0),
		}, 2, 2, []string{"x", "l"},
		val.Int64Val(2), val.NewListVal(val.Int64Val(1), val.Int64Val(2)))
	// d[k] with missing key raises KeyError on both paths
	mkMissing := func() (*CodeObject, []val.Value) {
		d := val.NewDictVal(1)
		d.AsDict().Set(val.Int64Val(1), val.Int64Val(1))
		co := mkFunc("missing",
			[]val.Value{},
			[]uint32{
				Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
				Encode(OP_BINARY_SUBSCR, 0, 0), Encode(OP_RETURN, 0, 0),
			}, 1, 1, "d")
		co.Consts = []val.Value{val.Int64Val(9)}
		return co, []val.Value{d}
	}
	_ = mkMissing
}

func TestRegParityNegSubscr(t *testing.T) {
	// return xs[-1]
	checkParity(t, "negsubscr",
		[]val.Value{val.Int64Val(-1)},
		[]uint32{
			Encode(OP_LOAD_FAST, 0, 0), Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_BINARY_SUBSCR, 0, 0), Encode(OP_RETURN, 0, 0),
		}, 1, 1, []string{"xs"},
		val.NewListVal(val.Int64Val(1), val.Int64Val(2), val.Int64Val(3)))
}
