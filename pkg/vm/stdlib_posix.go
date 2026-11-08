package vm

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

// Posix primitives shared by the native os, errno, gettext and shutil
// modules. They give the unmodified upstream posixpath, genericpath, stat,
// fnmatch, gettext callers and argparse the surface they expect.

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
	panic("TypeError: expected str, bytes or os.PathLike object, not " + typeNameOf(v))
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
		panic("TypeError: expected " + typeNameOf(v) + ".__fspath__() to return str or bytes, not " + typeNameOf(r))
	}
	return r, true
}

// errnoOf extracts a syscall.Errno from err, or 0.
func errnoOf(err error) syscall.Errno {
	var en syscall.Errno
	if errors.As(err, &en) {
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

// raiseOSError panics with the Python exception matching a Go error.
func raiseOSError(err error, filename string) {
	if en := errnoOf(err); en != 0 {
		panic(newOSError(en, filename, filename != ""))
	}
	panic("OSError: " + err.Error())
}

// ---- stat_result ------------------------------------------------------

var (
	statResultType *val.PyType
	statResultOnce sync.Once
	statFieldNames = [...]string{
		"st_mode", "st_ino", "st_dev", "st_nlink", "st_uid",
		"st_gid", "st_size", "st_atime", "st_mtime", "st_ctime",
	}
)

type statInfo struct {
	Mode, Ino, Dev, Nlink, Uid, Gid, Size int64
	AtimeNs, MtimeNs, CtimeNs             int64
}

var (
	dirEntryType    *val.PyType
	dirEntryOnce    sync.Once
	scandirIterType *val.PyType
	scandirOnce     sync.Once
	pathLikeType    *val.PyType
	pathLikeOnce    sync.Once
)

func initPathLikeType() {
	pathLikeOnce.Do(func() {
		t := val.MustNewType("PathLike", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("os"))
		t.Dict.Set(val.InternString("register"), val.NewBuiltinFuncVal("register", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 1 {
				return args[1]
			}
			return val.NoneVal()
		}))
		t.Dict.Set(val.InternString("__subclasshook__"), val.NewBuiltinFuncVal("__subclasshook__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				return val.NotImplementedVal()
			}
			subclass := args[1]
			if subclass.IsType() {
				tp := subclass.AsType()
				if _, _, ok := tp.LookupMRO(val.InternString("__fspath__")); ok {
					return val.BoolVal(true)
				}
			}
			return val.NotImplementedVal()
		}))
		t.InvalidateVersion()
		pathLikeType = t
	})
}

func initDirEntryType() {
	dirEntryOnce.Do(func() {
		t := val.MustNewType("DirEntry", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("os"))
		t.Dict.Set(val.InternString("is_dir"), val.NewBuiltinFuncVal("is_dir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			follow := true
			if v, ok := kwArg(args, kwnames, "follow_symlinks"); ok {
				follow = v.IsTruthy()
			} else if len(args) > 1 {
				follow = args[1].IsTruthy()
			}
			isSymlinkVal, _ := inst.GetAttr(val.InternString("_is_symlink"))
			isSymlink := isSymlinkVal.IsTruthy()
			isDirVal, _ := inst.GetAttr(val.InternString("_is_dir"))
			if !isSymlink || !follow {
				return isDirVal
			}
			pVal, _ := inst.GetAttr(val.InternString("path"))
			info, err := os.Stat(pVal.AsString())
			if err != nil {
				return val.BoolVal(false)
			}
			return val.BoolVal(info.IsDir())
		}))
		t.Dict.Set(val.InternString("is_file"), val.NewBuiltinFuncVal("is_file", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			follow := true
			if v, ok := kwArg(args, kwnames, "follow_symlinks"); ok {
				follow = v.IsTruthy()
			} else if len(args) > 1 {
				follow = args[1].IsTruthy()
			}
			isSymlinkVal, _ := inst.GetAttr(val.InternString("_is_symlink"))
			isSymlink := isSymlinkVal.IsTruthy()
			isDirVal, _ := inst.GetAttr(val.InternString("_is_dir"))
			if !isSymlink || !follow {
				return val.BoolVal(!isDirVal.IsTruthy() && !isSymlink)
			}
			pVal, _ := inst.GetAttr(val.InternString("path"))
			info, err := os.Stat(pVal.AsString())
			if err != nil {
				return val.BoolVal(false)
			}
			return val.BoolVal(!info.IsDir())
		}))
		t.Dict.Set(val.InternString("is_symlink"), val.NewBuiltinFuncVal("is_symlink", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			v, _ := inst.GetAttr(val.InternString("_is_symlink"))
			return v
		}))
		t.Dict.Set(val.InternString("stat"), val.NewBuiltinFuncVal("stat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			follow := true
			if v, ok := kwArg(args, kwnames, "follow_symlinks"); ok {
				follow = v.IsTruthy()
			} else if len(args) > 1 {
				follow = args[1].IsTruthy()
			}
			pVal, _ := inst.GetAttr(val.InternString("path"))
			p := pVal.AsString()
			var (
				info fs.FileInfo
				err  error
			)
			if follow {
				info, err = os.Stat(p)
			} else {
				info, err = os.Lstat(p)
			}
			if err != nil {
				raiseOSError(err, p)
			}
			return fileInfoToStat(info)
		}))
		t.Dict.Set(val.InternString("inode"), val.NewBuiltinFuncVal("inode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			pVal, _ := inst.GetAttr(val.InternString("path"))
			info, err := os.Lstat(pVal.AsString())
			if err != nil {
				raiseOSError(err, pVal.AsString())
			}
			st := fileInfoToStat(info).AsInstance()
			ino, _ := st.GetAttr(val.InternString("st_ino"))
			return ino
		}))
		t.Dict.Set(val.InternString("__fspath__"), val.NewBuiltinFuncVal("__fspath__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			pVal, _ := inst.GetAttr(val.InternString("path"))
			return pVal
		}))
		t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			nVal, _ := inst.GetAttr(val.InternString("name"))
			return val.NewStringVal(fmt.Sprintf("<DirEntry %q>", nVal.AsString()))
		}))
		t.InvalidateVersion()
		dirEntryType = t
	})
}

