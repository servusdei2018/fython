package vm_test

import "testing"

func TestLiveInstanceDict(t *testing.T) {
	runSnippet(t, `
class O:
    pass
obj = O()
obj.x = 1
d = vars(obj)
assert isinstance(d, dict)
d['x'] = 123
assert obj.x == 123
obj.y = 456
assert d['y'] == 456
assert 'y' in d and len(d) == 2
assert list(d) == ['x', 'y'], list(d)
del d['x']
assert not hasattr(obj, 'x')
assert 'x' not in d and len(d) == 1
d['z'] = 7
assert obj.z == 7
assert obj.__dict__ == {'y': 456, 'z': 7}
assert d.pop('z') == 7 and not hasattr(obj, 'z')
assert d.setdefault('w', 9) == 9 and obj.w == 9
d.update({'a': 1}, b=2)
assert obj.a == 1 and obj.b == 2
assert dict(d) == {'y': 456, 'w': 9, 'a': 1, 'b': 2}
snap = dict(d)
snap['y'] = 0
assert obj.y == 456
assert (d | {'q': 1})['q'] == 1 and not hasattr(obj, 'q')
assert sorted(d.keys()) == ['a', 'b', 'w', 'y']
assert sorted(d.values()) == [1, 2, 9, 456]
assert sorted(d.items())[0] == ('a', 1)
d.clear()
assert obj.__dict__ == {} and not hasattr(obj, 'y')
obj.__dict__ = {'m': 1, 'n': 2}
assert obj.m == 1 and obj.n == 2
obj.__dict__['m'] = 5
assert obj.m == 5
`)
}

func TestStringLiterals(t *testing.T) {
	runSnippet(t, `
x = 1
assert r"a\b" == "a" + chr(92) + "b" and len(r"\n") == 2
assert "\x41\101\u00e9\U0001F600\a\v\f" == "AA\xe9" + chr(0x1F600) + chr(7) + chr(11) + chr(12)
assert "\ud83d\ude00" == chr(0x1F600)
assert "a\
b" == "ab"
assert b"\x41\101\n" == bytes([65, 65, 10]) and rb"\x41" == b"\\x41"
assert b"BLUE" == bytes([66, 76, 85, 69])
assert f"{{x}} '{x}' \x41" == "{x} '1' A"
assert rf"\n{x}" == chr(92) + "n1"
assert f"{x:3}" == "  1"
`)
}

func TestReEngine(t *testing.T) {
	runSnippet(t, `
import re
assert re.sub(r"(?P<a>\w)(?P=a)", r"<\g<a>\1>", "aabbcd") == "<aa><bb>cd"
assert re.split(r"(?<=,)\s*", "a, b,c") == ["a,", "b,", "c"]
m = re.search(r"(?<=é)(\w+)", "héllo é wörld")
assert m.span() == (2, 5) and m.group(1) == "llo" and m.lastindex == 1
mb = re.search(rb"(?<=\xc3\xa9)(\w+)", "héllo".encode())
assert mb.span() == (3, 6)
assert re.findall(rb"\d+", b"a12b345") == [b"12", b"345"]
assert re.sub("x*", "-", "abxd") == "-a-b--d-"
assert [m.span() for m in re.finditer(r"\b", "ab cd")] == [(0, 0), (2, 2), (3, 3), (5, 5)]
assert re.match(r"(a)?(?(1)b|c)", "ab").group() == "ab"
assert re.match(r"(?>a+)b", "aaab") and not re.match(r"(?>a+)ab", "aaab")
assert re.match(r"a++a", "aaa") is None
assert re.match(r"(?i)straße", "STRASSE") is None and re.match(r"(?i)abc", "ABC")
assert re.fullmatch(r"a.c", "a\nc", re.S) and not re.fullmatch(r"a.c", "a\nc")
assert re.compile("a", re.I | re.M).flags == re.I | re.M | re.U
try:
    re.compile("(?<=a+)b")
except re.error as e:
    assert e.msg == "look-behind requires fixed-width pattern" and e.pos is None
else:
    raise AssertionError
try:
    re.compile("a(")
except re.error as e:
    assert e.pos == 1
assert re.escape("a.b") == "a\\.b"
assert re.compile("(?P<n>x)").groupindex == {"n": 1}
`)
}

func TestTextwrapUpstream(t *testing.T) {
	runSnippet(t, `
import textwrap
assert textwrap.dedent("   a\n    b\n   c") == "a\n b\nc"
assert textwrap.fill("The quick brown fox jumps over the lazy dog", 20) == "The quick brown fox\njumps over the lazy\ndog"
assert textwrap.shorten("Hello  world!", width=11) == "Hello [...]"
assert textwrap.indent("a\nb\n\nc", "> ") == "> a\n> b\n\n> c"
assert min([3, 1, 2], key=lambda v: -v) == 3 and max([], default=7) == 7
`)
}

