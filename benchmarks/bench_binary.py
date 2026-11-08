import time

def run_benchmark():
    payload_size = 10 * 1024 * 1024  # 10 MB
    raw = bytearray(b"x" * payload_size)
    mv = memoryview(raw)
    t0 = time.perf_counter()
    total = 0
    for i in range(5000):
        start = (i * 100) % (payload_size - 100000)
        sub = mv[start:start+100000]
        total += len(sub)
    elapsed = time.perf_counter() - t0
    return total, elapsed

if __name__ == "__main__":
    total, elapsed = run_benchmark()
    print(f"Binary Zero-Copy Benchmark completed in {elapsed*1000:.2f} ms ({total} bytes)")
