package vm_test

import (
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runIntrospectPy(t *testing.T, src string) (val.Value, *vm.VM) {
	t.Helper()
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	v := engine.AcquireVM()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString error: %v", err)
	}
	res := v.RunCode(co)
	return res, v
}

func runIntrospectPyWithFilename(t *testing.T, src string, filename string) (val.Value, *vm.VM) {
	t.Helper()
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	v := engine.AcquireVM()
	arena := compiler.NewASTArena(256)
	co, err := compiler.CompileWithFilename([]byte(src), arena, filename)
	if err != nil {
		t.Fatalf("CompileWithFilename error: %v", err)
	}
	res := v.RunCode(co)
	return res, v
}

func TestPEP667LiveFrameLocalsProxyMutation(t *testing.T) {
	src := `
import sys

def mutate_local():
    x = 10
    frame = sys._getframe(0)
    frame.f_locals['x'] = 99
    return x

res = mutate_local()
`
	res, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)
	_ = res

	resVal, ok := v.Globals().Get(val.InternString("res"))
	if !ok {
		t.Fatalf("expected 'res' in globals")
	}
	if !resVal.IsInt64() || resVal.AsInt64() != 99 {
		t.Fatalf("expected res == 99 after PEP 667 live mutation, got %v", resVal)
	}
}

func TestPEP667LiveFrameLocalsClosureMutation(t *testing.T) {
	src := `
import sys

def outer():
    x = 10
    def inner():
        nonlocal x
        frame = sys._getframe(0)
        # In inner, x is a cell var
        frame.f_locals['x'] = 88
        return x
    return inner()

res = outer()
`
	res, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)
	_ = res

	resVal, ok := v.Globals().Get(val.InternString("res"))
	if !ok {
		t.Fatalf("expected 'res' in globals")
	}
	if !resVal.IsInt64() || resVal.AsInt64() != 88 {
		t.Fatalf("expected res == 88 after PEP 667 cell mutation, got %v", resVal)
	}
}

func TestFrameDetachedLocalsSnapshot(t *testing.T) {
	src := `
import sys

saved_frame = None

def compute():
    global saved_frame
    a = 123
    b = "hello"
    saved_frame = sys._getframe(0)
    return a

compute()
locals_after = saved_frame.f_locals
a_val = locals_after['a']
b_val = locals_after['b']
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	aVal, ok := v.Globals().Get(val.InternString("a_val"))
	if !ok || !aVal.IsInt64() || aVal.AsInt64() != 123 {
		t.Fatalf("expected a_val == 123 from detached frame, got %v", aVal)
	}

	bVal, ok := v.Globals().Get(val.InternString("b_val"))
	if !ok || !bVal.IsAnyString() || bVal.AsString() != "hello" {
		t.Fatalf("expected b_val == 'hello' from detached frame, got %v", bVal)
	}
}

func TestFrameAttributesAndBack(t *testing.T) {
	src := `
import sys

back_name = None
lineno = None

def caller_func():
    return callee_func()

def callee_func():
    global back_name, lineno
    f = sys._getframe(0)
    lineno = f.f_lineno
    back_name = f.f_back.f_code.co_name
    return 42

caller_func()
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	backName, ok := v.Globals().Get(val.InternString("back_name"))
	if !ok || !backName.IsAnyString() || backName.AsString() != "caller_func" {
		t.Fatalf("expected back_name == 'caller_func', got %v", backName)
	}

	lineVal, ok := v.Globals().Get(val.InternString("lineno"))
	if !ok || !lineVal.IsInt64() || lineVal.AsInt64() <= 0 {
		t.Fatalf("expected valid f_lineno > 0, got %v", lineVal)
	}
}

