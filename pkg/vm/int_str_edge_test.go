package vm_test

import (
	"testing"
)

func TestIntStrEdgeCases(t *testing.T) {
	eng, v := runPySource(t, `
assert int("0") == 0
assert int("12345") == 12345
assert int("  \t+42\n ") == 42
assert int(" -99\r\f\v") == -99
assert int("1_000_000") == 1000000
assert int("-1_2_3") == -123
assert int("9223372036854775807") == (1 << 63) - 1
assert int("-9223372036854775808") == -(1 << 63)
assert int("9223372036854775808") == 1 << 63
assert int("-9223372036854775809") == -(1 << 63) - 1
assert int("999999999999999999999999999999") == 10**30 - 1

for bad in ["", "   ", "+", "-", "_1", "1_", "1__2", "+_1", "-_1", "12a", "1.5"]:
    raised = False
    try:
        int(bad)
    except ValueError:
        raised = True
    assert raised, f"expected ValueError for {bad!r}"

class MyInt(int):
    pass

mi = MyInt("12345")
assert type(mi) is MyInt
assert mi == 12345
`)
	defer eng.ReleaseVM(v)
}
