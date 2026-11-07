package vm_test

import "testing"

func TestPathlibTempfileIntegration(t *testing.T) {
	runSnippet(t, `
import os, tempfile
from pathlib import Path

with tempfile.TemporaryDirectory() as td:
    base = Path(td)
    sub = base / "nested" / "dir"
    sub.mkdir(parents=True, exist_ok=True)
    f1 = sub / "hello.txt"
    f1.write_text("Hello from Pathlib!")
    assert f1.read_text() == "Hello from Pathlib!"
    assert f1.exists()
    assert f1.is_file()
    assert not f1.is_dir()

    f2 = base / "root.txt"
    f2.write_text("Root file")
    
    txt_files = sorted(p.name for p in base.rglob("*.txt"))
    assert txt_files == ["hello.txt", "root.txt"], f"Unexpected files: {txt_files}"
`)
}

func TestDateTimeIntegration(t *testing.T) {
	runSnippet(t, `
import datetime
from datetime import date, time, datetime as dt, timedelta, timezone

d1 = date(2026, 11, 6)
assert d1.isoformat() == "2026-11-06"
assert d1.year == 2026 and d1.month == 11 and d1.day == 6

td = timedelta(days=2, hours=3, minutes=30)
d2 = d1 + td
assert d2.year == 2026 and d2.month == 11 and d2.day == 8
assert (d2 - d1).days == 2

t1 = dt(2026, 11, 6, 12, 0, 0, tzinfo=timezone.utc)
assert t1.isoformat() == "2026-11-06T12:00:00+00:00"

parsed = dt.fromisoformat("2026-11-06T18:42:00+00:00")
assert parsed.year == 2026 and parsed.hour == 18 and parsed.minute == 42
assert parsed.strftime("%Y/%m/%d %H:%M") == "2026/11/06 18:42"

diff = parsed - t1
assert isinstance(diff, timedelta)
assert diff.total_seconds() == 6 * 3600 + 42 * 60

now = dt.now()
assert now.year >= 2026
`)
}

func TestCryptoBatteriesIntegration(t *testing.T) {
	runSnippet(t, `
import hashlib
import hmac
import base64
import secrets
import uuid

# Base64 test
raw = b"Hello, Python 3.15 standard library!"
enc = base64.b64encode(raw)
assert base64.b64decode(enc) == raw, "b64decode mismatch"
url_enc = base64.urlsafe_b64encode(raw)
assert base64.urlsafe_b64decode(url_enc) == raw, f"urlsafe mismatch: got {base64.urlsafe_b64decode(url_enc)!r} expected {raw!r}"

# Hashlib test
h = hashlib.sha256(b"hello ")
h.update(b"world")
expected = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
assert h.hexdigest() == expected, f"hexdigest got {h.hexdigest()} expected {expected}"
h2 = h.copy()
assert h2.hexdigest() == expected, "copy hexdigest mismatch"

# HMAC test
key = b"secret_key"
msg = b"message to authenticate"
sig = hmac.new(key, msg, hashlib.sha256).hexdigest()
assert isinstance(sig, str) and len(sig) == 64, "hmac sig mismatch"
assert hmac.compare_digest(sig, sig), "compare_digest equal"
assert not hmac.compare_digest(sig, "0" * 64), "compare_digest not equal"

# Secrets test
tok = secrets.token_hex(16)
assert len(tok) == 32, "token_hex length"
tok_url = secrets.token_urlsafe(16)
assert isinstance(tok_url, str) and len(tok_url) > 0, "token_urlsafe"

# UUID test
u4 = uuid.uuid4()
assert u4.version == 4, "u4.version"
u4_str = str(u4)
assert len(u4_str) == 36, "u4_str length"
u4_parsed = uuid.UUID(u4_str)
assert u4_parsed == u4, f"u4_parsed mismatch: {u4_parsed} vs {u4}"
`)
}

func TestStructIntegration(t *testing.T) {
	runSnippet(t, `
import struct

# Standard packing test
fmt = "<h5sI"
packed = struct.pack(fmt, 42, b"hello", 123456)
assert len(packed) == struct.calcsize(fmt)
assert struct.calcsize(fmt) == 2 + 5 + 4

unpacked = struct.unpack(fmt, packed)
assert unpacked == (42, b"hello", 123456)

# Struct class test
s = struct.Struct(">i")
assert s.size == 4
assert s.format == ">i"
assert s.pack(1000) == b"\x00\x00\x03\xe8"
assert s.unpack(b"\x00\x00\x03\xe8") == (1000,)

# iter_unpack test
data = struct.pack("<2H", 1, 2) + struct.pack("<2H", 3, 4)
items = list(struct.iter_unpack("<2H", data))
assert items == [(1, 2), (3, 4)]
`)
}

