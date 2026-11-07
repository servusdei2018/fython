package vm_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/vm"
)

func runPythonScript(t *testing.T, code string) {
	t.Helper()
	engine := vm.NewEngine()
	engine.SetCompileHook(compiler.CompileSource)
	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("Compile error: %v", err)
	}
	v := engine.AcquireVM()
	defer engine.ReleaseVM(v)
	v.RunCode(co)
	if exc := v.UnhandledException(); !exc.IsNone() && exc.HeaderPtr() != nil {
		t.Fatalf("Python unhandled exception: %v", exc)
	}
}

func generateTestCertAndKey(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(24 * time.Hour)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("Serial number generation failed: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Fython Test"},
			CommonName:   "127.0.0.1",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate failed: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("failed to open cert.pem: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		t.Fatalf("failed to write data to cert.pem: %v", err)
	}
	certOut.Close()

	keyPath = filepath.Join(dir, "key.pem")
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("failed to open key.pem: %v", err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		t.Fatalf("failed to write data to key.pem: %v", err)
	}
	keyOut.Close()

	return certPath, keyPath
}

func TestHTTPAndSSLStack(t *testing.T) {
	tempDir := t.TempDir()
	certFile, keyFile := generateTestCertAndKey(t, tempDir)

	script := `
import http.server
import http.client
import urllib.request
import ssl
import threading
import json

# 1. Plain HTTP loopback with GET and POST
post_body = json.dumps({"agent": "fython", "status": "online"}).encode("utf-8")

class TestHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"HTTP OK"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        data = self.rfile.read(length)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        pass

server = http.server.HTTPServer(("127.0.0.1", 0), TestHandler)
port = server.server_port
t = threading.Thread(target=server.serve_forever)
t.daemon = True
t.start()

try:
    # Test GET via urllib.request
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/") as resp:
        assert resp.status == 200, f"Expected 200, got {resp.status}"
        assert resp.read() == b"HTTP OK"

    # Test POST via urllib.request
    req = urllib.request.Request(f"http://127.0.0.1:{port}/post", data=post_body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req) as resp:
        assert resp.status == 200
        echoed = json.loads(resp.read().decode("utf-8"))
        assert echoed["agent"] == "fython"
        assert echoed["status"] == "online"
finally:
    server.shutdown()
    t.join()

# 2. HTTPS loopback with self-signed certificate and HTTPSConnection
server_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
server_ctx.load_cert_chain("` + certFile + `", "` + keyFile + `")

class HTTPSHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"HTTPS OK"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass

https_server = http.server.HTTPServer(("127.0.0.1", 0), HTTPSHandler)
https_server.socket = server_ctx.wrap_socket(https_server.socket, server_side=True)
https_port = https_server.server_port

ht = threading.Thread(target=https_server.serve_forever)
ht.daemon = True
ht.start()

client_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
client_ctx.check_hostname = False
client_ctx.verify_mode = ssl.CERT_NONE

try:
    conn = http.client.HTTPSConnection("127.0.0.1", https_port, context=client_ctx)
    conn.request("GET", "/")
    resp = conn.getresponse()
    assert resp.status == 200, f"Expected 200, got {resp.status}"
    payload = resp.read()
    assert payload == b"HTTPS OK", f"Unexpected payload: {payload}"
    conn.close()
finally:
    https_server.shutdown()
    ht.join()
`
	runPythonScript(t, script)
}

func TestSubprocessPipeline(t *testing.T) {
	script := `
import subprocess
import os

# 1. subprocess.run with stdout capture and exit code
res = subprocess.run(["sh", "-c", "echo 'hello subprocess'"], stdout=subprocess.PIPE)
assert res.returncode == 0
assert b"hello subprocess" in res.stdout

# 2. Popen with pipe input/output redirection
proc = subprocess.Popen(["sh", "-c", "cat"], stdin=subprocess.PIPE, stdout=subprocess.PIPE)
out, err = proc.communicate(input=b"streamed pipeline input\n")
assert proc.returncode == 0
assert out == b"streamed pipeline input\n"

# 3. Environment variable inheritance
res = subprocess.run(["sh", "-c", "echo $FYTHON_SUB_VAR"], stdout=subprocess.PIPE, env=dict(os.environ, FYTHON_SUB_VAR="sub_val"))
assert b"sub_val" in res.stdout

# 4. CalledProcessError on check=True
caught_err = False
try:
    subprocess.run(["sh", "-c", "exit 42"], check=True)
except subprocess.CalledProcessError as e:
    caught_err = True
    assert e.returncode == 42
assert caught_err

# 5. TimeoutExpired
caught_timeout = False
try:
    subprocess.run(["sh", "-c", "sleep 5"], timeout=0.1)
except subprocess.TimeoutExpired:
    caught_timeout = True
assert caught_timeout
`
	runPythonScript(t, script)
}

