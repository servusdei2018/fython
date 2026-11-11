package vm

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/servusdei2018/fython/pkg/val"
)

// pyFD extracts a file descriptor from an integer or from an object with a
// fileno() method.
func pyFD(ctx *val.ExecContext, v val.Value) int {
	if v.IsInt64() {
		return int(v.AsInt64())
	}
	if v.IsInstance() {
		if fn, _, ok := v.Type().LookupMRO(val.InternString("fileno")); ok {
			res := ctx.Invoke1(fn, v)
			if res.IsInt64() {
				return int(res.AsInt64())
			}
		}
	}
	panic("TypeError: argument should be an integer, or have a fileno() method")
}

// pyInt extracts a Python integer argument.
func pyInt(args []val.Value, i int, name string) int {
	if len(args) <= i {
		panic("TypeError: " + name + "() takes more arguments")
	}
	if !args[i].IsInt64() {
		panic("TypeError: " + name + "() argument must be an integer")
	}
	return int(args[i].AsInt64())
}

// pySeqItems returns the elements of a list or a tuple.
func pySeqItems(v val.Value) []val.Value {
	if v.IsList() {
		return v.AsList().Items
	}
	if v.IsTuple() {
		return v.AsTuple().Items
	}
	return nil
}

// createTermiosModule builds the POSIX terminal interface module.
func (e *Engine) createTermiosModule() val.Value {
	modVal := val.NewModuleVal("termios", "POSIX terminal interface")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))

	// The indices of the attribute list.
	mod.Dict.Set(val.InternString("IFLAG"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("OFLAG"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("CFLAG"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("LFLAG"), val.Int64Val(3))
	mod.Dict.Set(val.InternString("ISPEED"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("OSPEED"), val.Int64Val(5))
	mod.Dict.Set(val.InternString("CC"), val.Int64Val(6))

	set := func(name string, v int64) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(v))
	}

	// Input flags.
	set("IGNBRK", int64(unix.IGNBRK))
	set("BRKINT", int64(unix.BRKINT))
	set("IGNPAR", int64(unix.IGNPAR))
	set("PARMRK", int64(unix.PARMRK))
	set("INPCK", int64(unix.INPCK))
	set("ISTRIP", int64(unix.ISTRIP))
	set("INLCR", int64(unix.INLCR))
	set("IGNCR", int64(unix.IGNCR))
	set("ICRNL", int64(unix.ICRNL))
	set("IXON", int64(unix.IXON))
	set("IXANY", int64(unix.IXANY))
	set("IXOFF", int64(unix.IXOFF))
	set("IMAXBEL", int64(unix.IMAXBEL))
	set("IUTF8", int64(unix.IUTF8))

	// Output flags.
	set("OPOST", int64(unix.OPOST))
	set("ONLCR", int64(unix.ONLCR))
	set("OCRNL", int64(unix.OCRNL))
	set("ONOCR", int64(unix.ONOCR))
	set("ONLRET", int64(unix.ONLRET))
	set("OFILL", int64(unix.OFILL))
	set("OFDEL", int64(unix.OFDEL))

	// Control flags.
	set("CSIZE", int64(unix.CSIZE))
	set("CS5", int64(unix.CS5))
	set("CS6", int64(unix.CS6))
	set("CS7", int64(unix.CS7))
	set("CS8", int64(unix.CS8))
	set("CSTOPB", int64(unix.CSTOPB))
	set("CREAD", int64(unix.CREAD))
	set("PARENB", int64(unix.PARENB))
	set("PARODD", int64(unix.PARODD))
	set("HUPCL", int64(unix.HUPCL))
	set("CLOCAL", int64(unix.CLOCAL))
	set("CBAUD", int64(unix.CBAUD))
	set("B0", int64(unix.B0))
	set("B50", int64(unix.B50))
	set("B75", int64(unix.B75))
	set("B110", int64(unix.B110))
	set("B134", int64(unix.B134))
	set("B150", int64(unix.B150))
	set("B200", int64(unix.B200))
	set("B300", int64(unix.B300))
	set("B600", int64(unix.B600))
	set("B1200", int64(unix.B1200))
	set("B1800", int64(unix.B1800))
	set("B2400", int64(unix.B2400))
	set("B4800", int64(unix.B4800))
	set("B9600", int64(unix.B9600))
	set("B19200", int64(unix.B19200))
	set("B38400", int64(unix.B38400))
	set("B57600", int64(unix.B57600))
	set("B115200", int64(unix.B115200))
	set("B230400", int64(unix.B230400))
	set("B460800", int64(unix.B460800))
	set("B500000", int64(unix.B500000))
	set("B576000", int64(unix.B576000))
	set("B921600", int64(unix.B921600))
	set("B1000000", int64(unix.B1000000))
	set("B1152000", int64(unix.B1152000))
	set("B1500000", int64(unix.B1500000))
	set("B2000000", int64(unix.B2000000))
	set("B2500000", int64(unix.B2500000))
	set("B3000000", int64(unix.B3000000))
	set("B3500000", int64(unix.B3500000))
	set("B4000000", int64(unix.B4000000))

	// Local flags.
	set("ISIG", int64(unix.ISIG))
	set("ICANON", int64(unix.ICANON))
	set("XCASE", int64(unix.XCASE))
	set("ECHO", int64(unix.ECHO))
	set("ECHOE", int64(unix.ECHOE))
	set("ECHOK", int64(unix.ECHOK))
	set("ECHONL", int64(unix.ECHONL))
	set("NOFLSH", int64(unix.NOFLSH))
	set("TOSTOP", int64(unix.TOSTOP))
	set("ECHOCTL", int64(unix.ECHOCTL))
	set("ECHOPRT", int64(unix.ECHOPRT))
	set("ECHOKE", int64(unix.ECHOKE))
	set("FLUSHO", int64(unix.FLUSHO))
	set("PENDIN", int64(unix.PENDIN))
	set("IEXTEN", int64(unix.IEXTEN))

	// Control characters.
	set("VINTR", 0)
	set("VQUIT", 1)
	set("VERASE", 2)
	set("VKILL", 3)
	set("VEOF", 4)
	set("VTIME", 5)
	set("VMIN", 6)
	set("VSWTC", 7)
	set("VSTART", 8)
	set("VSTOP", 9)
	set("VSUSP", 10)
	set("VEOL", 11)
	set("VREPRINT", 12)
	set("VDISCARD", 13)
	set("VWERASE", 14)
	set("VLNEXT", 15)
	set("VEOL2", 16)

	// Actions. The Python values are the POSIX ones, not the ioctl numbers.
	set("TCSADRAIN", 1)
	set("TCSAFLUSH", 2)
	set("TCSANOW", 0)
	set("TCIFLUSH", 0)
	set("TCIOFLUSH", 1)

	mod.Dict.Set(val.InternString("tcgetattr"), val.NewBuiltinFuncVal("tcgetattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fd := pyFD(ctx, args[0])
		t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			panic("OSError: " + err.Error())
		}
		cc := make([]val.Value, len(t.Cc))
		for i, b := range t.Cc {
			cc[i] = val.Int64Val(int64(b))
		}
		return val.NewListVal(
			val.Int64Val(int64(t.Iflag)),
			val.Int64Val(int64(t.Oflag)),
			val.Int64Val(int64(t.Cflag)),
			val.Int64Val(int64(t.Lflag)),
			val.Int64Val(int64(t.Ispeed)),
			val.Int64Val(int64(t.Ospeed)),
			val.NewListVal(cc...),
		)
	}))

	mod.Dict.Set(val.InternString("tcsetattr"), val.NewBuiltinFuncVal("tcsetattr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: tcsetattr() takes 3 arguments")
		}
		fd := pyFD(ctx, args[0])
		when := pyInt(args, 1, "tcsetattr")
		items := pySeqItems(args[2])
		if items == nil {
			panic("TypeError: tcsetattr() attribute list must be a list or a tuple")
		}
		if len(items) != 7 {
			panic("ValueError: tcsetattr() attribute list must have 7 elements")
		}
		t := &unix.Termios{}
		t.Iflag = uint32(items[0].AsInt64())
		t.Oflag = uint32(items[1].AsInt64())
		t.Cflag = uint32(items[2].AsInt64())
		t.Lflag = uint32(items[3].AsInt64())
		t.Ispeed = uint32(items[4].AsInt64())
		t.Ospeed = uint32(items[5].AsInt64())
		cc := pySeqItems(items[6])
		for i := range t.Cc {
			if i < len(cc) {
				t.Cc[i] = byte(cc[i].AsInt64())
			}
		}
		var err error
		switch when {
		case 0:
			err = unix.IoctlSetTermios(fd, unix.TCSETS, t)
		case 1:
			err = unix.IoctlSetTermios(fd, unix.TCSETSW, t)
		case 2:
			err = unix.IoctlSetTermios(fd, unix.TCSETSF, t)
		default:
			panic("ValueError: tcsetattr() when must be TCSANOW, TCSADRAIN or TCSAFLUSH")
		}
		if err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("tcgetwinsize"), val.NewBuiltinFuncVal("tcgetwinsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		fd := pyFD(ctx, args[0])
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NewTupleVal(val.Int64Val(int64(ws.Row)), val.Int64Val(int64(ws.Col)))
	}))

	mod.Dict.Set(val.InternString("tcsetwinsize"), val.NewBuiltinFuncVal("tcsetwinsize", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: tcsetwinsize() takes 3 arguments")
		}
		fd := pyFD(ctx, args[0])
		rows := uint16(pyInt(args, 1, "tcsetwinsize"))
		cols := uint16(pyInt(args, 2, "tcsetwinsize"))
		if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols}); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	return modVal
}

