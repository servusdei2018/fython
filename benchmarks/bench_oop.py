import time

class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y

    def dist_sq(self):
        return self.x * self.x + self.y * self.y

def run_benchmark(n=50000):
    p = Point(1, 2)
    s = 0
    t0 = time.perf_counter()
    for i in range(n):
        p.x = i
        p.y = i + 1
        s += p.dist_sq()
    elapsed = time.perf_counter() - t0
    return s, elapsed

if __name__ == "__main__":
    checksum, elapsed = run_benchmark(50000)
    print(f"OOP Benchmark completed in {elapsed*1000:.2f} ms (checksum: {checksum})")
