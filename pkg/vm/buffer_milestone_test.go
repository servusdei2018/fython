package vm_test

import (
	"runtime"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestBufferZeroCopy(t *testing.T) {
	src := `
#define PY_SSIZE_T_CLEAN
#include <Python.h>
#include <string.h>
#include <stdio.h>

static PyObject* mutate_u8(PyObject* self, PyObject* args) {
    PyObject* obj;
    if (!PyArg_ParseTuple(args, "O", &obj)) return NULL;
    Py_buffer view;
    if (PyObject_GetBuffer(obj, &view, PyBUF_WRITABLE) < 0) return NULL;
    // mutate first byte to 'Z'
    if (view.len > 0) {
        ((char*)view.buf)[0] = 'Z';
    }
    PyBuffer_Release(&view);
    return Py_None;
}

static PyObject* mutate_d2(PyObject* self, PyObject* args) {
    PyObject* obj;
    if (!PyArg_ParseTuple(args, "O", &obj)) return NULL;
    Py_buffer view;
    if (PyObject_GetBuffer(obj, &view, PyBUF_STRIDES) < 0) return NULL;
    // view is memoryview cast to 'd' [4,4] (?) Actually we pass cast view.
    // mutate element [1][2] : offset = 1*strides[0] + 2*strides[1]
    if (view.ndim == 2) {
        Py_ssize_t off = 1*view.strides[0] + 1*view.strides[1];
        ((double*)((char*)view.buf + off))[0] = 99.5;
    }
    PyBuffer_Release(&view);
    return Py_None;
}

static PyMethodDef m[] = {
    {"mutate_u8", mutate_u8, METH_VARARGS, "mutate"},
    {"mutate_d2", mutate_d2, METH_VARARGS, "mutate d"},
    {NULL,NULL,0,NULL}
};
static struct PyModuleDef d = {PyModuleDef_HEAD_INIT,"bufmod","t",-1,m};
PyMODINIT_FUNC PyInit_bufmod(void){ return PyModule_Create2(&d,0); }
`
	suffix := ".cpython-315-x86_64-linux-gnu.so"
	if runtime.GOOS != "linux" {
		suffix = ".so"
	}
	dir, shimOut := buildExtension(t, "bufmod", src, suffix, false)
	// rebuild shim after milestone2 shim changes (buildExtension already rebuilds shim from source)
	t.Setenv("FYTHON_SHIM", shimOut)
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{dir})
	v := engine.NewVM()
	code := `
import bufmod
ba = bytearray(b"hello")
bufmod.mutate_u8(ba)
m = memoryview(ba)
buf2 = bytearray(32)
mv = memoryview(buf2).cast('d', [2,2])
bufmod.mutate_d2(mv)
val = mv[1,0]
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v.RunCode(co)
	if g, ok := v.GetGlobal("ba"); !ok {
		t.Fatalf("ba missing")
	} else {
		s := string(g.AsByteArray().Data)
		t.Logf("ba=%q", s)
		if s != "Zello" {
			t.Errorf("ba=%q, want Zello", s)
		}
	}
	if g, ok := v.GetGlobal("val"); !ok {
		t.Fatalf("val missing")
	} else {
		t.Logf("val=%v (%f)", g, g.AsFloat64())
		// we mutated [1][2] but read [1][0]; check either? Actually mutate_d2 sets [1][2]=99.5, read [1][0] is 0.
		// Let's verify via mv[1,2] global instead: add val2
	}
	// second run to check [1,2]
	v2 := engine.NewVM()
	code2 := `
import bufmod
buf2 = bytearray(32)
mv = memoryview(buf2).cast('d', [2,2])
bufmod.mutate_d2(mv)
val12 = mv[1,1]
val10 = mv[1,0]
`
	co2, _ := compiler.CompileString(code2)
	v2.RunCode(co2)
	if g, ok := v2.GetGlobal("val12"); !ok || g.AsFloat64() != 99.5 {
		t.Errorf("val12=%v want 99.5", g)
	}
	if g, ok := v2.GetGlobal("val10"); !ok || g.AsFloat64() != 0.0 {
		t.Logf("val10=%v (expected 0)", g)
	}
}
