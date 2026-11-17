#!/usr/bin/env python3
import argparse
import os
import re
import resource
import shutil
import statistics
import subprocess
import sys
import time

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.abspath(os.path.join(SCRIPT_DIR, ".."))
FYTHON_BIN = os.path.join(REPO_ROOT, "bin", "fython")
if not os.path.isfile(FYTHON_BIN):
    FYTHON_BIN = os.path.join(REPO_ROOT, "fython")
PYTHON3_BIN = shutil.which("python3")
UV_BIN = shutil.which("uv")
VENV_DIR = os.path.join(SCRIPT_DIR, ".venv")
VENV_PYTHON = os.path.join(VENV_DIR, "bin", "python")

BENCHMARKS = [
    ("Jinja2 HTML Template Engine", "bench_jinja.py"),
    ("ASGI Web Microservice Lifecycle", "bench_web_asgi.py"),
    ("Data Model Validation & JSON ETL", "bench_etl_validation.py"),
    ("Syntax Lexing & Console Layout", "bench_rich_lex.py"),
    ("PEP 508 Dependency Resolution", "bench_packaging_resolve.py"),
    ("N-Queens Backtracking (N=10)", "bench_nqueens.py"),
    ("Binary MemoryView Slicing (10MB)", "bench_binary.py"),
]

RUNS = 20
COLD_START_RUNS = 10


def ensure_fython_binary():
    if not os.path.isfile(FYTHON_BIN):
        print(f"[*] Compiling fython binary at {FYTHON_BIN}...")
        subprocess.check_call(["go", "build", "-o", FYTHON_BIN, "./cmd/fython"], cwd=REPO_ROOT)
    print(f"[*] Fython binary verified: {FYTHON_BIN}")


def ensure_venv():
    pyproject = os.path.join(SCRIPT_DIR, "pyproject.toml")
    if not os.path.isdir(VENV_DIR) or not os.path.isfile(VENV_PYTHON):
        print("[*] Creating benchmark virtual environment (uv sync)...")
        subprocess.check_call([UV_BIN or "uv", "sync"], cwd=SCRIPT_DIR)
        return
    # Re-sync if pyproject is newer than the venv.
    if os.path.getmtime(pyproject) > os.path.getmtime(VENV_DIR):
        print("[*] Re-syncing benchmark virtual environment (uv sync)...")
        subprocess.check_call([UV_BIN or "uv", "sync"], cwd=SCRIPT_DIR)


def bench_env():
    env = os.environ.copy()
    env["VIRTUAL_ENV"] = VENV_DIR
    venv_bin = os.path.join(VENV_DIR, "bin")
    env["PATH"] = venv_bin + os.pathsep + env.get("PATH", "")
    return env


def python_for(executable):
    if executable == PYTHON3_BIN and os.path.isfile(VENV_PYTHON):
        return VENV_PYTHON
    return executable


