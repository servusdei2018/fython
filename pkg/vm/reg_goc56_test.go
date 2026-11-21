package vm_test

import (
	"testing"
)

func TestRegGOCNest5(t *testing.T) {
	runHotSnippet(t, `
def lastitem(xs):
    return xs[-1]

for _ in range(12):
    r = lastitem([{"kind": "apple"}])
    assert r == {"kind": "apple"}, r
`)
}

func TestRegGOCNest6(t *testing.T) {
	runHotSnippet(t, `
def islist(x):
    return isinstance(x, list)

def isdict(x):
    return isinstance(x, dict)

for _ in range(12):
    assert islist([{"a": 1}])
    assert isdict({"a": 1})
    assert not islist({"a": 1})
    assert not isdict([1])
`)
}
