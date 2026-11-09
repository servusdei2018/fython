#define PY_SSIZE_T_CLEAN
#include <Python.h>

// Multi-phase module via PyModuleDef_Init + Py_mod_exec

static PyObject *hello_multi(PyObject *self, PyObject *args) {
    (void)self; (void)args;
    return PyLong_FromLong(43);
}

static PyMethodDef methods[] = {
    {"hello", hello_multi, METH_NOARGS, NULL},
    {NULL, NULL, 0, NULL}
};

static int exec_module(PyObject *m) {
    if (PyModule_AddIntConstant(m, "VERSION", 2) < 0) return -1;
    if (PyModule_AddStringConstant(m, "NAME", "fython_multi") < 0) return -1;
    return 0;
}

static struct PyModuleDef_Slot slots[] = {
    {Py_mod_exec, exec_module},
    {0, NULL}
};

static struct PyModuleDef moduledef = {
    PyModuleDef_HEAD_INIT,
    "fython_testext_multi", "multi test", -1, methods, slots,
};

PyMODINIT_FUNC PyInit_fython_testext_multi(void) {
    return PyModuleDef_Init(&moduledef);
}
