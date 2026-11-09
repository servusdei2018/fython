package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

type keyTable struct {
	keyType          val.Value
	keyHttp          val.Value
	keyAsgi          val.Value
	keyVersion       val.Value
	keySpecVersion   val.Value
	keyHttpVersion   val.Value
	keyMethod        val.Value
	keyScheme        val.Value
	keyPath          val.Value
	keyRawPath       val.Value
	keyQueryString   val.Value
	keyRootPath      val.Value
	keyHeaders       val.Value
	keyClient        val.Value
	keyServer        val.Value
	keyState         val.Value
	keyHttpRequest   val.Value
	keyHttpRespStart val.Value
	keyHttpRespBody  val.Value
	keyStatus        val.Value
	keyBody          val.Value
	keyMoreBody      val.Value

	keyRequestMethod    val.Value
	keyScriptName       val.Value
	keyPathInfo         val.Value
	keyQueryStringWsgi  val.Value
	keyServerName       val.Value
	keyServerPort       val.Value
	keyServerProtocol   val.Value
	keyContentType      val.Value
	keyContentLength    val.Value
	keyWsgiVersion      val.Value
	keyWsgiUrlScheme    val.Value
	keyWsgiInput        val.Value
	keyWsgiErrors       val.Value
	keyWsgiMultithread  val.Value
	keyWsgiMultiprocess val.Value
	keyWsgiRunOnce      val.Value

	valEmptyBytes val.Value
	valEmptyStr   val.Value
	valHttp       val.Value
	valHttp11     val.Value
	valHttp20     val.Value
	valAsgiSpec   val.Value
	valWsgiVer    val.Value
}

var keys keyTable

func init() {
	keys = keyTable{
		keyType:          val.InternString("type"),
		keyHttp:          val.InternString("http"),
		keyAsgi:          val.InternString("asgi"),
		keyVersion:       val.InternString("version"),
		keySpecVersion:   val.InternString("spec_version"),
		keyHttpVersion:   val.InternString("http_version"),
		keyMethod:        val.InternString("method"),
		keyScheme:        val.InternString("scheme"),
		keyPath:          val.InternString("path"),
		keyRawPath:       val.InternString("raw_path"),
		keyQueryString:   val.InternString("query_string"),
		keyRootPath:      val.InternString("root_path"),
		keyHeaders:       val.InternString("headers"),
		keyClient:        val.InternString("client"),
		keyServer:        val.InternString("server"),
		keyState:         val.InternString("state"),
		keyHttpRequest:   val.InternString("http.request"),
		keyHttpRespStart: val.InternString("http.response.start"),
		keyHttpRespBody:  val.InternString("http.response.body"),
		keyStatus:        val.InternString("status"),
		keyBody:          val.InternString("body"),
		keyMoreBody:      val.InternString("more_body"),

		keyRequestMethod:    val.InternString("REQUEST_METHOD"),
		keyScriptName:       val.InternString("SCRIPT_NAME"),
		keyPathInfo:         val.InternString("PATH_INFO"),
		keyQueryStringWsgi:  val.InternString("QUERY_STRING"),
		keyServerName:       val.InternString("SERVER_NAME"),
		keyServerPort:       val.InternString("SERVER_PORT"),
		keyServerProtocol:   val.InternString("SERVER_PROTOCOL"),
		keyContentType:      val.InternString("CONTENT_TYPE"),
		keyContentLength:    val.InternString("CONTENT_LENGTH"),
		keyWsgiVersion:      val.InternString("wsgi.version"),
		keyWsgiUrlScheme:    val.InternString("wsgi.url_scheme"),
		keyWsgiInput:        val.InternString("wsgi.input"),
		keyWsgiErrors:       val.InternString("wsgi.errors"),
		keyWsgiMultithread:  val.InternString("wsgi.multithread"),
		keyWsgiMultiprocess: val.InternString("wsgi.multiprocess"),
		keyWsgiRunOnce:      val.InternString("wsgi.run_once"),

		valEmptyBytes: val.NewBytesVal(nil),
		valEmptyStr:   val.InternString(""),
		valHttp:       val.InternString("http"),
		valHttp11:     val.InternString("1.1"),
		valHttp20:     val.InternString("2.0"),
	}

	asgiDict := val.NewDictVal(2).AsDict()
	asgiDict.Set(keys.keyVersion, val.InternString("3.0"))
	asgiDict.Set(keys.keySpecVersion, val.InternString("2.3"))
	keys.valAsgiSpec = val.DictVal(asgiDict)

	keys.valWsgiVer = val.NewTupleVal(val.Int64Val(1), val.Int64Val(0))

	fNone := val.NewFutureVal()
	fNone.AsFuture().SetResult(val.NoneVal())
	completedNoneFuture = fNone

	eventDict := val.NewDictVal(4).AsDict()
	eventDict.Set(keys.keyType, keys.keyHttpRequest)
	eventDict.Set(keys.keyBody, keys.valEmptyBytes)
	eventDict.Set(keys.keyMoreBody, val.BoolVal(false))

	fEmpty := val.NewFutureVal()
	fEmpty.AsFuture().SetResult(val.DictVal(eventDict))
	completedEmptyReqFuture = fEmpty
}

