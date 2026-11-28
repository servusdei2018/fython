package vm_test

import (
	"testing"
)

func TestArrayParityFixes(t *testing.T) {
	runSnippet(t, `
import array
# slice-step fix: stride must be honored (Fython slice returns list)
a = array.array("i", range(10))
assert list(a[1:8:2]) == [1, 3, 5, 7], list(a[1:8:2])
assert list(a[::-1]) == list(range(9, -1, -1))
# tobytes immutability: mutating the array must not mutate prior bytes
b = array.array("i", [1, 2, 3])
snap = b.tobytes()
b[0] = 99
assert snap == array.array("i", [1, 2, 3]).tobytes(), snap
assert b.tobytes() != snap
# bulk paths preserve values
c = array.array("i")
c.extend([4, 5, 6])
c.append(7)
assert c.tolist() == [4, 5, 6, 7]
c.fromlist([8])
assert c.tolist() == [4, 5, 6, 7, 8]
d = array.array("b", b"xy")
e = array.array("b")
e.extend(d.tobytes())
assert e.tolist() == [120, 121]
`)
}
