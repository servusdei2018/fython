package vm_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func init() {
	vm.DefaultEngine().SetCompileHook(compiler.CompileSource)
}

func TestSimpleModuleImport(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "math_helper.py")
	src := `
PI = 3.14159
def double_val(x):
    return x * 2
`
	if err := os.WriteFile(modPath, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	v := engine.NewVM()
	mainSrc := `
import math_helper
res = math_helper.double_val(21)
pi = math_helper.PI
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}

	v.RunCode(co)

	res, ok := v.GetGlobal("res")
	if !ok || res.AsInt64() != 42 {
		t.Errorf("res = %v, want 42", res)
	}
	pi, ok := v.GetGlobal("pi")
	if !ok || pi.AsFloat64() != 3.14159 {
		t.Errorf("pi = %v, want 3.14159", pi)
	}
}

func TestImportWithAlias(t *testing.T) {
	tempDir := t.TempDir()
	modPath := filepath.Join(tempDir, "greeter.py")
	src := `
def greet():
    return "hello alias"
`
	if err := os.WriteFile(modPath, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	v := engine.NewVM()
	mainSrc := `
import greeter as g
msg = g.greet()
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)

	msg, ok := v.GetGlobal("msg")
	if !ok || msg.AsString() != "hello alias" {
		t.Errorf("msg = %v, want 'hello alias'", msg)
	}
	// 'greeter' should NOT be bound in globals
	if _, ok := v.GetGlobal("greeter"); ok {
		t.Errorf("expected 'greeter' not to be bound in globals")
	}
}