// Config specifies runtime options for the native HTTP server.
type Config struct {
	Host      string
	Port      int
	Workers   int
	Interface string // "auto", "asgi", "wsgi"
	Engine    *vm.Engine
	App       val.Value // Preloaded application callable
}

// Server exposes a Python ASGI 3.0 or PEP 3333 WSGI application over Go's net/http.
type Server struct {
	cfg        Config
	engine     *vm.Engine
	app        val.Value
	isASGI     bool
	vmRing     chan *vm.VM
	httpServer *http.Server
	listener   net.Listener
	mu         sync.Mutex
}

// NewServer initializes a Server instance for the given module:callable target.
func NewServer(target string, cfg Config) (*Server, error) {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 8000
	}
	if cfg.Workers <= 0 {
		cfg.Workers = runtime.GOMAXPROCS(0)
	}
	if cfg.Engine == nil {
		cfg.Engine = vm.NewEngine()
	}

	cfg.Engine.SetSharedModules(true)
	val.SetMultiThreaded(true)

	s := &Server{
		cfg:    cfg,
		engine: cfg.Engine,
	}

	var appVal val.Value
	switch {
	case !cfg.App.IsNone() && cfg.App.HeaderPtr() != nil:
		appVal = cfg.App
	case target != "":
		bootstrapVM := cfg.Engine.AcquireVM()
		defer cfg.Engine.ReleaseVM(bootstrapVM)

		resolved, err := resolveAppCallable(bootstrapVM, target)
		if err != nil {
			return nil, err
		}
		appVal = resolved
	default:
		return nil, errors.New("server: application target or callable must be specified")
	}

	s.app = appVal

	switch strings.ToLower(cfg.Interface) {
	case "asgi":
		s.isASGI = true
	case "wsgi":
		s.isASGI = false
	default:
		s.isASGI = isASGIApp(appVal)
	}

	s.vmRing = make(chan *vm.VM, cfg.Workers)
	for range cfg.Workers {
		s.vmRing <- s.engine.NewVM()
	}

	s.httpServer = &http.Server{
		Handler:        s,
		ReadTimeout:    30 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	return s, nil
}

// IsASGI reports whether the server is operating in ASGI 3.0 mode.
func (s *Server) IsASGI() bool {
	return s.isASGI
}

// ServeHTTP multiplexes an incoming HTTP request across a worker VM.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var workerVM *vm.VM
	select {
	case workerVM = <-s.vmRing:
		defer func() { s.vmRing <- workerVM }()
	default:
		workerVM = s.engine.NewVM()
	}

	if s.isASGI {
		s.handleASGI(w, r, workerVM)
	} else {
		s.handleWSGI(w, r, workerVM)
	}
}

// Serve starts listening on l and serving incoming HTTP requests.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	s.listener = l
	s.mu.Unlock()
	return s.httpServer.Serve(l)
}

