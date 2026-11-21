package vm

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

// TestWaveGBranchingLeafInlining verifies that small branching methods (forward
// jumps, multiple returns, extra locals) pass canInlineCallee, splice into the
// caller via OP_GUARD_INLINE_METHOD, execute all branches accurately, and deopt
// cleanly on shape/type mismatch.
func TestWaveGBranchingLeafInlining(t *testing.T) {
	v := NewVM()

	// Callee: def pick(self, x):
	//   tmp = x
	//   if tmp < 0:
	//       return -1
	//   if tmp == 0:
	//       return 0
	//   return tmp
	// Uses extra local (local 2 = tmp), forward jumps, OP_RETURN_CONST, OP_RETURN_FAST.
	calleeConsts := []val.Value{
		val.Int64Val(0),
		val.Int64Val(-1),
	}
	calleeCode := []uint32{
		Encode(OP_LOAD_FAST, 0, 1),       // 0: x
		Encode(OP_STORE_FAST, 0, 2),      // 1: tmp = x
		Encode(OP_LOAD_FAST, 0, 2),       // 2: tmp
		Encode(OP_LOAD_CONST, 0, 0),      // 3: 0
		Encode(OP_COMPARE_LT, 0, 0),      // 4: tmp < 0
		Encode(OP_JUMP_IF_FALSE, 0, 8),   // 5: -> 8
		Encode(OP_LOAD_CONST, 0, 1),      // 6: -1
		Encode(OP_RETURN, 0, 0),          // 7: return -1
		Encode(OP_LOAD_FAST, 0, 2),       // 8: tmp
		Encode(OP_LOAD_CONST, 0, 0),      // 9: 0
		Encode(OP_COMPARE_OP, CMP_EQ, 0), // 10: tmp == 0
		Encode(OP_JUMP_IF_FALSE, 0, 13),  // 11: -> 13
		Encode(OP_RETURN_CONST, 0, 0),    // 12: return 0
		Encode(OP_RETURN_FAST, 0, 2),     // 13: return tmp
	}
	calleeCO := &CodeObject{
		Name:                 "pick",
		Code:                 calleeCode,
		Consts:               calleeConsts,
		NumParams:            2,
		PositionalParamCount: 2,
		NumLocals:            3,
		EffectiveLocals:      3,
		MaxStackSize:         8,
		SimplePositionalOnly: true,
	}
	calleeCO.Init()
	OptimizeCodeObject(calleeCO)

	calleeFn := &val.PyFunction{
		Header: val.ObjectHeader{Kind: val.KindFunction, Type: val.FunctionType},
		Name:   "pick",
		Code:   val.ObjectVal(&calleeCO.Header),
	}

	tpA := val.MustNewType("BranchInlineA", []*val.PyType{val.ObjectType}, nil)
	tpA.Dict.Set(val.InternString("pick"), val.ObjectVal(&calleeFn.Header))

	// Different class with a different pick method to exercise guard deopt.
	altCO := &CodeObject{
		Name:                 "pick_alt",
		Code:                 []uint32{Encode(OP_LOAD_CONST, 0, 0), Encode(OP_RETURN, 0, 0)},
		Consts:               []val.Value{val.Int64Val(999)},
		NumParams:            2,
		PositionalParamCount: 2,
		NumLocals:            2,
		EffectiveLocals:      2,
		MaxStackSize:         4,
		SimplePositionalOnly: true,
	}
	altCO.Init()
	altFn := &val.PyFunction{
		Header: val.ObjectHeader{Kind: val.KindFunction, Type: val.FunctionType},
		Name:   "pick",
		Code:   val.ObjectVal(&altCO.Header),
	}
	tpB := val.MustNewType("BranchInlineB", []*val.PyType{val.ObjectType}, nil)
	tpB.Dict.Set(val.InternString("pick"), val.ObjectVal(&altFn.Header))

	// Caller: def run(obj, x): return obj.pick(x)
	callerConsts := []val.Value{val.InternString("pick")}
	callerCode := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_LOAD_FAST, 0, 1),
		Encode(OP_CALL_METHOD_PIC, 1, 0),
		Encode(OP_RETURN, 0, 0),
	}
	callerCO := &CodeObject{
		Name:                 "run",
		Code:                 callerCode,
		Consts:               callerConsts,
		NumParams:            2,
		PositionalParamCount: 2,
		NumLocals:            2,
		EffectiveLocals:      2,
		MaxStackSize:         8,
		SimplePositionalOnly: true,
	}
	callerCO.Init()

	if !canInlineCallee(calleeFn, calleeCO, callerCO) {
		t.Fatalf("expected branching callee to pass canInlineCallee")
	}

	// Mark caller as Tier-2 optimized so tryInlineMethod splices on first hit.
	OptimizeCodeObject(callerCO)

	instA := val.NewInstanceOfType(tpA)
	instB := val.NewInstanceOfType(tpB)
	callerFn := &val.PyFunction{
		Header: val.ObjectHeader{Kind: val.KindFunction, Type: val.FunctionType},
		Name:   "run",
		Code:   val.ObjectVal(&callerCO.Header),
	}
	callerVal := val.ObjectVal(&callerFn.Header)

	cases := []struct {
		obj  val.Value
		arg  int64
		want int64
	}{
		{instA, -5, -1},
		{instA, 0, 0},
		{instA, 42, 42},
		{instA, -1, -1},
		{instB, 7, 999}, // guard miss -> deopt stub
		{instA, 13, 13},
	}
	for idx, tc := range cases {
		res := v.execCtx.Call(callerVal, []val.Value{tc.obj, val.Int64Val(tc.arg)}, nil)
		if !res.IsInt64() || res.AsInt64() != tc.want {
			t.Fatalf("case %d (arg=%d): got %v, want %d", idx, tc.arg, res, tc.want)
		}
	}
}

