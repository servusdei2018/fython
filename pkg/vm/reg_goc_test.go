package vm_test

import (
	"testing"
)

func TestRegGOCNest(t *testing.T) {
	runHotSnippet(t, `
class P:
    def __init__(self):
        self.d = {}
    def get_or_create(self, key, *, access_lists=True):
        cont = self.d
        for k in key:
            if k not in cont:
                cont[k] = {}
            cont = cont[k]
            if access_lists and isinstance(cont, list):
                cont = cont[-1]
            if not isinstance(cont, dict):
                raise KeyError("no nest")
        return cont

out = []
for _ in range(12):
    p = P()
    c = p.get_or_create(("a", "b"))
    c["x"] = 1
    out.append(p.d)
assert out[-1] == {"a": {"b": {"x": 1}}}, out

# exercise access_lists=False explicitly
p2 = P()
c2 = p2.get_or_create(("m",), access_lists=False)
c2["y"] = 2
assert p2.d == {"m": {"y": 2}}, p2.d

# lists inside the nest (array-of-tables shape)
p3 = P()
c3 = p3.get_or_create(("fruits",))
c3["kind"] = "apple"
lst = p3.get_or_create(("fruits",))
assert isinstance(lst, dict), lst
p3.d["fruits"] = [p3.d["fruits"]]
c4 = p3.get_or_create(("fruits",))
assert c4 == {"kind": "apple"}, c4
`)
}