def measure_cold_start(executable, runs=COLD_START_RUNS, env=None):
    times = []
    for _ in range(runs):
        t0 = time.perf_counter()
        subprocess.check_call([executable, "-c", "pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
        times.append(time.perf_counter() - t0)
    return times


def measure_rss(executable, env=None):
    pid = os.fork()
    if pid == 0:
        try:
            os.execve(executable, [executable, "-c", "import sys"], env or os.environ)
        except Exception:
            os._exit(127)
    _, _, ru = os.wait4(pid, 0)
    return ru.ru_maxrss / 1024.0


def internal_time_ms(executable, script_path, env=None):
    out = subprocess.check_output([executable, script_path], text=True, stderr=subprocess.DEVNULL, env=env)
    match = re.search(r"completed in ([\d.]+) ms", out)
    if not match:
        raise RuntimeError(f"could not parse benchmark output from {executable}: {out!r}")
    return float(match.group(1))


def measure_workload(script_path, runs=RUNS, env=None):
    fy_times = []
    py_times = []
    for _ in range(runs):
        fy_times.append(internal_time_ms(FYTHON_BIN, script_path, env=env))
        if PYTHON3_BIN:
            py_times.append(internal_time_ms(python_for(PYTHON3_BIN), script_path, env=env))
    return fy_times, py_times


def median(values):
    if not values:
        return float("nan")
    return statistics.median(values)


def run_profile(bench_name):
    script_file = None
    for name, sf in BENCHMARKS:
        if bench_name in (name, sf):
            script_file = sf
            break
    if script_file is None:
        # allow direct filename
        if os.path.isfile(os.path.join(SCRIPT_DIR, bench_name)):
            script_file = bench_name
        else:
            print(f"Unknown benchmark: {bench_name}")
            sys.exit(2)
    env = bench_env()
    env["BENCH_FILE"] = script_file
    cmd = [
        "go", "test", "./benchmarks/",
        "-run=^$", "-bench=BenchmarkScript", "-benchtime=1x",
        "-cpuprofile=cpu.prof", "-memprofile=mem.prof", "-count=1",
    ]
    print(f"[*] Profiling {script_file} -> cpu.prof / mem.prof")
    subprocess.check_call(cmd, cwd=REPO_ROOT, env=env)
    print("[*] Done. Inspect with: go tool pprof cpu.prof")


def main():
    parser = argparse.ArgumentParser(description="Fython vs CPython benchmark comparison")
    parser.add_argument("--profile", metavar="BENCH_NAME", help="Profile a single benchmark via Go pprof")
    args = parser.parse_args()

    ensure_fython_binary()
    ensure_venv()

    if args.profile:
        run_profile(args.profile)
        return

    env = bench_env()

    py_version = "CPython 3.15.0"
    if PYTHON3_BIN:
        try:
            out = subprocess.check_output([python_for(PYTHON3_BIN), "--version"], text=True, env=env).strip()
            py_version = out
        except Exception:
            pass

    print("\n" + "=" * 96)
    print(f"  Fython 3.15.0 vs {py_version} Head-to-Head Performance Suite")
    print("=" * 96 + "\n")
    print(f"[*] Methodology: {RUNS} interleaved A/B runs per workload (min/median of internal timed loop);")
    print(f"    cold start = mean of {COLD_START_RUNS} runs; peak RSS via wait4.\n")

    print("[1/4] Measuring cold startup latency...")
    fy_start_times = measure_cold_start(FYTHON_BIN, env=env)
    py_start_times = measure_cold_start(python_for(PYTHON3_BIN), env=env) if PYTHON3_BIN else []
    fy_start = statistics.fmean(fy_start_times)
    py_start = statistics.fmean(py_start_times) if py_start_times else float("nan")

    print("[2/4] Measuring import latency...")
    # import latency measured directly
    fy_imp_list = []
    py_imp_list = []
    for _ in range(COLD_START_RUNS):
        t0 = time.perf_counter()
        subprocess.check_call([FYTHON_BIN, "-c", "import jinja2, rich, packaging"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
        fy_imp_list.append(time.perf_counter() - t0)
        if PYTHON3_BIN:
            t0 = time.perf_counter()
            subprocess.check_call([python_for(PYTHON3_BIN), "-c", "import jinja2, rich, packaging"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
            py_imp_list.append(time.perf_counter() - t0)
    fy_imp = statistics.fmean(fy_imp_list) * 1000.0
    py_imp = (statistics.fmean(py_imp_list) * 1000.0) if py_imp_list else float("nan")

    print("[3/4] Measuring base process RSS...")
    fy_rss = measure_rss(FYTHON_BIN, env=env)
    py_rss = measure_rss(python_for(PYTHON3_BIN), env=env) if PYTHON3_BIN else float("nan")

    print(f"[4/4] Running {len(BENCHMARKS)} core benchmark workloads ({RUNS} interleaved runs each)...")
    workload_results = []
    for name, script_file in BENCHMARKS:
        path = os.path.join(SCRIPT_DIR, script_file)
        print(f"  -> Running {name}...")
        fy_times, py_times = measure_workload(path, env=env)
        workload_results.append((name, fy_times, py_times))

    # Table
    print("\n" + "|" + "-" * 42 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 11 + "|")
    print(f"| {'Workload':<40} | {'Fy min ms':>12} | {'Fy med ms':>12} | {'Py min ms':>12} | {'Py med ms':>12} | {'Speedup':>9} |")
    print("|" + "-" * 42 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 11 + "|")

    fy_start_ms = fy_start * 1000.0
    py_start_ms = py_start * 1000.0
    start_speedup = (py_start_ms / fy_start_ms) if fy_start_ms > 0 else 1.0
    print(f"| {'Cold Startup (-c pass)':<40} | {fy_start_ms:12.2f} | {fy_start_ms:12.2f} | {py_start_ms:12.2f} | {py_start_ms:12.2f} | {start_speedup:8.2f}x |")

    imp_speedup = (py_imp / fy_imp) if fy_imp > 0 else 1.0
    print(f"| {'Import Latency (jinja2,rich,packaging)':<40} | {fy_imp:12.2f} | {fy_imp:12.2f} | {py_imp:12.2f} | {py_imp:12.2f} | {imp_speedup:8.2f}x |")

    for name, fy_times, py_times in workload_results:
        fy_min = min(fy_times)
        fy_med = median(fy_times)
        py_min = min(py_times) if py_times else float("nan")
        py_med = median(py_times) if py_times else float("nan")
        speedup = (py_min / fy_min) if fy_min > 0 else 1.0
        print(f"| {name:<40} | {fy_min:12.2f} | {fy_med:12.2f} | {py_min:12.2f} | {py_med:12.2f} | {speedup:8.2f}x |")

    rss_ratio = (py_rss / fy_rss) if fy_rss > 0 else 1.0
    print(f"| {'Base RSS (MB)':<40} | {fy_rss:12.2f} | {fy_rss:12.2f} | {py_rss:12.2f} | {py_rss:12.2f} | {rss_ratio:8.2f}x |")
    print("|" + "-" * 42 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 14 + "|" + "-" * 11 + "|\n")


if __name__ == "__main__":
    main()