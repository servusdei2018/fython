package vm_test

import (
	"testing"
)

func TestDunderCallFromHookAndTruthy(t *testing.T) {
	runSnippet(t, `
from dataclasses import dataclass

class Flag:
    def __init__(self, v):
        self.v = v
    def __bool__(self):
        return bool(self.v)

class Sized:
    def __init__(self, n):
        self.n = n
    def __len__(self):
        return self.n

class Boom:
    def __bool__(self):
        raise ValueError("bool boom")

class DeepBool:
    def __init__(self, depth):
        self.depth = depth
    def __bool__(self):
        def rec(k):
            if k <= 0:
                return True
            return rec(k - 1)
        return rec(self.depth)

assert bool(Flag(1)) is True
assert bool(Flag(0)) is False
assert (True if Flag(1) else False) is True
assert (True if Flag(0) else False) is False
assert (not Flag(0)) is True
assert (not Flag(1)) is False
assert (Flag(1) and 42) == 42
assert (Flag(0) or 99) == 99

assert bool(Sized(3)) is True
assert bool(Sized(0)) is False
assert (True if Sized(0) else False) is False

# Exception propagation from dunders via bool() / __add__ / __lt__
raised = False
try:
    bool(Boom())
except ValueError as e:
    raised = "bool boom" in str(e)
assert raised

# Stack growth inside __bool__
assert (True if DeepBool(40) else False) is True

# __add__, __hash__, __eq__, __lt__
class Vec:
    def __init__(self, x):
        self.x = x
    def __add__(self, other):
        return Vec(self.x + other.x)
    def __hash__(self):
        return hash(self.x)
    def __eq__(self, other):
        return isinstance(other, Vec) and self.x == other.x
    def __lt__(self, other):
        return self.x < other.x

assert (Vec(2) + Vec(3)).x == 5
assert {Vec(10): "ten"}[Vec(10)] == "ten"
assert Vec(1) < Vec(2)

# @dataclass(order=True) comparisons and sorting
@dataclass(order=True)
class SemVer:
    major: int
    minor: int
    patch: int
    pre: str = ""

items = [SemVer(2, 0, 0), SemVer(1, 10, 0), SemVer(1, 2, 3, "rc1"), SemVer(1, 2, 3, "")]
s = sorted(items)
assert s == [SemVer(1, 2, 3, ""), SemVer(1, 2, 3, "rc1"), SemVer(1, 10, 0), SemVer(2, 0, 0)]
`)
}