func newDirEntryVal(dir string, ent os.DirEntry) val.Value {
	initDirEntryType()
	v := val.NewInstanceOfType(dirEntryType)
	inst := v.AsInstance()
	name := ent.Name()
	fullPath := filepath.Join(dir, name)
	isDir := ent.IsDir()
	isSymlink := ent.Type()&fs.ModeSymlink != 0
	inst.SetAttr(val.InternString("name"), val.NewStringVal(name))
	inst.SetAttr(val.InternString("path"), val.NewStringVal(fullPath))
	inst.SetAttr(val.InternString("_is_dir"), val.BoolVal(isDir))
	inst.SetAttr(val.InternString("_is_symlink"), val.BoolVal(isSymlink))
	return v
}

func initScandirIterType() {
	scandirOnce.Do(func() {
		t := val.MustNewType("ScandirIterator", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("os"))
		t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		}))
		t.Dict.Set(val.InternString("__next__"), val.NewBuiltinFuncVal("__next__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			closedVal, _ := inst.GetAttr(val.InternString("_closed"))
			if closedVal.IsTruthy() {
				if ctx != nil {
					ctx.Raise(val.NewStopIteration(val.NoneVal()))
					return val.NoneVal()
				}
				panic(val.NewStopIteration(val.NoneVal()))
			}
			idxVal, _ := inst.GetAttr(val.InternString("_idx"))
			entriesVal, _ := inst.GetAttr(val.InternString("_entries"))
			entries := entriesVal.AsList().Items
			idx := int(idxVal.AsInt64())
			if idx >= len(entries) {
				if ctx != nil {
					ctx.Raise(val.NewStopIteration(val.NoneVal()))
					return val.NoneVal()
				}
				panic(val.NewStopIteration(val.NoneVal()))
			}
			inst.SetAttr(val.InternString("_idx"), val.Int64Val(int64(idx+1)))
			return entries[idx]
		}))
		t.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			inst.SetAttr(val.InternString("_closed"), val.BoolVal(true))
			return val.NoneVal()
		}))
		t.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return args[0]
		}))
		t.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			inst := args[0].AsInstance()
			inst.SetAttr(val.InternString("_closed"), val.BoolVal(true))
			return val.NoneVal()
		}))
		t.InvalidateVersion()
		scandirIterType = t
	})
}

func newScandirIterVal(dir string, entries []os.DirEntry) val.Value {
	initScandirIterType()
	v := val.NewInstanceOfType(scandirIterType)
	inst := v.AsInstance()
	items := make([]val.Value, len(entries))
	for i, ent := range entries {
		items[i] = newDirEntryVal(dir, ent)
	}
	inst.SetAttr(val.InternString("_entries"), val.NewListVal(items...))
	inst.SetAttr(val.InternString("_idx"), val.Int64Val(0))
	inst.SetAttr(val.InternString("_closed"), val.BoolVal(false))
	return v
}

func initStatResultType() {
	statResultOnce.Do(func() {
		t := val.MustNewType("stat_result", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("os"))
		seq := func(self val.Value) []val.Value {
			inst := self.AsInstance()
			out := make([]val.Value, len(statFieldNames))
			for i, n := range statFieldNames {
				v, _ := inst.GetAttr(val.InternString(n))
				if i >= 7 {
					// The positional tuple holds integer seconds.
					if v.IsFloat64() {
						v = val.Int64Val(int64(v.AsFloat64()))
					}
				}
				out[i] = v
			}
			return out
		}
		t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			items := seq(args[0])
			i := int(args[1].AsInt64())
			if i < 0 {
				i += len(items)
			}
			if i < 0 || i >= len(items) {
				panic("IndexError: tuple index out of range")
			}
			return items[i]
		}))
		t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(int64(len(statFieldNames)))
		}))
		t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewIterVal(val.NewTupleVal(seq(args[0])...))
		}))
		t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			var b strings.Builder
			b.WriteString("os.stat_result(")
			for i, v := range seq(args[0]) {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(statFieldNames[i] + "=" + val.PyRepr(ctx, v))
			}
			b.WriteString(")")
			return val.NewStringVal(b.String())
		}))
		t.InvalidateVersion()
		statResultType = t
	})
}

func statResultVal(si statInfo) val.Value {
	initStatResultType()
	v := val.NewInstanceOfType(statResultType)
	inst := v.AsInstance()
	set := func(name string, x val.Value) { inst.SetAttr(val.InternString(name), x) }
	set("st_mode", val.Int64Val(si.Mode))
	set("st_ino", val.Int64Val(si.Ino))
	set("st_dev", val.Int64Val(si.Dev))
	set("st_nlink", val.Int64Val(si.Nlink))
	set("st_uid", val.Int64Val(si.Uid))
	set("st_gid", val.Int64Val(si.Gid))
	set("st_size", val.Int64Val(si.Size))
	set("st_atime", val.Float64Val(float64(si.AtimeNs)/1e9))
	set("st_mtime", val.Float64Val(float64(si.MtimeNs)/1e9))
	set("st_ctime", val.Float64Val(float64(si.CtimeNs)/1e9))
	set("st_atime_ns", val.Int64Val(si.AtimeNs))
	set("st_mtime_ns", val.Int64Val(si.MtimeNs))
	set("st_ctime_ns", val.Int64Val(si.CtimeNs))
	return v
}

