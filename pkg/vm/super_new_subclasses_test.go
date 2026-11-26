package vm_test

import (
	"testing"
)

func TestSuperNewAndPrimitiveSubclasses(t *testing.T) {
	_, _ = runPySource(t, `
class BaseStr(str):
    def __new__(cls, v):
        return super().__new__(cls, v)

class SubStrA(BaseStr):
    def __new__(cls, v, tag="a"):
        inst = super().__new__(cls, v)
        inst.tag = tag
        return inst

class SubStrB(BaseStr):
    def __new__(cls, v):
        inst = super().__new__(cls, v)
        inst.extra = len(v)
        return inst

class SlottedStr(str):
    __slots__ = ("meta",)
    def __new__(cls, v, meta):
        inst = super().__new__(cls, v)
        inst.meta = meta
        return inst

class MyInt(int):
    def __new__(cls, v):
        return super().__new__(cls, v)

class MyTuple(tuple):
    def __new__(cls, iterable):
        return super().__new__(cls, iterable)

class SubTuple(MyTuple):
    def __new__(cls, iterable, name="t"):
        inst = super().__new__(cls, iterable)
        inst.name = name
        return inst

class MyBytes(bytes):
    def __new__(cls, v):
        return super().__new__(cls, v)

class MyObj:
    def __new__(cls, x):
        inst = super().__new__(cls)
        inst.x = x
        return inst

# Polymorphic super().__new__ calls across multiple subclasses
for i in range(50):
    a = SubStrA("hello", "t1")
    b = SubStrB(a)
    c = BaseStr(123)  # non-string coercion fallback
    assert isinstance(a, SubStrA) and a == "hello" and a.tag == "t1"
    assert isinstance(b, SubStrB) and b == "hello" and b.extra == 5
    assert isinstance(c, BaseStr) and c == "123"

ss = SlottedStr("abc", 42)
assert ss == "abc" and ss.meta == 42

ni = MyInt(7)
nb = MyInt(True)
ns = MyInt("99")
assert isinstance(ni, MyInt) and ni == 7
assert isinstance(nb, MyInt) and nb == 1
assert isinstance(ns, MyInt) and ns == 99

t1 = MyTuple((1, 2, 3))
t2 = MyTuple((1, 2, 3, 4, 5))
t3 = SubTuple((10, 20), "pair")
assert isinstance(t1, MyTuple) and t1 == (1, 2, 3)
assert isinstance(t2, MyTuple) and t2 == (1, 2, 3, 4, 5)
assert isinstance(t3, SubTuple) and t3 == (10, 20) and t3.name == "pair"

mb = MyBytes(b"xyz")
assert isinstance(mb, MyBytes) and mb == b"xyz"

mo = MyObj(9)
assert isinstance(mo, MyObj) and mo.x == 9

# Unrelated class must still raise TypeError
class Unrelated:
    pass

for fn, args in [
    (str.__new__, (Unrelated, "x")),
    (int.__new__, (Unrelated, 1)),
    (tuple.__new__, (Unrelated, (1, 2))),
    (bytes.__new__, (Unrelated, b"a")),
]:
    raised = False
    try:
        fn(*args)
    except TypeError:
        raised = True
    assert raised, f"expected TypeError for {fn}"
`)
}
