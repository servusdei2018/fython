package vm_test

import (
	"strings"
	"testing"
)

func TestArgparseUpstream(t *testing.T) {
	runSnippet(t, `
import argparse
p = argparse.ArgumentParser(prog="tool", description="demo")
p.add_argument("path")
p.add_argument("-v", "--verbose", action="store_true")
p.add_argument("-n", type=int, default=3)
p.add_argument("--tag", action="append", default=[])
ns = p.parse_args(["file.txt", "-v", "-n", "7", "--tag", "a", "--tag", "b"])
assert ns.path == "file.txt", ns
assert ns.verbose is True
assert ns.n == 7
assert ns.tag == ["a", "b"], ns.tag
ns = p.parse_args(["x"])
assert ns.verbose is False and ns.n == 3 and ns.tag == []

sub = argparse.ArgumentParser(prog="git")
sp = sub.add_subparsers(dest="cmd")
c = sp.add_parser("commit")
c.add_argument("-m")
ns = sub.parse_args(["commit", "-m", "msg"])
assert ns.cmd == "commit" and ns.m == "msg"
`)
}

func TestArgparseErrorExits(t *testing.T) {
	runSnippet(t, `
import argparse, io, sys
p = argparse.ArgumentParser(prog="tool")
p.add_argument("--num", type=int)
old = sys.stderr
sys.stderr = io.StringIO()
try:
    p.parse_args(["--num", "abc"])
    code = None
except SystemExit as e:
    code = e.code
finally:
    msg = sys.stderr.getvalue()
    sys.stderr = old
assert code == 2, code
assert "invalid int value" in msg, msg
`)
}

func TestPosixOS(t *testing.T) {
	runSnippet(t, `
import os, errno, stat
assert os.fspath("a/b") == "a/b"
assert os.path.join("a", "b", "c") == "a/b/c"
assert os.path.basename("/x/y.py") == "y.py"
assert os.path.splitext("y.tar.gz") == ("y.tar", ".gz")
assert os.path.normpath("a/../b//c") == "b/c"
assert os.path.isabs("/x")
st = os.stat("/")
assert stat.S_ISDIR(st.st_mode)
assert st[stat.ST_MODE] == st.st_mode
try:
    os.stat("/definitely/not/here")
    raise AssertionError("no error")
except FileNotFoundError as e:
    assert e.errno == errno.ENOENT
    assert e.filename == "/definitely/not/here"
assert errno.errorcode[errno.ENOENT] == "ENOENT"
assert "No such file" in os.strerror(errno.ENOENT)
`)
}

func TestFnmatchGettextShutil(t *testing.T) {
	runSnippet(t, `
import fnmatch, gettext, shutil
assert fnmatch.fnmatch("foo.py", "*.py")
assert not fnmatch.fnmatch("foo.pyc", "*.py")
assert fnmatch.filter(["a.py", "b.txt", "c.py"], "*.py") == ["a.py", "c.py"]
assert gettext.gettext("hello") == "hello"
assert gettext.ngettext("one", "many", 2) == "many"
sz = shutil.get_terminal_size()
assert sz.columns > 0 and sz.lines > 0
assert len(sz) == 2
`)
}

func TestPprintWarnings(t *testing.T) {
	runSnippet(t, `
import pprint, warnings
s = pprint.pformat({"a": [1, 2, 3], "b": {"c": (1, 2)}})
assert s == "{'a': [1, 2, 3], 'b': {'c': (1, 2)}}", s
with warnings.catch_warnings(record=True) as w:
    warnings.simplefilter("always")
    warnings.warn("hey", UserWarning)
assert len(w) == 1 and w[0].category is UserWarning
`)
}

func TestSignalModule(t *testing.T) {
	runSnippet(t, `
import signal
seen = []
def handler(signum, frame):
    seen.append(signum)
old = signal.signal(signal.SIGUSR1, handler)
assert old == signal.SIG_DFL
assert signal.getsignal(signal.SIGUSR1) is handler
signal.raise_signal(signal.SIGUSR1)
for _ in range(10):
    pass
assert seen == [signal.SIGUSR1], seen
signal.signal(signal.SIGUSR1, signal.SIG_IGN)
signal.raise_signal(signal.SIGUSR1)
assert seen == [signal.SIGUSR1]

try:
    signal.raise_signal(signal.SIGINT)
    for _ in range(10):
        pass
    raise AssertionError("no KeyboardInterrupt")
except KeyboardInterrupt:
    pass
`)
}

func TestHandlerExceptionPropagates(t *testing.T) {
	runSnippet(t, `
import signal
class Boom(Exception):
    pass
def h(n, f):
    raise Boom()
signal.signal(signal.SIGUSR2, h)
try:
    signal.raise_signal(signal.SIGUSR2)
    for _ in range(10):
        pass
    raise AssertionError("handler exception lost")
except Boom:
    pass
`)
}

func TestStdlibRegressions(t *testing.T) {
	runSnippet(t, `
# comprehension isolation
name = "outer"
def f(name):
    r = [name for name in range(3)]
    return name, r
assert f("p") == ("p", [0, 1, 2])

# nested dict literals and large literals
d = {"a": 1, "b": {"c": 2}, "e": {"f": {"g": 3}}}
assert d["b"]["c"] == 2 and d["e"]["f"]["g"] == 3
big = {0: 0, `+bigDictPairs()+`}
assert len(big) == 300 and big[299] == 299

# kw-only after *args
def g(*items, append):
    return items, append
assert g(1, 2, append=3) == ((1, 2), 3)

# SystemExit.code
try:
    raise SystemExit(5)
except SystemExit as e:
    assert e.code == 5 and e.args == (5,)

# print(file=), sep/end
import io
buf = io.StringIO()
print("a", "b", sep="-", end="!", file=buf)
assert buf.getvalue() == "a-b!"

# dict.fromkeys
assert dict.fromkeys("ab", 0) == {"a": 0, "b": 0}

# per-type repr
from collections import deque, defaultdict
assert repr(deque([1, 2])) == "deque([1, 2])"
assert repr(b"ab") == "b'ab'"
assert repr(bytearray(b"x")) == "bytearray(b'x')"
assert "%r" % ("zz",) == "'zz'"

# user iterables through builtins
class S:
    def __iter__(self):
        return iter([1, 2])
assert list(enumerate(S())) == [(0, 1), (1, 2)]
assert sorted(S(), reverse=True) == [2, 1]
assert sum(S()) == 3 and list(zip(S(), S())) == [(1, 1), (2, 2)]

# dir() and cmp_to_key
class A:
    def test_a(self): pass
assert "test_a" in dir(A) and "test_a" in dir(A())
import functools
k = functools.cmp_to_key(lambda a, b: (a > b) - (a < b))
assert sorted([3, 1, 2], key=k) == [1, 2, 3]
`)
}

func TestUnittestRuns(t *testing.T) {
	runSnippet(t, `
import unittest, io
class T(unittest.TestCase):
    def test_a(self):
        self.assertEqual(1, 1)
    def test_b(self):
        self.assertTrue([1] == [1])
suite = unittest.defaultTestLoader.loadTestsFromTestCase(T)
res = unittest.TextTestRunner(stream=io.StringIO(), verbosity=0).run(suite)
assert res.testsRun == 2 and res.wasSuccessful()
`)
}

func bigDictPairs() string {
	var sb strings.Builder
	for i := 1; i < 300; i++ {
		if i > 1 {
			sb.WriteString(", ")
		}
		sb.WriteString(itoa(i) + ": " + itoa(i))
	}
	return sb.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
