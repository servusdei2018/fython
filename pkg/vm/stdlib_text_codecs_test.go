package vm_test

import (
	"testing"

	"github.com/servusdei2018/fython/pkg/val"
)

func TestStrTranslateMappingAndDictLiterals(t *testing.T) {
	eng, v := runPySource(t, `
# Literal dict (exercises split-table dicts), bool/float keys, and custom mapping
t1 = "hello world".translate({104: 72, 119: "W", 111: None})
t2 = "\x01A!".translate({True: "ONE", 65.0: "Z"})

class CustomMap:
    def __getitem__(self, k):
        if k == ord("a"):
            return "AA"
        if k == ord("b"):
            return None
        raise KeyError(k)

t3 = "abc".translate(CustomMap())

class BoomMap:
    def __getitem__(self, k):
        raise RuntimeError("boom")

boom_ok = False
try:
    "a".translate(BoomMap())
except RuntimeError:
    boom_ok = True

ok = (t1 == "Hell Wrld" and t2 == "ONEZ!" and t3 == "AAc" and boom_ok)
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("ok"))
	if !ok || !got.IsBool() || !got.AsBool() {
		t.Fatalf("str.translate test failed: ok=%v", got)
	}
}

func TestCsvEmbeddedNewlinesAndDialects(t *testing.T) {
	eng, v := runPySource(t, `
import csv
import io

# 1. Embedded \n and \r\n inside quoted fields + escaped quotes ""
raw = '1,"a\nb","c""d"\r\n2,"line1\r\nline2",plain\r\n\r\n3,tail,ok\r\n'
rdr = csv.reader(io.StringIO(raw))
rows = list(rdr)
embedded_ok = (
    len(rows) == 4
    and rows[0] == ["1", "a\nb", 'c"d']
    and rows[1] == ["2", "line1\r\nline2", "plain"]
    and rows[2] == []
    and rows[3] == ["3", "tail", "ok"]
    and rdr.line_num == 6
)

# 2. strict=True raises csv.Error on EOF inside unclosed quote
strict_err = False
try:
    list(csv.reader(io.StringIO('1,"unclosed\nstill open'), strict=True))
except csv.Error:
    strict_err = True

# 3. Writer round-trip with float/int/bool/None/str and quoting modes
buf = io.StringIO()
w = csv.writer(buf)
w.writerows([
    ["id", "note café 日本語 🎉", "score", "flag", "empty"],
    [1, 'quoted "x", with\nnewline', 1.5, True, None],
    [2, "plain", 0.0, False, ""],
])
text = buf.getvalue()
rt = list(csv.reader(io.StringIO(text)))
rt_ok = (
    len(rt) == 3
    and rt[0] == ["id", "note café 日本語 🎉", "score", "flag", "empty"]
    and rt[1] == ["1", 'quoted "x", with\nnewline', "1.5", "True", ""]
    and rt[2] == ["2", "plain", "0.0", "False", ""]
)

# 4. Custom delimiter, skipinitialspace, QUOTE_ALL, QUOTE_NONNUMERIC
buf_qall = io.StringIO()
csv.writer(buf_qall, delimiter=";", quoting=csv.QUOTE_ALL, lineterminator="\n").writerow(["a", 42])
qall_ok = (buf_qall.getvalue() == '"a";"42"\n')

buf_qnn = io.StringIO()
csv.writer(buf_qnn, quoting=csv.QUOTE_NONNUMERIC, lineterminator="\n").writerow(["txt", 3, 2.5])
qnn_rows = list(csv.reader(io.StringIO(buf_qnn.getvalue()), quoting=csv.QUOTE_NONNUMERIC))
qnn_ok = (qnn_rows == [["txt", 3.0, 2.5]])

skip_rows = list(csv.reader(['a, "b,c",  d'], skipinitialspace=True))
skip_ok = (skip_rows == [["a", "b,c", "d"]])

# 5. StringIO non-ASCII runeLen consistency across overwrite, seek-past-EOF, truncate
sio = io.StringIO("héllo 日本語!")
sio.seek(6)
sio.write("世")
sio.seek(0)
s1 = sio.read()
sio.truncate(5)
s2 = sio.getvalue()
sio.seek(7)
sio.write("Z")
s3 = sio.getvalue()
sio_ok = (s1 == "héllo 世本語!" and s2 == "héllo" and s3 == "héllo\x00\x00Z" and sio.tell() == 8)

ok = (embedded_ok and strict_err and rt_ok and qall_ok and qnn_ok and skip_ok and sio_ok)
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("ok"))
	if !ok || !got.IsBool() || !got.AsBool() {
		t.Fatalf("csv/StringIO test failed: ok=%v", got)
	}
}

