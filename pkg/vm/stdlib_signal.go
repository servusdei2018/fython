package vm

import (
	"os"
	ossignal "os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

// Native _signal. The Go runtime catches the OS signal on its own goroutine
// and only sets a bit in Engine.sigPending. The VM that installed a handler
// (the "owner", normally the main VM) polls that word at loop back-edges and
// in time.sleep, then runs the Python handler on its own goroutine. A Python
// handler therefore never runs concurrently with the code it interrupts.

const (
	sigNSIG = 65
	sigDfl  = 0
	sigIgn  = 1
)

// sigState is the per-engine signal table.
type sigState struct {
	once     sync.Once
	pumpOnce sync.Once
	mu       sync.Mutex
	handlers [sigNSIG]val.Value
	pending  atomic.Uint64
	owner    atomic.Pointer[VM]
	wake     chan struct{}
	ch       chan os.Signal
	watching [sigNSIG]bool
}

type sigName struct {
	name string
	num  syscall.Signal
}

// portableSignals exist in the syscall package on every supported OS.
var portableSignals = []sigName{
	{"SIGHUP", syscall.SIGHUP}, {"SIGINT", syscall.SIGINT}, {"SIGQUIT", syscall.SIGQUIT},
	{"SIGILL", syscall.SIGILL}, {"SIGTRAP", syscall.SIGTRAP}, {"SIGABRT", syscall.SIGABRT},
	{"SIGBUS", syscall.SIGBUS}, {"SIGFPE", syscall.SIGFPE}, {"SIGKILL", syscall.SIGKILL},
	{"SIGSEGV", syscall.SIGSEGV}, {"SIGPIPE", syscall.SIGPIPE}, {"SIGALRM", syscall.SIGALRM},
	{"SIGTERM", syscall.SIGTERM},
}

func (e *Engine) sig() *sigState {
	s := &e.signals
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.ch = make(chan os.Signal, 16)
		for n := range s.handlers {
			s.handlers[n] = val.Int64Val(sigDfl)
		}
		s.handlers[int(syscall.SIGPIPE)] = val.Int64Val(sigIgn)
	})
	return s
}

// pump turns Go signal deliveries into pending bits.
func (s *sigState) pump() {
	for sg := range s.ch {
		if n, ok := sg.(syscall.Signal); ok && int(n) > 0 && int(n) < sigNSIG {
			s.raise(int(n))
		}
	}
}

func (s *sigState) raise(n int) {
	s.pending.Or(1 << uint(n))
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// signalsPending is the cheap poll used on interpreter back-edges.
func (vm *VM) signalsPending() bool {
	return vm.engine.signals.pending.Load() != 0 && vm.engine.signals.owner.Load() == vm
}

// deliverSignals runs the Python handlers of every pending signal.
func (vm *VM) deliverSignals() {
	s := &vm.engine.signals
	bits := s.pending.Swap(0)
	for n := 1; n < sigNSIG; n++ {
		if bits&(1<<uint(n)) == 0 {
			continue
		}
		s.mu.Lock()
		h := s.handlers[n]
		s.mu.Unlock()
		if h.IsInt64() {
			continue
		}
		frame := val.NoneVal()
		if vm.activeFrameIdx >= 0 {
			frame = vm.materializeFrame(vm.activeFrameIdx)
		}
		vm.execCtx.Invoke2(h, val.Int64Val(int64(n)), frame)
	}
}

// deliverSignalsSafe converts a handler exception into a pending VM exception.
//
//go:noinline
func (vm *VM) deliverSignalsSafe() {
	defer func() {
		if r := recover(); r != nil {
			if !vm.execCtx.HasException() {
				vm.execCtx.Raise(panicToPyException(r))
			}
		}
	}()
	vm.deliverSignals()
}

// sleepInterruptibly sleeps for d, running signal handlers as they arrive.
func (vm *VM) sleepInterruptibly(d time.Duration) {
	s := vm.engine.sig()
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		timer := time.NewTimer(left)
		select {
		case <-timer.C:
			return
		case <-s.wake:
			timer.Stop()
			if vm.signalsPending() {
				vm.deliverSignals()
			}
		}
	}
}

func signalNumber(ctx *val.ExecContext, v val.Value) int {
	i, ok := intOf(ctx, v)
	if !ok {
		panic("TypeError: 'signalnum' must be an integer, not " + typeNameOf(v))
	}
	n := int(i)
	if n < 1 || n >= sigNSIG {
		panic("ValueError: signal number out of range")
	}
	return n
}

