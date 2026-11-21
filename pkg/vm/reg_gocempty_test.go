package vm_test

import (
	"testing"
)

func TestRegGOCNestEmpty(t *testing.T) {
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
    def append_nest_to_list(self, key):
        cont = self.get_or_create(key[:-1])
        last_key = key[-1]
        if last_key in cont:
            list_ = cont[last_key]
            if not isinstance(list_, list):
                raise KeyError("not a list")
            list_.append({})
        else:
            cont[last_key] = [{}]

for _ in range(12):
    p = P()
    p.append_nest_to_list(("fruits",))
    p.append_nest_to_list(("fruits",))
    assert len(p.d["fruits"]) == 2, p.d
`)
}