func TestDecimalAndFractions(t *testing.T) {
	script := `
from decimal import Decimal, localcontext, ROUND_UP, ROUND_DOWN
from fractions import Fraction

# 1. Exact decimal arithmetic without float inaccuracies
d1 = Decimal('0.1')
d2 = Decimal('0.2')
d3 = Decimal('0.3')
assert d1 + d2 == d3, f"Expected 0.3, got {d1 + d2}"

# 2. Local quantization and rounding context
with localcontext() as ctx:
    ctx.prec = 4
    val = Decimal('1') / Decimal('3')
    assert str(val) == '0.3333'

with localcontext() as ctx:
    d = Decimal('1.25')
    q_up = d.quantize(Decimal('0.1'), rounding=ROUND_UP)
    q_down = d.quantize(Decimal('0.1'), rounding=ROUND_DOWN)
    assert q_up == Decimal('1.3')
    assert q_down == Decimal('1.2')

# 3. Exact rational fraction arithmetic
f1 = Fraction(1, 3)
f2 = Fraction(1, 6)
f_sum = f1 + f2
assert f_sum == Fraction(1, 2), f"Expected 1/2, got {f_sum}"
assert f_sum.numerator == 1
assert f_sum.denominator == 2

# Fraction from float and string
assert Fraction('3/7') + Fraction('4/7') == 1
assert Fraction(0.5) == Fraction(1, 2)
`
	runPythonScript(t, script)
}

func TestSQLite3Transactions(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	script := `
import sqlite3

def run_suite(db_name):
    conn = sqlite3.connect(db_name)
    cur = conn.cursor()
    cur.execute("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, balance REAL)")

    # 1. executemany with ?
    cur.executemany("INSERT INTO users (name, balance) VALUES (?, ?)", [
        ("Alice", 100.5),
        ("Bob", 200.0),
    ])

    # 2. executemany with :named parameters
    cur.executemany("INSERT INTO users (name, balance) VALUES (:name, :balance)", [
        {"name": "Charlie", "balance": 300.0},
        {"name": "Diana", "balance": 400.0},
    ])

    # 3. Transaction rollback on exception inside with conn:
    caught = False
    try:
        with conn:
            conn.execute("INSERT INTO users (name, balance) VALUES (?, ?)", ("Eve", 500.0))
            raise RuntimeError("Rollback test")
    except RuntimeError:
        caught = True
    assert caught, "Expected RuntimeError rollback exception"

    # 4. Query with conn.row_factory = sqlite3.Row
    conn.row_factory = sqlite3.Row
    cur = conn.cursor()
    cur.execute("SELECT id, name, balance FROM users ORDER BY id")
    rows = cur.fetchall()

    assert len(rows) == 4, f"Expected 4 rows, got {len(rows)}"

    # 5. Verify column access by name and index
    row0 = rows[0]
    assert row0[0] == 1
    assert row0[1] == "Alice"
    assert row0["name"] == "Alice"
    assert row0["balance"] == 100.5

    assert rows[1]["name"] == "Bob"
    assert rows[2]["name"] == "Charlie"
    assert rows[3]["name"] == "Diana"

    # Verify keys()
    assert row0.keys() == ["id", "name", "balance"]

    conn.close()

# Test both :memory: and file-backed persistence
run_suite(":memory:")
run_suite("` + strings.ReplaceAll(dbPath, "\\", "/") + `")
`
	runPythonScript(t, script)
}
