package vm_test

import (
	"testing"
)

func TestVarKWFrameDictSurvivesLocalsCapture(t *testing.T) {
	runSnippet(t, `
import sys
saved = []
def f(**kw):
    def g():
        return sys._getframe(1).f_locals["kw"]
    saved.append(g())
f(a=1)
f(b=2)
assert saved[0] == {"a": 1}, saved[0]
assert saved[1] == {"b": 2}, saved[1]
assert saved[0] is not saved[1]
# captured dict is fully detached: later mutation of the live call must not leak in
f(c=3)
assert saved[0] == {"a": 1}, saved[0]
assert saved[1] == {"b": 2}, saved[1]
`)
}
