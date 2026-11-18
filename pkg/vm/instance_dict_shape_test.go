package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestInstanceDictKeepsShapeAndIdentity(t *testing.T) {
	eng, v := runPySource(t, `
from dataclasses import dataclass, replace, asdict

class Box:
    def __init__(self, a, b):
        self.a = a
        self.b = b

box = Box(1, 2)
d1 = box.__dict__
d2 = vars(box)
same_identity = (d1 is d2) and (vars(box) is box.__dict__)

# Mutations through __dict__ stay synchronized with Shape slots
d1["c"] = 3
d1.update({"d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10})
del d1["b"]
copied = dict(d1)
popped = d1.pop("j")
val_i = box.i
val_c = box.c

# Replace __dict__ re-transitions Shape without deoptimizing
box.__dict__ = {"x": 11, "y": 22}
after_replace = box.x + box.y
has_old = hasattr(box, "a")

# Repeated attribute reads after vars() still use Shape IC
def sum_xy(o):
    return o.x + o.y
sum_after = 0
for _ in range(10):
    sum_after += sum_xy(box)

# clear() on __dict__ clears slots while preserving Shape-backed storage
box2 = Box(7, 8)
box2.__dict__.clear()
len_after_clear = len(box2.__dict__)

# dataclass (__eq__, __repr__, replace, asdict) stays Shape-backed
@dataclass
class Point:
    x: int
    y: int
    label: str = "origin"

p1 = Point(3, 4)
p2 = replace(p1, y=5)
eq_ok = (p1 == Point(3, 4)) and (p1 != p2)
asdict_ok = (asdict(p2) == {"x": 3, "y": 5, "label": "origin"})
p_vars = vars(p2)
`)
	defer eng.ReleaseVM(v)

	g := v.Globals()
	checkBool := func(name string) {
		t.Helper()
		got, ok := g.Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want true", name, got, ok)
		}
	}
	checkInt := func(name string, want int64) {
		t.Helper()
		got, ok := g.Get(val.InternString(name))
		if !ok || !got.IsInt64() || got.AsInt64() != want {
			t.Fatalf("%s = %v (ok=%v), want %d", name, got, ok, want)
		}
	}

	checkBool("same_identity")
	checkInt("popped", 10)
	checkInt("val_i", 9)
	checkInt("val_c", 3)
	checkInt("after_replace", 33)
	if got, ok := g.Get(val.InternString("has_old")); !ok || got.AsBool() {
		t.Fatalf("has_old = %v, want false", got)
	}
	checkInt("sum_after", 330)
	checkInt("len_after_clear", 0)
	checkBool("eq_ok")
	checkBool("asdict_ok")

	for _, name := range []string{"box", "box2", "p1", "p2"} {
		obj, ok := g.Get(val.InternString(name))
		if !ok || !obj.IsInstance() {
			t.Fatalf("%s is not an instance: %v", name, obj)
		}
		inst := obj.AsInstance()
		if inst.Shape == nil || inst.Slots == nil {
			t.Fatalf("%s lost Shape (%v) or Slots (%v)", name, inst.Shape, inst.Slots)
		}
	}
}