func TestNestedPackageHierarchies(t *testing.T) {
	tempDir := t.TempDir()
	// Create pkg/sub/mod.py
	pkgDir := filepath.Join(tempDir, "pkg")
	subDir := filepath.Join(pkgDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte("pkg_val = 'pkg'"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "__init__.py"), []byte("sub_val = 'sub'"), 0644); err != nil {
		t.Fatal(err)
	}
	modSrc := `
def compute(x):
    return x * 10
`
	if err := os.WriteFile(filepath.Join(subDir, "mod.py"), []byte(modSrc), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	t.Run("import pkg.sub.mod without alias", func(t *testing.T) {
		v := engine.NewVM()
		mainSrc := `
import pkg.sub.mod
res = pkg.sub.mod.compute(5)
p_val = pkg.pkg_val
s_val = pkg.sub.sub_val
`
		co, err := compiler.CompileString(mainSrc)
		if err != nil {
			t.Fatalf("CompileString failed: %v", err)
		}
		v.RunCode(co)

		res, ok := v.GetGlobal("res")
		if !ok || res.AsInt64() != 50 {
			t.Errorf("res = %v, want 50", res)
		}
		pVal, ok := v.GetGlobal("p_val")
		if !ok || pVal.AsString() != "pkg" {
			t.Errorf("p_val = %v, want 'pkg'", pVal)
		}
		sVal, ok := v.GetGlobal("s_val")
		if !ok || sVal.AsString() != "sub" {
			t.Errorf("s_val = %v, want 'sub'", sVal)
		}
	})

	t.Run("import pkg.sub.mod as m", func(t *testing.T) {
		v := engine.NewVM()
		mainSrc := `
import pkg.sub.mod as m
res = m.compute(7)
`
		co, err := compiler.CompileString(mainSrc)
		if err != nil {
			t.Fatalf("CompileString failed: %v", err)
		}
		v.RunCode(co)

		res, ok := v.GetGlobal("res")
		if !ok || res.AsInt64() != 70 {
			t.Errorf("res = %v, want 70", res)
		}
		if _, ok := v.GetGlobal("pkg"); ok {
			t.Errorf("expected 'pkg' not to be bound in globals when aliased")
		}
	})

	t.Run("from pkg.sub.mod import compute as c", func(t *testing.T) {
		v := engine.NewVM()
		mainSrc := `
from pkg.sub.mod import compute as c
res = c(9)
`
		co, err := compiler.CompileString(mainSrc)
		if err != nil {
			t.Fatalf("CompileString failed: %v", err)
		}
		v.RunCode(co)

		res, ok := v.GetGlobal("res")
		if !ok || res.AsInt64() != 90 {
			t.Errorf("res = %v, want 90", res)
		}
	})
}

func TestRelativeImports(t *testing.T) {
	tempDir := t.TempDir()
	pkgDir := filepath.Join(tempDir, "my_package")
	sub1Dir := filepath.Join(pkgDir, "sub1")
	sub2Dir := filepath.Join(pkgDir, "sub2")
	if err := os.MkdirAll(sub1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub2Dir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "__init__.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub1Dir, "__init__.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub2Dir, "__init__.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// sub2/helper.py
	helperSrc := `
def helper_func():
    return 1234
`
	if err := os.WriteFile(filepath.Join(sub2Dir, "helper.py"), []byte(helperSrc), 0644); err != nil {
		t.Fatal(err)
	}

	// sub1/worker.py uses relative import to reach sibling package: from ..sub2.helper import helper_func
	workerSrc := `
from ..sub2.helper import helper_func
def run():
    return helper_func() + 1
`
	if err := os.WriteFile(filepath.Join(sub1Dir, "worker.py"), []byte(workerSrc), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	v := engine.NewVM()
	mainSrc := `
from my_package.sub1.worker import run
res = run()
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)

	res, ok := v.GetGlobal("res")
	if !ok || res.AsInt64() != 1235 {
		t.Errorf("res = %v, want 1235", res)
	}
}

func TestRelativeImportBeyondTopLevelPackage(t *testing.T) {
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	v := engine.NewVM()

	// Calling from a root module with no __package__
	mainSrc := `
from ..outside import foo
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for relative import beyond top level, got nil")
		}
		msg := r.(string)
		if !strings.Contains(msg, "ImportError") {
			t.Errorf("unexpected panic message: %v", msg)
		}
	}()

	v.RunCode(co)
}

func TestCircularImports(t *testing.T) {
	tempDir := t.TempDir()

	// Module A imports B
	srcA := `
import mod_b
VAL_A = 100
def get_b():
    return mod_b.VAL_B
`
	// Module B imports A
	srcB := `
import mod_a
VAL_B = 200
def get_a():
    return mod_a.VAL_A
`
	if err := os.WriteFile(filepath.Join(tempDir, "mod_a.py"), []byte(srcA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "mod_b.py"), []byte(srcB), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	v := engine.NewVM()
	mainSrc := `
import mod_a
import mod_b

a_from_b = mod_b.get_a()
b_from_a = mod_a.get_b()
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)

	aFromB, ok := v.GetGlobal("a_from_b")
	if !ok || aFromB.AsInt64() != 100 {
		t.Errorf("a_from_b = %v, want 100", aFromB)
	}

	bFromA, ok := v.GetGlobal("b_from_a")
	if !ok || bFromA.AsInt64() != 200 {
		t.Errorf("b_from_a = %v, want 200", bFromA)
	}
}

func TestWildcardImport(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("with __all__ defined", func(t *testing.T) {
		src := `
__all__ = ['exported_func', 'EXPORTED_CONST']
def exported_func():
    return 10
def hidden_func():
    return 20
EXPORTED_CONST = 30
`
		if err := os.WriteFile(filepath.Join(tempDir, "mod_all.py"), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		engine := vm.NewEngine()
		engine.SetCompileHook(compiler.CompileSource)
		engine.SetSearchPaths([]string{tempDir})

		v := engine.NewVM()
		mainSrc := `
from mod_all import *
f = exported_func()
c = EXPORTED_CONST
`
		co, err := compiler.CompileString(mainSrc)
		if err != nil {
			t.Fatalf("CompileString failed: %v", err)
		}
		v.RunCode(co)

		if f, ok := v.GetGlobal("f"); !ok || f.AsInt64() != 10 {
			t.Errorf("f = %v, want 10", f)
		}
		if c, ok := v.GetGlobal("c"); !ok || c.AsInt64() != 30 {
			t.Errorf("c = %v, want 30", c)
		}
		if _, ok := v.GetGlobal("hidden_func"); ok {
			t.Errorf("expected 'hidden_func' not to be imported via wildcard")
		}
	})

	t.Run("without __all__ defined ignores leading underscore", func(t *testing.T) {
		src := `
def public_func():
    return 100
def _private_func():
    return 200
`
		if err := os.WriteFile(filepath.Join(tempDir, "mod_no_all.py"), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}

		engine := vm.NewEngine()
		engine.SetCompileHook(compiler.CompileSource)
		engine.SetSearchPaths([]string{tempDir})

		v := engine.NewVM()
		mainSrc := `
from mod_no_all import *
p = public_func()
`
		co, err := compiler.CompileString(mainSrc)
		if err != nil {
			t.Fatalf("CompileString failed: %v", err)
		}
		v.RunCode(co)

		if p, ok := v.GetGlobal("p"); !ok || p.AsInt64() != 100 {
			t.Errorf("p = %v, want 100", p)
		}
		if _, ok := v.GetGlobal("_private_func"); ok {
			t.Errorf("expected '_private_func' not to be imported via wildcard")
		}
	})
}

func TestSysModulesCaching(t *testing.T) {
	tempDir := t.TempDir()
	modSrc := `
COUNTER = 1
def inc():
    global COUNTER
    COUNTER += 1
`
	if err := os.WriteFile(filepath.Join(tempDir, "counter_mod.py"), []byte(modSrc), 0644); err != nil {
		t.Fatal(err)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths([]string{tempDir})

	v := engine.NewVM()
	mainSrc := `
import counter_mod
counter_mod.inc()

import counter_mod
res = counter_mod.COUNTER
`
	co, err := compiler.CompileString(mainSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)

	res, ok := v.GetGlobal("res")
	if !ok || res.AsInt64() != 2 {
		t.Errorf("COUNTER after re-import = %v, want 2", res)
	}

	// Verify sys.modules in Python
	sysSrc := `
import sys
has_counter = "counter_mod" in sys.modules
`
	co2, err := compiler.CompileString(sysSrc)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co2)

	hasCounter, ok := v.GetGlobal("has_counter")
	if !ok || !hasCounter.AsBool() {
		t.Errorf("has_counter in sys.modules = %v, want True", hasCounter)
	}
}

func TestSharedVsIsolatedModules(t *testing.T) {
	tempDir := t.TempDir()
	modSrc := `
STATE = 10
`
	if err := os.WriteFile(filepath.Join(tempDir, "shared_state.py"), []byte(modSrc), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("isolated modules (default)", func(t *testing.T) {
		engine := vm.NewEngine()
		engine.SetCompileHook(compiler.CompileSource)
		engine.SetSearchPaths([]string{tempDir})

		v1 := engine.NewVM()
		v2 := engine.NewVM()

		co1, _ := compiler.CompileString("import shared_state; shared_state.STATE = 99")
		v1.RunCode(co1)

		co2, _ := compiler.CompileString("import shared_state; s = shared_state.STATE")
		v2.RunCode(co2)

		sVal, _ := v2.GetGlobal("s")
		if sVal.AsInt64() != 10 {
			t.Errorf("expected isolated module STATE to be 10, got %v", sVal)
		}
	})

	t.Run("shared modules configured", func(t *testing.T) {
		engine := vm.NewEngine()
		engine.SetCompileHook(compiler.CompileSource)
		engine.SetSearchPaths([]string{tempDir})
		engine.SetSharedModules(true)

		v1 := engine.NewVM()
		v2 := engine.NewVM()

		co1, _ := compiler.CompileString("import shared_state; shared_state.STATE = 99")
		v1.RunCode(co1)

		co2, _ := compiler.CompileString("import shared_state; s = shared_state.STATE")
		v2.RunCode(co2)

		sVal, _ := v2.GetGlobal("s")
		if sVal.AsInt64() != 99 {
			t.Errorf("expected shared module STATE to be 99, got %v", sVal)
		}
	})
}
