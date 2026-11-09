/*
 * Fython abi3 shim — pure C, no dependency on Python.h for its own build
 * (uses stdlib only). Exported symbols provide the Py_* entry points that
 * extensions expect from libpython/the host executable. Each forwards to a
 * Go handler installed via fython_abi3_bind().
 *
 * Build:
 *   gcc -fPIC -shared -o libfython_abi3.so shim/fython_abi3_shim.c
 *   clang -fPIC -shared -o libfython_abi3.dylib shim/fython_abi3_shim.c (darwin, no Python.h needed)
 *   gcc -shared -o python315.dll shim/fython_abi3_shim.c (windows via mingw)
 *
 * The shim deliberately does NOT include <Python.h> so it builds with zero
 * python3-devel installed. Type declarations below are minimal opaque
 * equivalents; actual C ABI (pointer width, int64 for Py_ssize_t) matches
 * CPython stable ABI on 64-bit.
 */
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "fython_abi3_shim.h"

// Opaque stubs for PyObject / PyTypeObject (stable layout: refcnt+type).
struct _fython_object { void *a, *b; };
struct _fython_typeobject { char dummy[512]; };

// Dispatch table
static void *dispatch[80] = {0};

int fython_abi3_bind(void **table, int n) {
    if (!table || n <= 0) return -1;
    if (n > 80) n = 80;
    for (int i = 0; i < n; i++) dispatch[i] = table[i];
    return 0;
}

// Data symbols (zero-initialized type objects) for extensions that reference
// &PyModule_Type etc. Go's PyType_IsSubtype handler translates these to Go types
// via arena shadow map; direct pointer equality via Py_IS_TYPE will compare against
// these static addresses, but TypeCheck goes through IsSubtype so it works.
// Provide several commonly referenced ones; add more as needed.
struct { char dummy[512]; } PyModule_Type;
struct { char dummy[512]; } PyType_Type;
struct { char dummy[512]; } PyLong_Type;
struct { char dummy[512]; } PyFloat_Type;
struct { char dummy[512]; } PyUnicode_Type;
struct { char dummy[512]; } PyBytes_Type;
struct { char dummy[512]; } PyTuple_Type;
struct { char dummy[512]; } PyList_Type;
struct { char dummy[512]; } PyDict_Type;
struct { char dummy[512]; } PyCFunction_Type;
struct { char dummy[512]; } PyComplex_Type;
struct { char dummy[512]; } PyBool_Type;
struct { char dummy[512]; } _Py_NoneStruct;
struct { char dummy[512]; } _Py_TrueStruct;
struct { char dummy[512]; } _Py_FalseStruct;
struct { char dummy[512]; } _Py_NotImplementedStruct;
struct { char dummy[512]; } _Py_EllipsisStruct;
// Exception type variables (PyObject* variables holding exception type objects).
// Go initializes these on BindToShim to point at shadow type objects.
void *PyExc_BaseException;
void *PyExc_Exception;
void *PyExc_ArithmeticError;
void *PyExc_AssertionError;
void *PyExc_AttributeError;
void *PyExc_BufferError;
void *PyExc_ImportError;
void *PyExc_IndexError;
void *PyExc_KeyError;
void *PyExc_LookupError;
void *PyExc_MemoryError;
void *PyExc_NameError;
void *PyExc_OverflowError;
void *PyExc_RuntimeError;
void *PyExc_SyntaxError;
void *PyExc_SystemError;
void *PyExc_TypeError;
void *PyExc_ValueError;
void *PyExc_ZeroDivisionError;
void *PyExc_OSError;
void *PyExc_StopIteration;
void *PyExc_KeyboardInterrupt;
void *PyExc_SystemExit;
void *PyExc_NotImplementedError;
void *PyExc_RecursionError;
void *PyExc_ReferenceError;
void *PyExc_FloatingPointError;
void *PyExc_UnboundLocalError;
void *PyExc_ModuleNotFoundError;

