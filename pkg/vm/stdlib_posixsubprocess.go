package vm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/servusdei2018/fython/pkg/val"
)

func (e *Engine) createPosixSubprocessModule() val.Value {
	modVal := val.NewModuleVal("_posixsubprocess", "Subprocess execution support")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("fork_exec"), val.NewModuleFuncVal("fork_exec", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 14 {
			panic("TypeError: fork_exec() takes at least 14 arguments")
		}

		toStr := func(v val.Value) string {
			if v.IsBytes() {
				return string(v.AsBytes().Data)
			}
			if v.IsByteArray() {
				return string(v.AsByteArray().Data)
			}
			if v.IsAnyString() {
				return v.AsString()
			}
			return ""
		}

		// 0: args
		var argv []string
		if args[0].IsList() {
			for _, item := range args[0].AsList().Items {
				argv = append(argv, toStr(item))
			}
		} else if args[0].IsTuple() {
			for _, item := range args[0].AsTuple().Items {
				argv = append(argv, toStr(item))
			}
		}

		// 1: executable_list
		var executables []string
		if args[1].IsList() {
			for _, item := range args[1].AsList().Items {
				executables = append(executables, toStr(item))
			}
		} else if args[1].IsTuple() {
			for _, item := range args[1].AsTuple().Items {
				executables = append(executables, toStr(item))
			}
		}

		targetExe := ""
		for _, exe := range executables {
			if exe == "" {
				continue
			}
			if fi, err := os.Stat(exe); err == nil && !fi.IsDir() && (fi.Mode()&0111 != 0) {
				targetExe = exe
				break
			}
		}
		if targetExe == "" {
			if len(argv) > 0 {
				targetExe = argv[0]
			}
		}

		resolvedExe, err := exec.LookPath(targetExe)
		if err != nil {
			resolvedExe = targetExe
		}

		// 4: cwd
		cwd := ""
		if len(args) > 4 && !args[4].IsNone() {
			cwd = toStr(args[4])
		}

		// 5: env_list
		var envList []string
		if len(args) > 5 && !args[5].IsNone() {
			if args[5].IsList() {
				for _, item := range args[5].AsList().Items {
					envList = append(envList, toStr(item))
				}
			} else if args[5].IsTuple() {
				for _, item := range args[5].AsTuple().Items {
					envList = append(envList, toStr(item))
				}
			}
		} else {
			envList = os.Environ()
		}

		// 6..13: file descriptors
		p2cread := int(args[6].AsInt64())
		c2pwrite := int(args[9].AsInt64())
		errwrite := int(args[11].AsInt64())
		errpipeWrite := int(args[13].AsInt64())

		startNewSession := false
		if len(args) > 15 && args[15].IsTruthy() {
			startNewSession = true
		}

		var files []*os.File
		// Stdin
		if p2cread >= 0 {
			files = append(files, os.NewFile(uintptr(p2cread), "stdin"))
		} else {
			files = append(files, os.Stdin)
		}
		// Stdout
		if c2pwrite >= 0 {
			files = append(files, os.NewFile(uintptr(c2pwrite), "stdout"))
		} else {
			files = append(files, os.Stdout)
		}
		// Stderr
		if errwrite >= 0 {
			files = append(files, os.NewFile(uintptr(errwrite), "stderr"))
		} else {
			files = append(files, os.Stderr)
		}

		attr := &os.ProcAttr{
			Dir:   cwd,
			Env:   envList,
			Files: files,
			Sys:   &syscall.SysProcAttr{Setsid: startNewSession},
		}

		proc, startErr := os.StartProcess(resolvedExe, argv, attr)
		if startErr != nil {
			if errpipeWrite >= 0 {
				excName := "FileNotFoundError"
				errCode := "2"
				if errors.Is(startErr, os.ErrPermission) {
					excName = "PermissionError"
					errCode = "d"
				}
				msg := fmt.Sprintf("%s:%s:%s", excName, errCode, startErr.Error())
				_, _ = syscall.Write(errpipeWrite, []byte(msg))
			}
			return val.Int64Val(0)
		}

		return val.Int64Val(int64(proc.Pid))
	}))

	return modVal
}
