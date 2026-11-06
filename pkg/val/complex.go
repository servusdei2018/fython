package val

import (
	"math"
	"strconv"
	"strings"
	"unsafe"
)

// PyComplex is a heap-allocated Python complex number.
// Header MUST be at byte offset 0.
type PyComplex struct {
	Header ObjectHeader
	Real   float64
	Imag   float64
}

var _ [0 - unsafe.Offsetof(PyComplex{}.Header)]byte

// NewComplexVal constructs a complex Value.
func NewComplexVal(re, im float64) Value {
	c := &PyComplex{
		Header: ObjectHeader{Kind: KindComplex, Type: ComplexType},
		Real:   re,
		Imag:   im,
	}
	return Value{num: 0, ptr: &c.Header}
}

// AsComplex performs a zero-cost offset-0 downcast from Value to *PyComplex.
func (v Value) AsComplex() *PyComplex {
	return (*PyComplex)(unsafe.Pointer(v.ptr))
}

// IsComplex reports whether v is a complex number.
func (v Value) IsComplex() bool {
	return v.ptr != nil && v.ptr.Kind == KindComplex
}

// toComplexParts converts any Python number to (real, imag).
func toComplexParts(v Value) (re, im float64, ok bool) {
	switch {
	case v.ptr == nil:
		return 0, 0, false
	case v.ptr.Kind == KindComplex:
		c := v.AsComplex()
		return c.Real, c.Imag, true
	case isNumberKind(v):
		return toFloat64(v), 0, true
	}
	return 0, 0, false
}

func complexDiv(ar, ai, br, bi float64) (float64, float64) {
	abr, abi := math.Abs(br), math.Abs(bi)
	switch {
	case abr >= abi:
		if abr == 0 {
			panic("vm: ZeroDivisionError: division by zero")
		}
		ratio := bi / br
		denom := br + bi*ratio
		return (ar + ai*ratio) / denom, (ai - ar*ratio) / denom
	case abi >= abr:
		ratio := br / bi
		denom := br*ratio + bi
		return (ar*ratio + ai) / denom, (ai*ratio - ar) / denom
	}
	return math.NaN(), math.NaN()
}

func complexPowU(ar, ai float64, n int) (float64, float64) {
	r, i := 1.0, 0.0
	mask := 1
	pr, pi := ar, ai
	for mask > 0 && n >= mask {
		if n&mask != 0 {
			r, i = r*pr-i*pi, r*pi+i*pr
		}
		mask <<= 1
		pr, pi = pr*pr-pi*pi, 2*pr*pi
	}
	return r, i
}

func complexPowParts(ar, ai, br, bi float64) (float64, float64) {
	if br == 0 && bi == 0 {
		return 1, 0
	}
	if ar == 0 && ai == 0 {
		if bi != 0 || br < 0 {
			panic("vm: ZeroDivisionError: zero to a negative or complex power")
		}
		return 0, 0
	}
	if bi == 0 && br == math.Floor(br) && math.Abs(br) <= 100 {
		n := int(br)
		if n > 0 {
			return complexPowU(ar, ai, n)
		}
		r, i := complexPowU(ar, ai, -n)
		return complexDiv(1, 0, r, i)
	}
	vabs := math.Hypot(ar, ai)
	length := math.Pow(vabs, br)
	at := math.Atan2(ai, ar)
	phase := at * br
	if bi != 0 {
		length /= math.Exp(at * bi)
		phase += bi * math.Log(vabs)
	}
	return length * math.Cos(phase), length * math.Sin(phase)
}

func complexPow(a, b Value) Value {
	ar, ai, ok1 := toComplexParts(a)
	br, bi, ok2 := toComplexParts(b)
	if !ok1 || !ok2 {
		panic("val: TypeError: unsupported operand type(s) for ** or pow(): '" + a.Type().Name + "' and '" + b.Type().Name + "'")
	}
	r, i := complexPowParts(ar, ai, br, bi)
	if math.IsInf(r, 0) || math.IsInf(i, 0) {
		if !math.IsInf(ar, 0) && !math.IsInf(ai, 0) && !math.IsInf(br, 0) && !math.IsInf(bi, 0) {
			panic("OverflowError: complex exponentiation")
		}
	}
	return NewComplexVal(r, i)
}

