package repl

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/stdlib"
	"github.com/servusdei2018/fython/pkg/val"
)

// Candidate is one completion entry.
type Candidate struct {
	Text     string
	Callable bool
}

// keywords is the CPython keyword set.
var keywords = []string{
	"False", "None", "True", "and", "as", "assert", "async", "await",
	"break", "class", "continue", "def", "del", "elif", "else", "except",
	"finally", "for", "from", "global", "if", "import", "in", "is",
	"lambda", "nonlocal", "not", "or", "pass", "raise", "return", "try",
	"type", "while", "with", "yield", "match", "case",
}

func isKeyword(s string) bool { return slices.Contains(keywords, s) }

func isKeywordToken(t compiler.TokenType) bool {
	return t >= compiler.TOKEN_DEF && t <= compiler.TOKEN_DEL
}

// isBuiltinName reports whether name is available as a builtin.
func isBuiltinName(name string) bool {
	_, ok := builtinNamesSet[name]
	return ok
}

var builtinNamesSet = buildBuiltinNamesSet()

func buildBuiltinNamesSet() map[string]bool {
	// Starts empty; the worker fills it from the live builtins dict on
	// first use via ensureBuiltinNames. Most names are common enough
	// that a static fallback list covers fresh VMs.
	m := make(map[string]bool, 160)
	for _, n := range []string{
		"abs", "all", "any", "ascii", "bin", "bool", "breakpoint", "bytearray",
		"bytes", "callable", "chr", "classmethod", "compile", "complex",
		"copyright", "credits", "delattr", "dict", "dir", "divmod", "enumerate",
		"eval", "exec", "exit", "filter", "float", "format", "frozenset",
		"getattr", "globals", "hasattr", "hash", "help", "hex", "id", "input",
		"int", "isinstance", "issubclass", "iter", "len", "license", "list",
		"locals", "map", "max", "memoryview", "min", "next", "object", "oct",
		"open", "ord", "pow", "print", "property", "quit", "range", "repr",
		"reversed", "round", "set", "setattr", "slice", "sorted", "staticmethod",
		"str", "sum", "super", "tuple", "type", "vars", "zip",
		"Ellipsis", "NotImplemented", "StopIteration", "ArithmeticError",
		"AssertionError", "AttributeError", "BaseException", "BaseExceptionGroup",
		"BlockingIOError", "BrokenPipeError", "BufferError", "BytesWarning",
		"ChildProcessError", "ConnectionAbortedError", "ConnectionError",
		"ConnectionRefusedError", "ConnectionResetError", "DeprecationWarning",
		"EOFError", "EncodingWarning", "EnvironmentError", "Exception",
		"ExceptionGroup", "FileExistsError", "FileNotFoundError",
		"FloatingPointError", "FutureWarning", "GeneratorExit", "ImportError",
		"ImportWarning", "IndentationError", "IndexError", "InterruptedError",
		"IsADirectoryError", "KeyError", "KeyboardInterrupt", "LookupError",
		"MemoryError", "ModuleNotFoundError", "NameError",
		"NotADirectoryError", "NotImplementedError", "OSError",
		"OverflowError", "PendingDeprecationWarning", "PermissionError",
		"ProcessLookupError", "RecursionError", "ReferenceError",
		"ResourceWarning", "RuntimeError", "RuntimeWarning", "SyntaxError",
		"SyntaxWarning", "SystemError", "SystemExit", "TabError",
		"TimeoutError", "TypeError", "UnboundLocalError", "UnicodeDecodeError",
		"UnicodeEncodeError", "UnicodeError", "UnicodeTranslateError",
		"UnicodeWarning", "UserWarning", "ValueError", "Warning",
		"ZeroDivisionError",
	} {
		m[n] = true
	}
	return m
}