type unameInfo struct {
	Sysname  string
	Nodename string
	Release  string
	Version  string
	Machine  string
}

var (
	unameResultType *val.PyType
	unameResultOnce sync.Once
	unameFieldNames = []string{"sysname", "nodename", "release", "version", "machine"}
)

func initUnameResultType() {
	unameResultOnce.Do(func() {
		t := val.MustNewType("uname_result", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("posix"))
		seq := func(self val.Value) []val.Value {
			inst := self.AsInstance()
			out := make([]val.Value, len(unameFieldNames))
			for i, n := range unameFieldNames {
				v, _ := inst.GetAttr(val.InternString(n))
				out[i] = v
			}
			return out
		}
		t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			items := seq(args[0])
			i := int(args[1].AsInt64())
			if i < 0 {
				i += len(items)
			}
			if i < 0 || i >= len(items) {
				panic("IndexError: tuple index out of range")
			}
			return items[i]
		}))
		t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(int64(len(unameFieldNames)))
		}))
		t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewIterVal(val.NewTupleVal(seq(args[0])...))
		}))
		t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			var b strings.Builder
			b.WriteString("posix.uname_result(")
			for i, v := range seq(args[0]) {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(unameFieldNames[i] + "=" + val.PyRepr(ctx, v))
			}
			b.WriteString(")")
			return val.NewStringVal(b.String())
		}))
		t.InvalidateVersion()
		unameResultType = t
	})
}

func unameResultVal(ui unameInfo) val.Value {
	initUnameResultType()
	v := val.NewInstanceOfType(unameResultType)
	inst := v.AsInstance()
	inst.SetAttr(val.InternString("sysname"), val.NewStringVal(ui.Sysname))
	inst.SetAttr(val.InternString("nodename"), val.NewStringVal(ui.Nodename))
	inst.SetAttr(val.InternString("release"), val.NewStringVal(ui.Release))
	inst.SetAttr(val.InternString("version"), val.NewStringVal(ui.Version))
	inst.SetAttr(val.InternString("machine"), val.NewStringVal(ui.Machine))
	return v
}

// portableMode converts a Go FileMode into POSIX st_mode bits.
func portableMode(m fs.FileMode) int64 {
	mode := int64(m.Perm())
	if m&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	switch {
	case m.IsDir():
		mode |= 0o040000
	case m&fs.ModeSymlink != 0:
		mode |= 0o120000
	case m&fs.ModeNamedPipe != 0:
		mode |= 0o010000
	case m&fs.ModeSocket != 0:
		mode |= 0o140000
	case m&fs.ModeCharDevice != 0:
		mode |= 0o020000
	case m&fs.ModeDevice != 0:
		mode |= 0o060000
	default:
		mode |= 0o100000
	}
	return mode
}

func fileInfoToStat(info fs.FileInfo) val.Value {
	si := statInfo{
		Mode:    portableMode(info.Mode()),
		Nlink:   1,
		Size:    info.Size(),
		MtimeNs: info.ModTime().UnixNano(),
	}
	si.AtimeNs, si.CtimeNs = si.MtimeNs, si.MtimeNs
	sysStatInfo(info, &si)
	return statResultVal(si)
}

func lookupFD(fd int) (*os.File, bool) {
	switch fd {
	case 0:
		return os.Stdin, true
	case 1:
		return os.Stdout, true
	case 2:
		return os.Stderr, true
	}
	fdMutex.Lock()
	defer fdMutex.Unlock()
	f, ok := fdMap[fd]
	return f, ok
}

