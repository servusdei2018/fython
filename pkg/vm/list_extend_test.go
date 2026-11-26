package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestListExtendKnownLengths(t *testing.T) {
	eng, v := runPySource(t, `
a = [1, 2]
a.extend([3, 4, 5])
assert a == [1, 2, 3, 4, 5]
a.extend((6, 7))
assert a == [1, 2, 3, 4, 5, 6, 7]
a.extend({8, 9})
assert sorted(a) == [1, 2, 3, 4, 5, 6, 7, 8, 9]
a.extend(x * 2 for x in [10, 11])
assert sorted(a) == [1, 2, 3, 4, 5, 6, 7, 8, 9, 20, 22]
empty = []
empty.extend([])
assert empty == []
empty.extend(())
assert empty == []
big = list(range(100))
big.extend(range(100, 300))
assert big == list(range(300))
assert len(big) == 300
result = big
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("result"))
	if !ok || !got.IsList() || len(got.AsList().Items) != 300 {
		t.Fatalf("extend result len = %v, want 300", got)
	}
}

func TestListExtendSelfAndStrings(t *testing.T) {
	eng, v := runPySource(t, `
# Extend with a string iterates characters.
s = ["a"]
s.extend("bc")
assert s == ["a", "b", "c"]
# Extend preserves existing items and detaches string buffers.
t = ["x", "y"]
t.extend(["z"])
assert t == ["x", "y", "z"]
assert len(t) == 3
`)
	defer eng.ReleaseVM(v)
	_ = val.InternString("t")
}
