package vm_test

import (
	"testing"
)

func TestJSONKeyCacheAndASTShapes(t *testing.T) {
	runPySource(t, `
import ast
import json

# 1. JSON key cache: <=7B inline, 8..32B heap, >32B, empty "", escaped, unicode, duplicates, strict control chars
long_key = "k" * 40
payload = (
    '['
    '{"id": 1, "customer_name": "alice", "": 0, "' + long_key + '": 9, "a\\"b": 2, "caf\\u00e9": 3, "日本語": 4},'
    '{"id": 2, "customer_name": "bob",   "": 5, "' + long_key + '": 8, "a\\"b": 6, "café": 7, "日本語": 8}'
    ']'
)
for _ in range(10):
    rows = json.loads(payload)
    assert len(rows) == 2
    assert rows[0]["id"] == 1
    assert rows[0]["customer_name"] == "alice"
    assert rows[0][""] == 0
    assert rows[0][long_key] == 9
    assert rows[0]['a"b'] == 2
    assert rows[0]["café"] == 3
    assert rows[0]["日本語"] == 4
    assert rows[1]["id"] == 2
    assert rows[1]["customer_name"] == "bob"
    assert rows[1][""] == 5

# Duplicate keys: last key wins, len(d) stays 1
dup = json.loads('{"a": 1, "a": 2, "long_repeated_key": 10, "long_repeated_key": 20}')
assert dup == {"a": 2, "long_repeated_key": 20}
assert len(dup) == 2

# Control characters in keys must fail in strict mode and succeed when strict=False
try:
    json.loads('{"a\nb": 1}')
    assert False, "expected JSONDecodeError for control char in key"
except json.JSONDecodeError:
    pass
non_strict = json.loads('{"a\nb": 1}', strict=False)
assert non_strict == {"a\nb": 1}

# 2. _ast pre-seeded shapes: attribute layout, vars(), del, dynamic attribute addition
tree = ast.parse("def f(x):\n    y = x + 1\n    return y\n")
fn = tree.body[0]
assert isinstance(fn, ast.FunctionDef)
assert fn.name == "f"
assert fn.lineno == 1
assert fn.col_offset == 0
v = vars(fn)
assert v["name"] == "f"
assert v["lineno"] == 1
assert "body" in v

# Dynamic attribute addition on pre-seeded shape
fn.custom_meta = "tagged"
assert fn.custom_meta == "tagged"
assert vars(fn)["custom_meta"] == "tagged"

# del attribute on pre-seeded shape
del fn.custom_meta
assert not hasattr(fn, "custom_meta")
del fn.name
assert not hasattr(fn, "name")
assert "name" not in vars(fn)
del fn.returns
assert "returns" not in vars(fn)
assert fn.returns is None  # optional field falls back to class default None
`)
}
