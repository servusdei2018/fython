package vm_test

import (
	"testing"
)

func TestSplitTableConstKeyMapDicts(t *testing.T) {
	runPySource(t, `
import json

def make_rec(a, b, c):
    return {"alpha": a, "beta": b, "gamma": c}

def make_wide(i):
    return {
        "k0": i, "k1": i + 1, "k2": i + 2, "k3": i + 3,
        "k4": i + 4, "k5": i + 5, "k6": i + 6, "k7": i + 7,
    }

# Warm up const-key template
for i in range(10):
    d = make_rec(i, i * 2, i * 3)
    assert len(d) == 3
    assert d["alpha"] == i
    assert d["beta"] == i * 2
    assert d["gamma"] == i * 3
    assert d.get("missing", -1) == -1
    assert list(d.keys()) == ["alpha", "beta", "gamma"]
    assert list(d.values()) == [i, i * 2, i * 3]
    assert list(d.items()) == [("alpha", i), ("beta", i * 2), ("gamma", i * 3)]
    assert list(d) == ["alpha", "beta", "gamma"]
    assert set(d) == {"alpha", "beta", "gamma"}

# Overwrite existing key in-place
d1 = make_rec(1, 2, 3)
d1["beta"] = 20
assert d1["beta"] == 20
assert d1 == {"alpha": 1, "beta": 20, "gamma": 3}

# Copy preserves split semantics and independence
d2 = d1.copy()
d2["alpha"] = 99
assert d1["alpha"] == 1
assert d2["alpha"] == 99

# JSON serialization
s = json.dumps(make_rec(10, "hi", True))
assert s == '{"alpha": 10, "beta": "hi", "gamma": true}', s

# Unpacking (**d) and dict merge
def take_kw(alpha, beta, gamma):
    return alpha + beta + gamma
assert take_kw(**make_rec(2, 3, 5)) == 10
merged = {**make_rec(1, 2, 3), "delta": 4}
assert merged == {"alpha": 1, "beta": 2, "gamma": 3, "delta": 4}

# Promotion on new key insert
d3 = make_rec(1, 2, 3)
d3["delta"] = 4
assert len(d3) == 4 and d3["delta"] == 4 and d3["alpha"] == 1

# Promotion on delete / pop / popitem / update / clear
d4 = make_rec(1, 2, 3)
del d4["beta"]
assert "beta" not in d4 and len(d4) == 2
assert d4.pop("alpha") == 1
assert d4.popitem() == ("gamma", 3)
assert len(d4) == 0

d5 = make_rec(7, 8, 9)
d5.update({"beta": 80, "extra": 100})
assert d5 == {"alpha": 7, "beta": 80, "gamma": 9, "extra": 100}
d5.clear()
assert len(d5) == 0 and list(d5.items()) == []

# Wide 8-key split dict (hash-probed splitLookup)
w = make_wide(5)
assert len(w) == 8 and w["k0"] == 5 and w["k7"] == 12
w["k4"] = 44
assert w["k4"] == 44
w["k8"] = 88
assert len(w) == 9 and w["k8"] == 88 and w["k4"] == 44
`)
}