// Helper cast macros for handler types.
#define CALL0(ret_ft, idx) (((ret_ft)dispatch[idx]))
#define CHECK(idx) do { if (!dispatch[idx]) return (void)0; } while(0)

// --- PyLong ---
// Note: C long is 64-bit on linux/darwin, 32-bit on windows x64. Use intptr_t.
PyObject *PyLong_FromLong(long v) {
    if (!dispatch[0]) return NULL;
    return ((PyObject *(*)(long))dispatch[0])(v);
}
PyObject *PyLong_FromLongLong(long long v) {
    if (!dispatch[1]) return NULL;
    return ((PyObject *(*)(long long))dispatch[1])(v);
}
PyObject *PyLong_FromUnsignedLongLong(unsigned long long v) {
    if (!dispatch[2]) return NULL;
    return ((PyObject *(*)(unsigned long long))dispatch[2])(v);
}
PyObject *PyLong_FromSsize_t(Py_ssize_t v) {
    if (!dispatch[3]) return NULL;
    return ((PyObject *(*)(Py_ssize_t))dispatch[3])(v);
}
long PyLong_AsLong(PyObject *o) {
    if (!dispatch[4]) return -1;
    return ((long (*)(PyObject *))dispatch[4])(o);
}
long long PyLong_AsLongLong(PyObject *o) {
    if (!dispatch[5]) return -1;
    return ((long long (*)(PyObject *))dispatch[5])(o);
}
Py_ssize_t PyLong_AsSsize_t(PyObject *o) {
    if (!dispatch[6]) return -1;
    return ((Py_ssize_t (*)(PyObject *))dispatch[6])(o);
}
PyObject *PyBool_FromLong(long v) {
    if (!dispatch[7]) return NULL;
    return ((PyObject *(*)(long))dispatch[7])(v);
}

// --- Float / Complex ---
PyObject *PyFloat_FromDouble(double v) {
    if (!dispatch[8]) return NULL;
    return ((PyObject *(*)(double))dispatch[8])(v);
}
double PyFloat_AsDouble(PyObject *o) {
    if (!dispatch[9]) return -1.0;
    double out = -1.0;
    ((void (*)(PyObject *, double *))dispatch[9])(o, &out);
    return out;
}
PyObject *PyComplex_FromDoubles(double re, double im) {
    if (!dispatch[10]) return NULL;
    return ((PyObject *(*)(double,double))dispatch[10])(re, im);
}

// --- Strings / Bytes ---
PyObject *PyUnicode_FromString(const char *v) {
    if (!dispatch[11]) return NULL;
    return ((PyObject *(*)(const char *))dispatch[11])(v);
}
PyObject *PyUnicode_FromStringAndSize(const char *v, Py_ssize_t size) {
    if (!dispatch[12]) return NULL;
    return ((PyObject *(*)(const char *, Py_ssize_t))dispatch[12])(v, size);
}
const char *PyUnicode_AsUTF8(PyObject *o) {
    if (!dispatch[13]) return NULL;
    return ((const char *(*)(PyObject *))dispatch[13])(o);
}
const char *PyUnicode_AsUTF8AndSize(PyObject *o, Py_ssize_t *size) {
    if (!dispatch[14]) return NULL;
    return ((const char *(*)(PyObject *, Py_ssize_t *))dispatch[14])(o, size);
}
PyObject *PyBytes_FromStringAndSize(const char *v, Py_ssize_t size) {
    if (!dispatch[15]) return NULL;
    return ((PyObject *(*)(const char *, Py_ssize_t))dispatch[15])(v, size);
}
char *PyBytes_AsString(PyObject *o) {
    if (!dispatch[16]) return NULL;
    return ((char *(*)(PyObject *))dispatch[16])(o);
}
Py_ssize_t PyBytes_Size(PyObject *o) {
    if (!dispatch[17]) return -1;
    return ((Py_ssize_t (*)(PyObject *))dispatch[17])(o);
}

