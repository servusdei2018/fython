package vm_test

import "testing"

func TestSRESubASCIIAndHighBytesAndCapsBuf(t *testing.T) {
	eng, v := runPySource(t, `
import re

# Literal replacement, zero-match fast path, and count.
p = re.compile(r"ERROR_(\d+)")
src = "ok ERROR_404 mid ERROR_503 end"
assert p.sub("WARN", src) == "ok WARN mid WARN end"
assert p.subn("WARN", src, 1) == ("ok WARN mid ERROR_503 end", 1)
assert p.subn("WARN", "no matches here") == ("no matches here", 0)

# Backreferences: \2, \g<1>, \g<name>, \g<0>.
pn = re.compile(r"(?P<k>[a-z]+)=(\d+)")
assert pn.sub(r"\g<k>:[\2](\g<0>)", "a=1 b=22") == "a:[1](a=1) b:[22](b=22)"

# Zero-length matches.
assert re.sub(r"", "-", "ab") == "-a-b-"
assert re.sub(r"\b", "|", "hi ok") == "|hi| |ok|"

# Callable returning ASCII and non-ASCII Unicode, plus retaining Match objects.
saved = []
def repl_retain(m):
    saved.append(m)
    return "caf\u00e9_" + m.group(1)

res = p.sub(repl_retain, src)
assert res == "ok caf\u00e9_404 mid caf\u00e9_503 end"
assert len(saved) == 2
assert saved[0].group(1) == "404" and saved[0].span() == (3, 12)
assert saved[1].group(1) == "503" and saved[1].span() == (17, 26)

# Callable raising exception unwinds cleanly.
def repl_boom(m):
    raise RuntimeError("stop")

raised = False
try:
    p.sub(repl_boom, src)
except RuntimeError:
    raised = True
assert raised

# > 15 capturing groups (exercises [64]int32 capsBuf without heap overflow).
pat20 = re.compile("".join(f"({chr(97+i)})" for i in range(20)))
s20 = "".join(chr(97+i) for i in range(20))
m20 = pat20.match(s20)
assert m20 is not None
assert m20.group(1) == "a" and m20.group(20) == "t"
assert len(m20.groups()) == 20

# Raw bytes with high bytes >= 0x80 (exercises [256]bool table populated from latinBits).
assert re.findall(rb"[^\n]+", b"abc\xffdef\nghi\x80jkl") == [b"abc\xffdef", b"ghi\x80jkl"]
assert re.findall(rb"[\x80-\xff]+", b"a\x80\xfe\xffb\x90c") == [b"\x80\xfe\xff", b"\x90"]

# Non-ASCII subject fallback.
assert re.sub(r"\w+", lambda m: m.group(0).upper(), "caf\u00e9 na\u00efve") == "CAF\u00c9 NA\u00cfVE"
`)
	defer eng.ReleaseVM(v)
}
