package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestFusedTupleBundleAllocsAndSemantics(t *testing.T) {
	for n := range 11 {
		elems := make([]val.Value, n)
		for i := range n {
			elems[i] = val.Int64Val(int64(i + 1))
		}
		tv := val.NewTupleFromSlice(elems)
		tp := tv.AsTuple()
		if len(tp.Items) != n {
			t.Fatalf("len=%d: got %d", n, len(tp.Items))
		}
		for i := range n {
			if tp.Items[i].AsInt64() != int64(i+1) {
				t.Fatalf("len=%d idx=%d: got %v", n, i, tp.Items[i])
			}
		}
		if n >= 1 && n <= 8 {
			allocs := testing.AllocsPerRun(100, func() {
				_ = val.NewTupleFromSlice(elems)
			})
			if allocs != 1 {
				t.Fatalf("NewTupleFromSlice(len=%d) allocated %.1f times, want 1", n, allocs)
			}
		}
	}

	runPySource(t, `
t4 = (1, 2, 3, 4)
t6 = (1, 2, 3, 4, 5, 6)
t8 = (1, 2, 3, 4, 5, 6, 7, 8)
t10 = (1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
assert t4 == (1, 2, 3, 4) and hash(t4) == hash((1, 2, 3, 4))
assert t6[1:5] == (2, 3, 4, 5)
assert t8[:4] == t4
assert len(t10) == 10 and t10[-1] == 10
d = {t4: "four", t6: "six", t8: "eight"}
assert d[(1, 2, 3, 4)] == "four"
assert d[(1, 2, 3, 4, 5, 6)] == "six"
assert d[(1, 2, 3, 4, 5, 6, 7, 8)] == "eight"
`)
}