// --- Tuples ---
PyObject *PyTuple_New(Py_ssize_t size) {
    if (!dispatch[18]) return NULL;
    return ((PyObject *(*)(Py_ssize_t))dispatch[18])(size);
}
// Variadic Pack: collect varargs into array and forward to Go handler (n, arr).
PyObject *PyTuple_Pack(Py_ssize_t n, ...) {
    if (!dispatch[19]) return NULL;
    PyObject **arr = (PyObject **)malloc((size_t)n * sizeof(PyObject *));
    if (!arr && n > 0) return NULL;
    va_list ap; va_start(ap, n);
    for (Py_ssize_t i = 0; i < n; i++) arr[i] = va_arg(ap, PyObject *);
    va_end(ap);
    PyObject *res = ((PyObject *(*)(Py_ssize_t, PyObject **))dispatch[19])(n, arr);
    free(arr);
    return res;
}
PyObject *PyTuple_GetItem(PyObject *o, Py_ssize_t idx) {
    if (!dispatch[20]) return NULL;
    return ((PyObject *(*)(PyObject *, Py_ssize_t))dispatch[20])(o, idx);
}
int PyTuple_SetItem(PyObject *o, Py_ssize_t idx, PyObject *v) {
    if (!dispatch[21]) return -1;
    return ((int (*)(PyObject *, Py_ssize_t, PyObject *))dispatch[21])(o, idx, v);
}
Py_ssize_t PyTuple_Size(PyObject *o) {
    if (!dispatch[22]) return -1;
    return ((Py_ssize_t (*)(PyObject *))dispatch[22])(o);
}

// --- Lists ---
PyObject *PyList_New(Py_ssize_t size) {
    if (!dispatch[23]) return NULL;
    return ((PyObject *(*)(Py_ssize_t))dispatch[23])(size);
}
int PyList_Append(PyObject *o, PyObject *v) {
    if (!dispatch[24]) return -1;
    return ((int (*)(PyObject *, PyObject *))dispatch[24])(o, v);
}
PyObject *PyList_GetItem(PyObject *o, Py_ssize_t idx) {
    if (!dispatch[25]) return NULL;
    return ((PyObject *(*)(PyObject *, Py_ssize_t))dispatch[25])(o, idx);
}
int PyList_SetItem(PyObject *o, Py_ssize_t idx, PyObject *v) {
    if (!dispatch[26]) return -1;
    return ((int (*)(PyObject *, Py_ssize_t, PyObject *))dispatch[26])(o, idx, v);
}
Py_ssize_t PyList_Size(PyObject *o) {
    if (!dispatch[27]) return -1;
    return ((Py_ssize_t (*)(PyObject *))dispatch[27])(o);
}

// --- Dicts ---
PyObject *PyDict_New(void) {
    if (!dispatch[28]) return NULL;
    return ((PyObject *(*)(void))dispatch[28])();
}
int PyDict_SetItemString(PyObject *o, const char *k, PyObject *v) {
    if (!dispatch[29]) return -1;
    return ((int (*)(PyObject *, const char *, PyObject *))dispatch[29])(o, k, v);
}
PyObject *PyDict_GetItemString(PyObject *o, const char *k) {
    if (!dispatch[30]) return NULL;
    return ((PyObject *(*)(PyObject *, const char *))dispatch[30])(o, k);
}
int PyDict_SetItem(PyObject *o, PyObject *k, PyObject *v) {
    if (!dispatch[31]) return -1;
    return ((int (*)(PyObject *, PyObject *, PyObject *))dispatch[31])(o, k, v);
}
PyObject *PyDict_GetItemWithError(PyObject *o, PyObject *k) {
    if (!dispatch[32]) return NULL;
    return ((PyObject *(*)(PyObject *, PyObject *))dispatch[32])(o, k);
}
int PyDict_Next(PyObject *o, Py_ssize_t *pos, PyObject **k, PyObject **v) {
    if (!dispatch[33]) return 0;
    return ((int (*)(PyObject *, Py_ssize_t *, PyObject **, PyObject **))dispatch[33])(o, pos, k, v);
}

