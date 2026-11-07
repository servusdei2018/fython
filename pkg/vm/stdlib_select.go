package vm

import (
	"syscall"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createSelectModule() val.Value {
	modVal := val.NewModuleVal("select", "I/O multiplexing primitives")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))
	mod.Dict.Set(val.InternString("PIPE_BUF"), val.Int64Val(4096))
	mod.Dict.Set(val.InternString("POLLIN"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("POLLPRI"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("POLLOUT"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("POLLERR"), val.Int64Val(8))
	mod.Dict.Set(val.InternString("POLLHUP"), val.Int64Val(16))
	mod.Dict.Set(val.InternString("POLLNVAL"), val.Int64Val(32))

	extractFD := func(ctx *val.ExecContext, v val.Value) (int, bool) {
		if v.IsInt64() {
			return int(v.AsInt64()), true
		}
		if v.IsInstance() {
			if fn, _, ok := v.Type().LookupMRO(val.InternString("fileno")); ok {
				res := ctx.Invoke1(fn, v)
				if res.IsInt64() {
					return int(res.AsInt64()), true
				}
			}
		}
		return -1, false
	}

	mod.Dict.Set(val.InternString("select"), val.NewModuleFuncVal("select", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: select() takes at least 3 arguments")
		}

		getList := func(v val.Value) []val.Value {
			if v.IsList() {
				return v.AsList().Items
			}
			if v.IsTuple() {
				return v.AsTuple().Items
			}
			if v.IsSet() {
				return v.AsSet().Items()
			}
			return nil
		}

		rItems := getList(args[0])
		wItems := getList(args[1])
		xItems := getList(args[2])

		var rSet, wSet, xSet syscall.FdSet
		hasR, hasW, hasX := false, false, false
		maxFd := -1

		type fdEntry struct {
			fd  int
			val val.Value
		}

		var rEntries, wEntries, xEntries []fdEntry

		for _, item := range rItems {
			fd, ok := extractFD(ctx, item)
			if !ok || fd < 0 {
				panic("ValueError: invalid file object")
			}
			rSet.Bits[fd/64] |= 1 << (uint(fd) % 64)
			hasR = true
			if fd > maxFd {
				maxFd = fd
			}
			rEntries = append(rEntries, fdEntry{fd: fd, val: item})
		}

		for _, item := range wItems {
			fd, ok := extractFD(ctx, item)
			if !ok || fd < 0 {
				panic("ValueError: invalid file object")
			}
			wSet.Bits[fd/64] |= 1 << (uint(fd) % 64)
			hasW = true
			if fd > maxFd {
				maxFd = fd
			}
			wEntries = append(wEntries, fdEntry{fd: fd, val: item})
		}

		for _, item := range xItems {
			fd, ok := extractFD(ctx, item)
			if !ok || fd < 0 {
				panic("ValueError: invalid file object")
			}
			xSet.Bits[fd/64] |= 1 << (uint(fd) % 64)
			hasX = true
			if fd > maxFd {
				maxFd = fd
			}
			xEntries = append(xEntries, fdEntry{fd: fd, val: item})
		}

		var tv *syscall.Timeval
		if len(args) > 3 && !args[3].IsNone() {
			var dur time.Duration
			if args[3].IsFloat64() {
				dur = time.Duration(args[3].AsFloat64() * float64(time.Second))
			} else if args[3].IsInt64() {
				dur = time.Duration(args[3].AsInt64()) * time.Second
			}
			tVal := syscall.NsecToTimeval(dur.Nanoseconds())
			tv = &tVal
		}

		var rPtr, wPtr, xPtr *syscall.FdSet
		if hasR {
			rPtr = &rSet
		}
		if hasW {
			wPtr = &wSet
		}
		if hasX {
			xPtr = &xSet
		}

		n, err := syscall.Select(maxFd+1, rPtr, wPtr, xPtr, tv)
		_ = n
		if err != nil && err != syscall.EINTR {
			raiseOSError(err, "")
		}

		var readyR, readyW, readyX []val.Value
		if rPtr != nil {
			for _, e := range rEntries {
				if (rSet.Bits[e.fd/64] & (1 << (uint(e.fd) % 64))) != 0 {
					readyR = append(readyR, e.val)
				}
			}
		}
		if wPtr != nil {
			for _, e := range wEntries {
				if (wSet.Bits[e.fd/64] & (1 << (uint(e.fd) % 64))) != 0 {
					readyW = append(readyW, e.val)
				}
			}
		}
		if xPtr != nil {
			for _, e := range xEntries {
				if (xSet.Bits[e.fd/64] & (1 << (uint(e.fd) % 64))) != 0 {
					readyX = append(readyX, e.val)
				}
			}
		}

		return val.NewTupleVal(
			val.NewListVal(readyR...),
			val.NewListVal(readyW...),
			val.NewListVal(readyX...),
		)
	}))

	return modVal
}
