package val

import (
	"math"
	"testing"
)

func TestTimsortDirectAPI(t *testing.T) {
	// Int64 fast path (already sorted, reverse, random, duplicates)
	n := 500
	items := make([]Value, n)
	for i := range n {
		items[i] = Int64Val(int64((i * 2654435761) % 97))
	}
	l := NewListVal(items...).AsList()
	SortList(nil, l, Value{}, false)
	for i := 1; i < len(l.Items); i++ {
		if l.Items[i-1].AsInt64() > l.Items[i].AsInt64() {
			t.Fatalf("int64 sort not ascending at %d", i)
		}
	}
	SortList(nil, l, Value{}, true)
	for i := 1; i < len(l.Items); i++ {
		if l.Items[i-1].AsInt64() < l.Items[i].AsInt64() {
			t.Fatalf("int64 reverse sort not descending at %d", i)
		}
	}

	// NaN check in direct API
	l2 := NewListVal(Float64Val(math.NaN()), Float64Val(1.0)).AsList()
	SortList(nil, l2, Value{}, false)
	if len(l2.Items) != 2 {
		t.Fatalf("unexpected len: %d", len(l2.Items))
	}
}
