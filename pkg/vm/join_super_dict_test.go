package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestInLoopStrJoinGeneratorDrain(t *testing.T) {
	eng, v := runPySource(t, `
def gen_inline():
    yield "a"
    yield "bc"
    yield "def"

def gen_long():
    for i in range(12):
        yield "item_" + str(i)

# 1. Direct sep.join(gen) with inline (<= 7 B) and heap (> 7 B) results
assert "".join(gen_inline()) == "abcdef"
assert "-".join(gen_inline()) == "a-bc-def"
assert ",".join(gen_long()) == ",".join("item_" + str(i) for i in range(12))

# 2. Bound-method alias and class-attribute bound method (Environment.concat pattern)
class Env:
    concat = "".join

env = Env()
assert env.concat(gen_inline()) == "abcdef"
concat_fn = "".join
assert concat_fn(gen_inline()) == "abcdef"
assert str.join("|", gen_inline()) == "a|bc|def"

# 3. Nested str.join(gen) inside an outer str.join(gen) + yield from + for-loop
def inner(n):
    yield "x" + str(n)
    yield "y" + str(n)

def sub_delegated():
    yield from inner(9)
    yield from ["z1", "z2"]

def outer():
    yield "["
    for i in range(3):
        yield ".".join(inner(i))
        yield ";"
    yield "".join(sub_delegated())
    yield "]"

assert "".join(outer()) == "[x0.y0;x1.y1;x2.y2;x9y9z1z2]"

# 4. Empty & already-exhausted generator
def empty_gen():
    if False:
        yield "never"

g_done = empty_gen()
assert "".join(g_done) == ""
assert "".join(g_done) == ""

# 5. Partially consumed generator
def partial_gen():
    yield "p0"
    yield "p1"
    yield "p2"

gp = partial_gen()
assert next(gp) == "p0"
assert "-".join(gp) == "p1-p2"

# 6. Non-string yield must raise TypeError at the str.join caller, not inside gen's try/except
def bad_gen():
    try:
        yield "ok"
        yield 42
    except TypeError:
        yield "caught_inside"

g_bad = bad_gen()
raised = False
try:
    "".join(g_bad)
except TypeError as exc:
    raised = True
    assert "expected str instance, int found" in str(exc)
assert raised

# 7. Early close() / throw() / GeneratorExit cleanup under genModeStrJoin
def cleanup_gen(log):
    try:
        yield "start"
        raise ValueError("boom")
    finally:
        log.append("closed")

events = []
threw = False
try:
    "".join(cleanup_gen(events))
except ValueError:
    threw = True
assert threw and events == ["closed"]

# GeneratorExit via inner subgenerator close()
def sub_closable(log):
    try:
        yield "a"
        yield "b"
    finally:
        log.append("sub_closed")

def outer_close_test(log):
    sg = sub_closable(log)
    yield next(sg)
    sg.close()
    yield "end"

sub_events = []
assert "".join(outer_close_test(sub_events)) == "aend"
assert sub_events == ["sub_closed"]
`)
	defer eng.ReleaseVM(v)
}