// createFcntlModule builds the POSIX file control module.
func (e *Engine) createFcntlModule() val.Value {
	modVal := val.NewModuleVal("fcntl", "POSIX file control")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))

	set := func(name string, v int64) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(v))
	}
	set("F_DUPFD", int64(unix.F_DUPFD))
	set("F_GETFD", int64(unix.F_GETFD))
	set("F_GETFL", int64(unix.F_GETFL))
	set("F_SETFD", int64(unix.F_SETFD))
	set("F_SETFL", int64(unix.F_SETFL))
	set("F_GETLK", int64(unix.F_GETLK))
	set("F_SETLK", int64(unix.F_SETLK))
	set("F_SETLKW", int64(unix.F_SETLKW))
	set("FD_CLOEXEC", int64(unix.FD_CLOEXEC))
	set("F_RDLCK", int64(unix.F_RDLCK))
	set("F_WRLCK", int64(unix.F_WRLCK))
	set("F_UNLCK", int64(unix.F_UNLCK))
	set("LOCK_SH", int64(unix.LOCK_SH))
	set("LOCK_EX", int64(unix.LOCK_EX))
	set("LOCK_NB", int64(unix.LOCK_NB))
	set("LOCK_UN", int64(unix.LOCK_UN))

	mod.Dict.Set(val.InternString("fcntl"), val.NewBuiltinFuncVal("fcntl", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: fcntl() takes at least 2 arguments")
		}
		fd := pyFD(ctx, args[0])
		op := pyInt(args, 1, "fcntl")
		switch op {
		case unix.F_GETFD:
			res, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(int64(res))
		case unix.F_SETFD:
			if len(args) < 3 {
				panic("TypeError: fcntl() missing argument")
			}
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, pyInt(args, 2, "fcntl")); err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(0)
		case unix.F_GETFL:
			res, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(int64(res))
		case unix.F_SETFL:
			if len(args) < 3 {
				panic("TypeError: fcntl() missing argument")
			}
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, pyInt(args, 2, "fcntl")); err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(0)
		case unix.F_DUPFD:
			if len(args) < 3 {
				panic("TypeError: fcntl() missing argument")
			}
			res, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD, pyInt(args, 2, "fcntl"))
			if err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(int64(res))
		case unix.F_GETLK, unix.F_SETLK, unix.F_SETLKW:
			if len(args) < 3 {
				panic("TypeError: fcntl() missing argument")
			}
			items := pySeqItems(args[2])
			if len(items) < 3 {
				panic("ValueError: fcntl() lock must have at least 3 elements")
			}
			lk := unix.Flock_t{
				Type:   int16(items[0].AsInt64()),
				Whence: int16(items[1].AsInt64()),
				Start:  items[2].AsInt64(),
			}
			if len(items) > 3 {
				lk.Len = items[3].AsInt64()
			}
			if op == unix.F_GETLK {
				if err := unix.FcntlFlock(uintptr(fd), unix.F_GETLK, &lk); err != nil {
					panic("OSError: " + err.Error())
				}
				return val.NewListVal(
					val.Int64Val(int64(lk.Type)),
					val.Int64Val(int64(lk.Whence)),
					val.Int64Val(lk.Start),
					val.Int64Val(lk.Len),
					val.Int64Val(int64(lk.Pid)),
				)
			}
			if err := unix.FcntlFlock(uintptr(fd), op, &lk); err != nil {
				panic("OSError: " + err.Error())
			}
			return val.Int64Val(0)
		default:
			panic("ValueError: fcntl() unsupported command")
		}
	}))

	return modVal
}