var (
	attrChainRe  = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\.([A-Za-z_][A-Za-z0-9_]*)?$`)
	importTopRe  = regexp.MustCompile(`\bimport\s+([A-Za-z_][A-Za-z0-9_.]*)$`)
	fromImportRe = regexp.MustCompile(`\bfrom\s+([A-Za-z_][A-Za-z0-9_.]*)\s+import\s+([A-Za-z_][A-Za-z0-9_]*)?$`)
	defNameRe    = regexp.MustCompile(`(?m)^(?:class|def)\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	assignNameRe = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*(?::[^=]+)?=`)
	allListRe    = regexp.MustCompile(`(?s)__all__\s*=\s*\[(.*?)\]`)
	quotedNameRe = regexp.MustCompile(`["']([A-Za-z_][A-Za-z0-9_]*)["']`)
)

// completeAt returns completion candidates for src with the cursor at the
// given offset. It runs on the VM worker goroutine: attribute chains are
// resolved by dictionary/shape walking only (no eval of arbitrary logic).
func (w *Worker) completeAt(src string, cursor int) []Candidate {
	if cursor < 0 || cursor > len(src) {
		cursor = len(src)
	}
	before := src[:cursor]

	// Attribute completion: obj.attr_prefix.
	if m := attrChainRe.FindStringSubmatch(before); m != nil && strings.Contains(m[0], ".") {
		chain := strings.Split(m[1], ".")
		prefix := m[2]
		cur := w.resolveChain(chain)
		if cur.HeaderPtr() == nil && !cur.IsNone() {
			return nil
		}
		return w.attrCandidates(cur, prefix)
	}

	// from <pkg> import <prefix>.
	if m := fromImportRe.FindStringSubmatch(before); m != nil {
		return w.fromImportCandidates(m[1], m[2])
	}

	// import <prefix>.
	if m := importTopRe.FindStringSubmatch(before); m != nil {
		return w.importCandidates(m[1])
	}

	// Bare name: keywords, builtins, globals.
	prefix := trailingIdent(before)
	var out []Candidate
	for _, kw := range keywords {
		if strings.HasPrefix(kw, prefix) {
			out = append(out, Candidate{Text: kw})
		}
	}
	for name := range builtinNamesSet {
		if strings.HasPrefix(name, prefix) && prefix != "" {
			out = append(out, Candidate{Text: name})
		} else if prefix == "" && len(out) < 200 {
			out = append(out, Candidate{Text: name})
		}
	}
	if g := w.vm.Globals(); g != nil {
		for _, item := range g.Items() {
			if item.Key.IsAnyString() {
				name := item.Key.AsString()
				if strings.HasPrefix(name, prefix) {
					out = append(out, Candidate{Text: name, Callable: val.IsCallable(item.Value)})
				}
			}
		}
	}
	sortCandidates(out)
	return out
}

func trailingIdent(s string) string {
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			i--
		} else {
			break
		}
	}
	return s[i:]
}

// resolveChain walks a dotted identifier chain through dictionaries and
// shapes only. The root is looked up in __main__ globals, then builtins.
func (w *Worker) resolveChain(chain []string) val.Value {
	var cur val.Value
	var ok bool
	if g := w.vm.Globals(); g != nil {
		cur, ok = g.Get(val.InternString(chain[0]))
	}
	if !ok {
		if b := w.vm.Builtins(); b != nil {
			cur, ok = b.Get(val.InternString(chain[0]))
		}
	}
	if !ok {
		return val.NoneVal()
	}
	for _, name := range chain[1:] {
		cur = w.vm.GetAttrForRepl(cur, val.InternString(name))
		if cur.IsNone() || cur.HeaderPtr() == nil {
			return val.NoneVal()
		}
	}
	return cur
}

// attrCandidates lists attributes of cur matching prefix.
func (w *Worker) attrCandidates(cur val.Value, prefix string) []Candidate {
	names := listAttrNames(cur)
	var out []Candidate
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		callable := false
		if attr := w.vm.GetAttrForRepl(cur, val.InternString(name)); attr.HeaderPtr() != nil {
			callable = val.IsCallable(attr)
		}
		out = append(out, Candidate{Text: name, Callable: callable})
	}
	sortCandidates(out)
	return out
}

