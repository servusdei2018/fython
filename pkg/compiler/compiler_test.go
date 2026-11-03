package compiler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runPython(t *testing.T, src string) (val.Value, *vm.VM) {
	t.Helper()
	co, err := CompileString(src)
	if err != nil {
		t.Fatalf("CompileString failed: %v", err)
	}
	v := vm.NewVM()
	res := v.RunCode(co)
	return res, v
}

func TestEndToEndFibonacci(t *testing.T) {
	src := `
def fib(n):
    if n < 2:
        return n
    return fib(n - 1) + fib(n - 2)

res = fib(30)
`
	_, v := runPython(t, src)
	got, ok := v.GetGlobal("res")
	if !ok {
		t.Fatalf("global 'res' not found")
	}
	if !got.IsInt64() || got.AsInt64() != 832040 {
		t.Fatalf("fib(30) = %v, want 832040", got)
	}
}

func TestEndToEndClosuresAndNonlocal(t *testing.T) {
	src := `
def make_counter(start):
    count = start
    def inc(step):
        nonlocal count
        count = count + step
        return count
    def get():
        return count
    return inc, get

inc, get = make_counter(10)
r1 = inc(5)
r2 = inc(20)
r3 = get()
`
	_, v := runPython(t, src)

	r1, _ := v.GetGlobal("r1")
	if !r1.IsInt64() || r1.AsInt64() != 15 {
		t.Fatalf("r1 = %v, want 15", r1)
	}
	r2, _ := v.GetGlobal("r2")
	if !r2.IsInt64() || r2.AsInt64() != 35 {
		t.Fatalf("r2 = %v, want 35", r2)
	}
	r3, _ := v.GetGlobal("r3")
	if !r3.IsInt64() || r3.AsInt64() != 35 {
		t.Fatalf("r3 = %v, want 35", r3)
	}
}

func TestEndToEndListAndDictComprehensions(t *testing.T) {
	src := `
nums = [1, 2, 3, 4, 5]
odds_squared = [x * x for x in nums if x % 2 == 1]
doubled_map = {x: x * 2 for x in [1, 2, 3]}
`
	_, v := runPython(t, src)

	resList, ok := v.GetGlobal("odds_squared")
	if !ok || !resList.IsList() {
		t.Fatalf("odds_squared is not a list: %v", resList)
	}
	items := resList.AsList().Items
	if len(items) != 3 || items[0].AsInt64() != 1 || items[1].AsInt64() != 9 || items[2].AsInt64() != 25 {
		t.Fatalf("odds_squared = %v, want [1, 9, 25]", items)
	}

	resDict, ok := v.GetGlobal("doubled_map")
	if !ok || !resDict.IsDict() {
		t.Fatalf("doubled_map is not a dict: %v", resDict)
	}
	d := resDict.AsDict()
	if d.Len() != 3 {
		t.Fatalf("doubled_map len = %d, want 3", d.Len())
	}
	val1, _ := d.Get(val.Int64Val(1))
	val2, _ := d.Get(val.Int64Val(2))
	val3, _ := d.Get(val.Int64Val(3))
	if val1.AsInt64() != 2 || val2.AsInt64() != 4 || val3.AsInt64() != 6 {
		t.Fatalf("doubled_map values = %v, %v, %v, want 2, 4, 6", val1, val2, val3)
	}
}

func TestEndToEndClassInheritance(t *testing.T) {
	src := `
class Animal:
    def __init__(self, name):
        self.name = name
    def speak(self):
        return "..."

class Dog(Animal):
    def speak(self):
        return self.name + " barks"

d = Dog("Buddy")
msg = d.speak()
`
	_, v := runPython(t, src)

	msg, ok := v.GetGlobal("msg")
	if !ok {
		t.Fatalf("global 'msg' not found")
	}
	if !msg.IsAnyString() || msg.AsString() != "Buddy barks" {
		t.Fatalf("Dog.speak() = %q, want \"Buddy barks\"", msg.AsString())
	}
}

