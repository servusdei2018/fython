package vm_test

import (
	"testing"
)

func TestCompositeTupleDictAndSetKeys(t *testing.T) {
	runSnippet(t, `
# Numeric cross-type equality inside tuple keys
d = {(1, "a"): "hit", (0, "b"): "zero", (42, "long_string_key_over_7_bytes"): "long"}
assert d[(True, "a")] == "hit"
assert d[(1.0, "a")] == "hit"
assert d[(False, "b")] == "zero"
assert d[(0.0, "b")] == "zero"
assert d[(42, "long_string_key_over_7_bytes")] == "long"
assert (1, "a") in d
assert (True, "a") in d
assert (2, "a") not in d
assert (1, "c") not in d

# NaN identity inside tuple keys (matches CPython PyObject_RichCompareBool)
nan = float("nan")
d_nan = {(nan, 1): 99}
assert d_nan[(nan, 1)] == 99
assert (nan, 1) in d_nan

# Custom __eq__ and __hash__ element inside tuple key
class Box:
    def __init__(self, v):
        self.v = v
    def __hash__(self):
        return hash(self.v)
    def __eq__(self, other):
        return isinstance(other, Box) and self.v == other.v

d_box = {(1, Box(7)): "boxed"}
assert d_box[(1, Box(7))] == "boxed"
assert d_box[(True, Box(7))] == "boxed"
assert (1, Box(8)) not in d_box

# Set and frozenset composite tuple membership
s = {(i, i + 1, "tag") for i in range(50)}
assert (10, 11, "tag") in s
assert (0, 1, "tag") in s
assert (False, True, "tag") in s
assert (10, 12, "tag") not in s

fs = frozenset(s)
assert (10, 11, "tag") in fs
assert (False, True, "tag") in fs
assert len(fs) == 50
`)
}
