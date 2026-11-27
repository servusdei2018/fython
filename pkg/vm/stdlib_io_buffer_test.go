package vm_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestLargeFileReadAndUpstreamStdlibParse(t *testing.T) {
	dir := t.TempDir()
	largePath := filepath.Join(dir, "large.txt")
	var sb strings.Builder
	for i := range 4000 {
		fmt.Fprintf(&sb, "line-%05d: The quick brown fox jumps over the lazy dog.\n", i)
	}
	wantText := sb.String()
	if len(wantText) <= 131072 {
		t.Fatalf("test fixture too small: %d bytes", len(wantText))
	}
	if err := os.WriteFile(largePath, []byte(wantText), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	crlfPath := filepath.Join(dir, "crlf_utf8.txt")
	crlfContent := "héllo\r\nwörld\r日本語\n"
	if err := os.WriteFile(crlfPath, []byte(crlfContent), 0600); err != nil {
		t.Fatalf("write crlf fixture: %v", err)
	}

	src := fmt.Sprintf(`
path = %q
crlf_path = %q
want_len = %d

with open(path, "r") as f:
    text_all = f.read()
with open(path, "rb") as f:
    bin_all = f.read()

# Verify full length and prefix/suffix (no 64 KiB front drop)
full_ok = (
    len(text_all) == want_len
    and len(bin_all) == want_len
    and text_all.startswith("line-00000:")
    and text_all.endswith("line-03999: The quick brown fox jumps over the lazy dog.\n")
    and bin_all.startswith(b"line-00000:")
    and bin_all.endswith(b"line-03999: The quick brown fox jumps over the lazy dog.\n")
)

# Verify seek + read(), read(n), readline(), readlines()
with open(path, "rb") as f:
    first10 = f.read(10)
    f.seek(100)
    rest = f.read()
seek_ok = (first10 == b"line-00000" and len(rest) == want_len - 100 and rest == bin_all[100:])

with open(path, "r") as f:
    l0 = f.readline()
    l1 = f.readline()
    rem = f.read()
line_ok = (
    l0.startswith("line-00000:")
    and l1.startswith("line-00001:")
    and len(l0) + len(l1) + len(rem) == want_len
)

with open(path, "r") as f:
    all_lines = f.readlines()
readlines_ok = (len(all_lines) == 4000 and all_lines[0] == l0 and all_lines[-1].startswith("line-03999:"))

# Closed-file read raises ValueError (including fast-path 0-arg read)
f_closed = open(path, "r")
f_closed.close()
closed_err = False
try:
    f_closed.read()
except ValueError:
    closed_err = True

# Universal newline normalization & multi-byte UTF-8 read(n)
with open(crlf_path, "r") as f:
    norm_all = f.read()
with open(crlf_path, "r") as f:
    c1 = f.read(6)   # "héllo\n" (6 Unicode chars)
    c2 = f.read(6)   # "wörld\n"
    c3 = f.read(4)   # "日本語\n"
    c4 = f.read(1)   # ""
utf8_nl_ok = (
    norm_all == "héllo\nwörld\n日本語\n"
    and c1 == "héllo\n"
    and c2 == "wörld\n"
    and c3 == "日本語\n"
    and c4 == ""
)
`, largePath, crlfPath, len(wantText))

	eng, v := runPySource(t, src)
	defer eng.ReleaseVM(v)

	for _, name := range []string{"full_ok", "seek_ok", "line_ok", "readlines_ok", "closed_err", "utf8_nl_ok"} {
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want True", name, got, ok)
		}
	}

	// Test real upstream CPython stdlib argparse.py and inspect.py if available.
	candidates := []string{
		"/opt/homebrew/Cellar/python@3.15/3.15.2/Frameworks/Python.framework/Versions/3.15/lib/python3.15/argparse.py",
		"/opt/homebrew/Cellar/python@3.15/3.15.2/Frameworks/Python.framework/Versions/3.15/lib/python3.15/inspect.py",
	}
	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		parseSrc := fmt.Sprintf(`
import ast
with open(%q, "r") as f:
    src = f.read()
tree = ast.parse(src)
code = compile(src, %q, "exec")
ok = (len(src) == %d and len(tree.body) > 20 and code is not None)
`, p, p, info.Size())
		e2, v2 := runPySource(t, parseSrc)
		got, ok := v2.Globals().Get(val.InternString("ok"))
		e2.ReleaseVM(v2)
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("upstream %s parse/compile failed: ok=%v (%v)", filepath.Base(p), got, ok)
		}
	}
}

