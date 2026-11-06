package vm_test

import "testing"

// PEP 649/749: class and module annotations are deferred, and dataclasses can
// describe self-referential types.
func TestDataclassSelfReferentialAnnotations(t *testing.T) {
	runSnippet(t, `
from dataclasses import dataclass, fields
import annotationlib
from annotationlib import Format

@dataclass
class Node:
    val: int
    next: Node | None = None

n = Node(1, Node(2))
assert n.next.val == 2 and n.next.next is None
assert [f.name for f in fields(Node)] == ['val', 'next']

s = annotationlib.get_annotations(Node, format=Format.STRING)
assert s == {'val': 'int', 'next': 'Node | None'}, s

fr = annotationlib.get_annotations(Node, format=Format.FORWARDREF)
assert fr['val'] is int
assert isinstance(fr['next'], annotationlib.ForwardRef) or fr['next'] is not None

v = annotationlib.get_annotations(Node, format=Format.VALUE)
assert v['val'] is int
assert v['next'] == (Node | None)
assert Node.__annotations__ is Node.__annotations__
`)
}

func TestClassAnnotationsAreLazy(t *testing.T) {
	runSnippet(t, `
import annotationlib
from annotationlib import Format

class A:
    x: int
    y: Later
    z: list[Later2]

assert '__annotations__' not in A.__dict__
assert callable(A.__dict__['__annotate_func__'])
try:
    A.__annotations__
except NameError as e:
    assert 'Later' in str(e)
else:
    raise AssertionError("expected NameError")

Later = int
Later2 = str
assert A.__annotations__ == {'x': int, 'y': int, 'z': list[str]}
assert A.__annotations__ is A.__annotations__
assert '__annotations_cache__' in A.__dict__
assert A.__annotate__ is A.__dict__['__annotate_func__']
try:
    A.__annotate__(3)
except NotImplementedError:
    pass
else:
    raise AssertionError("format > 2 must raise NotImplementedError")
assert annotationlib.get_annotations(A, format=Format.STRING) == {'x': 'int', 'y': 'Later', 'z': 'list[Later2]'}

class B:
    pass
assert B.__annotate__ is None
assert B.__annotations__ == {}
B.__annotate__ = lambda fmt: {'q': fmt}
assert B.__annotations__ == {'q': 1}  # installing an annotate hook resets the cache
class C(A):
    pass
assert C.__annotations__ == {}  # annotations are not inherited
`)
}

func TestClassAnnotationScopes(t *testing.T) {
	runSnippet(t, `
def factory():
    T = int
    class K:
        inner = float
        a: T
        b: inner
        c: K
    return K

K = factory()
assert K.__annotations__ == {'a': int, 'b': float, 'c': K}

class Outer:
    x: int
    class Inner:
        y: Outer
assert Outer.Inner.__annotations__ == {'y': Outer}

class Meta(type):
    @classmethod
    def __prepare__(mcs, name, bases, **kw):
        return {}
class M(metaclass=Meta):
    a: int
    if True:
        b: "str"
    for i in range(1):
        c: float
    try:
        d: bytes
    except Exception:
        pass
assert M.__annotations__ == {'a': int, 'b': 'str', 'c': float, 'd': bytes}

class Skipped:
    if False:
        never: int
    always: str
assert Skipped.__annotations__ == {'always': str}
`)
}

func TestClassAnnotationsSetDelete(t *testing.T) {
	runSnippet(t, `
class A:
    x: int
A.__annotations__ = {'q': 1}
assert A.__annotations__ == {'q': 1}
assert A.__annotate__ is None
del A.__annotations__
assert A.__annotations__ == {}

class W:
    __annotations__ = {'explicit': 1}
    zz: int
assert W.__annotations__ == {'explicit': 1}

try:
    int.__annotate__
except AttributeError:
    pass
else:
    raise AssertionError
try:
    int.__annotations__
except AttributeError:
    pass
else:
    raise AssertionError
`)
}

func TestModuleAnnotationsAreLazy(t *testing.T) {
	runSnippet(t, `
import sys, types, annotationlib
mod = sys.modules[__name__]
x: int
y: Forward = 1
if True:
    cond: float
if False:
    skipped: complex

assert callable(__annotate__)
try:
    __annotations__
except NameError:
    pass
else:
    raise AssertionError("module __annotations__ must not be a plain global")
try:
    mod.__annotations__
except NameError:
    pass
else:
    raise AssertionError("Forward is undefined")

Forward = str
assert mod.__annotations__ == {'x': int, 'y': str, 'cond': float}
assert '__annotations__' in globals()
assert __annotate__(1) == {'x': int, 'y': str, 'cond': float}
assert annotationlib.get_annotations(mod, format=annotationlib.Format.STRING) == {'x': 'int', 'y': 'Forward', 'cond': 'float', 'skipped': 'complex'}

m = types.ModuleType('fresh')
assert m.__annotations__ == {}
assert m.__annotate__ is None
`)
}

