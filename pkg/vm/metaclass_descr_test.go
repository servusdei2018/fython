package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runPy(t *testing.T, src string) (val.Value, *vm.VM) {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString error: %v", err)
	}
	v := vm.NewVM()
	res := v.RunCode(co)
	return res, v
}

func getGlobal(t *testing.T, v *vm.VM, name string) val.Value {
	t.Helper()
	got, ok := v.GetGlobal(name)
	if !ok {
		t.Fatalf("global %q not found", name)
	}
	return got
}

// 1. Custom metaclass using __prepare__ to track definition order and kwargs forwarding to __init_subclass__
func TestMetaclassPrepareAndInitSubclass(t *testing.T) {
	src := `
order = []

class Meta(type):
    @classmethod
    def __prepare__(mcls, name, bases, **kwds):
        class OrderDict(dict):
            def __setitem__(self, key, value):
                order.append(key)
                super().__setitem__(key, value)
        return OrderDict()

    def __new__(mcls, name, bases, ns, **kwds):
        return super().__new__(mcls, name, bases, ns)

class Base:
    tag = None
    def __init_subclass__(cls, tag=None, **kwds):
        super().__init_subclass__(**kwds)
        cls.tag = tag

class Sub(Base, metaclass=Meta, tag="special_tag"):
    first = 1
    second = 2
    third = 3
`
	_, v := runPy(t, src)
	tag, _ := getGlobal(t, v, "Sub").AsType().Dict.Get(val.InternString("tag"))
	if tag.AsString() != "special_tag" {
		t.Fatalf("Sub.tag = %v, want %q", tag, "special_tag")
	}

	orderVal := getGlobal(t, v, "order")
	if !orderVal.IsList() {
		t.Fatalf("order = %v, want list", orderVal)
	}
	var keys []string
	for _, item := range orderVal.AsList().Items {
		keys = append(keys, item.AsString())
	}
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, "first,second,third") {
		t.Fatalf("expected attribute order to contain 'first,second,third', got: %v", joined)
	}
}

// 2. Descriptors with __set_name__, __get__, __set__ and zero-arg super() across 3 levels
func TestDescriptorAndZeroArgSuperHierarchy(t *testing.T) {
	src := `
events = []

class LoggedDescriptor:
    def __set_name__(self, owner, name):
        self.name = name
        self.owner = owner
        events.append("set_name:" + name + ":" + owner.__name__)

    def __get__(self, instance, owner):
        if instance is None:
            return self
        return instance.val

    def __set__(self, instance, value):
        events.append("set:" + self.name + ":" + str(value))
        instance.val = value

class Grandparent:
    x = LoggedDescriptor()
    def __init__(self, v):
        self.x = v
    def get_val(self):
        return self.x + 10

class Parent(Grandparent):
    def get_val(self):
        return super().get_val() + 100

class Child(Parent):
    def get_val(self):
        return super().get_val() + 1000

c = Child(5)
res = c.get_val()
`
	_, v := runPy(t, src)
	res := getGlobal(t, v, "res")
	if !res.IsInt64() || res.AsInt64() != 1115 {
		t.Fatalf("c.get_val() = %v, want 1115", res)
	}

	eventsVal := getGlobal(t, v, "events")
	var evs []string
	for _, item := range eventsVal.AsList().Items {
		evs = append(evs, item.AsString())
	}
	if len(evs) < 2 || evs[0] != "set_name:x:Grandparent" || evs[1] != "set:x:5" {
		t.Fatalf("unexpected events: %v", evs)
	}
}

// 3. Subclass reflected operator precedence: Base() + Sub() calls Sub.__radd__ before Base.__add__
func TestSubclassReflectedPrecedence(t *testing.T) {
	src := `
calls = []

class Base:
    def __add__(self, other):
        calls.append("Base.__add__")
        return NotImplemented

class Sub(Base):
    def __radd__(self, other):
        calls.append("Sub.__radd__")
        return "sub_radd_success"

b = Base()
s = Sub()
res = b + s
`
	_, v := runPy(t, src)
	res := getGlobal(t, v, "res")
	if res.AsString() != "sub_radd_success" {
		t.Fatalf("b + s = %v, want 'sub_radd_success'", res)
	}

	callsVal := getGlobal(t, v, "calls")
	var calls []string
	for _, item := range callsVal.AsList().Items {
		calls = append(calls, item.AsString())
	}
	if len(calls) != 1 || calls[0] != "Sub.__radd__" {
		t.Fatalf("expected ['Sub.__radd__'], got: %v", calls)
	}
}

