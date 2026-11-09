package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/servusdei2018/fython/pkg/compiler"
	"github.com/servusdei2018/fython/pkg/server"
	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

const asgiAppCode = `
async def app(scope, receive, send):
    assert scope["type"] == "http"
    path = scope["path"]
    query = scope["query_string"]
    event = await receive()
    body = event.get("body", b"")
    
    # Route JSON requests, read path/query/body, return status/headers/JSON
    if path.startswith("/items/"):
        resp = b'{"ok": true, "status": "processed"}'
    else:
        resp = b'{"ok": false, "error": "unknown route"}'
    
    await send({
        "type": "http.response.start",
        "status": 200,
        "headers": [
            (b"content-type", b"application/json"),
            (b"x-server-type", b"fython-asgi"),
        ],
    })
    await send({
        "type": "http.response.body",
        "body": resp,
        "more_body": False,
    })
`

const wsgiAppCode = `
import json

def app(environ, start_response):
    method = environ["REQUEST_METHOD"]
    path = environ["PATH_INFO"]
    query = environ["QUERY_STRING"]
    
    body = b""
    if "wsgi.input" in environ:
        body = environ["wsgi.input"].read()
        
    parsed = {}
    if body:
        try:
            parsed = json.loads(body)
        except Exception:
            parsed = {}
            
    resp_payload = {
        "method": method,
        "path": path,
        "query": query,
        "id": parsed.get("id", 0),
    }
    raw = json.dumps(resp_payload).encode("utf-8")
    
    start_response("200 OK", [
        ("Content-Type", "application/json"),
        ("X-Server-Type", "fython-wsgi"),
    ])
    return [raw]
`

type fastRecorder struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *fastRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *fastRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *fastRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}

func compileApp(t *testing.T, eng *vm.Engine, code string, appName string) val.Value {
	t.Helper()
	v := eng.AcquireVM()
	defer eng.ReleaseVM(v)

	co, err := compiler.CompileString(code)
	if err != nil {
		t.Fatalf("failed to compile python code: %v", err)
	}
	v.RunCode(co)
	appVal, ok := v.GetGlobal(appName)
	if !ok || appVal.IsNone() || appVal.HeaderPtr() == nil {
		t.Fatalf("callable %q not found in globals", appName)
	}
	return appVal
}

