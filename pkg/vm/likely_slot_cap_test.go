package vm_test

import (
	"testing"
)

func TestLikelySlotCapMultiAttrAndRace(t *testing.T) {
	runSnippet(t, `
class Wide:
    def __init__(self, base):
        self.a0 = base + 0
        self.a1 = base + 1
        self.a2 = base + 2
        self.a3 = base + 3
        self.a4 = base + 4
        self.a5 = base + 5
        self.a6 = base + 6
        self.a7 = base + 7
        self.a8 = base + 8
        self.a9 = base + 9
        self.a10 = base + 10
        self.a11 = base + 11

objs = [Wide(i * 100) for i in range(25)]
for i, o in enumerate(objs):
    assert o.a0 == i * 100
    assert o.a6 == i * 100 + 6
    assert o.a11 == i * 100 + 11

# delattr and vars(obj) on pre-allocated Slots
delattr(objs[5], "a7")
assert not hasattr(objs[5], "a7")
v = vars(objs[5])
assert "a7" not in v
assert v["a0"] == 500 and v["a11"] == 511
objs[5].extra = "dyn"
assert objs[5].extra == "dyn"
`)

	runSnippet(t, `
import threading

class SharedWide:
    def __init__(self, n):
        self.x0 = n; self.x1 = n+1; self.x2 = n+2; self.x3 = n+3
        self.x4 = n+4; self.x5 = n+5; self.x6 = n+6; self.x7 = n+7
        self.x8 = n+8; self.x9 = n+9

errors = []
def worker(tid):
    try:
        for i in range(60):
            o = SharedWide(tid * 100 + i)
            assert o.x9 == tid * 100 + i + 9
    except Exception as e:
        errors.append(e)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()
assert not errors, f"concurrent errors: {errors}"
`)
}