// --- Callables ---
PyObject *PyObject_Call(PyObject *c, PyObject *a, PyObject *k) {
    if (!dispatch[34]) return NULL;
    return ((PyObject *(*)(PyObject *, PyObject *, PyObject *))dispatch[34])(c, a, k);
}
PyObject *PyObject_CallNoArgs(PyObject *c) {
    if (!dispatch[35]) return NULL;
    return ((PyObject *(*)(PyObject *))dispatch[35])(c);
}
PyObject *PyObject_Vectorcall(PyObject *c, PyObject *const *args, size_t nargsf, PyObject *kwnames) {
    if (!dispatch[36]) return NULL;
    return ((PyObject *(*)(PyObject *, PyObject *const *, Py_ssize_t, PyObject *))dispatch[36])(c, args, (Py_ssize_t)nargsf, kwnames);
}
PyObject *PyObject_GetAttrString(PyObject *o, const char *name) {
    if (!dispatch[37]) return NULL;
    return ((PyObject *(*)(PyObject *, const char *))dispatch[37])(o, name);
}
int PyObject_SetAttrString(PyObject *o, const char *name, PyObject *v) {
    if (!dispatch[38]) return -1;
    return ((int (*)(PyObject *, const char *, PyObject *))dispatch[38])(o, name, v);
}

// --- Arg parsing (variadic: shim collects varargs into array of MAX=16 void*) ---
// Forward to Go handlers with same signature as registry (args, fmt, outArr).
int PyArg_ParseTuple(PyObject *args, const char *fmt, ...) {
    if (!dispatch[39]) return 0;
    void *outArr[16] = {0};
    va_list ap; va_start(ap, fmt);
    for (int i = 0; i < 16; i++) outArr[i] = va_arg(ap, void *);
    va_end(ap);
    return ((int (*)(PyObject *, const char *, void *))dispatch[39])(args, fmt, outArr);
}
int PyArg_ParseTupleAndKeywords(PyObject *args, PyObject *kwargs, const char *fmt, const char *const *kwlist, ...) {
    if (!dispatch[40]) return 0;
    void *outArr[16] = {0};
    va_list ap; va_start(ap, kwlist);
    for (int i = 0; i < 16; i++) outArr[i] = va_arg(ap, void *);
    va_end(ap);
    return ((int (*)(PyObject *, PyObject *, const char *, const char *const *, void *))dispatch[40])(args, kwargs, fmt, kwlist, outArr);
}
int PyArg_UnpackTuple(PyObject *args, const char *fmt, ...) {
    if (!dispatch[41]) return 0;
    void *outArr[16] = {0};
    va_list ap; va_start(ap, fmt);
    for (int i = 0; i < 16; i++) outArr[i] = va_arg(ap, void *);
    va_end(ap);
    return ((int (*)(PyObject *, const char *, void *))dispatch[41])(args, fmt, outArr);
}

// --- Py_BuildValue (variadic typed args) ---
// Format units and va types (CPython):
//  i,l,k,n,N -> long/int (promoted to int/long long?), PyLong,
//  O,N,S,T -> PyObject*, f,d -> double, s,z,y,Y -> char*, u -> PyObject*?
// Shim parses format and extracts each arg with the correct va type, storing into BuildArg array.
struct BuildArg { int tag; int _pad; uint64_t bits; void *ptr; double d; }; // 32 bytes
enum { BA_INT, BA_UINT, BA_DOUBLE, BA_OBJ, BA_STR };

