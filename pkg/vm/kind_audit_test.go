package vm_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestKindInstanceInvariant enforces that a heap object may only set
// Header.Kind = KindInstance when its Go struct is val.PyInstance or has a
// val.PyInstance as its first field (so every unsafe downcast from Value to
// *PyInstance stays inside the allocation).
func TestKindInstanceInvariant(t *testing.T) {
	root := filepath.Join("..", "..", "pkg")
	fset := token.NewFileSet()

	type fileInfo struct {
		file *ast.File
		path string
	}
	var files []fileInfo
	// instanceFirst records struct type names whose first field is a PyInstance.
	instanceFirst := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		files = append(files, fileInfo{f, path})
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || len(st.Fields.List) == 0 {
					continue
				}
				if typeName(st.Fields.List[0].Type) == "PyInstance" {
					instanceFirst[ts.Name.Name] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, fi := range files {
		var stack []ast.Node
		ast.Inspect(fi.file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			lit, ok := n.(*ast.CompositeLit)
			if !ok || typeName(lit.Type) != "ObjectHeader" || !setsKindInstance(lit) {
				return true
			}
			checked++
			// Find the enclosing struct literal (skip the KeyValueExpr).
			var outer *ast.CompositeLit
			for i := len(stack) - 2; i >= 0; i-- {
				if c, ok := stack[i].(*ast.CompositeLit); ok {
					outer = c
					break
				}
			}
			pos := fset.Position(lit.Pos())
			if outer == nil {
				// Bare header assignment: allowed only inside methods of
				// types that the invariant already covers (none today).
				t.Errorf("%s: ObjectHeader with KindInstance outside a struct literal", pos)
				return true
			}
			name := typeName(outer.Type)
			if name == "PyInstance" {
				return true
			}
			// Must be a struct whose first field is a PyInstance and the
			// header must be set through that field.
			parentKV := stack[len(stack)-2]
			if kv, ok := parentKV.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "inst" && instanceFirst[name] {
					return true
				}
			}
			t.Errorf("%s: %s sets Kind=KindInstance but is not a PyInstance and does not embed it as its first field", pos, name)
			return true
		})
	}
	if checked == 0 {
		t.Fatal("audit found no KindInstance headers; the test is broken")
	}
}

// typeName returns the unqualified name of a type expression ("" if unknown).
func typeName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return typeName(x.X)
	case *ast.IndexListExpr:
		return typeName(x.X)
	}
	return ""
}

func setsKindInstance(lit *ast.CompositeLit) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Kind" && typeName(kv.Value) == "KindInstance" {
			return true
		}
	}
	return false
}
