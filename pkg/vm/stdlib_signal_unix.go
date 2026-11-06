//go:build unix

package vm

import "syscall"

func killProcess(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

const extraSigStop = syscall.SIGSTOP

var extraSignals = []sigName{
	{"SIGUSR1", syscall.SIGUSR1}, {"SIGUSR2", syscall.SIGUSR2}, {"SIGCHLD", syscall.SIGCHLD},
	{"SIGCONT", syscall.SIGCONT}, {"SIGSTOP", syscall.SIGSTOP}, {"SIGTSTP", syscall.SIGTSTP},
	{"SIGTTIN", syscall.SIGTTIN}, {"SIGTTOU", syscall.SIGTTOU}, {"SIGURG", syscall.SIGURG},
	{"SIGXCPU", syscall.SIGXCPU}, {"SIGXFSZ", syscall.SIGXFSZ}, {"SIGVTALRM", syscall.SIGVTALRM},
	{"SIGPROF", syscall.SIGPROF}, {"SIGWINCH", syscall.SIGWINCH}, {"SIGIO", syscall.SIGIO},
	{"SIGSYS", syscall.SIGSYS},
}
