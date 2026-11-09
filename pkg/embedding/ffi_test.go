package embedding_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/embedding"
	"github.com/servusdei2018/fython/pkg/val"
)

type Counter struct {
	Count int64
}

func (c *Counter) Add(x int64) int64 {
	c.Count += x
	return c.Count
}

func (c *Counter) Reset() {
	c.Count = 0
}

func TestGenericBindings(t *testing.T) {
	// BindFunc0
	f0 := embedding.BindFunc0("get_num", func() int64 {
		return 42
	})
	res0 := f0.AsBuiltinFunc().Fn(nil, f0, nil, nil)
	if !res0.IsInt64() || res0.AsInt64() != 42 {
		t.Fatalf("BindFunc0 got %v, want 42", res0)
	}

	// BindFunc1
	f1 := embedding.BindFunc1("double", func(x int64) int64 {
		return x * 2
	})
	res1 := f1.AsBuiltinFunc().Fn(nil, f1, []val.Value{val.Int64Val(21)}, nil)
	if !res1.IsInt64() || res1.AsInt64() != 42 {
		t.Fatalf("BindFunc1 got %v, want 42", res1)
	}

	// BindFunc2
	f2 := embedding.BindFunc2("add", func(a int64, b float64) float64 {
		return float64(a) + b
	})
	res2 := f2.AsBuiltinFunc().Fn(nil, f2, []val.Value{val.Int64Val(10), val.Float64Val(2.5)}, nil)
	if !res2.IsFloat64() || res2.AsFloat64() != 12.5 {
		t.Fatalf("BindFunc2 got %v, want 12.5", res2)
	}

	// BindFunc3
	f3 := embedding.BindFunc3("greet", func(prefix string, id int64, excited bool) string {
		s := prefix + " " + string(rune('0'+id))
		if excited {
			s += "!"
		}
		return s
	})
	res3 := f3.AsBuiltinFunc().Fn(nil, f3, []val.Value{val.NewStringVal("Worker"), val.Int64Val(7), val.BoolVal(true)}, nil)
	if !res3.IsAnyString() || res3.AsString() != "Worker 7!" {
		t.Fatalf("BindFunc3 got %v, want 'Worker 7!'", res3.AsString())
	}

	// BindAction1
	var sideEffect int64
	act := embedding.BindAction1("record", func(v int64) {
		sideEffect = v
	})
	resAct := act.AsBuiltinFunc().Fn(nil, act, []val.Value{val.Int64Val(999)}, nil)
	if !resAct.IsNone() || sideEffect != 999 {
		t.Fatalf("BindAction1 failed, sideEffect=%v", sideEffect)
	}
}

func TestStructBinding(t *testing.T) {
	sb := embedding.NewStructBinding[Counter]("Counter")
	embedding.BindMethod1R(sb, "add", func(self *Counter, x int64) int64 {
		return self.Add(x)
	})
	embedding.BindMethod0(sb, "reset", func(self *Counter) {
		self.Reset()
	})

	c := &Counter{Count: 10}
	pyObj := sb.Wrap(c)

	// Call method add(5)
	addMethod, ok := sb.Type().Dict.Get(val.InternString("add"))
	if !ok {
		t.Fatal("method 'add' not found in type dict")
	}
	res := addMethod.AsBuiltinFunc().Fn(nil, addMethod, []val.Value{pyObj, val.Int64Val(5)}, nil)
	if !res.IsInt64() || res.AsInt64() != 15 || c.Count != 15 {
		t.Fatalf("addMethod result=%v, count=%v, want 15", res, c.Count)
	}

	// Call method reset()
	resetMethod, ok := sb.Type().Dict.Get(val.InternString("reset"))
	if !ok {
		t.Fatal("method 'reset' not found in type dict")
	}
	resetMethod.AsBuiltinFunc().Fn(nil, resetMethod, []val.Value{pyObj}, nil)
	if c.Count != 0 {
		t.Fatalf("resetMethod count=%v, want 0", c.Count)
	}
}

func BenchmarkGenericDirectCall(b *testing.B) {
	bound := embedding.BindFunc2("add", func(a, b int64) int64 {
		return a + b
	})
	fn := bound.AsBuiltinFunc().Fn
	args := []val.Value{val.Int64Val(10), val.Int64Val(20)}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		res := fn(nil, bound, args, nil)
		if res.AsInt64() != 30 {
			b.Fatal("wrong result")
		}
	}
}
