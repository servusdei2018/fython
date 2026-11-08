import io
import os
import tempfile
import time

def run_benchmark():
    tmp_path = os.path.join(tempfile.gettempdir(), f"fython_bench_io_{os.getpid()}.txt")
    t0 = time.perf_counter()
    try:
        s = io.StringIO()
        for _ in range(5000):
            s.write("data line for benchmark\n")
        s_val = s.getvalue()

        with open(tmp_path, "w") as f:
            for _ in range(2000):
                f.write("content line\n")

        with open(tmp_path, "r") as f:
            content = f.read()
    finally:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
    elapsed = time.perf_counter() - t0
    return len(s_val) + len(content), elapsed

if __name__ == "__main__":
    total_bytes, elapsed = run_benchmark()
    print(f"Stream and File I/O Benchmark completed in {elapsed*1000:.2f} ms ({total_bytes} bytes)")