func TestExecModuleAnnotations(t *testing.T) {
	runSnippet(t, `
ns = {}
exec("a: int = 1\nclass K:\n    b: a\nr = K.__annotations__\nif a:\n    c: str\nr2 = __annotate__(1)", ns)
assert ns['r'] == {'b': 1}, ns['r']
assert ns['r2'] == {'a': int, 'c': str}
`)
}

// Memory safety: native objects must never be reinterpreted as PyInstance.
func TestNativeObjectIntrospection(t *testing.T) {
	runSnippet(t, `
import io, re, copy, asyncio

for ctor in (io.StringIO, io.BytesIO):
    o = ctor()
    o.custom = 1
    assert vars(o)['custom'] == 1
    assert 'custom' in dir(o)
    assert copy.copy(o) is not None
    setattr(o, 'other', 2)
    assert o.other == 2

p = re.compile('a(b)')
m = p.match('ab')
assert isinstance(dir(p), list) and isinstance(dir(m), list)
assert copy.copy(p) is p
assert copy.copy(m) is m
for obj in (p, m):
    try:
        obj.attr = 1
    except AttributeError:
        pass
    else:
        raise AssertionError("re objects reject new attributes")
    try:
        vars(obj)
    except TypeError:
        pass
    else:
        raise AssertionError("vars() needs __dict__")
assert m[0] == 'ab' and m[1] == 'b'
assert p.prefixmatch('abz').group() == 'ab'

async def work():
    return 5
async def main():
    task = asyncio.create_task(work())
    task.tag = 'x'
    setattr(task, 'other', 1)
    assert task.tag == 'x'
    assert isinstance(dir(task), list)
    assert await task == 5
    return task.other
assert asyncio.run(main()) == 1
`)
}

func TestFormatSpecMatrix(t *testing.T) {
	runSnippet(t, `
assert f"{42:>08x}" == "0000002a"
assert f"{1234567.89:,.2f}" == "1,234,567.89"
assert f"{'hi':*^10.2s}" == "****hi****"
assert f"{-42:=+6d}" == "-   42"
assert f"{255:#b}" == "0b11111111"
assert f"{255:#X}" == "0XFF"
assert f"{1234567:_d}" == "1_234_567"
assert f"{0.000123:.2e}" == "1.23e-04"
assert f"{0.25:%}" == "25.000000%"
assert f"{1e20:g}" == "1e+20"
assert f"{3.14159:10.3f}|" == "     3.142|"
assert f"{'x':<5}|{'y':>5}|{'z':^5}" == "x    |    y|  z  "
assert format(10**20, ',') == "100,000,000,000,000,000,000"
assert format(True, 'd') == '1'
assert format(65, 'c') == 'A'
assert '{:{w}.{p}f}'.format(3.14159, w=8, p=2) == '    3.14'
for bad in ((object(), 'x'), (1, 's'), ('s', 'd'), (1.5, 'd')):
    try:
        format(*bad)
    except (TypeError, ValueError):
        pass
    else:
        raise AssertionError(bad)
`)
}

func TestComplexNumbers(t *testing.T) {
	runSnippet(t, `
assert (1+2j)*(3-4j) == (11+2j)
assert abs(3+4j) == 5.0
assert (1+2j) + 1 == (2+2j)
assert (1+2j) - (1+2j) == 0j
assert (1+2j) / (1-1j) == (-0.5+1.5j)
assert (1+2j) ** 2 == (-3+4j)
assert -(1+2j) == (-1-2j)
assert (3+4j).real == 3.0 and (3+4j).imag == 4.0
assert (3+4j).conjugate() == (3-4j)
assert complex(1, 2) == 1+2j and complex('1+2j') == 1+2j and complex('3') == 3+0j
assert repr(1+2j) == '(1+2j)' and repr(3j) == '3j' and repr(-0j) == '(-0-0j)'
assert str(1.5-2.5j) == '(1.5-2.5j)'
assert 1j * 1j == -1
assert 2j == 2.0j and 1_0j == 10j
assert hash(1+0j) == hash(1) and hash(3j) == hash(3j)
assert bool(0j) is False and bool(1j) is True
assert format(1+2j, '.2f') == '1.00+2.00j'
assert isinstance(1j, complex)
assert complex(1, 2) != 1
for op in ('<', '<=', '>', '>='):
    try:
        eval('(1+2j) ' + op + ' (2+3j)')
    except TypeError as e:
        assert "not supported between instances of 'complex' and 'complex'" in str(e), e
    else:
        raise AssertionError(op)
try:
    1j / 0
except ZeroDivisionError:
    pass
else:
    raise AssertionError
import ast
tree = ast.parse('4j', mode='eval')
assert tree.body.value == 4j
`)
}

