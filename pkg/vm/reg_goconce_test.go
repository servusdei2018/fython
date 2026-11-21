package vm_test

import (
	"testing"
)

func TestRegGOCOnce(t *testing.T) {
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

p = P()
c = p.get_or_create(("fruits",))
c["kind"] = "apple"
assert p.d == {"fruits": {"kind": "apple"}}, p.d
lst = p.get_or_create(("fruits",))
assert lst == {"kind": "apple"}, lst
p.d["fruits"] = [p.d["fruits"]]
c4 = p.get_or_create(("fruits",))
assert c4 == {"kind": "apple"}, c4
`)
}
