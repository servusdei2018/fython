package vm_test

import (
	"testing"
)

func TestMemviewFastPathGuards(t *testing.T) {
	runSnippet(t, `
raw = bytearray(b"0123456789ABCDEF")
mv = memoryview(raw)
# basic 1D indexing incl. negative
assert mv[0] == 48 and mv[-1] == 70 and mv[5] == 53
# out-of-bounds raises
try:
    mv[100]
    raise AssertionError("expected IndexError")
except IndexError:
    pass
try:
    mv[-100]
    raise AssertionError("expected IndexError")
except IndexError:
    pass
# multi-byte formats via cast
buf2 = bytearray(8)
mv2 = memoryview(buf2).cast("h")
mv2[0] = 0x1234
mv2[1] = -1
assert mv2[0] == 0x1234 and mv2[1] == -1
mv4 = memoryview(bytearray(16)).cast("i")
mv4[0] = -123456
assert mv4[0] == -123456
mvf = memoryview(bytearray(8)).cast("d")
mvf[0] = 1.5
assert mvf[0] == 1.5
# non-zero slice offset then index
sub = mv[4:]
assert sub[0] == mv[4] and sub[-1] == mv[-1]
# released view raises ValueError
r = memoryview(bytearray(b"ab"))
r.release()
try:
    r[0]
    raise AssertionError("expected ValueError")
except ValueError:
    pass
# read-only write raises TypeError
ro = memoryview(b"ab")
try:
    ro[0] = 1
    raise AssertionError("expected TypeError")
except TypeError:
    pass
`)
}