PyObject *Py_BuildValue(const char *fmt, ...)
{
    if (!dispatch[42] || !fmt) return NULL;
    struct BuildArg args[32];
    uint64_t count = 0;
    va_list ap; va_start(ap, fmt);
    for (const char *p = fmt; *p && count < 32; p++) {
        switch (*p) {
            case 'i': case 'l': {
                long v = va_arg(ap, long);
                args[count].tag = BA_INT; args[count].bits = (uint64_t)(int64_t)v; args[count].ptr = NULL; args[count].d = 0; count++; break;
            }
            case 'I': case 'L': case 'k': case 'n': case 'N': {
                // N steals, treat as OBJ; but for int units k/L/I/n/N: distinguished by Go format parsing, shim just stores bits
                // 'N' overlaps: in BuildValue, 'N' means PyObject* stolen. Go dispatch distinguishes by format, not tag for N vs O? To keep simple, treat all as bits/ptr based on format char in Go.
                // For 'N' as object, it's pointer:
                // Disambiguate: 'N' could be Py_ssize_t int or stolen object. CPython docs: 'N' = steal PyObject*. So pointer.
                // Similarly 'n' is Py_ssize_t int, 'k' unsigned long long.
                if (*p == 'N') { // stolen object
                    void *v = va_arg(ap, void*);
                    args[count].tag = BA_OBJ; args[count].bits = 0; args[count].ptr = v; args[count].d = 0; count++;
                    break;
                }
                // For unsigned, read as unsigned long long
                unsigned long long v = va_arg(ap, unsigned long long);
                args[count].tag = BA_UINT; args[count].bits = (uint64_t)v; args[count].ptr = NULL; args[count].d = 0; count++; break;
            }
            case 'f': case 'd': {
                double v = va_arg(ap, double);
                args[count].tag = BA_DOUBLE; args[count].bits = 0; args[count].ptr = NULL; args[count].d = v; count++; break;
            }
            case 'O': case 'S': case 'T': {
                void *v = va_arg(ap, void*);
                args[count].tag = BA_OBJ; args[count].bits = 0; args[count].ptr = v; args[count].d = 0; count++; break;
            }
            case 's': case 'z': case 'y': case 'Y': {
                char *v = va_arg(ap, char*);
                args[count].tag = BA_STR; args[count].bits = 0; args[count].ptr = v; args[count].d = 0; count++; break;
            }
            case 'u': {
                void *v = va_arg(ap, void*);
                args[count].tag = BA_OBJ; args[count].bits = 0; args[count].ptr = v; args[count].d = 0; count++; break;
            }
            default: break; // '(' ')' '[' ']' '{' '}' ';' ':' ',' ' ' '%' are structural
        }
    }
    va_end(ap);
    return ((PyObject *(*)(const char *, struct BuildArg *, uint64_t))dispatch[42])(fmt, args, count);
}

