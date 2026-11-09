# fython

A CPython 3.15-compatible interpreter written in Go.

## Highlights

* **Drop-in `python3` CLI:** Run scripts, inline commands (`-c`), modules (`-m`), and post-mortem interactive sessions (`-i`) with existing flags and muscle memory.
* **Modern interactive REPL:** Built-in syntax highlighting, multiline block editing, smart tab completion, and persistent history out of the box.
* **Zero-config `uv` & virtualenv support:** Automatically detects `.venv` environments, `site-packages`, and editable installs—plus imports `.whl` and `.zip` archives directly without unpacking.
* **Native C & Rust extensions:** Loads compiled `.so` and `abi3` extension modules alongside pure-Python packages, all from a `CGO_ENABLED=0` Go binary.
* **Single-binary compiler (`fython build`):** Package any Python application and its wheel dependencies into a self-contained native executable in milliseconds—no Go or C toolchain required.
* **Built-in multi-core web server (`fython serve`):** Serve ASGI 3.0 and WSGI web applications directly on Go's HTTP engine across all CPU cores without needing `uvicorn` or `gunicorn`.
* **Cross-platform:** Runs natively on Linux, macOS, and Windows.4

## Installation

From source:

```console
$ go install github.com/servusdei2018/fython/cmd/fython@latest
```

Or build locally:

```console
$ make build
$ ./bin/fython --version
Fython 3.15.0
```

Benchmarks live in [`benchmarks/`](benchmarks/) — `python3 benchmarks/run_comparison.py` builds `fython` and compares it against `python3` on six workloads (OOP, I/O,  N-Queens, asyncio, `memoryview`, JSON).

## Features

### Run Python code

Drop-in replacement for the common `python` invocation modes —
scripts, `-c` strings, `-m` modules, stdin:

```console
$ fython example.py
[0, 1, 4, 9, 16]

$ fython -c 'print("hello", 1 + 2)'
hello 3

$ echo 'print("from stdin")' | fython -
from stdin
```

### Interactive REPL

Start the interpreter with no arguments. `PYTHONSTARTUP`, `PYTHON_BASIC_REPL`,
and `-q` follow CPython conventions:

```console
$ printf 'x = 41\nprint(x + 1)\nexit\n' | fython
42
```

### Wheels and packages

Use a wheel straight from `sys.path`:

```console
$ fython -c '
import sys
sys.path.insert(0, "pkg/vm/testdata/wheels/packaging-26.3-py3-none-any.whl")
from packaging.version import Version
print(Version("3.15.0"))
'
3.15.0
```

The same mechanism works for `.zip`/`.egg` files, zipapps, and
`.venv` environments (`site-packages`, `.pth`, `pyvenv.cfg`).

### Standalone binaries

Compile an app plus its wheels into one executable:

```console
$ fython build example.py -o example --wheel demo.whl
Successfully built standalone binary example

$ ./example
[0, 1, 4, 9, 16]
```

### HTTP server

Run an ASGI or WSGI app with `GOMAXPROCS` worker VMs by default:

```console
$ fython serve path/to/app.py:app --port 8000
Fython 3.15.0 server started on http://127.0.0.1:8000 (ASGI 3.0, workers: 12)
```

## Contributing

We love open source devs! You don't need our permission to start on an open issue, but it's a good idea to indicate that you are going to work on an issue to avoid concurrent attempts to solve the same problem. Please do not open pull requests for new features without prior discussion. 

## FAQ

#### What is fython?

A Python 3.15 interpreter implemented in Go. The pure-Python stdlib modules
under `pkg/stdlib/lib/` are vendored from CPython `v3.15.0` (see `pkg/stdlib/NOTICE`),
while the interpreter core, compiler, and C-extension modules are written in Go.

#### What platforms does fython support?

Linux, macOS, and Windows. The core build keeps `CGO_ENABLED=0`; the abi3
shim is built per-platform (`make shim`, `make shim-darwin`, `make shim-windows`).

#### How do I report issues or contribute?

Open an issue or pull request against this repository. Please include the
Python snippet, the `fython` invocation used, and `fython -V` output.

AI should not be used to generate comments when communicating with maintainers. If you are opening an issue, we expect you to describe the problem in your own words.

## Acknowledgements

fython's CLI is powered by [Kong](https://github.com/alecthomas/kong), its REPL
by [Bubble Tea](https://github.com/charmbracelet/bubbletea), and
[`purego`](https://github.com/ebitengine/purego) for loading the abi3 shim.
The stdlib sources under `pkg/stdlib/lib/` are from CPython (PSF License;
see `pkg/stdlib/lib/LICENSE`).

## License

fython is licensed under the MIT License; see [LICENSE](LICENSE). The vendored CPython stdlib retains its original PSF/CNRI license terms.
