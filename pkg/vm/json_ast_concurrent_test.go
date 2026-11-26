package vm_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestConcurrentJSONAndAST(t *testing.T) {
	eng := vm.NewEngine()
	eng.SetCompileHook(compiler.CompileSource)
	src := `
import ast
import json

raw = '[{"id": 1, "name": "alpha", "a": 1, "a": 2}, {"id": 2, "name": "beta"}]'
for _ in range(20):
    d = json.loads(raw)
    assert d[0]["a"] == 2 and d[1]["name"] == "beta"
    tree = ast.parse("x = 1 + 2\n")
    assert tree.body[0].lineno == 1
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			v := eng.AcquireVM()
			defer eng.ReleaseVM(v)
			v.RunCode(co)
			if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
				errs <- fmt.Errorf("python exception: %s", v.FormatException(exc))
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent exec failed: %v", err)
	}
}