func TestBinasciiBase64AndZlibSemantics(t *testing.T) {
	eng, v := runPySource(t, `
import base64
import binascii
import zlib

# base64 & binascii
src = b"Hello, Fython codec test! 0123456789" * 10
enc = base64.b64encode(src)
dec = base64.b64decode(enc)
b64_ok = (dec == src)

# b2a_base64 newline=True/False and wrapcol
nl_true = binascii.b2a_base64(b"abc", newline=True)
nl_false = binascii.b2a_base64(b"abc", newline=False)
b2a_ok = (nl_true == b"YWJj\n" and nl_false == b"YWJj")

# Trailing bytes after '=' padding ignored in non-strict mode
trailing_ok = (binascii.a2b_base64(b"YWJj==EXTRA_GARBAGE") == b"abc" or binascii.a2b_base64(b"YWI=EXTRA") == b"ab")

# strict_mode / validate=True rejects invalid input
strict_b64_err = False
try:
    base64.b64decode(b"YW!Jj", validate=True)
except binascii.Error:
    strict_b64_err = True

# hexlify / unhexlify / crc32
hx = binascii.hexlify(b"\x00\xab\xff")
uhx = binascii.unhexlify(b"  00abff \n")
hex_ok = (hx == b"00abff" and uhx == b"\x00\xab\xff")

odd_hex_err = False
try:
    binascii.unhexlify(b"abc")
except binascii.Error:
    odd_hex_err = True

crc_ok = (binascii.crc32(b"hello-world") == zlib.crc32(b"hello-world"))

# zlib levels -1..9, wbits 15 / -15 / 31 / 47, empty/small/large
zlib_ok = True
for payload in (b"", b"x", b"hello world " * 500):
    for lvl in (-1, 0, 1, 6, 9):
        c = zlib.compress(payload, lvl)
        if zlib.decompress(c) != payload:
            zlib_ok = False
    for wb_c, wb_d in ((15, 15), (-15, -15), (31, 31), (31, 47), (15, 47)):
        c = zlib.compress(payload, 6, wb_c)
        if zlib.decompress(c, wb_d) != payload:
            zlib_ok = False

zlib_err = False
try:
    zlib.decompress(b"not valid zlib data!!!")
except zlib.error:
    zlib_err = True

ok = (b64_ok and b2a_ok and trailing_ok and strict_b64_err and hex_ok and odd_hex_err and crc_ok and zlib_ok and zlib_err)
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("ok"))
	if !ok || !got.IsBool() || !got.AsBool() {
		t.Fatalf("binascii/base64/zlib test failed: ok=%v", got)
	}
}

func TestCodecsConcurrentRaceSafety(t *testing.T) {
	eng, v := runPySource(t, `
import base64
import binascii
import threading
import zlib

payload = (b"concurrent codec payload 0123456789 " * 120)
results = [False] * 6

def worker(idx):
    local_ok = True
    for i in range(40):
        e = base64.b64encode(payload)
        if base64.b64decode(e) != payload:
            local_ok = False
        h = binascii.hexlify(payload)
        if binascii.unhexlify(h) != payload:
            local_ok = False
        lvl = (i % 10)
        c = zlib.compress(payload, lvl)
        if zlib.decompress(c) != payload:
            local_ok = False
        cg = zlib.compress(payload, 1, 31)
        if zlib.decompress(cg, 31) != payload:
            local_ok = False
        cf = zlib.compress(payload, 1, -15)
        if zlib.decompress(cf, -15) != payload:
            local_ok = False
    results[idx] = local_ok

threads = [threading.Thread(target=worker, args=(i,)) for i in range(6)]
for th in threads:
    th.start()
for th in threads:
    th.join()

ok = all(results)
`)
	defer eng.ReleaseVM(v)
	got, ok := v.Globals().Get(val.InternString("ok"))
	if !ok || !got.IsBool() || !got.AsBool() {
		t.Fatalf("concurrent codec test failed: ok=%v", got)
	}
}
