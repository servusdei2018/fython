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
	registerBinary("hypot", math.Hypot)
	registerBinary("fmod", math.Mod)
	registerBinary("remainder", math.Remainder)

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
