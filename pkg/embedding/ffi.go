package embedding

import (
	"fmt"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Sandbox resource limits exported from vm package.
type RuntimeLimits = vm.RuntimeLimits

var (
	ErrStepLimitExceeded     = vm.ErrStepLimitExceeded
	ErrTimeoutExceeded       = vm.ErrTimeoutExceeded
	ErrMaxStackDepthExceeded = vm.ErrMaxStackDepthExceeded
)

// RunWithLimits compiles and runs Python source on v subject to limits.
func RunWithLimits(v *vm.VM, src string, limits RuntimeLimits) (val.Value, error) {
	co, err := compiler.CompileString(src)
	if err != nil {
		return val.NoneVal(), err
	}
	return v.RunCodeWithLimits(co, limits)
}

// Unbox extracts a Go value of type T from a val.Value with zero heap allocations.
func Unbox[T any](v val.Value) T {
	var zero T
	switch any(zero).(type) {
	case int64:
		return any(v.AsInt64()).(T)
	case int:
		return any(int(v.AsInt64())).(T)
	case uint64:
		return any(uint64(v.AsInt64())).(T)
	case float64:
		return any(v.AsFloat64()).(T)
	case string:
		return any(v.AsString()).(T)
	case []byte:
		if v.IsBytes() {
			return any(v.AsBytes().Data).(T)
		} else if v.IsByteArray() {
			return any(v.AsByteArray().Data).(T)
		} else if v.IsMemoryView() {
			return any(v.AsMemoryView().Buf).(T)
		}
		s := v.AsString()
		return any([]byte(s)).(T)
	case bool:
		return any(v.AsBool()).(T)
	case val.Value:
		return any(v).(T)
	default:
		panic(fmt.Sprintf("fython embedding: unsupported parameter type %T", zero))
	}
}

// Box wraps a Go value of type T into a val.Value with zero heap allocations for primitives.
func Box[T any](t T) val.Value {
	switch v := any(t).(type) {
	case int64:
		return val.Int64Val(v)
	case int:
		return val.Int64Val(int64(v))
	case uint64:
		return val.Int64Val(int64(v))
	case float64:
		return val.Float64Val(v)
	case string:
		return val.NewStringVal(v)
	case []byte:
		return val.NewBytesVal(v)
	case bool:
		return val.BoolVal(v)
	case val.Value:
		return v
	case nil:
		return val.NoneVal()
	default:
		panic(fmt.Sprintf("fython embedding: unsupported return type %T", t))
	}
}

// RegisterFunc0 creates a PyBuiltinFunc wrapping a 0-arg Go function.
func RegisterFunc0[R any](name string, fn func() R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return Box[R](fn())
	})
}

// RegisterFunc0Err creates a PyBuiltinFunc wrapping a 0-arg Go function returning (R, error).
func RegisterFunc0Err[R any](name string, fn func() (R, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		r, err := fn()
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return Box[R](r)
	})
}

// RegisterFunc1 creates a PyBuiltinFunc wrapping a 1-arg Go function.
func RegisterFunc1[A, R any](name string, fn func(A) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(fmt.Sprintf("TypeError: %s() missing 1 required positional argument", name))
		}
		a := Unbox[A](args[0])
		return Box[R](fn(a))
	})
}

// RegisterFunc1Err creates a PyBuiltinFunc wrapping a 1-arg Go function returning (R, error).
func RegisterFunc1Err[A, R any](name string, fn func(A) (R, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(fmt.Sprintf("TypeError: %s() missing 1 required positional argument", name))
		}
		a := Unbox[A](args[0])
		r, err := fn(a)
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return Box[R](r)
	})
}

// RegisterFunc2 creates a PyBuiltinFunc wrapping a 2-arg Go function.
func RegisterFunc2[A, B, R any](name string, fn func(A, B) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		return Box[R](fn(a, b))
	})
}

