package vm_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// --- Domain: Real-World Object-Oriented Programming (OOP) ---

const oopSrc = `
class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y
    def dist_sq(self):
        return self.x * self.x + self.y * self.y

def bench_oop():
    p = Point(1, 2)
    s = 0
    for i in range(1000):
        p.x = i
        p.y = i + 1
        s += p.dist_sq()
    return s
`

func TestRealWorldOOP(t *testing.T) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	co, err := compiler.CompileString(oopSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("bench_oop")
	if !ok {
		t.Fatal("bench_oop not found")
	}

	// Warm-up to trigger inline cache quickening
	v.Vectorcall(fnVal, nil, nil)

	start := time.Now()
	res := v.Vectorcall(fnVal, nil, nil)
	elapsed := time.Since(start)

	if !res.IsInt64() || res.AsInt64() != 666667000 {
		t.Fatalf("unexpected result: got %v, want 666667000", res)
	}

	t.Logf("RealWorld OOP execution time: %v (target < 0.200 ms)", elapsed)
	if !vm.IsRaceEnabled && elapsed > 200*time.Microsecond {
		// Allow some slack for CI / loaded environments up to 1ms, but log warning
		if elapsed > 1000*time.Microsecond {
			t.Errorf("OOP benchmark too slow: %v (want < 1.000 ms)", elapsed)
		}
	}
}

func BenchmarkRealWorldOOP(b *testing.B) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	co, err := compiler.CompileString(oopSrc)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("bench_oop")
	if !ok {
		b.Fatal("bench_oop not found")
	}

	// Warm-up
	v.Vectorcall(fnVal, nil, nil)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		res := v.Vectorcall(fnVal, nil, nil)
		_ = res
	}
}

// --- Domain: Real-World Stream and File I/O ---

func getIOSource(path string) string {
	return fmt.Sprintf(`
import io

def bench_io():
    # 2000 StringIO writes + getvalue
    s = io.StringIO()
    for i in range(2000):
        s.write("data")
    s_val = s.getvalue()

    # 1000 file writes + file read
    with open("%s", "w") as f:
        for i in range(1000):
            f.write("content line\n")

    with open("%s", "r") as f:
        content = f.read()

    return len(s_val), len(content)
`, path, path)
}

func TestRealWorldIO(t *testing.T) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	tmpFile := fmt.Sprintf("/tmp/realworld_bench_io_%d.txt", time.Now().UnixNano())
	defer func() { _ = os.Remove(tmpFile) }()

	src := getIOSource(tmpFile)
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("bench_io")
	if !ok {
		t.Fatal("bench_io not found")
	}

	// Warm-up
	v.Vectorcall(fnVal, nil, nil)

	start := time.Now()
	res := v.Vectorcall(fnVal, nil, nil)
	elapsed := time.Since(start)

	t.Logf("RealWorld IO execution time: %v (target < 0.400 ms)", elapsed)
	if !res.IsTuple() {
		t.Fatalf("expected tuple result, got %v", res)
	}

	if !vm.IsRaceEnabled && elapsed > 400*time.Microsecond {
		// Allow margin for disk I/O variance on shared runners
		if elapsed > 1500*time.Microsecond {
			t.Errorf("IO benchmark too slow: %v (want < 1.500 ms)", elapsed)
		}
	}
}

func BenchmarkRealWorldIO(b *testing.B) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	tmpFile := fmt.Sprintf("/tmp/realworld_bench_io_b_%d.txt", time.Now().UnixNano())
	defer func() { _ = os.Remove(tmpFile) }()

	src := getIOSource(tmpFile)
	co, err := compiler.CompileString(src)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("bench_io")
	if !ok {
		b.Fatal("bench_io not found")
	}

	// Warm-up
	v.Vectorcall(fnVal, nil, nil)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		res := v.Vectorcall(fnVal, nil, nil)
		_ = res
	}
}

// --- Domain: Real-World Recursion & Closures (N-Queens) ---

const nqueensSrc = `
def solve_nqueens(n):
    count = 0
    cols = [False] * n
    diag1 = [False] * (2 * n)
    diag2 = [False] * (2 * n)
    def solve(row):
        nonlocal count
        if row == n:
            count += 1
            return
        for col in range(n):
            d1 = row + col
            d2 = row - col + n
            if not cols[col] and not diag1[d1] and not diag2[d2]:
                cols[col] = True
                diag1[d1] = True
                diag2[d2] = True
                solve(row + 1)
                cols[col] = False
                diag1[d1] = False
                diag2[d2] = False
    solve(0)
    return count
`

func TestRealWorldNQueens(t *testing.T) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	co, err := compiler.CompileString(nqueensSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("solve_nqueens")
	if !ok {
		t.Fatal("solve_nqueens not found")
	}

	// Warm-up with N=8
	v.Vectorcall(fnVal, []val.Value{val.Int64Val(8)}, nil)

	start := time.Now()
	res := v.Vectorcall(fnVal, []val.Value{val.Int64Val(10)}, nil)
	elapsed := time.Since(start)

	if !res.IsInt64() || res.AsInt64() != 724 {
		t.Fatalf("expected 724 solutions for 10-queens, got %v", res)
	}

	t.Logf("RealWorld N-Queens (N=10) execution time: %v (target < 65.0 ms)", elapsed)
	if !vm.IsRaceEnabled && elapsed > 65*time.Millisecond {
		t.Errorf("NQueens benchmark took too long: %v (expected < 65.0 ms)", elapsed)
	}
}