// listAttrNames mirrors dir(): type MRO dicts plus instance shapes.
func listAttrNames(cur val.Value) []string {
	if cur.HeaderPtr() == nil {
		return nil
	}
	tp := cur.Type()
	seen := map[string]bool{}
	for _, base := range tp.MRO {
		if base.Dict != nil {
			for _, item := range base.Dict.Items() {
				if item.Key.IsAnyString() {
					seen[item.Key.AsString()] = true
				}
			}
		}
	}
	if cur.IsInstance() {
		for s := cur.AsInstance().Shape; s != nil; s = s.Parent {
			if s.AttrName.IsAnyString() {
				if n := s.AttrName.AsString(); n != "" {
					seen[n] = true
				}
			}
		}
	}
	if cur.IsType() {
		for _, base := range cur.AsType().MRO {
			if base.Dict != nil {
				for _, item := range base.Dict.Items() {
					if item.Key.IsAnyString() {
						seen[item.Key.AsString()] = true
					}
				}
			}
		}
	}
	if cur.IsModule() {
		for _, item := range cur.AsModule().Dict.Items() {
			if item.Key.IsAnyString() {
				seen[item.Key.AsString()] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func sortCandidates(cands []Candidate) {
	sort.Slice(cands, func(i, j int) bool { return cands[i].Text < cands[j].Text })
}

// importCandidates completes a top-level "import <prefix>": candidates are
// already-imported modules, builtin module names, embedded stdlib modules,
// and filesystem modules under sys.path (incl. .venv site-packages), all
// without importing anything.
func (w *Worker) importCandidates(prefix string) []Candidate {
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
		}
	}
	// Already-imported modules.
	if mods := w.vm.Modules(); mods != nil {
		for _, item := range mods.Items() {
			if item.Key.IsAnyString() {
				add(item.Key.AsString())
			}
		}
	}
	// sys.builtin_module_names.
	if sysVal, ok := w.vm.GetModule("sys"); ok && sysVal.IsModule() {
		if bn, ok := sysVal.AsModule().Dict.Get(val.InternString("builtin_module_names")); ok && bn.IsTuple() {
			for _, item := range bn.AsTuple().Items {
				if item.IsAnyString() {
					add(item.AsString())
				}
			}
		}
	}
	// Embedded stdlib.
	_ = fs.WalkDir(stdlib.StdlibFS, "lib", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, ".py") {
			name := strings.TrimPrefix(p, "lib/")
			name = strings.TrimSuffix(name, ".py")
			name = strings.ReplaceAll(name, "/", ".")
			name, _ = strings.CutSuffix(name, ".__init__")
			add(name)
		}
		return nil
	})
	// Filesystem under sys.path.
	for _, dir := range w.vm.SysPath() {
		if dir == "" || dir == "." {
			dir, _ = os.Getwd()
		}
		scanDirModules(dir, add)
		scanDirModules(filepath.Join(dir, "lib", "python3.15", "site-packages"), add)
		scanDirModules(filepath.Join(dir, ".venv", "lib", "python3.15", "site-packages"), add)
	}

	var out []Candidate
	for name := range seen {
		if strings.HasPrefix(name, prefix) {
			out = append(out, Candidate{Text: name})
		}
	}
	sortCandidates(out)
	return out
}

func scanDirModules(dir string, add func(string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			add(e.Name())
			continue
		}
		if name := e.Name(); strings.HasSuffix(name, ".py") {
			add(strings.TrimSuffix(name, ".py"))
		}
	}
}

// fromImportCandidates completes "from <pkg> import <prefix>" by reading
// the package source (never by importing it).
func (w *Worker) fromImportCandidates(pkg, prefix string) []Candidate {
	var src []byte
	if relPath, _, found := stdlib.FindModule(pkg); found {
		if data, err := stdlib.ReadModule(relPath); err == nil {
			src = data
		}
	}
	if src == nil {
		// Filesystem packages: lib/<pkg>/__init__.py or <pkg>.py.
		for _, dir := range w.vm.SysPath() {
			if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(pkg)+".py")); err == nil {
				src = data
				break
			}
			if data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(pkg), "__init__.py")); err == nil {
				src = data
				break
			}
		}
	}
	if src == nil {
		return nil
	}
	seen := map[string]bool{}
	if m := allListRe.FindSubmatch(src); m != nil {
		for _, q := range quotedNameRe.FindAllSubmatch(m[1], -1) {
			seen[string(q[1])] = true
		}
	}
	for _, m := range defNameRe.FindAllSubmatch(src, -1) {
		seen[string(m[1])] = true
	}
	for _, m := range assignNameRe.FindAllSubmatch(src, -1) {
		seen[string(m[1])] = true
	}
	var out []Candidate
	for name := range seen {
		if strings.HasPrefix(name, prefix) {
			out = append(out, Candidate{Text: name})
		}
	}
	sortCandidates(out)
	return out
}

var _ = bufio.NewReader