// complexBinary applies op ('+', '-', '*', '/') when at least one operand is
// complex and the other is a number. ok is false when the pair is unsupported.
func complexBinary(op byte, a, b Value) (Value, bool) {
	ar, ai, ok1 := toComplexParts(a)
	br, bi, ok2 := toComplexParts(b)
	if !ok1 || !ok2 {
		return Value{}, false
	}
	switch op {
	case '+':
		return NewComplexVal(ar+br, ai+bi), true
	case '-':
		return NewComplexVal(ar-br, ai-bi), true
	case '*':
		return NewComplexVal(ar*br-ai*bi, ar*bi+ai*br), true
	case '/':
		r, i := complexDiv(ar, ai, br, bi)
		return NewComplexVal(r, i), true
	}
	return Value{}, false
}

func complexBinaryAny(op byte, a, b Value) (Value, bool) {
	if a.ptr == nil || b.ptr == nil || (a.ptr.Kind != KindComplex && b.ptr.Kind != KindComplex) {
		return Value{}, false
	}
	return complexBinary(op, a, b)
}

// complexCompare handles ==/!= between a complex and any number.
func complexCompare(a, b Value, op uint8) (Value, bool) {
	if op != CmpEQ && op != CmpNEQ {
		return Value{}, false
	}
	ar, ai, ok1 := toComplexParts(a)
	br, bi, ok2 := toComplexParts(b)
	if !ok1 || !ok2 {
		return Value{}, false
	}
	eq := ar == br && ai == bi
	if op == CmpNEQ {
		eq = !eq
	}
	return BoolVal(eq), true
}

func hashComplex(c *PyComplex) uint32 {
	hr := HashValue(Float64Val(c.Real))
	if c.Imag == 0 {
		return hr
	}
	return hr + 1000003*HashValue(Float64Val(c.Imag))
}

// parseComplexString implements complex("1+2j").
func parseComplexString(s string) (float64, float64) {
	bad := func() { panic("ValueError: complex() arg is a malformed string") }
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		bad()
	}
	parseNum := func(t string, allowEmpty bool) float64 {
		switch t {
		case "":
			if allowEmpty {
				return 1
			}
		case "+":
			if allowEmpty {
				return 1
			}
		case "-":
			if allowEmpty {
				return -1
			}
		}
		if t == "" || strings.ContainsAny(t, "xXpP") || strings.ContainsAny(t, " \t") {
			bad()
		}
		if strings.Contains(t, "_") {
			if strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") {
				bad()
			}
			t = strings.ReplaceAll(t, "_", "")
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return f
			}
			bad()
		}
		return f
	}
	last := s[len(s)-1]
	if last == 'j' || last == 'J' {
		body := s[:len(s)-1]
		split := -1
		for i := len(body) - 1; i > 0; i-- {
			if (body[i] == '+' || body[i] == '-') && body[i-1] != 'e' && body[i-1] != 'E' {
				split = i
				break
			}
		}
		if split < 0 {
			return 0, parseNum(body, true)
		}
		return parseNum(body[:split], false), parseNum(body[split:], true)
	}
	return parseNum(s, false), 0
}

func complexFromArg(a Value, which string) (re, im float64) {
	if r, i, ok := toComplexParts(a); ok {
		return r, i
	}
	panic("TypeError: complex() " + which + " argument must be a string or a number, not '" + a.Type().Name + "'")
}

