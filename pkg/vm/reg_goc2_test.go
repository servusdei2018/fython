package vm_test

import (
	"testing"
)

func TestRegGOCNest2(t *testing.T) {
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

p3 = P()
c3 = p3.get_or_create(("fruits",))
c3["kind"] = "apple"
`)
}
