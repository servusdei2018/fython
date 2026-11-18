package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestHotPathAllocations(t *testing.T) {
	eng := vm.NewEngine()
	eng.SetCompileHook(compiler.CompileSource)
	v := eng.AcquireVM()
	defer eng.ReleaseVM(v)

	setupCO, err := compiler.CompileString(`
from collections import namedtuple

class SlotsObj:
    __slots__ = ("a", "b")
    def __init__(self, a, b):
        self.a = a
        self.b = b

class PropObj:
    CONST = 42
    def __init__(self, x):
        self._x = x
    @property
    def val(self):
        return self._x
    def base_m(self, y):
        return self._x + y

class SubObj(PropObj):
    def m(self, y):
        return super().base_m(y)

Pt = namedtuple("Pt", ["x", "y"])

so = SlotsObj(1, 2)
po = PropObj(10)
sub = SubObj(10)
pt = Pt(3, 4)

def bench_slots():
    acc = 0
    for _ in range(100):
        acc += so.a + so.b
    return acc

def bench_prop():
    acc = 0
    for _ in range(100):
        acc += po.val
    return acc

def bench_class_const():
    acc = 0
    for _ in range(100):
        acc += po.CONST + PropObj.CONST
    return acc

def bench_super():
    acc = 0
    for _ in range(100):
        acc += sub.m(1)
    return acc

def bench_namedtuple():
    acc = 0
    for _ in range(100):
        acc += pt.x + pt.y
    return acc

def bench_vars():
    acc = 0
    for _ in range(100):
        acc += len(vars(po))
    return acc

def gen_one(n):
    i = 0
    while i < n:
        yield i
        i += 1

def bench_gen():
    acc = 0
    for _ in range(20):
        for x in gen_one(5):
            acc += x
    return acc

def bench_tuple2():
    acc = 0
    for i in range(100):
        t = (i, i + 1)
        acc += t[0] + t[1]
    return acc
`)
	if err != nil {
		t.Fatalf("compile setup: %v", err)
	}
	v.RunCode(setupCO)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("setup raised: %s", v.FormatException(exc))
	}

	g := v.Globals()
	assertAllocs := func(fnName string, maxAllocs float64) {
		t.Helper()
		co, err := compiler.CompileString(fnName + "()")
		if err != nil {
			t.Fatalf("compile call %s: %v", fnName, err)
		}
		// Warm up ICs and pools before measuring.
		for range 5 {
			v.RunCodeWithGlobals(co, g)
			if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
				t.Fatalf("%s warmup raised: %s", fnName, v.FormatException(exc))
			}
		}
		got := testing.AllocsPerRun(20, func() {
			v.RunCodeWithGlobals(co, g)
		})
		if got > maxAllocs {
			t.Fatalf("%s allocated %.1f objects/run, want <= %.1f", fnName, got, maxAllocs)
		}
	}

	assertAllocs("bench_slots", 2)
	assertAllocs("bench_prop", 2)
	assertAllocs("bench_class_const", 2)
	// super() allocates 1 PySuper per call, but 0 PyBoundMethod objects on method lookup.
	assertAllocs("bench_super", 102)
	assertAllocs("bench_namedtuple", 2)
	assertAllocs("bench_vars", 2)
	// Generator loop: 20 iterations reusing pooled stack; only the 20 PyGenerator headers + outer PyIter allocate.
	assertAllocs("bench_gen", 28)
	// 100 2-tuples: single 80-byte allocation per tuple (no separate slice allocation).
	assertAllocs("bench_tuple2", 105)
}