func TestCsvIntegration(t *testing.T) {
	runSnippet(t, `
import csv, io

# Basic writer and reader test
out = io.StringIO()
w = csv.writer(out)
w.writerow(["name", "age", "city"])
w.writerow(["Alice", "30", "New York, NY"])
w.writerow(["Bob", "25", "San Francisco"])

content = out.getvalue()
inp = io.StringIO(content)
r = csv.reader(inp)
rows = list(r)
assert len(rows) == 3, f"rows: {rows}, content: {content!r}"
assert rows[0] == ["name", "age", "city"]
assert rows[1] == ["Alice", "30", "New York, NY"]
assert rows[2] == ["Bob", "25", "San Francisco"]

# DictWriter and DictReader test
d_out = io.StringIO()
fieldnames = ["id", "title"]
dw = csv.DictWriter(d_out, fieldnames=fieldnames)
dw.writeheader()
dw.writerow({"id": "1", "title": "Learning Python"})
dw.writerow({"id": "2", "title": "Advanced Go"})

d_inp = io.StringIO(d_out.getvalue())
dr = csv.DictReader(d_inp)
d_rows = list(dr)
assert len(d_rows) == 2
assert d_rows[0] == {"id": "1", "title": "Learning Python"}
assert d_rows[1] == {"id": "2", "title": "Advanced Go"}
`)
}

func TestZlibGzipZipfileIntegration(t *testing.T) {
	runSnippet(t, `
import zlib, gzip, zipfile, io

# Test zlib basic functions
orig = b"Hello, world! Battery-included standard library for fython."
compressed = zlib.compress(orig)
assert len(compressed) > 0
decompressed = zlib.decompress(compressed)
assert decompressed == orig

assert zlib.crc32(b"") == 0
assert zlib.crc32(orig) != 0
assert zlib.adler32(b"") == 1
assert zlib.adler32(orig) != 0

# Test compressobj and decompressobj
c = zlib.compressobj()
cdata = c.compress(orig) + c.flush()
d = zlib.decompressobj()
ddata = d.decompress(cdata) + d.flush()
assert ddata == orig

# Test gzip module round-trip
gz_data = gzip.compress(orig)
gz_decomp = gzip.decompress(gz_data)
assert gz_decomp == orig

# Test zipfile in-memory archive
bio = io.BytesIO()
with zipfile.ZipFile(bio, mode="w", compression=zipfile.ZIP_DEFLATED) as zf:
    zf.writestr("hello.txt", "Hello from inside zip archive!")
    zf.writestr("nested/data.bin", orig)

bio.seek(0)
with zipfile.ZipFile(bio, mode="r") as zf:
    names = zf.namelist()
    assert sorted(names) == ["hello.txt", "nested/data.bin"], f"names: {names}"
    txt = zf.read("hello.txt").decode("utf-8")
    assert txt == "Hello from inside zip archive!"
    bin_data = zf.read("nested/data.bin")
    assert bin_data == orig
`)
}

func TestUrllibParseIpaddressIntegration(t *testing.T) {
	runSnippet(t, `
import urllib.parse
from urllib.parse import urlparse, urlunparse, parse_qs, urlencode, quote, unquote
import ipaddress

# URL Parsing and Encoding
url = "https://user:pass@example.com:8080/path/to/resource?query=val&flag=1#section"
parsed = urlparse(url)
assert parsed.scheme == "https"
assert parsed.netloc == "user:pass@example.com:8080"
assert parsed.path == "/path/to/resource"
assert parsed.query == "query=val&flag=1"
assert parsed.fragment == "section"
assert parsed.port == 8080
assert parsed.username == "user"
assert parsed.password == "pass"

qs = parse_qs("name=Alice&tag=fast&tag=cool")
assert qs == {"name": ["Alice"], "tag": ["fast", "cool"]}, f"qs: {qs}"

encoded = urlencode({"lang": "Python", "version": "3.15"})
assert encoded == "lang=Python&version=3.15"

quoted = quote("hello world/test?=")
assert quoted == "hello%20world/test%3F%3D"
assert unquote(quoted) == "hello world/test?="

# IP Address and Networks
ip4 = ipaddress.ip_address("192.168.1.1")
assert ip4.version == 4
assert str(ip4) == "192.168.1.1"
assert not ip4.is_loopback

net4 = ipaddress.ip_network("192.168.1.0/24")
assert ip4 in net4

ip6 = ipaddress.ip_address("2001:db8::1")
assert ip6.version == 6
assert str(ip6) == "2001:db8::1"
net6 = ipaddress.ip_network("2001:db8::/32")
assert ip6 in net6
`)
}
