package val

import (
	"strings"
	"sync"
)

// PEP 750 template strings. Template and Interpolation are instances so
// attribute lookup uses the shape fast path. string.templatelib discovers
// the types with type(t"{0}").

var (
	templateOnce       sync.Once
	TemplateType       *PyType
	InterpolationType  *PyType
	attrStrings        Value
	attrInterpolations Value
	attrValues         Value
	attrValue          Value
	attrExpression     Value
	attrConversion     Value
	attrFormatSpec     Value
)

func initTemplateTypes() {
	templateOnce.Do(func() {
		attrStrings = InternString("strings")
		attrInterpolations = InternString("interpolations")
		attrValues = InternString("values")
		attrValue = InternString("value")
		attrExpression = InternString("expression")
		attrConversion = InternString("conversion")
		attrFormatSpec = InternString("format_spec")

		InterpolationType = MustNewType("Interpolation", []*PyType{ObjectType}, nil)
		InterpolationType.Dict.Set(InternString("__module__"), InternString("string.templatelib"))
		InterpolationType.Slots.TpCall = interpolationTpCall
		InterpolationType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("__repr__", interpolationRepr))

		TemplateType = MustNewType("Template", []*PyType{ObjectType}, nil)
		TemplateType.Dict.Set(InternString("__module__"), InternString("string.templatelib"))
		TemplateType.Slots.TpCall = templateTpCall
		TemplateType.Dict.Set(InternString("__repr__"), NewBuiltinFuncVal("__repr__", templateRepr))
		TemplateType.Slots.TpIter = templateIter
	})
}

func interpolationTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	value, expression, conversion, formatSpec, ok := bindInterpolationArgs(args, kwnames)
	if !ok {
		panic(NewTypeError("Interpolation() missing required argument: 'value'"))
	}
	if !expression.IsAnyString() {
		panic(NewTypeError("Interpolation() argument 'expression' must be str"))
	}
	if !formatSpec.IsAnyString() {
		panic(NewTypeError("Interpolation() argument 'format_spec' must be str"))
	}
	if !conversion.IsNone() {
		if !conversion.IsAnyString() {
			panic(NewTypeError("Interpolation() argument 'conversion' must be str or None"))
		}
		conv := conversion.AsString()
		if conv != "r" && conv != "s" && conv != "a" {
			panic(NewException(ValueErrorType, "Interpolation() conversion must be None, 's', 'r', or 'a'"))
		}
	}
	return MakeInterpolation(value, expression, conversion, formatSpec)
}

func bindInterpolationArgs(args, kwnames []Value) (value, expression, conversion, formatSpec Value, ok bool) {
	expression = NewStringVal("")
	conversion = NoneVal()
	formatSpec = NewStringVal("")
	gotValue := false
	pos := args
	if len(kwnames) > 0 {
		if len(kwnames) > len(args) {
			panic(NewTypeError("Interpolation() got unexpected keyword arguments"))
		}
		pos = args[:len(args)-len(kwnames)]
		for i, kn := range kwnames {
			v := args[len(pos)+i]
			switch kn.AsString() {
			case "value":
				value = v
				gotValue = true
			case "expression":
				expression = v
			case "conversion":
				conversion = v
			case "format_spec":
				formatSpec = v
			default:
				panic(NewTypeError("Interpolation() got an unexpected keyword argument '" + kn.AsString() + "'"))
			}
		}
	}
	if len(pos) > 4 {
		panic(NewTypeError("Interpolation() takes at most 4 positional arguments"))
	}
	if len(pos) > 0 {
		value = pos[0]
		gotValue = true
	}
	if len(pos) > 1 {
		expression = pos[1]
	}
	if len(pos) > 2 {
		conversion = pos[2]
	}
	if len(pos) > 3 {
		formatSpec = pos[3]
	}
	return value, expression, conversion, formatSpec, gotValue
}

// MakeInterpolation builds an Interpolation instance. Callers must pass a str
// expression, a str format spec, and a conversion of None or a one-character str.
func MakeInterpolation(value, expression, conversion, formatSpec Value) Value {
	initTemplateTypes()
	instVal := NewInstanceOfType(InterpolationType)
	inst := instVal.AsInstance()
	inst.SetAttr(attrValue, value)
	inst.SetAttr(attrExpression, expression)
	inst.SetAttr(attrConversion, conversion)
	inst.SetAttr(attrFormatSpec, formatSpec)
	return instVal
}

