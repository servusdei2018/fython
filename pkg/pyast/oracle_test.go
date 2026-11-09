package pyast

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func f64hex(f float64) string { return fmt.Sprintf("%016x", math.Float64bits(f)) }

func genConst(v any) any {
	switch x := v.(type) {
	case NoneT:
		return map[string]any{"t": "none"}
	case EllipsisT:
		return map[string]any{"t": "ellipsis"}
	case bool:
		return map[string]any{"t": "bool", "v": x}
	case int64:
		return map[string]any{"t": "int", "v": strconv.FormatInt(x, 10)}
	case int:
		return map[string]any{"t": "int", "v": strconv.Itoa(x)}
	case *big.Int:
		return map[string]any{"t": "int", "v": x.String()}
	case float64:
		return map[string]any{"t": "float", "v": f64hex(x)}
	case complex128:
		return map[string]any{"t": "complex", "re": f64hex(real(x)), "im": f64hex(imag(x))}
	case string:
		return map[string]any{"t": "str", "v": x}
	case []byte:
		return map[string]any{"t": "bytes", "v": hex.EncodeToString(x)}
	}
	return fmt.Sprintf("?%T", v)
}

func toGeneric(n *Node) any {
	if n == nil {
		return nil
	}
	d := map[string]any{"_": n.Spec.Name}
	for i, f := range n.Spec.Fields {
		v := n.F[i]
		if n.Spec.Name == "Constant" && f.Name == "value" {
			d[f.Name] = genConst(v)
			continue
		}
		switch x := v.(type) {
		case *Node:
			d[f.Name] = toGeneric(x)
		case []*Node:
			l := make([]any, len(x))
			for j, c := range x {
				l[j] = toGeneric(c)
			}
			d[f.Name] = l
		case []string:
			l := make([]any, len(x))
			for j, c := range x {
				l[j] = c
			}
			d[f.Name] = l
		case int:
			d[f.Name] = float64(x)
		case nil:
			d[f.Name] = nil
		case NoneT:
			d[f.Name] = nil
		default:
			if n.Spec.Name == "Constant" && f.Name == "value" {
				d[f.Name] = genConst(v)
			} else {
				d[f.Name] = v
			}
		}
	}
	for i, a := range n.Spec.Attrs {
		if n.Has(i) {
			d[a] = float64(n.Pos[i])
		}
	}
	return d
}

func diff(path string, a, b any) string {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return path + ": type mismatch"
		}
		for k, v := range x {
			if r := diff(path+"."+k, v, y[k]); r != "" {
				return r
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				return path + "." + k + ": extra in oracle"
			}
		}
		return ""
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return fmt.Sprintf("%s: list length %d vs %v", path, len(x), b)
		}
		for i := range x {
			if r := diff(fmt.Sprintf("%s[%d]", path, i), x[i], y[i]); r != "" {
				return r
			}
		}
		return ""
	}
	if sa, okA := a.(string); okA {
		if sb, okB := b.(string); okB {
			if sa == sb {
				return ""
			}
			// When strings contain lone surrogates, Go's json.Marshal encodes each of the 3 WTF-8 bytes
			// as utf8.RuneError (\uFFFD\uFFFD\uFFFD) while json.Unmarshal on \uXXXX escapes decodes to a single \uFFFD.
			if strings.ReplaceAll(sa, "\ufffd\ufffd\ufffd", "\ufffd") == sb {
				return ""
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Sprintf("%s: got %#v want %#v", path, a, b)
	}
	return ""
}

type oracleResult struct {
	Path   string
	Tree   any
	Error  string
	Lineno int
}

func runOracle(t *testing.T, args []string) map[string]oracleResult {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	cmd := exec.Command(py, append([]string{"oracle.py"}, args...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	res := map[string]oracleResult{}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	for sc.Scan() {
		var r oracleResult
		var raw struct {
			Path   string          `json:"path"`
			Tree   json.RawMessage `json:"tree"`
			Error  string          `json:"error"`
			Lineno int             `json:"lineno"`
		}
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		r.Path, r.Error, r.Lineno = raw.Path, raw.Error, raw.Lineno
		if raw.Tree != nil {
			if err := json.Unmarshal(raw.Tree, &r.Tree); err != nil {
				t.Fatal(err)
			}
		}
		res[raw.Path] = r
	}
	_ = cmd.Wait()
	return res
}

func compareFile(path string, mode Mode, o oracleResult) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	n, perr := Parse(src, mode)
	if o.Error != "" {
		if perr == nil {
			return "expected syntax error: " + o.Error
		}
		return ""
	}
	if perr != nil {
		return "unexpected error: " + perr.Error()
	}
	g := toGeneric(n)
	b, _ := json.Marshal(g)
	var g2 any
	_ = json.Unmarshal(b, &g2)
	return diff("$", g2, o.Tree)
}

func TestOracleCorpus(t *testing.T) {
	var files []string
	roots := []string{"../stdlib/lib", "testdata"}
	if extra := os.Getenv("PYAST_CORPUS"); extra != "" {
		roots = append(roots, strings.Split(extra, string(os.PathListSeparator))...)
	}
	for _, root := range roots {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ".py") {
				files = append(files, p)
			}
			return nil
		})
	}
	res := runOracle(t, files)
	bad := 0
	for _, f := range files {
		o, ok := res[f]
		if !ok {
			t.Errorf("%s: no oracle result", f)
			continue
		}
		if msg := compareFile(f, ModeExec, o); msg != "" {
			t.Errorf("%s: %s", f, msg)
			if bad++; bad > 40 {
				break
			}
		}
	}
}