func TestCodeObjectMetadataAndInspectSignature(t *testing.T) {
	src := `
import inspect

def test_fn(a, b, /, c, d=10, *args, e, f=20, **kwargs):
    pass

sig = inspect.signature(test_fn)
sig_str = str(sig)
code = test_fn.__code__
argcount = code.co_argcount
posonly = code.co_posonlyargcount
kwonly = code.co_kwonlyargcount
varnames = list(code.co_varnames)
qualname = test_fn.__qualname__
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	sigStrVal, _ := v.Globals().Get(val.InternString("sig_str"))
	if !sigStrVal.IsAnyString() {
		t.Fatalf("expected sig_str to be string, got %v", sigStrVal)
	}
	s := sigStrVal.AsString()
	if !strings.Contains(s, "a") || !strings.Contains(s, "*args") || !strings.Contains(s, "**kwargs") {
		t.Fatalf("unexpected signature string: %s", s)
	}

	argCountVal, _ := v.Globals().Get(val.InternString("argcount"))
	if !argCountVal.IsInt64() || argCountVal.AsInt64() != 4 {
		t.Fatalf("expected co_argcount == 4 (a, b, c, d), got %v", argCountVal)
	}

	posOnlyVal, _ := v.Globals().Get(val.InternString("posonly"))
	if !posOnlyVal.IsInt64() || posOnlyVal.AsInt64() != 2 {
		t.Fatalf("expected co_posonlyargcount == 2 (a, b), got %v", posOnlyVal)
	}

	kwOnlyVal, _ := v.Globals().Get(val.InternString("kwonly"))
	if !kwOnlyVal.IsInt64() || kwOnlyVal.AsInt64() != 2 {
		t.Fatalf("expected co_kwonlyargcount == 2 (e, f), got %v", kwOnlyVal)
	}

	qualNameVal, _ := v.Globals().Get(val.InternString("qualname"))
	if !qualNameVal.IsAnyString() || qualNameVal.AsString() != "test_fn" {
		t.Fatalf("expected qualname == 'test_fn', got %v", qualNameVal)
	}
}

func TestTracebackChainingAndFormatting(t *testing.T) {
	src := `
import traceback

exc_str = ""

def alpha():
    beta()

def beta():
    gamma()

def gamma():
    raise ValueError("custom error message")

try:
    alpha()
except ValueError as e:
    exc_str = traceback.format_exc()
`
	_, v := runIntrospectPyWithFilename(t, src, "pipeline.py")
	defer v.Engine().ReleaseVM(v)

	excStrVal, ok := v.Globals().Get(val.InternString("exc_str"))
	if !ok || !excStrVal.IsAnyString() {
		t.Fatalf("expected exc_str in globals")
	}
	out := excStrVal.AsString()
	if !strings.Contains(out, "pipeline.py") {
		t.Fatalf("expected 'pipeline.py' in traceback, got:\n%s", out)
	}
	if !strings.Contains(out, "in alpha") || !strings.Contains(out, "in beta") || !strings.Contains(out, "in gamma") {
		t.Fatalf("expected alpha, beta, gamma in traceback, got:\n%s", out)
	}
	if !strings.Contains(out, "ValueError: custom error message") {
		t.Fatalf("expected 'ValueError: custom error message' in traceback, got:\n%s", out)
	}
}

func TestWeakRefRefAndCallback(t *testing.T) {
	src := `
import weakref
import gc

callback_fired = False

class Dummy:
    def __init__(self, val):
        self.val = val

def on_cleanup(r):
    global callback_fired
    callback_fired = True

obj = Dummy(42)
r = weakref.ref(obj, on_cleanup)
assert r() is obj
assert r().val == 42

del obj
gc.collect()
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	cbFiredVal, ok := v.Globals().Get(val.InternString("callback_fired"))
	if !ok || !cbFiredVal.IsBool() || !cbFiredVal.AsBool() {
		t.Fatalf("expected callback_fired == True after collection, got %v", cbFiredVal)
	}
}

func TestWeakRefProxy(t *testing.T) {
	src := `
import weakref
import gc

class Target:
    def __init__(self, x):
        self.x = x
    def compute(self):
        return self.x * 2

obj = Target(21)
p = weakref.proxy(obj)
assert p.x == 21
assert p.compute() == 42

del obj
gc.collect()

dead = False
try:
    _ = p.x
except ReferenceError:
    dead = True
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	deadVal, ok := v.Globals().Get(val.InternString("dead"))
	if !ok || !deadVal.IsBool() || !deadVal.AsBool() {
		t.Fatalf("expected dead == True after proxy referent collected, got %v", deadVal)
	}
}

func TestWeakKeyAndValueDictionaries(t *testing.T) {
	src := `
import weakref
import gc

class Key:
    pass

class Val:
    pass

wk = weakref.WeakKeyDictionary()
wv = weakref.WeakValueDictionary()

k1 = Key()
v1 = Val()

wk[k1] = "val1"
wv["key1"] = v1

assert k1 in wk
assert "key1" in wv
assert wk[k1] == "val1"
assert wv["key1"] is v1

del k1
del v1
gc.collect()

wk_empty = (len(wk) == 0)
wv_empty = (len(wv) == 0)
`
	_, v := runIntrospectPy(t, src)
	defer v.Engine().ReleaseVM(v)

	wkEmptyVal, _ := v.Globals().Get(val.InternString("wk_empty"))
	if !wkEmptyVal.IsBool() || !wkEmptyVal.AsBool() {
		t.Fatalf("expected wk_empty == True, got %v", wkEmptyVal)
	}

	wvEmptyVal, _ := v.Globals().Get(val.InternString("wv_empty"))
	if !wvEmptyVal.IsBool() || !wvEmptyVal.AsBool() {
		t.Fatalf("expected wv_empty == True, got %v", wvEmptyVal)
	}
}

func TestZeroOverheadNormalFunctionCalls(t *testing.T) {
	src := `
def add(a, b):
    x = a + 1
    y = b + 2
    return x + y

def loop(n):
    total = 0
    i = 0
    while i < n:
        total += add(i, 1)
        i += 1
    return total

res = loop(10)
`
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile error: %v", err)
	}
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)

	// Warm up
	v.RunCode(co)

	// Verify zero allocations during pure execution without introspection
	loopCO, err := compiler.CompileString(`
total = 0
i = 0
while i < 100:
    total += add(i, 1)
    i += 1
`)
	if err != nil {
		t.Fatalf("compile loop error: %v", err)
	}

	allocs := testing.AllocsPerRun(10, func() {
		v.RunCode(loopCO)
	})

	if allocs > 0 {
		t.Fatalf("expected 0 allocs/op for pure function calls on slab, got %v", allocs)
	}
}
