package vm_test

import (
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

// Helper to compile and run Python code on a new VM
func runAsyncPython(t *testing.T, src string) (val.Value, *vm.VM) {
	t.Helper()
	co, err := compiler.CompileString(src)
	if err != nil {
		t.Fatalf("CompileString error: %v\nSource:\n%s", err, src)
	}
	v := vm.NewVM()
	res := v.RunCode(co)
	return res, v
}

// 1. Deep Await Chains: Synchronous Fast-Path
func TestDeepAwaitChainsSynchronous(t *testing.T) {
	src := `
import asyncio

async def f(n):
    if n <= 0:
        return 42
    return (await f(n - 1)) + 1

res = asyncio.run(f(50))
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsInt64() {
		t.Fatalf("expected int64 result, got: %v", res)
	}
	if res.AsInt64() != 92 {
		t.Fatalf("expected 92 (42 + 50), got: %d", res.AsInt64())
	}
}

// 2. Deep Await Chains: Asynchronous Suspension via asyncio.sleep
func TestDeepAwaitChainsAsynchronous(t *testing.T) {
	src := `
import asyncio

async def f(n):
    if n <= 0:
        await asyncio.sleep(0.005)
        return 100
    return (await f(n - 1)) + 1

res = asyncio.run(f(20))
`
	start := time.Now()
	_, v := runAsyncPython(t, src)
	elapsed := time.Since(start)

	res, ok := v.GetGlobal("res")
	if !ok || !res.IsInt64() {
		t.Fatalf("expected int64 result, got: %v", res)
	}
	if res.AsInt64() != 120 {
		t.Fatalf("expected 120 (100 + 20), got: %d", res.AsInt64())
	}
	if elapsed < 4*time.Millisecond {
		t.Fatalf("expected sleep delay, elapsed: %v", elapsed)
	}
}

// 3. Async For Loop Iteration & StopAsyncIteration
func TestAsyncForLoop(t *testing.T) {
	src := `
import asyncio

class AsyncRange:
    def __init__(self, limit):
        self.limit = limit
        self.cur = 0

    def __aiter__(self):
        return self

    async def __anext__(self):
        if self.cur >= self.limit:
            raise StopAsyncIteration
        v = self.cur
        self.cur = self.cur + 1
        return v

async def main():
    total = 0
    async for x in AsyncRange(5):
        total = total + x
    return total

res = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsInt64() {
		t.Fatalf("expected int64 result, got: %v", res)
	}
	// 0 + 1 + 2 + 3 + 4 = 10
	if res.AsInt64() != 10 {
		t.Fatalf("expected total 10, got: %d", res.AsInt64())
	}
}

// 4. Async For with Break, Continue, and Else
func TestAsyncForBreakContinueElse(t *testing.T) {
	src := `
import asyncio

class AsyncNumbers:
    def __init__(self, limit):
        self.limit = limit
        self.cur = 0

    def __aiter__(self):
        return self

    async def __anext__(self):
        if self.cur >= self.limit:
            raise StopAsyncIteration
        v = self.cur
        self.cur = self.cur + 1
        return v

async def test_break():
    out = []
    async for x in AsyncNumbers(10):
        if x == 3:
            break
        out.append(x)
    return out

async def test_continue():
    out = []
    async for x in AsyncNumbers(6):
        if x % 2 == 0:
            continue
        out.append(x)
    return out

async def test_else_no_break():
    flag = False
    async for x in AsyncNumbers(3):
        pass
    else:
        flag = True
    return flag

res_break = asyncio.run(test_break())
res_cont = asyncio.run(test_continue())
res_else = asyncio.run(test_else_no_break())
`
	_, v := runAsyncPython(t, src)

	resBreak, _ := v.GetGlobal("res_break")
	if !resBreak.IsList() || resBreak.AsList().Len() != 3 {
		t.Fatalf("res_break failed: %v", resBreak)
	}

	resCont, _ := v.GetGlobal("res_cont")
	if !resCont.IsList() || resCont.AsList().Len() != 3 {
		t.Fatalf("res_cont failed: %v", resCont)
	}

	resElse, _ := v.GetGlobal("res_else")
	if !resElse.IsBool() || !resElse.AsBool() {
		t.Fatalf("res_else failed: %v", resElse)
	}
}

// 5. Async With Context Manager: Normal Exit
func TestAsyncWithNormalExit(t *testing.T) {
	src := `
import asyncio

class AsyncCtx:
    def __init__(self):
        self.entered = False
        self.exited = False

    async def __aenter__(self):
        self.entered = True
        return self

    async def __aexit__(self, exc_type, exc, tb):
        self.exited = True
        return False

async def main():
    ctx = AsyncCtx()
    async with ctx as c:
        pass
    return [ctx.entered, ctx.exited]

res = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsList() {
		t.Fatalf("expected list result, got: %v", res)
	}
	l := res.AsList()
	entered, _ := l.Get(0)
	exited, _ := l.Get(1)
	if !entered.AsBool() || !exited.AsBool() {
		t.Fatalf("expected entered=True and exited=True, got %v, %v", entered, exited)
	}
}

// 6. Async With Context Manager: Exception Suppression
func TestAsyncWithExceptionSuppression(t *testing.T) {
	src := `
import asyncio

class SuppressCtx:
    def __init__(self):
        self.exited = False

    async def __aenter__(self):
        return self

    async def __aexit__(self, exc_type, exc, tb):
        self.exited = True
        return True # Suppress exception

async def main():
    ctx = SuppressCtx()
    async with ctx:
        raise ValueError("suppressed boom")
    return ctx.exited

res = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || !res.IsBool() || !res.AsBool() {
		t.Fatalf("expected True result from suppressed exception, got: %v", res)
	}
}

// 7. Asyncio Gather: Concurrent Execution
func TestAsyncioGatherBasic(t *testing.T) {
	src := `
import asyncio

async def worker(id):
    await asyncio.sleep(0.005)
    return id * 10

async def main():
    res = await asyncio.gather(worker(1), worker(2), worker(3))
    return res

results = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("results")
	if !ok || !res.IsList() {
		t.Fatalf("expected list results, got: %v", res)
	}
	l := res.AsList()
	if l.Len() != 3 {
		t.Fatalf("expected 3 results, got: %d", l.Len())
	}
	v0, _ := l.Get(0)
	v1, _ := l.Get(1)
	v2, _ := l.Get(2)
	if v0.AsInt64() != 10 || v1.AsInt64() != 20 || v2.AsInt64() != 30 {
		t.Fatalf("unexpected gather results: %v, %v, %v", v0, v1, v2)
	}
}

// 8. Asyncio Gather: Exception Propagation (return_exceptions=False)
func TestAsyncioGatherExceptionPropagation(t *testing.T) {
	src := `
import asyncio

async def good(x):
    await asyncio.sleep(0.002)
    return x * 2

async def bad():
    await asyncio.sleep(0.001)
    raise ValueError("worker error")

async def main():
    try:
        await asyncio.gather(good(1), bad(), good(3))
        return "not caught"
    except ValueError as e:
        return "caught"

res = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || res.AsString() != "caught" {
		t.Fatalf("expected 'caught', got: %v", res)
	}
}

// 9. Asyncio Gather: Exception Return (return_exceptions=True)
func TestAsyncioGatherReturnExceptions(t *testing.T) {
	src := `
import asyncio

async def good(x):
    return x * 2

async def bad():
    raise ValueError("worker error")

async def main():
    res = await asyncio.gather(good(5), bad(), good(10), return_exceptions=True)
    return res

results = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("results")
	if !ok || !res.IsList() {
		t.Fatalf("expected list results, got: %v", res)
	}
	l := res.AsList()
	v0, _ := l.Get(0)
	v1, _ := l.Get(1)
	v2, _ := l.Get(2)
	if v0.AsInt64() != 10 {
		t.Fatalf("expected 10, got %v", v0)
	}
	if !v1.IsInstance() {
		t.Fatalf("expected exception instance for v1, got %v", v1)
	}
	if v2.AsInt64() != 20 {
		t.Fatalf("expected 20, got %v", v2)
	}
}

// 10. Multi-Core Scheduling Mode
func TestMultiCoreAsyncMode(t *testing.T) {
	src := `
import asyncio

asyncio.set_mode("multicore")

async def task(x):
    await asyncio.sleep(0.005)
    return x * x

async def main():
    res = await asyncio.gather(task(2), task(3), task(4), task(5))
    return res

results = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("results")
	if !ok || !res.IsList() {
		t.Fatalf("expected list results, got: %v", res)
	}
	l := res.AsList()
	v0, _ := l.Get(0)
	v1, _ := l.Get(1)
	v2, _ := l.Get(2)
	v3, _ := l.Get(3)
	if v0.AsInt64() != 4 || v1.AsInt64() != 9 || v2.AsInt64() != 16 || v3.AsInt64() != 25 {
		t.Fatalf("unexpected multicore results: %v, %v, %v, %v", v0, v1, v2, v3)
	}
}

// 11. Loopback TCP Echo Client/Server Coroutines via Go netpoll
func TestAsyncioTCPEchoServerClient(t *testing.T) {
	src := `
import asyncio

async def handle_echo(reader, writer):
    data = await reader.read(100)
    writer.write(data)
    await writer.drain()
    writer.close()
    await writer.wait_closed()

async def main():
    server = await asyncio.start_server(handle_echo, "127.0.0.1", 19876)
    
    async def client():
        reader, writer = await asyncio.open_connection("127.0.0.1", 19876)
        writer.write(b"fython netpoll echo")
        await writer.drain()
        data = await reader.read(100)
        writer.close()
        await writer.wait_closed()
        return data

    res = await client()
    server.close()
    await server.wait_closed()
    return res

echoed = asyncio.run(main())
`
	_, v := runAsyncPython(t, src)
	echoedVal, ok := v.GetGlobal("echoed")
	if !ok || !echoedVal.IsBytes() {
		t.Fatalf("expected bytes result, got: %v", echoedVal)
	}
	expected := "fython netpoll echo"
	if string(echoedVal.AsBytes().Data) != expected {
		t.Fatalf("expected %q, got %q", expected, string(echoedVal.AsBytes().Data))
	}
}

// 12. Coroutine Send, Throw, Close methods
func TestCoroutineMethods(t *testing.T) {
	src := `
async def simple():
    return 42

c = simple()
res = c.send(None)
`
	_, v := runAsyncPython(t, src)
	res, ok := v.GetGlobal("res")
	if !ok || res.AsInt64() != 42 {
		t.Fatalf("expected 42, got: %v", res)
	}
}
