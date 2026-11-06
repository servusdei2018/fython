package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestPEP750Template(t *testing.T) {
	src := `
name = "Ada"
t = t"Hello {name!r:>10}"
if type(t).__name__ != "Template":
    raise SystemExit("type")
if t.strings != ("Hello ", ""):
    raise SystemExit("strings " + repr(t.strings))
if len(t.interpolations) != 1:
    raise SystemExit("ninterp")
i = t.interpolations[0]
if i.value != "Ada":
    raise SystemExit("value")
if i.expression != "name":
    raise SystemExit("expr " + repr(i.expression))
if i.conversion != "r":
    raise SystemExit("conv " + repr(i.conversion))
if i.format_spec != ">10":
    raise SystemExit("spec " + repr(i.format_spec))
if t.values != ("Ada",):
    raise SystemExit("values")
parts = list(t)
if parts != ["Hello ", i]:
    raise SystemExit("iter")

raw = rt"a\nb{name}"
if raw.strings[0] != "a\\nb":
    raise SystemExit("raw " + repr(raw.strings[0]))
if raw.interpolations[0].value != "Ada":
    raise SystemExit("raw value")

built = type(t)("X", i, "Y")
if built.strings != ("X", "Y"):
    raise SystemExit("ctor " + repr(built.strings))

empty = t""
if empty.strings != ("",):
    raise SystemExit("empty")
if empty.interpolations != ():
    raise SystemExit("empty interps")
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatal(err)
	}
	engine := vm.NewEngine()
	v := engine.AcquireVM()
	v.RunCode(co)
	if exc := v.LastException(); exc.HeaderPtr() != nil && !exc.IsNone() {
		t.Fatalf("exception: %s", exc.String())
	}
}
