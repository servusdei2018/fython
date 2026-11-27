package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestMathFactorialBigIntAndErrors(t *testing.T) {
	eng, v := runPySource(t, `
import math

# Fast int64 table path (0..20)
fast_ok = (
    math.factorial(0) == 1
    and math.factorial(1) == 1
    and math.factorial(5) == 120
    and math.factorial(10) == 3628800
    and math.factorial(20) == 2432902008176640000
    and math.factorial(False) == 1
    and math.factorial(True) == 1
)

# Arbitrary-precision BigInt path (n > 20)
f21 = math.factorial(21)
f100 = math.factorial(100)
bigint_ok = (
    f21 == 51090942171709440000
    and (f100 % 1000003) == 371073
    and str(f100) == "93326215443944152681699238856266700490715968264381621468592963895217599993229915608941463976156518286253697920827223758251185210916864000000000000000000000000"
)

# __index__ protocol support
class IndexFive:
    def __index__(self):
        return 5

index_ok = (math.factorial(IndexFive()) == 120)

# Negative int -> ValueError
neg_int_err = False
try:
    math.factorial(-1)
except ValueError:
    neg_int_err = True

# Negative huge BigInt -> ValueError (NOT OverflowError)
neg_big_err = False
try:
    math.factorial(-(10 ** 100))
except ValueError:
    neg_big_err = True

# Positive huge BigInt -> OverflowError
pos_big_err = False
try:
    math.factorial(10 ** 100)
except OverflowError:
    pos_big_err = True

# Non-integer inputs -> TypeError
float_err = False
try:
    math.factorial(5.0)
except TypeError:
    float_err = True

str_err = False
try:
    math.factorial("5")
except TypeError:
    str_err = True

arity_err = False
try:
    math.factorial(1, 2)
except TypeError:
    arity_err = True

ok = (
    fast_ok
    and bigint_ok
    and index_ok
    and neg_int_err
    and neg_big_err
    and pos_big_err
    and float_err
    and str_err
    and arity_err
)
`)
	defer eng.ReleaseVM(v)

	for _, name := range []string{
		"fast_ok", "bigint_ok", "index_ok",
		"neg_int_err", "neg_big_err", "pos_big_err",
		"float_err", "str_err", "arity_err", "ok",
	} {
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want True", name, got, ok)
		}
	}
}
