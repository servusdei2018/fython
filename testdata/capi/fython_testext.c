#define PY_SSIZE_T_CLEAN
#include <Python.h>

// hello(): returns 42 via PyLong_FromLong
static PyObject *hello(PyObject *self, PyObject *args) {
    (void)self;
    if (!PyArg_ParseTuple(args, "")) return NULL;
    return PyLong_FromLong(42);
}

// add(int a, int b) -> int
static PyObject *add(PyObject *self, PyObject *args) {
    (void)self;
    long a, b;
    if (!PyArg_ParseTuple(args, "ll", &a, &b)) return NULL;
    return PyLong_FromLong(a + b);
}

// greet(str name) -> str via PyUnicode_FromString
static PyObject *greet(PyObject *self, PyObject *args) {
    (void)self;
    const char *name = NULL;
    if (!PyArg_ParseTuple(args, "s", &name)) return NULL;
    PyObject *res = PyUnicode_FromString(name);
    if (!res) return NULL;
    Py_ssize_t sz = 0;
    const char *utf8s = PyUnicode_AsUTF8AndSize(res, &sz);
    (void)utf8s;
    (void)sz;
    return res;
}

// make_dict() -> dict with manipulation via PyDict_SetItemString
static PyObject *make_dict(PyObject *self, PyObject *args) {
    (void)self; (void)args;
    PyObject *d = PyDict_New();
    if (!d) return NULL;
    PyObject *v = PyLong_FromLong(99);
    if (!v) { Py_DECREF(d); return NULL; }
    if (PyDict_SetItemString(d, "answer", v) < 0) { Py_DECREF(v); Py_DECREF(d); return NULL; }
    Py_DECREF(v);
    return d;
}

// make_list() -> list via PyList_New/Append
static PyObject *make_list(PyObject *self, PyObject *args) {
    (void)self; (void)args;
    PyObject *ls = PyList_New(0);
    if (!ls) return NULL;
    PyObject *a = PyLong_FromLong(1);
    PyObject *b = PyLong_FromLong(2);
    if (!a || !b) { Py_XDECREF(a); Py_XDECREF(b); Py_DECREF(ls); return NULL; }
    if (PyList_Append(ls, a) < 0 || PyList_Append(ls, b) < 0) { Py_DECREF(a); Py_DECREF(b); Py_DECREF(ls); return NULL; }
    Py_DECREF(a); Py_DECREF(b);
    Py_ssize_t sz = PyList_Size(ls);
    (void)sz;
    return ls;
}

// build_pair(int a, str s) -> (a, s) via Py_BuildValue
static PyObject *build_pair(PyObject *self, PyObject *args) {
    (void)self;
    long a; const char *s;
    if (!PyArg_ParseTuple(args, "ls", &a, &s)) return NULL;
    return Py_BuildValue("(ls)", a, s);
}

// fail() -> raises ValueError
static PyObject *fail(PyObject *self, PyObject *args) {
    (void)self; (void)args;
    PyErr_SetString(PyExc_ValueError, "oops from C");
    return NULL;
}

// float_test(double x) -> double
static PyObject *float_test(PyObject *self, PyObject *args) {
    (void)self;
    double x;
    if (!PyArg_ParseTuple(args, "d", &x)) return NULL;
    return PyFloat_FromDouble(x * 2.0);
}

// bytes_test(bytes b) -> bytes len via PyBytes_AsString/PyBytes_Size
static PyObject *bytes_len(PyObject *self, PyObject *args) {
    (void)self;
    PyObject *b;
    if (!PyArg_ParseTuple(args, "O", &b)) return NULL;
    Py_ssize_t sz = PyBytes_Size(b);
    if (sz < 0) return NULL;
    char *s = PyBytes_AsString(b);
    (void)s;
    return PyLong_FromSsize_t(sz);
}

// call_test(callable, arg) -> callable(arg) via PyObject_Call
static PyObject *call_test(PyObject *self, PyObject *args) {
    (void)self;
    PyObject *callable, *arg;
    if (!PyArg_ParseTuple(args, "OO", &callable, &arg)) return NULL;
    PyObject *tup = PyTuple_Pack(1, arg);
    if (!tup) return NULL;
    PyObject *res = PyObject_Call(callable, tup, NULL);
    Py_DECREF(tup);
    return res;
}

// kw_add(a, b=10) via ParseTupleAndKeywords
static PyObject *kw_add(PyObject *self, PyObject *args, PyObject *kwargs) {
    (void)self;
    long a = 0, b = 10;
    static char *kwlist[] = {"a", "b", NULL};
    if (!PyArg_ParseTupleAndKeywords(args, kwargs, "l|l", kwlist, &a, &b)) return NULL;
    return PyLong_FromLong(a + b);
}

// churn() -> creates/destroys many temp objects in C (exercises _Py_Dealloc/free-list)
static PyObject *churn(PyObject *self, PyObject *args) {
    (void)self; (void)args;
    for (int i = 0; i < 1000; i++) {
        PyObject *o = PyLong_FromLong(i);
        if (!o) return NULL;
        long v = PyLong_AsLong(o);
        (void)v;
        Py_DECREF(o);
        PyObject *s = PyUnicode_FromString("temp");
        if (!s) return NULL;
        Py_DECREF(s);
    }
    return PyLong_FromLong(42);
}

static PyMethodDef methods[] = {
    {"hello", hello, METH_VARARGS, "return 42"},
    {"add", add, METH_VARARGS, "add two ints"},
    {"greet", greet, METH_VARARGS, "greeting"},
    {"make_dict", make_dict, METH_NOARGS, "make dict"},
    {"make_list", make_list, METH_NOARGS, "make list"},
    {"build_pair", build_pair, METH_VARARGS, "build pair"},
    {"fail", fail, METH_NOARGS, "raise"},
    {"float_test", float_test, METH_VARARGS, "double"},
    {"bytes_len", bytes_len, METH_VARARGS, "bytes len"},
    {"call_test", call_test, METH_VARARGS, "call"},
    {"kw_add", (PyCFunction)(void(*)(void))kw_add, METH_VARARGS | METH_KEYWORDS, "kwargs add"},
    {"churn", churn, METH_NOARGS, "churn temp objects"},
    {NULL, NULL, 0, NULL}
};

static struct PyModuleDef moduledef = {
    PyModuleDef_HEAD_INIT, "fython_testext", "test", -1, methods,
};

PyMODINIT_FUNC PyInit_fython_testext(void) {
    PyObject *m = PyModule_Create2(&moduledef, 0);
    if (!m) return NULL;
    if (PyModule_AddIntConstant(m, "VERSION", 1) < 0) { Py_DECREF(m); return NULL; }
    if (PyModule_AddStringConstant(m, "NAME", "fython") < 0) { Py_DECREF(m); return NULL; }
    return m;
}
