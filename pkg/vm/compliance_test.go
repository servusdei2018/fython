package vm_test

import (
	"fmt"
	"runtime/debug"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runSnippet(t *testing.T, code string) {
	t.Helper()
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(code), "<compliance>")
	if err != nil {
		t.Fatalf("Compilation error: %v\nCode:\n%s", err, code)
	}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v", r)
			if excVal, ok := r.(val.Value); ok {
				msg = excVal.String()
				if excVal.IsInstance() {
					if argsVal, ok := excVal.AsInstance().GetAttr(val.InternString("args")); ok {
						msg += fmt.Sprintf(" (args: %s)", argsVal.String())
					}
				}
			}
			t.Fatalf("Runtime panic: %s\nStack:\n%s\nCode:\n%s", msg, string(debug.Stack()), code)
		}
	}()

	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Unhandled Python exception:\n%s\nCode:\n%s", v.FormatException(exc), code)
	}
}

func TestDebugSnippet(t *testing.T) {
	code := `
import enum
print("enum loaded successfully:", enum)
`
	runSnippet(t, code)
}

func TestClassClosurePassthrough(t *testing.T) {
	code := `
def outer(x):
    class C:
        def method(self):
            return x
    return C().method()

assert outer(7) == 7

def outer_shadow(x):
    class C:
        x = 2
        def method(self):
            return x
    return C().method()

assert outer_shadow(7) == 7

def outer_read(x):
    class C:
        y = x
    return C.y

assert outer_read(9) == 9
`
	runSnippet(t, code)
}

func TestComplianceCollections(t *testing.T) {
	code := `
import collections
from collections import deque, defaultdict, namedtuple, Counter, ChainMap

# 1. Deque
d = deque([1, 2, 3], maxlen=4)
d.append(4)
d.append(5)
assert list(d) == [2, 3, 4, 5]
d.appendleft(1)
assert list(d) == [1, 2, 3, 4]
assert d.pop() == 4
assert d.popleft() == 1
assert len(d) == 2

# 2. DefaultDict
dd = defaultdict(int)
dd["a"] += 10
dd["b"] += 20
assert dd["a"] == 10
assert dd["b"] == 20
assert dd["c"] == 0

dd_list = defaultdict(list)
dd_list["k"].append("item")
assert dd_list["k"] == ["item"]

# 3. NamedTuple
Point = namedtuple("Point", ["x", "y"])
p = Point(10, 20)
assert p.x == 10
assert p.y == 20
assert p[0] == 10
assert p[1] == 20
assert len(p) == 2

# 4. Counter
c = Counter("abracadabra")
assert c["a"] == 5
assert c["b"] == 2
assert c["r"] == 2
assert c["c"] == 1
assert c["d"] == 1
assert c["z"] == 0

# 5. ChainMap
d1 = {"a": 1, "b": 2}
d2 = {"b": 20, "c": 30}
cm = ChainMap(d1, d2)
assert cm["a"] == 1
assert cm["b"] == 2
assert cm["c"] == 30
`
	runSnippet(t, code)
}

func TestComplianceEnum(t *testing.T) {
	code := `
import enum
from enum import Enum, IntEnum, Flag, auto, unique

# 1. Standard Enum
class Color(Enum):
    RED = 1
    GREEN = 2
    BLUE = 3

assert Color.RED.name == "RED"
assert Color.RED.value == 1
assert Color(1) == Color.RED
assert Color["BLUE"] == Color.BLUE
assert list(Color) == [Color.RED, Color.GREEN, Color.BLUE]

# 2. IntEnum
class Priority(IntEnum):
    LOW = 1
    MEDIUM = 2
    HIGH = 3

assert Priority.LOW == 1
assert Priority.HIGH > Priority.MEDIUM
assert Priority(2) == Priority.MEDIUM

# 3. Flag
class Permission(Flag):
    R = 1
    W = 2
    X = 4

rw = Permission.R | Permission.W
assert Permission.R in rw
assert Permission.W in rw
assert Permission.X not in rw
assert rw.value == 3

# 4. @unique
@unique
class Status(Enum):
    OK = 200
    NOT_FOUND = 404

assert Status.OK.value == 200
`
	runSnippet(t, code)
}

