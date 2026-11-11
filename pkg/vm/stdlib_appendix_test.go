package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// TestAppendixAModulesImport checks that every module of Appendix A of the
// architecture document can be imported.
func TestAppendixAModulesImport(t *testing.T) {
	modules := []string{
		"zoneinfo", "array", "mmap", "fcntl", "grp", "pwd", "termios",
		"tty", "pty", "resource", "syslog", "multiprocessing", "statistics",
		"tracemalloc",
	}
	for _, m := range modules {
		t.Run(m, func(t *testing.T) {
			runSnippet(t, "import "+m)
		})
	}
}

// TestAppendixAArray checks the typed array module.
func TestAppendixAArray(t *testing.T) {
	runSnippet(t, `
import array
for code in "bBhHiIlLqQfduw":
    a = array.array(code)
    assert a.typecode == code, (a.typecode, code)
    assert a.itemsize > 0
    assert len(a) == 0
    a.append(1)
    a.extend([2, 3])
    assert len(a) == 3, len(a)
    assert a.tolist() == [1, 2, 3], a.tolist()
    assert a[0] == 1
    assert isinstance(a.tobytes(), bytes)
    assert a.buffer_info()[0] == 3 * a.itemsize
`)
}

// TestAppendixAMmap checks the memory mapped file module.
func TestAppendixAMmap(t *testing.T) {
	runSnippet(t, `
import mmap
import os
path = os.path.join(os.environ.get("TMPDIR", "/tmp"), "fython_mmap_test.bin")
f = open(path, "w+b")
f.write(b"hello world")
f.flush()
m = mmap.mmap(f.fileno(), 11)
try:
    assert len(m) == 11, len(m)
    assert m[0:5] == b"hello", m[0:5]
    m.seek(0)
    assert m.read(5) == b"hello"
    m.seek(0)
    m.write(b"HELLO")
    m.seek(0)
    assert m.read(5) == b"HELLO"
finally:
    m.close()
    f.close()
    os.unlink(path)
`)
}

// TestAppendixAPosix checks the POSIX interface modules.
func TestAppendixAPosix(t *testing.T) {
	runSnippet(t, `
import fcntl
import grp
import pwd
import resource
import termios

assert termios.IFLAG == 0
assert termios.CC == 6
assert termios.ECHO > 0
assert fcntl.F_GETFL > 0
assert fcntl.LOCK_EX > 0
assert resource.RLIMIT_NOFILE > 0
assert pwd.getpwuid(0)[0] == "root"
assert grp.getgrgid(0)[0] == "root"
`)
}

// TestAppendixAZoneInfo checks the time zone database module.
func TestAppendixAZoneInfo(t *testing.T) {
	runSnippet(t, `
import datetime
import zoneinfo

z = zoneinfo.ZoneInfo("Europe/Berlin")
assert z.key == "Europe/Berlin", z.key
assert z.utcoffset(datetime.datetime(2024, 1, 15, 12, 0, 0)).total_seconds() == 3600
assert z.utcoffset(datetime.datetime(2024, 7, 15, 12, 0, 0)).total_seconds() == 7200
assert z.tzname(datetime.datetime(2024, 7, 15, 12, 0, 0)) == "CEST"
`)
}

// TestAppendixAStatistics checks the statistics module.
func TestAppendixAStatistics(t *testing.T) {
	runSnippet(t, `
import math
import statistics

assert math.erfc(0.0) == 1.0
assert abs(math.erfc(1.0) - 0.15729920705028513) < 1e-12
assert math.fsum([0.1] * 10) == 1.0
assert math.sumprod([1, 2, 3], [4, 5, 6]) == 32
assert statistics.mean([1, 2, 3, 4]) == 2.5
assert statistics.median([3, 1, 2]) == 2
`)
}

// TestAppendixATracemalloc checks the memory tracing accelerator.
func TestAppendixATracemalloc(t *testing.T) {
	runSnippet(t, `
import tracemalloc

assert tracemalloc.is_tracing() == False
tracemalloc.start(5)
assert tracemalloc.is_tracing() == True
assert tracemalloc.get_traceback_limit() == 5
current, peak = tracemalloc.get_traced_memory()
assert current > 0 and peak > 0
tracemalloc.stop()
assert tracemalloc.is_tracing() == False
`)
}

// TestAppendixAMultiprocessing checks the process module.
func TestAppendixAMultiprocessing(t *testing.T) {
	runSnippet(t, `
import multiprocessing

assert multiprocessing.cpu_count() > 0
q = multiprocessing.Queue()
q.put(7)
assert q.get() == 7
assert multiprocessing.Pool(2).map(abs, [-1, -2, -3]) == [1, 2, 3]
assert multiprocessing.Pool(2).apply(abs, (-5,)) == 5
`)
}

// TestMemoryViewRecyclingKeepsEveryViewAlive checks that recycling the memory
// views does not overwrite the views that are still referenced.
func TestMemoryViewRecyclingKeepsEveryViewAlive(t *testing.T) {
	runSnippet(t, `
raw = bytearray(200000)
for i in range(200):
    raw[i * 100] = (i % 250) + 1
mv = memoryview(raw)
keep = [mv[i * 100:i * 100 + 1] for i in range(2000)]
assert keep[0].tobytes()[0] == 1, keep[0].tobytes()[0]
assert keep[1].tobytes()[0] == 2, keep[1].tobytes()[0]
assert keep[1999].tobytes()[0] == 0, keep[1999].tobytes()[0]
`)
}

// TestMemoryViewRecyclingReusesOneView checks that the slicing loop of the
// binary benchmark does not allocate a view per iteration.
func TestMemoryViewRecyclingReusesOneView(t *testing.T) {
	src := `
def make():
    return memoryview(bytearray(b"x" * 4096))

def slice_loop(mv, n):
    total = 0
    for i in range(n):
        sub = mv[i % 1024:i % 1024 + 16]
        total += len(sub)
    return total
`
	e := vm.NewEngine()
	e.SetCompileHook(compiler.CompileSource)
	v := e.AcquireVM()
	defer e.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(src), "<recycle>")
	if err != nil {
		t.Fatalf("compilation error: %v", err)
	}
	v.RunCode(co)
	mk, ok := v.GetGlobal("make")
	if !ok {
		t.Fatal("make not found")
	}
	sl, ok := v.GetGlobal("slice_loop")
	if !ok {
		t.Fatal("slice_loop not found")
	}
	mvVal := v.Vectorcall(mk, nil, nil)
	allocs := testing.AllocsPerRun(10, func() {
		v.Vectorcall(sl, []val.Value{mvVal, val.Int64Val(1000)}, nil)
	})
	if allocs > 2 {
		t.Fatalf("the slicing loop allocated %v objects per call, want at most 2", allocs)
	}
}
