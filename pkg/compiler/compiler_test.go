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
	for b.Loop() {
		res := v.Vectorcall(fibFn, args, nil)
		if !res.IsInt64() || res.AsInt64() != 75025 {
			b.Fatalf("unexpected fib result: %v", res)
		}
	}
}

func BenchmarkArenaReuse500Lines(b *testing.B) {
	// Generate a 500-line valid Python script
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "x_%d = (%d + %d) * 2\n", i, i, i+1)
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
	for b.Loop() {
		arena.Reset()
		parser.Reset(src)
		_, _ = parser.ParseModule()
	}
}

func BenchmarkFullCompile500Lines(b *testing.B) {
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "x_%d = (%d + %d) * 2\n", i, i, i+1)
	}
	src := []byte(sb.String())
	arena := NewASTArena(4096)

	b.ReportAllocs()
	for b.Loop() {
		arena.Reset()
		_, _ = Compile(src, arena)
	}
}

func TestTryExceptBasic(t *testing.T) {
	src := `
caught = False
val = 0
try:
    val = 1
    raise ValueError
    val = 2
except ValueError:
    caught = True
    val = 3
`
	_, v := runPython(t, src)
	c, ok := v.GetGlobal("caught")
	if !ok || !c.IsTruthy() {
		t.Fatalf("expected caught = True, got %v", c)
	}
	resVal, ok := v.GetGlobal("val")
	if !ok || !resVal.IsInt64() || resVal.AsInt64() != 3 {
		t.Fatalf("expected val = 3, got %v", resVal)
	}
}

func TestTryExceptAsName(t *testing.T) {
	src := `
err_name = ""
try:
    raise ValueError
except ValueError as e:
    err_name = "ValueError caught"
`
	_, v := runPython(t, src)
	res, ok := v.GetGlobal("err_name")
	if !ok || res.AsString() != "ValueError caught" {
		t.Fatalf("expected 'ValueError caught', got %v", res)
	}
}

func TestTryExceptSubclassMatching(t *testing.T) {
	src := `
res = ""
try:
    raise TypeError
except Exception:
    res = "caught by Exception"
`
	_, v := runPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || res.AsString() != "caught by Exception" {
		t.Fatalf("expected 'caught by Exception', got %v", res)
	}
}

func TestTryExceptElseFinally(t *testing.T) {
	// 1. Happy path: try -> else -> finally
	srcHappy := `
log = []
try:
    log.append(1)
except Exception:
    log.append(2)
else:
    log.append(3)
finally:
    log.append(4)
`
	_, vHappy := runPython(t, srcHappy)
	logVal, ok := vHappy.GetGlobal("log")
	if !ok || !logVal.IsList() {
		t.Fatalf("log is not a list: %v", logVal)
	}
	items := logVal.AsList().Items
	if len(items) != 3 || items[0].AsInt64() != 1 || items[1].AsInt64() != 3 || items[2].AsInt64() != 4 {
		t.Fatalf("happy path log = %v, want [1, 3, 4]", items)
	}

	// 2. Exception path: try -> except -> finally (no else)
	srcExc := `
log = []
try:
    log.append(1)
    raise KeyError
    log.append(2)
except KeyError:
    log.append(3)
else:
    log.append(4)
finally:
    log.append(5)
`
	_, vExc := runPython(t, srcExc)
	logVal2, ok := vExc.GetGlobal("log")
	if !ok || !logVal2.IsList() {
		t.Fatalf("log is not a list: %v", logVal2)
	}
	items2 := logVal2.AsList().Items
	if len(items2) != 3 || items2[0].AsInt64() != 1 || items2[1].AsInt64() != 3 || items2[2].AsInt64() != 5 {
		t.Fatalf("exception path log = %v, want [1, 3, 5]", items2)
	}
}

func TestNestedExceptionUnwindingMultiFrame(t *testing.T) {
	src := `
log = []

def h():
    log.append("h_enter")
    raise ZeroDivisionError
    log.append("h_exit")

def g():
    log.append("g_enter")
    h()
    log.append("g_exit")

def f():
    log.append("f_enter")
    try:
        g()
    except ZeroDivisionError:
        log.append("f_caught")
    finally:
        log.append("f_finally")

f()
`
	_, v := runPython(t, src)
	logVal, ok := v.GetGlobal("log")
	if !ok || !logVal.IsList() {
		t.Fatalf("log is not a list: %v", logVal)
	}
	items := logVal.AsList().Items
	expected := []string{"f_enter", "g_enter", "h_enter", "f_caught", "f_finally"}
	if len(items) != len(expected) {
		t.Fatalf("log length = %d, want %d: %v", len(items), len(expected), items)
	}
	for i, exp := range expected {
		if items[i].AsString() != exp {
			t.Fatalf("log[%d] = %s, want %s", i, items[i].AsString(), exp)
		}
	}
}

