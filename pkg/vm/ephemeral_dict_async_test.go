package vm_test

import "testing"

func TestEphemeralDictAsyncEscape(t *testing.T) {
	runSnippet(t, `
import asyncio

async def store_and_churn(scope, out):
    out.append(scope)
    # Churn the dict pools with many short-lived const-key literals.
    for _ in range(200):
        d = {"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11, "l": 12}
        assert len(d) == 12
    # Original scope must be intact, not recycled while live.
    assert scope["type"] == "http", scope
    assert scope["l"] == 12, scope
    assert out[0] is scope
    return len(out)

async def main():
    scope = {"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11, "l": 12, "type": "http"}
    out = []
    n = await store_and_churn(scope, out)
    assert n == 1
    assert out[0]["a"] == 1 and out[0]["l"] == 12
    # Mutating the stored dict must not affect new literals (no aliasing).
    out[0]["a"] = 99
    d2 = {"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11, "l": 12}
    assert d2["a"] == 1, d2
    assert out[0]["a"] == 99

asyncio.run(main())
`)
}

func TestConstKeyMapEphemeralUnpack(t *testing.T) {
	runSnippet(t, `
def f(**kw):
    return kw

# Const-key literals used for ** unpacking should recycle without corruption.
for _ in range(100):
    d = f(**{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11, "l": 12})
    assert d == {"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8, "i": 9, "j": 10, "k": 11, "l": 12}, d
    e = {"a": 1, "b": 2, "c": 3}
    assert e["a"] == 1
`)
}
