package embedding

import (
	"fmt"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

// Unbox extracts a Go value of type T from a val.Value with zero heap allocations.
func Unbox[T any](v val.Value) T {
	var zero T
	switch any(zero).(type) {
	case int64:
		return any(v.AsInt64()).(T)
	case int:
		return any(int(v.AsInt64())).(T)
	case float64:
		return any(v.AsFloat64()).(T)
	case string:
		return any(v.AsString()).(T)
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
	case float64:
		return val.Float64Val(v)
	case string:
		return val.NewStringVal(v)
	case bool:
		return val.BoolVal(v)
	case val.Value:
		return v
	default:
		panic(fmt.Sprintf("fython embedding: unsupported return type %T", t))
	}
}

// BindFunc0 creates a PyBuiltinFunc wrapping a 0-arg Go function.
func BindFunc0[R any](name string, fn func() R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		r := fn()
		return Box[R](r)
	})
}

// BindFunc1 creates a PyBuiltinFunc wrapping a 1-arg Go function.
func BindFunc1[A, R any](name string, fn func(A) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(fmt.Sprintf("TypeError: %s() missing 1 required positional argument", name))
		}
		a := Unbox[A](args[0])
		r := fn(a)
		return Box[R](r)
	})
}

// BindFunc2 creates a PyBuiltinFunc wrapping a 2-arg Go function.
func BindFunc2[A, B, R any](name string, fn func(A, B) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		r := fn(a, b)
		return Box[R](r)
	})
}

// BindFunc3 creates a PyBuiltinFunc wrapping a 3-arg Go function.
func BindFunc3[A, B, C, R any](name string, fn func(A, B, C) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		r := fn(a, b, c)
		return Box[R](r)
	})
}

// BindFunc4 creates a PyBuiltinFunc wrapping a 4-arg Go function.
func BindFunc4[A, B, C, D, R any](name string, fn func(A, B, C, D) R) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		d := Unbox[D](args[3])
		r := fn(a, b, c, d)
		return Box[R](r)
	})
}

// BindAction0 creates a PyBuiltinFunc wrapping a void 0-arg Go function.
func BindAction0(name string, fn func()) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fn()
		return val.NoneVal()
	})
}

// BindAction1 creates a PyBuiltinFunc wrapping a void 1-arg Go function.
func BindAction1[A any](name string, fn func(A)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic(fmt.Sprintf("TypeError: %s() missing 1 required positional argument", name))
		}
		a := Unbox[A](args[0])
		fn(a)
		return val.NoneVal()
	})
}

// BindAction2 creates a PyBuiltinFunc wrapping a void 2-arg Go function.
func BindAction2[A, B any](name string, fn func(A, B)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		fn(a, b)
		return val.NoneVal()
	})
}

// BindAction3 creates a PyBuiltinFunc wrapping a void 3-arg Go function.
func BindAction3[A, B, C any](name string, fn func(A, B, C)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		fn(a, b, c)
		return val.NoneVal()
	})
}

// BindAction4 creates a PyBuiltinFunc wrapping a void 4-arg Go function.
func BindAction4[A, B, C, D any](name string, fn func(A, B, C, D)) val.Value {
	return val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic(fmt.Sprintf("TypeError: %s() missing required positional arguments", name))
		}
		a := Unbox[A](args[0])
		b := Unbox[B](args[1])
		c := Unbox[C](args[2])
		d := Unbox[D](args[3])
		fn(a, b, c, d)
		return val.NoneVal()
	})
}

// PyGoStruct wraps an arbitrary Go struct pointer as a Python Value.
// Header MUST be at offset 0.
type PyGoStruct[T any] struct {
	Header val.ObjectHeader
	Ptr    *T
}

// StructBinding registers and binds a Go struct type T for exposure to fython.
type StructBinding[T any] struct {
	name   string
	pyType *val.PyType
}

// NewStructBinding creates a new StructBinding for Go struct type T.
func NewStructBinding[T any](name string) *StructBinding[T] {
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
		Header: val.ObjectHeader{
			Kind: val.KindNative,
			Type: sb.pyType,
		},
		Ptr: ptr,
	}
	return val.ObjectVal(&obj.Header)
}

// unboxSelf extracts *T from receiver args[0].
func unboxSelf[T any](receiver val.Value) *T {
	gs := (*PyGoStruct[T])(unsafe.Pointer(receiver.HeaderPtr()))
	return gs.Ptr
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
