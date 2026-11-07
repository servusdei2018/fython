package vm_test

import "testing"

func TestLoggingEcosystem(t *testing.T) {
	runSnippet(t, `
import logging, io

stream = io.StringIO()
handler = logging.StreamHandler(stream)
formatter = logging.Formatter("%(levelname)s:%(funcName)s:%(lineno)d:%(message)s")
handler.setFormatter(formatter)

logger = logging.getLogger("test_logger")
logger.setLevel(logging.DEBUG)
logger.addHandler(handler)

def worker():
    logger.info("working hard")

worker()

try:
    1 / 0
except ZeroDivisionError:
    logger.exception("calculation failed")

handler.flush()
output = stream.getvalue()
assert "INFO:worker:" in output, f"Missing INFO in {output}"
assert "working hard" in output, f"Missing message in {output}"
assert "ZeroDivisionError: division by zero" in output, f"Missing traceback in {output}"
`)
}

func TestMockEcosystem(t *testing.T) {
	runSnippet(t, `
from unittest.mock import Mock, MagicMock, AsyncMock, patch
import asyncio

m = MagicMock()
m[1] = 42
m.__setitem__.assert_called_once_with(1, 42)

m.__getitem__.return_value = 42
assert m[1] == 42
m.__getitem__.assert_called_once_with(1)

m.__contains__.return_value = True
assert 1 in m

class Target:
    def add(self, a, b):
        return a + b

with patch.object(Target, "add", return_value=100) as mock_add:
    t = Target()
    res = t.add(2, 3)
    assert res == 100
    mock_add.assert_called_once_with(2, 3)

async def test_async():
    am = AsyncMock(return_value="async_result")
    val = await am("arg1", key="kw")
    assert val == "async_result"
    am.assert_awaited_once_with("arg1", key="kw")

asyncio.run(test_async())
`)
}

func TestTomllibConfigparserEcosystem(t *testing.T) {
	runSnippet(t, `
import tomllib, configparser

toml_data = """
title = "TOML Example"

[owner]
name = "Tom Preston-Werner"
dob = 1979-05-27T07:32:00-08:00

[database]
server = "192.168.1.1"
ports = [ 8001, 8001, 8002 ]
connection_max = 5000
enabled = true

[[fruits]]
name = "apple"
physical = { color = "red", shape = "round" }

[[fruits]]
name = "banana"
physical = { color = "yellow" }
"""

d = tomllib.loads(toml_data)
assert d["title"] == "TOML Example"
assert d["owner"]["name"] == "Tom Preston-Werner"
assert d["database"]["ports"] == [8001, 8001, 8002]
assert len(d["fruits"]) == 2
assert d["fruits"][0]["name"] == "apple"
assert d["fruits"][1]["physical"]["color"] == "yellow"

ini_data = """
[DEFAULT]
ServerAliveInterval = 45
Compression = yes

[forge.example]
User = hg
Port = 50022
"""

cfg = configparser.ConfigParser()
cfg.read_string(ini_data)
assert cfg["DEFAULT"]["ServerAliveInterval"] == "45"
assert cfg["forge.example"]["User"] == "hg"
assert cfg.getint("forge.example", "Port") == 50022
`)
}

func TestTarfileEcosystem(t *testing.T) {
	runSnippet(t, `
import io, tarfile, tempfile, os

buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode="w:gz") as tar:
    data1 = b"Hello from file1!"
    t1 = tarfile.TarInfo("file1.txt")
    t1.size = len(data1)
    tar.addfile(t1, io.BytesIO(data1))
    
    data2 = b"Hello from nested/file2.txt!"
    t2 = tarfile.TarInfo("nested/file2.txt")
    t2.size = len(data2)
    tar.addfile(t2, io.BytesIO(data2))

buf.seek(0)
with tempfile.TemporaryDirectory() as tmpdir:
    with tarfile.open(fileobj=buf, mode="r:gz") as tar:
        tar.extractall(tmpdir)
    with open(os.path.join(tmpdir, "file1.txt"), "rb") as f:
        c1 = f.read()
        assert c1 == b"Hello from file1!", f"file1 mismatch: {c1}"
    with open(os.path.join(tmpdir, "nested/file2.txt"), "rb") as f:
        c2 = f.read()
        assert c2 == b"Hello from nested/file2.txt!", f"file2 mismatch: {c2}"
`)
}

func TestConcurrentFuturesQueueEcosystem(t *testing.T) {
	runSnippet(t, `
from concurrent.futures import ThreadPoolExecutor, as_completed
import queue, _queue

sq = _queue.SimpleQueue()
assert sq.empty()
sq.put("item1")
assert not sq.empty()
assert sq.qsize() == 1
assert sq.get() == "item1"
assert sq.empty()

q = queue.Queue()
q.put(10)
q.put(20)
assert q.get() == 10
assert q.get() == 20

lq = queue.LifoQueue()
lq.put(1)
lq.put(2)
assert lq.get() == 2
assert lq.get() == 1

pq = queue.PriorityQueue()
pq.put((2, "second"))
pq.put((1, "first"))
assert pq.get() == (1, "first")
assert pq.get() == (2, "second")

def square(x):
    return x * x

with ThreadPoolExecutor(max_workers=4) as executor:
    results = list(executor.map(square, [1, 2, 3, 4, 5]))
    assert results == [1, 4, 9, 16, 25], f"unexpected map results: {results}"

with ThreadPoolExecutor(max_workers=4) as executor:
    futures = [executor.submit(square, x) for x in range(6)]
    completed = []
    for f in as_completed(futures):
        completed.append(f.result())
    completed.sort()
    assert completed == [0, 1, 4, 9, 16, 25], f"unexpected completed results: {completed}"
`)
}

func TestHeapqBisectEcosystem(t *testing.T) {
	runSnippet(t, `
import heapq, bisect

data = [5, 1, 8, 3, 2]
heapq.heapify(data)
heapq.heappush(data, 0)
popped = [heapq.heappop(data) for _ in range(len(data))]
assert popped == [0, 1, 2, 3, 5, 8], f"heapq failed: {popped}"

arr = [10, 20, 30, 40]
idx = bisect.bisect_left(arr, 25)
assert idx == 2
bisect.insort_left(arr, 25)
assert arr == [10, 20, 25, 30, 40]
`)
}

func TestPPrintEcosystem(t *testing.T) {
	runSnippet(t, `
import pprint

d = {"a": [1, 2, 3], "b": {"nested": "value", "numbers": list(range(5))}}
s = pprint.pformat(d, indent=2, width=40)
assert "nested" in s
assert "numbers" in s
`)
}
