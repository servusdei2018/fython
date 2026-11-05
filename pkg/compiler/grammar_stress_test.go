package compiler

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestPEP701FStrings(t *testing.T) {
	src := `
x = 42
s1 = f"value: {x}"
s2 = f"nested: {f'{x * 2}'}"
s3 = f"math: {1 + 2 + 3}"
s4 = f"quote reuse: { 'hello ' + 'world' }"
s5 = f"{x=}"
`
	_, v := runPython(t, src)
	checkGlobalString(t, v, "s1", "value: 42")
	checkGlobalString(t, v, "s2", "nested: 84")
	checkGlobalString(t, v, "s3", "math: 6")
	checkGlobalString(t, v, "s4", "quote reuse: hello world")
	checkGlobalString(t, v, "s5", "x=42")
}

func TestPEP634PatternMatching(t *testing.T) {
	src := `
def match_test(val):
    match val:
        case 0:
            return "zero"
        case 1 | 2:
            return "one or two"
        case [first, *mid, last]:
            return f"seq: {first}, mid_len={len(mid)}, {last}"
        case {"type": "person", "name": name}:
            return f"person: {name}"
        case x if x > 100:
            return f"large: {x}"
        case _:
            return "other"

r0 = match_test(0)
r1 = match_test(2)
r2 = match_test([10, 20, 30, 40])
r3 = match_test({"type": "person", "name": "Alice", "extra": 123})
r4 = match_test(105)
r5 = match_test(-1)
`
	_, v := runPython(t, src)
	checkGlobalString(t, v, "r0", "zero")
	checkGlobalString(t, v, "r1", "one or two")
	checkGlobalString(t, v, "r2", "seq: 10, mid_len=2, 40")
	checkGlobalString(t, v, "r3", "person: Alice")
	checkGlobalString(t, v, "r4", "large: 105")
	checkGlobalString(t, v, "r5", "other")
}

func TestPEP634ClassPatternMatching(t *testing.T) {
	src := `
class Point:
    __match_args__ = ("x", "y")
    def __init__(self, x, y):
        self.x = x
        self.y = y

def match_point(p):
    match p:
        case Point(0, 0):
            return "origin"
        case Point(0, y):
            return f"y-axis: {y}"
        case Point(x, 0):
            return f"x-axis: {x}"
        case Point(x, y):
            return f"point: {x}, {y}"
        case _:
            return "not a point"

p0 = match_point(Point(0, 0))
p1 = match_point(Point(0, 5))
p2 = match_point(Point(7, 0))
p3 = match_point(Point(3, 4))
p4 = match_point(42)
`
	_, v := runPython(t, src)
	checkGlobalString(t, v, "p0", "origin")
	checkGlobalString(t, v, "p1", "y-axis: 5")
	checkGlobalString(t, v, "p2", "x-axis: 7")
	checkGlobalString(t, v, "p3", "point: 3, 4")
	checkGlobalString(t, v, "p4", "not a point")
}

func TestPEP695GenericsAndTypeAlias(t *testing.T) {
	src := `
type IntOrStr = int | str
type MyList[T] = list[T]

def identity[T](x: T) -> T:
    return x

class Container[T]:
    def __init__(self, val: T):
        self.val = val

res_id = identity(42)
c = Container("hello")
val_c = c.val
`
	_, v := runPython(t, src)
	checkGlobalInt(t, v, "res_id", 42)
	checkGlobalString(t, v, "val_c", "hello")

	aliasVal, ok := v.GetGlobal("IntOrStr")
	if !ok {
		t.Fatalf("IntOrStr global not found")
	}
	if !aliasVal.IsInstance() {
		t.Fatalf("expected IntOrStr to be TypeAliasType instance, got %v", aliasVal)
	}
}

func TestPEP649DeferredAnnotations(t *testing.T) {
	src := `
# Late-defined type referenced in annotation before its definition!
def process(item: ForwardType) -> ReturnType:
    return item

class ForwardType:
    pass

class ReturnType:
    pass

# Access annotations lazily
ann = process.__annotations__
param_type = ann["item"]
ret_type = ann["return"]
`
	_, v := runPython(t, src)
	fwdVal, ok := v.GetGlobal("ForwardType")
	if !ok {
		t.Fatalf("ForwardType global not found")
	}
	retVal, ok := v.GetGlobal("ReturnType")
	if !ok {
		t.Fatalf("ReturnType global not found")
	}
	paramType, ok := v.GetGlobal("param_type")
	if !ok {
		t.Fatalf("param_type not found in annotations")
	}
	if paramType != fwdVal {
		t.Fatalf("expected param_type == ForwardType, got %v vs %v", paramType, fwdVal)
	}
	retType, ok := v.GetGlobal("ret_type")
	if !ok {
		t.Fatalf("ret_type not found in annotations")
	}
	if retType != retVal {
		t.Fatalf("expected ret_type == ReturnType, got %v vs %v", retType, retVal)
	}
}

