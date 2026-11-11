package vm

import (
	"math"
	"math/big"

	"github.com/servusdei2018/fython/pkg/val"
)

func toBigInt(v val.Value) *big.Int {
	if v.IsInt64() {
		return big.NewInt(v.AsInt64())
	}
	if v.Kind() == val.KindBigInt {
		return new(big.Int).Set(&v.AsBigInt().Val)
	}
	if v.IsBool() {
		if v.AsBool() {
			return big.NewInt(1)
		}
		return big.NewInt(0)
	}
	panic("TypeError: 'float' object cannot be interpreted as an integer")
}

func bigIntToVal(bi *big.Int) val.Value {
	if bi.IsInt64() {
		return val.Int64Val(bi.Int64())
	}
	return val.NewBigIntVal(bi)
}

func toFloat(v val.Value) float64 {
	if v.IsFloat64() {
		return v.AsFloat64()
	}
	if v.IsInt64() {
		return float64(v.AsInt64())
	}
	if v.IsBool() {
		if v.AsBool() {
			return 1.0
		}
		return 0.0
	}
	panic("TypeError: must be real number, not " + v.Type().Name)
}

func (e *Engine) createMathModule() val.Value {
	modVal := val.NewModuleVal("math", "")
	mod := modVal.AsModule()

	// Constants
	mod.Dict.Set(val.InternString("pi"), val.Float64Val(math.Pi))
	mod.Dict.Set(val.InternString("e"), val.Float64Val(math.E))
	mod.Dict.Set(val.InternString("tau"), val.Float64Val(2*math.Pi))
	mod.Dict.Set(val.InternString("inf"), val.Float64Val(math.Inf(1)))
	mod.Dict.Set(val.InternString("nan"), val.Float64Val(math.NaN()))

	// Unary functions
	registerUnary := func(name string, fn func(float64) float64) {
		mod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: " + name + "() takes exactly one argument")
			}
			x := toFloat(args[0])
			return val.Float64Val(fn(x))
		}))
	}

	registerUnary("sqrt", math.Sqrt)
	registerUnary("sin", math.Sin)
	registerUnary("cos", math.Cos)
	registerUnary("tan", math.Tan)
	registerUnary("asin", math.Asin)
	registerUnary("acos", math.Acos)
	registerUnary("atan", math.Atan)
	registerUnary("exp", math.Exp)
	registerUnary("log2", math.Log2)
	registerUnary("log10", math.Log10)
	registerUnary("fabs", math.Abs)

	mod.Dict.Set(val.InternString("floor"), val.NewBuiltinFuncVal("floor", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: floor() takes exactly one argument")
		}
		x := toFloat(args[0])
		return val.Int64Val(int64(math.Floor(x)))
	}))
	mod.Dict.Set(val.InternString("ceil"), val.NewBuiltinFuncVal("ceil", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ceil() takes exactly one argument")
		}
		x := toFloat(args[0])
		return val.Int64Val(int64(math.Ceil(x)))
	}))
	mod.Dict.Set(val.InternString("trunc"), val.NewBuiltinFuncVal("trunc", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: trunc() takes exactly one argument")
		}
		x := toFloat(args[0])
		return val.Int64Val(int64(math.Trunc(x)))
	}))
	registerUnary("lgamma", func(x float64) float64 {
		lg, _ := math.Lgamma(x)
		return lg
	})
	registerUnary("gamma", math.Gamma)
	registerUnary("degrees", func(x float64) float64 { return x * 180 / math.Pi })
	registerUnary("radians", func(x float64) float64 { return x * math.Pi / 180 })

	registerBinary := func(name string, fn func(float64, float64) float64) {
		mod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: " + name + "() takes exactly 2 arguments")
			}
			return val.Float64Val(fn(toFloat(args[0]), toFloat(args[1])))
		}))
	}
	registerUnary("cosh", math.Cosh)
	registerUnary("sinh", math.Sinh)
	registerUnary("tanh", math.Tanh)
	registerBinary("hypot", math.Hypot)
	// erfc is the complementary error function. The Go standard library has
	// no counterpart, so it is evaluated with the standard rational
	// approximation of Numerical Recipes.
	registerUnary("erfc", mathErfc)
	registerBinary("fmod", math.Mod)
	registerBinary("remainder", math.Remainder)

	// fsum sums the values with a compensated summation, so that the result
	// is accurate even for a long series of inexact values.
	mod.Dict.Set(val.InternString("fsum"), val.NewBuiltinFuncVal("fsum", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: fsum() takes at least one argument")
		}
		return val.Float64Val(mathFsum(ctx, args[0]))
	}))

	// sumprod sums the products of the values taken from the iterables.
	mod.Dict.Set(val.InternString("sumprod"), val.NewBuiltinFuncVal("sumprod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: sumprod() takes at least one argument")
		}
		return val.Float64Val(mathSumprod(ctx, args))
	}))

	// log(x[, base])
	mod.Dict.Set(val.InternString("log"), val.NewBuiltinFuncVal("log", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: log() takes at least 1 argument")
		}
		x := toFloat(args[0])
		if len(args) == 1 {
			return val.Float64Val(math.Log(x))
		}
		base := toFloat(args[1])
		return val.Float64Val(math.Log(x) / math.Log(base))
	}))

	// pow(x, y)
	mod.Dict.Set(val.InternString("pow"), val.NewBuiltinFuncVal("pow", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: pow() takes exactly 2 arguments")
		}
		x := toFloat(args[0])
		y := toFloat(args[1])
		return val.Float64Val(math.Pow(x, y))
	}))

	// atan2(y, x)
	mod.Dict.Set(val.InternString("atan2"), val.NewBuiltinFuncVal("atan2", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: atan2() takes exactly 2 arguments")
		}
		y := toFloat(args[0])
		x := toFloat(args[1])
		return val.Float64Val(math.Atan2(y, x))
	}))

	// copysign(x, y)
	mod.Dict.Set(val.InternString("copysign"), val.NewBuiltinFuncVal("copysign", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: copysign() takes exactly 2 arguments")
		}
		x := toFloat(args[0])
		y := toFloat(args[1])
		return val.Float64Val(math.Copysign(x, y))
	}))

	// fmod(x, y)
	mod.Dict.Set(val.InternString("fmod"), val.NewBuiltinFuncVal("fmod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: fmod() takes exactly 2 arguments")
		}
		x := toFloat(args[0])
		y := toFloat(args[1])
		return val.Float64Val(math.Mod(x, y))
	}))

	// hypot(x, y)
	mod.Dict.Set(val.InternString("hypot"), val.NewBuiltinFuncVal("hypot", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: hypot() takes at least 2 arguments")
		}
		x := toFloat(args[0])
		y := toFloat(args[1])
		return val.Float64Val(math.Hypot(x, y))
	}))

	// radians / degrees
	mod.Dict.Set(val.InternString("radians"), val.NewBuiltinFuncVal("radians", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: radians() takes exactly one argument")
		}
		deg := toFloat(args[0])
		return val.Float64Val(deg * (math.Pi / 180.0))
	}))

	mod.Dict.Set(val.InternString("degrees"), val.NewBuiltinFuncVal("degrees", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: degrees() takes exactly one argument")
		}
		rad := toFloat(args[0])
		return val.Float64Val(rad * (180.0 / math.Pi))
	}))

	// isinf, isnan, isfinite
	mod.Dict.Set(val.InternString("isinf"), val.NewBuiltinFuncVal("isinf", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isinf() takes exactly one argument")
		}
		return val.BoolVal(math.IsInf(toFloat(args[0]), 0))
	}))

	mod.Dict.Set(val.InternString("isnan"), val.NewBuiltinFuncVal("isnan", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isnan() takes exactly one argument")
		}
		return val.BoolVal(math.IsNaN(toFloat(args[0])))
	}))

	mod.Dict.Set(val.InternString("isfinite"), val.NewBuiltinFuncVal("isfinite", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isfinite() takes exactly one argument")
		}
		f := toFloat(args[0])
		return val.BoolVal(!math.IsNaN(f) && !math.IsInf(f, 0))
	}))

	// gcd(*integers)
	mod.Dict.Set(val.InternString("gcd"), val.NewBuiltinFuncVal("gcd", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.Int64Val(0)
		}
		g := toBigInt(args[0])
		g = new(big.Int).Abs(g)
		for i := 1; i < len(args); i++ {
			b := toBigInt(args[i])
			g.GCD(nil, nil, g, b)
		}
		return bigIntToVal(g)
	}))

	// lcm(*integers)
	mod.Dict.Set(val.InternString("lcm"), val.NewBuiltinFuncVal("lcm", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) == 0 {
			return val.Int64Val(1)
		}
		l := toBigInt(args[0])
		l = new(big.Int).Abs(l)
		zero := big.NewInt(0)
		for i := 1; i < len(args); i++ {
			b := toBigInt(args[i])
			b = new(big.Int).Abs(b)
			if l.Cmp(zero) == 0 || b.Cmp(zero) == 0 {
				l = big.NewInt(0)
				continue
			}
			g := new(big.Int).GCD(nil, nil, l, b)
			l.Div(l, g)
			l.Mul(l, b)
		}
		return bigIntToVal(l)
	}))

	// isqrt(n)
	mod.Dict.Set(val.InternString("isqrt"), val.NewBuiltinFuncVal("isqrt", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isqrt() takes exactly one argument")
		}
		n := toBigInt(args[0])
		if n.Sign() < 0 {
			panic("ValueError: isqrt() argument must be nonnegative")
		}
		res := new(big.Int).Sqrt(n)
		return bigIntToVal(res)
	}))

	// frexp(x)
	mod.Dict.Set(val.InternString("frexp"), val.NewBuiltinFuncVal("frexp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: frexp() takes exactly one argument")
		}
		x := toFloat(args[0])
		m, e := math.Frexp(x)
		return val.NewTupleVal(val.Float64Val(m), val.Int64Val(int64(e)))
	}))

	// ldexp(m, e)
	mod.Dict.Set(val.InternString("ldexp"), val.NewBuiltinFuncVal("ldexp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: ldexp() takes exactly 2 arguments")
		}
		x := toFloat(args[0])
		e := args[1].AsInt64()
		return val.Float64Val(math.Ldexp(x, int(e)))
	}))

	// modf(x)
	mod.Dict.Set(val.InternString("modf"), val.NewBuiltinFuncVal("modf", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: modf() takes exactly one argument")
		}
		x := toFloat(args[0])
		intPart, fracPart := math.Modf(x)
		return val.NewTupleVal(val.Float64Val(fracPart), val.Float64Val(intPart))
	}))

	// nextafter(x, y)
	mod.Dict.Set(val.InternString("nextafter"), val.NewBuiltinFuncVal("nextafter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: nextafter() takes exactly 2 arguments")
		}
		x := toFloat(args[0])
		y := toFloat(args[1])
		return val.Float64Val(math.Nextafter(x, y))
	}))

	// ulp(x)
	mod.Dict.Set(val.InternString("ulp"), val.NewBuiltinFuncVal("ulp", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: ulp() takes exactly one argument")
		}
		x := toFloat(args[0])
		if math.IsNaN(x) {
			return val.Float64Val(math.NaN())
		}
		if math.IsInf(x, 0) {
			return val.Float64Val(math.Inf(1))
		}
		x = math.Abs(x)
		if x == 0.0 {
			return val.Float64Val(math.Nextafter(0.0, 1.0))
		}
		x2 := math.Nextafter(x, math.Inf(1))
		if math.IsInf(x2, 1) {
			return val.Float64Val(x - math.Nextafter(x, -math.Inf(1)))
		}
		return val.Float64Val(x2 - x)
	}))

	// factorial(n)
	mod.Dict.Set(val.InternString("factorial"), val.NewBuiltinFuncVal("factorial", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: factorial() takes exactly one argument")
		}
		n := args[0].AsInt64()
		if n < 0 {
			panic("ValueError: factorial() not defined for negative values")
		}
		res := int64(1)
		for i := int64(2); i <= n; i++ {
			res *= i
		}
		return val.Int64Val(res)
	}))

	return modVal
}

