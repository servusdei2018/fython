package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestJSONDumpsParityAndReentrancy(t *testing.T) {
	eng, v := runPySource(t, `
import json
import math

# 1. NaN / Infinity with allow_nan=True (default) and allow_nan=False
assert json.dumps(math.nan) == "NaN"
assert json.dumps(math.inf) == "Infinity"
assert json.dumps(-math.inf) == "-Infinity"
assert json.dumps([1, math.nan, math.inf, -math.inf]) == "[1, NaN, Infinity, -Infinity]"

raised = False
try:
    json.dumps(math.nan, allow_nan=False)
except ValueError:
    raised = True
assert raised, "expected ValueError for NaN with allow_nan=False"

raised_inf = False
try:
    json.dumps(math.inf, allow_nan=False)
except ValueError:
    raised_inf = True
assert raised_inf, "expected ValueError for Inf with allow_nan=False"

# 2. ensure_ascii=True (default) vs ensure_ascii=False, control chars, long spans
assert json.dumps("é") == '"\\u00e9"'
assert json.dumps("\u1234") == '"\\u1234"'
assert json.dumps("😀") == '"\\ud83d\\ude00"'
assert json.dumps("café 😀", ensure_ascii=False) == '"café 😀"'
assert json.dumps("a\x00\x08\x0c\n\r\t\x1f\"\\b") == '"a\\u0000\\b\\f\\n\\r\\t\\u001f\\"\\\\b"'

long_clean = "x" * 75
assert json.dumps(long_clean) == '"' + long_clean + '"'
long_esc = ("y" * 40) + "\n" + ("z" * 40) + "é"
assert json.dumps(long_esc) == '"' + ("y" * 40) + '\\n' + ("z" * 40) + '\\u00e9"'
assert json.dumps(long_esc, ensure_ascii=False) == '"' + ("y" * 40) + '\\n' + ("z" * 40) + 'é"'

# 3. separators, sort_keys, indent, and primitive non-str keys
assert json.dumps({"b": 1, "a": [2, 3]}, sort_keys=True, separators=(",", ":")) == '{"a":[2,3],"b":1}'
assert json.dumps({1: "one", False: "f", None: "n"}, sort_keys=False) == '{"1": "one", "false": "f", "null": "n"}'
assert json.dumps({"b": [1, 2], "a": 3}, sort_keys=True, indent=2) == '{\n  "a": 3,\n  "b": [\n    1,\n    2\n  ]\n}'

# 4. Re-entrant default= callback invoking json.dumps recursively
class Box:
    def __init__(self, v):
        self.v = v

def enc(obj):
    if isinstance(obj, Box):
        return {"inner": json.dumps({"nested": obj.v, "arr": [1, 2, 3]}), "raw": obj.v}
    raise TypeError("unexpected")

res = json.dumps({"top": Box("hello_world_12345"), "tail": [10, 20]}, default=enc, separators=(",", ":"))
assert res == '{"top":{"inner":"{\\"nested\\": \\"hello_world_12345\\", \\"arr\\": [1, 2, 3]}","raw":"hello_world_12345"},"tail":[10,20]}'
`)
	defer eng.ReleaseVM(v)
}

func TestVarKWSafeSubscriptAndTupleSlicePooling(t *testing.T) {
	eng, v := runPySource(t, `
class EvilKey:
    def __init__(self, name):
        self.name = name
    def __hash__(self):
        return hash(self.name)
    def __eq__(self, other):
        return False

captured = []
class CaptureKey:
    def __hash__(self):
        return 0
    def __eq__(self, other):
        return False

def safe_read(**kwargs):
    x = kwargs["a"] if "a" in kwargs else 0
    if not kwargs:
        return -1
    return x

assert safe_read(a=42, b=7) == 42
assert safe_read() == -1

def unsafe_custom_key(k, **kwargs):
    # k is a local (not a scalar constant), so VarKWSafe must stay false.
    return kwargs.get(k, 99)

assert unsafe_custom_key(EvilKey("a"), a=1) == 99

def unsafe_escape_subscr(**kwargs):
    captured.append(kwargs)
    return kwargs["a"]

assert unsafe_escape_subscr(a=5) == 5
assert captured[0]["a"] == 5

# Ephemeral tuple slice followed by unpacking and *args forwarding
def pair_sum(x, y):
    return x * 10 + y

def slice_unpack_and_call(t):
    a, b = t[1:3]
    c = pair_sum(*t[2:4])
    return a + b + c

assert slice_unpack_and_call((5, 6, 7, 8, 9)) == 6 + 7 + 78
`)
	defer eng.ReleaseVM(v)

	src := `
def safe_fn(**kwargs):
    if not kwargs:
        return 0
    if "k" in kwargs:
        return kwargs["k"]
    return 1

def unsafe_fn(k, **kwargs):
    return kwargs[k]
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v2 := eng.NewVM()
	defer eng.ReleaseVM(v2)
	v2.RunCode(co)

	safeVal, _ := v2.Globals().Get(v2.Intern("safe_fn"))
	safeCO := vm.AsCodeObject(safeVal.AsFunction().Code)
	if !safeCO.VarKWSafe {
		t.Fatalf("expected safe_fn.VarKWSafe == true")
	}

	unsafeVal, _ := v2.Globals().Get(v2.Intern("unsafe_fn"))
	unsafeCO := vm.AsCodeObject(unsafeVal.AsFunction().Code)
	if unsafeCO.VarKWSafe {
		t.Fatalf("expected unsafe_fn.VarKWSafe == false for non-const subscript key")
	}
}
