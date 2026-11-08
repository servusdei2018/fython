import asyncio
import time

async def add(a, b):
    return a + b

async def bench_async_loop(n):
    total = 0
    i = 0
    while i < n:
        total = total + (await add(i, 1))
        i = i + 1
    return total

def run_benchmark(n=2000):
    t0 = time.perf_counter()
    res = asyncio.run(bench_async_loop(n))
    elapsed = time.perf_counter() - t0
    return res, elapsed

if __name__ == "__main__":
    res, elapsed = run_benchmark(2000)
    print(f"Asyncio Benchmark completed in {elapsed*1000:.2f} ms (result: {res})")