func interpolationRepr(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) == 0 || !args[0].IsInstance() {
		return InternString("Interpolation()")
	}
	inst := args[0].AsInstance()
	v, _ := inst.GetAttr(attrValue)
	e, _ := inst.GetAttr(attrExpression)
	c, _ := inst.GetAttr(attrConversion)
	f, _ := inst.GetAttr(attrFormatSpec)
	var b strings.Builder
	b.WriteString("Interpolation(")
	b.WriteString(PyRepr(ctx, v))
	b.WriteString(", ")
	b.WriteString(PyRepr(ctx, e))
	b.WriteString(", ")
	b.WriteString(PyRepr(ctx, c))
	b.WriteString(", ")
	b.WriteString(PyRepr(ctx, f))
	b.WriteByte(')')
	return NewStringVal(b.String())
}

func templateTpCall(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(kwnames) > 0 {
		panic(NewTypeError("Template() takes no keyword arguments"))
	}
	return TemplateFromArgs(args)
}

// TemplateFromArgs partitions str and Interpolation arguments the way the
// CPython Template constructor does: adjacent strings are concatenated and
// len(strings) == len(interpolations)+1.
func TemplateFromArgs(args []Value) Value {
	initTemplateTypes()
	var strs []Value
	var interps []Value
	pending := ""
	hasPending := false
	flush := func(emptyIfMissing bool) {
		if hasPending {
			strs = append(strs, NewStringVal(pending))
			pending = ""
			hasPending = false
			return
		}
		if emptyIfMissing {
			strs = append(strs, NewStringVal(""))
		}
	}
	for _, arg := range args {
		if arg.Type() == InterpolationType {
			flush(true)
			interps = append(interps, arg)
			continue
		}
		if !arg.IsAnyString() {
			panic(NewTypeError("Template() arguments must be str or Interpolation"))
		}
		if hasPending {
			pending += arg.AsString()
		} else {
			pending = arg.AsString()
			hasPending = true
		}
	}
	if hasPending {
		strs = append(strs, NewStringVal(pending))
	} else if len(interps) == 0 {
		strs = append(strs, NewStringVal(""))
	} else {
		strs = append(strs, NewStringVal(""))
	}
	return makeTemplate(strs, interps)
}

// MakeTemplate builds a Template from already-partitioned strings and interpolations.
// len(strs) must be len(interps)+1.
func MakeTemplate(strs, interps []Value) Value {
	initTemplateTypes()
	return makeTemplate(strs, interps)
}

func makeTemplate(strs, interps []Value) Value {
	instVal := NewInstanceOfType(TemplateType)
	inst := instVal.AsInstance()
	inst.SetAttr(attrStrings, NewTupleVal(strs...))
	inst.SetAttr(attrInterpolations, NewTupleVal(interps...))
	vals := make([]Value, len(interps))
	for i, iv := range interps {
		if iv.IsInstance() {
			if v, ok := iv.AsInstance().GetAttr(attrValue); ok {
				vals[i] = v
				continue
			}
		}
		vals[i] = NoneVal()
	}
	inst.SetAttr(attrValues, NewTupleVal(vals...))
	return instVal
}

func templateRepr(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
	if len(args) == 0 || !args[0].IsInstance() {
		return InternString("Template()")
	}
	inst := args[0].AsInstance()
	s, _ := inst.GetAttr(attrStrings)
	ip, _ := inst.GetAttr(attrInterpolations)
	var b strings.Builder
	b.WriteString("Template(strings=")
	b.WriteString(PyRepr(ctx, s))
	b.WriteString(", interpolations=")
	b.WriteString(PyRepr(ctx, ip))
	b.WriteByte(')')
	return NewStringVal(b.String())
}

func templateIter(ctx *ExecContext, o Value) Value {
	if !o.IsInstance() {
		panic(NewTypeError("Template.__iter__ expected a Template"))
	}
	inst := o.AsInstance()
	s, _ := inst.GetAttr(attrStrings)
	ip, _ := inst.GetAttr(attrInterpolations)
	var ss, is []Value
	if s.IsTuple() {
		ss = s.AsTuple().Items
	}
	if ip.IsTuple() {
		is = ip.AsTuple().Items
	}
	parts := make([]Value, 0, len(ss)+len(is))
	for i := range is {
		if i < len(ss) && ss[i].IsAnyString() && ss[i].AsString() != "" {
			parts = append(parts, ss[i])
		}
		parts = append(parts, is[i])
	}
	if len(ss) > len(is) {
		last := ss[len(ss)-1]
		if last.IsAnyString() && last.AsString() != "" {
			parts = append(parts, last)
		}
	}
	return NewIterVal(NewTupleVal(parts...))
}