// createResourceModule builds the POSIX resource usage module.
func (e *Engine) createResourceModule() val.Value {
	modVal := val.NewModuleVal("resource", "POSIX resource usage")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))

	set := func(name string, v int64) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(v))
	}
	set("RLIMIT_AS", int64(unix.RLIMIT_AS))
	set("RLIMIT_CORE", int64(unix.RLIMIT_CORE))
	set("RLIMIT_CPU", int64(unix.RLIMIT_CPU))
	set("RLIMIT_DATA", int64(unix.RLIMIT_DATA))
	set("RLIMIT_FSIZE", int64(unix.RLIMIT_FSIZE))
	set("RLIMIT_NOFILE", int64(unix.RLIMIT_NOFILE))
	set("RLIMIT_STACK", int64(unix.RLIMIT_STACK))
	set("RUSAGE_CHILDREN", int64(unix.RUSAGE_CHILDREN))
	set("RUSAGE_SELF", int64(unix.RUSAGE_SELF))

	mod.Dict.Set(val.InternString("getrlimit"), val.NewBuiltinFuncVal("getrlimit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getrlimit() takes 1 argument")
		}
		var res unix.Rlimit
		if err := unix.Getrlimit(pyInt(args, 0, "getrlimit"), &res); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NewTupleVal(val.Int64Val(int64(res.Cur)), val.Int64Val(int64(res.Max)))
	}))

	mod.Dict.Set(val.InternString("setrlimit"), val.NewBuiltinFuncVal("setrlimit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: setrlimit() takes 2 arguments")
		}
		items := pySeqItems(args[1])
		if len(items) != 2 {
			panic("ValueError: setrlimit() limit must be a 2-tuple")
		}
		lim := unix.Rlimit{Cur: uint64(items[0].AsInt64()), Max: uint64(items[1].AsInt64())}
		if err := unix.Setrlimit(pyInt(args, 0, "setrlimit"), &lim); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("getrusage"), val.NewBuiltinFuncVal("getrusage", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		who := 0
		if len(args) > 0 {
			who = pyInt(args, 0, "getrusage")
		}
		var ru unix.Rusage
		if err := unix.Getrusage(who, &ru); err != nil {
			panic("OSError: " + err.Error())
		}
		return val.NewTupleVal(
			val.Float64Val(float64(ru.Utime.Sec)+float64(ru.Utime.Usec)/1e6),
			val.Float64Val(float64(ru.Stime.Sec)+float64(ru.Stime.Usec)/1e6),
			val.Int64Val(ru.Maxrss),
			val.Int64Val(ru.Ixrss),
			val.Int64Val(ru.Idrss),
			val.Int64Val(ru.Isrss),
			val.Int64Val(ru.Minflt),
			val.Int64Val(ru.Majflt),
			val.Int64Val(ru.Nswap),
			val.Int64Val(ru.Inblock),
			val.Int64Val(ru.Oublock),
			val.Int64Val(ru.Msgsnd),
			val.Int64Val(ru.Msgrcv),
			val.Int64Val(ru.Nsignals),
			val.Int64Val(ru.Nvcsw),
			val.Int64Val(ru.Nivcsw),
		)
	}))

	return modVal
}

