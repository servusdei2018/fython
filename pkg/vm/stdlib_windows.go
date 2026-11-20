//go:build windows

package vm

import (
	"os"
	"sync"
	"syscall"

	"github.com/servusdei2018/fython/pkg/val"
)

// fdMap/fdMutex back the synthetic fd layer shared with stdlib_io.go. They
// live in the Unix os module on other platforms; Windows keeps the same
// registry so io.open and friends still compile and fail cleanly.
var (
	fdMap   = make(map[int]*os.File)
	fdNext  = 10
	fdMutex sync.Mutex
)

// unsupportedModule builds a module whose exposed functions raise OSError with
// a clear message. Platform-specific modules are intentionally not ported to
// Windows; keeping their surface present avoids hard import failures.
func unsupportedModule(name, desc string, funcNames ...string) val.Value {
	modVal := val.NewModuleVal(name, desc)
	mod := modVal.AsModule()
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))
	for _, fn := range funcNames {
		mod.Dict.Set(val.InternString(fn), val.NewBuiltinFuncVal(name+"."+fn, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			ctx.Raise(val.NewException(val.OSErrorType, name+"."+fn+" is not supported on Windows"))
			return val.NoneVal()
		}))
	}
	return modVal
}

func (e *Engine) createOSModule() val.Value {
	return unsupportedModule("os", "portable os surface", "getcwd", "chdir", "listdir", "stat", "lstat", "mkdir", "makedirs", "remove", "unlink", "rename", "replace", "rmdir", "walk", "system", "getpid", "getenv", "putenv", "urandom", "cpu_count", "sep", "pathsep", "linesep", "devnull", "name")
}

func (e *Engine) createPosixModule() val.Value {
	return unsupportedModule("nt", "POSIX primitives", "getcwd", "chdir", "listdir", "stat", "lstat", "dup", "dup2", "read", "write", "close", "open", "pipe", "fork", "waitpid", "kill", "getpid", "getuid", "geteuid", "getgid", "umask", "strerror", "fstat", "isatty", "ttyname", "urandom", "system", "putenv")
}

func (e *Engine) createPosixSubprocessModule() val.Value {
	return unsupportedModule("_posixsubprocess", "subprocess execution", "fork_exec")
}

func (e *Engine) createSelectModule() val.Value {
	return unsupportedModule("select", "I/O multiplexing", "select", "poll", "epoll")
}

func (e *Engine) createSocketModule() val.Value {
	return unsupportedModule("_socket", "low-level socket interface", "socket", "socketpair", "close", "bind", "listen", "accept", "connect", "send", "recv", "sendto", "recvfrom", "getsockname", "getpeername", "setsockopt", "getsockopt", "shutdown", "gethostname", "gethostbyname", "inet_aton", "inet_ntoa", "inet_pton", "inet_ntop", "ntohl", "ntohs", "htonl", "htons")
}

func (e *Engine) createSSLModule() val.Value {
	return unsupportedModule("_ssl", "TLS/SSL interface", "wrap_socket", "sslwrap", "get_default_verify_paths", "RAND_bytes", "OPENSSL_VERSION")
}