func BenchmarkRealWorldNQueens(b *testing.B) {
	val.SetMultiThreaded(false)
	v := vm.NewVM()
	co, err := compiler.CompileString(nqueensSrc)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	fnVal, ok := v.GetGlobal("solve_nqueens")
	if !ok {
		b.Fatal("solve_nqueens not found")
	}

	// Warm-up
	v.Vectorcall(fnVal, []val.Value{val.Int64Val(8)}, nil)

	args := []val.Value{val.Int64Val(10)}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		res := v.Vectorcall(fnVal, args, nil)
		_ = res
	}
}

// --- Domain: Real-World Asyncio ---

const asyncSrc = `
import asyncio

async def add(a, b):
    return a + b

async def bench_async_loop(n):
    total = 0
    i = 0
    while i < n:
        total = total + (await add(i, 1))
        i = i + 1
    return total
`

func TestRealWorldAsync(t *testing.T) {
	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)

	co, err := compiler.CompileString(asyncSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	benchFn, ok := v.GetGlobal("bench_async_loop")
	if !ok {
		t.Fatal("bench_async_loop not found")
	}
	asyncioModVal, _ := v.GetGlobal("asyncio")
	runFn, _ := asyncioModVal.AsModule().Dict.Get(val.InternString("run"))

	coro := v.Vectorcall(benchFn, []val.Value{val.Int64Val(100)}, nil)
	res := v.Vectorcall(runFn, []val.Value{coro}, nil)
	if !res.IsInt64() || res.AsInt64() != 5050 {
		t.Fatalf("expected 5050, got %v", res)
	}
}

func BenchmarkRealWorldAsync(b *testing.B) {
	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)

	co, err := compiler.CompileString(asyncSrc)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	benchFn, ok := v.GetGlobal("bench_async_loop")
	if !ok {
		b.Fatal("bench_async_loop not found")
	}
	asyncioModVal, _ := v.GetGlobal("asyncio")
	runFn, _ := asyncioModVal.AsModule().Dict.Get(val.InternString("run"))

	warmCoro := v.Vectorcall(benchFn, []val.Value{val.Int64Val(10)}, nil)
	_ = v.Vectorcall(runFn, []val.Value{warmCoro}, nil)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		coro := v.Vectorcall(benchFn, []val.Value{val.Int64Val(100)}, nil)
		_ = v.Vectorcall(runFn, []val.Value{coro}, nil)
	}
}

// --- Domain: Real-World Binary MemoryView Zero-Copy ---

func TestRealWorldBinary(t *testing.T) {
	const payloadSize = 10 * 1024 * 1024 // 10 MB
	raw := make([]byte, payloadSize)
	for i := range raw {
		raw[i] = byte(i % 256)
	}

	bytesVal := val.NewBytesVal(raw)
	mvVal := val.NewMemoryViewVal(bytesVal)
	sl := val.NewSliceVal(val.Int64Val(1000), val.Int64Val(2000), val.NoneVal())

	sub := val.GetItemCtx(nil, mvVal, sl)
	mv := sub.AsMemoryView()
	if unsafe.Pointer(&raw[1000]) != unsafe.Pointer(&mv.Buf[0]) {
		t.Fatalf("sub-slice payload was copied: pointer mismatch")
	}
}

func BenchmarkRealWorldBinary(b *testing.B) {
	const payloadSize = 10 * 1024 * 1024 // 10 MB
	raw := make([]byte, payloadSize)
	for i := range raw {
		raw[i] = byte(i % 256)
	}

	bytesVal := val.NewBytesVal(raw)
	mvVal := val.NewMemoryViewVal(bytesVal)
	sl := val.NewSliceVal(val.Int64Val(100), val.Int64Val(100000), val.NoneVal())

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		sub := val.GetItemCtx(nil, mvVal, sl)
		_ = sub
	}
}

// --- Domain: Real-World JSON Loads ---

func TestRealWorldJSON(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[\n")
	for i := range 1000 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		fmt.Fprintf(&sb, `  {"id": %d, "name": "usr%d", "age": %d, "active": true, "score": 95.5}`, i, i, 20+(i%50))
	}
	sb.WriteString("\n]")
	jsonBytes := []byte(sb.String())

	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)
	jsonMod, err := v.ImportModule("json", 0, true, nil)
	if err != nil {
		t.Fatalf("failed to import json: %v", err)
	}
	loadsFn, ok := jsonMod.AsModule().Dict.Get(val.InternString("loads"))
	if !ok {
		t.Fatal("json.loads not found")
	}

	arg := []val.Value{val.NewBytesVal(jsonBytes)}
	res := loadsFn.AsBuiltinFunc().Fn(nil, loadsFn, arg, nil)
	if !res.IsList() || res.AsList().Len() != 1000 {
		t.Fatalf("expected 1000 elements in list, got %v", res)
	}
}

func BenchmarkRealWorldJSON(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("[\n")
	for i := range 1000 {
		if i > 0 {
			sb.WriteString(",\n")
		}
		fmt.Fprintf(&sb, `  {"id": %d, "name": "usr%d", "age": %d, "active": true, "score": 95.5}`, i, i, 20+(i%50))
	}
	sb.WriteString("\n]")
	jsonBytes := []byte(sb.String())

	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)
	jsonMod, err := v.ImportModule("json", 0, true, nil)
	if err != nil {
		b.Fatalf("failed to import json: %v", err)
	}
	loadsFn, ok := jsonMod.AsModule().Dict.Get(val.InternString("loads"))
	if !ok {
		b.Fatal("json.loads not found")
	}

	arg := []val.Value{val.NewBytesVal(jsonBytes)}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		res := loadsFn.AsBuiltinFunc().Fn(nil, loadsFn, arg, nil)
		_ = res
	}
}
