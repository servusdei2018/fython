package vm_test

import (
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// TestSingleModeDisplayHookAndUnderscore verifies CPython "single" mode
// expression echo and the builtins._ binding through the Python-level
// compile()/exec() path (the same path code.InteractiveConsole drives).
func TestSingleModeDisplayHookAndUnderscore(t *testing.T) {
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)
	v.SetGlobal("__name__", val.NewStringVal("__main__"))

	run := func(code string) {
		t.Helper()
		co, err := compiler.CompileSourceWithFilename([]byte(code), "<test>")
		if err != nil {
			t.Fatalf("compile: %v\n%s", err, code)
		}
		v.RunCode(co)
		if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
			t.Fatalf("unhandled exception:\n%s\n%s", v.FormatException(exc), code)
		}
	}

	run(`
import io, sys
buf = io.StringIO()
old = sys.stdout
sys.stdout = buf
exec(compile("21 * 2", "<stdin>", "single"))
sys.stdout = old
result_echo = buf.getvalue()
assert result_echo == "42\n", repr(result_echo)
assert _ == 42, _
buf.truncate(0)
buf.seek(0)
sys.stdout = buf
exec(compile("None", "<stdin>", "single"))
sys.stdout = old
assert buf.getvalue() == "", repr(buf.getvalue())
assert _ == 42, _
exec("_ = 99")
assert _ == 99, _
exec(compile("21 * 2", "<stdin>", "exec"))
`)
}

// TestInterruptCancelsInfiniteLoop verifies VM.RequestInterrupt raises a
// catchable KeyboardInterrupt inside running bytecode.
func TestInterruptCancelsInfiniteLoop(t *testing.T) {
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)
	v.SetGlobal("__name__", val.NewStringVal("__main__"))

	co, err := compiler.CompileSourceWithFilename([]byte(`
stopped = False
try:
    while True:
        pass
except KeyboardInterrupt:
    stopped = True
`), "<test>")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	done := make(chan struct{})
	go func() {
		v.RunCode(co)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	v.RequestInterrupt()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("VM did not unwind after RequestInterrupt")
	}
	stopped, ok := v.GetGlobal("stopped")
	if !ok || !stopped.IsTruthy() {
		t.Fatal("KeyboardInterrupt was not caught by Python code")
	}
	// The VM stays usable: a fresh statement evaluates after the interrupt.
	if !v.UnhandledException().IsNone() && v.UnhandledException().HeaderPtr() != nil {
		t.Fatalf("unexpected leftover exception: %s", v.FormatException(v.UnhandledException()))
	}
}

// TestSingleModeCompileEcho sanity-checks that a plain "single" ModeSingle
// compilation of an expression statement stores the underscore binding.
func TestSingleModeCompileEcho(t *testing.T) {
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)
	v.SetGlobal("__name__", val.NewStringVal("__main__"))

	co, err := compiler.CompileSourceModeWithOptimize([]byte("6 * 7"), "<stdin>", compiler.ModeSingle, 0)
	if err != nil {
		t.Fatalf("compile single: %v", err)
	}
	v.RunCode(co)
	if !v.UnhandledException().IsNone() && v.UnhandledException().HeaderPtr() != nil {
		t.Fatalf("exception: %s", v.FormatException(v.UnhandledException()))
	}
	b, _ := v.Builtins().Get(val.InternString("_"))
	if !b.IsInt64() || b.AsInt64() != 42 {
		t.Fatalf("builtins._ = %v, want 42", b)
	}
	// And "exec" mode must stay silent on value (no echo, no rebinding).
	co2, err := compiler.CompileSourceModeWithOptimize([]byte("1 + 1"), "<stdin>", compiler.ModeExec, 0)
	if err != nil {
		t.Fatalf("compile exec: %v", err)
	}
	v.RunCode(co2)
	b2, _ := v.Builtins().Get(val.InternString("_"))
	if !b2.IsInt64() || b2.AsInt64() != 42 {
		t.Fatalf("exec mode clobbered builtins._: %v", b2)
	}
	// displayhook is callable and returns None via the Python surface.
	hook, ok := v.GetModule("sys")
	if !ok || !hook.IsModule() {
		t.Fatal("missing sys module")
	}
	dh, ok := hook.AsModule().Dict.Get(val.InternString("displayhook"))
	if !ok || !val.IsCallable(dh) {
		t.Fatal("sys.displayhook must be callable")
	}
	// ps1/ps2 defaults exist.
	ps1, ok := hook.AsModule().Dict.Get(val.InternString("ps1"))
	if !ok || !ps1.IsAnyString() || ps1.AsString() != ">>> " {
		t.Fatalf("sys.ps1 = %v", ps1)
	}
	ps2, ok := hook.AsModule().Dict.Get(val.InternString("ps2"))
	if !ok || !ps2.IsAnyString() || ps2.AsString() != "... " {
		t.Fatalf("sys.ps2 = %v", ps2)
	}
	// excepthook and __excepthook__ are the same callable.
	eh, _ := hook.AsModule().Dict.Get(val.InternString("excepthook"))
	eeh, _ := hook.AsModule().Dict.Get(val.InternString("__excepthook__"))
	if !val.IsCallable(eh) || !val.IsCallable(eeh) {
		t.Fatal("__excepthook__ must be callable")
	}
}
