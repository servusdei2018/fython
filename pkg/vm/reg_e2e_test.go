package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// runHotSnippet compiles src, executes it, and requires clean completion.
// Hot functions inside run past the Tier-2 threshold, so register-compiled
// frames execute whenever eligible.
func runHotSnippet(t *testing.T, src string) {
	t.Helper()
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(src), "<reghot>")
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("unhandled exception:\n%s", v.FormatException(exc))
	}
}

func TestRegHotArithmeticLoop(t *testing.T) {
	runHotSnippet(t, `
def total(n):
    s = 0
    for i in range(n):
        s += i
    return s

got = 0
for _ in range(12):
    got = total(100)
assert got == 4950, got
`)
}

func TestRegHotRange2(t *testing.T) {
	runHotSnippet(t, `
def total(a, b):
    s = 0
    for i in range(a, b):
        s += i
    return s

got = 0
for _ in range(12):
    got = total(2, 8)
assert got == 27, got
`)
}

func TestRegHotRecursion(t *testing.T) {
	runHotSnippet(t, `
def fib(n):
    if n < 2:
        return n
    return fib(n - 1) + fib(n - 2)

got = 0
for _ in range(12):
    got = fib(10)
assert got == 55, got
`)
}

func TestRegHotClassAttrs(t *testing.T) {
	runHotSnippet(t, `
class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y
    def norm2(self):
        return self.x * self.x + self.y * self.y

got = 0
for _ in range(12):
    p = Point(3, 4)
    got = p.norm2()
assert got == 25, got
`)
}

func TestRegHotGenerators(t *testing.T) {
	runHotSnippet(t, `
def gen(n):
    for i in range(n):
        yield i * i

got = 0
for _ in range(12):
    got = list(gen(10))
assert got == [0, 1, 4, 9, 16, 25, 36, 49, 64, 81], got

total = 0
for _ in range(12):
    total = sum(gen(10))
assert total == 285, total

joined = ""
for _ in range(12):
    joined = ",".join(str(x) for x in [1, 2, 3])
assert joined == "1,2,3", joined
`)
}

func TestRegHotStrings(t *testing.T) {
	runHotSnippet(t, `
def greet(name, n):
    out = []
    for i in range(n):
        out.append(f"hello {name} {i}")
    return out

got = []
for _ in range(12):
    got = greet("ada", 3)
assert got == ["hello ada 0", "hello ada 1", "hello ada 2"], got
`)
}

func TestRegHotRaiseCaughtOutside(t *testing.T) {
	runHotSnippet(t, `
def fail(n):
    s = 0
    for i in range(n):
        s += i
    if s > 10:
        raise ValueError("too big")
    return s

ok = 0
caught = 0
for _ in range(12):
    try:
        ok = fail(3)
    except ValueError:
        caught += 1
assert ok == 3, ok
assert caught == 0, caught

for _ in range(12):
    try:
        fail(10)
    except ValueError as e:
        caught += 1
        assert str(e) == "too big", e
assert caught == 12, caught
`)
}

func TestRegHotContainers(t *testing.T) {
	runHotSnippet(t, `
def build(n):
    d = {}
    for i in range(n):
        d[i] = i * i
    return d

got = {}
for _ in range(12):
    got = build(5)
assert got == {0: 0, 1: 1, 2: 4, 3: 9, 4: 16}, got

def first(xs):
    a, b = xs
    return a + b

r = 0
for _ in range(12):
    r = first((3, 4))
assert r == 7, r
`)
}

func TestRegHotDefaultKwStar(t *testing.T) {
	runHotSnippet(t, `
def add(a, b=10, *rest):
    return a + b + sum(rest)

r = 0
for _ in range(12):
    r = add(1, b=2)
assert r == 3, r

for _ in range(12):
    r = add(1, 2, 3, 4)
assert r == 10, r

def kw(a, b):
    return a - b

for _ in range(12):
    r = kw(10, b=4)
assert r == 6, r
`)
}

func TestRegHotWhileShortCircuit(t *testing.T) {
	runHotSnippet(t, `
def find(xs, want):
    i = 0
    n = len(xs)
    while i < n and xs[i] != want:
        i += 1
    return i if i < n else -1

r = 0
for _ in range(12):
    r = find([1, 2, 3], 2)
assert r == 1, r

for _ in range(12):
    r = find([1, 2, 3], 9)
assert r == -1, r

def pick(a, b):
    return a or b

for _ in range(12):
    r = pick(0, 5) or pick(4, 5)
assert r == 5, r
`)
}

func TestRegHotOpStatsReduction(t *testing.T) {
	if !vm.OpStatsEnabled() {
		t.Skip("opstats build required")
	}
	vm.ResetOpStats()
	vm.ResetRegStats()
	runHotSnippet(t, `
def total(n):
    s = 0
    for i in range(n):
        s = s + i
    return s

for _ in range(12):
    total(50)
`)
	var stackTotal, regTotal uint64
	for op := range 256 {
		stackTotal += vm.OpExecCount(vm.Opcode(op))
		regTotal += vm.RegOpExecCount(vm.RegOp(op))
	}
	t.Logf("stack ops=%d reg ops=%d", stackTotal, regTotal)
	if regTotal == 0 {
		t.Fatalf("no register instructions executed")
	}
	if !strings.Contains(vm.RegOp(0).String(), "REG") {
		t.Fatalf("bad reg op name")
	}
	_ = stackTotal
}

func TestRegHotCondPreserved(t *testing.T) {
	// A conditional jump whose target entry needs local normalization
	// must not recycle the condition register mid-shuffle (hold/unhold
	// protection for popped jump conditions).
	runHotSnippet(t, `
def target(**kw):
    return kw

def f(ff=None, *, scope="function", params=None):
    m = target(
        s=scope,
        b=tuple(params) if params is not None else None,
    )
    return m

for _ in range(12):
    r = f()
    assert r == {"s": "function", "b": None}, r
    r2 = f(params=(9,))
    assert r2 == {"s": "function", "b": (9,)} , r2
`)
}

func TestRegHotPropertyNoClobber(t *testing.T) {
	// A nested property-getter call runs its frame above the register
	// file so live caller registers (e.g. a callee loaded earlier) are
	// never disturbed by the callee frame.
	runHotSnippet(t, `
class Opt:
    pass

class Arg:
    def __init__(self, dest, default):
        self._dest = dest
        self._default = default
    @property
    def dest(self):
        return self._dest
    @property
    def default(self):
        return self._default

class Parser:
    def __init__(self, opt):
        self.option = opt
    def _processopt(self, opt):
        if not hasattr(self.option, opt.dest):
            setattr(self.option, opt.dest, opt.default)

for _ in range(12):
    o = Opt()
    p = Parser(o)
    p._processopt(Arg("verbose", False))
    assert o.verbose == False, o.__dict__
    p._processopt(Arg("other", 42))
    assert o.other == 42, o.__dict__
`)
}

func TestRegHotExtendGenerator(t *testing.T) {
	// list.extend over a generator-based __iter__ must drive the
	// generator instead of misreading it as a PyIter.
	runHotSnippet(t, `
class Seq:
    def __init__(self, n):
        self.n = n
    def __iter__(self):
        i = 0
        while i < self.n:
            yield i * 10
            i += 1

for _ in range(12):
    out = []
    out.extend(Seq(4))
    assert out == [0, 10, 20, 30], out
    out.extend((1, 2))
    assert out == [0, 10, 20, 30, 1, 2], out
`)
}