// RegisterFunc2Err creates a PyBuiltinFunc wrapping a 2-arg Go function returning (R, error).
func RegisterFunc2Err[A, B, R any](name string, fn func(A, B) (R, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		r, err := fn(a, b)
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return Box[R](r)
	})
}

// RegisterFunc3 creates a PyBuiltinFunc wrapping a 3-arg Go function.
func RegisterFunc3[A, B, C, R any](name string, fn func(A, B, C) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		return Box[R](fn(a, b, c))
	})
}

// RegisterFunc3Err creates a PyBuiltinFunc wrapping a 3-arg Go function returning (R, error).
func RegisterFunc3Err[A, B, C, R any](name string, fn func(A, B, C) (R, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		r, err := fn(a, b, c)
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return Box[R](r)
	})
}

// RegisterFunc4 creates a PyBuiltinFunc wrapping a 4-arg Go function.
func RegisterFunc4[A, B, C, D, R any](name string, fn func(A, B, C, D) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		d := Unbox[D](args[3])
		return Box[R](fn(a, b, c, d))
	})
}

// RegisterFunc4Err creates a PyBuiltinFunc wrapping a 4-arg Go function returning (R, error).
func RegisterFunc4Err[A, B, C, D, R any](name string, fn func(A, B, C, D) (R, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		d := Unbox[D](args[3])
		r, err := fn(a, b, c, d)
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return Box[R](r)
	})
}

// RegisterRawFunc wraps a generic slice-based function.
func RegisterRawFunc(name string, fn func(ctx *val.ExecContext, args []val.Value) (val.Value, error)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		r, err := fn(ctx, args)
		if err != nil {
			panic(val.NewRuntimeError(err.Error()))
		}
		return r
	})
}

// Backward compatibility BindFunc wrappers
func BindFunc0[R any](name string, fn func() R) val.Value {
	return RegisterFunc0[R](name, fn)
}
func BindFunc1[A, R any](name string, fn func(A) R) val.Value {
	return RegisterFunc1[A, R](name, fn)
}
func BindFunc2[A, B, R any](name string, fn func(A, B) R) val.Value {
	return RegisterFunc2[A, B, R](name, fn)
}
func BindFunc3[A, B, C, R any](name string, fn func(A, B, C) R) val.Value {
	return RegisterFunc3[A, B, C, R](name, fn)
}
func BindFunc4[A, B, C, D, R any](name string, fn func(A, B, C, D) R) val.Value {
	return RegisterFunc4[A, B, C, D, R](name, fn)
}
func BindAction0(name string, fn func()) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fn()
		return val.NoneVal()
	})
}
func BindAction1[A any](name string, fn func(A)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(fmt.Sprintf("TypeError: %s() missing 1 required positional argument", name))
		}
		fn(Unbox[A](args[0]))
		return val.NoneVal()
	})
}
func BindAction2[A, B any](name string, fn func(A, B)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		fn(Unbox[A](args[0]), Unbox[B](args[1]))
		return val.NoneVal()
	})
}
func BindAction3[A, B, C any](name string, fn func(A, B, C)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		fn(Unbox[A](args[0]), Unbox[B](args[1]), Unbox[C](args[2]))
		return val.NoneVal()
	})
}
func BindAction4[A, B, C, D any](name string, fn func(A, B, C, D)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		fn(Unbox[A](args[0]), Unbox[B](args[1]), Unbox[C](args[2]), Unbox[D](args[3]))
		return val.NoneVal()
	})
}

// StructFieldDescriptor represents a fast data descriptor storing typed getter and setter closures.
type StructFieldDescriptor struct {
	Header  val.ObjectHeader
	Name    string
	GetFunc func(ctx *val.ExecContext, self val.Value) val.Value
	SetFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
}

var structFieldType *val.PyType

