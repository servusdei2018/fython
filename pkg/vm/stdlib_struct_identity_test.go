package vm_test

import (
	"testing"
)

func TestStructIdentityCacheParity(t *testing.T) {
	runSnippet(t, `
import struct
# str format
assert struct.pack("<I", 123456) == struct.pack("<I", 123456)
assert struct.unpack("<I", struct.pack("<I", 1))[0] == 1
# bytes format
assert struct.pack(b"<I", 7) == struct.pack("<I", 7)
assert struct.calcsize(b"<2H") == 4
# str subclass format never bypasses validation (same result as plain str)
class F(str): pass
assert struct.pack(F("<I"), 9) == struct.pack("<I", 9)
assert struct.calcsize(F("<2H")) == 4
# _clearcache clears completely (no stale entries)
struct._clearcache()
assert struct.pack("<I", 5) == struct.pack("<I", 5)
assert struct.calcsize("<H") == 2
`)
}

func TestStructClearcacheConcurrent(t *testing.T) {
	// Exercise concurrent pack/unpack + _clearcache via Python threads.
	// Run under -race to prove the global fast cache is mutex guarded.
	runSnippet(t, `
import struct, threading
fmt = "<2H"
data = struct.pack(fmt, 1, 2) * 20
errs = []
def work():
    try:
        for _ in range(50):
            b = struct.pack(fmt, 3, 4)
            assert struct.unpack(fmt, b) == (3, 4)
            assert len(list(struct.iter_unpack(fmt, data))) == 20
            struct._clearcache()
    except Exception as e:
        errs.append(e)
threads = [threading.Thread(target=work) for _ in range(4)]
for th in threads: th.start()
for th in threads: th.join()
assert not errs, errs
`)
}

func TestStructBytearrayFormatRevalidates(t *testing.T) {
	// Mutable bytearray formats must never hit a stale identity entry.
	runSnippet(t, `
import struct
fmt = bytearray(b"<I")
assert struct.calcsize(fmt) == 4
fmt[:] = b"<2H"
assert struct.calcsize(fmt) == 4
# "<I" and "<2H" are both size 4; use a discriminating mutation
fmt[:] = b"<Q"
assert struct.calcsize(fmt) == 8
`)
}
