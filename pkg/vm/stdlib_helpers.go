package vm

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"

	"github.com/servusdei2018/fython/pkg/val"
)

// posArgs strips trailing keyword values so a builtin sees only positionals.
func posArgs(args, kwnames []val.Value) []val.Value {
	if len(kwnames) == 0 {
		return args
	}
	return args[:len(args)-len(kwnames)]
}

// kwArg returns the keyword argument called name, if present.
func kwArg(args, kwnames []val.Value, name string) (val.Value, bool) {
	start := len(args) - len(kwnames)
	for i, kn := range kwnames {
		if kn.AsString() == name {
			return args[start+i], true
		}
	}
	return val.NoneVal(), false
}

func typeNameOf(v val.Value) string {
	if t := v.Type(); t != nil {
		return t.Name
	}
	return "object"
}

// intOf reads an int or any object that implements __index__ (IntEnum members).
func intOf(ctx *val.ExecContext, v val.Value) (int64, bool) {
	if v.IsInt64() || v.IsBool() {
		return v.AsInt64(), true
	}
	if t := v.Type(); t != nil && !v.IsAnyString() {
		if fn, _, ok := t.LookupMRO(val.InternString("__index__")); ok {
			if r := ctx.Invoke1(fn, v); r.IsInt64() {
				return r.AsInt64(), true
			}
		}
	}
	return 0, false
}

// pathArg converts a str, bytes or os.PathLike value into a Go string.
func pathArg(ctx *val.ExecContext, v val.Value) string {
	switch {
	case v.IsAnyString():
		return v.AsString()
	case v.IsBytes():
		return string(v.AsBytes().Data)
	}
	if r, ok := fspathOf(ctx, v); ok {
		if r.IsAnyString() {
			return r.AsString()
		}
		if r.IsBytes() {
			return string(r.AsBytes().Data)
		}
	}
	ctx.Raise(val.NewException(val.TypeErrorType, "expected str, bytes or os.PathLike object, not"+typeNameOf(v)))
	return ""
}

// fspathOf calls v.__fspath__() when the type defines it.
func fspathOf(ctx *val.ExecContext, v val.Value) (val.Value, bool) {
	t := v.Type()
	if t == nil {
		return val.NoneVal(), false
	}
	name := val.InternString("__fspath__")
	if _, _, ok := t.LookupMRO(name); !ok {
		return val.NoneVal(), false
	}
	m := val.GetAttrCtx(ctx, v, name)
	r := ctx.Invoke0(m)
	if !r.IsAnyString() && !r.IsBytes() {
		ctx.Raise(val.NewException(val.TypeErrorType, "expected"+typeNameOf(v)+".__fspath__() to return str or bytes, not "+typeNameOf(r)))
		return val.NoneVal(), false
	}
	return r, true
}

// errnoOf extracts a syscall.Errno from err, or 0.
func errnoOf(err error) syscall.Errno {
	if en, ok := errors.AsType[syscall.Errno](err); ok {
		return en
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	}
	return 0
}

// strerrorOf formats an errno the way CPython does ("No such file or directory").
func strerrorOf(n int) string {
	if n == 0 {
		return "Success"
	}
	s := syscall.Errno(n).Error()
	if s == "" {
		return "Unknown error " + itoa(n)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func osErrorClass(en syscall.Errno) *val.PyType {
	switch en {
	case syscall.ENOENT:
		return val.FileNotFoundErrorType
	case syscall.EACCES, syscall.EPERM:
		return val.PermissionErrorType
	case syscall.EEXIST:
		return val.FileExistsErrorType
	case syscall.EISDIR:
		return val.IsADirectoryErrorType
	case syscall.ENOTDIR:
		return val.NotADirectoryErrorType
	case syscall.EINTR:
		return val.InterruptedErrorType
	case syscall.ESRCH:
		return val.ProcessLookupErrorType
	case syscall.ETIMEDOUT:
		return val.TimeoutErrorType
	case syscall.EPIPE:
		return val.BrokenPipeErrorType
	case syscall.ECONNABORTED:
		return val.ConnectionAbortedErrorType
	case syscall.ECONNREFUSED:
		return val.ConnectionRefusedErrorType
	case syscall.ECONNRESET:
		return val.ConnectionResetErrorType
	case syscall.ECHILD:
		return val.ChildProcessErrorType
	case syscall.EAGAIN:
		return val.BlockingIOErrorType
	}
	return val.OSErrorType
}

// newOSError builds an OSError subclass instance with errno, strerror and
// filename attributes, matching "[Errno 2] No such file or directory: 'x'".
func newOSError(en syscall.Errno, filename string, hasFilename bool) val.Value {
	msg := "[Errno " + itoa(int(en)) + "] " + strerrorOf(int(en))
	if hasFilename {
		msg += ": " + val.PyRepr(nil, val.NewStringVal(filename))
	}
	exc := val.NewException(osErrorClass(en), msg)
	inst := exc.AsInstance()
	inst.SetAttr(val.InternString("errno"), val.Int64Val(int64(en)))
	inst.SetAttr(val.InternString("strerror"), val.NewStringVal(strerrorOf(int(en))))
	if hasFilename {
		inst.SetAttr(val.InternString("filename"), val.NewStringVal(filename))
	} else {
		inst.SetAttr(val.InternString("filename"), val.NoneVal())
	}
	return exc
}

// raiseOSError raises the Python exception matching a Go error via ctx.
func raiseOSError(ctx *val.ExecContext, err error, filename string) val.Value {
	if en := errnoOf(err); en != 0 {
		ctx.Raise(newOSError(en, filename, filename != ""))
		return val.NoneVal()
	}
	ctx.Raise(val.NewException(val.OSErrorType, err.Error()))
	return val.NoneVal()
}