func TestBigIntegerPower(t *testing.T) {
	runSnippet(t, `
assert 2 ** 100 == 1267650600228229401496703205376
assert (-2) ** 101 == -2535301200456458802993406410752
assert 10 ** 30 // 10 ** 28 == 100
assert 2 ** -2 == 0.25
assert (-8) ** (1/3) != 0
`)
}

func TestGeneratorExpressionEagerIterable(t *testing.T) {
	runSnippet(t, `
try:
    g = (x for x in 123)
except TypeError as e:
    assert str(e) == "'int' object is not iterable", str(e)
else:
    raise AssertionError("outermost iterable must be evaluated eagerly")

try:
    iter(123)
except TypeError as e:
    assert str(e) == "'int' object is not iterable", str(e)
else:
    raise AssertionError

# Only the outermost iterable is eager; later ones stay lazy.
def source():
    raise RuntimeError("late")
    yield
g = (y for x in [1] for y in source())
try:
    next(g)
except RuntimeError:
    pass
else:
    raise AssertionError

lst = [1, 2, 3]
g = (x * 2 for x in lst)
lst.append(4)
assert list(g) == [2, 4, 6, 8]
`)
}

func TestQualifiedTypeRepr(t *testing.T) {
	runSnippet(t, `
import collections
class A:
    pass
def f():
    class B:
        pass
    return B
assert repr(A) == "<class '__main__.A'>"
assert repr(f()) == "<class '__main__.f.<locals>.B'>"
assert repr(int) == "<class 'int'>"
assert repr(type) == "<class 'type'>"
assert repr(collections.OrderedDict) == "<class 'collections.OrderedDict'>"
assert str(A) == repr(A) and repr([A]) == "[<class '__main__.A'>]"
assert repr(A | None) == "__main__.A | None"
assert repr(list[A]) == "list[__main__.A]"
`)
}

func TestNamedUnicodeEscapes(t *testing.T) {
	runSnippet(t, `
import unicodedata, re
assert "\N{BULLET}" == "\u2022"
assert "\N{greek small letter alpha}\N{EM DASH}" == "\u03b1\u2014"
assert "\N{LINE FEED}" == "\n" and "\N{NULL}" == "\x00"
assert "\N{CJK UNIFIED IDEOGRAPH-4E2D}" == "\u4e2d"
assert "\N{HANGUL SYLLABLE GAG}" == "\uac01"
assert f"\N{BULLET} {1 + 1} \N{EM DASH}" == "\u2022 2 \u2014"
assert r"\N{BULLET}" == "\\N{BULLET}"
assert unicodedata.lookup("SNOWMAN") == "\u2603"
assert unicodedata.name("a") == "LATIN SMALL LETTER A"
assert unicodedata.name("\n", "dflt") == "dflt"
assert unicodedata.category("A") == "Lu" and unicodedata.category("1") == "Nd"
assert unicodedata.normalize("NFD", "\xe9") == "e\u0301"
assert unicodedata.normalize("NFC", "e\u0301") == "\xe9"
try:
    unicodedata.lookup("NO SUCH CHARACTER")
except KeyError:
    pass
else:
    raise AssertionError
try:
    unicodedata.name("\n")
except ValueError:
    pass
else:
    raise AssertionError
assert re.match(r"\N{BULLET}+", "\u2022\u2022x").group() == "\u2022\u2022"
assert re.findall(r"[\N{GREEK SMALL LETTER ALPHA}-\N{GREEK SMALL LETTER GAMMA}]", "\u03b1\u03b2\u03b4\u03b3") == ["\u03b1", "\u03b2", "\u03b3"]
`)
}

// Regression: builtin calls from a frame entered via a type call out of a
// generator must not re-point the frame at the VM stack.
func TestBuiltinCallInInitFromGenerator(t *testing.T) {
	runSnippet(t, `
class S:
    __slots__ = ('a',)
    def __init__(self, node, *, sd):
        assert isinstance(node, str)
        self.a = len(node) + abs(-1)
    def mk(self, node):
        return S(node, sd=self)
def g(obj):
    yield obj.mk("xyz")
s = S("x", sd=1)
assert next(g(s)).a == 4
`)
}

func TestZipStrictAndLazy(t *testing.T) {
	runSnippet(t, `
import itertools
assert list(zip([1], [2], strict=True)) == [(1, 2)]
for a, b, msg in (([1, 2], [1], "zip() argument 2 is shorter than argument 1"),
                  ([1], [1, 2], "zip() argument 2 is longer than argument 1")):
    try:
        list(zip(a, b, strict=True))
    except ValueError as e:
        assert str(e) == msg, str(e)
    else:
        raise AssertionError
assert list(zip(itertools.count(), 'abc')) == [(0, 'a'), (1, 'b'), (2, 'c')]
assert {1, 2, 3} - {1} == {2, 3}
`)
}
