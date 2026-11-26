package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestJSONSchemaRepeatedShapes(t *testing.T) {
	eng, v := runPySource(t, `
import json
payload = '[{"id": 1, "customer": "alice", "created": "2025-01-01T00:00:00", "items": [{"sku": "a", "qty": 2, "price": 3.5}], "tax_rate": 0.1}]'
rows = []
for _ in range(50):
    data = json.loads(payload)
    assert len(data) == 1
    assert data[0]["id"] == 1
    assert data[0]["items"][0]["sku"] == "a"
    rows.append(data[0])
assert len(rows) == 50
assert rows[0]["customer"] == "alice"
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("rows"))
	if !ok || !got.IsList() || len(got.AsList().Items) != 50 {
		t.Fatalf("rows = %v, want 50 items", got)
	}
}

func TestJSONSchemaKeyOrderAndDuplicates(t *testing.T) {
	eng, v := runPySource(t, `
import json
# Same keys, different order: must not share a schema entry incorrectly.
a = json.loads('{"x": 1, "y": 2, "z": 3}')
b = json.loads('{"z": 3, "y": 2, "x": 1}')
assert a == b == {"x": 1, "y": 2, "z": 3}
assert list(a.keys()) == ["x", "y", "z"]
assert list(b.keys()) == ["z", "y", "x"]
# Duplicate keys: last wins, never cached as 2-key schema.
dup = json.loads('{"a": 1, "a": 2, "long_repeated_key": 10, "long_repeated_key": 20}')
assert dup == {"a": 2, "long_repeated_key": 20}
assert len(dup) == 2
# Escaped, unicode, long keys still hit by value equality.
long_key = "k" * 40
p = json.loads('[{"id": 1, "caf\\u00e9": 2, "' + long_key + '": 3, "a\\"b": 4}]')
q = json.loads('[{"id": 5, "caf\\u00e9": 6, "' + long_key + '": 7, "a\\"b": 8}]')
assert p[0]["caf\u00e9"] == 2 and q[0]["caf\u00e9"] == 6
assert p[0][long_key] == 3 and q[0][long_key] == 7
`)
	defer eng.ReleaseVM(v)
}

func TestJSONSchemaSizeEdges(t *testing.T) {
	eng, v := runPySource(t, `
import json
assert json.loads('{}') == {}
assert json.loads('{"only": 1}') == {"only": 1}
big = {("k%d" % i): i for i in range(17)}
s = json.dumps(big, sort_keys=True)
assert json.loads(s) == big
# 16 keys is the max cached shape.
sixteen = {("m%d" % i): i for i in range(16)}
assert json.loads(json.dumps(sixteen, sort_keys=True)) == sixteen
`)
	defer eng.ReleaseVM(v)
}

func TestJSONSchemaMutationPromotion(t *testing.T) {
	eng, v := runPySource(t, `
import json
d = json.loads('{"a": 1, "b": 2}')
d["c"] = 3
assert d == {"a": 1, "b": 2, "c": 3}
del d["a"]
assert d == {"b": 2, "c": 3}
assert "a" not in d
d["b"] = 20
assert d["b"] == 20
# Mutating one parsed dict must not corrupt later parses of the same shape.
e = json.loads('{"a": 10, "b": 20}')
assert e == {"a": 10, "b": 20}
`)
	defer eng.ReleaseVM(v)
}

func TestJSONSchemaHooksBypass(t *testing.T) {
	eng, v := runPySource(t, `
import json
seen = []
def hook(d):
    seen.append(dict(d))
    d["hooked"] = True
    return d
r = json.loads('{"a": 1, "b": 2}', object_hook=hook)
assert r == {"a": 1, "b": 2, "hooked": True}
assert seen == [{"a": 1, "b": 2}]
pairs = json.loads('{"a": 1, "b": 2}', object_pairs_hook=list)
assert pairs == [("a", 1), ("b", 2)]
# After hook parses, plain parses of the same shape still work.
plain = json.loads('{"a": 3, "b": 4}')
assert plain == {"a": 3, "b": 4}
`)
	defer eng.ReleaseVM(v)
}

func TestJSONSchemaResetIsolation(t *testing.T) {
	eng, v := runPySource(t, `
import json
first = json.loads('{"a": 1, "b": 2}')
assert first == {"a": 1, "b": 2}
`)
	eng.ReleaseVM(v)
	v2 := eng.AcquireVM()
	defer eng.ReleaseVM(v2)
	eng2, v3 := runPySource(t, `
import json
second = json.loads('{"a": 3, "b": 4}')
assert second == {"a": 3, "b": 4}
`)
	defer eng2.ReleaseVM(v3)
	_ = v2
}

func TestJSONSchemaConcurrentParses(t *testing.T) {
	eng, v := runPySource(t, `
import json
import threading
errors = []
def worker():
    try:
        for _ in range(20):
            d = json.loads('{"a": 1, "b": 2, "c": 3}')
            assert d == {"a": 1, "b": 2, "c": 3}
            dup = json.loads('{"a": 1, "a": 2}')
            assert dup == {"a": 2}
    except Exception as e:
        errors.append(e)
threads = [threading.Thread(target=worker) for _ in range(8)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, f"concurrent errors: {errors}"
`)
	defer eng.ReleaseVM(v)
}