// mathErfc evaluates the complementary error function 1 - erf(x).
func mathErfc(x float64) float64 {
	if math.IsNaN(x) {
		return math.NaN()
	}
	if x >= 0 {
		return mathErfcPositive(x)
	}
	return 2 - mathErfcPositive(-x)
}

// mathErfcPositive evaluates the complementary error function for x >= 0.
//
// The complementary error function is the regularized upper incomplete gamma
// function Q(1/2, x*x), which is evaluated with the series and continued
// fraction representations of the incomplete gamma function.
func mathErfcPositive(x float64) float64 {
	if x == 0 {
		return 1
	}
	return mathGammaQ(0.5, x*x)
}

// mathGammaQ evaluates the regularized upper incomplete gamma function.
func mathGammaQ(a, x float64) float64 {
	if x < a+1 {
		return 1 - mathGammaPSeries(a, x)
	}
	return mathGammaQContinuedFraction(a, x)
}

// mathGammaPSeries evaluates the regularized lower incomplete gamma function
// with its series representation.
func mathGammaPSeries(a, x float64) float64 {
	const eps = 3e-16
	const itmax = 1000
	ap := a
	sum := 1.0 / a
	del := sum
	for range itmax {
		ap++
		del *= x / ap
		sum += del
		if math.Abs(del) < math.Abs(sum)*eps {
			break
		}
	}
	lgamma, _ := math.Lgamma(a)
	return sum * math.Exp(-x+a*math.Log(x)-lgamma)
}