func (e *Engine) createErrnoModule() val.Value {
	modVal := val.NewModuleVal("errno", "error codes")
	mod := modVal.AsModule()
	set := func(name string, en syscall.Errno) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(int64(en)))
	}
	set("EPERM", syscall.EPERM)
	set("ENOENT", syscall.ENOENT)
	set("ESRCH", syscall.ESRCH)
	set("EINTR", syscall.EINTR)
	set("EIO", syscall.EIO)
	set("EBADF", syscall.EBADF)
	set("EAGAIN", syscall.EAGAIN)
	set("ENOMEM", syscall.ENOMEM)
	set("EACCES", syscall.EACCES)
	set("EFAULT", syscall.EFAULT)
	set("EBUSY", syscall.EBUSY)
	set("EEXIST", syscall.EEXIST)
	set("EXDEV", syscall.EXDEV)
	set("ENODEV", syscall.ENODEV)
	set("ENOTDIR", syscall.ENOTDIR)
	set("EISDIR", syscall.EISDIR)
	set("EINVAL", syscall.EINVAL)
	set("ENFILE", syscall.ENFILE)
	set("EMFILE", syscall.EMFILE)
	set("ENOTTY", syscall.ENOTTY)
	set("EFBIG", syscall.EFBIG)
	set("ENOSPC", syscall.ENOSPC)
	set("ESPIPE", syscall.ESPIPE)
	set("EROFS", syscall.EROFS)
	set("EMLINK", syscall.EMLINK)
	set("EPIPE", syscall.EPIPE)
	set("EDOM", syscall.EDOM)
	set("ERANGE", syscall.ERANGE)
	set("ENOSYS", syscall.ENOSYS)
	set("ENOTEMPTY", syscall.ENOTEMPTY)
	set("ELOOP", syscall.ELOOP)
	set("ENOTSOCK", syscall.ENOTSOCK)
	set("ECONNABORTED", syscall.ECONNABORTED)
	set("ECONNREFUSED", syscall.ECONNREFUSED)
	set("ECONNRESET", syscall.ECONNRESET)
	set("ETIMEDOUT", syscall.ETIMEDOUT)
	set("EALREADY", syscall.EALREADY)
	set("ENOTCONN", syscall.ENOTCONN)
	set("EISCONN", syscall.EISCONN)
	set("EAFNOSUPPORT", syscall.EAFNOSUPPORT)
	return modVal
}

func (e *Engine) createTermiosModule() val.Value {
	return unsupportedModule("termios", "POSIX terminal interface", "tcgetattr", "tcsetattr", "tcgetwinsize", "tcsetwinsize", "tcsendbreak", "tcdrain", "tcflush", "tcflow")
}

func (e *Engine) createFcntlModule() val.Value {
	return unsupportedModule("fcntl", "POSIX file control", "fcntl", "lockf", "ioctl", "flock")
}

func (e *Engine) createResourceModule() val.Value {
	return unsupportedModule("resource", "POSIX resource usage", "getrlimit", "setrlimit", "getrusage")
}

func (e *Engine) createSyslogModule() val.Value {
	return unsupportedModule("syslog", "POSIX system logging", "openlog", "closelog", "syslog", "setlogmask")
}

func (e *Engine) createGrpModule() val.Value {
	return unsupportedModule("grp", "group database", "getgrall", "getgrgid", "getgrnam")
}

func (e *Engine) createPwdModule() val.Value {
	return unsupportedModule("pwd", "password database", "getpwall", "getpwuid", "getpwnam")
}

func (e *Engine) createMmapModule() val.Value {
	modVal := unsupportedModule("mmap", "memory mapped files", "mmap", "PAGESIZE", "ALLOCATIONGRANULARITY")
	mod := modVal.AsModule()
	set := func(name string, v int) {
		mod.Dict.Set(val.InternString(name), val.Int64Val(int64(v)))
	}
	set("ACCESS_READ", val.ACCESS_READ)
	set("ACCESS_WRITE", val.ACCESS_WRITE)
	set("ACCESS_COPY", val.ACCESS_COPY)
	set("ACCESS_DEFAULT", val.ACCESS_DEFAULT)
	set("MAP_SHARED", val.MAP_SHARED)
	set("MAP_PRIVATE", val.MAP_PRIVATE)
	set("PROT_READ", val.PROT_READ)
	set("PROT_WRITE", val.PROT_WRITE)
	set("PROT_EXEC", val.PROT_EXEC)
	return modVal
}

func (e *Engine) createGettextModule() val.Value {
	return unsupportedModule("gettext", "message translation", "gettext", "dgettext", "textdomain", "bindtextdomain", "install", "NullTranslations", "GNUTranslations")
}
