package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// BenchmarkAsyncDefAwaitLoop benchmarks calling and awaiting a pooled
// `async def add(a, b): return a + b` coroutine in a tight loop.
// In steady state, frame reuse and fast-path execution achieves 0 B/op, 0 allocs/op.
func BenchmarkAsyncDefAwaitLoop(b *testing.B) {
	src := `
import asyncio

async def add(a, b):
    return a + b

async def bench_loop(n):
    total = 0
    i = 0
    while i < n:
        total = total + (await add(i, 1))
        i = i + 1
    return total
`
	engine := vm.NewEngine()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)

	co, err := compiler.CompileString(src)
	if err != nil {
		b.Fatalf("compile error: %v", err)
	}

	_ = v.RunCode(co)

	benchFn, ok := v.GetGlobal("bench_loop")
	if !ok || !benchFn.IsFunction() {
		b.Fatalf("bench_loop function not found")
	}

	asyncioModVal, _ := v.GetGlobal("asyncio")
	runFn, _ := asyncioModVal.AsModule().Dict.Get(val.InternString("run"))

	// Warm up pool
	warmCoro := v.Vectorcall(benchFn, []val.Value{val.Int64Val(100)}, nil)
	_ = v.Vectorcall(runFn, []val.Value{warmCoro}, nil)

	b.ResetTimer()
	b.ReportAllocs()

	testCoro := v.Vectorcall(benchFn, []val.Value{val.Int64Val(int64(b.N))}, nil)
	res := v.Vectorcall(runFn, []val.Value{testCoro}, nil)
	_ = res
}
