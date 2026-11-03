package vm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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
			panic("OSError: " + err.Error())
		}
		return val.NewStringVal(wd)
	}))

	// chdir(path)
	osMod.Dict.Set(val.InternString("chdir"), val.NewBuiltinFuncVal("chdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: chdir() takes 1 argument")
		}
		path := args[0].AsString()
		if err := os.Chdir(path); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	// listdir(path=".")
	osMod.Dict.Set(val.InternString("listdir"), val.NewBuiltinFuncVal("listdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		dir := "."
		if len(args) >= 1 && !args[0].IsNone() {
			dir = args[0].AsString()
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			panic("FileNotFoundError: " + err.Error())
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
		path := args[0].AsString()
		if err := os.Mkdir(path, 0777); err != nil {
			panic("FileExistsError: " + err.Error())
		}
		return val.NoneVal()
	}))

	// makedirs(path, exist_ok=False)
	osMod.Dict.Set(val.InternString("makedirs"), val.NewBuiltinFuncVal("makedirs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: makedirs() takes at least 1 argument")
		}
		path := args[0].AsString()
		existOk := false
		if len(args) >= 2 {
			existOk = args[1].IsTruthy()
		}
		err := os.MkdirAll(path, 0777)
		if err != nil && !existOk {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	// remove(path) / unlink(path)
	removeFn := val.NewBuiltinFuncVal("remove", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: remove() takes 1 argument")
		}
		path := args[0].AsString()
		if err := os.Remove(path); err != nil {
			panic("FileNotFoundError: " + err.Error())
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
		path := args[0].AsString()
		if err := os.Remove(path); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	// rename(src, dst)
	osMod.Dict.Set(val.InternString("rename"), val.NewBuiltinFuncVal("rename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: rename() takes 2 arguments")
		}
		src := args[0].AsString()
		dst := args[1].AsString()
		if err := os.Rename(src, dst); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	// getpid()
	osMod.Dict.Set(val.InternString("getpid"), val.NewBuiltinFuncVal("getpid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(int64(os.Getpid()))
	}))

	// stat(path)
	osMod.Dict.Set(val.InternString("stat"), val.NewBuiltinFuncVal("stat", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: stat() takes 1 argument")
		}
		path := args[0].AsString()
		info, err := os.Stat(path)
		if err != nil {
			panic("FileNotFoundError: " + err.Error())
		}
		statDictVal := val.NewDictVal(8)
		statDict := statDictVal.AsDict()
		statDict.Set(val.InternString("st_size"), val.Int64Val(info.Size()))
		statDict.Set(val.InternString("st_mtime"), val.Float64Val(float64(info.ModTime().UnixNano())/1e9))
		statDict.Set(val.InternString("st_mode"), val.Int64Val(int64(info.Mode())))
		return statDictVal
	}))

	// Low-level fd operations
	osMod.Dict.Set(val.InternString("open"), val.NewBuiltinFuncVal("open", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: open() takes at least 2 arguments")
		}
		name := args[0].AsString()
		flag := int(args[1].AsInt64())
		perm := os.FileMode(0666)
		if len(args) >= 3 {
			perm = os.FileMode(args[2].AsInt64())
		}
		f, err := os.OpenFile(name, flag, perm)
		if err != nil {
			panic("OSError: " + err.Error())
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
			panic("OSError: [Errno 9] Bad file descriptor")
		}
		_ = f.Close()
		return val.NoneVal()
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
		if args[1].IsBytes() {
			data = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			data = args[1].AsByteArray().Data
		} else if args[1].IsAnyString() {
			data = []byte(args[1].AsString())
		} else {
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
			panic("OSError: " + err.Error())
		}
		return val.Int64Val(int64(writtenN))
	}))

	// os.path submodule
	pathModVal := val.NewModuleVal("os.path", "")
	pathMod := pathModVal.AsModule()

	pathMod.Dict.Set(val.InternString("join"), val.NewBuiltinFuncVal("join", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = a.AsString()
		}
		return val.NewStringVal(filepath.Join(parts...))
	}))

	pathMod.Dict.Set(val.InternString("split"), val.NewBuiltinFuncVal("split", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: split() takes 1 argument")
		}
		p := args[0].AsString()
		dir, file := filepath.Split(p)
		return val.NewTupleVal(val.NewStringVal(strings.TrimSuffix(dir, string(filepath.Separator))), val.NewStringVal(file))
	}))

	pathMod.Dict.Set(val.InternString("splitext"), val.NewBuiltinFuncVal("splitext", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: splitext() takes 1 argument")
		}
		p := args[0].AsString()
		ext := filepath.Ext(p)
		base := strings.TrimSuffix(p, ext)
		return val.NewTupleVal(val.NewStringVal(base), val.NewStringVal(ext))
	}))

	pathMod.Dict.Set(val.InternString("basename"), val.NewBuiltinFuncVal("basename", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: basename() takes 1 argument")
		}
		return val.NewStringVal(filepath.Base(args[0].AsString()))
	}))

	pathMod.Dict.Set(val.InternString("dirname"), val.NewBuiltinFuncVal("dirname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dirname() takes 1 argument")
		}
		return val.NewStringVal(filepath.Dir(args[0].AsString()))
	}))

	pathMod.Dict.Set(val.InternString("exists"), val.NewBuiltinFuncVal("exists", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: exists() takes 1 argument")
		}
		_, err := os.Stat(args[0].AsString())
		return val.BoolVal(err == nil)
	}))

	pathMod.Dict.Set(val.InternString("isfile"), val.NewBuiltinFuncVal("isfile", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isfile() takes 1 argument")
		}
		info, err := os.Stat(args[0].AsString())
		return val.BoolVal(err == nil && !info.IsDir())
	}))

	pathMod.Dict.Set(val.InternString("isdir"), val.NewBuiltinFuncVal("isdir", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: isdir() takes 1 argument")
		}
		info, err := os.Stat(args[0].AsString())
		return val.BoolVal(err == nil && info.IsDir())
	}))

	pathMod.Dict.Set(val.InternString("abspath"), val.NewBuiltinFuncVal("abspath", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: abspath() takes 1 argument")
		}
		abs, err := filepath.Abs(args[0].AsString())
		if err != nil {
			return args[0]
		}
		return val.NewStringVal(abs)
	}))

	pathMod.Dict.Set(val.InternString("getsize"), val.NewBuiltinFuncVal("getsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getsize() takes 1 argument")
		}
		info, err := os.Stat(args[0].AsString())
		if err != nil {
			panic("FileNotFoundError: " + err.Error())
		}
		return val.Int64Val(info.Size())
	}))

	osMod.Dict.Set(val.InternString("path"), pathModVal)

	return osModVal
}