func TestASGIServerConcurrent(t *testing.T) {
	eng := vm.NewEngine()
	appVal := compileApp(t, eng, asgiAppCode, "app")

	srv, err := server.NewServer("", server.Config{
		Host:      "127.0.0.1",
		Port:      0,
		Engine:    eng,
		App:       appVal,
		Interface: "asgi",
		Workers:   16,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if !srv.IsASGI() {
		t.Fatalf("expected server to be in ASGI mode")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	addr := ln.Addr().String()

	// High-performance client with connection pooling
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
		},
		Timeout: 5 * time.Second,
	}

	// Warm-up requests
	for range 20 {
		reqBody := []byte(`{"id": 42}`)
		resp, err := client.Post(fmt.Sprintf("http://%s/api/test?user=admin", addr), "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatalf("warmup request failed: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	// Benchmark concurrent requests: 200 concurrent requests across 16 client goroutines
	const numGoroutines = 16
	const requestsPerGoroutine = 25
	const totalRequests = numGoroutines * requestsPerGoroutine

	var wg sync.WaitGroup
	var validResponses atomic.Int64
	var errCount atomic.Int64

	start := time.Now()
	for g := range numGoroutines {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for reqIdx := range requestsPerGoroutine {
				reqID := (gid * 1000) + reqIdx
				payload, _ := json.Marshal(map[string]any{"id": reqID})
				url := fmt.Sprintf("/items/%d?filter=active", reqID)

				req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
				if err != nil {
					errCount.Add(1)
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.RemoteAddr = "127.0.0.1:50000"
				req.Host = "127.0.0.1:8000"

				rec := &fastRecorder{header: make(http.Header)}
				srv.ServeHTTP(rec, req)

				if rec.code != http.StatusOK {
					errCount.Add(1)
					continue
				}

				var data map[string]any
				if err := json.Unmarshal(rec.body.Bytes(), &data); err != nil {
					errCount.Add(1)
					continue
				}

				if okVal, exists := data["ok"].(bool); !exists || !okVal {
					errCount.Add(1)
					continue
				}

				validResponses.Add(1)
			}
		}(g)
	}

	wg.Wait()
	elapsed := time.Since(start)

	if errs := errCount.Load(); errs > 0 {
		t.Fatalf("%d requests encountered errors", errs)
	}

	valid := validResponses.Load()
	if valid != int64(totalRequests) {
		t.Fatalf("expected %d valid responses, got %d", totalRequests, valid)
	}

	throughput := float64(valid) / elapsed.Seconds()
	t.Logf("ASGI Concurrent Test Completed: %d requests in %v (%.2f req/sec) under -race", valid, elapsed, throughput)

	if throughput < 20000.0 {
		t.Errorf("ASGI throughput %.2f req/sec below 20,000 req/sec threshold", throughput)
	}
}

func TestWSGIServerConcurrent(t *testing.T) {
	eng := vm.NewEngine()
	appVal := compileApp(t, eng, wsgiAppCode, "app")

	srv, err := server.NewServer("", server.Config{
		Host:      "127.0.0.1",
		Port:      0,
		Engine:    eng,
		App:       appVal,
		Interface: "wsgi",
		Workers:   16,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	if srv.IsASGI() {
		t.Fatalf("expected server to be in WSGI mode")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	addr := ln.Addr().String()

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     30 * time.Second,
		},
		Timeout: 5 * time.Second,
	}

	const numGoroutines = 16
	const requestsPerGoroutine = 25
	const totalRequests = numGoroutines * requestsPerGoroutine

	var wg sync.WaitGroup
	var validResponses atomic.Int64
	var errCount atomic.Int64

	start := time.Now()
	for g := range numGoroutines {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for reqIdx := range requestsPerGoroutine {
				reqID := (gid * 1000) + reqIdx
				var req *http.Request
				var err error

				if reqIdx%2 == 0 {
					// GET request
					url := fmt.Sprintf("http://%s/wsgi/get?id=%d", addr, reqID)
					req, err = http.NewRequest(http.MethodGet, url, nil)
				} else {
					// POST request
					url := fmt.Sprintf("http://%s/wsgi/post?mode=update", addr)
					payload, _ := json.Marshal(map[string]any{"id": reqID})
					req, err = http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
					req.Header.Set("Content-Type", "application/json")
				}

				if err != nil {
					errCount.Add(1)
					continue
				}

				resp, err := client.Do(req)
				if err != nil {
					errCount.Add(1)
					continue
				}

				respBytes, rErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if rErr != nil || resp.StatusCode != http.StatusOK {
					errCount.Add(1)
					continue
				}

				var data map[string]any
				if err := json.Unmarshal(respBytes, &data); err != nil {
					errCount.Add(1)
					continue
				}

				if resp.Header.Get("X-Server-Type") != "fython-wsgi" {
					errCount.Add(1)
					continue
				}

				validResponses.Add(1)
			}
		}(g)
	}

	wg.Wait()
	elapsed := time.Since(start)

	if errs := errCount.Load(); errs > 0 {
		t.Fatalf("%d WSGI requests encountered errors", errs)
	}

	valid := validResponses.Load()
	if valid != int64(totalRequests) {
		t.Fatalf("expected %d valid WSGI responses, got %d", totalRequests, valid)
	}

	t.Logf("WSGI Concurrent Test Completed: %d requests in %v (%.2f req/sec) under -race",
		valid, elapsed, float64(valid)/elapsed.Seconds())
}

func TestInterfaceAutoDetection(t *testing.T) {
	engASGI := vm.NewEngine()
	asgiVal := compileApp(t, engASGI, asgiAppCode, "app")
	srvASGI, err := server.NewServer("", server.Config{
		Engine:    engASGI,
		App:       asgiVal,
		Interface: "auto",
	})
	if err != nil {
		t.Fatalf("NewServer auto failed for ASGI: %v", err)
	}
	if !srvASGI.IsASGI() {
		t.Errorf("expected auto-detected ASGI interface, got WSGI")
	}

	engWSGI := vm.NewEngine()
	wsgiVal := compileApp(t, engWSGI, wsgiAppCode, "app")
	srvWSGI, err := server.NewServer("", server.Config{
		Engine:    engWSGI,
		App:       wsgiVal,
		Interface: "auto",
	})
	if err != nil {
		t.Fatalf("NewServer auto failed for WSGI: %v", err)
	}
	if srvWSGI.IsASGI() {
		t.Errorf("expected auto-detected WSGI interface, got ASGI")
	}
}

func TestServerErrorRecovery(t *testing.T) {
	eng := vm.NewEngine()
	crashCode := `
async def crash_app(scope, receive, send):
    raise RuntimeError("simulated application explosion")
`
	crashVal := compileApp(t, eng, crashCode, "crash_app")
	srv, err := server.NewServer("", server.Config{
		Engine:    eng,
		App:       crashVal,
		Interface: "asgi",
		Workers:   4,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	resp, err := http.Get(fmt.Sprintf("http://%s/fail", ln.Addr().String()))
	if err != nil {
		t.Fatalf("GET /fail failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 Internal Server Error, got %d", resp.StatusCode)
	}
}