// installPosix adds the posix surface to the os module dictionary.
func installPosix(osMod *val.PyModule) {
	set := func(name string, fn val.VectorcallFunc) {
		osMod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, fn))
	}
	str := func(name, v string) { osMod.Dict.Set(val.InternString(name), val.NewStringVal(v)) }

	str("curdir", ".")
	str("pardir", "..")
	str("extsep", ".")
	str("defpath", ":/bin:/usr/bin")
	str("devnull", "/dev/null")
	osMod.Dict.Set(val.InternString("altsep"), val.NoneVal())
	osMod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))

	initStatResultType()
	osMod.Dict.Set(val.InternString("stat_result"), val.TypeVal(statResultType))
	initDirEntryType()
	osMod.Dict.Set(val.InternString("DirEntry"), val.TypeVal(dirEntryType))
	initPathLikeType()
	osMod.Dict.Set(val.InternString("PathLike"), val.TypeVal(pathLikeType))
	terminalSizeVal(80, 24)
	osMod.Dict.Set(val.InternString("terminal_size"), val.TypeVal(terminalSizeType))
	initUnameResultType()
	osMod.Dict.Set(val.InternString("uname_result"), val.TypeVal(unameResultType))
	set("uname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return unameResultVal(getUnameInfo())
	})
	set("umask", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: umask() takes exactly one argument (0 given)")
		}
		mask := int(args[0].AsInt64())
		oldMask := syscall.Umask(mask)
		return val.Int64Val(int64(oldMask))
	})

	osMod.Dict.Set(val.InternString("supports_dir_fd"), val.NewSetVal())
	osMod.Dict.Set(val.InternString("supports_fd"), val.NewSetVal())
	osMod.Dict.Set(val.InternString("supports_follow_symlinks"), val.NewSetVal())
	osMod.Dict.Set(val.InternString("_walk_symlinks_as_files"), val.NewInstanceOfType(val.ObjectType))

	osMod.Dict.Set(val.InternString("O_RDONLY"), val.Int64Val(int64(syscall.O_RDONLY)))
	osMod.Dict.Set(val.InternString("O_WRONLY"), val.Int64Val(int64(syscall.O_WRONLY)))
	osMod.Dict.Set(val.InternString("O_RDWR"), val.Int64Val(int64(syscall.O_RDWR)))
	osMod.Dict.Set(val.InternString("O_APPEND"), val.Int64Val(int64(syscall.O_APPEND)))
	osMod.Dict.Set(val.InternString("O_CREAT"), val.Int64Val(int64(syscall.O_CREAT)))
	osMod.Dict.Set(val.InternString("O_EXCL"), val.Int64Val(int64(syscall.O_EXCL)))
	osMod.Dict.Set(val.InternString("O_TRUNC"), val.Int64Val(int64(syscall.O_TRUNC)))
	osMod.Dict.Set(val.InternString("O_BINARY"), val.Int64Val(0))
	osMod.Dict.Set(val.InternString("O_TEXT"), val.Int64Val(0))
	osMod.Dict.Set(val.InternString("F_OK"), val.Int64Val(0))
	osMod.Dict.Set(val.InternString("R_OK"), val.Int64Val(4))
	osMod.Dict.Set(val.InternString("W_OK"), val.Int64Val(2))
	osMod.Dict.Set(val.InternString("X_OK"), val.Int64Val(1))
	osMod.Dict.Set(val.InternString("SEEK_SET"), val.Int64Val(0))
	osMod.Dict.Set(val.InternString("SEEK_CUR"), val.Int64Val(1))
	osMod.Dict.Set(val.InternString("SEEK_END"), val.Int64Val(2))

	set("fspath", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		var p val.Value
		switch {
		case len(pa) == 1:
			p = pa[0]
		case len(pa) == 0:
			if v, ok := kwArg(args, kwnames, "path"); ok {
				p = v
			} else {
				panic("TypeError: fspath() missing required argument 'path' (pos 1)")
			}
		default:
			panic("TypeError: fspath() takes exactly one argument")
		}
		if p.IsAnyString() || p.IsBytes() {
			return p
		}
		if r, ok := fspathOf(ctx, p); ok {
			return r
		}
		panic("TypeError: expected str, bytes or os.PathLike object, not " + typeNameOf(p))
	})
	set("fsencode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: fsencode() takes 1 argument")
		}
		if args[0].IsBytes() {
			return args[0]
		}
		return val.NewBytesVal([]byte(pathArg(ctx, args[0])))
	})
	set("fsdecode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: fsdecode() takes 1 argument")
		}
		if args[0].IsAnyString() {
			return args[0]
		}
		if args[0].IsBytes() {
			return val.NewStringVal(string(args[0].AsBytes().Data))
		}
		return val.NewStringVal(pathArg(ctx, args[0]))
	})

	set("get_exec_path", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pathStr := os.Getenv("PATH")
		if len(args) > 0 && !args[0].IsNone() && args[0].IsDict() {
			if v, ok := args[0].AsDict().Get(val.InternString("PATH")); ok && v.IsAnyString() {
				pathStr = v.AsString()
			}
		}
		if pathStr == "" {
			pathStr = ":/bin:/usr/bin"
		}
		parts := strings.Split(pathStr, string(filepath.ListSeparator))
		var items []val.Value
		for _, p := range parts {
			items = append(items, val.NewStringVal(p))
		}
		return val.NewListVal(items...)
	})

	set("_get_exports_list", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: _get_exports_list() takes 1 argument")
		}
		var names []val.Value
		if args[0].IsModule() {
			dict := args[0].AsModule().Dict
			for _, entry := range dict.Entries {
				if entry.Active && entry.Key.IsAnyString() {
					k := entry.Key.AsString()
					if !strings.HasPrefix(k, "_") {
						names = append(names, entry.Key)
					}
				}
			}
		}
		return val.NewListVal(names...)
	})

	set("pipe", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		r, w, err := os.Pipe()
		if err != nil {
			raiseOSError(err, "")
		}
		rFd := int(r.Fd())
		wFd := int(w.Fd())
		fdMutex.Lock()
		fdMap[rFd] = r
		fdMap[wFd] = w
		fdMutex.Unlock()
		return val.NewTupleVal(val.Int64Val(int64(rFd)), val.Int64Val(int64(wFd)))
	})

	set("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: close() takes 1 argument")
		}
		fd := int(args[0].AsInt64())
		fdMutex.Lock()
		f, ok := fdMap[fd]
		delete(fdMap, fd)
		fdMutex.Unlock()
		if ok {
			_ = f.Close()
		} else {
			_ = syscall.Close(fd)
		}
		return val.NoneVal()
	})

	set("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: read() takes 2 arguments")
		}
		fd := int(args[0].AsInt64())
		n := int(args[1].AsInt64())
		buf := make([]byte, n)
		fdMutex.Lock()
		f, ok := fdMap[fd]
		fdMutex.Unlock()
		var readN int
		var err error
		if ok {
			readN, err = f.Read(buf)
		} else {
			readN, err = syscall.Read(fd, buf)
		}
		if err != nil && readN == 0 {
			return val.NewBytesVal(nil)
		}
		return val.NewBytesVal(buf[:readN])
	})

	set("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: write() takes 2 arguments")
		}
		fd := int(args[0].AsInt64())
		var data []byte
		if args[1].IsBytes() {
			data = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			data = args[1].AsByteArray().Data
		} else if args[1].IsMemoryView() {
			data = args[1].AsMemoryView().Buf
		} else if args[1].IsAnyString() {
			data = []byte(args[1].AsString())
		} else {
			panic("TypeError: a bytes-like object is required")
		}
		fdMutex.Lock()
		f, ok := fdMap[fd]
		fdMutex.Unlock()
		var writtenN int
		var err error
		if ok {
			writtenN, err = f.Write(data)
		} else {
			writtenN, err = syscall.Write(fd, data)
		}
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(writtenN))
	})

	set("dup", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dup() takes 1 argument")
		}
		oldfd := int(args[0].AsInt64())
		newfd, err := syscall.Dup(oldfd)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(newfd))
	})

	set("dup2", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: dup2() takes at least 2 arguments")
		}
		oldfd := int(args[0].AsInt64())
		newfd := int(args[1].AsInt64())
		if err := syscall.Dup2(oldfd, newfd); err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(newfd))
	})

	set("waitpid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: waitpid() takes 2 arguments")
		}
		pid := int(args[0].AsInt64())
		options := int(args[1].AsInt64())
		var wstatus syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &wstatus, options, nil)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NewTupleVal(val.Int64Val(int64(wpid)), val.Int64Val(int64(wstatus)))
	})

	set("waitstatus_to_exitcode", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: waitstatus_to_exitcode() takes exactly 1 argument")
		}
		st := int(args[0].AsInt64())
		ws := syscall.WaitStatus(st)
		if ws.Exited() {
			return val.Int64Val(int64(ws.ExitStatus()))
		}
		if ws.Signaled() {
			return val.Int64Val(-int64(ws.Signal()))
		}
		return val.Int64Val(int64(st))
	})

	set("WIFEXITED", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WIFEXITED() takes 1 argument")
		}
		return val.BoolVal(syscall.WaitStatus(int(args[0].AsInt64())).Exited())
	})
	set("WEXITSTATUS", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WEXITSTATUS() takes 1 argument")
		}
		return val.Int64Val(int64(syscall.WaitStatus(int(args[0].AsInt64())).ExitStatus()))
	})
	set("WIFSIGNALED", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WIFSIGNALED() takes 1 argument")
		}
		return val.BoolVal(syscall.WaitStatus(int(args[0].AsInt64())).Signaled())
	})
	set("WTERMSIG", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WTERMSIG() takes 1 argument")
		}
		return val.Int64Val(int64(syscall.WaitStatus(int(args[0].AsInt64())).Signal()))
	})
	set("WIFSTOPPED", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WIFSTOPPED() takes 1 argument")
		}
		return val.BoolVal(syscall.WaitStatus(int(args[0].AsInt64())).Stopped())
	})
	set("WSTOPSIG", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: WSTOPSIG() takes 1 argument")
		}
		return val.Int64Val(int64(syscall.WaitStatus(int(args[0].AsInt64())).StopSignal()))
	})
	osMod.Dict.Set(val.InternString("WNOHANG"), val.Int64Val(int64(syscall.WNOHANG)))

	set("strerror", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsInt64() {
			panic("TypeError: strerror() argument must be int")
		}
		return val.NewStringVal(strerrorOf(int(args[0].AsInt64())))
	})
	set("getcwdb", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wd, err := os.Getwd()
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NewBytesVal([]byte(wd))
	})
	set("kill", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 2 {
			panic("TypeError: kill expected 2 arguments, got " + itoa(len(args)))
		}
		pid, ok1 := intOf(ctx, args[0])
		sig, ok2 := intOf(ctx, args[1])
		if !ok1 || !ok2 {
			panic("TypeError: kill() arguments must be integers")
		}
		if err := killProcess(int(pid), syscall.Signal(sig)); err != nil {
			raiseOSError(err, "")
		}
		return val.NoneVal()
	})
	set("getppid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(os.Getppid()))
	})
	set("getuid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(os.Getuid()))
	})
	set("getgid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(os.Getgid()))
	})
	set("stat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: stat() missing required argument 'path' (pos 1)")
		}
		if pa[0].IsInt64() {
			return fstatFD(int(pa[0].AsInt64()))
		}
		p := pathArg(ctx, pa[0])
		follow := true
		if v, ok := kwArg(args, kwnames, "follow_symlinks"); ok {
			follow = v.IsTruthy()
		}
		var (
			info fs.FileInfo
			err  error
		)
		if follow {
			info, err = os.Stat(p)
		} else {
			info, err = os.Lstat(p)
		}
		if err != nil {
			raiseOSError(err, p)
		}
		return fileInfoToStat(info)
	})
	set("lstat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: lstat() missing required argument 'path' (pos 1)")
		}
		p := pathArg(ctx, pa[0])
		info, err := os.Lstat(p)
		if err != nil {
			raiseOSError(err, p)
		}
		return fileInfoToStat(info)
	})
	set("fstat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsInt64() {
			panic("TypeError: fstat() argument must be int")
		}
		return fstatFD(int(args[0].AsInt64()))
	})
	set("readlink", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: readlink() missing required argument 'path' (pos 1)")
		}
		p := pathArg(ctx, pa[0])
		dst, err := os.Readlink(p)
		if err != nil {
			raiseOSError(err, p)
		}
		if pa[0].IsBytes() {
			return val.NewBytesVal([]byte(dst))
		}
		return val.NewStringVal(dst)
	})

	set("walk", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		top := "."
		pa := posArgs(args, kwnames)
		if len(pa) >= 1 && !pa[0].IsNone() {
			top = pathArg(ctx, pa[0])
		} else if v, ok := kwArg(args, kwnames, "top"); ok {
			top = pathArg(ctx, v)
		}
		topdown := true
		if len(pa) >= 2 {
			topdown = pa[1].IsTruthy()
		} else if v, ok := kwArg(args, kwnames, "topdown"); ok {
			topdown = v.IsTruthy()
		}
		followlinks := false
		if v, ok := kwArg(args, kwnames, "followlinks"); ok {
			followlinks = v.IsTruthy()
		}
		var walkTuples []val.Value
		var walkDir func(current string)
		walkDir = func(current string) {
			entries, err := os.ReadDir(current)
			if err != nil {
				return
			}
			var dirnames []val.Value
			var filenames []val.Value
			for _, ent := range entries {
				name := ent.Name()
				isDir := ent.IsDir()
				if !isDir && followlinks && ent.Type()&fs.ModeSymlink != 0 {
					if info, err := os.Stat(filepath.Join(current, name)); err == nil {
						isDir = info.IsDir()
					}
				}
				if isDir {
					dirnames = append(dirnames, val.NewStringVal(name))
				} else {
					filenames = append(filenames, val.NewStringVal(name))
				}
			}
			tuple := val.NewTupleVal(val.NewStringVal(current), val.NewListVal(dirnames...), val.NewListVal(filenames...))
			if topdown {
				walkTuples = append(walkTuples, tuple)
				for _, d := range dirnames {
					walkDir(filepath.Join(current, d.AsString()))
				}
			} else {
				for _, d := range dirnames {
					walkDir(filepath.Join(current, d.AsString()))
				}
				walkTuples = append(walkTuples, tuple)
			}
		}
		walkDir(top)
		return val.NewIterVal(val.NewTupleVal(walkTuples...))
	})

	set("scandir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dir := "."
		pa := posArgs(args, kwnames)
		if len(pa) >= 1 && !pa[0].IsNone() {
			dir = pathArg(ctx, pa[0])
		} else if v, ok := kwArg(args, kwnames, "path"); ok && !v.IsNone() {
			dir = pathArg(ctx, v)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			raiseOSError(err, dir)
		}
		return newScandirIterVal(dir, entries)
	})

	set("replace", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: replace() takes 2 arguments")
		}
		src := pathArg(ctx, args[0])
		dst := pathArg(ctx, args[1])
		if err := os.Rename(src, dst); err != nil {
			raiseOSError(err, src)
		}
		return val.NoneVal()
	})

	set("symlink", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			panic("TypeError: symlink() takes at least 2 arguments")
		}
		src := pathArg(ctx, pa[0])
		dst := pathArg(ctx, pa[1])
		if err := os.Symlink(src, dst); err != nil {
			raiseOSError(err, src)
		}
		return val.NoneVal()
	})

	set("chmod", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			panic("TypeError: chmod() takes 2 arguments")
		}
		p := pathArg(ctx, pa[0])
		mode, ok := intOf(ctx, pa[1])
		if !ok {
			panic("TypeError: chmod() mode must be int")
		}
		if err := os.Chmod(p, os.FileMode(mode)); err != nil {
			raiseOSError(err, p)
		}
		return val.NoneVal()
	})

	set("utime", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: utime() takes at least 1 argument")
		}
		p := pathArg(ctx, pa[0])
		now := time.Now()
		atime := now
		mtime := now
		if len(pa) >= 2 && !pa[1].IsNone() {
			if pa[1].IsTuple() && len(pa[1].AsTuple().Items) == 2 {
				items := pa[1].AsTuple().Items
				var aSec, mSec float64
				if items[0].IsInt64() {
					aSec = float64(items[0].AsInt64())
				} else if items[0].IsFloat64() {
					aSec = items[0].AsFloat64()
				}
				if items[1].IsInt64() {
					mSec = float64(items[1].AsInt64())
				} else if items[1].IsFloat64() {
					mSec = items[1].AsFloat64()
				}
				atime = time.Unix(int64(aSec), int64((aSec-float64(int64(aSec)))*1e9))
				mtime = time.Unix(int64(mSec), int64((mSec-float64(int64(mSec)))*1e9))
			}
		} else if nsVal, ok := kwArg(args, kwnames, "ns"); ok && nsVal.IsTuple() && len(nsVal.AsTuple().Items) == 2 {
			items := nsVal.AsTuple().Items
			if items[0].IsInt64() && items[1].IsInt64() {
				atime = time.Unix(0, items[0].AsInt64())
				mtime = time.Unix(0, items[1].AsInt64())
			}
		}
		if err := os.Chtimes(p, atime, mtime); err != nil {
			raiseOSError(err, p)
		}
		return val.NoneVal()
	})

	set("urandom", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: urandom() takes 1 argument")
		}
		n, ok := intOf(ctx, args[0])
		if !ok || n < 0 {
			panic("ValueError: negative size")
		}
		buf := make([]byte, n)
		if _, err := crand.Read(buf); err != nil {
			panic("OSError: urandom failed: " + err.Error())
		}
		return val.NewBytesVal(buf)
	})

	set("get_terminal_size", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fc, fl := 80, 24
		fb := val.NoneVal()
		if pa := posArgs(args, kwnames); len(pa) >= 1 {
			fb = pa[0]
		} else if v, ok := kwArg(args, kwnames, "fallback"); ok {
			fb = v
		}
		if fb.IsTuple() && len(fb.AsTuple().Items) == 2 {
			it := fb.AsTuple().Items
			if it[0].IsInt64() && it[1].IsInt64() {
				fc, fl = int(it[0].AsInt64()), int(it[1].AsInt64())
			}
		}
		c, l := terminalSize(fc, fl)
		return terminalSizeVal(c, l)
	})
}

