package benchmarks_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

var benchmarkFiles = []string{
	"bench_oop.py",
	"bench_io.py",
	"bench_nqueens.py",
	"bench_async.py",
	"bench_binary.py",
	"bench_json.py",
}

func TestBenchmarkScriptsInEngine(t *testing.T) {
	for _, bf := range benchmarkFiles {
		t.Run(bf, func(t *testing.T) {
			path := filepath.Join(".", bf)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("failed to read benchmark script %s: %v", bf, err)
			}

			eng := vm.NewEngine()
			v := eng.AcquireVM()
			defer eng.ReleaseVM(v)

			co, err := compiler.CompileString(string(src))
			if err != nil {
				t.Fatalf("failed to compile %s: %v", bf, err)
			}

			v.RunCode(co)
			exc := v.UnhandledException()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				t.Fatalf("benchmark %s threw unhandled exception: %s", bf, v.FormatException(exc))
			}
		})
	}
}
