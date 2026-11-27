package vm_test

import (
	"bytes"
	"regexp"
	"strconv"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

var storeAttrSlowRe = regexp.MustCompile(`StoreAttrSlowEntries:\s+(\d+)`)

func TestSlotsStoreAttrICFastPathAndSubclasses(t *testing.T) {
	if vm.ICStatsEnabled() {
		vm.ResetICStats()
	}

	eng, v := runPySource(t, `
class Point:
    __slots__ = ("x", "y")
    def __init__(self, x, y):
        self.x = x
        self.y = y

class Point3D(Point):
    __slots__ = ("z",)
    def __init__(self, x, y, z):
        super().__init__(x, y)
        self.z = z

class DynPoint(Point):
    def __init__(self, x, y, tag):
        super().__init__(x, y)
        self.tag = tag

# Module-level loop stays in Tier-1 VM.runLoopWithGlobals.
pts = [Point(i, i + 1) for i in range(8)]
for step in range(400):
    p = pts[step & 7]
    p.x = step
    p.y = step * 2

# Generator stays in Tier-1 VM.runLoopWithGlobals and exercises polymorphic subclasses.
def update_gen(objs, rounds):
    for r in range(rounds):
        for o in objs:
            o.x = r
            o.y = r + 10
        yield r

p2 = Point(0, 0)
p3 = Point3D(0, 0, 5)
pd = DynPoint(0, 0, "init")
for _ in update_gen([p2, p3, pd], 50):
    pass

# Also transition DynPoint's shape dynamically and keep writing inherited slot.
pd.extra = 99
for k in range(20):
    pd.x = k + 100
    p3.z = k + 200

ok_pts = all(pts[i].x == 392 + i and pts[i].y == (392 + i) * 2 for i in range(8))
ok_sub = (p2.x == 49 and p2.y == 59 and p3.x == 49 and p3.y == 59 and p3.z == 219 and pd.x == 119 and pd.y == 59 and pd.tag == "init" and pd.extra == 99)
`)
	defer eng.ReleaseVM(v)

	for _, name := range []string{"ok_pts", "ok_sub"} {
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want True", name, got, ok)
		}
	}

	if vm.ICStatsEnabled() {
		var buf bytes.Buffer
		vm.DumpICStats(&buf)
		m := storeAttrSlowRe.FindStringSubmatch(buf.String())
		if len(m) != 2 {
			t.Fatalf("missing StoreAttrSlowEntries in IC stats:\n%s", buf.String())
		}
		slowCount, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			t.Fatalf("parse StoreAttrSlowEntries: %v", err)
		}
		// 800+ monomorphic __slots__ stores in the module loop should hit attrPIC
		// in VM.runLoopWithGlobals instead of entering opStoreAttrSlow every iteration.
		if slowCount > 250 {
			t.Fatalf("StoreAttrSlowEntries = %d, expected <= 250 (attrPIC should absorb monomorphic __slots__ stores)", slowCount)
		}
	}
}

func TestSlotsStoreAttrICPropertyMonkeyPatchInvalidation(t *testing.T) {
	runSnippet(t, `
class Box:
    __slots__ = ("x", "_shadow")
    def __init__(self, x):
        self.x = x
        self._shadow = 0

def set_box_gen(b, n):
    for i in range(n):
        b.x = i
        yield i

b = Box(1)
# Warm attrPIC on both module-level and generator Tier-1 sites.
for i in range(30):
    b.x = i
assert b.x == 29

for _ in set_box_gen(b, 10):
    pass
assert b.x == 9

calls = []
def _get(self):
    return self._shadow
def _set(self, v):
    calls.append(v)
    self._shadow = v * 10

# Monkey-patching Box.x bumps TypeVersion and must invalidate attrPIC.
Box.x = property(_get, _set)

b.x = 7
assert b.x == 70
assert calls == [7]

for _ in set_box_gen(b, 3):
    pass
assert b.x == 20
assert calls == [7, 0, 1, 2]
`)
}

