package vm_test

import (
	"testing"
)

func TestCallFromHookReentryAndExceptions(t *testing.T) {
	eng, v := runPySource(t, `
class Box:
    def __init__(self, v, nested=None):
        self.v = v
        self.nested = nested

    def __lt__(self, other):
        """Compare boxes with docstring (EntryIP == 2)."""
        if self.v == -999 or other.v == -999:
            raise RuntimeError("boom in __lt__")
        if self.nested is not None and other.nested is not None:
            return self.nested < other.nested
        return self.v < other.v

# 1. Re-entrant list.sort with Python key + recursive __lt__
items = [Box(i, Box(100 - i)) for i in range(25)]
items.sort()
assert [b.v for b in items] == list(range(24, -1, -1))

def sort_key(b):
    """Key function with docstring."""
    inner = [b.v, b.v - 1, b.v + 1]
    inner.sort(key=lambda x: -x)
    return inner[0]

items.sort(key=sort_key)
assert [b.v for b in items] == list(range(25))

# 2. Nested exception propagation through callFromHook
bad_items = [Box(1), Box(-999), Box(2)]
caught = None
try:
    bad_items.sort()
except RuntimeError as e:
    caught = str(e)
assert caught == "boom in __lt__", f"unexpected exception: {caught!r}"
`)
	defer eng.ReleaseVM(v)
}
