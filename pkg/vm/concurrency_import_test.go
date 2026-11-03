package vm_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

// TestConcurrentModuleImportSingleflight verifies that when 32 concurrent goroutine VMs
// import the exact same uncached module simultaneously, source compilation occurs exactly ONCE.
func TestConcurrentModuleImportSingleflight(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "concurrent_target.py")
	src := `
TOTAL = 0
for i in range(100):
    TOTAL += i
def get_total():
    return TOTAL
`
	if err := os.WriteFile(modPath, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	var compileCount int32
	engine.SetCompileHook(func(s []byte) (*vm.CodeObject, error) {
		atomic.AddInt32(&compileCount, 1)
		// Small sleep to ensure concurrent goroutines arrive while compilation is in-flight
		time.Sleep(5 * time.Millisecond)
		return compiler.CompileSource(s)
	})
	engine.SetSearchPaths([]string{tempDir})

	const numGoroutines = 32
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errCh := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			v := engine.NewVM()
			mainSrc := fmt.Sprintf(`
import concurrent_target
res_%d = concurrent_target.get_total()
`, gid)
			co, err := compiler.CompileString(mainSrc)
			if err != nil {
				errCh <- fmt.Errorf("gid=%d compile main failed: %v", gid, err)
				return
			}
			v.RunCode(co)

			val, ok := v.GetGlobal(fmt.Sprintf("res_%d", gid))
			if !ok || val.AsInt64() != 4950 {
				errCh <- fmt.Errorf("gid=%d unexpected result: got %v (ok=%v), want 4950", gid, val, ok)
			}
		}(g)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// Verify that compilation happened exactly 1 time!
	c := atomic.LoadInt32(&compileCount)
	if c != 1 {
		t.Fatalf("Engine singleflight failed: expected exactly 1 compilation call, got %d", c)
	}
}

// TestConcurrentCircularImportsRace verifies that 32 concurrent goroutines can run
// circular imports simultaneously without race conditions, deadlocks, or state corruption.
func TestConcurrentCircularImportsRace(t *testing.T) {
	tempDir := t.TempDir()

	srcA := `
import circ_b
VAL_A = 42
def query_b():
    return circ_b.VAL_B
`
	srcB := `
import circ_a
VAL_B = 84
def query_a():
    return circ_a.VAL_A
`
	if err := os.WriteFile(filepath.Join(tempDir, "circ_a.py"), []byte(srcA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "circ_b.py"), []byte(srcB), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	const numGoroutines = 32
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errCh := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			v := engine.NewVM()
			mainSrc := fmt.Sprintf(`
import circ_a
import circ_b
qa_%d = circ_b.query_a()
qb_%d = circ_a.query_b()
`, gid, gid)
			co, err := compiler.CompileString(mainSrc)
			if err != nil {
				errCh <- fmt.Errorf("gid=%d compile main failed: %v", gid, err)
				return
			}
			v.RunCode(co)

			qa, ok := v.GetGlobal(fmt.Sprintf("qa_%d", gid))
			if !ok || qa.AsInt64() != 42 {
				errCh <- fmt.Errorf("gid=%d qa = %v, want 42", gid, qa)
			}
			qb, ok := v.GetGlobal(fmt.Sprintf("qb_%d", gid))
			if !ok || qb.AsInt64() != 84 {
				errCh <- fmt.Errorf("gid=%d qb = %v, want 84", gid, qb)
			}
		}(g)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}