func (e *Engine) createSignalModule() val.Value {
	modVal := val.NewModuleVal("_signal", "")
	mod := modVal.AsModule()
	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, fn))
	}
	num := func(name string, n int) { mod.Dict.Set(val.InternString(name), val.Int64Val(int64(n))) }

	num("SIG_DFL", sigDfl)
	num("SIG_IGN", sigIgn)
	num("NSIG", sigNSIG)
	for _, sn := range portableSignals {
		num(sn.name, int(sn.num))
	}
	for _, sn := range extraSignals {
		num(sn.name, int(sn.num))
	}
	mod.Dict.Set(val.InternString("ITIMER_REAL"), val.Int64Val(0))

	s := e.sig()

	defaultInt := val.NewBuiltinFuncVal("default_int_handler", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		panic(val.NewException(val.KeyboardInterruptType, ""))
	})
	mod.Dict.Set(val.InternString("default_int_handler"), defaultInt)
	s.mu.Lock()
	if s.handlers[int(syscall.SIGINT)].IsInt64() && s.handlers[int(syscall.SIGINT)].AsInt64() == sigDfl {
		s.handlers[int(syscall.SIGINT)] = defaultInt
	}
	s.mu.Unlock()

	set("signal", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 2 {
			panic("TypeError: signal expected 2 arguments, got " + itoa(len(args)))
		}
		n := signalNumber(ctx, args[0])
		h := args[1]
		if h.IsInt64() {
			if k := h.AsInt64(); k != sigDfl && k != sigIgn {
				panic("TypeError: signal handler must be signal.SIG_IGN, signal.SIG_DFL, or a callable object")
			}
		} else if !isCallableValue(h) {
			panic("TypeError: signal handler must be signal.SIG_IGN, signal.SIG_DFL, or a callable object")
		}
		sg := syscall.Signal(n)
		if sg == syscall.SIGKILL || sg == extraSigStop {
			panic(newOSError(syscall.EINVAL, "", false))
		}
		vm := ctx.VM.(*VM)
		s.pumpOnce.Do(func() { go s.pump() })

		s.mu.Lock()
		defer s.mu.Unlock()
		old := s.handlers[n]
		s.handlers[n] = h
		s.owner.Store(vm)
		switch {
		case h.IsInt64() && h.AsInt64() == sigIgn:
			ossignal.Ignore(sg)
			s.watching[n] = false
		case h.IsInt64():
			ossignal.Reset(sg)
			s.watching[n] = false
		default:
			ossignal.Notify(s.ch, sg)
			s.watching[n] = true
		}
		return old
	})

	set("getsignal", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 1 {
			panic("TypeError: getsignal expected 1 argument, got " + itoa(len(args)))
		}
		n := signalNumber(ctx, args[0])
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.handlers[n]
	})

	set("raise_signal", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 1 {
			panic("TypeError: raise_signal expected 1 argument, got " + itoa(len(args)))
		}
		n := signalNumber(ctx, args[0])
		s.mu.Lock()
		h := s.handlers[n]
		s.mu.Unlock()
		switch {
		case h.IsInt64() && h.AsInt64() == sigIgn:
		case h.IsInt64():
			if err := killProcess(os.Getpid(), syscall.Signal(n)); err != nil {
				raiseOSError(err, "")
			}
		default:
			s.raise(n)
			vm := ctx.VM.(*VM)
			if vm.signalsPending() {
				vm.deliverSignals()
			}
		}
		return val.NoneVal()
	})

	set("strsignal", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) != 1 {
			panic("TypeError: strsignal expected 1 argument, got " + itoa(len(args)))
		}
		n := signalNumber(ctx, args[0])
		text := syscall.Signal(n).String()
		if strings.HasPrefix(text, "signal ") {
			return val.NoneVal()
		}
		return val.NewStringVal(strings.ToUpper(text[:1]) + text[1:])
	})

	set("valid_signals", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		out := val.NewSetVal()
		for n := 1; n < sigNSIG; n++ {
			if n == 32 || n == 33 {
				continue
			}
			out.AsSet().Add(val.Int64Val(int64(n)))
		}
		return out
	})

	return modVal
}

func isCallableValue(v val.Value) bool {
	return val.IsCallable(v)
}