func TestZeroAllocSuperMethodDispatch(t *testing.T) {
	eng, v := runPySource(t, `
class Base:
    def __init__(self, x):
        self.x = x
    def compute(self, y):
        return self.x + y
    @staticmethod
    def helper(z):
        return z * 2

class Child(Base):
    def __init__(self, x, bonus):
        super().__init__(x)
        self.bonus = bonus
    def compute(self, y):
        return super().compute(y) + self.bonus
    def explicit_super(self, y):
        return super(Child, self).compute(y) + self.bonus
    def nested_super(self, a, b):
        return super().compute(super().helper(a) + b)
    def stored_super(self, y):
        s = super()
        return s.compute(y) + s.helper(1)
    def kw_super(self, val):
        return super().compute(y=val)

c = Child(10, 5)
assert c.compute(3) == 18
assert c.explicit_super(4) == 19
assert c.nested_super(3, 4) == 20
assert c.stored_super(2) == 14
assert c.kw_super(7) == 17

class SafeStr(str):
    def __new__(cls, s=""):
        return super().__new__(cls, s)

assert str(SafeStr("hello")) == "hello"
assert isinstance(SafeStr("hello"), SafeStr)
`)
	defer eng.ReleaseVM(v)

	// Verify 0 Go heap allocations per super().compute(y) call after IC warmup.
	codeSrc := `
class Base:
    def compute(self, y):
        return y + 1

class Sub(Base):
    def compute(self, y):
        return super().compute(y) + 2

inst = Sub()
def loop_super():
    return inst.compute(10)
`
	co, err := compiler.CompileString(codeSrc)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v2 := eng.NewVM()
	defer eng.ReleaseVM(v2)
	v2.RunCode(co)
	loopFn, _ := v2.Globals().Get(v2.Intern("loop_super"))
	loopCO := vm.AsCodeObject(loopFn.AsFunction().Code)

	// Warm up ICs and freelist
	for range 10 {
		res := v2.RunCode(loopCO)
		if res.AsInt64() != 13 {
			t.Fatalf("got %v, want 13", res)
		}
	}
	allocs1 := testing.AllocsPerRun(50, func() {
		res := v2.RunCode(loopCO)
		if res.AsInt64() != 13 {
			t.Fatalf("got %v, want 13", res)
		}
	})
	loopManySrc := `
def loop_super_20():
    acc = 0
    for _ in range(20):
        acc += inst.compute(10)
    return acc
`
	co20, err := compiler.CompileString(loopManySrc)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v2.RunCode(co20)
	fn20, _ := v2.Globals().Get(v2.Intern("loop_super_20"))
	co20Fn := vm.AsCodeObject(fn20.AsFunction().Code)
	for range 5 {
		if r := v2.RunCode(co20Fn); r.AsInt64() != 260 {
			t.Fatalf("got %v, want 260", r)
		}
	}
	allocs20 := testing.AllocsPerRun(50, func() {
		if r := v2.RunCode(co20Fn); r.AsInt64() != 260 {
			t.Fatalf("got %v, want 260", r)
		}
	})
	if allocs20 > allocs1 {
		t.Fatalf("super().compute(y) scaled with iterations: 1-call=%v, 20-calls=%v", allocs1, allocs20)
	}
	if allocs1 != 0 {
		t.Fatalf("super().compute(y) allocated %v times (20-calls=%v), want 0", allocs1, allocs20)
	}
}

