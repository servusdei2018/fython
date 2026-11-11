package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// runLocalSnippet runs a snippet and returns the value of its "result" global.
func runLocalSnippet(t *testing.T, code string) string {
	t.Helper()
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(code), "<local>")
	if err != nil {
		t.Fatalf("Compilation error: %v\nCode:\n%s", err, code)
	}
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Unhandled Python exception:\n%s\nCode:\n%s", v.FormatException(exc), code)
	}
	out, ok := v.Globals().Get(val.InternString("result"))
	if !ok {
		t.Fatalf("snippet did not define 'result'\nCode:\n%s", code)
	}
	return val.PyRepr(v.ExecContext(), out)
}

// TestThreadingLocalIsolation verifies that threading.local keeps a separate
// attribute dictionary for every thread.
//
// It used to alias plain object, so every thread shared the very same
// attributes.
func TestThreadingLocalIsolation(t *testing.T) {
	got := runLocalSnippet(t, `
import threading

loc = threading.local()
loc.value = 'main'
seen = []

def worker():
    try:
        seen.append(loc.value)
    except AttributeError:
        seen.append('missing')
    loc.value = 'worker'

t = threading.Thread(target=worker)
t.start()
t.join()
result = (seen[0], loc.value)
`)
	want := "('missing', 'main')"
	if got != want {
		t.Fatalf("threading.local isolation = %s, want %s", got, want)
	}
}

// TestThreadingLocalIsSharedType verifies that threading.local and
// _thread._local are the same type.
func TestThreadingLocalIsSharedType(t *testing.T) {
	got := runLocalSnippet(t, `
import threading
import _thread
result = (_thread._local is threading.local, threading.local.__name__)
`)
	want := "(True, 'local')"
	if got != want {
		t.Fatalf("threading.local type = %s, want %s", got, want)
	}
}
