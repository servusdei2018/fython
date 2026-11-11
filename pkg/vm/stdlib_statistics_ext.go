package vm

import (
	"math"

	"github.com/servusdei2018/fython/pkg/val"
)

// normalDistInvCDF evaluates the inverse of the cumulative distribution
// function of the normal distribution. It uses the rational approximation of
// Peter Acklam, refined with one step of Halley's method.
func normalDistInvCDF(p, mu, sigma float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	q := p - 0.5
	var r float64
	if math.Abs(q) <= 0.425 {
		r = 0.180625 - q*q
		num := (((((((2509.0809287301226727*r+33430.575583588128105)*r+67265.770927008700853)*r+45921.953931549871457)*r+13731.693765509461125)*r+1971.5909503065514427)*r+133.14166789178437745)*r + 3.387132872796366608)
		den := (((((((5226.495278852854561*r+28729.085735721942674)*r+39307.89580009271061)*r+21213.794301586595867)*r+5394.1960214247511077)*r+687.1870074920579083)*r+42.313330701600911252)*r + 1)
		r = q * num / den
	} else {
		var num, den float64
		if q < 0 {
			r = math.Sqrt(-math.Log(p))
		} else {
			r = math.Sqrt(-math.Log(1 - p))
		}
		if r <= 5 {
			r -= 1.6
			num = (((((((7.7454501427834140764e-4*r+0.0227238449892691845833)*r+0.24178072517745061177)*r+1.27045825245236838258)*r+3.64784832476320460504)*r+5.7694972214606914055)*r+4.6303378461565452959)*r + 1.42343711074968357734)
			den = (((((((1.05075007164441684324e-9*r+5.475938084995344946e-4)*r+0.0151986665636164571966)*r+0.14810397642748007459)*r+0.68976733498510000455)*r+1.6763848301838038494)*r+2.05319162663775882187)*r + 1)
		} else {
			r -= 5
			num = (((((((2.01033439929228813265e-7*r+2.71155556874348757815e-5)*r+0.0012426609473880784386)*r+0.026532189526576123093)*r+0.29656057182850489123)*r+1.7848265399172913358)*r+5.4637849111641143699)*r + 6.6579046435011037772)
			den = (((((((2.04426310338993978564e-15*r+1.4215117583164458887e-7)*r+1.8463183175100546818e-5)*r+7.868691311456132591e-4)*r+0.0148753612908506148525)*r+0.13692988092273580531)*r+0.59983220655588793769)*r + 1)
		}
		r = num / den
		if q < 0 {
			r = -r
		}
	}
	// One step of Halley's method refines the approximation.
	e := 0.5*math.Erfc(-r/math.Sqrt2) - p
	u := e * math.Sqrt(2*math.Pi) * math.Exp(r*r/2)
	r -= u / (1 + r*u/2)
	return mu + sigma*r
}

// createStatisticsExtModule builds the _statistics accelerator.
func (e *Engine) createStatisticsExtModule() val.Value {
	modVal := val.NewModuleVal("_statistics", "statistics accelerator")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("_normal_dist_inv_cdf"), val.NewBuiltinFuncVal("_normal_dist_inv_cdf", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: _normal_dist_inv_cdf() takes 3 arguments")
		}
		return val.Float64Val(normalDistInvCDF(toFloat(args[0]), toFloat(args[1]), toFloat(args[2])))
	}))
	return modVal
}
