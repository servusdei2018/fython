package compiler_test

import (
	"math"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func hasOpcode(code []uint32, target vm.Opcode) bool {
	for _, inst := range code {
		if vm.DecodeOpcode(inst) == target {
			return true
		}
	}
	return false
}

func runPy(t *testing.T, src string) *vm.VM {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	eng := vm.NewEngine()
	v := eng.NewVM()
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception: %v", exc)
	}
	return v
}

func TestConstantFoldingAndEdgeCases(t *testing.T) {
	// 1. Unary & binary folding, -0.0 sign preservation, bool promotion, reversed repeat.
	src := `
def folded():
    a = -1 + 2 * 3
    b = -7 // 2
    c = -7 % 2
    d = ~5
    e = not False
    f = (True + True) * 3
    g = "ab" * 3
    h = 3 * "cd"
    i = "x" * -1
    j = b"xy" * 2
    k = "hello " "world"
    pos_zero = 0.0
    neg_zero = -0.0
    return (a, b, c, d, e, f, g, h, i, j, k, pos_zero, neg_zero)

res = folded()
assert res[0] == 5
assert res[1] == -4
assert res[2] == 1
assert res[3] == -6
assert res[4] is True
assert res[5] == 6 and type(res[5]) is int
assert res[6] == "ababab"
assert res[7] == "cdcdcd"
assert res[8] == ""
assert res[9] == b"xyxy"
assert res[10] == "hello world"
import math
assert math.copysign(1.0, res[11]) == 1.0
assert math.copysign(1.0, res[12]) == -1.0
`
	v := runPy(t, src)
	fnVal, ok := v.GetGlobal("folded")
	if !ok {
		t.Fatal("missing folded")
	}
	co := vm.AsCodeObject(fnVal.AsFunction().Code)
	for _, op := range []vm.Opcode{
		vm.OP_BINARY_ADD, vm.OP_BINARY_MUL, vm.OP_BINARY_FLOOR_DIV,
		vm.OP_BINARY_MOD, vm.OP_UNARY_NEGATIVE, vm.OP_UNARY_INVERT, vm.OP_UNARY_NOT,
	} {
		if hasOpcode(co.Code, op) {
			t.Fatalf("expected %v to be folded away in folded()", op)
		}
	}
	// Verify +0.0 and -0.0 did not deduplicate to the same constant slot.
	foundPosZero, foundNegZero := false, false
	for _, c := range co.Consts {
		if c.IsFloat64() && c.AsFloat64() == 0 {
			if math.Signbit(c.AsFloat64()) {
				foundNegZero = true
			} else {
				foundPosZero = true
			}
		}
	}
	if !foundPosZero || !foundNegZero {
		t.Fatalf("expected distinct +0.0 and -0.0 constants, got pos=%v neg=%v", foundPosZero, foundNegZero)
	}

	// 2. Safety caps: div-by-zero stays runtime, huge shift/repeat not folded.
	coErr, err := compiler.CompileString(`
def div_zero():
    return 1 // 0

def huge_shift():
    return 1 << 100

def huge_repeat():
    return "a" * 10000
`)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	for _, c := range coErr.Consts {
		if c.Kind() != val.KindCode {
			continue
		}
		sub := vm.AsCodeObject(c)
		switch sub.Name {
		case "div_zero":
			if !hasOpcode(sub.Code, vm.OP_BINARY_FLOOR_DIV) {
				t.Fatal("div_zero must keep OP_BINARY_FLOOR_DIV for runtime ZeroDivisionError")
			}
		case "huge_shift":
			if !hasOpcode(sub.Code, vm.OP_BINARY_LSHIFT) {
				t.Fatal("huge_shift must keep OP_BINARY_LSHIFT")
			}
		case "huge_repeat":
			if !hasOpcode(sub.Code, vm.OP_BINARY_MUL) {
				t.Fatal("huge_repeat must keep OP_BINARY_MUL")
			}
		}
	}

	// 3. Dead-branch scope/generator preservation: if False: x = 1 keeps x in
	//    local scope (never falls back to global x), and if False: yield 1 keeps CO_GENERATOR.
	vScope := runPy(t, `
x = 123

def dead_assign():
    if False:
        x = 1
    return x

def dead_gen():
    if False:
        yield 1
    return 42

def pure_fold():
    if True:
        return 10
    else:
        return 20

g = dead_gen()
assert list(g) == []
assert pure_fold() == 10
`)
	daFn, ok := vScope.GetGlobal("dead_assign")
	if !ok {
		t.Fatal("missing dead_assign")
	}
	daCO := vm.AsCodeObject(daFn.AsFunction().Code)
	if hasOpcode(daCO.Code, vm.OP_LOAD_GLOBAL) {
		t.Fatal("dead_assign must keep x as a local variable, not OP_LOAD_GLOBAL")
	}
	if daCO.NumLocals < 1 || len(daCO.VarNames) == 0 || daCO.VarNames[0].AsString() != "x" {
		t.Fatalf("dead_assign must reserve local slot for x, got NumLocals=%d VarNames=%v", daCO.NumLocals, daCO.VarNames)
	}
	dgFn, ok := vScope.GetGlobal("dead_gen")
	if !ok {
		t.Fatal("missing dead_gen")
	}
	dgCO := vm.AsCodeObject(dgFn.AsFunction().Code)
	if !dgCO.IsGenerator {
		t.Fatal("dead_gen must preserve IsGenerator=true when yield is in dead if False branch")
	}
}

