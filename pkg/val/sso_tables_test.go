package val

import "testing"

func TestAsciiBackingTables(t *testing.T) {
	for i := range 256 {
		if got := ascii1(byte(i)); got != string([]byte{byte(i)}) {
			t.Fatalf("ascii1(%d)=%q", i, got)
		}
	}
	for i := range 128 {
		for j := range 128 {
			if got := ascii2(byte(i), byte(j)); got != string([]byte{byte(i), byte(j)}) {
				t.Fatalf("ascii2(%d,%d)=%q", i, j, got)
			}
		}
	}
}
