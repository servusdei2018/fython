import asyncio
import json
import time


async def app(scope, receive, send):
    if scope.get("type") != "http":
        return
    path = scope.get("path", "/")
    method = scope.get("method", "GET")
    query = scope.get("query_string", b"").decode("latin-1")

    if path.startswith("/users/"):
        user_id = path[len("/users/"):]
        body = b""
        while True:
            msg = await receive()
            body += msg.get("body", b"")
            if not msg.get("more_body", False):
                break
        payload = json.loads(body) if body else {}
        response = {"id": user_id, "method": method, "query": query, "echo": payload}
        status = 200
    elif path == "/orders":
        body = b""
        while True:
            msg = await receive()
            body += msg.get("body", b"")
            if not msg.get("more_body", False):
                break
        items = json.loads(body) if body else []
        total = sum((it.get("qty", 0) * it.get("price", 0.0)) for it in items)
        response = {"orders": len(items), "total": total, "query": query}
        status = 200
    else:
        response = {"error": "not found", "path": path}
        status = 404

    raw = json.dumps(response).encode()
    await send({
        "type": "http.response.start",
        "status": status,
        "headers": [
            (b"content-type", b"application/json"),
            (b"x-middleware", b"injected"),
            (b"content-length", str(len(raw)).encode()),
        ],
    })
    await send({"type": "http.response.body", "body": raw, "more_body": False})


async def drive_one(i):
    if i % 3 == 0:
        path = "/users/" + str(i % 50)
        method = "POST"
        body = json.dumps({"n": i, "name": "user_" + str(i)}).encode()
    elif i % 3 == 1:
        path = "/orders"
        method = "POST"
        body = json.dumps([{"qty": 2, "price": 3.5}, {"qty": 1, "price": 10.0}]).encode()
    else:
        path = "/users/" + str(i % 50)
        method = "GET"
        body = b""

    scope = {
        "type": "http",
        "asgi": {"version": "3.0"},
        "http_version": "1.1",
        "method": method,
        "scheme": "http",
        "path": path,
        "raw_path": path.encode(),
        "root_path": "",
        "query_string": b"q=search&limit=10",
        "server": ("127.0.0.1", 8000),
        "client": ("127.0.0.1", 12345),
        "headers": [(b"content-type", b"application/json")],
    }

    async def receive():
        return {"type": "http.request", "body": body, "more_body": False}

    sent = []

    async def send(msg):
        sent.append(msg)

    await app(scope, receive, send)
    return len(sent[-1].get("body", b""))


async def drive(n):
    total = 0
    for i in range(n):
        total += await drive_one(i)
    return total


def run_benchmark():
    # warmup
    asyncio.run(drive(100))
    t0 = time.perf_counter()
    total = asyncio.run(drive(5000))
    elapsed = time.perf_counter() - t0
    return total, elapsed


if __name__ == "__main__":
    total, elapsed = run_benchmark()
    print(f"ASGI Web Microservice Benchmark completed in {elapsed * 1000:.2f} ms ({total} bytes)")