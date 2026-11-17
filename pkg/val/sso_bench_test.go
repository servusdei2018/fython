package val

import "testing"

var sinkStr string

func BenchmarkInlineAsString(b *testing.B) {
	vals := []Value{NewStringVal("a"), NewStringVal("ab"), NewStringVal("xyz"), NewStringVal("Zq")}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		sinkStr = vals[i&3].AsString()
	}
}