func fstatFD(fd int) val.Value {
	f, ok := lookupFD(fd)
	if !ok {
		panic(newOSError(syscall.EBADF, "", false))
	}
	info, err := f.Stat()
	if err != nil {
		raiseOSError(err, "")
	}
	return fileInfoToStat(info)
}

// ---- errno ------------------------------------------------------------

var errnoTable = []struct {
	name string
	num  syscall.Errno
}{
	{"EPERM", syscall.EPERM}, {"ENOENT", syscall.ENOENT}, {"ESRCH", syscall.ESRCH},
	{"EINTR", syscall.EINTR}, {"EIO", syscall.EIO}, {"ENXIO", syscall.ENXIO},
	{"E2BIG", syscall.E2BIG}, {"ENOEXEC", syscall.ENOEXEC}, {"EBADF", syscall.EBADF},
	{"ECHILD", syscall.ECHILD}, {"EAGAIN", syscall.EAGAIN}, {"ENOMEM", syscall.ENOMEM},
	{"EACCES", syscall.EACCES}, {"EFAULT", syscall.EFAULT}, {"EBUSY", syscall.EBUSY},
	{"EEXIST", syscall.EEXIST}, {"EXDEV", syscall.EXDEV}, {"ENODEV", syscall.ENODEV},
	{"ENOTDIR", syscall.ENOTDIR}, {"EISDIR", syscall.EISDIR}, {"EINVAL", syscall.EINVAL},
	{"ENFILE", syscall.ENFILE}, {"EMFILE", syscall.EMFILE}, {"ENOTTY", syscall.ENOTTY},
	{"EFBIG", syscall.EFBIG}, {"ENOSPC", syscall.ENOSPC}, {"ESPIPE", syscall.ESPIPE},
	{"EROFS", syscall.EROFS}, {"EMLINK", syscall.EMLINK}, {"EPIPE", syscall.EPIPE},
	{"EDOM", syscall.EDOM}, {"ERANGE", syscall.ERANGE}, {"EDEADLK", syscall.EDEADLK},
	{"ENAMETOOLONG", syscall.ENAMETOOLONG}, {"ENOLCK", syscall.ENOLCK},
	{"ENOSYS", syscall.ENOSYS}, {"ENOTEMPTY", syscall.ENOTEMPTY},
	{"ELOOP", syscall.ELOOP}, {"EWOULDBLOCK", syscall.EWOULDBLOCK},
	{"ENOTSOCK", syscall.ENOTSOCK}, {"EADDRINUSE", syscall.EADDRINUSE},
	{"ECONNABORTED", syscall.ECONNABORTED}, {"ECONNREFUSED", syscall.ECONNREFUSED},
	{"ECONNRESET", syscall.ECONNRESET}, {"ETIMEDOUT", syscall.ETIMEDOUT},
	{"ENOTSUP", syscall.ENOTSUP}, {"EOPNOTSUPP", syscall.EOPNOTSUPP},
	{"EINPROGRESS", syscall.EINPROGRESS}, {"EALREADY", syscall.EALREADY},
	{"ENOTCONN", syscall.ENOTCONN}, {"EISCONN", syscall.EISCONN},
	{"EDESTADDRREQ", syscall.EDESTADDRREQ}, {"EMSGSIZE", syscall.EMSGSIZE},
	{"EPROTOTYPE", syscall.EPROTOTYPE}, {"ENOPROTOOPT", syscall.ENOPROTOOPT},
	{"EPROTONOSUPPORT", syscall.EPROTONOSUPPORT}, {"ESOCKTNOSUPPORT", syscall.ESOCKTNOSUPPORT},
	{"EPFNOSUPPORT", syscall.EPFNOSUPPORT}, {"EAFNOSUPPORT", syscall.EAFNOSUPPORT},
	{"EADDRNOTAVAIL", syscall.EADDRNOTAVAIL}, {"ENETDOWN", syscall.ENETDOWN},
	{"ENETUNREACH", syscall.ENETUNREACH}, {"ENETRESET", syscall.ENETRESET},
	{"ENOBUFS", syscall.ENOBUFS}, {"EHOSTDOWN", syscall.EHOSTDOWN},
	{"EHOSTUNREACH", syscall.EHOSTUNREACH},
}

