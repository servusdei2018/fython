package vm_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

var expectedWheelChecksums = map[string]string{
	"six-1.17.0-py2.py3-none-any.whl":       "4721f391ed90541fddacab5acf947aa0d3dc7d27b2e1e8eda2be8970586c3274",
	"packaging-26.3-py3-none-any.whl":       "d7193f7c8e4e93f444fde0262bf90af30e16fa0ad0ad44cb553c87339b23cd1c",
	"attrs-26.1.0-py3-none-any.whl":         "c647aa4a12dfbad9333ca4e71fe62ddc36f4e63b2d260a37a8b83d2f043ac309",
	"click-8.5.1-py3-none-any.whl":          "8bb5376d2006950386f8c6427613334a2acadc5f559cb80e638d00b4df957e37",
	"markupsafe-3.0.4-py3-none-any.whl":     "635517e02b7bc3f9b42867bc7895d72909a65882248252953cddb193b2905e33",
	"jinja2-3.1.6-py3-none-any.whl":         "85ece4451f492d0c13c5dd7c13a64681a86afae63a5f347908daf103ce6d2f67",
	"mdurl-0.1.2-py3-none-any.whl":          "84008a41e51615a49fc9966191ff91509e3c40b939176e643fd50a5c2196b8f8",
	"markdown_it_py-4.2.0-py3-none-any.whl": "9f7ebbcd14fe59494226453aed97c1070d83f8d24b6fc3a3bcf9a38092641c4a",
	"pygments-2.21.0-py3-none-any.whl":      "2363c69b61c4a97c838da3b130dcd6468f4848992b21a82f2a63ec34377137d9",
	"rich-15.0.0-py3-none-any.whl":          "33bd4ef74232fb73fe9279a257718407f169c09b78a87ad3d296f548e27de0bb",
}

func setupPyPIEngine(t *testing.T) (*vm.Engine, []string) {
	t.Helper()
	wheelsDir, err := filepath.Abs("testdata/wheels")
	if err != nil {
		t.Fatalf("Failed to resolve testdata/wheels path: %v", err)
	}

	wheelPaths := make([]string, 0, len(expectedWheelChecksums))
	for filename, expectedHash := range expectedWheelChecksums {
		path := filepath.Join(wheelsDir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Failed to read wheel %s: %v", filename, err)
		}
		actualHash := sha256.Sum256(data)
		actualHex := hex.EncodeToString(actualHash[:])
		if actualHex != expectedHash {
			t.Fatalf("Wheel %s checksum mismatch: expected %s, got %s", filename, expectedHash, actualHex)
		}
		wheelPaths = append(wheelPaths, path)
	}

	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	engine.SetSearchPaths(wheelPaths)
	return engine, wheelPaths
}

func runPyPIScript(t *testing.T, engine *vm.Engine, wheelPaths []string, script string) {
	t.Helper()
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)

	co, err := compiler.CompileSourceWithFilename([]byte(script), "<pypi_test>")
	if err != nil {
		t.Fatalf("Compilation error: %v\nScript:\n%s", err, script)
	}

	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%v\n%s", r, string(debug.Stack()))
			if excVal, ok := r.(val.Value); ok {
				msg = excVal.String()
				if excVal.IsInstance() {
					if argsVal, ok := excVal.AsInstance().GetAttr(val.InternString("args")); ok {
						msg += fmt.Sprintf(" (args: %s)", argsVal.String())
					}
				}
			}
			t.Fatalf("Runtime panic: %s\nScript:\n%s", msg, script)
		}
	}()

	v.RunCode(co)
	if !v.UnhandledException().IsNone() && v.UnhandledException().HeaderPtr() != nil {
		excVal := v.UnhandledException()
		msg := excVal.String()
		if excVal.IsInstance() {
			if argsVal, ok := excVal.AsInstance().GetAttr(val.InternString("args")); ok {
				msg += fmt.Sprintf(" (args: %s)", argsVal.String())
			}
			if tbVal, ok := excVal.AsInstance().GetAttr(val.InternString("__traceback__")); ok && !tbVal.IsNone() {
				msg += "\nTraceback:"
				curTB := tbVal
				for !curTB.IsNone() && curTB.HeaderPtr() != nil && curTB.Kind() == val.KindTraceback {
					tb := curTB.AsTraceback()
					fnName := "?"
					fileName := "?"
					if !tb.Frame.IsNone() && tb.Frame.Kind() == val.KindFrame {
						fr := tb.Frame.AsFrame()
						if val.CodeInspectorHook != nil && fr.CO.Kind() == val.KindCode {
							fnName = val.CodeInspectorHook.Name(fr.CO)
							fileName = val.CodeInspectorHook.FileName(fr.CO)
						}
					}
					msg += fmt.Sprintf("\n  File %q, line %d, in %s", fileName, tb.LineNo, fnName)
					curTB = tb.Next
				}
			}
		}
		t.Fatalf("Unhandled Python exception: %s\nScript:\n%s", msg, script)
	}
}

