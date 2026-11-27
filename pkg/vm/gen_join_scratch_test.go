package vm_test

import "testing"

func TestStrFormatAndSplitlinesKernels(t *testing.T) {
	eng, v := runPySource(t, `
# Positional, keyword, nested spec, and overflow-like numeric field names.
assert "{0} {1} {name} {val:.2f}".format("a", 1, name="x", val=2.5) == "a 1 x 2.50"
assert "{f}".format(f=1.0) == "1.0"
assert "{:{w}}".format("hi", w=5) == "hi   "
assert "{99999999999999999999999999999}".format(**{"99999999999999999999999999999": "ok"}) == "ok"

# Subclass overriding __format__ must not be bypassed by primitive fast path.
class CustomStr(str):
    def __format__(self, spec):
        return "CUSTOM:" + str(self)

class CustomInt(int):
    def __format__(self, spec):
        return f"INT({int(self)})"

assert "{}".format(CustomStr("abc")) == "CUSTOM:abc"
assert "{}".format(CustomInt(7)) == "INT(7)"

# splitlines: ASCII, CRLF, control chars, Unicode separators, and keepends.
assert "".splitlines() == []
assert "a\nb\r\nc\rd".splitlines() == ["a", "b", "c", "d"]
assert "a\nb\r\nc\rd".splitlines(True) == ["a\n", "b\r\n", "c\r", "d"]
assert "a\vb\fc\x1cd\x1de\x1ef".splitlines() == ["a", "b", "c", "d", "e", "f"]
assert "a\x85b\u2028c\u2029d".splitlines() == ["a", "b", "c", "d"]
assert "a\x85b\u2028c\u2029d".splitlines(keepends=True) == ["a\x85", "b\u2028", "c\u2029", "d"]
`)
	defer eng.ReleaseVM(v)
}

func TestGeneratorStrJoinScratchReentrancyAndCaps(t *testing.T) {
	eng, v := runPySource(t, `
# Basic empty, short, and repeated joins verify zero aliasing across scratch reuse.
results = []
for i in range(20):
    s = "|".join(f"item_{i}_{j}" for j in range(10))
    results.append(s)

for i in range(20):
    expected = "|".join([f"item_{i}_{j}" for j in range(10)])
    assert results[i] == expected

assert ",".join(x for x in []) == ""
assert ",".join(x for x in ["a"]) == "a"

# Nested generator str.join reentrancy (3 levels deep).
nested = "\n".join(
    ",".join(
        ":".join(f"{i}{j}{k}" for k in range(3))
        for j in range(3)
    )
    for i in range(3)
)
assert nested == "000:001:002,010:011:012,020:021:022\n100:101:102,110:111:112,120:121:122\n200:201:202,210:211:212,220:221:222"

# Large join (> 64 KiB) succeeds and subsequent small join still works cleanly.
big = "".join("abcdefgh" for _ in range(10000))
assert len(big) == 80000
small = "-".join(str(i) for i in range(5))
assert small == "0-1-2-3-4"

# Exception mid-join cleanly releases scratch state.
def failing_gen():
    yield "ok"
    raise ValueError("boom")

caught = False
try:
    ",".join(failing_gen())
except ValueError:
    caught = True
assert caught
assert ",".join(x for x in ["after", "error"]) == "after,error"
`)
	defer eng.ReleaseVM(v)
}
