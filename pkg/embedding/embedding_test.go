package embedding_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/embedding"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

type Order struct {
	ID    int64
	Price float64
	Side  string
}

func (o *Order) Total(qty float64) float64 {
	return o.Price * qty
}

func TestRegisterStructTypeOrder(t *testing.T) {
	sb := embedding.RegisterStructType[Order]("Order")
	sb.RegisterFieldInt64("id", func(o *Order) int64 { return o.ID }, func(o *Order, v int64) { o.ID = v })
	sb.RegisterFieldFloat64("price", func(o *Order) float64 { return o.Price }, func(o *Order, v float64) { o.Price = v })
	sb.RegisterFieldString("side", func(o *Order) string { return o.Side }, func(o *Order, v string) { o.Side = v })
	sb.RegisterMethod1R("total", func(o *Order, qty float64) float64 { return o.Total(qty) })

	ord := &Order{ID: 101, Price: 100.5, Side: "BUY"}
	pyOrd := sb.Wrap(ord)

	v := vm.NewVM()
	v.SetGlobal("order", pyOrd)

	src := `
order.price += 1.5
order.side = 'SELL'
order.id = 202
tot = order.total(2.0)
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}

	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Unhandled Python exception: %s", v.FormatException(exc))
	}

	// Verify mutations reflect live on the Go struct pointer
	if ord.ID != 202 {
		t.Errorf("expected ord.ID == 202, got %d", ord.ID)
	}
	if ord.Price != 102.0 {
		t.Errorf("expected ord.Price == 102.0, got %f", ord.Price)
	}
	if ord.Side != "SELL" {
		t.Errorf("expected ord.Side == 'SELL', got %s", ord.Side)
	}

	totVal, ok := v.GetGlobal("tot")
	if !ok || !totVal.IsFloat64() || totVal.AsFloat64() != 204.0 {
		t.Errorf("expected tot == 204.0, got %v", totVal)
	}
}

func TestContextTimeoutCancellation(t *testing.T) {
	v := vm.NewVM()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel()

	limits := embedding.RuntimeLimits{
		Context: ctx,
	}

	src := `
while True:
    pass
`
	start := time.Now()
	_, err := embedding.RunWithLimits(v, src, limits)
	elapsed := time.Since(start)

	if !errors.Is(err, embedding.ErrTimeoutExceeded) {
		t.Fatalf("expected ErrTimeoutExceeded, got %v", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("cancellation took too long: %v (expected < 100ms)", elapsed)
	}
}

func TestStepLimitQuota(t *testing.T) {
	v := vm.NewVM()
	limits := embedding.RuntimeLimits{
		MaxSteps: 5000,
	}

	src := `
while True:
    pass
`
	start := time.Now()
	_, err := embedding.RunWithLimits(v, src, limits)
	elapsed := time.Since(start)

	if !errors.Is(err, embedding.ErrStepLimitExceeded) {
		t.Fatalf("expected ErrStepLimitExceeded, got %v", err)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("step limit halt took too long: %v (expected < 5ms)", elapsed)
	}
}

func TestTypedFuncRegistration(t *testing.T) {
	f0 := embedding.RegisterFunc0("f0", func() int64 { return 10 })
	f1 := embedding.RegisterFunc1("f1", func(a int64) int64 { return a * 2 })
	f2 := embedding.RegisterFunc2("f2", func(a int64, b float64) float64 { return float64(a) + b })
	f3 := embedding.RegisterFunc3("f3", func(a, b, c int64) int64 { return a + b + c })
	f4 := embedding.RegisterFunc4("f4", func(a, b, c, d int64) int64 { return a + b + c + d })

	fErr := embedding.RegisterFunc1Err("f_err", func(x int64) (int64, error) {
		if x < 0 {
			return 0, errors.New("negative value")
		}
		return x * 10, nil
	})

	v := vm.NewVM()
	v.SetGlobal("f0", f0)
	v.SetGlobal("f1", f1)
	v.SetGlobal("f2", f2)
	v.SetGlobal("f3", f3)
	v.SetGlobal("f4", f4)
	v.SetGlobal("f_err", fErr)

	src := `
assert f0() == 10
assert f1(5) == 10
assert f2(10, 2.5) == 12.5
assert f3(1, 2, 3) == 6
assert f4(1, 2, 3, 4) == 10
assert f_err(5) == 50

caught = False
try:
    f_err(-1)
except RuntimeError as e:
    caught = True
assert caught
`
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Unhandled Python exception: %s", v.FormatException(exc))
	}
}

func BenchmarkStructFieldMutation(b *testing.B) {
	sb := embedding.RegisterStructType[Order]("BenchOrder")
	sb.RegisterFieldFloat64("price", func(o *Order) float64 { return o.Price }, func(o *Order, v float64) { o.Price = v })

	ord := &Order{Price: 10.0}
	pyOrd := sb.Wrap(ord)

	v := vm.NewVM()
	v.SetGlobal("order", pyOrd)

	src := `
def mutate_loop(order, n):
    i = 0
    while i < n:
        order.price += 1.5
        i += 1
`
	co, err := compiler.CompileString(src)
	if err != nil {
		b.Fatalf("CompileString failed: %v", err)
	}
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		b.Fatalf("Unhandled Python exception: %s", v.FormatException(exc))
	}

	mutateFn, ok := v.GetGlobal("mutate_loop")
	if !ok {
		b.Fatal("mutate_loop not found")
	}

	// Warm-up to warm inline caches
	args := []val.Value{pyOrd, val.Int64Val(10)}
	v.Vectorcall(mutateFn, args, nil)

	args[1] = val.Int64Val(int64(b.N))
	b.ResetTimer()
	b.ReportAllocs()
	v.Vectorcall(mutateFn, args, nil)
}