func TestPyPIEcosystem(t *testing.T) {
	engine, wheelPaths := setupPyPIEngine(t)

	t.Run("packaging_and_six", func(t *testing.T) {
		script := `
import sys
import six
from packaging.version import Version
from packaging.requirements import Requirement
from packaging.markers import Marker

# 1. PEP 440 version comparisons
v_rc = Version("3.15.0rc1")
v_final = Version("3.15.0")
assert v_rc < v_final, f"Expected {v_rc} < {v_final}"
assert Version("1.0.0") == Version("1.0"), "Zero patch equivalence"
assert Version("2.0.0.post1") > Version("2.0.0"), "Post release ordering"

# 2. PEP 508 dependency specifiers
req = Requirement('foo[extra]>=1.2,<2.0; python_version>="3.12"')
assert req.name == "foo", f"Expected foo, got {req.name}"
assert "extra" in req.extras, "Extras parsing"
assert req.specifier.contains("1.5.0"), "Specifier range check"
assert not req.specifier.contains("2.1.0"), "Specifier outside range"

# 3. Platform markers
marker = Marker('sys_platform == "linux" or sys_platform == "darwin"')
eval_result = marker.evaluate()
assert isinstance(eval_result, bool), "Marker evaluation must return bool"
assert eval_result == (sys.platform in ("linux", "darwin")), f"Evaluated {eval_result} for {sys.platform}"

# 4. six compatibility helpers
assert six.PY3, "six.PY3 must be True"
assert six.text_type is str, "six.text_type must be str"
assert six.binary_type is bytes, "six.binary_type must be bytes"
`
		runPyPIScript(t, engine, wheelPaths, script)
	})

	t.Run("attrs", func(t *testing.T) {
		script := `
import attrs
from attrs import define, frozen, field, validators, Factory

@define(slots=True)
class InventoryItem:
    name: str = field(validator=validators.instance_of(str))
    quantity: int = field(default=0, converter=int)
    tags: list = field(default=Factory(list))

item1 = InventoryItem(name="widget", quantity="42", tags=["a", "b"])
assert item1.name == "widget"
assert item1.quantity == 42
assert item1.tags == ["a", "b"]

# 1. Convert via attrs.asdict()
d = attrs.asdict(item1)
assert d == {"name": "widget", "quantity": 42, "tags": ["a", "b"]}

# 2. Evolve instance via attrs.evolve()
item2 = attrs.evolve(item1, quantity=100)
assert item2.quantity == 100
assert item1.quantity == 42

# 3. Frozen class
@frozen(slots=True)
class ImmutablePoint:
    x: float
    y: float

p = ImmutablePoint(1.5, 2.5)
assert p.x == 1.5
assert p.y == 2.5
try:
    p.x = 3.0
    assert False, "Mutation on frozen class must fail"
except (attrs.exceptions.FrozenInstanceError, AttributeError):
    pass
`
		runPyPIScript(t, engine, wheelPaths, script)
	})

	t.Run("click", func(t *testing.T) {
		script := `
import click
from click.testing import CliRunner

@click.group()
def cli():
    pass

@cli.command()
@click.option("--count", default=3, type=int, help="Number of greetings.")
@click.argument("name")
def hello(count, name):
    for _ in range(count):
        click.echo(f"Hello, {name}!")

runner = CliRunner()
result = runner.invoke(cli, ["hello", "Fython", "--count", "2"])
if result.exit_code != 0:
    import traceback
    print("CLI EXCEPTION:", repr(result.exception))
    if hasattr(result, "exc_info") and result.exc_info and result.exc_info[2]:
        traceback.print_exception(*result.exc_info)
assert result.exit_code == 0, f"CLI failed with exit code {result.exit_code}: {result.output} (exc: {result.exception})"
expected = "Hello, Fython!\nHello, Fython!\n"
assert result.output == expected, f"Expected {expected!r}, got {result.output!r}"
`
		runPyPIScript(t, engine, wheelPaths, script)
	})

	t.Run("jinja2_and_markupsafe", func(t *testing.T) {
		script := `
import jinja2

templates = {
    "base.html": """<!DOCTYPE html>
<html>
<head><title>{% block title %}Default Title{% endblock %}</title></head>
<body>
{% block content %}{% endblock %}
</body>
</html>""",
    "child.html": """{% extends "base.html" %}
{% macro render_item(item) -%}
<li>{{ item.name | upper }}: {{ item.value | e }}</li>
{%- endmacro %}
{% block title %}User List{% endblock %}
{% block content %}
<ul>
{% for u in users %}
{{ render_item(u) }}
{% endfor %}
</ul>
{% endblock %}"""
}

loader = jinja2.DictLoader(templates)
env = jinja2.Environment(loader=loader, autoescape=True)
template = env.get_template("child.html")
users = [
    {"name": "alice", "value": "A < B & C"},
    {"name": "bob", "value": "100% pure 'python'"},
]
rendered = template.render(users=users)
assert "<title>User List</title>" in rendered
assert "<li>ALICE: A &lt; B &amp; C</li>" in rendered
assert "<li>BOB: 100% pure &#39;python&#39;</li>" in rendered or "<li>BOB: 100% pure &#x27;python&#x27;</li>" in rendered or "<li>BOB: 100% pure 'python'</li>" in rendered
`
		runPyPIScript(t, engine, wheelPaths, script)
	})

	t.Run("rich", func(t *testing.T) {
		script := `
import io
from rich.console import Console
from rich.table import Table
from rich.panel import Panel
from rich.syntax import Syntax

f = io.StringIO()
console = Console(file=f, force_terminal=True, width=80, color_system="standard")

# Table rendering
table = Table(title="PyPI Ecosystem Status")
table.add_column("Package", style="cyan")
table.add_column("Status", style="green")
table.add_row("packaging", "OK")
table.add_row("attrs", "OK")
table.add_row("click", "OK")
table.add_row("jinja2", "OK")
table.add_row("rich", "OK")
console.print(table)

# Panel rendering
console.print(Panel("Rich Panel Output", title="Summary"))

# Syntax highlighting
code = "def hello(name: str) -> str:\n    return f'Hello, {name}!'"
syntax = Syntax(code, "python", theme="monokai", line_numbers=True)
console.print(syntax)

output = f.getvalue()
assert "PyPI Ecosystem Status" in output
assert "packaging" in output
assert "Summary" in output
assert "hello" in output
`
		runPyPIScript(t, engine, wheelPaths, script)
	})
}