// createSyslogModule builds the POSIX syslog module.
func (e *Engine) createSyslogModule() val.Value {
	modVal := val.NewModuleVal("syslog", "POSIX system logging")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))

	set := func(name string, v int64) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(v))
	}
	set("LOG_EMERG", 0)
	set("LOG_ALERT", 1)
	set("LOG_CRIT", 2)
	set("LOG_ERR", 3)
	set("LOG_WARNING", 4)
	set("LOG_NOTICE", 5)
	set("LOG_INFO", 6)
	set("LOG_DEBUG", 7)
	set("LOG_PID", 1)
	set("LOG_CONS", 2)
	set("LOG_NDELAY", 4)
	set("LOG_NOWAIT", 8)
	set("LOG_WAIT", 16)
	set("LOG_KERN", 0)
	set("LOG_USER", 8)
	set("LOG_MAIL", 16)
	set("LOG_DAEMON", 24)
	set("LOG_AUTH", 32)
	set("LOG_SYSLOG", 40)
	set("LOG_LPR", 48)
	set("LOG_NEWS", 56)
	set("LOG_UUCP", 64)
	set("LOG_CRON", 72)
	set("LOG_AUTHPRIV", 80)
	set("LOG_FTP", 88)
	set("LOG_LOCAL0", 128)
	set("LOG_LOCAL1", 136)
	set("LOG_LOCAL2", 144)
	set("LOG_LOCAL3", 152)
	set("LOG_LOCAL4", 160)
	set("LOG_LOCAL5", 168)
	set("LOG_LOCAL6", 176)
	set("LOG_LOCAL7", 184)

	mod.Dict.Set(val.InternString("openlog"), val.NewBuiltinFuncVal("openlog", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		// The ident, the option and the facility are accepted and ignored:
		// the messages are written to the standard error stream.
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("closelog"), val.NewBuiltinFuncVal("closelog", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("setlogmask"), val.NewBuiltinFuncVal("setlogmask", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(0)
	}))

	mod.Dict.Set(val.InternString("syslog"), val.NewBuiltinFuncVal("syslog", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: syslog() takes at least 1 argument")
		}
		priority := pyInt(args, 0, "syslog")
		message := ""
		if len(args) > 1 && args[1].IsAnyString() {
			message = args[1].AsString()
		}
		names := []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}
		level := priority & 7
		name := "unknown"
		if level < len(names) {
			name = names[level]
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", name, message)
		return val.NoneVal()
	}))

	return modVal
}