func initStructFieldType() {
	if structFieldType != nil {
		return
	}
	structFieldType = val.MustNewType("struct_field_descriptor", []*val.PyType{val.ObjectType}, nil)
	structFieldType.Slots.TpDescrGet = func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
		if obj.IsNone() || obj.IsUnbound() {
			return descr
		}
		sfd := (*StructFieldDescriptor)(unsafe.Pointer(descr.HeaderPtr()))
		return sfd.GetFunc(ctx, obj)
	}
	structFieldType.Slots.TpDescrSet = func(ctx *val.ExecContext, descr, obj, v val.Value) {
		sfd := (*StructFieldDescriptor)(unsafe.Pointer(descr.HeaderPtr()))
		if sfd.SetFunc == nil {
			panic(fmt.Sprintf("AttributeError: attribute '%s' is read-only", sfd.Name))
		}
		sfd.SetFunc(ctx, obj, v)
	}
}

// PyGoStruct wraps an arbitrary Go struct pointer as a Python Value.
// Inst MUST be at offset 0 so that obj.IsInstance() and obj.AsInstance() are valid.
type PyGoStruct[T any] struct {
	Inst val.PyInstance
	Ptr  *T
}

// StructBinding registers and binds a Go struct type T for exposure to fython.
type StructBinding[T any] struct {
	name   string
	pyType *val.PyType
}

// RegisterStructType registers a new StructBinding for Go struct pointer *T.
func RegisterStructType[T any](name string) *StructBinding[T] {
	return NewStructBinding[T](name)
}

// NewStructBinding creates a new StructBinding for Go struct type T.
func NewStructBinding[T any](name string) *StructBinding[T] {
	initStructFieldType()
	cls := val.MustNewType(name, []*val.PyType{val.ObjectType}, nil)
	return &StructBinding[T]{
		name:   name,
		pyType: cls,
	}
}

// Type returns the underlying *val.PyType.
func (sb *StructBinding[T]) Type() *val.PyType {
	return sb.pyType
}

// Wrap wraps a Go pointer *T into a Python Value belonging to this struct's type.
func (sb *StructBinding[T]) Wrap(ptr *T) val.Value {
	obj := &PyGoStruct[T]{
		Inst: val.PyInstance{
			Header: val.ObjectHeader{
				Kind: val.KindInstance,
				Type: sb.pyType,
			},
			Shape: sb.pyType.RootShape,
		},
		Ptr: ptr,
	}
	return val.ObjectVal(&obj.Inst.Header)
}

// Unbox extracts *T from a wrapped Python Value.
func (sb *StructBinding[T]) Unbox(v val.Value) *T {
	return unboxSelf[T](v)
}

func unboxSelf[T any](receiver val.Value) *T {
	gs := (*PyGoStruct[T])(unsafe.Pointer(receiver.HeaderPtr()))
	return gs.Ptr
}

// RegisterFieldInt64 registers an int64 field accessor with 0 reflect and 0 allocations.
func (sb *StructBinding[T]) RegisterFieldInt64(name string, getter func(*T) int64, setter func(*T, int64)) {
	var setFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
	if setter != nil {
		setFunc = func(ctx *val.ExecContext, self val.Value, v val.Value) {
			ptr := unboxSelf[T](self)
			setter(ptr, v.AsInt64())
		}
	}
	sfd := &StructFieldDescriptor{
		Header: val.ObjectHeader{
			Kind:  val.KindNative,
			Flags: val.FlagDataDescriptor,
			Type:  structFieldType,
		},
		Name: name,
		GetFunc: func(ctx *val.ExecContext, self val.Value) val.Value {
			ptr := unboxSelf[T](self)
			return val.Int64Val(getter(ptr))
		},
		SetFunc: setFunc,
	}
	sb.pyType.Dict.Set(val.InternString(name), val.ObjectVal(&sfd.Header))
}