func complexConstruct(ctx *ExecContext, args []Value, kwnames []Value) Value {
	npos := len(args) - len(kwnames)
	pos := args[:npos]
	var realArg, imagArg Value
	hasReal, hasImag := false, false
	if npos > 2 {
		panic("TypeError: complex() takes at most 2 arguments (" + strconv.Itoa(npos) + " given)")
	}
	if npos > 0 {
		realArg, hasReal = pos[0], true
	}
	if npos > 1 {
		imagArg, hasImag = pos[1], true
	}
	for i, k := range kwnames {
		v := args[npos+i]
		switch k.AsString() {
		case "real":
			if hasReal {
				panic("TypeError: argument for complex() given by name ('real') and position (1)")
			}
			realArg, hasReal = v, true
		case "imag":
			if hasImag {
				panic("TypeError: argument for complex() given by name ('imag') and position (2)")
			}
			imagArg, hasImag = v, true
		default:
			panic("TypeError: complex() got an unexpected keyword argument '" + k.AsString() + "'")
		}
	}
	if !hasReal && !hasImag {
		return NewComplexVal(0, 0)
	}
	var rr, ri, ir, ii float64
	realIsComplex, imagIsComplex := false, false
	if hasReal {
		if realArg.IsAnyString() {
			if hasImag {
				panic("TypeError: complex() can't take second arg if first is a string")
			}
			re, im := parseComplexString(realArg.AsString())
			return NewComplexVal(re, im)
		}
		if realArg.IsComplex() && !hasImag {
			return realArg
		}
		if ctx != nil && !realArg.IsComplex() && !isNumberKind(realArg) {
			if fn, _, ok := realArg.Type().LookupMRO(InternString("__complex__")); ok {
				res := ctx.Invoke1(fn, realArg)
				if !res.IsComplex() {
					panic("TypeError: __complex__ returned non-complex (type " + res.Type().Name + ")")
				}
				if !hasImag {
					return res
				}
				realArg = res
			} else if fn, _, ok := realArg.Type().LookupMRO(InternString("__float__")); ok {
				res := ctx.Invoke1(fn, realArg)
				if res.IsFloat64() {
					realArg = res
				}
			}
		}
		rr, ri = complexFromArg(realArg, "first")
		realIsComplex = realArg.IsComplex()
	}
	if hasImag {
		if imagArg.IsAnyString() {
			panic("TypeError: complex() second arg can't be a string")
		}
		ir, ii = complexFromArg(imagArg, "second")
		imagIsComplex = imagArg.IsComplex()
	}
	if imagIsComplex {
		rr -= ii
	}
	if realIsComplex && hasImag {
		ir += ri
	}
	return NewComplexVal(rr, ir)
}

func complexSelf(args []Value) *PyComplex {
	if len(args) < 1 || !args[0].IsComplex() {
		panic("TypeError: descriptor requires a 'complex' object")
	}
	return args[0].AsComplex()
}

