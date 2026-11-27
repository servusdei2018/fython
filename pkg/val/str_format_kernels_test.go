package val_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestPercentFormatKernels(t *testing.T) {
	cases := []struct {
		fmt  string
		arg  val.Value
		want string
	}{
		{"%s:%04d -> %s (%.2f) [%d]", val.NewTupleVal(
			val.NewStringVal("tok"), val.Int64Val(7), val.NewStringVal("dst"), val.Float64Val(3.5), val.Int64Val(12),
		), "tok:0007 -> dst (3.50) [12]"},
		{"%#x %#X %#o", val.NewTupleVal(val.Int64Val(255), val.Int64Val(255), val.Int64Val(8)), "0xff 0XFF 0o10"},
		{"%#o %#x", val.NewTupleVal(val.Int64Val(0), val.Int64Val(0)), "0o0 0x0"},
		{"%#06x", val.Int64Val(-10), "-0x00a"},
		{"%06.3d", val.Int64Val(7), "   007"}, // explicit precision overrides '0' flag
		{"%+d % d %-5d|", val.NewTupleVal(val.Int64Val(5), val.Int64Val(5), val.Int64Val(5)), "+5  5 5    |"},
		{"%.1f %.1f", val.NewTupleVal(val.Float64Val(math.Copysign(0, -1)), val.Float64Val(0)), "-0.0 0.0"},
		{"%f %F %e %E %g %G", val.NewTupleVal(
			val.Float64Val(math.NaN()), val.Float64Val(math.NaN()),
			val.Float64Val(math.Inf(1)), val.Float64Val(math.Inf(1)),
			val.Float64Val(math.Inf(-1)), val.Float64Val(math.Inf(-1)),
		), "nan NAN inf INF -inf -INF"},
		{"%08f %08F", val.NewTupleVal(val.Float64Val(math.Inf(1)), val.Float64Val(math.NaN())), "     inf      NAN"},
		{"%#g %#.4g", val.NewTupleVal(val.Float64Val(1.0), val.Float64Val(2.5)), "1.00000 2.500"},
		{"%*d %.*f", val.NewTupleVal(val.Int64Val(5), val.Int64Val(42), val.Int64Val(3), val.Float64Val(1.5)), "   42 1.500"},
		{"%r %a %c", val.NewTupleVal(val.NewStringVal("hi"), val.NewStringVal("caf\u00e9"), val.Int64Val(65)), `'hi' 'caf\xe9' A`},
		{"%d", val.NewBigIntVal(new(big.Int).Lsh(big.NewInt(1), 80)), "1208925819614629174706176"},
	}
	for _, tc := range cases {
		got := val.Mod(val.NewStringVal(tc.fmt), tc.arg)
		if !got.IsAnyString() || got.AsString() != tc.want {
			t.Errorf("%q %% %v = %q, want %q", tc.fmt, tc.arg, got.AsString(), tc.want)
		}
	}

	// Dict mapping formatting.
	d := val.NewDictVal(2)
	d.AsDict().Set(val.InternString("k"), val.NewStringVal("v"))
	d.AsDict().Set(val.InternString("n"), val.Int64Val(9))
	got := val.Mod(val.NewStringVal("%(k)s=%(n)03d%%"), d)
	if got.AsString() != "v=009%" {
		t.Errorf("dict mod = %q, want %q", got.AsString(), "v=009%")
	}
}

func TestFormatValueBytesEdgeSpecs(t *testing.T) {
	cases := []struct {
		v    val.Value
		spec string
		want string
	}{
		{val.Int64Val(42), "", "42"},
		{val.Int64Val(42), "d", "42"},
		{val.Int64Val(42), "04d", "0042"},
		{val.Int64Val(-10), "#06x", "-0x00a"},
		{val.Int64Val(255), "#X", "0XFF"},
		{val.Int64Val(5), "04b", "0101"},
		{val.Float64Val(1.0), "", "1.0"},
		{val.Float64Val(3.14159), ".2f", "3.14"},
		{val.Float64Val(math.Copysign(0, -1)), ".1f", "-0.0"},
		{val.Float64Val(math.Inf(-1)), ".2f", "-inf"},
		{val.Float64Val(math.NaN()), "F", "NAN"},
		{val.SmallStringVal("hi"), ">5", "   hi"},
		{val.NewStringVal("abcdef"), ".3s", "abc"},
	}
	for _, tc := range cases {
		got := val.FormatValueBytes(tc.v, []byte(tc.spec), 0)
		if !got.IsAnyString() || got.AsString() != tc.want {
			t.Errorf("FormatValueBytes(%v, %q) = %q, want %q", tc.v, tc.spec, got.AsString(), tc.want)
		}
	}
}