func TestBytesIOAndStringIOBuffers(t *testing.T) {
	eng, v := runPySource(t, `
import io

# 1. BytesIO: amortized growth, overwrite, seek past EOF zero-pad, truncate, zero-aliasing
bio = io.BytesIO()
for i in range(2000):
    bio.write(b"0123456789")
snap = bio.getvalue()
bio.seek(0)
bio.write(b"XXXXX")
alias_ok = (snap[:5] == b"01234" and bio.getvalue()[:5] == b"XXXXX" and len(snap) == 20000)

bio2 = io.BytesIO(b"abcdef")
bio2.seek(10)
bio2.write(b"Z")
pad_ok = (bio2.getvalue() == b"abcdef\x00\x00\x00\x00Z" and bio2.tell() == 11)

# CPython truncate semantics: does not move pos; size > len leaves buffer unchanged, returns size
bio3 = io.BytesIO(b"abcdef")
bio3.seek(4)
r1 = bio3.truncate(10)
v1 = bio3.getvalue()
p1 = bio3.tell()
r2 = bio3.truncate(3)
v2 = bio3.getvalue()
p2 = bio3.tell()
bytes_trunc_ok = (r1 == 10 and v1 == b"abcdef" and p1 == 4 and r2 == 3 and v2 == b"abc" and p2 == 4)

bio4 = io.BytesIO(b"a\nb\nc")
bytes_iter_ok = (list(bio4) == [b"a\n", b"b\n", b"c"])

# 2. StringIO: ASCII fast path, seek past EOF zero-pad, truncate
sio = io.StringIO()
for i in range(2000):
    sio.write("hello\n")
s_snap = sio.getvalue()
sio.seek(0)
sio.write("WORLD")
sio_ascii_ok = (
    len(s_snap) == 12000
    and s_snap.startswith("hello\n")
    and sio.getvalue().startswith("WORLD\n")
)

sio_pad = io.StringIO("abc")
sio_pad.seek(6)
sio_pad.write("Z")
sio_pad_ok = (sio_pad.getvalue() == "abc\x00\x00\x00Z" and sio_pad.tell() == 7)

# 3. StringIO: non-ASCII initial_value & writes (Unicode code-point indexing)
su = io.StringIO("héllo 日本語!")
su_r1 = su.read(5)
su_t1 = su.tell()
su_r2 = su.read(4)
su_t2 = su.tell()
su.seek(6)
w_cnt = su.write("世界")
su_val = su.getvalue()
su.seek(0)
su_tr = su.truncate(8)
su_after_tr = su.getvalue()
su_tr_big = su.truncate(50)
su_after_big = su.getvalue()
sio_utf8_ok = (
    su_r1 == "héllo"
    and su_t1 == 5
    and su_r2 == " 日本語"
    and su_t2 == 9
    and w_cnt == 2
    and su_val == "héllo 世界語!"
    and su_tr == 8
    and su_after_tr == "héllo 世界"
    and su_tr_big == 50
    and su_after_big == "héllo 世界"
    and su.tell() == 0
)

# ASCII StringIO transitioning to non-ASCII on write
st = io.StringIO("abc")
st.seek(3)
w2 = st.write("éü")
st.seek(3)
r_u = st.read(1)
sio_trans_ok = (w2 == 2 and st.getvalue() == "abcéü" and r_u == "é" and st.tell() == 4)

# 4. StringIO newline modes
sn_none = io.StringIO("a\r\nb\rc\n", newline=None)
sn_crlf = io.StringIO("", newline="\r\n")
sn_crlf.write("x\ny\n")
sio_nl_ok = (sn_none.getvalue() == "a\nb\nc\n" and sn_crlf.getvalue() == "x\r\ny\r\n")
`)
	defer eng.ReleaseVM(v)

	for _, name := range []string{
		"alias_ok", "pad_ok", "bytes_trunc_ok", "bytes_iter_ok",
		"sio_ascii_ok", "sio_pad_ok", "sio_utf8_ok", "sio_trans_ok", "sio_nl_ok",
	} {
		got, ok := v.Globals().Get(val.InternString(name))
		if !ok || !got.IsBool() || !got.AsBool() {
			t.Fatalf("%s = %v (ok=%v), want True", name, got, ok)
		}
	}
}