// createGrpModule builds the POSIX group database module.
func (e *Engine) createGrpModule() val.Value {
	modVal := val.NewModuleVal("grp", "POSIX group database")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.KeyErrorType))

	toEntry := func(name, passwd string, gid int, members []string) val.Value {
		mem := make([]val.Value, len(members))
		for i, m := range members {
			mem[i] = val.NewStringVal(m)
		}
		return val.NewTupleVal(
			val.NewStringVal(name),
			val.NewStringVal(passwd),
			val.Int64Val(int64(gid)),
			val.NewListVal(mem...),
		)
	}

	readGroups := func() []val.Value {
		data, err := os.ReadFile("/etc/group")
		if err != nil {
			return nil
		}
		var out []val.Value
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) < 4 {
				continue
			}
			gid, _ := strconv.Atoi(parts[2])
			out = append(out, toEntry(parts[0], parts[1], gid, strings.Fields(parts[3])))
		}
		return out
	}

	mod.Dict.Set(val.InternString("getgrall"), val.NewBuiltinFuncVal("getgrall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		entries := readGroups()
		if entries == nil {
			panic("OSError: cannot read the group database")
		}
		return val.NewListVal(entries...)
	}))

	mod.Dict.Set(val.InternString("getgrgid"), val.NewBuiltinFuncVal("getgrgid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getgrgid() takes 1 argument")
		}
		gid := pyInt(args, 0, "getgrgid")
		for _, e := range readGroups() {
			items := e.AsTuple().Items
			if items[2].IsInt64() && int(items[2].AsInt64()) == gid {
				return e
			}
		}
		panic("KeyError: gid " + strconv.Itoa(gid) + ": no such group")
	}))

	mod.Dict.Set(val.InternString("getgrnam"), val.NewBuiltinFuncVal("getgrnam", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getgrnam() takes 1 argument")
		}
		name := args[0].AsString()
		for _, e := range readGroups() {
			items := e.AsTuple().Items
			if items[0].AsString() == name {
				return e
			}
		}
		panic("KeyError: name " + name + ": no such group")
	}))

	return modVal
}

// createPwdModule builds the POSIX password database module.
func (e *Engine) createPwdModule() val.Value {
	modVal := val.NewModuleVal("pwd", "POSIX password database")
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.KeyErrorType))

	readEntries := func() []val.Value {
		data, err := os.ReadFile("/etc/passwd")
		if err != nil {
			return nil
		}
		var out []val.Value
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) < 7 {
				continue
			}
			uid, _ := strconv.Atoi(parts[2])
			gid, _ := strconv.Atoi(parts[3])
			out = append(out, val.NewTupleVal(
				val.NewStringVal(parts[0]),
				val.NewStringVal(parts[1]),
				val.Int64Val(int64(uid)),
				val.Int64Val(int64(gid)),
				val.NewStringVal(parts[4]),
				val.NewStringVal(parts[5]),
				val.NewStringVal(parts[6]),
			))
		}
		return out
	}

	mod.Dict.Set(val.InternString("getpwall"), val.NewBuiltinFuncVal("getpwall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		entries := readEntries()
		if entries == nil {
			panic("OSError: cannot read the password database")
		}
		return val.NewListVal(entries...)
	}))

	mod.Dict.Set(val.InternString("getpwuid"), val.NewBuiltinFuncVal("getpwuid", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getpwuid() takes 1 argument")
		}
		uid := pyInt(args, 0, "getpwuid")
		for _, e := range readEntries() {
			items := e.AsTuple().Items
			if items[2].IsInt64() && int(items[2].AsInt64()) == uid {
				return e
			}
		}
		panic("KeyError: uid " + strconv.Itoa(uid) + ": no such user")
	}))

	mod.Dict.Set(val.InternString("getpwnam"), val.NewBuiltinFuncVal("getpwnam", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: getpwnam() takes 1 argument")
		}
		name := args[0].AsString()
		for _, e := range readEntries() {
			items := e.AsTuple().Items
			if items[0].AsString() == name {
				return e
			}
		}
		panic("KeyError: name " + name + ": no such user")
	}))

	return modVal
}