func TestExceptionChaining(t *testing.T) {
	src := `
caught = False
cause_is_type_error = False
try:
    try:
        raise TypeError
    except TypeError as e1:
        raise ValueError from e1
except ValueError as e2:
    caught = True
    cause = e2.__cause__
    cause_is_type_error = isinstance(cause, TypeError)
`
	_, v := runPython(t, src)
	c, ok := v.GetGlobal("caught")
	if !ok || !c.IsTruthy() {
		t.Fatalf("expected caught = True, got %v", c)
	}
	isTE, ok := v.GetGlobal("cause_is_type_error")
	if !ok || !isTE.IsTruthy() {
		t.Fatalf("expected cause_is_type_error = True, got %v", isTE)
	}
}

func TestWithStatementSuppressionAndPropagation(t *testing.T) {
	src := `
class SuppressManager:
    def __init__(self):
        self.entered = False
        self.exited = False
    def __enter__(self):
        self.entered = True
        return self
    def __exit__(self, exc_type, exc_val, tb):
        self.exited = True
        return True # Suppress!

class PropagateManager:
    def __init__(self):
        self.entered = False
        self.exited = False
    def __enter__(self):
        self.entered = True
        return self
    def __exit__(self, exc_type, exc_val, tb):
        self.exited = True
        return False # Do not suppress!

# Test suppression
supp_res = "before"
with SuppressManager() as m1:
    supp_res = "inside"
    raise RuntimeError
    supp_res = "after_raise"
supp_res = "suppressed_successfully"

# Test propagation
prop_caught = False
try:
    with PropagateManager() as m2:
        raise KeyError
except KeyError:
    prop_caught = True
`
	_, v := runPython(t, src)
	sRes, ok := v.GetGlobal("supp_res")
	if !ok || sRes.AsString() != "suppressed_successfully" {
		t.Fatalf("expected 'suppressed_successfully', got %v", sRes)
	}
	pCaught, ok := v.GetGlobal("prop_caught")
	if !ok || !pCaught.IsTruthy() {
		t.Fatalf("expected prop_caught = True, got %v", pCaught)
	}
}

func TestGeneratorBasicYieldAndSend(t *testing.T) {
	src := `
def gen_count():
    yield 10
    val = yield 20
    yield val * 2

g = gen_count()
v1 = next(g)
v2 = next(g)
v3 = g.send(5)
`
	_, v := runPython(t, src)
	v1, _ := v.GetGlobal("v1")
	v2, _ := v.GetGlobal("v2")
	v3, _ := v.GetGlobal("v3")
	if !v1.IsInt64() || v1.AsInt64() != 10 {
		t.Fatalf("v1 = %v, want 10", v1)
	}
	if !v2.IsInt64() || v2.AsInt64() != 20 {
		t.Fatalf("v2 = %v, want 20", v2)
	}
	if !v3.IsInt64() || v3.AsInt64() != 10 {
		t.Fatalf("v3 = %v, want 10 (5 * 2)", v3)
	}
}

func TestGeneratorForLoopIteration(t *testing.T) {
	src := `
def my_range(n):
    i = 0
    while i < n:
        yield i
        i = i + 1

res = []
for x in my_range(5):
    res.append(x)
`
	_, v := runPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsList() {
		t.Fatalf("res is not a list: %v", res)
	}
	items := res.AsList().Items
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	for i := range 5 {
		if items[i].AsInt64() != int64(i) {
			t.Fatalf("items[%d] = %v, want %d", i, items[i], i)
		}
	}
}

func TestGeneratorYieldFrom(t *testing.T) {
	src := `
def subgen():
    yield 1
    yield 2
    return 42

def delegator():
    yield 0
    ret = yield from subgen()
    yield ret
    yield 100

res = []
for x in delegator():
    res.append(x)
`
	_, v := runPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsList() {
		t.Fatalf("res is not a list: %v", res)
	}
	items := res.AsList().Items
	expected := []int64{0, 1, 2, 42, 100}
	if len(items) != len(expected) {
		t.Fatalf("len(items) = %d, want %d: %v", len(items), len(expected), items)
	}
	for i, exp := range expected {
		if items[i].AsInt64() != exp {
			t.Fatalf("items[%d] = %d, want %d", i, items[i].AsInt64(), exp)
		}
	}
}

func TestZeroDivisionErrorAutomaticUnwind(t *testing.T) {
	src := `
caught = False
try:
    x = 10 / 0
except ZeroDivisionError:
    caught = True
`
	_, v := runPython(t, src)
	c, ok := v.GetGlobal("caught")
	if !ok || !c.IsTruthy() {
		t.Fatalf("expected ZeroDivisionError caught, got %v", c)
	}
}
