#!/usr/bin/env python3
import os
import re
import resource
import shutil
import subprocess
import sys
import time

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.abspath(os.path.join(SCRIPT_DIR, ".."))
FYTHON_BIN = os.path.join(REPO_ROOT, "fython")
PYTHON3_BIN = shutil.which("python3")

BENCHMARKS = [
    ("OOP Attribute Mutation (Point)", "bench_oop.py"),
    ("Stream & File I/O (StringIO + File)", "bench_io.py"),
    ("N-Queens Backtracking (N=10)", "bench_nqueens.py"),
    ("Asyncio Coroutines (await add)", "bench_async.py"),
    ("Binary MemoryView Slicing (10MB)", "bench_binary.py"),
    ("JSON Parsing (1000 items)", "bench_json.py"),
]

# Workload numbers are the minimum of RUNS interleaved A/B runs of each
# script's internal timed loop (process startup and payload allocation
# excluded). Cold start is the mean of COLD_START_RUNS runs.
RUNS = 20
COLD_START_RUNS = 10


def ensure_fython_binary():
    if not os.path.isfile(FYTHON_BIN):
        print(f"[*] Compiling fython binary at {FYTHON_BIN}...")
        subprocess.check_call(["go", "build", "-o", FYTHON_BIN, "./cmd/fython"], cwd=REPO_ROOT)
    print(f"[*] Fython binary verified: {FYTHON_BIN}")


def measure_cold_start(executable, runs=COLD_START_RUNS):
    times = []
    for _ in range(runs):
        t0 = time.perf_counter()
        subprocess.check_call([executable, "-c", "pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        times.append(time.perf_counter() - t0)
    return (sum(times) / len(times)) * 1000.0


def measure_rss(executable):
    pid = os.fork()
    if pid == 0:
        os.execv(executable, [executable, "-c", "import sys"])
    _, _, ru = os.wait4(pid, 0)
    # ru_maxrss is in kilobytes on Linux
    return ru.ru_maxrss / 1024.0


def internal_time_ms(executable, script_path):
    """Run one benchmark script and return its internal timed-loop result."""
    out = subprocess.check_output([executable, script_path], text=True, stderr=subprocess.DEVNULL)
    match = re.search(r"completed in ([\d.]+) ms", out)
    if not match:
        raise RuntimeError(f"could not parse benchmark output from {executable}: {out!r}")
    return float(match.group(1))


def measure_workload(script_path, runs=RUNS):
    """Interleaved A/B: run both interpreters `runs` times each, return minima."""
    fy_times = []
    py_times = []
    for _ in range(runs):
        fy_times.append(internal_time_ms(FYTHON_BIN, script_path))
        if PYTHON3_BIN:
            py_times.append(internal_time_ms(PYTHON3_BIN, script_path))
    fy_ms = min(fy_times)
    py_ms = min(py_times) if py_times else float("nan")
    return fy_ms, py_ms


def main():
    ensure_fython_binary()

    py_version = "CPython 3.15.0"
    if PYTHON3_BIN:
        try:
            out = subprocess.check_output([PYTHON3_BIN, "--version"], text=True).strip()
            py_version = out
        except Exception:
            pass

    print(f"\n==========================================================================")
    print(f"  Fython 3.15.0 vs {py_version} Head-to-Head Performance Suite")
    print(f"==========================================================================\n")

    print(f"[*] Methodology: {RUNS} interleaved A/B runs per workload (internal timed")
    print(f"    loop, min); cold start = mean of {COLD_START_RUNS} runs; peak RSS = wait4.\n")

    print("[1/3] Measuring cold startup latency...")
    fy_start = measure_cold_start(FYTHON_BIN)
    py_start = measure_cold_start(PYTHON3_BIN) if PYTHON3_BIN else float("nan")

    print("[2/3] Measuring base process RSS...")
    fy_rss = measure_rss(FYTHON_BIN)
    py_rss = measure_rss(PYTHON3_BIN) if PYTHON3_BIN else float("nan")

    print(f"[3/3] Running {len(BENCHMARKS)} core benchmark workloads ({RUNS} interleaved runs each)...")
    workload_results = []
    for name, script_file in BENCHMARKS:
        path = os.path.join(SCRIPT_DIR, script_file)
        print(f"  -> Running {name}...")
        fy_ms, py_ms = measure_workload(path)
        ratio = py_ms / fy_ms if fy_ms > 0 else 1.0
        workload_results.append((name, fy_ms, py_ms, ratio))

    print("\n" + "=" * 80)
    print(f"| Workload Domain                       | Fython (Go)   | {py_version:<15} | Comparison   |")
    print("|" + "-" * 39 + "|" + "-" * 15 + "|" + "-" * 17 + "|" + "-" * 14 + "|")

    start_comp = f"{py_start/fy_start:.2f}x faster" if fy_start < py_start else f"{fy_start/py_start:.2f}x slower"
    print(f"| Cold Startup Latency (-c 'pass')     | {fy_start:8.2f} ms   | {py_start:8.2f} ms     | {start_comp:<12} |")

    rss_comp = f"{fy_rss/py_rss:.2f}x heavier" if fy_rss > py_rss else f"{py_rss/fy_rss:.2f}x lighter"
    print(f"| Base Memory Footprint (Peak RSS)      | {fy_rss:8.2f} MB   | {py_rss:8.2f} MB     | {rss_comp:<12} |")

    for name, fy_ms, py_ms, ratio in workload_results:
        comp_str = f"{ratio:.2f}x faster" if ratio >= 1.0 else f"{1.0/ratio:.2f}x slower"
        print(f"| {name:<37} | {fy_ms:8.2f} ms   | {py_ms:8.2f} ms     | {comp_str:<12} |")
    print("=" * 80 + "\n")


if __name__ == "__main__":
    main()