// 4. Non-subclass reflected operator fallback on NotImplemented
func TestNonSubclassReflectedFallback(t *testing.T) {
	src := `
calls = []

class Left:
    def __add__(self, other):
        calls.append("Left.__add__")
        return NotImplemented

class Right:
    def __radd__(self, other):
        calls.append("Right.__radd__")
        return "right_radd_success"

l = Left()
r = Right()
res = l + r
`
	_, v := runPy(t, src)
	res := getGlobal(t, v, "res")
	if res.AsString() != "right_radd_success" {
		t.Fatalf("l + r = %v, want 'right_radd_success'", res)
	}

	callsVal := getGlobal(t, v, "calls")
	var calls []string
	for _, item := range callsVal.AsList().Items {
		calls = append(calls, item.AsString())
	}
	if len(calls) != 2 || calls[0] != "Left.__add__" || calls[1] != "Right.__radd__" {
		t.Fatalf("expected ['Left.__add__', 'Right.__radd__'], got: %v", calls)
	}
}

// 5. Slotted classes: Shape IC speed, forbid dynamic attributes
func TestSlottedClasses(t *testing.T) {
	src := `
class Slotted:
    __slots__ = ('a', 'b')
    def __init__(self, a, b):
        self.a = a
        self.b = b

s = Slotted(10, 20)
val_a = s.a
val_b = s.b

caught_dyn = False
try:
    s.c = 30
except AttributeError:
    caught_dyn = True
`
	_, v := runPy(t, src)
	a := getGlobal(t, v, "val_a")
	b := getGlobal(t, v, "val_b")
	if a.AsInt64() != 10 || b.AsInt64() != 20 {
		t.Fatalf("slotted attrs = %v, %v, want 10, 20", a, b)
	}
	caught := getGlobal(t, v, "caught_dyn")
	if !caught.IsBool() || !caught.AsBool() {
		t.Fatalf("expected dynamic attribute assignment on slotted class to raise AttributeError")
	}
}

// 6. PEP 560 __mro_entries__ support
func TestMROEntries(t *testing.T) {
	src := `
class GenericAlias:
    def __init__(self, target):
        self.target = target
    def __mro_entries__(self, bases):
        return (self.target,)

MyInt = GenericAlias(int)

class Foo(MyInt):
    pass

is_sub = issubclass(Foo, int)
f = Foo(42)
res = int(f) + 8
orig = Foo.__orig_bases__[0] is MyInt
`
	_, v := runPy(t, src)
	isSub := getGlobal(t, v, "is_sub")
	if !isSub.IsBool() || !isSub.AsBool() {
		t.Fatalf("expected Foo to be subclass of int")
	}
	res := getGlobal(t, v, "res")
	if !res.IsInt64() || res.AsInt64() != 50 {
		t.Fatalf("res = %v, want 50", res)
	}
	orig := getGlobal(t, v, "orig")
	if !orig.IsBool() || !orig.AsBool() {
		t.Fatalf("expected Foo.__orig_bases__[0] to be MyInt")
	}
}

// 7. Custom __getattribute__ and PIC bypass
func TestCustomGetAttributePICBypass(t *testing.T) {
	src := `
class Intercept:
    def __init__(self):
        self.val = 42
    def __getattribute__(self, name):
        if name == "val":
            return 999
        return super().__getattribute__(name)

obj = Intercept()
res = obj.val
`
	_, v := runPy(t, src)
	res := getGlobal(t, v, "res")
	if !res.IsInt64() || res.AsInt64() != 999 {
		t.Fatalf("obj.val = %v, want 999 (intercepted by __getattribute__)", res)
	}
}
