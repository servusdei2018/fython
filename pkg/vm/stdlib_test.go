package vm_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runPython(t *testing.T, src string) *vm.VM {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	engine := vm.NewEngine()
	v := engine.AcquireVM()
	v.RunCode(co)
	return v
}

func TestNativeJSON(t *testing.T) {
	src := `
import json

data = json.loads('{"id": 42, "name": "fython", "active": true, "score": 98.5, "tags": ["fast", "zero-copy"]}')
if data["id"] != 42:
    raise ValueError("id mismatch")
if data["name"] != "fython":
    raise ValueError("name mismatch")
if data["active"] != True:
    raise ValueError("active mismatch")
if data["score"] != 98.5:
    raise ValueError("score mismatch")
if len(data["tags"]) != 2:
    raise ValueError("tags len mismatch")
if data["tags"][0] != "fast":
    raise ValueError("tags[0] mismatch")
if data["tags"][1] != "zero-copy":
    raise ValueError("tags[1] mismatch")

dumped = json.dumps(data)
data2 = json.loads(dumped)
if data2["id"] != 42 or data2["name"] != "fython":
    raise ValueError("roundtrip mismatch")
`
	runPython(t, src)
}

func TestNativeMath(t *testing.T) {
	src := `
import math

if math.pi <= 3.14 or math.pi >= 3.15:
    raise ValueError("pi")
if math.e <= 2.71 or math.e >= 2.72:
    raise ValueError("e")
if math.sqrt(16.0) != 4.0:
    raise ValueError("sqrt")
if math.sin(0.0) != 0.0:
    raise ValueError("sin")
if math.cos(0.0) != 1.0:
    raise ValueError("cos")
if math.pow(2.0, 3.0) != 8.0:
    raise ValueError("pow")
if math.floor(3.7) != 3.0:
    raise ValueError("floor")
if math.ceil(3.2) != 4.0:
    raise ValueError("ceil")
if math.gcd(48, 18) != 6:
    raise ValueError("gcd")
if math.factorial(5) != 120:
    raise ValueError("factorial")
if not math.isinf(math.inf):
    raise ValueError("isinf")
if not math.isnan(math.nan):
    raise ValueError("isnan")
if not math.isfinite(100.0):
    raise ValueError("isfinite")
`
	runPython(t, src)
}

func TestNativeTime(t *testing.T) {
	src := `
import time

t1 = time.time()
t_ns = time.time_ns()
if t1 <= 0 or t_ns <= 0:
    raise ValueError("time")

mono1 = time.monotonic()
time.sleep(0.005)
mono2 = time.monotonic()
if mono2 < mono1:
    raise ValueError("monotonic")
`
	runPython(t, src)
}

func TestNativeOSAndPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fython_os_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFile, []byte("hello fython"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	src := `
import os

p = os.path.join("` + filepath.ToSlash(tmpDir) + `", "test.txt")
if not os.path.exists(p):
    raise ValueError("exists")
if not os.path.isfile(p):
    raise ValueError("isfile")
if os.path.basename(p) != "test.txt":
    raise ValueError("basename")

sub = os.path.join("` + filepath.ToSlash(tmpDir) + `", "sub")
os.mkdir(sub)
if not os.path.isdir(sub):
    raise ValueError("isdir")
if not os.path.exists(sub):
    raise ValueError("sub exists")
os.rmdir(sub)
if os.path.exists(sub):
    raise ValueError("rmdir failed")
`
	runPython(t, src)
}

func TestNativeIOAndContextManager(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "fython_io_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFilePath := filepath.ToSlash(filepath.Join(tmpDir, "sample.txt"))

	src := `
import io

# Test BytesIO
bio = io.BytesIO(b"abc")
bio.write(b"def")
if bio.getvalue() != b"abcdef":
    raise ValueError("BytesIO")

# Test StringIO
sio = io.StringIO("hello ")
sio.write("world")
if sio.getvalue() != "hello world":
    raise ValueError("StringIO")

# Test File with context manager
with open("` + testFilePath + `", "w") as f:
    f.write("Line 1\nLine 2\n")

with open("` + testFilePath + `", "r") as f:
    content = f.read()
    if content != "Line 1\nLine 2\n":
        raise ValueError("file read: " + content)
`
	runPython(t, src)
}

func TestByteStringAndSlicingEndToEnd(t *testing.T) {
	src := `
b = b"0123456789"
if len(b) != 10:
    raise ValueError("len b")
if b[2:6] != b"2345":
    raise ValueError("b[2:6]")
if b[:4] != b"0123":
    raise ValueError("b[:4]")
if b[7:] != b"789":
    raise ValueError("b[7:]")
if b[::2] != b"02468":
    raise ValueError("b[::2]")

ba = bytearray(b"abcdef")
mv = memoryview(ba)
sub = mv[2:5]
sub[0] = 90  # 'Z'
if ba != bytearray(b"abZdef"):
    raise ValueError("in-place mutation failed")

tup = (10, 20, 30, 40, 50, 60)
sub_tup = tup[1:4]
if sub_tup != (20, 30, 40):
    raise ValueError("tuple slice")
if len(sub_tup) != 3:
    raise ValueError("tuple len")
`
	runPython(t, src)
}