// --- Module / Capsule ---
PyObject *PyModule_Create2(PyModuleDef *def, int state) {
    if (!dispatch[43]) return NULL;
    // dispatch handler signature: func(def unsafe.Pointer, _ int32) unsafe.Pointer ; state is int.
    return ((PyObject *(*)(PyModuleDef *, int))dispatch[43])(def, state);
}
PyModuleDef *PyModuleDef_Init(PyModuleDef *def) {
    if (!dispatch[44]) return NULL;
    return ((PyModuleDef *(*)(PyModuleDef *))dispatch[44])(def);
}
int PyModule_AddObject(PyObject *m, const char *name, PyObject *v) {
    if (!dispatch[45]) return -1;
    return ((int (*)(PyObject *, const char *, PyObject *))dispatch[45])(m, name, v);
}
int PyModule_AddObjectRef(PyObject *m, const char *name, PyObject *v) {
    if (!dispatch[46]) return -1;
    return ((int (*)(PyObject *, const char *, PyObject *))dispatch[46])(m, name, v);
}
int PyModule_AddIntConstant(PyObject *m, const char *name, long v) {
    if (!dispatch[47]) return -1;
    return ((int (*)(PyObject *, const char *, long))dispatch[47])(m, name, v);
}
int PyModule_AddStringConstant(PyObject *m, const char *name, const char *v) {
    if (!dispatch[48]) return -1;
    return ((int (*)(PyObject *, const char *, const char *))dispatch[48])(m, name, v);
}
void *PyModule_GetState(PyObject *m) {
    if (!dispatch[49]) return NULL;
    return ((void *(*)(PyObject *))dispatch[49])(m);
}
PyObject *PyCapsule_New(void *ptr, const char *name, void (*dtor)(PyObject *)) {
    if (!dispatch[50]) return NULL;
    return ((PyObject *(*)(void *, const char *, void *))dispatch[50])(ptr, name, (void *)dtor);
}
void *PyCapsule_GetPointer(PyObject *caps, const char *name) {
    if (!dispatch[51]) return NULL;
    return ((void *(*)(PyObject *, const char *))dispatch[51])(caps, name);
}
PyObject *PyCapsule_Import(const char *name, int no_inherit) {
    if (!dispatch[52]) return NULL;
    return ((PyObject *(*)(const char *, int))dispatch[52])(name, no_inherit);
}

// --- Errors / GIL ---
void PyErr_SetString(PyObject *exctype, const char *msg) {
    if (!dispatch[53]) return;
    ((void (*)(PyObject *, const char *))dispatch[53])(exctype, msg);
}
void PyErr_SetObject(PyObject *exctype, PyObject *value) {
    if (!dispatch[54]) return;
    ((void (*)(PyObject *, PyObject *))dispatch[54])(exctype, value);
}
PyObject *PyErr_Occurred(void) {
    if (!dispatch[55]) return NULL;
    return ((PyObject *(*)(void))dispatch[55])();
}
void PyErr_Clear(void) {
    if (!dispatch[56]) return;
    ((void (*)(void))dispatch[56])();
}
void PyErr_Format(PyObject *exctype, const char *format, ...) {
    if (!dispatch[57]) return;
    char buf[1024];
    va_list ap; va_start(ap, format);
    vsnprintf(buf, sizeof(buf), format, ap);
    va_end(ap);
    ((void (*)(PyObject *, const char *))dispatch[57])(exctype, buf);
}
int PyGILState_Ensure(void) {
    if (!dispatch[58]) return 0;
    return ((int (*)(void))dispatch[58])();
}
void PyGILState_Release(int state) {
    if (!dispatch[59]) return;
    ((void (*)(int))dispatch[59])(state);
}
void *PyEval_SaveThread(void) {
    if (!dispatch[60]) return NULL;
    return ((void *(*)(void))dispatch[60])();
}
void PyEval_RestoreThread(void *state) {
    if (!dispatch[61]) return;
    ((void (*)(void *))dispatch[61])(state);
}
void _Py_Dealloc(PyObject *o) {
    if (!dispatch[62]) return;
    ((void (*)(PyObject *))dispatch[62])(o);
}
int PyType_IsSubtype(PyTypeObject *a, PyTypeObject *b) {
    if (!dispatch[63]) return 0;
    return ((int (*)(PyTypeObject *, PyTypeObject *))dispatch[63])(a, b);
}
int PyType_Ready(PyTypeObject *t) {
    if (!dispatch[64]) return 0;
    return ((int (*)(PyTypeObject *))dispatch[64])(t);
}
PyTypeObject *PyObject_Type(PyObject *o) {
    if (!dispatch[65]) return NULL;
    return ((PyTypeObject *(*)(PyObject *))dispatch[65])(o);
}
PyObject *PyCFunction_NewEx(void *ml, PyObject *self, PyObject *module) {
    if (!dispatch[66]) return NULL;
    return ((PyObject *(*)(void *, PyObject *, PyObject *))dispatch[66])(ml, self, module);
}
void PyErr_NormalizeException(PyObject **e, PyObject **v, PyObject **t) {
    if (!dispatch[67]) return;
    ((void (*)(PyObject **, PyObject **, PyObject **))dispatch[67])(e, v, t);
}
PyObject *PyErr_GetCause(void) {
    if (!dispatch[68]) return NULL;
    return ((PyObject *(*)(void))dispatch[68])();
}
PyObject *PyErr_GetContext(void) {
    if (!dispatch[69]) return NULL;
    return ((PyObject *(*)(void))dispatch[69])();
}