func (e *Engine) createErrnoModule() val.Value {
	modVal := val.NewModuleVal("errno", "")
	mod := modVal.AsModule()
	codes := val.NewDictVal(len(errnoTable))
	cd := codes.AsDict()
	for _, ent := range errnoTable {
		n := val.Int64Val(int64(ent.num))
		mod.Dict.Set(val.InternString(ent.name), n)
		if _, seen := cd.Get(n); !seen {
			cd.Set(n, val.NewStringVal(ent.name))
		}
	}
	mod.Dict.Set(val.InternString("errorcode"), codes)
	return modVal
}

// ---- gettext ----------------------------------------------------------

// createGettextModule is a pass-through catalog: no translations are
// loaded, so every lookup returns the message id. That is what the C
// locale yields and what argparse needs.
func (e *Engine) createGettextModule() val.Value {
	modVal := val.NewModuleVal("gettext", "")
	mod := modVal.AsModule()
	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, fn))
	}
	need := func(name string, args []val.Value, n int) {
		if len(args) < n {
			panic("TypeError: " + name + "() missing required positional arguments")
		}
	}
	plural := func(one, many, n val.Value) val.Value {
		if n.IsInt64() && n.AsInt64() == 1 {
			return one
		}
		return many
	}
	set("gettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("gettext", args, 1)
		return args[0]
	})
	set("dgettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("dgettext", args, 2)
		return args[1]
	})
	set("pgettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("pgettext", args, 2)
		return args[1]
	})
	set("dpgettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("dpgettext", args, 3)
		return args[2]
	})
	set("ngettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("ngettext", args, 3)
		return plural(args[0], args[1], args[2])
	})
	set("dngettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("dngettext", args, 4)
		return plural(args[1], args[2], args[3])
	})
	set("npgettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("npgettext", args, 4)
		return plural(args[1], args[2], args[3])
	})
	set("dnpgettext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		need("dnpgettext", args, 5)
		return plural(args[2], args[3], args[4])
	})
	domain := val.NewStringVal("messages")
	set("textdomain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 1 && !args[0].IsNone() {
			domain = args[0]
		}
		return domain
	})
	set("bindtextdomain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) >= 2 {
			return args[1]
		}
		return val.NoneVal()
	})
	return modVal
}

