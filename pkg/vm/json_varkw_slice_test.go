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

func TestJSONLoadsPoolingHooksAndParity(t *testing.T) {
	eng, v := runPySource(t, `
import json
import math

# 1. Exact-sized dicts across all size tiers (0, 1..5, 6..10, >10) + duplicate keys
d0 = json.loads("{}")
assert d0 == {} and len(d0) == 0

d3 = json.loads('{"a": 1, "b": 2, "c": 3}')
assert d3 == {"a": 1, "b": 2, "c": 3} and len(d3) == 3

d8 = json.loads('{"k1":1,"k2":2,"k3":3,"k4":4,"k5":5,"k6":6,"k7":7,"k8":8}')
assert len(d8) == 8 and d8["k8"] == 8

d15 = json.loads('{"k01":1,"k02":2,"k03":3,"k04":4,"k05":5,"k06":6,"k07":7,"k08":8,"k09":9,"k10":10,"k11":11,"k12":12,"k13":13,"k14":14,"k15":15}')
assert len(d15) == 15 and d15["k15"] == 15

dup = json.loads('{"x": 1, "x": 2, "y": 3, "x": 4}')
assert dup == {"x": 4, "y": 3} and len(dup) == 2

# 2. Nested arrays and long unescaped strings (SWAR/SIMD span path)
long_str = "abcdefghij" * 8
nested = json.loads('[[], [1, 2, 3], {"nested_key_longer_than_seven": "' + long_str + '"}, "' + long_str + '"]')
assert nested[0] == []
assert nested[1] == [1, 2, 3]
assert nested[2]["nested_key_longer_than_seven"] == long_str
assert nested[3] == long_str

# 3. Interned keys reuse across repeated objects (<= 32 bytes)
objs = json.loads('[{"long_shared_key_12": 1}, {"long_shared_key_12": 2}]')
k_a = list(objs[0].keys())[0]
k_b = list(objs[1].keys())[0]
assert k_a is k_b

# 4. Inline integer parsing, min/max int64, BigInt overflow, floats, constants
assert json.loads("0") == 0
assert json.loads("-0") == 0
assert json.loads("9223372036854775807") == 9223372036854775807
assert json.loads("-9223372036854775808") == -9223372036854775808
big = json.loads("99999999999999999999999999")
assert isinstance(big, int) and big == 99999999999999999999999999
neg_big = json.loads("-99999999999999999999999999")
assert isinstance(neg_big, int) and neg_big == -99999999999999999999999999
assert json.loads("12.345") == 12.345
assert json.loads("-0.0001") == -0.0001
assert math.isnan(json.loads("NaN"))
assert math.isinf(json.loads("Infinity")) and json.loads("Infinity") > 0
assert math.isinf(json.loads("-Infinity")) and json.loads("-Infinity") < 0

# 5. Surrogate pairs, escapes, and strict=False
assert json.loads('"\\ud83d\\ude00"') == "😀"
assert json.loads('"a\\nb\\tc\\"\\\\\\/\\b\\f\\r"') == 'a\nb\tc"\\/\b\f\r'
assert json.loads('"a\nb"', strict=False) == "a\nb"

strict_err = False
try:
    json.loads('"a\nb"')
except json.JSONDecodeError as e:
    strict_err = (e.pos == 2)
assert strict_err

# 6. Hooks: object_hook, object_pairs_hook, parse_float, parse_int, parse_constant
assert json.loads('{"a": 1, "b": {"c": 2}}', object_hook=lambda d: ("obj", len(d))) == ("obj", 2)
assert json.loads('{"b": 1, "a": 2, "b": 3}', object_pairs_hook=lambda pairs: pairs) == [("b", 1), ("a", 2), ("b", 3)]
assert json.loads('[1, 2.5, NaN]', parse_int=lambda s: "int:" + s, parse_float=lambda s: "flt:" + s, parse_constant=lambda s: "const:" + s) == ["int:1", "flt:2.5", "const:NaN"]

# 7. JSONDecodeError positions
for bad, want_pos in [("1 2", 2), ("01", 1), ("1.", 1), ("1e", 1), ('"\\q"', 1), ("[1,]", 2), ('{"a":1,}', 6)]:
    caught = None
    try:
        json.loads(bad)
    except json.JSONDecodeError as e:
        caught = e
	assert caught is not None and caught.pos == want_pos, (bad, caught)
`)
	defer eng.ReleaseVM(v)
}

func TestDict32AndConstKeyMap16(t *testing.T) {
	eng, v := runPySource(t, `
import json

def make_dict_13(x):
    return {
        "k01": x + 1,
        "k02": x + 2,
        "k03": x + 3,
        "k04": x + 4,
        "k05": x + 5,
        "k06": x + 6,
        "k07": x + 7,
        "k08": x + 8,
        "k09": x + 9,
        "k10": x + 10,
        "k11": x + 11,
        "k12": x + 12,
        "k13": x + 13,
    }

def make_dict_16(x):
    return {
        "a01": 1, "a02": 2, "a03": 3, "a04": 4,
        "a05": 5, "a06": 6, "a07": 7, "a08": 8,
        "a09": 9, "a10": 10, "a11": 11, "a12": 12,
        "a13": 13, "a14": 14, "a15": 15, "a16": x,
    }

for i in range(20):
    d13 = make_dict_13(i)
    assert len(d13) == 13
    assert d13["k01"] == i + 1 and d13["k13"] == i + 13
    d13["extra"] = 99
    assert len(d13) == 14 and d13["extra"] == 99

    d16 = make_dict_16(i)
    assert len(d16) == 16
    assert d16["a01"] == 1 and d16["a16"] == i
    d16["k17"] = 17
    d16["k18"] = 18
    d16["k19"] = 19
    d16["k20"] = 20
    d16["k21"] = 21
    assert len(d16) == 21 and d16["k21"] == 21

# Exercise json.loads across 11..20 key objects (dict32 pool range)
raw_20 = "{" + ",".join(f'"f{i:02d}": {i}' for i in range(20)) + "}"
for _ in range(5):
    parsed = json.loads(raw_20)
    assert len(parsed) == 20 and parsed["f00"] == 0 and parsed["f19"] == 19
`)
	defer eng.ReleaseVM(v)
}
