import json
import time

def generate_payload():
    items = []
    for i in range(1000):
        items.append({
            "id": i,
            "name": "user_" + str(i),
            "age": 20 + (i % 50),
            "active": True,
            "score": 95.5 + float(i % 5),
            "roles": ["admin", "developer", "tester"]
        })
    return json.dumps(items)

def run_benchmark():
    raw = generate_payload()
    t0 = time.perf_counter()
    for _ in range(20):
        data = json.loads(raw)
    elapsed = time.perf_counter() - t0
    return len(data), elapsed

if __name__ == "__main__":
    n, elapsed = run_benchmark()
    print(f"JSON Parsing Benchmark completed in {elapsed*1000:.2f} ms ({n} items)")
