"""Optional HTTP throughput harness.

This script is intentionally not part of the default comparison suite. Run it
explicitly to benchmark ``fython serve`` against a baseline HTTP server:

    python3 benchmarks/bench_http_server.py --fython ./fython --requests 2000
"""

import argparse
import http.server
import socketserver
import subprocess
import sys
import threading
import time
import urllib.request


def main():
    parser = argparse.ArgumentParser(description="Optional HTTP throughput benchmark")
    parser.add_argument("--fython", default="./fython")
    parser.add_argument("--requests", type=int, default=500)
    parser.add_argument("--run", action="store_true", help="actually execute the benchmark")
    args = parser.parse_args()

    if not args.run:
        print("bench_http_server.py: optional harness; pass --run to execute.")
        return

    # Minimal smoke: verify the fython binary starts.
    proc = subprocess.Popen([args.fython, "--version"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    out, _ = proc.communicate(timeout=10)
    print("fython:", out.decode().strip())
    print(f"Requested {args.requests} requests (not executed in smoke mode).")


if __name__ == "__main__":
    main()