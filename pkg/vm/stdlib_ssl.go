package vm

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

type sslContextState struct {
	protocol       int
	checkHostname  bool
	verifyMode     int
	options        int64
	verifyFlags    int64
	minVersion     uint16
	maxVersion     uint16
	alpnProtocols  []string
	certPool       *x509.CertPool
	certificates   []tls.Certificate
	ciphers        string
	keylogFilename string
}

type sslSocketState struct {
	tlsConn        *tls.Conn
	rawSock        val.Value
	fd             int
	serverSide     bool
	serverHostname string
	ctxState       *sslContextState
	handshakeDone  bool
	closed         bool
	mu             sync.Mutex
}

var (
	sslContextMap sync.Map // map[*val.PyInstance]*sslContextState
	sslSocketMap  sync.Map // map[*val.PyInstance]*sslSocketState
)

func (e *Engine) createSSLModule() val.Value {
	modVal := val.NewModuleVal("_ssl", "TLS/SSL wrapper module using crypto/tls")
	mod := modVal.AsModule()

	// Versions
	mod.Dict.Set(val.InternString("OPENSSL_VERSION_NUMBER"), val.Int64Val(0x30000000))
	mod.Dict.Set(val.InternString("OPENSSL_VERSION_INFO"), val.NewTupleVal(
		val.Int64Val(3), val.Int64Val(0), val.Int64Val(0), val.Int64Val(0), val.Int64Val(0),
	))
	mod.Dict.Set(val.InternString("OPENSSL_VERSION"), val.NewStringVal("OpenSSL 3.0.0 (Go crypto/tls)"))
	mod.Dict.Set(val.InternString("_OPENSSL_API_VERSION"), val.NewTupleVal(
		val.Int64Val(3), val.Int64Val(0), val.Int64Val(0), val.Int64Val(0), val.Int64Val(0),
	))
	mod.Dict.Set(val.InternString("_DEFAULT_CIPHERS"), val.NewStringVal("DEFAULT:!aNULL:!eNULL:!MD5:!3DES:!DES:!RC4:!IDEA:!SEED:!aDSS:!SRP:!PSK"))

	// Features
	mod.Dict.Set(val.InternString("HAS_SNI"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_ECDH"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_NPN"), val.BoolVal(false))
	mod.Dict.Set(val.InternString("HAS_ALPN"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_SSLv2"), val.BoolVal(false))
	mod.Dict.Set(val.InternString("HAS_SSLv3"), val.BoolVal(false))
	mod.Dict.Set(val.InternString("HAS_TLSv1"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_TLSv1_1"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_TLSv1_2"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_TLSv1_3"), val.BoolVal(true))
	mod.Dict.Set(val.InternString("HAS_PSK"), val.BoolVal(false))
	mod.Dict.Set(val.InternString("HAS_PSK_TLS13"), val.BoolVal(false))
	mod.Dict.Set(val.InternString("HAS_PHA"), val.BoolVal(false))

	// Protocols
	mod.Dict.Set(val.InternString("PROTOCOL_SSLv23"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("PROTOCOL_TLS"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("PROTOCOL_TLSv1"), val.Int64Val(3))
	mod.Dict.Set(val.InternString("PROTOCOL_TLSv1_1"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("PROTOCOL_TLSv1_2"), val.Int64Val(5))
	mod.Dict.Set(val.InternString("PROTOCOL_TLS_CLIENT"), val.Int64Val(16))
	mod.Dict.Set(val.InternString("PROTOCOL_TLS_SERVER"), val.Int64Val(17))

	// Options
	mod.Dict.Set(val.InternString("OP_ALL"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("OP_NO_SSLv2"), val.Int64Val(0x01000000))
	mod.Dict.Set(val.InternString("OP_NO_SSLv3"), val.Int64Val(0x02000000))
	mod.Dict.Set(val.InternString("OP_NO_TLSv1"), val.Int64Val(0x04000000))
	mod.Dict.Set(val.InternString("OP_NO_TLSv1_1"), val.Int64Val(0x10000000))
	mod.Dict.Set(val.InternString("OP_NO_TLSv1_2"), val.Int64Val(0x08000000))
	mod.Dict.Set(val.InternString("OP_NO_TLSv1_3"), val.Int64Val(0x20000000))
	mod.Dict.Set(val.InternString("OP_CIPHER_SERVER_PREFERENCE"), val.Int64Val(0x00400000))
	mod.Dict.Set(val.InternString("OP_SINGLE_DH_USE"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("OP_SINGLE_ECDH_USE"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("OP_NO_TICKET"), val.Int64Val(0x00004000))
	mod.Dict.Set(val.InternString("OP_NO_COMPRESSION"), val.Int64Val(0x00020000))
	mod.Dict.Set(val.InternString("OP_NO_RENEGOTIATION"), val.Int64Val(0x40000000))
	mod.Dict.Set(val.InternString("OP_ENABLE_MIDDLEBOX_COMPAT"), val.Int64Val(0x00100000))

	// Alert Descriptions
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_CLOSE_NOTIFY"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_UNEXPECTED_MESSAGE"), val.Int64Val(10))
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_BAD_RECORD_MAC"), val.Int64Val(20))
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_HANDSHAKE_FAILURE"), val.Int64Val(40))
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_CERTIFICATE_UNKNOWN"), val.Int64Val(46))
	mod.Dict.Set(val.InternString("ALERT_DESCRIPTION_INTERNAL_ERROR"), val.Int64Val(80))

	// SSL Errors
	mod.Dict.Set(val.InternString("SSL_ERROR_ZERO_RETURN"), val.Int64Val(6))
	mod.Dict.Set(val.InternString("SSL_ERROR_WANT_READ"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("SSL_ERROR_WANT_WRITE"), val.Int64Val(3))
	mod.Dict.Set(val.InternString("SSL_ERROR_WANT_X509_LOOKUP"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("SSL_ERROR_SYSCALL"), val.Int64Val(5))
	mod.Dict.Set(val.InternString("SSL_ERROR_SSL"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("SSL_ERROR_WANT_CONNECT"), val.Int64Val(7))
	mod.Dict.Set(val.InternString("SSL_ERROR_EOF"), val.Int64Val(8))
	mod.Dict.Set(val.InternString("SSL_ERROR_INVALID_ERROR_CODE"), val.Int64Val(10))

	// Verify Flags & Modes
	mod.Dict.Set(val.InternString("VERIFY_DEFAULT"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("VERIFY_CRL_CHECK_LEAF"), val.Int64Val(0x4))
	mod.Dict.Set(val.InternString("VERIFY_CRL_CHECK_CHAIN"), val.Int64Val(0xc))
	mod.Dict.Set(val.InternString("VERIFY_X509_STRICT"), val.Int64Val(0x20))
	mod.Dict.Set(val.InternString("VERIFY_X509_PARTIAL_CHAIN"), val.Int64Val(0x80000))

	mod.Dict.Set(val.InternString("CERT_NONE"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("CERT_OPTIONAL"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("CERT_REQUIRED"), val.Int64Val(2))

	// Proto constants
	mod.Dict.Set(val.InternString("PROTO_MINIMUM_SUPPORTED"), val.Int64Val(-2))
	mod.Dict.Set(val.InternString("PROTO_SSLv3"), val.Int64Val(0x0300))
	mod.Dict.Set(val.InternString("PROTO_TLSv1"), val.Int64Val(0x0301))
	mod.Dict.Set(val.InternString("PROTO_TLSv1_1"), val.Int64Val(0x0302))
	mod.Dict.Set(val.InternString("PROTO_TLSv1_2"), val.Int64Val(0x0303))
	mod.Dict.Set(val.InternString("PROTO_TLSv1_3"), val.Int64Val(0x0304))
	mod.Dict.Set(val.InternString("PROTO_MAXIMUM_SUPPORTED"), val.Int64Val(-1))

	// Exceptions
	sslErrorType := val.MustNewType("SSLError", []*val.PyType{val.OSErrorType}, nil)
	mod.Dict.Set(val.InternString("SSLError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLZeroReturnError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLWantReadError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLWantWriteError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLSyscallError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLEOFError"), val.TypeVal(sslErrorType))
	mod.Dict.Set(val.InternString("SSLCertVerificationError"), val.TypeVal(sslErrorType))

	// Functions: txt2obj, nid2obj, RAND_status, RAND_add, RAND_bytes, get_sigalgs
	mod.Dict.Set(val.InternString("txt2obj"), val.NewModuleFuncVal("txt2obj", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.Int64Val(0), val.NewStringVal(""), val.NewStringVal(""), val.NewStringVal(""))
	}))
	mod.Dict.Set(val.InternString("nid2obj"), val.NewModuleFuncVal("nid2obj", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(val.Int64Val(0), val.NewStringVal(""), val.NewStringVal(""), val.NewStringVal(""))
	}))
	mod.Dict.Set(val.InternString("RAND_status"), val.NewModuleFuncVal("RAND_status", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(1)
	}))
	mod.Dict.Set(val.InternString("RAND_add"), val.NewModuleFuncVal("RAND_add", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))
	mod.Dict.Set(val.InternString("RAND_bytes"), val.NewModuleFuncVal("RAND_bytes", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		n := int(args[0].AsInt64())
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return val.NewBytesVal(b)
	}))
	mod.Dict.Set(val.InternString("get_sigalgs"), val.NewModuleFuncVal("get_sigalgs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	mod.Dict.Set(val.InternString("get_default_verify_paths"), val.NewModuleFuncVal("get_default_verify_paths", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewTupleVal(
			val.NewStringVal("SSL_CERT_FILE"),
			val.NewStringVal("/etc/pki/tls/cert.pem"),
			val.NewStringVal("SSL_CERT_DIR"),
			val.NewStringVal("/etc/pki/tls/certs"),
		)
	}))

	// SSLSession dummy class
	sslSessionType := val.MustNewType("SSLSession", []*val.PyType{val.ObjectType}, nil)
	mod.Dict.Set(val.InternString("SSLSession"), val.TypeVal(sslSessionType))

	// MemoryBIO
	memoryBIOType := val.MustNewType("MemoryBIO", []*val.PyType{val.ObjectType}, nil)
	memoryBIOType.Dict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(0)
	}))
	memoryBIOType.Dict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewBytesVal([]byte{})
	}))
	memoryBIOType.Dict.Set(val.InternString("pending"), val.NewBuiltinFuncVal("pending", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(0)
	}))
	memoryBIOType.Dict.Set(val.InternString("eof"), val.NewBuiltinFuncVal("eof", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(true)
	}))
	mod.Dict.Set(val.InternString("MemoryBIO"), val.TypeVal(memoryBIOType))

	// _SSLSocket type
	sslSocketTypeDict := val.NewDictVal(32).AsDict()
	sslSocketType := val.MustNewType("_SSLSocket", []*val.PyType{val.ObjectType}, sslSocketTypeDict)

	// _SSLContext type
	sslContextTypeDict := val.NewDictVal(32).AsDict()
	sslContextType := val.MustNewType("_SSLContext", []*val.PyType{val.ObjectType}, sslContextTypeDict)
	mod.Dict.Set(val.InternString("_SSLContext"), val.TypeVal(sslContextType))

	// Helper to extract *sslContextState
	getContextState := func(inst val.Value) *sslContextState {
		if !inst.IsInstance() {
			panic("TypeError: descriptor requires a '_SSLContext' object")
		}
		if v, ok := sslContextMap.Load(inst.AsInstance()); ok {
			return v.(*sslContextState)
		}
		panic("ValueError: uninitialized _SSLContext")
	}

	// Helper to extract *sslSocketState
	getSSLSocketState := func(inst val.Value) *sslSocketState {
		if !inst.IsInstance() {
			panic("TypeError: descriptor requires a '_SSLSocket' object")
		}
		if v, ok := sslSocketMap.Load(inst.AsInstance()); ok {
			return v.(*sslSocketState)
		}
		panic("ValueError: uninitialized _SSLSocket")
	}

	// _SSLContext.__init__(protocol)
	sslContextTypeDict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("_SSLContext.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: _SSLContext.__init__() takes at least 1 argument")
		}
		self := args[0]
		proto := 2 // PROTOCOL_TLS
		if len(args) > 1 && !args[1].IsNone() {
			proto = int(args[1].AsInt64())
		}
		st := &sslContextState{
			protocol:      proto,
			checkHostname: false,
			verifyMode:    0, // CERT_NONE
			minVersion:    tls.VersionTLS12,
			maxVersion:    tls.VersionTLS13,
		}
		if proto == 16 { // PROTOCOL_TLS_CLIENT
			st.verifyMode = 2 // CERT_REQUIRED
			st.checkHostname = true
		}
		sslContextMap.Store(self.AsInstance(), st)
		return val.NoneVal()
	}))

	// Properties on _SSLContext
	makeCtxProp := func(name string, getter func(st *sslContextState) val.Value, setter func(st *sslContextState, v val.Value)) {
		var setFn val.Value = val.NoneVal()
		if setter != nil {
			setFn = val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) < 2 {
					panic("TypeError: descriptor '" + name + "' setter requires value")
				}
				st := getContextState(args[0])
				setter(st, args[1])
				return val.NoneVal()
			})
		}
		getFn := val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: descriptor '" + name + "' getter requires self")
			}
			st := getContextState(args[0])
			return getter(st)
		})
		sslContextTypeDict.Set(val.InternString(name), val.NewPropertyVal(getFn, setFn, val.NoneVal()))
	}

	makeCtxProp("protocol", func(st *sslContextState) val.Value { return val.Int64Val(int64(st.protocol)) }, nil)
	makeCtxProp("check_hostname",
		func(st *sslContextState) val.Value { return val.BoolVal(st.checkHostname) },
		func(st *sslContextState, v val.Value) { st.checkHostname = v.IsTruthy() },
	)
	makeCtxProp("verify_mode",
		func(st *sslContextState) val.Value { return val.Int64Val(int64(st.verifyMode)) },
		func(st *sslContextState, v val.Value) { st.verifyMode = int(v.AsInt64()) },
	)
	makeCtxProp("options",
		func(st *sslContextState) val.Value { return val.Int64Val(st.options) },
		func(st *sslContextState, v val.Value) { st.options = v.AsInt64() },
	)
	makeCtxProp("verify_flags",
		func(st *sslContextState) val.Value { return val.Int64Val(st.verifyFlags) },
		func(st *sslContextState, v val.Value) { st.verifyFlags = v.AsInt64() },
	)
	makeCtxProp("minimum_version",
		func(st *sslContextState) val.Value { return val.Int64Val(int64(st.minVersion)) },
		func(st *sslContextState, v val.Value) { st.minVersion = uint16(v.AsInt64()) },
	)
	makeCtxProp("maximum_version",
		func(st *sslContextState) val.Value { return val.Int64Val(int64(st.maxVersion)) },
		func(st *sslContextState, v val.Value) { st.maxVersion = uint16(v.AsInt64()) },
	)
	makeCtxProp("hostname_checks_common_name",
		func(st *sslContextState) val.Value { return val.BoolVal(false) },
		func(st *sslContextState, v val.Value) {},
	)
	makeCtxProp("keylog_filename",
		func(st *sslContextState) val.Value {
			if st.keylogFilename == "" {
				return val.NoneVal()
			}
			return val.NewStringVal(st.keylogFilename)
		},
		func(st *sslContextState, v val.Value) {
			if v.IsNone() {
				st.keylogFilename = ""
			} else {
				st.keylogFilename = v.AsString()
			}
		},
	)

	makeCtxProp("post_handshake_auth",
		func(st *sslContextState) val.Value { return val.BoolVal(false) },
		func(st *sslContextState, v val.Value) {},
	)

	// _SSLContext methods
	sslContextTypeDict.Set(val.InternString("load_cert_chain"), val.NewBuiltinFuncVal("load_cert_chain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: load_cert_chain() takes at least 1 argument")
		}
		st := getContextState(args[0])
		certfile := args[1].AsString()
		keyfile := certfile
		if len(args) > 2 && !args[2].IsNone() {
			keyfile = args[2].AsString()
		}
		cert, err := tls.LoadX509KeyPair(certfile, keyfile)
		if err != nil {
			panic("SSLError: " + err.Error())
		}
		st.certificates = append(st.certificates, cert)
		return val.NoneVal()
	}))

	sslContextTypeDict.Set(val.InternString("load_verify_locations"), val.NewBuiltinFuncVal("load_verify_locations", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getContextState(args[0])
		if st.certPool == nil {
			st.certPool = x509.NewCertPool()
		}
		var cafile, cadata string
		if len(args) > 1 && !args[1].IsNone() {
			cafile = args[1].AsString()
		}
		if len(args) > 3 && !args[3].IsNone() {
			if args[3].IsBytes() {
				cadata = string(args[3].AsBytes().Data)
			} else {
				cadata = args[3].AsString()
			}
		}
		if cafile != "" {
			pemData, err := os.ReadFile(cafile)
			if err != nil {
				panic("SSLError: " + err.Error())
			}
			st.certPool.AppendCertsFromPEM(pemData)
		}
		if cadata != "" {
			st.certPool.AppendCertsFromPEM([]byte(cadata))
		}
		return val.NoneVal()
	}))

	sslContextTypeDict.Set(val.InternString("set_default_verify_paths"), val.NewBuiltinFuncVal("set_default_verify_paths", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getContextState(args[0])
		pool, err := x509.SystemCertPool()
		if err == nil && pool != nil {
			st.certPool = pool
		} else if st.certPool == nil {
			st.certPool = x509.NewCertPool()
		}
		return val.NoneVal()
	}))

	sslContextTypeDict.Set(val.InternString("_set_alpn_protocols"), val.NewBuiltinFuncVal("_set_alpn_protocols", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: _set_alpn_protocols() takes 1 argument")
		}
		st := getContextState(args[0])
		st.alpnProtocols = nil
		if args[1].IsList() {
			for _, item := range args[1].AsList().Items {
				st.alpnProtocols = append(st.alpnProtocols, item.AsString())
			}
		} else if args[1].IsTuple() {
			for _, item := range args[1].AsTuple().Items {
				st.alpnProtocols = append(st.alpnProtocols, item.AsString())
			}
		}
		return val.NoneVal()
	}))

	sslContextTypeDict.Set(val.InternString("set_ciphers"), val.NewBuiltinFuncVal("set_ciphers", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getContextState(args[0])
		if len(args) > 1 {
			st.ciphers = args[1].AsString()
		}
		return val.NoneVal()
	}))

	sslContextTypeDict.Set(val.InternString("get_ciphers"), val.NewBuiltinFuncVal("get_ciphers", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	sslContextTypeDict.Set(val.InternString("get_ca_certs"), val.NewBuiltinFuncVal("get_ca_certs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	// socketConn implements net.Conn on top of our socket fd and state
	createSocketConn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "sslsock")
		var st *socketState
		if val, ok := socketRegistry.Load(fd); ok {
			st = val.(*socketState)
		}
		return &netSocketConn{fd: fd, file: f, st: st}
	}

	// _wrap_socket(sock, server_side, server_hostname=None, owner=None, session=None)
	sslContextTypeDict.Set(val.InternString("_wrap_socket"), val.NewBuiltinFuncVal("_wrap_socket", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: _wrap_socket() takes at least 2 arguments")
		}
		ctxState := getContextState(args[0])
		sock := args[1]
		serverSide := args[2].IsTruthy()
		serverHostname := ""
		if len(args) > 3 && !args[3].IsNone() {
			serverHostname = args[3].AsString()
		}

		fdVal, _, ok := sock.Type().LookupMRO(val.InternString("fileno"))
		if !ok {
			panic("TypeError: sock must have fileno()")
		}
		fdRes := ctx.Invoke1(fdVal, sock)
		fd := int(fdRes.AsInt64())

		netConn := createSocketConn(fd)

		tlsConfig := &tls.Config{
			ServerName:         serverHostname,
			NextProtos:         ctxState.alpnProtocols,
			Certificates:       ctxState.certificates,
			RootCAs:            ctxState.certPool,
			ClientCAs:          ctxState.certPool,
			InsecureSkipVerify: !ctxState.checkHostname || ctxState.verifyMode == 0,
			MinVersion:         tls.VersionTLS12,
		}

		var tlsConn *tls.Conn
		if serverSide {
			tlsConn = tls.Server(netConn, tlsConfig)
		} else {
			tlsConn = tls.Client(netConn, tlsConfig)
		}

		sockState := &sslSocketState{
			tlsConn:        tlsConn,
			rawSock:        sock,
			fd:             fd,
			serverSide:     serverSide,
			serverHostname: serverHostname,
			ctxState:       ctxState,
		}

		inst := val.NewInstanceOfType(sslSocketType)
		sslSocketMap.Store(inst.AsInstance(), sockState)
		inst.AsInstance().SetAttr(val.InternString("context"), args[0])
		inst.AsInstance().SetAttr(val.InternString("server_side"), val.BoolVal(serverSide))
		if serverHostname != "" {
			inst.AsInstance().SetAttr(val.InternString("server_hostname"), val.NewStringVal(serverHostname))
		} else {
			inst.AsInstance().SetAttr(val.InternString("server_hostname"), val.NoneVal())
		}
		inst.AsInstance().SetAttr(val.InternString("session"), val.NoneVal())
		inst.AsInstance().SetAttr(val.InternString("session_reused"), val.BoolVal(false))
		return inst
	}))

	// _SSLSocket methods
	sslSocketTypeDict.Set(val.InternString("do_handshake"), val.NewBuiltinFuncVal("do_handshake", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		st.mu.Lock()
		defer st.mu.Unlock()
		if !st.handshakeDone {
			err := st.tlsConn.Handshake()
			if err != nil {
				panic("SSLError: " + err.Error())
			}
			st.handshakeDone = true
		}
		return val.NoneVal()
	}))

	sslSocketTypeDict.Set(val.InternString("read"), val.NewBuiltinFuncVal("read", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		st.mu.Lock()
		if !st.handshakeDone {
			st.mu.Unlock()
			err := st.tlsConn.Handshake()
			if err != nil {
				panic("SSLError: " + err.Error())
			}
			st.mu.Lock()
			st.handshakeDone = true
		}
		st.mu.Unlock()

		n := 1024
		if len(args) > 1 && !args[1].IsNone() {
			n = int(args[1].AsInt64())
		}

		if len(args) > 2 && !args[2].IsNone() {
			// read into buffer
			var target []byte
			if args[2].IsByteArray() {
				target = args[2].AsByteArray().Data
			} else if args[2].IsMemoryView() {
				target = args[2].AsMemoryView().Buf
			}
			readN, err := st.tlsConn.Read(target)
			if err != nil && err != io.EOF {
				panic("SSLError: " + err.Error())
			}
			return val.Int64Val(int64(readN))
		}

		buf := make([]byte, n)
		readN, err := st.tlsConn.Read(buf)
		if err != nil && err != io.EOF {
			panic("SSLError: " + err.Error())
		}
		return val.NewBytesVal(buf[:readN])
	}))

	sslSocketTypeDict.Set(val.InternString("write"), val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		st.mu.Lock()
		if !st.handshakeDone {
			st.mu.Unlock()
			err := st.tlsConn.Handshake()
			if err != nil {
				panic("SSLError: " + err.Error())
			}
			st.mu.Lock()
			st.handshakeDone = true
		}
		st.mu.Unlock()

		var data []byte
		if len(args) > 1 {
			if args[1].IsBytes() {
				data = args[1].AsBytes().Data
			} else if args[1].IsByteArray() {
				data = args[1].AsByteArray().Data
			} else if args[1].IsMemoryView() {
				data = args[1].AsMemoryView().Buf
			} else {
				data = []byte(args[1].AsString())
			}
		}

		writtenN, err := st.tlsConn.Write(data)
		if err != nil {
			panic("SSLError: " + err.Error())
		}
		return val.Int64Val(int64(writtenN))
	}))

	sslSocketTypeDict.Set(val.InternString("cipher"), val.NewBuiltinFuncVal("cipher", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		cs := st.tlsConn.ConnectionState()
		cName := tls.CipherSuiteName(cs.CipherSuite)
		if cName == "" {
			cName = "TLS_AES_128_GCM_SHA256"
		}
		vName := "TLSv1.3"
		if cs.Version == tls.VersionTLS12 {
			vName = "TLSv1.2"
		}
		return val.NewTupleVal(
			val.NewStringVal(cName),
			val.NewStringVal(vName),
			val.Int64Val(128),
		)
	}))

	sslSocketTypeDict.Set(val.InternString("version"), val.NewBuiltinFuncVal("version", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		cs := st.tlsConn.ConnectionState()
		if cs.Version == tls.VersionTLS12 {
			return val.NewStringVal("TLSv1.2")
		}
		return val.NewStringVal("TLSv1.3")
	}))

	sslSocketTypeDict.Set(val.InternString("selected_alpn_protocol"), val.NewBuiltinFuncVal("selected_alpn_protocol", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		proto := st.tlsConn.ConnectionState().NegotiatedProtocol
		if proto == "" {
			return val.NoneVal()
		}
		return val.NewStringVal(proto)
	}))

	sslSocketTypeDict.Set(val.InternString("getpeercert"), val.NewBuiltinFuncVal("getpeercert", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		binaryForm := false
		if len(args) > 1 && args[1].IsTruthy() {
			binaryForm = true
		}
		certs := st.tlsConn.ConnectionState().PeerCertificates
		if len(certs) == 0 {
			if binaryForm {
				return val.NoneVal()
			}
			return val.NewDictVal(0)
		}
		leaf := certs[0]
		if binaryForm {
			return val.NewBytesVal(leaf.Raw)
		}

		d := val.NewDictVal(8).AsDict()
		d.Set(val.InternString("version"), val.Int64Val(int64(leaf.Version)))
		d.Set(val.InternString("serialNumber"), val.NewStringVal(fmt.Sprintf("%X", leaf.SerialNumber)))
		d.Set(val.InternString("notBefore"), val.NewStringVal(leaf.NotBefore.Format("Jan 2 15:04:05 2006 GMT")))
		d.Set(val.InternString("notAfter"), val.NewStringVal(leaf.NotAfter.Format("Jan 2 15:04:05 2006 GMT")))

		subjTuple := val.NewTupleVal(
			val.NewTupleVal(val.NewTupleVal(val.NewStringVal("commonName"), val.NewStringVal(leaf.Subject.CommonName))),
		)
		d.Set(val.InternString("subject"), subjTuple)

		issuerTuple := val.NewTupleVal(
			val.NewTupleVal(val.NewTupleVal(val.NewStringVal("commonName"), val.NewStringVal(leaf.Issuer.CommonName))),
		)
		d.Set(val.InternString("issuer"), issuerTuple)

		var sans []val.Value
		for _, name := range leaf.DNSNames {
			sans = append(sans, val.NewTupleVal(val.NewStringVal("DNS"), val.NewStringVal(name)))
		}
		for _, ip := range leaf.IPAddresses {
			sans = append(sans, val.NewTupleVal(val.NewStringVal("IP Address"), val.NewStringVal(ip.String())))
		}
		d.Set(val.InternString("subjectAltName"), val.NewTupleVal(sans...))

		return val.ObjectVal(&d.Header)
	}))

	sslSocketTypeDict.Set(val.InternString("shutdown"), val.NewBuiltinFuncVal("shutdown", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		st.mu.Lock()
		defer st.mu.Unlock()
		if !st.closed {
			st.closed = true
			_ = st.tlsConn.CloseWrite()
		}
		return st.rawSock
	}))

	sslSocketTypeDict.Set(val.InternString("unwrap"), val.NewBuiltinFuncVal("unwrap", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSSLSocketState(args[0])
		return st.rawSock
	}))

	sslSocketTypeDict.Set(val.InternString("pending"), val.NewBuiltinFuncVal("pending", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(0)
	}))

	sslSocketTypeDict.Set(val.InternString("get_verified_chain"), val.NewBuiltinFuncVal("get_verified_chain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	sslSocketTypeDict.Set(val.InternString("get_unverified_chain"), val.NewBuiltinFuncVal("get_unverified_chain", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewListVal()
	}))

	sslSocketTypeDict.Set(val.InternString("uses_ktls_for_send"), val.NewBuiltinFuncVal("uses_ktls_for_send", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.BoolVal(false)
	}))

	sslSocketTypeDict.Set(val.InternString("get_channel_binding"), val.NewBuiltinFuncVal("get_channel_binding", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	sslSocketTypeDict.Set(val.InternString("verify_client_post_handshake"), val.NewBuiltinFuncVal("verify_client_post_handshake", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	_ = bytes.Compare
	return modVal
}

// netSocketConn implements net.Conn using the underlying socket fd
type netSocketConn struct {
	fd   int
	file *os.File
	st   *socketState
}

func (c *netSocketConn) Read(b []byte) (n int, err error) {
	for {
		n, err := syscall.Read(c.fd, b)
		if err == nil {
			return n, nil
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			if c.st != nil && c.st.timeout == 0 {
				return 0, syscall.EWOULDBLOCK
			}
			timeout := 30.0
			if c.st != nil && c.st.timeout > 0 {
				timeout = c.st.timeout
			}
			dur := time.Duration(timeout * float64(time.Second))
			tv := syscall.NsecToTimeval(dur.Nanoseconds())
			var rSet syscall.FdSet
			rSet.Bits[c.fd/64] |= 1 << (uint(c.fd) % 64)
			sn, sErr := syscall.Select(c.fd+1, &rSet, nil, nil, &tv)
			if sErr != nil {
				return 0, sErr
			}
			if sn == 0 {
				return 0, syscall.ETIMEDOUT
			}
			continue
		}
		return 0, err
	}
}

func (c *netSocketConn) Write(b []byte) (n int, err error) {
	total := 0
	for len(b) > 0 {
		written, err := syscall.Write(c.fd, b)
		if err == nil {
			total += written
			b = b[written:]
			continue
		}
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			if c.st != nil && c.st.timeout == 0 {
				return total, syscall.EWOULDBLOCK
			}
			timeout := 30.0
			if c.st != nil && c.st.timeout > 0 {
				timeout = c.st.timeout
			}
			dur := time.Duration(timeout * float64(time.Second))
			tv := syscall.NsecToTimeval(dur.Nanoseconds())
			var wSet syscall.FdSet
			wSet.Bits[c.fd/64] |= 1 << (uint(c.fd) % 64)
			sn, sErr := syscall.Select(c.fd+1, nil, &wSet, nil, &tv)
			if sErr != nil {
				return total, sErr
			}
			if sn == 0 {
				return total, syscall.ETIMEDOUT
			}
			continue
		}
		return total, err
	}
	return total, nil
}

func (c *netSocketConn) Close() error {
	return nil
}

func (c *netSocketConn) LocalAddr() net.Addr {
	sa, _ := syscall.Getsockname(c.fd)
	if a, ok := sa.(*syscall.SockaddrInet4); ok {
		return &net.TCPAddr{IP: net.IP(a.Addr[:]), Port: a.Port}
	}
	return &net.TCPAddr{}
}

func (c *netSocketConn) RemoteAddr() net.Addr {
	sa, _ := syscall.Getpeername(c.fd)
	if a, ok := sa.(*syscall.SockaddrInet4); ok {
		return &net.TCPAddr{IP: net.IP(a.Addr[:]), Port: a.Port}
	}
	return &net.TCPAddr{}
}

func (c *netSocketConn) SetDeadline(t time.Time) error      { return nil }
func (c *netSocketConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *netSocketConn) SetWriteDeadline(t time.Time) error { return nil }
