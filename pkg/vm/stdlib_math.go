package vm

import (
	"math"

	"github.com/servusdei2018/fython/pkg/val"
)

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

	// gcd(a, b)
	mod.Dict.Set(val.InternString("gcd"), val.NewBuiltinFuncVal("gcd", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: gcd() takes at least 2 arguments")
		}
		a := args[0].AsInt64()
		b := args[1].AsInt64()
		if a < 0 {
			a = -a
		}
		if b < 0 {
			b = -b
		}
		for b != 0 {
			a, b = b, a%b
		}
		return val.Int64Val(a)
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
