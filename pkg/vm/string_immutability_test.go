package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// runStringSnippet runs a snippet and returns the value of its last printed
// line, failing the test when the snippet raises.
func runStringSnippet(t *testing.T, code string) string {
	t.Helper()
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(code), "<immutability>")
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

// TestStringAliasingAtModuleScope covers the module-scope aliasing bug: the
// left operand of "a + b" used to be extended in place, so every alias of it
// observed the appended bytes.
func TestStringAliasingAtModuleScope(t *testing.T) {
	got := runStringSnippet(t, `
a = 'foobarbaz' + 'x'
b = a
a = a + 'y'
result = (a, b)
`)
	want := "('foobarbazxy', 'foobarbazx')"
	if got != want {
		t.Fatalf("module-scope aliasing = %s, want %s", got, want)
	}
}

// TestStringAliasingInFunctionScope covers the same bug inside a function.
func TestStringAliasingInFunctionScope(t *testing.T) {
	got := runStringSnippet(t, `
def f():
    a = 'foobarbaz' + 'x'
    b = a
    a = a + 'y'
    return (a, b)
result = f()
`)
	want := "('foobarbazxy', 'foobarbazx')"
	if got != want {
		t.Fatalf("function-scope aliasing = %s, want %s", got, want)
	}
}

// TestStringDictKeyStability covers the dict-key corruption: the key was
// mutated after insertion, so the hash table no longer found it.
func TestStringDictKeyStability(t *testing.T) {
	got := runStringSnippet(t, `
k = 'foobarbaz' + 'x'
d = {k: 1}
k = k + 'y'
result = (d, d.get(k))
`)
	want := "({'foobarbazx': 1}, None)"
	if got != want {
		t.Fatalf("dict key stability = %s, want %s", got, want)
	}
}

// TestStringContainerStability covers list, tuple and set elements that keep
// their original bytes after the source variable is extended.
func TestStringContainerStability(t *testing.T) {
	got := runStringSnippet(t, `
l = ['hello' + 'world']
l0 = l[0]
l1 = l[0] + '!'
t = ('hello' + 'world',)
t0 = t[0]
t1 = t[0] + '!'
result = (l0, l1, t0, t1)
`)
	want := "('helloworld', 'helloworld!', 'helloworld', 'helloworld!')"
	if got != want {
		t.Fatalf("container stability = %s, want %s", got, want)
	}
}

// TestStringAttributeStability covers instance attributes.
func TestStringAttributeStability(t *testing.T) {
	got := runStringSnippet(t, `
class C:
    pass
o = C()
o.a = 'foobarbaz' + 'x'
b = o.a
o.a = o.a + 'y'
result = (o.a, b)
`)
	want := "('foobarbazxy', 'foobarbazx')"
	if got != want {
		t.Fatalf("attribute stability = %s, want %s", got, want)
	}
}

// TestStringAugAssignAliasing covers "s += x" at module scope, which used to
// corrupt every alias of s.
func TestStringAugAssignAliasing(t *testing.T) {
	got := runStringSnippet(t, `
s = 'abc' + 'defghij'
t = s
s += 'X'
result = (s, t)
`)
	want := "('abcdefghijX', 'abcdefghij')"
	if got != want {
		t.Fatalf("aug-assign aliasing = %s, want %s", got, want)
	}
}

// TestStringConcatThroughCall covers a string that is only ever passed to a
// builtin before being extended.
func TestStringConcatThroughCall(t *testing.T) {
	got := runStringSnippet(t, `
def identity(v):
    return v
s = identity('foobarbaz' + 'x')
t = s
s = s + 'y'
result = (s, t)
`)
	want := "('foobarbazxy', 'foobarbazx')"
	if got != want {
		t.Fatalf("concat through call = %s, want %s", got, want)
	}
}

// TestStringConcatChainKeepsBytes verifies that chained concatenations still
// produce the right bytes after the ownership rule was introduced.
func TestStringConcatChainKeepsBytes(t *testing.T) {
	got := runStringSnippet(t, `
a = 'alpha'
b = 'bravo'
c = 'charlie'
result = a + b + c
`)
	want := "'alphabravocharlie'"
	if got != want {
		t.Fatalf("concat chain = %s, want %s", got, want)
	}
}

// TestStringIOGetValueIsACopy verifies that StringIO.getvalue() does not
// alias the live buffer of the stream.
func TestStringIOGetValueIsACopy(t *testing.T) {
	got := runStringSnippet(t, `
import io
sio = io.StringIO()
sio.write('hello world')
v = sio.getvalue()
sio.seek(0)
sio.write('XXXXX')
result = (v, sio.getvalue())
`)
	want := "('hello world', 'XXXXX world')"
	if got != want {
		t.Fatalf("StringIO.getvalue = %s, want %s", got, want)
	}
}

// TestConcurrentStringConcat hammers a single shared string from several
// threads to prove the buffer ownership claim keeps the bytes intact.
func TestConcurrentStringConcat(t *testing.T) {
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(`
import threading
shared = 'shared-header-value'
results = []
lock = threading.Lock()
def worker():
    local = shared + '-suffix'
    with lock:
        results.append(local)
threads = [threading.Thread(target=worker) for _ in range(8)]
for t in threads:
    t.start()
for t in threads:
    t.join()
`), "<concurrency>")
	if err != nil {
		t.Fatalf("Compilation error: %v", err)
	}
	v.SetGlobal("__name__", val.NewStringVal("__main__"))
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Unhandled Python exception:\n%s", v.FormatException(exc))
	}

	sharedVal, _ := v.Globals().Get(val.InternString("shared"))
	resultsVal, _ := v.Globals().Get(val.InternString("results"))
	items := resultsVal.AsList().Items
	if len(items) != 8 {
		t.Fatalf("collected %d results, want 8", len(items))
	}
	want := sharedVal.String() + "-suffix"
	for i, item := range items {
		if item.String() != want {
			t.Fatalf("result %d = %s, want %s", i, item.String(), want)
		}
	}
}