func TestConstTupleAndSetHoisting(t *testing.T) {
	v := runPy(t, `
def make_tuple():
    return (1, "two", (3, 4), b"five")

def loop_const_list():
    total = 0
    for x in [10, 20, 30, 40]:
        total += x
    return total

def check_in(x):
    return x in ("a", "b", "c", (1, 2))

def check_not_in(x):
    return x not in [100, 200, 300]

assert make_tuple() == (1, "two", (3, 4), b"five")
assert loop_const_list() == 100
assert check_in("b") is True
assert check_in((1, 2)) is True
assert check_in("z") is False
assert check_not_in(200) is False
assert check_not_in(999) is True
`)
	for _, tc := range []struct {
		name      string
		forbidden vm.Opcode
		wantKind  val.Kind
	}{
		{"make_tuple", vm.OP_BUILD_TUPLE, val.KindTuple},
		{"loop_const_list", vm.OP_BUILD_LIST, val.KindTuple},
		{"check_in", vm.OP_BUILD_TUPLE, val.KindSet},
		{"check_not_in", vm.OP_BUILD_LIST, val.KindSet},
	} {
		fnVal, ok := v.GetGlobal(tc.name)
		if !ok {
			t.Fatalf("missing %s", tc.name)
		}
		co := vm.AsCodeObject(fnVal.AsFunction().Code)
		if hasOpcode(co.Code, tc.forbidden) {
			t.Fatalf("%s should have hoisted constant instead of emitting %v", tc.name, tc.forbidden)
		}
		foundKind := false
		for _, c := range co.Consts {
			if c.Kind() == tc.wantKind {
				foundKind = true
				break
			}
		}
		if !foundKind {
			t.Fatalf("%s missing hoisted constant of kind %v", tc.name, tc.wantKind)
		}
	}
}

func TestPeepholeFusionAndTier2Idempotency(t *testing.T) {
	v := runPy(t, `
class Box:
    def __init__(self, v):
        self.v = v

def f(a, b, box):
    x = a
    y = x + b
    if a < b:
        return box.v
    return y

def with_try(x):
    try:
        if x:
            return 1
        return 2
    except Exception:
        return 3

assert f(1, 2, Box(99)) == 99
assert f(5, 2, Box(99)) == 7
assert with_try(True) == 1
assert with_try(False) == 2
`)
	fnVal, ok := v.GetGlobal("f")
	if !ok {
		t.Fatal("missing f")
	}
	co := vm.AsCodeObject(fnVal.AsFunction().Code)
	if !hasOpcode(co.Code, vm.OP_LOAD_FAST_ATTR) {
		t.Fatal("expected compile-time OP_LOAD_FAST_ATTR in f()")
	}
	if !hasOpcode(co.Code, vm.OP_RETURN_FAST) {
		t.Fatal("expected compile-time OP_RETURN_FAST in f()")
	}
	if !hasOpcode(co.Code, vm.OP_COMPARE_JUMP_IF_FALSE) {
		t.Fatal("expected compile-time OP_COMPARE_JUMP_IF_FALSE in f()")
	}
	// Verify OP_LOAD_FAST_ATTR is immediately followed by OP_NOP.
	for i, inst := range co.Code {
		if vm.DecodeOpcode(inst) == vm.OP_LOAD_FAST_ATTR {
			if i+1 >= len(co.Code) || vm.DecodeOpcode(co.Code[i+1]) != vm.OP_NOP {
				t.Fatalf("OP_LOAD_FAST_ATTR at %d must preserve trailing OP_NOP", i)
			}
		}
	}
	// Verify Tier-2 OptimizeCodeObject is idempotent over compile-time fused bytecode.
	before := append([]uint32(nil), co.Active()...)
	vm.OptimizeCodeObject(co)
	after1 := append([]uint32(nil), co.Active()...)
	vm.OptimizeCodeObject(co)
	after2 := co.Active()
	if len(after1) != len(after2) || len(after1) != len(before) {
		t.Fatalf("OptimizeCodeObject changed code length: before=%d after1=%d after2=%d", len(before), len(after1), len(after2))
	}
	for i := range after1 {
		if after1[i] != after2[i] {
			t.Fatalf("OptimizeCodeObject not idempotent at ip %d", i)
		}
	}
}