func TestEndToEndControlFlow(t *testing.T) {
	src := `
# if/elif/else test
x = 10
if x < 5:
    res_if = "less"
elif x == 10:
    res_if = "equal"
else:
    res_if = "greater"

# while with break and continue
total_while = 0
i = 0
while i < 10:
    i = i + 1
    if i % 2 == 0:
        continue
    if i > 7:
        break
    total_while = total_while + i

# for loop with range
total_for = 0
for k in range(5):
    total_for = total_for + k
`
	_, v := runPython(t, src)

	resIf, _ := v.GetGlobal("res_if")
	if resIf.AsString() != "equal" {
		t.Fatalf("res_if = %q, want \"equal\"", resIf.AsString())
	}

	totalWhile, _ := v.GetGlobal("total_while")
	// odd numbers: i=1 (total=1), i=3 (total=4), i=5 (total=9), i=7 (total=16), i=9 (>7 -> break)
	if totalWhile.AsInt64() != 16 {
		t.Fatalf("total_while = %v, want 16", totalWhile)
	}

	totalFor, _ := v.GetGlobal("total_for")
	// 0 + 1 + 2 + 3 + 4 = 10
	if totalFor.AsInt64() != 10 {
		t.Fatalf("total_for = %v, want 10", totalFor)
	}
}

func TestConstantDeduplicationTypeSensitivity(t *testing.T) {
	src := `
a = 1
b = 1.0
c = True
`
	co, err := CompileString(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	// In co.Consts, 1 (int), 1.0 (float), and True (bool) must be distinct slots
	var foundInt, foundFloat, foundBool bool
	for _, c := range co.Consts {
		if c.Kind() == val.KindInt64 && c.AsInt64() == 1 {
			foundInt = true
		}
		if c.Kind() == val.KindFloat64 && c.AsFloat64() == 1.0 {
			foundFloat = true
		}
		if c.Kind() == val.KindBool && c.AsBool() == true {
			foundBool = true
		}
	}
	if !foundInt || !foundFloat || !foundBool {
		t.Fatalf("constants not strictly separated: int=%v, float=%v, bool=%v", foundInt, foundFloat, foundBool)
	}
}

func BenchmarkFib25CompiledZeroAlloc(b *testing.B) {
	src := `
def fib(n):
    if n < 2:
        return n
    return fib(n - 1) + fib(n - 2)
`
	co, err := CompileString(src)
	if err != nil {
		b.Fatalf("compile failed: %v", err)
	}

	v := vm.NewVM()
	v.RunCode(co) // Defines fib in v.Globals

	fibFn, ok := v.GetGlobal("fib")
	if !ok {
		b.Fatalf("fib function not found in globals")
	}

	// Warm up inline cache and quickened opcodes
	args := []val.Value{val.Int64Val(25)}
	warmup := v.Vectorcall(fibFn, args, nil)
	if !warmup.IsInt64() || warmup.AsInt64() != 75025 {
		b.Fatalf("warmup fib(25) = %v, want 75025", warmup)
	}

	// Verify 0 allocs per execution
	allocs := testing.AllocsPerRun(10, func() {
		res := v.Vectorcall(fibFn, args, nil)
		if !res.IsInt64() || res.AsInt64() != 75025 {
			b.Fatalf("unexpected fib result: %v", res)
		}
	})
	if allocs != 0 {
		b.Fatalf("fib(25) allocated %v times, want 0", allocs)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := v.Vectorcall(fibFn, args, nil)
		if !res.IsInt64() || res.AsInt64() != 75025 {
			b.Fatalf("unexpected fib result: %v", res)
		}
	}
}

func BenchmarkArenaReuse500Lines(b *testing.B) {
	// Generate a 500-line valid Python script
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString(fmt.Sprintf("x_%d = (%d + %d) * 2\n", i, i, i+1))
	}
	src := []byte(sb.String())

	arena := NewASTArena(4096)
	parser := NewParser(src, arena)
	_, _ = parser.ParseModule()

	// Verify 0 per-node allocations when reusing arena during AST parsing
	parseAllocs := testing.AllocsPerRun(10, func() {
		arena.Reset()
		parser.Reset(src)
		_, err := parser.ParseModule()
		if err != nil {
			b.Fatalf("parse failed: %v", err)
		}
	})
	if parseAllocs != 0 {
		b.Fatalf("reused arena parsing allocated %v times, want 0", parseAllocs)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		arena.Reset()
		parser.Reset(src)
		_, _ = parser.ParseModule()
	}
}

func BenchmarkFullCompile500Lines(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString(fmt.Sprintf("x_%d = (%d + %d) * 2\n", i, i, i+1))
	}
	src := []byte(sb.String())
	arena := NewASTArena(4096)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		arena.Reset()
		_, _ = Compile(src, arena)
	}
}
