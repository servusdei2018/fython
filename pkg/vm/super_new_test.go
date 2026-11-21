package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestSuperNewPrimitiveSubclasses(t *testing.T) {
	eng, v := runPySource(t, `
class MyStr(str):
    def __new__(cls, s):
        return super().__new__(cls, s)

class MyStrEx(str):
    def __new__(cls, s, tag):
        inst = super().__new__(cls, s)
        inst.tag = tag
        return inst

class MyStrInit(str):
    def __init__(self, s):
        self.label = "lbl:" + s

class MyInt(int):
    def __new__(cls, n):
        return super().__new__(cls, n)

class MyIntEx(int):
    def __new__(cls, n, unit):
        inst = super(MyIntEx, cls).__new__(cls, n)
        inst.unit = unit
        return inst

class MyBytes(bytes):
    def __new__(cls, b):
        return super().__new__(cls, b)

class MyBytesEx(bytes):
    def __new__(cls, b, kind):
        inst = super(MyBytesEx, cls).__new__(cls, b)
        inst.kind = kind
        return inst

class SimpleObj:
    def __new__(cls):
        return super().__new__(cls)

class DirectStr(str):
    pass

class DirectInt(int):
    pass

class DirectBytes(bytes):
    pass

# Zero-slot super().__new__ in a hot loop (exercises quickened super/method/builtin paths)
for i in range(25):
    s = MyStr("hello")
    assert isinstance(s, MyStr)
    assert isinstance(s, str)
    assert s == "hello"
    assert s.upper() == "HELLO"

    s2 = MyStr(s)
    assert isinstance(s2, MyStr)
    assert s2 == "hello"

    sx = MyStrEx("world", 42)
    assert isinstance(sx, MyStrEx)
    assert sx == "world"
    assert sx.tag == 42

    si = MyStrInit("abc")
    assert isinstance(si, MyStrInit)
    assert si == "abc"
    assert si.label == "lbl:abc"

    n = MyInt(123)
    assert isinstance(n, MyInt)
    assert isinstance(n, int)
    assert n == 123
    assert n + 7 == 130

    nx = MyIntEx(99, "ms")
    assert isinstance(nx, MyIntEx)
    assert nx == 99
    assert nx.unit == "ms"
    assert nx + 1 == 100

    b = MyBytes(b"xyz")
    assert isinstance(b, MyBytes)
    assert isinstance(b, bytes)
    assert b == b"xyz"
    assert len(b) == 3

    bx = MyBytesEx(b"ab", "raw")
    assert isinstance(bx, MyBytesEx)
    assert bx == b"ab"
    assert bx.kind == "raw"

    o = SimpleObj()
    assert isinstance(o, SimpleObj)

    ds = DirectStr("fast")
    di = DirectInt(77)
    db = DirectBytes(b"ok")
    assert ds == "fast" and isinstance(ds, DirectStr)
    assert di == 77 and isinstance(di, DirectInt)
    assert db == b"ok" and isinstance(db, DirectBytes)
`)
	defer eng.ReleaseVM(v)
}

func TestTryFastPrimitiveDunderNewAllocs(t *testing.T) {
	eng, v := runPySource(t, `
class MyStr(str):
    pass

class SlottedStr(str):
    __slots__ = ("extra",)

class MyInt(int):
    pass

class MyBytes(bytes):
    pass

class MyObj:
    pass
`)
	defer eng.ReleaseVM(v)

	g := v.Globals()
	mustType := func(name string) val.Value {
		tv, ok := g.Get(val.InternString(name))
		if !ok || !tv.IsType() {
			t.Fatalf("missing type %s", name)
		}
		return tv
	}
	mustNew := func(tp *val.PyType) val.Value {
		nw, ok := tp.Dict.Get(val.DunderNew)
		if !ok {
			t.Fatalf("missing __new__ on %s", tp.Name)
		}
		return nw
	}

	myStr := mustType("MyStr")
	slottedStr := mustType("SlottedStr")
	myInt := mustType("MyInt")
	myBytes := mustType("MyBytes")
	myObj := mustType("MyObj")

	strNew := mustNew(val.StrType)
	intNew := mustNew(val.IntType)
	bytesNew := mustNew(val.BytesType)

	strArg := val.InternString("hello")
	intArg := val.Int64Val(42)
	bytesArg := val.NewBytesVal([]byte("abc"))

	if _, ok := val.TryFastPrimitiveDunderNew(strNew, []val.Value{myStr, strArg}); !ok {
		t.Fatalf("expected str.__new__ fast path for MyStr")
	}
	if _, ok := val.TryFastPrimitiveDunderNew(strNew, []val.Value{slottedStr, strArg}); ok {
		t.Fatalf("expected str.__new__ fast path to reject slotted subclass")
	}
	if _, ok := val.TryFastPrimitiveDunderNew(intNew, []val.Value{myInt, intArg}); !ok {
		t.Fatalf("expected int.__new__ fast path for MyInt")
	}
	if _, ok := val.TryFastPrimitiveDunderNew(bytesNew, []val.Value{myBytes, bytesArg}); !ok {
		t.Fatalf("expected bytes.__new__ fast path for MyBytes")
	}
	if _, ok := val.TryFastPrimitiveDunderNew(val.ObjectNewBuiltin, []val.Value{myObj}); !ok {
		t.Fatalf("expected object.__new__ fast path for MyObj")
	}

	strArgs := []val.Value{myStr, strArg}
	allocs := testing.AllocsPerRun(50, func() {
		inst, ok := val.TryFastPrimitiveDunderNew(strNew, strArgs)
		if !ok || !inst.IsInstance() {
			t.Fatalf("fast path failed inside alloc check")
		}
	})
	if allocs > 1 {
		t.Fatalf("expected <= 1 alloc for PyInstance bootstrap fast path, got %.1f", allocs)
	}
}