// ListenAndServe binds to host:port and serves incoming HTTP requests.
func (s *Server) ListenAndServe() error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

// Shutdown gracefully terminates the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's network listener address, or nil if not listening.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

func isASGIApp(app val.Value) bool {
	if app.IsFunction() {
		coVal := app.AsFunction().Code
		c := vm.AsCodeObject(coVal)
		if c != nil {
			if c.IsCoroutine || (c.Flags&vm.CO_COROUTINE) != 0 {
				return true
			}
			if c.NumParams >= 3 || len(c.ParamNames) >= 3 {
				return true
			}
			if c.NumParams == 2 || len(c.ParamNames) == 2 {
				return false
			}
		}
	}
	if app.IsInstance() {
		inst := app.AsInstance()
		if attr, ok := inst.GetAttr(val.InternString("_is_coroutine")); ok && attr.IsTruthy() {
			return true
		}
		if callAttr, ok := inst.GetAttr(val.InternString("__call__")); ok && callAttr.IsFunction() {
			return isASGIApp(callAttr)
		}
	}
	return false
}

func resolveAppCallable(v *vm.VM, target string) (val.Value, error) {
	parts := strings.Split(target, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return val.NoneVal(), fmt.Errorf("server: invalid target %q; expected 'module:callable' format", target)
	}
	modTarget, callableName := parts[0], parts[1]

	var modName string
	if strings.HasSuffix(modTarget, ".py") || strings.ContainsRune(modTarget, filepath.Separator) {
		absPath, err := filepath.Abs(modTarget)
		if err != nil {
			return val.NoneVal(), fmt.Errorf("server: cannot resolve file path %q: %w", modTarget, err)
		}
		dir := filepath.Dir(absPath)
		modName = strings.TrimSuffix(filepath.Base(absPath), ".py")
		prependSysPath(v, dir)
	} else {
		if cwd, err := os.Getwd(); err == nil {
			prependSysPath(v, cwd)
		}
		modName = modTarget
	}

	modVal, err := v.ImportModule(modName, 0, true, nil)
	if err != nil {
		return val.NoneVal(), fmt.Errorf("server: failed to import module %q: %w", modName, err)
	}
	if !modVal.IsModule() {
		return val.NoneVal(), fmt.Errorf("server: %q is not a module", modName)
	}

	callableVal, ok := modVal.AsModule().Dict.Get(val.InternString(callableName))
	if !ok || callableVal.IsNone() || callableVal.HeaderPtr() == nil {
		return val.NoneVal(), fmt.Errorf("server: callable %q not found in module %q", callableName, modName)
	}

	return callableVal, nil
}

func prependSysPath(v *vm.VM, dir string) {
	sysModVal, ok := v.GetModule("sys")
	if !ok || !sysModVal.IsModule() {
		return
	}
	pathVal, ok := sysModVal.AsModule().Dict.Get(val.InternString("path"))
	if !ok || !pathVal.IsList() {
		return
	}
	list := pathVal.AsList()
	for _, it := range list.Items {
		if it.IsAnyString() && it.AsString() == dir {
			return
		}
	}
	list.Items = append([]val.Value{val.NewStringVal(dir)}, list.Items...)
}

func splitHostPort(addr string) (string, int64) {
	if addr == "" {
		return "127.0.0.1", 8000
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 80
	}
	port, _ := strconv.ParseInt(p, 10, 64)
	return h, port
}

func valueToBytes(v val.Value) []byte {
	if v.IsBytes() {
		return v.AsBytes().Data
	}
	if v.IsByteArray() {
		return v.AsByteArray().Data
	}
	if v.IsMemoryView() {
		return v.AsMemoryView().Buf
	}
	if v.IsAnyString() {
		return []byte(v.AsString())
	}
	return nil
}

func valueToString(v val.Value) string {
	if v.IsAnyString() {
		return v.AsString()
	}
	if v.IsBytes() {
		return string(v.AsBytes().Data)
	}
	if v.IsByteArray() {
		return string(v.AsByteArray().Data)
	}
	return v.String()
}
