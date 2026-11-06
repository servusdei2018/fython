import ast, sys, json, struct, math

def enc_const(v):
    if v is None: return {"t": "none"}
    if v is Ellipsis: return {"t": "ellipsis"}
    if isinstance(v, bool): return {"t": "bool", "v": v}
    if isinstance(v, int): return {"t": "int", "v": str(v)}
    if isinstance(v, float): return {"t": "float", "v": struct.pack(">d", v).hex()}
    if isinstance(v, complex): return {"t": "complex", "re": struct.pack(">d", v.real).hex(), "im": struct.pack(">d", v.imag).hex()}
    if isinstance(v, str): return {"t": "str", "v": v}
    if isinstance(v, bytes): return {"t": "bytes", "v": v.hex()}
    raise TypeError(type(v))

def enc(n):
    if isinstance(n, ast.AST):
        d = {"_": type(n).__name__}
        for f in n._fields:
            v = getattr(n, f, None)
            if isinstance(n, ast.Constant) and f == "value":
                d[f] = enc_const(v)
            else:
                d[f] = enc(v)
        for a in n._attributes:
            v = getattr(n, a, None)
            if v is not None:
                d[a] = v
        return d
    if isinstance(n, list):
        return [enc(x) for x in n]
    return n

for path in sys.argv[1:]:
    mode = "exec"
    if path.startswith("@"):  # inline snippet file: @mode:path
        mode, path = path[1:].split(":", 1)
    try:
        src = open(path, "rb").read()
        t = ast.parse(src, path, mode=mode)
        out = {"path": path, "tree": enc(t)}
    except SyntaxError as e:
        out = {"path": path, "error": str(e.msg), "lineno": e.lineno}
    except Exception as e:
        out = {"path": path, "error": "EXC " + repr(e)}
    sys.stdout.write(json.dumps(out) + "\n")
