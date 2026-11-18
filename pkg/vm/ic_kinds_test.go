package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runPySource(t *testing.T, src string) (*vm.Engine, *vm.VM) {
	t.Helper()
	eng := vm.NewEngine()
	eng.SetCompileHook(compiler.CompileSource)
	v := eng.AcquireVM()
	co, err := compiler.CompileString(src)
	if err != nil {
		eng.ReleaseVM(v)
		t.Fatalf("compile failed: %v", err)
	}
	v.RunCode(co)
	exc := v.UnhandledException()
	if !exc.IsNone() && exc.HeaderPtr() != nil {
		msg := v.FormatException(exc)
		eng.ReleaseVM(v)
		t.Fatalf("python exception: %s", msg)
	}
	return eng, v
}

func TestICKindsCorrectnessAndInvalidation(t *testing.T) {
	eng, v := runPySource(t, `
from collections import namedtuple

class SlotsBox:
    __slots__ = ("x", "y")
    def __init__(self, x):
        self.x = x

sb = SlotsBox(10)
sb.y = 20
acc_slots = 0
for _ in range(10):
    acc_slots += sb.x + sb.y
    sb.y += 1

# Unbound slot raises AttributeError
sb2 = SlotsBox(1)
got_unbound_err = False
try:
    _ = sb2.y
except AttributeError:
    got_unbound_err = True

# Property getter + setter + generator property + exception property
class PropBox:
    def __init__(self, v):
        self._v = v
    @property
    def val(self):
        return self._v * 2
    @val.setter
    def val(self, nv):
        self._v = nv
    @property
    def gen(self):
        yield self._v
        yield self._v + 1
    @property
    def boom(self):
        raise ValueError("bad")

pb = PropBox(3)
acc_prop = 0
for i in range(10):
    pb.val = i + 1
    acc_prop += pb.val

gen_vals = list(pb.gen)
got_prop_exc = False
try:
    _ = pb.boom
except ValueError:
    got_prop_exc = True

# Class constant + classmethod + staticmethod + method on type + bound method value
class Config:
    SCALE = 7
    def double(self, n):
        return n * 2
    @staticmethod
    def add_one(n):
        return n + 1
    @classmethod
    def get_scale(cls):
        return cls.SCALE

cfg = Config()
acc_cls = 0
bm = cfg.double
for _ in range(10):
    acc_cls += cfg.SCALE + Config.SCALE + cfg.add_one(1) + Config.add_one(1) + Config.get_scale()
    bm = cfg.double

# Monkey-patch Config.SCALE and verify cached sites update
Config.SCALE = 11
after_patch = cfg.SCALE + Config.SCALE + Config.get_scale()

# Soundness fix: AttrPICEntry must invalidate if class gains a property
class Dyn:
    pass
d = Dyn()
d.x = 5
def get_dyn_x(obj):
    return obj.x
before_prop = [get_dyn_x(d) for _ in range(5)]
Dyn.x = property(lambda self: 99)
after_prop = get_dyn_x(d)

# NamedTuple field getter
Pt = namedtuple("Pt", ["px", "py"])
pt = Pt(3, 4)
acc_nt = 0
for _ in range(10):
    acc_nt += pt.px + pt.py

# super() method and constant
class Base:
    CONST = 100
    def compute(self, n):
        return n + self.CONST
class Sub(Base):
    def compute(self, n):
        return super().compute(n) + super().CONST

sub = Sub()
acc_super = 0
for _ in range(10):
    acc_super += sub.compute(5)

# Monkey-patch Base.compute after super() IC warmup
Base.compute = lambda self, n: n + 200
after_super_patch = sub.compute(5)
`)
	defer eng.ReleaseVM(v)

	globals := v.Globals()
	checkInt := func(name string, want int64) {
		t.Helper()
		got, ok := globals.Get(val.InternString(name))
		if !ok || !got.IsInt64() || got.AsInt64() != want {
			t.Fatalf("%s = %v (ok=%v), want %d", name, got, ok, want)
		}
	}
	checkBool := func(name string, want bool) {
		t.Helper()
		got, ok := globals.Get(val.InternString(name))
		if !ok || !got.IsBool() || got.AsBool() != want {
			t.Fatalf("%s = %v (ok=%v), want %v", name, got, ok, want)
		}
	}

	checkInt("acc_slots", 10*10+(20+29)*10/2)
	checkBool("got_unbound_err", true)
	checkInt("acc_prop", 110)
	checkBool("got_prop_exc", true)
	checkInt("acc_cls", 10*(7+7+2+2+7))
	checkInt("after_patch", 33)
	checkInt("after_prop", 99)
	checkInt("acc_nt", 70)
	checkInt("acc_super", 10*205)
	checkInt("after_super_patch", 305)
}

func TestICKindsMultiThreadedMode(t *testing.T) {
	val.SetMultiThreaded(true)
	defer val.SetMultiThreaded(false)
	eng, v := runPySource(t, `
class S:
    __slots__ = ("a",)
    def __init__(self, a):
        self.a = a
s = S(4)
res = 0
for _ in range(10):
    res += s.a
    s.a += 1
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("res"))
	if !ok || got.AsInt64() != 85 {
		t.Fatalf("MT slots res = %v, want 85", got)
	}
}

func TestICKindsStatsHit(t *testing.T) {
	if !vm.ICStatsEnabled() {
		t.Skip("run with -tags icstats to verify IC hit counters")
	}
	vm.ResetICStats()
	eng, v := runPySource(t, `
from collections import namedtuple
class Box:
    __slots__ = ("x",)
    K = 5
    def __init__(self, x):
        self.x = x
    @property
    def p(self):
        return self.x + 1
    @p.setter
    def p(self, val):
        self.x = val
    def m(self):
        return self.x
class SubBox(Box):
    def m(self):
        return super().m()

Pt = namedtuple("Pt", ["a", "b"])
pt = Pt(1, 2)
b = SubBox(1)
tot = 0
for i in range(20):
    b.p = i
    tot += b.p + b.K + Box.K + pt.a + b.m()
    fn = b.m
`)
	defer eng.ReleaseVM(v)
	for _, k := range []vm.ICKind{
		vm.ICKindProperty,
		vm.ICKindPropertySet,
		vm.ICKindClassAttr,
		vm.ICKindTypeAttr,
		vm.ICKindTupleGetter,
		vm.ICKindSuperMethod,
		vm.ICKindClassFunc,
	} {
		if vm.ICSlotHitCount(k) == 0 {
			t.Fatalf("expected non-zero IC hits for kind %d", k)
		}
	}
}
