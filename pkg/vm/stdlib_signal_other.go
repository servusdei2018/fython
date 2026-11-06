//go:build !unix

package vm

import (
	"os"
	"syscall"
)

func killProcess(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(sig)
}

// No SIGSTOP off Unix: a value no real signal uses never matches.
const extraSigStop = syscall.Signal(-1)

var extraSignals = []sigName{}
