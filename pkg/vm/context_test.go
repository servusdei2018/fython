package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func TestContextVarIsolation(t *testing.T) {
	src := `
import asyncio
import _contextvars

var = _contextvars.ContextVar("var", default=0)

async def worker(n):
    token = var.set(n)
    await asyncio.sleep(0)
    got = var.get()
    var.reset(token)
    if got != n:
        raise SystemExit("isolated " + str(n) + " saw " + str(got))
    if var.get() != 0:
        raise SystemExit("reset")
    return got

async def main():
    return await asyncio.gather(worker(1), worker(2))

res = asyncio.run(main())
if sorted(res) != [1, 2]:
    raise SystemExit("gather " + repr(res))

var.set(1)
ctx = _contextvars.copy_context()
var.set(7)
if ctx.get(var) != 1:
    raise SystemExit("copy saw later set")
if var.get() != 7:
    raise SystemExit("current")
if var not in ctx:
    raise SystemExit("contains")
if len(ctx) != 1:
    raise SystemExit("len")
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatal(err)
	}
	v := vm.NewVM()
	v.RunCode(co)
	if exc := v.LastException(); exc.HeaderPtr() != nil && !exc.IsNone() {
		msg := exc.String()
		if exc.IsInstance() {
			if args, ok := exc.AsInstance().GetAttr(val.InternString("args")); ok {
				msg = args.String()
			}
		}
		t.Fatalf("exception: %s", msg)
	}
	res, ok := v.GetGlobal("res")
	if !ok {
		t.Fatal("missing res")
	}
	if !res.IsList() || len(res.AsList().Items) != 2 {
		t.Fatalf("res = %s", res.String())
	}
}

func TestContextGetZeroAlloc(t *testing.T) {
	c := val.NewPyContext()
	key := uintptr(1)
	c.Set(key, val.NoneVal(), val.Int64Val(1))
	allocs := testing.AllocsPerRun(1000, func() {
		v, ok := c.Get(key)
		if !ok || !v.IsInt64() || v.AsInt64() != 1 {
			t.Fatalf("get = %v %v", v, ok)
		}
	})
	if allocs != 0 {
		t.Fatalf("Context.Get allocs = %v, want 0", allocs)
	}
}