func TestSlotsStoreAttrICCustomSetAttrInvalidation(t *testing.T) {
	runSnippet(t, `
class Node:
    __slots__ = ("x", "y")
    def __init__(self, x):
        self.x = x
        self.y = 0

class SubNode(Node):
    __slots__ = ("z",)
    def __init__(self, x):
        super().__init__(x)
        self.z = 0

def write_x_gen(obj, vals):
    for v in vals:
        obj.x = v
        yield v

n = Node(1)
s = SubNode(2)
for i in range(30):
    n.x = i
    s.x = i + 1
list(write_x_gen(n, [10, 11, 12]))
assert n.x == 12 and s.x == 30

log = []
def custom_setattr(self, name, value):
    log.append((type(self).__name__, name, value))
    object.__setattr__(self, name, value + 100)

# Installing custom __setattr__ sets FlagCustomSetAttr and must bypass attrPIC.
Node.__setattr__ = custom_setattr

n.x = 5
s.x = 6
list(write_x_gen(n, [7]))

assert n.x == 107
assert s.x == 106
assert log == [("Node", "x", 5), ("SubNode", "x", 6), ("Node", "x", 7)]
`)
}

func TestSlotsStoreAttrICDelClassSlotDescriptorInvalidation(t *testing.T) {
	runSnippet(t, `
class SlottedOnly:
    __slots__ = ("x", "y")
    def __init__(self):
        self.x = 0
        self.y = 10

def write_x_gen(obj, val):
    obj.x = val
    yield obj.x

obj = SlottedOnly()
# Warm attrPIC on both direct and generator sites.
for i in range(25):
    obj.x = i
assert list(write_x_gen(obj, 1)) == [1]
assert obj.x == 1 and obj.y == 10

# Deleting the slot descriptor from the class bumps TypeVersion via PyType.DelAttr.
del SlottedOnly.x

raised_direct = False
try:
    obj.x = 2
except AttributeError:
    raised_direct = True
assert raised_direct, "expected AttributeError after del SlottedOnly.x"

raised_gen = False
try:
    next(write_x_gen(obj, 3))
except AttributeError:
    raised_gen = True
assert raised_gen, "expected AttributeError in warmed generator site after del SlottedOnly.x"

# Other slots remain unaffected.
obj.y = 20
assert obj.y == 20
`)
}

func TestSlotsStoreAttrICDelInstanceSlotAndSpecialValues(t *testing.T) {
	runSnippet(t, `
class Holder:
    __slots__ = ("x", "buf", "mv")
    def __init__(self):
        self.x = 0
        self.buf = ""
        self.mv = None

h = Holder()
for i in range(30):
    h.x = i
assert h.x == 29

# Delete instance slot, verify AttributeError on load, then re-store through warmed site.
del h.x
raised = False
try:
    _ = h.x
except AttributeError:
    raised = True
assert raised, "expected AttributeError for deleted slot"

for i in range(5):
    h.x = 100 + i
assert h.x == 104

# Store buffered/concatenated strings and memoryview through warmed __slots__ sites.
ba = bytearray(b"abcdef")
for i in range(20):
    parts = ["tok", str(i), "end"]
    h.buf = "-".join(parts)
    h.mv = memoryview(ba)[1:5]

assert h.buf == "tok-19-end"
assert bytes(h.mv) == b"bcde"
ba[2] = ord("Z")
assert bytes(h.mv) == b"bZde"
h.mv.release()
`)
}

func TestSlotsStoreAttrICConcurrentRace(t *testing.T) {
	runSnippet(t, `
import threading

class Counter:
    __slots__ = ("x", "y")
    def __init__(self):
        self.x = 0
        self.y = 0

shared = Counter()
errors = []

def worker(tid):
    try:
        local = Counter()
        for i in range(200):
            shared.x = tid * 1000 + i
            shared.y = i
            local.x = i
            local.y = i * 2
            if local.x * 2 != local.y:
                errors.append((tid, i, local.x, local.y))
                return
    except Exception as exc:
        errors.append(exc)

threads = [threading.Thread(target=worker, args=(t,)) for t in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()

assert not errors, f"concurrent __slots__ errors: {errors}"
assert isinstance(shared.x, int) and isinstance(shared.y, int)
`)
}
