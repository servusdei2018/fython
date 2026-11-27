package vm_test

import (
	"testing"
)

func TestClosureCaptureSemantics(t *testing.T) {
	runPySource(t, `
import types

# 1, 2, 3, and 5-cell closures (both direct-capture and real-cell)
def make_direct_1(a):
    def f(x):
        return a + x
    return f

def make_direct_2(a, b):
    c = a + b
    def f(x):
        return a + b + c + x
    return f

def make_direct_5(a, b, c, d, e):
    def f(x):
        return a + b + c + d + e + x
    return f

f1 = make_direct_1(10)
f2 = make_direct_2(1, 2)
f5 = make_direct_5(1, 2, 3, 4, 5)
assert f1(5) == 15
assert f2(4) == 10
assert f5(10) == 25

# Lazy __closure__ materialization and identity stability
c1 = f1.__closure__
assert c1 is not None and len(c1) == 1
assert c1[0].cell_contents == 10
assert f1.__closure__[0] is c1[0]

c5 = f5.__closure__
assert len(c5) == 5
assert {cell.cell_contents for cell in c5} == {1, 2, 3, 4, 5}

# types.FunctionType cloning a direct-capture function with explicit closure cells
cloned = types.FunctionType(f1.__code__, f1.__globals__, "cloned", None, f1.__closure__)
assert cloned(7) == 17
assert cloned.__closure__[0].cell_contents == 10

# Nonlocal mutation (must stay real PyCell, not direct capture)
def make_counter(start):
    n = start
    def inc(step):
        nonlocal n
        n += step
        return n
    return inc

cnt = make_counter(10)
assert cnt(3) == 13
assert cnt(4) == 17
assert cnt.__closure__[0].cell_contents == 17

# Sibling closures must share the exact same cell identity
def make_siblings(v):
    def get_v():
        return v
    def get_v2():
        return v + 1
    return get_v, get_v2

g1, g2 = make_siblings(42)
assert g1() == 42 and g2() == 43
assert g1.__closure__[0] is g2.__closure__[0]

# Late-binding closures in loops
fns = []
for i in range(5):
    fns.append(lambda: i)
assert [fn() for fn in fns] == [4, 4, 4, 4, 4]

# del on captured variable must not direct-capture (keeps real cell)
def make_del_capture():
    x = 99
    def reader():
        return x
    if False:
        del x
    x = 100
    return reader

del_reader = make_del_capture()
assert del_reader() == 100
assert del_reader.__closure__[0].cell_contents == 100

# Nested multi-level direct captures
def level0(a, b):
    s = a + b
    def level1(x):
        y = x * 2 + s
        def level2(z):
            return y + z + a
        return level2(x + 1)
    return level1

lv = level0(3, 4)
assert lv(5) == (5 * 2 + 7) + 6 + 3

# Reassigned outer variable must see updated value (not stale direct capture)
def reassigned_outer():
    x = 1
    def read():
        return x
    x = 2
    return read

assert reassigned_outer()() == 2

# Non-closure function has __closure__ is None
def plain(x):
    return x + 1
assert plain.__closure__ is None
`)
}

func TestClosureCaptureConcurrent(t *testing.T) {
	runPySource(t, `
import threading

def make_adder(base, scale):
    offset = base + 1
    def add(x):
        return x * scale + offset
    return add

expected = sum(i * 3 + (i + 1) for i in range(200))
results = [0] * 4
errors = []

def worker(idx):
    try:
        total = 0
        for i in range(200):
            fn = make_adder(i, 3)
            total += fn(i)
        results[idx] = total
    except Exception as exc:
        errors.append(exc)

threads = [threading.Thread(target=worker, args=(i,)) for i in range(4)]
for th in threads:
    th.start()
for th in threads:
    th.join()

assert not errors, errors
assert results == [expected] * 4, results
`)
}