func TestExtendedUnpacking(t *testing.T) {
	src := `
a, *b, c = [1, 2, 3, 4, 5]
first, *rest = (10, 20, 30)

def add(x, y, z):
    return x + y + z

args = [10, 20]
kwargs = {"z": 30}
total = add(*args, **kwargs)

d1 = {"a": 1, "b": 2}
d2 = {"c": 3}
merged = {**d1, "d": 4, **d2}
len_merged = len(merged)
val_d = merged["d"]
`
	_, v := runPython(t, src)
	checkGlobalInt(t, v, "a", 1)
	checkGlobalInt(t, v, "c", 5)
	checkGlobalInt(t, v, "first", 10)
	checkGlobalInt(t, v, "total", 60)
	checkGlobalInt(t, v, "len_merged", 4)
	checkGlobalInt(t, v, "val_d", 4)

	bVal, ok := v.GetGlobal("b")
	if !ok || !bVal.IsList() {
		t.Fatalf("expected list for b, got %v", bVal)
	}
	if len(bVal.AsList().Items) != 3 {
		t.Fatalf("expected 3 items in b, got %d", len(bVal.AsList().Items))
	}
}

func TestPEP709InlinedComprehensions(t *testing.T) {
	src := `
numbers = [1, 2, 3, 4, 5]
doubled = [x * 2 for x in numbers if x % 2 == 1]
square_map = {x: x * x for x in [1, 2, 3]}
mod_set = {x % 3 for x in [1, 2, 3, 4, 5, 6]}
`
	_, v := runPython(t, src)
	dVal, ok := v.GetGlobal("doubled")
	if !ok || !dVal.IsList() {
		t.Fatalf("expected doubled to be list, got %v", dVal)
	}
	items := dVal.AsList().Items
	if len(items) != 3 || items[0].AsInt64() != 2 || items[1].AsInt64() != 6 || items[2].AsInt64() != 10 {
		t.Fatalf("unexpected doubled list: %v", items)
	}

	sMap, ok := v.GetGlobal("square_map")
	if !ok || !sMap.IsDict() {
		t.Fatalf("expected square_map to be dict, got %v", sMap)
	}
	if v, _ := sMap.AsDict().Get(val.Int64Val(3)); v.AsInt64() != 9 {
		t.Fatalf("expected square_map[3] == 9, got %v", v)
	}

	mSet, ok := v.GetGlobal("mod_set")
	if !ok || !mSet.IsSet() {
		t.Fatalf("expected mod_set to be set, got %v", mSet)
	}
	if mSet.AsSet().Len() != 3 {
		t.Fatalf("expected 3 items in mod_set, got %d", mSet.AsSet().Len())
	}
}

func TestPython314OperatorsWalrusAssertDelLambda(t *testing.T) {
	src := `
# Bitwise & Shift
b_and = 0b1101 & 0b1011
b_or  = 0b1100 | 0b0011
b_xor = 0b1100 ^ 0b1010
b_inv = ~0
b_shl = 1 << 4
b_shr = 16 >> 2

# Walrus :=
walrus_val = 0
if (n := 42) > 20:
    walrus_val = n

# Assert
assert walrus_val == 42, "walrus failed"

# Lambda
fn = lambda x, y=10: x * y
lambda_res = fn(5)

# Del
to_del = 999
del to_del

d = {"k": 100}
del d["k"]
has_k = "k" in d
`
	_, v := runPython(t, src)
	checkGlobalInt(t, v, "b_and", 0b1001)
	checkGlobalInt(t, v, "b_or", 0b1111)
	checkGlobalInt(t, v, "b_xor", 0b0110)
	checkGlobalInt(t, v, "b_inv", -1)
	checkGlobalInt(t, v, "b_shl", 16)
	checkGlobalInt(t, v, "b_shr", 4)
	checkGlobalInt(t, v, "walrus_val", 42)
	checkGlobalInt(t, v, "lambda_res", 50)
	checkGlobalBool(t, v, "has_k", false)

	if _, ok := v.GetGlobal("to_del"); ok {
		t.Fatalf("expected to_del to be deleted from globals")
	}
}

func TestPEP654ExceptionGroup(t *testing.T) {
	src := `
caught_type = ""
try:
    raise ExceptionGroup("msg", [ValueError("bad val"), TypeError("bad type")])
except* ValueError as eg:
    caught_type = "ValueError"
`
	_, v := runPython(t, src)
	checkGlobalString(t, v, "caught_type", "ValueError")
}

func checkGlobalString(t *testing.T, v *vm.VM, name, expected string) {
	t.Helper()
	got, ok := v.GetGlobal(name)
	if !ok {
		t.Fatalf("global '%s' not found", name)
	}
	if got.AsString() != expected {
		t.Fatalf("global '%s' = %q, want %q", name, got.AsString(), expected)
	}
}

func checkGlobalInt(t *testing.T, v *vm.VM, name string, expected int64) {
	t.Helper()
	got, ok := v.GetGlobal(name)
	if !ok {
		t.Fatalf("global '%s' not found", name)
	}
	if !got.IsInt64() || got.AsInt64() != expected {
		t.Fatalf("global '%s' = %v, want %d", name, got, expected)
	}
}

func checkGlobalBool(t *testing.T, v *vm.VM, name string, expected bool) {
	t.Helper()
	got, ok := v.GetGlobal(name)
	if !ok {
		t.Fatalf("global '%s' not found", name)
	}
	if !got.IsBool() || got.AsBool() != expected {
		t.Fatalf("global '%s' = %v, want %v", name, got, expected)
	}
}
