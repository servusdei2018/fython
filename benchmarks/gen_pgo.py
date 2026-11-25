#!/usr/bin/env python3
"""Generate a merged Go PGO CPU profile from the 5 core library benchmarks."""

import argparse
import os
import shutil
import subprocess
import sys
import tempfile

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.abspath(os.path.join(SCRIPT_DIR, ".."))
VENV_DIR = os.path.join(SCRIPT_DIR, ".venv")

# (module_name, iterations) tuned so each workload contributes balanced pprof samples.
PGO_WORKLOADS = [
    ("bench_packaging_resolve", 15),
    ("bench_jinja", 25),
    ("bench_rich_lex", 25),
    ("bench_web_asgi", 80),
    ("bench_etl_validation", 300),
]

DRIVER_CODE = """\
import os, sys
sys.path.insert(0, os.environ["PGO_BENCH_DIR"])
mod = __import__(os.environ["PGO_BENCH_MOD"])
iters = int(os.environ["PGO_BENCH_ITERS"])
for _ in range(iters):
    mod.run_benchmark()
"""


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate merged Go PGO profile for fython")
    parser.add_argument(
        "--binary",
        default=os.path.join(REPO_ROOT, "bin", "fython"),
        help="Path to the non-PGO fython binary used to collect CPU profiles",
    )
    parser.add_argument(
        "--output",
        default=os.path.join(REPO_ROOT, "bin", "default.pgo"),
        help="Output path for the merged Go PGO profile",
    )
    args = parser.parse_args()

    binary = os.path.abspath(args.binary)
    output = os.path.abspath(args.output)
    if not os.path.isfile(binary):
        print(f"error: fython binary not found: {binary}", file=sys.stderr)
        return 1

    if not os.path.isdir(VENV_DIR):
        print(f"error: benchmark venv not found at {VENV_DIR}; run 'uv sync' in benchmarks/", file=sys.stderr)
        return 1

    base_env = os.environ.copy()
    base_env["VIRTUAL_ENV"] = VENV_DIR
    base_env["PATH"] = os.path.join(VENV_DIR, "bin") + os.pathsep + base_env.get("PATH", "")
    base_env["PGO_BENCH_DIR"] = SCRIPT_DIR

    tmp_dir = tempfile.mkdtemp(prefix="fython_pgo_")
    try:
        driver_path = os.path.join(tmp_dir, "driver.py")
        with open(driver_path, "w", encoding="utf-8") as f:
            f.write(DRIVER_CODE)

        prof_paths = []
        for mod_name, iters in PGO_WORKLOADS:
            prof_path = os.path.join(tmp_dir, f"{mod_name}.prof")
            env = base_env.copy()
            env["PGO_BENCH_MOD"] = mod_name
            env["PGO_BENCH_ITERS"] = str(iters)
            env["FYTHON_CPUPROFILE"] = prof_path
            print(f"[pgo] profiling {mod_name} ({iters} iters)...", flush=True)
            subprocess.check_call([binary, driver_path], env=env, stdout=subprocess.DEVNULL)
            if not os.path.isfile(prof_path) or os.path.getsize(prof_path) == 0:
                print(f"error: empty CPU profile generated for {mod_name}", file=sys.stderr)
                return 1
            prof_paths.append(prof_path)

        os.makedirs(os.path.dirname(output), exist_ok=True)
        print(f"[pgo] merging {len(prof_paths)} profiles -> {output}", flush=True)
        merged = subprocess.check_output(["go", "tool", "pprof", "-proto", *prof_paths], cwd=REPO_ROOT)
        if not merged:
            print("error: go tool pprof -proto produced empty output", file=sys.stderr)
            return 1
        with open(output, "wb") as f:
            f.write(merged)

        size = os.path.getsize(output)
        if size == 0:
            print(f"error: merged PGO profile is empty: {output}", file=sys.stderr)
            return 1
        print(f"[pgo] wrote {output} ({size} bytes)", flush=True)
        return 0
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