// --- Milestone 2: custom types + buffer protocol ---
typedef struct {
    const char *name;
    int basicsize;
    int itemsize;
    unsigned int flags;
    void *slots;
} FythonPyTypeSpec;
PyObject *PyType_FromSpec(FythonPyTypeSpec *spec) {
    if (!dispatch[70]) return NULL;
    return ((PyObject *(*)(void *))dispatch[70])(spec);
}
PyObject *PyType_FromSpecWithBases(FythonPyTypeSpec *spec, PyObject *bases) {
    if (!dispatch[71]) return NULL;
    return ((PyObject *(*)(void *, PyObject *))dispatch[71])(spec, bases);
}
PyObject *PyType_FromModuleAndSpec(PyObject *module, FythonPyTypeSpec *spec, PyObject *dict) {
    if (!dispatch[72]) return NULL;
    return ((PyObject *(*)(PyObject *, void *, PyObject *))dispatch[72])(module, spec, dict);
}
int PyObject_GetBuffer(PyObject *obj, void *view, int flags) {
    if (!dispatch[73]) return -1;
    return ((int (*)(PyObject *, void *, int))dispatch[73])(obj, view, flags);
}
void PyBuffer_Release(void *view) {
    if (!dispatch[74]) return;
    ((void (*)(void *))dispatch[74])(view);
}
int PyObject_CheckBuffer(PyObject *obj) {
    if (!dispatch[75]) return 0;
    return ((int (*)(PyObject *))dispatch[75])(obj);
}
Py_ssize_t PyBuffer_SizeFromFormat(const char *format) {
    if (!dispatch[76]) return -1;
    return ((Py_ssize_t (*)(const char *))dispatch[76])(format);
}
void PyBuffer_FillContiguousStrides(int ndims, Py_ssize_t *shape, Py_ssize_t *strides, int itemsize, char order) {
    if (!dispatch[77]) return;
    ((void (*)(int, Py_ssize_t *, Py_ssize_t *, int, char))dispatch[77])(ndims, shape, strides, itemsize, order);
}
int PyBuffer_FillInfo(void *view, PyObject *o, void *buf, Py_ssize_t len, int readonly, int flags) {
    if (!dispatch[78]) return -1;
    return ((int (*)(void *, PyObject *, void *, Py_ssize_t, int, int))dispatch[78])(view, o, buf, len, readonly, flags);
}
int PyBuffer_IsContiguous(const void *view, char order) {
    if (!dispatch[79]) return 0;
    return ((int (*)(const void *, char))dispatch[79])(view, order);
}

// Additional stubs for robustness (not in dispatch table): return NULL with no crash.
PyObject *PyUnicode_FromFormat(const char *fmt, ...) { return NULL; }
PyObject *PyObject_Repr(PyObject *o) { return NULL; }
PyObject *PyObject_Str(PyObject *o) { return NULL; }
int PyErr_WarnEx(PyObject *c, PyObject *m, int s) { return 0; }
void *PyMem_Malloc(size_t n) { return malloc(n); }
void *PyMem_Realloc(void *p, size_t n) { return realloc(p, n); }
void PyMem_Free(void *p) { free(p); }
PyObject *PyEval_EvalCodeEx(void *c, void *g, void *l, PyObject *a, int na, PyObject *k, int nk) { return NULL; }