// ---- shutil -----------------------------------------------------------

var (
	terminalSizeType *val.PyType
	terminalSizeOnce sync.Once
)

func terminalSizeVal(cols, lines int) val.Value {
	terminalSizeOnce.Do(func() {
		t := val.MustNewType("terminal_size", []*val.PyType{val.ObjectType}, nil)
		t.Dict.Set(val.InternString("__module__"), val.InternString("os"))
		t.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			var fc, fl int64 = 80, 24
			if len(args) > 0 {
				seq := args[0]
				if seq.IsTuple() && len(seq.AsTuple().Items) >= 2 {
					fc = seq.AsTuple().Items[0].AsInt64()
					fl = seq.AsTuple().Items[1].AsInt64()
				} else if seq.IsList() && len(seq.AsList().Items) >= 2 {
					fc = seq.AsList().Items[0].AsInt64()
					fl = seq.AsList().Items[1].AsInt64()
				}
			}
			return terminalSizeVal(int(fc), int(fl))
		}
		pair := func(self val.Value) val.Value {
			c, _ := self.AsInstance().GetAttr(val.InternString("columns"))
			l, _ := self.AsInstance().GetAttr(val.InternString("lines"))
			return val.NewTupleVal(c, l)
		}
		t.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.NewIterVal(pair(args[0]))
		}))
		t.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			return val.Int64Val(2)
		}))
		t.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			i := int(args[1].AsInt64())
			if i < 0 {
				i += 2
			}
			if i < 0 || i > 1 {
				panic("IndexError: tuple index out of range")
			}
			return pair(args[0]).AsTuple().Items[i]
		}))
		t.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			p := pair(args[0]).AsTuple().Items
			return val.NewStringVal("os.terminal_size(columns=" + val.PyRepr(ctx, p[0]) + ", lines=" + val.PyRepr(ctx, p[1]) + ")")
		}))
		t.InvalidateVersion()
		terminalSizeType = t
	})
	v := val.NewInstanceOfType(terminalSizeType)
	v.AsInstance().SetAttr(val.InternString("columns"), val.Int64Val(int64(cols)))
	v.AsInstance().SetAttr(val.InternString("lines"), val.Int64Val(int64(lines)))
	return v
}