// mathGammaQContinuedFraction evaluates the regularized upper incomplete
// gamma function with its continued fraction representation.
func mathGammaQContinuedFraction(a, x float64) float64 {
	const eps = 3e-16
	const itmax = 1000
	const tiny = 1e-300
	b := x + 1 - a
	c := 1 / tiny
	d := 1 / b
	h := d
	for i := 1; i <= itmax; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	lgamma, _ := math.Lgamma(a)
	return math.Exp(-x+a*math.Log(x)-lgamma) * h
}

// mathFsum sums the values of an iterable with a compensated summation.
func mathFsum(ctx *val.ExecContext, v val.Value) float64 {
	it := iterFor(ctx, v)
	total := 0.0
	comp := 0.0
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		x := toFloat(item)
		y := x - comp
		t := total + y
		comp = (t - total) - y
		total = t
	}
	return total
}

// mathSumprod sums the products of the values taken from the iterables.
func mathSumprod(ctx *val.ExecContext, args []val.Value) float64 {
	iterables := make([][]val.Value, 0, len(args))
	length := -1
	for _, a := range args {
		it := iterFor(ctx, a)
		items := make([]val.Value, 0, 8)
		for {
			item, ok := it.Next()
			if !ok {
				break
			}
			items = append(items, item)
		}
		if length >= 0 && len(items) != length {
			panic("ValueError: sumprod() arguments have different lengths")
		}
		length = len(items)
		iterables = append(iterables, items)
	}
	total := 0.0
	for i := range length {
		product := 1.0
		for _, items := range iterables {
			product *= toFloat(items[i])
		}
		total += product
	}
	return total
}