func initComplexType() {
	ComplexType = MustNewType("complex", []*PyType{ObjectType}, nil)
	ct := ComplexType
	ct.Slots.TpCall = func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
		return complexConstruct(ctx, args, kwnames)
	}
	ct.Slots.NbAdd = numericNbAdd
	ct.Slots.NbSub = numericNbSub
	ct.Slots.NbMul = numericNbMul
	ct.Slots.NbAbs = func(ctx *ExecContext, a Value) Value {
		c := a.AsComplex()
		r := math.Hypot(c.Real, c.Imag)
		if math.IsInf(r, 0) && !math.IsInf(c.Real, 0) && !math.IsInf(c.Imag, 0) {
			panic("OverflowError: absolute value too large")
		}
		return Float64Val(r)
	}
	ct.Slots.TpGetAttr = func(ctx *ExecContext, o, name Value) Value {
		if o.IsComplex() && name.IsAnyString() {
			switch name.AsString() {
			case "real":
				return Float64Val(o.AsComplex().Real)
			case "imag":
				return Float64Val(o.AsComplex().Imag)
			}
		}
		return objectGetAttr(ctx, o, name)
	}
	fn := func(name string, f func(ctx *ExecContext, args []Value) Value) {
		ct.Dict.Set(InternString(name), NewBuiltinFuncVal("complex."+name, func(ctx *ExecContext, callable Value, args []Value, kwnames []Value) Value {
			return f(ctx, args)
		}))
	}
	bin := func(name, rname string, op byte) {
		fn(name, func(ctx *ExecContext, args []Value) Value {
			if len(args) != 2 {
				return NotImplementedVal()
			}
			if r, ok := complexBinary(op, args[0], args[1]); ok {
				return r
			}
			return NotImplementedVal()
		})
		fn(rname, func(ctx *ExecContext, args []Value) Value {
			if len(args) != 2 {
				return NotImplementedVal()
			}
			if r, ok := complexBinary(op, args[1], args[0]); ok {
				return r
			}
			return NotImplementedVal()
		})
	}
	bin("__add__", "__radd__", '+')
	bin("__sub__", "__rsub__", '-')
	bin("__mul__", "__rmul__", '*')
	bin("__truediv__", "__rtruediv__", '/')
	fn("__pow__", func(ctx *ExecContext, args []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		if len(args) > 2 && !args[2].IsNone() {
			panic("ValueError: complex modulo")
		}
		if _, _, ok := toComplexParts(args[1]); !ok {
			return NotImplementedVal()
		}
		return complexPow(args[0], args[1])
	})
	fn("__rpow__", func(ctx *ExecContext, args []Value) Value {
		if len(args) < 2 {
			return NotImplementedVal()
		}
		if _, _, ok := toComplexParts(args[1]); !ok {
			return NotImplementedVal()
		}
		return complexPow(args[1], args[0])
	})
	fn("__neg__", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		return NewComplexVal(-c.Real, -c.Imag)
	})
	fn("__pos__", func(ctx *ExecContext, args []Value) Value {
		return args[0]
	})
	fn("__abs__", func(ctx *ExecContext, args []Value) Value {
		return ct.Slots.NbAbs(ctx, args[0])
	})
	fn("__bool__", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		return BoolVal(c.Real != 0 || c.Imag != 0)
	})
	fn("__hash__", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		if c.Imag == 0 && c.Real == math.Trunc(c.Real) && math.Abs(c.Real) < 1<<53 {
			return Int64Val(int64(c.Real))
		}
		return Int64Val(int64(hashComplex(c)))
	})
	fn("__eq__", func(ctx *ExecContext, args []Value) Value {
		if len(args) == 2 {
			if r, ok := complexCompare(args[0], args[1], CmpEQ); ok {
				return r
			}
		}
		return NotImplementedVal()
	})
	fn("__ne__", func(ctx *ExecContext, args []Value) Value {
		if len(args) == 2 {
			if r, ok := complexCompare(args[0], args[1], CmpNEQ); ok {
				return r
			}
		}
		return NotImplementedVal()
	})
	repr := func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		return NewStringVal(ComplexRepr(c.Real, c.Imag))
	}
	fn("__repr__", repr)
	fn("__str__", repr)
	fn("__complex__", func(ctx *ExecContext, args []Value) Value {
		return args[0]
	})
	fn("__getnewargs__", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		return NewTupleVal(Float64Val(c.Real), Float64Val(c.Imag))
	})
	fn("__format__", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		if len(args) != 2 || !args[1].IsAnyString() {
			panic("TypeError: complex.__format__() takes exactly one argument (str)")
		}
		return NewStringVal(FormatComplexSpec(c.Real, c.Imag, args[1].AsString()))
	})
	fn("conjugate", func(ctx *ExecContext, args []Value) Value {
		c := complexSelf(args)
		return NewComplexVal(c.Real, -c.Imag)
	})
	ct.syncDunderSlots()
	// syncDunderSlots installs trampolines that need a VM context; the native
	// arithmetic slots below work without one.
	ct.Slots.NbAdd = numericNbAdd
	ct.Slots.NbSub = numericNbSub
	ct.Slots.NbMul = numericNbMul
	ct.Slots.NbAbs = func(ctx *ExecContext, a Value) Value {
		c := a.AsComplex()
		return Float64Val(math.Hypot(c.Real, c.Imag))
	}
}