func TestComplianceImports(t *testing.T) {
	code := `
import functools
import typing
import contextlib
import unittest
T = typing.TypeVar("T")
class Box(typing.Generic[T]):
    def __init__(self, value: T):
        self.value = value
b = Box(1)
u = typing.Union[int, str]
opt = typing.Optional[int]
assert b.value == 1
assert type(u).__name__ == "UnionType"
assert type(opt).__name__ == "UnionType"
assert type(int | str).__name__ == "UnionType"
assert (int | str) == u
print("imports-ok", b.value, u, opt)
`
	runSnippet(t, code)
}

func TestComplianceStdlibBehavior(t *testing.T) {
	code := `
import abc
import contextlib
import functools
import unittest

@functools.lru_cache
def fib(n):
    if n < 2:
        return n
    return fib(n - 1) + fib(n - 2)
assert fib(10) == 55

@contextlib.contextmanager
def scale():
    yield 7
with scale() as n:
    assert n == 7

class Shape(abc.ABC):
    @abc.abstractmethod
    def area(self):
        pass

class Square(Shape):
    def __init__(self, side):
        self.side = side
    def area(self):
        return self.side * self.side

assert Square(3).area() == 9

class AddTests(unittest.TestCase):
    def test_add(self):
        self.assertEqual(1 + 1, 2)

suite = unittest.defaultTestLoader.loadTestsFromTestCase(AddTests)
result = unittest.TestResult()
suite.run(result)
assert result.wasSuccessful()
print("stdlib-ok", fib(10), n)
`
	runSnippet(t, code)
}

func TestComplianceDataclasses(t *testing.T) {
	code := `
import dataclasses
from dataclasses import dataclass, field, asdict

@dataclass
class Point:
    x: int
    y: int

p = Point(10, 20)
assert p.x == 10
assert p.y == 20
assert asdict(p) == {"x": 10, "y": 20}

@dataclass(frozen=True)
class FrozenPoint:
    x: int
    y: int

fp = FrozenPoint(1, 2)
assert fp.x == 1
assert fp.y == 2

@dataclass
class Person:
    name: str
    age: int = 30
    tags: list = field(default_factory=list)

person = Person("Alice")
assert person.name == "Alice"
assert person.age == 30
assert person.tags == []
person.tags.append("engineer")
assert person.tags == ["engineer"]

@dataclass
class Employee(Person):
    role: str = "Staff"

emp = Employee("Bob", 40, ["lead"], "Architect")
assert emp.name == "Bob"
assert emp.age == 40
assert emp.role == "Architect"
`
	runSnippet(t, code)
}

// Captured parameters are cells: they occupy frame slots but are absent from
// the fast-local list. A generator expression then allocates a temporary.
// That temporary used to reuse whichever local was assigned last, and map
// iteration made the victim change from compile to compile. collections.namedtuple
// builds its __new__ lambda from such a local, so the eval source sometimes
// began with "<".
func TestTempLocalDoesNotClobberCapturedParams(t *testing.T) {
	code := `
def build(typename, field_names, *, rename=False, defaults=None, module=None):
    def inner():
        return (typename, field_names)
    arg_list = "hits, misses, maxsize, currsize"
    _ = (name for name in field_names)
    return f"lambda _cls, {arg_list}: _tuple_new(_cls, ({arg_list}))"

got = build("CacheInfo", ("hits", "misses", "maxsize", "currsize"))
want = "lambda _cls, hits, misses, maxsize, currsize: _tuple_new(_cls, (hits, misses, maxsize, currsize))"
assert got == want, got
`
	for range 40 {
		runSnippet(t, code)
	}
}