func TestAstTransformUnparseExec(t *testing.T) {
	runSnippet(t, `
import ast, copy

src = '''
def add(a, b):
    """Add."""
    return a + b

class K:
    def m(self, x=1, *args, y, **kw):
        return [i * x for i in range(3) if i]

total = add(1, 2)
msg = f"total={total!r:>5} {{ok}}"
'''
tree = ast.parse(src)

class Swap(ast.NodeTransformer):
    def visit_Constant(self, node):
        if node.value == 1:
            return ast.Constant(value=10)
        return node
    def visit_BinOp(self, node):
        self.generic_visit(node)
        if isinstance(node.op, ast.Add):
            return ast.BinOp(left=node.left, op=ast.Mult(), right=node.right)
        return node

new = Swap().visit(tree)
new.body.append(ast.Assign(targets=[ast.Name('added', ast.Store())], value=ast.Constant("new stmt")))
ast.fix_missing_locations(new)
text = ast.unparse(new)
assert 'a * b' in text and 'added = ' in text, text
ns = {}
exec(compile(new, "<ast>", "exec"), ns)
assert ns["total"] == 20, ns["total"]
assert ns["added"] == "new stmt"
assert ast.get_docstring(tree.body[0]) == "Add."

# Synthesized nodes with no locations compile without fix_missing_locations.
bare = ast.Module(body=[ast.Assign(targets=[ast.Name('q', ast.Store())], value=ast.Constant(5))], type_ignores=[])
ns2 = {}
exec(compile(bare, "<bare>", "exec"), ns2)
assert ns2["q"] == 5

# Statement line numbers survive the round trip through the unparser.
ln = ast.parse("a = 1\n\n\nb = 2\nraise ValueError('x')\n")
try:
    exec(compile(ln, "f.py", "exec"), {})
except ValueError as e:
    tb = e.__traceback__
    while tb.tb_next:
        tb = tb.tb_next
    assert tb.tb_lineno == 5, tb.tb_lineno

assert ast.literal_eval("[1, (2, 'x'), {'a': -3}]") == [1, (2, 'x'), {'a': -3}]
assert ast.dump(ast.parse("1+2", mode="eval")) == \
    "Expression(body=BinOp(left=Constant(value=1), op=Add(), right=Constant(value=2)))"
c = copy.deepcopy(tree.body[0])
assert ast.dump(c) == ast.dump(tree.body[0])
assert ast.dump(ast.parse("match x:\n case [1, *r]: pass")) == \
    "Module(body=[Match(subject=Name(id='x'), cases=[match_case(pattern=MatchSequence(patterns=[MatchValue(value=Constant(value=1)), MatchStar(name='r')]), body=[Pass()])])])"
try:
    ast.parse("def f(:\n")
except SyntaxError as e:
    assert (e.msg, e.lineno) == ("invalid syntax", 1), (e.msg, e.lineno)
else:
    raise AssertionError("expected SyntaxError")
assert isinstance(compile("1+1", "<s>", "eval", ast.PyCF_ONLY_AST), ast.Expression)
`)
}

func TestAnnotationlibFormats(t *testing.T) {
	runSnippet(t, `
import annotationlib
from annotationlib import Format, get_annotations

def f(x: int, y: "list[str]" = None, *a: Undefined, **k: dict[str, Foo]) -> Result: pass
assert get_annotations(f, format=Format.STRING) == {
    'x': 'int', 'y': 'list[str]', 'a': 'Undefined', 'k': 'dict[str, Foo]', 'return': 'Result'}
fr = get_annotations(f, format=Format.FORWARDREF)
assert fr['x'] is int and fr['y'] == 'list[str]'
assert isinstance(fr['a'], annotationlib.ForwardRef) and fr['a'].__forward_arg__ == 'Undefined'
assert fr['return'].__forward_arg__ == 'Result'
assert fr['k'].__args__[1].__forward_arg__ == 'Foo'
try:
    get_annotations(f)
except NameError:
    pass
else:
    raise AssertionError("expected NameError")

def g(a: int | None, b: list["X"]) -> str: ...
assert get_annotations(g, format=Format.STRING) == {'a': 'int | None', 'b': "list['X']", 'return': 'str'}
assert get_annotations(g) == {'a': int | None, 'b': list['X'], 'return': str}
`)
}

func TestLambdaClosureAndTruthiness(t *testing.T) {
	runSnippet(t, `
def a(s):
    ee = s
    g = lambda q: ee + q
    return g(1)
assert a(2) == 3

from collections import deque
d = deque([1])
n = 0
while d:
    d.popleft()
    n += 1
assert n == 1 and not d

class L:
    def __len__(self): return 0
class B:
    def __bool__(self): return False
assert not L() and not B() and (L() or 5) == 5

class P:
    __slots__ = ('x',)
    def __init__(self): self.x = 1
class Q:
    __slots__ = ('x',)
p = P()
p.__class__ = Q
assert type(p) is Q and p.x == 1

class D(dict):
    def __missing__(self, k): return k.upper()
import types
def fn(): return abc + len
h = types.FunctionType(fn.__code__, D(), argdefs=None)
assert h() == 'ABCLEN'
`)
}