func TestSmallDictPoolingAndEscapes(t *testing.T) {
	eng, v := runPySource(t, `
# 1. VarKWSafe + OP_BUILD_MAP_UNPACK -> OP_CALL_FUNCTION_EX preservation across multiple calls
def leaf(a, k=0, m=0):
    return a + k + m

def relay(a, **kwargs):
    x = kwargs.get("k", 0)
    y = kwargs.pop("m", 0)
    return leaf(a, **kwargs) + x + y

assert relay(1, k=2, m=3) == 8
assert relay(5) == 5

# 2. Multi-use of **kwargs inside a VarKWSafe frame must NOT wipe kwargs prematurely
def multi_use(**kwargs):
    a = kwargs.pop("x", 0)
    r1 = leaf(1, **kwargs)
    r2 = leaf(2, **kwargs)
    return a + r1 + r2

assert multi_use(x=0, k=10, m=20) == 63

# 3. Escaping **kwargs via return, container store, or growth > 5 entries
def escaping_return(**kwargs):
    return kwargs

d_ret = escaping_return(a=1, b=2)
assert d_ret == {"a": 1, "b": 2}
d_ret["c"] = 3
assert len(d_ret) == 3

captured = []
def escaping_append(**kwargs):
    captured.append(kwargs)

escaping_append(p=1, q=2)
escaping_append(r=3, s=4)
assert captured == [{"p": 1, "q": 2}, {"r": 3, "s": 4}]

# Growing past inline capacity (> 5 keys) inside a call
def big_kw(**kwargs):
    return len(kwargs), kwargs["k7"]

assert big_kw(k1=1, k2=2, k3=3, k4=4, k5=5, k6=6, k7=7) == (7, 7)

# 4. OP_BUILD_MAP + OP_BUILD_MAP_UNPACK + OP_CALL_FUNCTION_EX
def unpack_mixed(base, **kwargs):
    return leaf(base, k=1, **kwargs)

assert unpack_mixed(10, m=4) == 15
`)
	defer eng.ReleaseVM(v)

	// Verify 0 heap allocations for transient **kwargs relay after warmup.
	src := `
def target(k=0):
    return k + 1

def wrapper(**kwargs):
    return target(**kwargs)

def bench_step():
    return wrapper(k=2)
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	v2 := eng.NewVM()
	defer eng.ReleaseVM(v2)
	v2.RunCode(co)
	stepFn, _ := v2.Globals().Get(v2.Intern("bench_step"))
	stepCO := vm.AsCodeObject(stepFn.AsFunction().Code)
	for range 10 {
		res := v2.RunCode(stepCO)
		if res.AsInt64() != 3 {
			t.Fatalf("got %v, want 3", res)
		}
	}
	allocs := testing.AllocsPerRun(50, func() {
		res := v2.RunCode(stepCO)
		if res.AsInt64() != 3 {
			t.Fatalf("got %v, want 3", res)
		}
	})
	if allocs != 0 {
		t.Fatalf("VarKWSafe wrapper->target allocated %v times, want 0", allocs)
	}
}

func TestInLoopCollectGeneratorDrain(t *testing.T) {
	eng, v := runPySource(t, `
def gen_items(n):
    for i in range(n):
        yield "v" + str(i)

# 1. Direct list(gen), tuple(gen), set(gen), and list.extend(gen)
assert list(gen_items(5)) == ["v0", "v1", "v2", "v3", "v4"]
assert tuple(gen_items(4)) == ("v0", "v1", "v2", "v3")
assert tuple(gen_items(2)) == ("v0", "v1")
assert set(gen_items(3)) == {"v0", "v1", "v2"}

lst = ["a"]
lst.extend(gen_items(3))
assert lst == ["a", "v0", "v1", "v2"]

ext = lst.extend
ext(gen_items(2))
assert lst == ["a", "v0", "v1", "v2", "v0", "v1"]

list.extend(lst, gen_items(1))
assert lst == ["a", "v0", "v1", "v2", "v0", "v1", "v0"]

# 2. Empty and already-exhausted generators
def empty_gen():
    if False:
        yield 1

g_empty = empty_gen()
assert list(g_empty) == []
assert list(g_empty) == []
assert tuple(empty_gen()) == ()
assert set(empty_gen()) == set()
ext_empty = []
ext_empty.extend(empty_gen())
assert ext_empty == []

# 3. Partially consumed generators
def partial():
    yield 10
    yield 20
    yield 30

gp = partial()
assert next(gp) == 10
assert list(gp) == [20, 30]
gp2 = partial()
assert next(gp2) == 10
assert tuple(gp2) == (20, 30)

# 4. Nested yield from (generator + non-generator iterable) and drain isolation
def sub(n):
    yield n
    yield n + 1

def delegator():
    yield from sub(1)
    yield from [3, 4]
    yield from range(5, 7)

assert list(delegator()) == [1, 2, 3, 4, 5, 6]
assert tuple(delegator()) == (1, 2, 3, 4, 5, 6)
assert set(delegator()) == {1, 2, 3, 4, 5, 6}

def outer_for_join():
    yield "-".join(str(x) for x in list(delegator()))
    yield "|"
    yield ",".join(str(x) for x in tuple(sub(8)))

assert "".join(outer_for_join()) == "1-2-3-4-5-6|8,9"

# 5. Unhashable item in set(gen) raises TypeError at caller, not inside gen's try/except
def bad_set_gen(log):
    try:
        yield 1
        yield [2, 3]
    except TypeError:
        yield 99
    finally:
        log.append("done")

events = []
g_bad_set = bad_set_gen(events)
caught = False
try:
    set(g_bad_set)
except TypeError:
    caught = True
assert caught and events == []
g_bad_set.close()
assert events == ["done"]

# 6. Exception raised inside list(gen) runs finally cleanup
def cleanup_list_gen(log):
    try:
        yield 1
        raise ValueError("boom")
    finally:
        log.append("cleaned")

clean_events = []
threw = False
try:
    list(cleanup_list_gen(clean_events))
except ValueError:
    threw = True
assert threw and clean_events == ["cleaned"]
`)
	defer eng.ReleaseVM(v)
}
