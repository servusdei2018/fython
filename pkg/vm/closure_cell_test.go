package vm_test

import (
	"testing"
)

func TestClosureCellsAndDataclass(t *testing.T) {
	runPySource(t, `
from dataclasses import dataclass, asdict, replace

# super() / __class__ cell
class Base:
    def greet(self):
        return "base"

class Child(Base):
    def greet(self):
        return super().greet() + "+child"

assert Child().greet() == "base+child"

# nonlocal mutation + cell_contents inspection (1, 2, 4 cells)
def make_counter(start):
    x = start
    y = start * 2
    def inc(delta):
        nonlocal x, y
        x += delta
        y += delta * 2
        return (x, y)
    return inc

c = make_counter(10)
assert c(3) == (13, 26)
assert c(2) == (15, 30)
assert len(c.__closure__) == 2
assert {cell.cell_contents for cell in c.__closure__} == {15, 30}

# Late-binding closures in loops
fns = []
for i in range(4):
    fns.append(lambda: i)
assert [f() for f in fns] == [3, 3, 3, 3]

# @dataclass asdict / replace
@dataclass
class Point:
    x: int
    y: int
    label: str = "p"

p = Point(3, 4)
assert asdict(p) == {"x": 3, "y": 4, "label": "p"}
p2 = replace(p, y=10)
assert p2 == Point(3, 10, "p")
`)
}
