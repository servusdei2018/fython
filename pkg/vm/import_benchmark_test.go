package vm_test

import (
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// BenchmarkLoadGlobalCached benchmarks reading a module-level constant or calling an imported
// function inside a hot loop, verifying 0 B/op and 0 allocs/op via inline caching.
func BenchmarkLoadGlobalCached(b *testing.B) {
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)

	// Virtual module defining an imported constant and function
	engine.RegisterVirtualModule("bench_mod", `
GLOBAL_CONST = 42

def add_one(x):
    return x + 1
`)

	v := engine.NewVM()

	// Bytecode in main VM executing a loop:
	// from bench_mod import GLOBAL_CONST, add_one
	// total = 0
	// for i in range(100):
	//     total += add_one(GLOBAL_CONST)
	mainSrc := `
from bench_mod import GLOBAL_CONST, add_one

def run_loop(n):
    total = 0
    for i in range(n):
        total += add_one(GLOBAL_CONST)
    return total
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)

	runLoopFn, ok := v.GetGlobal("run_loop")
	if !ok {
		b.Fatalf("run_loop not found in globals")
	}

	argSlice := []val.Value{val.Int64Val(100)}

	b.ReportAllocs()
	for b.Loop() {
		res := v.Vectorcall(runLoopFn, argSlice, nil)
		if !res.IsInt64() || res.AsInt64() != 4300 {
			b.Fatalf("unexpected result: %v", res)
		}
	}
}

// BenchmarkFycDeserialize benchmarks deserializing a compiled .fyc bytecode payload vs compiling from source.
func BenchmarkFycDeserialize(b *testing.B) {
	// A representative Python module with functions, arithmetic, classes, and loops
	moduleSrc := `
def fib(n):
    if n <= 1:
        return n
    return fib(n - 1) + fib(n - 2)

class Vector:
    def __init__(self, x, y):
        self.x = x
        self.y = y

    def length_sq(self):
        return self.x * self.x + self.y * self.y

def process_items(items):
    total = 0
    for item in items:
        total += item * 2
    return total

def safe_divide(a, b):
    try:
        return a // b
    except ZeroDivisionError:
        return 0
`
	srcBytes := []byte(moduleSrc)
	co, err := compiler.CompileSource(srcBytes)
	if err != nil {
		b.Fatalf("CompileSource failed: %v", err)
	}

	srcHash := vm.ComputeSourceHash(srcBytes)
	mtime := time.Now().UnixNano()
	fycData, err := vm.SerializeCodeObject(co, uint64(len(srcBytes)), mtime, srcHash)
	if err != nil {
		b.Fatalf("SerializeCodeObject failed: %v", err)
	}

	b.Run("CompileSource", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c, err := compiler.CompileSource(srcBytes)
			if err != nil || c == nil {
				b.Fatalf("compile failed: %v", err)
			}
		}
	})

	b.Run("FycDeserialize", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c, _, err := vm.DeserializeCodeObject(fycData)
			if err != nil || c == nil {
				b.Fatalf("deserialize failed: %v", err)
			}
		}
	})
}
