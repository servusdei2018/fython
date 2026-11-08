import time

def solve_nqueens(n):
    count = 0
    cols = [False] * n
    diag1 = [False] * (2 * n)
    diag2 = [False] * (2 * n)
    def solve(row):
        nonlocal count
        if row == n:
            count += 1
            return
        for col in range(n):
            d1 = row + col
            d2 = row - col + n
            if not cols[col] and not diag1[d1] and not diag2[d2]:
                cols[col] = True
                diag1[d1] = True
                diag2[d2] = True
                solve(row + 1)
                cols[col] = False
                diag1[d1] = False
                diag2[d2] = False
    solve(0)
    return count

def run_benchmark(n=10):
    t0 = time.perf_counter()
    solutions = solve_nqueens(n)
    elapsed = time.perf_counter() - t0
    return solutions, elapsed

if __name__ == "__main__":
    solutions, elapsed = run_benchmark(10)
    print(f"N-Queens (N=10) Benchmark completed in {elapsed*1000:.2f} ms ({solutions} solutions)")
