import os, sys as s
from . import x
from ..a.b import (c as d, e,)
x: int = 3
y = z = 1, 2,
a, *b = c
def f(a, b=1, /, c=2, *args, d, e=5, **kw) -> int:
    """doc"""
    return a + b * c ** -d
class C(Base, metaclass=M):
    x = 1
    @property
    def p(self): yield from self.q
for i, j in zip(a, b):
    print(i, end="")
else:
    pass
while x < 3 < y: x += 1
try:
    pass
except (A, B) as e:
    raise X from e
finally:
    del a, b[0], c.d
with open(f) as g, h:
    pass
lam = lambda x, *y, z=1, **k: (x, y)
l = [i for i in range(10) if i % 2 if i > 3]
d = {k: v for k, v in x.items()}
s = {1, 2, *a}
g = (x for x in y)
print(f"a{x!r:>{w}}b{{c}}" "d" 'e', b"abc" b"d", 1_000, 0x1f, 1e3, 3j, ...)
x = a if b else c
x = not a or b and c
x = a[1:2, ::3, ...]
x = a[1]
async def g():
    async with a as b: pass
    async for i in x: await y
    return [i async for i in z]
match cmd:
    case [1, 2, *rest] | (3, 4):
        pass
    case {"a": 1, **kw}:
        pass
    case Point(x=0, y=1) as p if p:
        pass
    case -1 | 1+2j | None | "s" | a.b:
        pass
    case _:
        pass
type X[T: int, *Ts, **P] = list[T]
def h[T](x: T) -> T: return x
global_ = (yield)
assert x, "m"
x = (a for a in b)
f(a for a in b)
f(*a, b, c=1, **d)