// RegisterFieldFloat64 registers a float64 field accessor with 0 reflect and 0 allocations.
func (sb *StructBinding[T]) RegisterFieldFloat64(name string, getter func(*T) float64, setter func(*T, float64)) {
	var setFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
	if setter != nil {
		setFunc = func(ctx *val.ExecContext, self val.Value, v val.Value) {
			ptr := unboxSelf[T](self)
			setter(ptr, v.AsFloat64())
		}
	}
	sfd := &StructFieldDescriptor{
		Header: val.ObjectHeader{
			Kind:  val.KindNative,
			Flags: val.FlagDataDescriptor,
			Type:  structFieldType,
		},
		Name: name,
		GetFunc: func(ctx *val.ExecContext, self val.Value) val.Value {
			ptr := unboxSelf[T](self)
			return val.Float64Val(getter(ptr))
		},
		SetFunc: setFunc,
	}
	sb.pyType.Dict.Set(val.InternString(name), val.ObjectVal(&sfd.Header))
}

// RegisterFieldString registers a string field accessor with 0 reflect and 0 allocations.
func (sb *StructBinding[T]) RegisterFieldString(name string, getter func(*T) string, setter func(*T, string)) {
	var setFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
	if setter != nil {
		setFunc = func(ctx *val.ExecContext, self val.Value, v val.Value) {
			ptr := unboxSelf[T](self)
			setter(ptr, v.AsString())
		}
	}
	sfd := &StructFieldDescriptor{
		Header: val.ObjectHeader{
			Kind:  val.KindNative,
			Flags: val.FlagDataDescriptor,
			Type:  structFieldType,
		},
		Name: name,
		GetFunc: func(ctx *val.ExecContext, self val.Value) val.Value {
			ptr := unboxSelf[T](self)
			return val.NewStringVal(getter(ptr))
		},
		SetFunc: setFunc,
	}
	sb.pyType.Dict.Set(val.InternString(name), val.ObjectVal(&sfd.Header))
}

// RegisterFieldBool registers a bool field accessor with 0 reflect and 0 allocations.
func (sb *StructBinding[T]) RegisterFieldBool(name string, getter func(*T) bool, setter func(*T, bool)) {
	var setFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
	if setter != nil {
		setFunc = func(ctx *val.ExecContext, self val.Value, v val.Value) {
			ptr := unboxSelf[T](self)
			setter(ptr, v.AsBool())
		}
	}
	sfd := &StructFieldDescriptor{
		Header: val.ObjectHeader{
			Kind:  val.KindNative,
			Flags: val.FlagDataDescriptor,
			Type:  structFieldType,
		},
		Name: name,
		GetFunc: func(ctx *val.ExecContext, self val.Value) val.Value {
			ptr := unboxSelf[T](self)
			return val.BoolVal(getter(ptr))
		},
		SetFunc: setFunc,
	}
	sb.pyType.Dict.Set(val.InternString(name), val.ObjectVal(&sfd.Header))
}

// RegisterField registers a generic field accessor on sb.
func RegisterField[T, F any](sb *StructBinding[T], name string, getter func(*T) F, setter func(*T, F)) {
	var setFunc func(ctx *val.ExecContext, self val.Value, v val.Value)
	if setter != nil {
		setFunc = func(ctx *val.ExecContext, self val.Value, v val.Value) {
			ptr := unboxSelf[T](self)
			setter(ptr, Unbox[F](v))
		}
	}
	sfd := &StructFieldDescriptor{
		Header: val.ObjectHeader{
			Kind:  val.KindNative,
			Flags: val.FlagDataDescriptor,
			Type:  structFieldType,
		},
		Name: name,
		GetFunc: func(ctx *val.ExecContext, self val.Value) val.Value {
			ptr := unboxSelf[T](self)
			return Box[F](getter(ptr))
		},
		SetFunc: setFunc,
	}
	sb.pyType.Dict.Set(val.InternString(name), val.ObjectVal(&sfd.Header))
}

// Method binding helpers
func (sb *StructBinding[T]) RegisterMethod0(name string, fn func(*T)) {
	BindMethod0(sb, name, fn)
}

