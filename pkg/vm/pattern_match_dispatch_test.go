package vm_test

import (
	"testing"
)

func TestKindNativePatternMatchDispatch(t *testing.T) {
	eng, v := runPySource(t, `
import re

pat = re.compile(r"([a-z]+)=([0-9]+)")

def test_bool_only(p, items):
    hits = 0
    for s in items:
        if p.match(s):
            hits += 1
    return hits

def test_ephemeral(p, items):
    out = []
    for s in items:
        m = p.match(s)
        if m:
            out.append(m.group(1))
    return out

def test_escaping(p, s):
    return p.match(s)

samples = ["foo=123", "bad", "bar=456", "baz=789"] * 5
assert test_bool_only(pat, samples) == 15
assert test_ephemeral(pat, samples[:4]) == ["foo", "bar", "baz"]
m = test_escaping(pat, "hello=42")
assert m is not None and m.group(1) == "hello" and m.group(2) == "42"
assert test_escaping(pat, "nope") is None
`)
	defer eng.ReleaseVM(v)
}
