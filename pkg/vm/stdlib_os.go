package vm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	fdMap   = make(map[int]*os.File)
	fdNext  = 10
	fdMutex sync.Mutex
)

func (e *Engine) createOSModule() val.Value {
	osModVal := val.NewModuleVal("os", "")
	osMod := osModVal.AsModule()

	// Constants
	osMod.Dict.Set(val.InternString("sep"), val.NewStringVal(string(filepath.Separator)))
	osMod.Dict.Set(val.InternString("linesep"), val.NewStringVal("\n"))
	osMod.Dict.Set(val.InternString("pathsep"), val.NewStringVal(string(filepath.ListSeparator)))
	if runtime.GOOS == "windows" {
		osMod.Dict.Set(val.InternString("name"), val.NewStringVal("nt"))
	} else {
		osMod.Dict.Set(val.InternString("name"), val.NewStringVal("posix"))
	}

	// Environment
	envDictVal := val.NewDictVal(32)
	envDict := envDictVal.AsDict()
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envDict.Set(val.NewStringVal(parts[0]), val.NewStringVal(parts[1]))
		}
	}
	osMod.Dict.Set(val.InternString("environ"), envDictVal)

	// getenv(key, default=None)
	osMod.Dict.Set(val.InternString("getenv"), val.NewBuiltinFuncVal("getenv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getenv() takes at least 1 argument")
		}
		key := args[0].AsString()
		if v, ok := os.LookupEnv(key); ok {
			return val.NewStringVal(v)
		}
		if len(args) >= 2 {
			return args[1]
		}
		return val.NoneVal()
	}))

	// putenv(key, val)
	osMod.Dict.Set(val.InternString("putenv"), val.NewBuiltinFuncVal("putenv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: putenv() takes 2 arguments")
		}
		key := args[0].AsString()
		v := args[1].AsString()
		_ = os.Setenv(key, v)
		envDict.Set(args[0], args[1])
		return val.NoneVal()
	}))

	// unsetenv(key)
	osMod.Dict.Set(val.InternString("unsetenv"), val.NewBuiltinFuncVal("unsetenv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: unsetenv() takes 1 argument")
		}
		key := args[0].AsString()
		_ = os.Unsetenv(key)
		return val.NoneVal()
	}))

	// getcwd()
	osMod.Dict.Set(val.InternString("getcwd"), val.NewBuiltinFuncVal("getcwd", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		wd, err := os.Getwd()
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NewStringVal(wd)
	}))

	// chdir(path)
	osMod.Dict.Set(val.InternString("chdir"), val.NewBuiltinFuncVal("chdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: chdir() takes 1 argument")
		}
		path := pathArg(ctx, args[0])
		if err := os.Chdir(path); err != nil {
			raiseOSError(err, path)
		}
		return val.NoneVal()
	}))

	// listdir(path=".")
	osMod.Dict.Set(val.InternString("listdir"), val.NewBuiltinFuncVal("listdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dir := "."
		if len(args) >= 1 && !args[0].IsNone() {
			dir = pathArg(ctx, args[0])
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			raiseOSError(err, dir)
		}
		items := make([]val.Value, len(entries))
		for i, ent := range entries {
			items[i] = val.NewStringVal(ent.Name())
		}
		return val.NewListVal(items...)
	}))

	// mkdir(path)
	osMod.Dict.Set(val.InternString("mkdir"), val.NewBuiltinFuncVal("mkdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: mkdir() takes 1 argument")
		}
		path := pathArg(ctx, args[0])
		if err := os.Mkdir(path, 0777); err != nil {
			raiseOSError(err, path)
		}
		return val.NoneVal()
	}))

	// makedirs(path, exist_ok=False)
	osMod.Dict.Set(val.InternString("makedirs"), val.NewBuiltinFuncVal("makedirs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: makedirs() takes at least 1 argument")
		}
		path := pathArg(ctx, args[0])
		existOk := false
		if v, ok := kwArg(args, kwnames, "exist_ok"); ok {
			existOk = v.IsTruthy()
		} else if len(args) >= 2 {
			existOk = args[1].IsTruthy()
		}
		if _, serr := os.Stat(path); serr == nil && !existOk {
			panic(newOSError(syscall.EEXIST, path, true))
		}
		err := os.MkdirAll(path, 0777)
		if err != nil {
			raiseOSError(err, path)
		}
		return val.NoneVal()
	}))

	// remove(path) / unlink(path)
	removeFn := val.NewBuiltinFuncVal("remove", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: remove() takes 1 argument")
		}
		path := pathArg(ctx, args[0])
		if err := os.Remove(path); err != nil {
			raiseOSError(err, path)
		}
		return val.NoneVal()
	})
	osMod.Dict.Set(val.InternString("remove"), removeFn)
	osMod.Dict.Set(val.InternString("unlink"), removeFn)

	// rmdir(path)
	osMod.Dict.Set(val.InternString("rmdir"), val.NewBuiltinFuncVal("rmdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: rmdir() takes 1 argument")
		}
		path := pathArg(ctx, args[0])
		if err := os.Remove(path); err != nil {
			raiseOSError(err, path)
		}
		return val.NoneVal()
	}))

	// rename(src, dst)
	osMod.Dict.Set(val.InternString("rename"), val.NewBuiltinFuncVal("rename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: rename() takes 2 arguments")
		}
		src := pathArg(ctx, args[0])
		dst := pathArg(ctx, args[1])
		if err := os.Rename(src, dst); err != nil {
			raiseOSError(err, src)
		}
		return val.NoneVal()
	}))

	// getpid()
	osMod.Dict.Set(val.InternString("getpid"), val.NewBuiltinFuncVal("getpid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(os.Getpid()))
	}))

	// Low-level fd operations
	osMod.Dict.Set(val.InternString("open"), val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: open() takes at least 2 arguments")
		}
		name := pathArg(ctx, args[0])
		flag := int(args[1].AsInt64())
		perm := os.FileMode(0666)
		if len(args) >= 3 {
			perm = os.FileMode(args[2].AsInt64())
		}
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			raiseOSError(err, name)
		}
		fdMutex.Lock()
		fd := fdNext
		fdNext++
		fdMap[fd] = f
		fdMutex.Unlock()
		return val.Int64Val(int64(fd))
	}))

	osMod.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: close() takes 1 argument")
		}
		fd := int(args[0].AsInt64())
		fdMutex.Lock()
		f, ok := fdMap[fd]
		delete(fdMap, fd)
		fdMutex.Unlock()
		if !ok {
			if err := syscall.Close(fd); err != nil {
				panic("OSError: [Errno 9] Bad file descriptor")
			}
			return val.NoneVal()
		}
		_ = f.Close()
		return val.NoneVal()
	}))

	osMod.Dict.Set(val.InternString("dup"), val.NewBuiltinFuncVal("dup", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dup() takes 1 argument")
		}
		fd := int(args[0].AsInt64())
		newFd, err := syscall.Dup(fd)
		if err != nil {
			panic("OSError: " + err.Error())
		}
		return val.Int64Val(int64(newFd))
	}))

	osMod.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: read() takes 2 arguments")
		}
		fd := int(args[0].AsInt64())
		n := int(args[1].AsInt64())
		fdMutex.Lock()
		f, ok := fdMap[fd]
		fdMutex.Unlock()
		if !ok {
			panic("OSError: [Errno 9] Bad file descriptor")
		}
		buf := make([]byte, n)
		readN, err := f.Read(buf)
		if err != nil && readN == 0 {
			return val.NewBytesVal(nil)
		}
		return val.NewBytesVal(buf[:readN])
	}))

	osMod.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: write() takes 2 arguments")
		}
		fd := int(args[0].AsInt64())
		var data []byte
		switch {
		case args[1].IsBytes():
			data = args[1].AsBytes().Data
		case args[1].IsByteArray():
			data = args[1].AsByteArray().Data
		case args[1].IsAnyString():
			data = []byte(args[1].AsString())
		default:
			panic("TypeError: a bytes-like object is required")
		}
		fdMutex.Lock()
		f, ok := fdMap[fd]
		fdMutex.Unlock()
		if !ok {
			panic("OSError: [Errno 9] Bad file descriptor")
		}
		writtenN, err := f.Write(data)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(writtenN))
	}))

	// os.path is bound to the upstream posixpath module on first import of os
	// (see bindOSPath); it cannot load while the engine builds its modules.
	installPosix(osMod)

	return osModVal
}