func (sb *StructBinding[T]) RegisterMethod0R(name string, fn any) {
	switch f := fn.(type) {
	case func(*T) (val.Value, error):
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: method requires 'self' argument")
			}
			self := unboxSelf[T](args[0])
			r, err := f(self)
			if err != nil {
				panic(val.NewRuntimeError(err.Error()))
			}
			return r
		}))
	default:
		BindMethod0R(sb, name, f.(func(*T) val.Value))
	}
}

func (sb *StructBinding[T]) RegisterMethod1(name string, fn func(*T, val.Value)) {
	BindMethod1(sb, name, fn)
}

func (sb *StructBinding[T]) RegisterMethod1R(name string, fn any) {
	switch f := fn.(type) {
	case func(*T, float64) float64:
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: method requires 1 argument")
			}
			self := unboxSelf[T](args[0])
			return val.Float64Val(f(self, args[1].AsFloat64()))
		}))
	case func(*T, int64) int64:
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: method requires 1 argument")
			}
			self := unboxSelf[T](args[0])
			return val.Int64Val(f(self, args[1].AsInt64()))
		}))
	case func(*T, string) string:
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: method requires 1 argument")
			}
			self := unboxSelf[T](args[0])
			return val.NewStringVal(f(self, args[1].AsString()))
		}))
	default:
		panic(fmt.Sprintf("RegisterMethod1R: unsupported signature for %s", name))
	}
}

func (sb *StructBinding[T]) RegisterMethod2R(name string, fn any) {
	switch f := fn.(type) {
	case func(*T, float64, float64) float64:
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: method requires 2 arguments")
			}
			self := unboxSelf[T](args[0])
			return val.Float64Val(f(self, args[1].AsFloat64(), args[2].AsFloat64()))
		}))
	case func(*T, int64, int64) int64:
		sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 3 {
				panic("TypeError: method requires 2 arguments")
			}
			self := unboxSelf[T](args[0])
			return val.Int64Val(f(self, args[1].AsInt64(), args[2].AsInt64()))
		}))
	default:
		panic(fmt.Sprintf("RegisterMethod2R: unsupported signature for %s", name))
	}
}

// BindMethod0 binds a method on sb with 0 arguments and no return value.
func BindMethod0[T any](sb *StructBinding[T], name string, fn func(*T)) {
	sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: method requires 'self' argument")
		}
		self := unboxSelf[T](args[0])
		fn(self)
		return val.NoneVal()
	}))
}

// BindMethod0R binds a method on sb with 0 arguments returning R.
func BindMethod0R[T, R any](sb *StructBinding[T], name string, fn func(*T) R) {
	sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: method requires 'self' argument")
		}
		self := unboxSelf[T](args[0])
		r := fn(self)
		return Box[R](r)
	}))
}

// BindMethod1 binds a method on sb with 1 argument and no return value.
func BindMethod1[T, A any](sb *StructBinding[T], name string, fn func(*T, A)) {
	sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: method requires 1 argument")
		}
		self := unboxSelf[T](args[0])
		a := Unbox[A](args[1])
		fn(self, a)
		return val.NoneVal()
	}))
}

// BindMethod1R binds a method on sb with 1 argument returning R.
func BindMethod1R[T, A, R any](sb *StructBinding[T], name string, fn func(*T, A) R) {
	sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: method requires 1 argument")
		}
		self := unboxSelf[T](args[0])
		a := Unbox[A](args[1])
		r := fn(self, a)
		return Box[R](r)
	}))
}

// BindMethod2R binds a method on sb with 2 arguments returning R.
func BindMethod2R[T, A, B, R any](sb *StructBinding[T], name string, fn func(*T, A, B) R) {
	sb.pyType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: method requires 2 arguments")
		}
		self := unboxSelf[T](args[0])
		a := Unbox[A](args[1])
		b := Unbox[B](args[2])
		r := fn(self, a, b)
		return Box[R](r)
	}))
}
