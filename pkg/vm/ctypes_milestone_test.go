package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestCTypesIntegration(t *testing.T) {
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	v := engine.NewVM()
	code := `
import ctypes
lib = ctypes.CDLL(None)
lib.strlen.argtypes = [ctypes.c_char_p]
lib.strlen.restype = ctypes.c_size_t
n = lib.strlen(b"hello")
lib.abs.argtypes = [ctypes.c_int]
lib.abs.restype = ctypes.c_int
a = lib.abs(-7)
class Point(ctypes.Structure):
    _fields_ = [("x", ctypes.c_int), ("y", ctypes.c_int)]
sz = ctypes.sizeof(Point)
p = Point(1, 2)
px = p.x
p.x = 10
px2 = p.x
py = p.y
CMP = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_void_p, ctypes.c_void_p)
def cmp(a, b):
    av = ctypes.cast(a, ctypes.POINTER(ctypes.c_int)).contents.value
    bv = ctypes.cast(b, ctypes.POINTER(ctypes.c_int)).contents.value
    return av - bv
cmp_fn = CMP(cmp)
arr = (ctypes.c_int * 5)(5, 2, 4, 1, 3)
lib.qsort.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_size_t, CMP]
lib.qsort.restype = None
lib.qsort(ctypes.cast(arr, ctypes.c_void_p), 5, ctypes.sizeof(ctypes.c_int), cmp_fn)
s0 = arr[0]
s4 = arr[4]
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v.RunCode(co)
	checkInt := func(name string, want int64) {
		g, ok := v.GetGlobal(name)
		if !ok {
			t.Fatalf("global %s missing", name)
		}
		if g.AsInt64() != want {
			t.Errorf("%s = %d, want %d", name, g.AsInt64(), want)
		}
	}
	checkInt("n", 5)
	checkInt("a", 7)
	checkInt("sz", 8)
	checkInt("px", 1)
	checkInt("px2", 10)
	checkInt("py", 2)
	checkInt("s0", 1)
	checkInt("s4", 5)
}
