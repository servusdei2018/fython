package vm

import (
	"strconv"
	"unicode/utf8"

	"github.com/servusdei2018/fython/pkg/unicodedata"
	"github.com/servusdei2018/fython/pkg/val"
)

// unicodedataChar validates a one-character str argument.
func unicodedataChar(fn string, args []val.Value, i int) rune {
	if len(args) <= i {
		panic("TypeError: " + fn + "() missing required argument 'chr' (pos " + strconv.Itoa(i+1) + ")")
	}
	a := args[i]
	if !a.IsAnyString() {
		panic("TypeError: " + fn + "() argument " + strconv.Itoa(i+1) + " must be a unicode character, not " + a.Type().Name)
	}
	s := a.AsString()
	if utf8.RuneCountInString(s) != 1 {
		panic("TypeError: " + fn + "() argument " + strconv.Itoa(i+1) + " must be a unicode character, not a string of length " + strconv.Itoa(utf8.RuneCountInString(s)))
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func (e *Engine) createUnicodedataModule() val.Value {
	modVal := val.NewModuleVal("unicodedata", "")
	mod := modVal.AsModule()
	def := func(name string, fn func(args []val.Value) val.Value) {
		mod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return fn(args)
		}))
	}
	mod.Dict.Set(val.InternString("unidata_version"), val.InternString("16.0.0"))

	def("lookup", func(args []val.Value) val.Value {
		if len(args) != 1 || !args[0].IsAnyString() {
			panic("TypeError: lookup() argument must be str")
		}
		name := args[0].AsString()
		r, ok := unicodedata.Lookup(name)
		if !ok {
			panic("KeyError: \"undefined character name '" + name + "'\"")
		}
		return val.InternString(string(r))
	})
	def("name", func(args []val.Value) val.Value {
		r := unicodedataChar("name", args, 0)
		if n, ok := unicodedata.Name(r); ok {
			return val.InternString(n)
		}
		if len(args) > 1 {
			return args[1]
		}
		panic("ValueError: no such name")
	})
	def("category", func(args []val.Value) val.Value {
		return val.InternString(unicodedata.Category(unicodedataChar("category", args, 0)))
	})
	numeric := func(fn string) func(args []val.Value) val.Value {
		return func(args []val.Value) val.Value {
			r := unicodedataChar(fn, args, 0)
			if d, ok := unicodedata.Decimal(r); ok {
				if fn == "numeric" {
					return val.Float64Val(float64(d))
				}
				return val.Int64Val(int64(d))
			}
			if len(args) > 1 {
				return args[1]
			}
			if fn == "numeric" {
				panic("ValueError: not a numeric character")
			}
			panic("ValueError: not a " + fn + " digit")
		}
	}
	def("decimal", numeric("decimal"))
	def("digit", numeric("digit"))
	def("numeric", numeric("numeric"))
	def("normalize", func(args []val.Value) val.Value {
		if len(args) != 2 || !args[0].IsAnyString() || !args[1].IsAnyString() {
			panic("TypeError: normalize() argument must be str")
		}
		out, ok := unicodedata.Normalize(args[0].AsString(), args[1].AsString())
		if !ok {
			panic("ValueError: invalid normalization form")
		}
		return val.InternString(out)
	})
	def("is_normalized", func(args []val.Value) val.Value {
		if len(args) != 2 || !args[0].IsAnyString() || !args[1].IsAnyString() {
			panic("TypeError: is_normalized() argument must be str")
		}
		out, ok := unicodedata.Normalize(args[0].AsString(), args[1].AsString())
		if !ok {
			panic("ValueError: invalid normalization form")
		}
		return val.BoolVal(out == args[1].AsString())
	})
	return modVal
}