// terminalSize follows shutil.get_terminal_size: COLUMNS/LINES win, then
// the controlling terminal, then the fallback.
func terminalSize(fallbackCols, fallbackLines int) (int, int) {
	cols, lines := envInt("COLUMNS"), envInt("LINES")
	if cols <= 0 || lines <= 0 {
		c, l, ok := ttySize()
		if ok {
			if cols <= 0 {
				cols = c
			}
			if lines <= 0 {
				lines = l
			}
		}
	}
	if cols <= 0 {
		cols = fallbackCols
	}
	if lines <= 0 {
		lines = fallbackLines
	}
	return cols, lines
}

func envInt(name string) int {
	s, ok := os.LookupEnv(name)
	if !ok {
		return 0
	}
	n := 0
	if s == "" {
		return 0
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}

func (e *Engine) createPosixModule() val.Value {
	modVal := val.NewModuleVal("posix", "")
	mod := modVal.AsModule()
	installPosix(mod)
	return modVal
}

func (e *Engine) createShutilModule() val.Value {
	modVal := val.NewModuleVal("shutil", "")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("get_terminal_size"), val.NewBuiltinFuncVal("get_terminal_size", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fc, fl := 80, 24
		fb := val.NoneVal()
		if pa := posArgs(args, kwnames); len(pa) >= 1 {
			fb = pa[0]
		} else if v, ok := kwArg(args, kwnames, "fallback"); ok {
			fb = v
		}
		if fb.IsTuple() && len(fb.AsTuple().Items) == 2 {
			it := fb.AsTuple().Items
			if it[0].IsInt64() && it[1].IsInt64() {
				fc, fl = int(it[0].AsInt64()), int(it[1].AsInt64())
			}
		}
		c, l := terminalSize(fc, fl)
		return terminalSizeVal(c, l)
	}))
	return modVal
}
