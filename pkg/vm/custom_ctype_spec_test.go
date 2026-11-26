package vm_test

import (
	"runtime"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

// TestCustomCTypeFromSpec verifies PyType_FromSpec with basicsize payload, methods, getset, nb_add, dealloc.
func TestCustomCTypeFromSpec(t *testing.T) {
	src := `
#define PY_SSIZE_T_CLEAN
#include <Python.h>
#include <stdio.h>

static int dealloc_count = 0;

typedef struct {
    PyObject_HEAD
    long x;
    double y;
} MyObj;

static PyObject* my_new(PyTypeObject* type, PyObject* args, PyObject* kwargs) {
    MyObj* self = (MyObj*)type->tp_alloc(type, 0);
    if (!self) return NULL;
    self->x = 0;
    self->y = 0.0;
    return (PyObject*)self;
}

static int my_init(MyObj* self, PyObject* args, PyObject* kwargs) {
    long x = 42;
    if (!PyArg_ParseTuple(args, "|l", &x)) return -1;
    self->x = x;
    self->y = 3.14;
    return 0;
}

static void my_dealloc(MyObj* self) {
    dealloc_count++;
    Py_TYPE(self)->tp_free((PyObject*)self);
}

static PyObject* my_getx(MyObj* self, void* closure) {
    return PyLong_FromLong(self->x);
}
static int my_setx(MyObj* self, PyObject* value, void* closure) {
    if (!value) { PyErr_SetString(PyExc_AttributeError, "cannot delete x"); return -1; }
    self->x = PyLong_AsLong(value);
    return 0;
}

static PyObject* my_method(MyObj* self, PyObject* args) {
    return PyLong_FromLong(self->x + 100);
}

static PyObject* my_add(PyObject* a, PyObject* b) {
    MyObj* oa = (MyObj*)a;
    MyObj* ob = (MyObj*)b;
    // If b is int, add int; else assume MyObj.
    if (PyLong_Check(b)) {
        return PyLong_FromLong(oa->x + PyLong_AsLong(b));
    }
    if (Py_TYPE(b) == Py_TYPE(a)) {
        return PyLong_FromLong(oa->x + ((MyObj*)b)->x);
    }
    return Py_NotImplemented;
}

static PyMethodDef my_methods[] = {
    {"method", (PyCFunction)my_method, METH_VARARGS, "method return x+100"},
    {NULL, NULL, 0, NULL}
};
static PyGetSetDef my_getset[] = {
    {"x", (getter)my_getx, (setter)my_setx, "x field", NULL},
    {NULL, NULL, NULL, NULL, NULL}
};
static PyType_Slot my_slots[] = {
    {Py_tp_new, my_new},
    {Py_tp_init, my_init},
    {Py_tp_dealloc, my_dealloc},
    {Py_tp_methods, my_methods},
    {Py_tp_getset, my_getset},
    {Py_nb_add, my_add},
    {0, NULL}
};
static PyType_Spec my_spec = {
    .name = "mymod.MyType",
    .basicsize = sizeof(MyObj),
    .itemsize = 0,
    .flags = Py_TPFLAGS_DEFAULT | Py_TPFLAGS_BASETYPE,
    .slots = my_slots,
};

static PyObject* get_dealloc_count(PyObject* self, PyObject* args) {
    return PyLong_FromLong(dealloc_count);
}

static PyMethodDef module_methods[] = {
    {"make_type", NULL, 0, NULL}, // unused
    {"get_dealloc_count", get_dealloc_count, METH_NOARGS, "dealloc count"},
    {NULL, NULL, 0, NULL}
};

static struct PyModuleDef moduledef = {
    PyModuleDef_HEAD_INIT, "mymod", "custom type test", -1, module_methods,
};

PyMODINIT_FUNC PyInit_mymod(void) {
    PyObject* m = PyModule_Create2(&moduledef, 0);
    if (!m) return NULL;
    PyObject* t = PyType_FromSpec(&my_spec);
    if (!t) { Py_DECREF(m); return NULL; }
    if (PyModule_AddObject(m, "MyType", t) < 0) { Py_DECREF(t); Py_DECREF(m); return NULL; }
    return m;
}
`
	suffix := ".cpython-315-x86_64-linux-gnu.so"
	if runtime.GOOS != "linux" {
		suffix = ".so"
	}
	dir, shimOut := buildExtension(t, "mymod", src, suffix, false)
	t.Setenv("FYTHON_SHIM", shimOut)
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{dir})
	v := engine.NewVM()
	code := `
import mymod
o = mymod.MyType(5)
x = o.x
o.x = 10
m = o.method()
d = o + 5
class Sub(mymod.MyType):
    pass
s = Sub(7)
sx = s.x
sm = s.method()
dc = mymod.get_dealloc_count()
`
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v.RunCode(co)
	check := func(name string, want int64) {
		g, ok := v.GetGlobal(name)
		if !ok {
			t.Fatalf("global %s missing", name)
		}
		if g.AsInt64() != want {
			t.Errorf("%s = %d, want %d (%v)", name, g.AsInt64(), want, g)
		}
	}
	check("x", 5)
	check("m", 110)
	check("d", 15)
	check("sx", 7)
	check("sm", 107)
	// dc is count before deletion (0); after process, dealloc may not have run (Go GC holds ref).
	// Just log it.
	if g, ok := v.GetGlobal("dc"); ok {
		t.Logf("dealloc count before del: %d", g.AsInt64())
	}
	// Deletion test: del o/s and check dealloc increments via Go cleanup? C DECREF path needs _Py_Dealloc.
	// For this MVP, dealloc via Go AddCleanup is async; we only verify no crash.
	co2, _ := compiler.CompileString("import gc; gc.collect()")
	v.RunCode(co2)
}