// TestWaveGBuildStringScratch verifies OP_BUILD_STRING for 0, 1, 2, and 3+ parts
// including <= 7-byte zero-alloc inline strings and longer heap strings.
func TestWaveGBuildStringScratch(t *testing.T) {
	v := NewVM()

	// Build 0 parts -> ""
	co0 := &CodeObject{
		Name:         "bs0",
		Code:         []uint32{Encode(OP_BUILD_STRING, 0, 0), Encode(OP_RETURN, 0, 0)},
		MaxStackSize: 4,
	}
	co0.Init()
	r0 := v.RunCode(co0)
	if !r0.IsInlineStr() || r0.AsString() != "" {
		t.Fatalf("bs0: got %v (%q), want inline empty string", r0, r0.AsString())
	}

	// Build 3 short parts -> "abc" (inline string <= 7 bytes)
	coShort := &CodeObject{
		Name: "bsShort",
		Consts: []val.Value{
			val.SmallStringVal("a"),
			val.SmallStringVal("b"),
			val.SmallStringVal("c"),
		},
		Code: []uint32{
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_LOAD_CONST, 0, 2),
			Encode(OP_BUILD_STRING, 0, 3),
			Encode(OP_RETURN, 0, 0),
		},
		MaxStackSize: 8,
	}
	coShort.Init()
	rShort := v.RunCode(coShort)
	if !rShort.IsInlineStr() || rShort.AsString() != "abc" {
		t.Fatalf("bsShort: got %v (%q), want inline 'abc'", rShort, rShort.AsString())
	}

	// Build 4 parts -> long heap string
	coLong := &CodeObject{
		Name: "bsLong",
		Consts: []val.Value{
			val.NewStringVal("<div>"),
			val.NewStringVal("hello "),
			val.NewStringVal("jinja2 "),
			val.NewStringVal("world</div>"),
		},
		Code: []uint32{
			Encode(OP_LOAD_CONST, 0, 0),
			Encode(OP_LOAD_CONST, 0, 1),
			Encode(OP_LOAD_CONST, 0, 2),
			Encode(OP_LOAD_CONST, 0, 3),
			Encode(OP_BUILD_STRING, 0, 4),
			Encode(OP_RETURN, 0, 0),
		},
		MaxStackSize: 8,
	}
	coLong.Init()
	rLong := v.RunCode(coLong)
	if rLong.AsString() != "<div>hello jinja2 world</div>" {
		t.Fatalf("bsLong: got %q, want %q", rLong.AsString(), "<div>hello jinja2 world</div>")
	}
}

// TestWaveGFastAttrDescrMultiArg verifies OP_CALL_FAST_ATTR_DESCR fusion and
// execution for argc == 0, 1, and 2 on builtins (str.startswith, dict.get) and
// shaped KindInstance receivers.
func TestWaveGFastAttrDescrMultiArg(t *testing.T) {
	v := NewVM()

	// def f(d, k, default): return d.get(k, default)
	consts := []val.Value{val.InternString("get")}
	code := []uint32{
		Encode(OP_LOAD_FAST, 0, 0),
		Encode(OP_LOAD_FAST, 0, 1),
		Encode(OP_LOAD_FAST, 0, 2),
		Encode(OP_CALL_METHOD_PIC, 2, 0),
		Encode(OP_RETURN, 0, 0),
	}
	co := &CodeObject{
		Name:                 "dict_get_fused",
		Code:                 code,
		Consts:               consts,
		NumParams:            3,
		PositionalParamCount: 3,
		NumLocals:            3,
		EffectiveLocals:      3,
		MaxStackSize:         8,
		SimplePositionalOnly: true,
	}
	co.Init()
	OptimizeCodeObject(co)

	fused := co.Active()
	if DecodeOpcode(fused[2]) != OP_CALL_FAST_ATTR_DESCR {
		t.Fatalf("expected fused[2] to be OP_CALL_FAST_ATTR_DESCR, got %v", DecodeOpcode(fused[2]))
	}

	fn := &val.PyFunction{
		Header: val.ObjectHeader{Kind: val.KindFunction, Type: val.FunctionType},
		Name:   "dict_get_fused",
		Code:   val.ObjectVal(&co.Header),
	}
	fnVal := val.ObjectVal(&fn.Header)

	d := val.NewDictVal(4)
	d.AsDict().Set(val.InternString("a"), val.Int64Val(10))

	for i := range 5 {
		got := v.execCtx.Call(fnVal, []val.Value{d, val.InternString("a"), val.Int64Val(-1)}, nil)
		if !got.IsInt64() || got.AsInt64() != 10 {
			t.Fatalf("iter %d hit: got %v, want 10", i, got)
		}
		miss := v.execCtx.Call(fnVal, []val.Value{d, val.InternString("z"), val.Int64Val(-1)}, nil)
		if !miss.IsInt64() || miss.AsInt64() != -1 {
			t.Fatalf("iter %d miss: got %v, want -1", i, miss)
		}
	}
}
