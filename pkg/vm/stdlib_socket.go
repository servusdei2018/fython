package vm

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/servusdei2018/fython/pkg/val"
)

type socketState struct {
	fd      int
	family  int
	typ     int
	proto   int
	timeout float64 // <0: blocking, 0: non-blocking, >0: timeout in seconds
	closed  bool
}

var (
	socketInstanceMap    sync.Map // map[*val.PyInstance]*socketState
	socketRegistry       sync.Map // map[int]*socketState
	globalDefaultTimeout *float64
	globalTimeoutMu      sync.RWMutex
)

func (e *Engine) createSocketModule() val.Value {
	modVal := val.NewModuleVal("_socket", "Low-level socket interface")
	mod := modVal.AsModule()

	// Constants
	// Address families
	mod.Dict.Set(val.InternString("AF_UNSPEC"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("AF_UNIX"), val.Int64Val(syscall.AF_UNIX))
	mod.Dict.Set(val.InternString("AF_INET"), val.Int64Val(syscall.AF_INET))
	mod.Dict.Set(val.InternString("AF_INET6"), val.Int64Val(syscall.AF_INET6))

	// Socket types
	mod.Dict.Set(val.InternString("SOCK_STREAM"), val.Int64Val(syscall.SOCK_STREAM))
	mod.Dict.Set(val.InternString("SOCK_DGRAM"), val.Int64Val(syscall.SOCK_DGRAM))
	mod.Dict.Set(val.InternString("SOCK_RAW"), val.Int64Val(syscall.SOCK_RAW))

	// Protocols
	mod.Dict.Set(val.InternString("IPPROTO_IP"), val.Int64Val(syscall.IPPROTO_IP))
	mod.Dict.Set(val.InternString("IPPROTO_TCP"), val.Int64Val(syscall.IPPROTO_TCP))
	mod.Dict.Set(val.InternString("IPPROTO_UDP"), val.Int64Val(syscall.IPPROTO_UDP))

	// Socket options
	mod.Dict.Set(val.InternString("SOL_SOCKET"), val.Int64Val(syscall.SOL_SOCKET))
	mod.Dict.Set(val.InternString("SOL_TCP"), val.Int64Val(syscall.SOL_TCP))
	mod.Dict.Set(val.InternString("SO_REUSEADDR"), val.Int64Val(syscall.SO_REUSEADDR))
	mod.Dict.Set(val.InternString("SO_REUSEPORT"), val.Int64Val(0x0f))
	mod.Dict.Set(val.InternString("SO_TYPE"), val.Int64Val(syscall.SO_TYPE))
	mod.Dict.Set(val.InternString("SO_ERROR"), val.Int64Val(syscall.SO_ERROR))
	mod.Dict.Set(val.InternString("SO_KEEPALIVE"), val.Int64Val(syscall.SO_KEEPALIVE))
	mod.Dict.Set(val.InternString("SO_BROADCAST"), val.Int64Val(syscall.SO_BROADCAST))
	mod.Dict.Set(val.InternString("SO_RCVBUF"), val.Int64Val(syscall.SO_RCVBUF))
	mod.Dict.Set(val.InternString("SO_SNDBUF"), val.Int64Val(syscall.SO_SNDBUF))
	mod.Dict.Set(val.InternString("TCP_NODELAY"), val.Int64Val(syscall.TCP_NODELAY))

	// Shutdown
	mod.Dict.Set(val.InternString("SHUT_RD"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("SHUT_WR"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("SHUT_RDWR"), val.Int64Val(2))

	// Special IP
	mod.Dict.Set(val.InternString("INADDR_ANY"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("INADDR_BROADCAST"), val.Int64Val(-1))
	mod.Dict.Set(val.InternString("has_ipv6"), val.BoolVal(true))

	const (
		aiPassive     = 1
		aiCanonname   = 2
		aiNumerichost = 4
		aiV4mapped    = 8
		aiAll         = 16
		aiAddrconfig  = 32
		aiNumericserv = 1024
	)

	// AI / NI flags
	mod.Dict.Set(val.InternString("AI_PASSIVE"), val.Int64Val(aiPassive))
	mod.Dict.Set(val.InternString("AI_CANONNAME"), val.Int64Val(aiCanonname))
	mod.Dict.Set(val.InternString("AI_NUMERICHOST"), val.Int64Val(aiNumerichost))
	mod.Dict.Set(val.InternString("AI_V4MAPPED"), val.Int64Val(aiV4mapped))
	mod.Dict.Set(val.InternString("AI_ALL"), val.Int64Val(aiAll))
	mod.Dict.Set(val.InternString("AI_ADDRCONFIG"), val.Int64Val(aiAddrconfig))
	mod.Dict.Set(val.InternString("AI_NUMERICSERV"), val.Int64Val(aiNumericserv))

	mod.Dict.Set(val.InternString("NI_NUMERICHOST"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("NI_NUMERICSERV"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("NI_NOFQDN"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("NI_NAMEREQD"), val.Int64Val(8))
	mod.Dict.Set(val.InternString("NI_DGRAM"), val.Int64Val(16))

	mod.Dict.Set(val.InternString("EAI_AGAIN"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("EAI_BADFLAGS"), val.Int64Val(3))
	mod.Dict.Set(val.InternString("EAI_FAIL"), val.Int64Val(4))
	mod.Dict.Set(val.InternString("EAI_FAMILY"), val.Int64Val(5))
	mod.Dict.Set(val.InternString("EAI_MEMORY"), val.Int64Val(6))
	mod.Dict.Set(val.InternString("EAI_NODATA"), val.Int64Val(7))
	mod.Dict.Set(val.InternString("EAI_NONAME"), val.Int64Val(8))
	mod.Dict.Set(val.InternString("EAI_SERVICE"), val.Int64Val(9))
	mod.Dict.Set(val.InternString("EAI_SOCKTYPE"), val.Int64Val(10))

	// Exceptions
	mod.Dict.Set(val.InternString("error"), val.TypeVal(val.OSErrorType))
	mod.Dict.Set(val.InternString("herror"), val.TypeVal(val.OSErrorType))
	mod.Dict.Set(val.InternString("gaierror"), val.TypeVal(val.OSErrorType))
	mod.Dict.Set(val.InternString("timeout"), val.TypeVal(val.TimeoutErrorType))

	// Timeout helpers
	mod.Dict.Set(val.InternString("getdefaulttimeout"), val.NewModuleFuncVal("getdefaulttimeout", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		globalTimeoutMu.RLock()
		defer globalTimeoutMu.RUnlock()
		if globalDefaultTimeout == nil {
			return val.NoneVal()
		}
		return val.Float64Val(*globalDefaultTimeout)
	}))

	mod.Dict.Set(val.InternString("setdefaulttimeout"), val.NewModuleFuncVal("setdefaulttimeout", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: setdefaulttimeout() takes 1 argument")
		}
		globalTimeoutMu.Lock()
		defer globalTimeoutMu.Unlock()
		if args[0].IsNone() {
			globalDefaultTimeout = nil
		} else if args[0].IsFloat64() {
			v := args[0].AsFloat64()
			globalDefaultTimeout = &v
		} else if args[0].IsInt64() {
			v := float64(args[0].AsInt64())
			globalDefaultTimeout = &v
		}
		return val.NoneVal()
	}))

	// Hostname and DNS
	mod.Dict.Set(val.InternString("gethostname"), val.NewModuleFuncVal("gethostname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		h, err := os.Hostname()
		if err != nil {
			h = "localhost"
		}
		return val.NewStringVal(h)
	}))

	mod.Dict.Set(val.InternString("gethostbyname"), val.NewModuleFuncVal("gethostbyname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: gethostbyname() takes 1 argument")
		}
		host := args[0].AsString()
		ips, err := net.LookupIP(host)
		if err != nil {
			panic("OSError: [Errno -2] Name or service not known")
		}
		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 != nil {
				return val.NewStringVal(ip4.String())
			}
		}
		panic("OSError: [Errno -2] Name or service not known")
	}))

	mod.Dict.Set(val.InternString("gethostbyaddr"), val.NewModuleFuncVal("gethostbyaddr", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: gethostbyaddr() takes 1 argument")
		}
		ipStr := args[0].AsString()
		names, err := net.LookupAddr(ipStr)
		h := "localhost"
		if err == nil && len(names) > 0 {
			h = strings.TrimSuffix(names[0], ".")
		}
		return val.NewTupleVal(
			val.NewStringVal(h),
			val.NewListVal(),
			val.NewListVal(val.NewStringVal(ipStr)),
		)
	}))

	// Byte order
	mod.Dict.Set(val.InternString("htons"), val.NewModuleFuncVal("htons", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		x := uint16(args[0].AsInt64())
		return val.Int64Val(int64((x >> 8) | (x << 8)))
	}))
	mod.Dict.Set(val.InternString("ntohs"), val.NewModuleFuncVal("ntohs", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		x := uint16(args[0].AsInt64())
		return val.Int64Val(int64((x >> 8) | (x << 8)))
	}))
	mod.Dict.Set(val.InternString("htonl"), val.NewModuleFuncVal("htonl", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		x := uint32(args[0].AsInt64())
		res := ((x & 0xFF) << 24) | ((x & 0xFF00) << 8) | ((x & 0xFF0000) >> 8) | ((x >> 24) & 0xFF)
		return val.Int64Val(int64(res))
	}))
	mod.Dict.Set(val.InternString("ntohl"), val.NewModuleFuncVal("ntohl", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		x := uint32(args[0].AsInt64())
		res := ((x & 0xFF) << 24) | ((x & 0xFF00) << 8) | ((x & 0xFF0000) >> 8) | ((x >> 24) & 0xFF)
		return val.Int64Val(int64(res))
	}))

	// inet_aton, inet_ntoa, inet_pton, inet_ntop
	mod.Dict.Set(val.InternString("inet_aton"), val.NewModuleFuncVal("inet_aton", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: inet_aton() takes 1 argument")
		}
		ipStr := args[0].AsString()
		ip := net.ParseIP(ipStr)
		if ip == nil {
			panic("OSError: illegal IP address string passed to inet_aton")
		}
		ip4 := ip.To4()
		if ip4 == nil {
			panic("OSError: illegal IP address string passed to inet_aton")
		}
		return val.NewBytesVal(ip4)
	}))

	mod.Dict.Set(val.InternString("inet_ntoa"), val.NewModuleFuncVal("inet_ntoa", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsBytes() || len(args[0].AsBytes().Data) != 4 {
			panic("OSError: packed IP wrong length for inet_ntoa")
		}
		b := args[0].AsBytes().Data
		ip := net.IPv4(b[0], b[1], b[2], b[3])
		return val.NewStringVal(ip.String())
	}))

	mod.Dict.Set(val.InternString("inet_pton"), val.NewModuleFuncVal("inet_pton", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: inet_pton() takes 2 arguments")
		}
		af := int(args[0].AsInt64())
		ipStr := args[1].AsString()
		ip := net.ParseIP(ipStr)
		if ip == nil {
			panic("OSError: illegal IP address string passed to inet_pton")
		}
		if af == syscall.AF_INET {
			ip4 := ip.To4()
			if ip4 == nil {
				panic("OSError: illegal IP address string passed to inet_pton")
			}
			return val.NewBytesVal(ip4)
		} else if af == syscall.AF_INET6 {
			ip16 := ip.To16()
			if ip16 == nil {
				panic("OSError: illegal IP address string passed to inet_pton")
			}
			return val.NewBytesVal(ip16)
		}
		panic("OSError: unknown address family")
	}))

	mod.Dict.Set(val.InternString("inet_ntop"), val.NewModuleFuncVal("inet_ntop", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 || !args[1].IsBytes() {
			panic("TypeError: inet_ntop() takes 2 arguments")
		}
		af := int(args[0].AsInt64())
		b := args[1].AsBytes().Data
		if af == syscall.AF_INET {
			if len(b) != 4 {
				panic("ValueError: invalid length of packed IP address string")
			}
			ip := net.IPv4(b[0], b[1], b[2], b[3])
			return val.NewStringVal(ip.String())
		} else if af == syscall.AF_INET6 {
			if len(b) != 16 {
				panic("ValueError: invalid length of packed IP address string")
			}
			ip := net.IP(b)
			return val.NewStringVal(ip.String())
		}
		panic("ValueError: unknown address family")
	}))

	// getaddrinfo
	mod.Dict.Set(val.InternString("getaddrinfo"), val.NewModuleFuncVal("getaddrinfo", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: getaddrinfo() takes at least 2 arguments")
		}
		host := ""
		if !args[0].IsNone() {
			host = args[0].AsString()
		}

		port := 0
		if args[1].IsInt64() {
			port = int(args[1].AsInt64())
		} else if args[1].IsAnyString() {
			pStr := args[1].AsString()
			pInt, err := strconv.Atoi(pStr)
			if err == nil {
				port = pInt
			} else {
				switch pStr {
				case "http":
					port = 80
				case "https":
					port = 443
				case "ftp":
					port = 21
				case "ssh":
					port = 22
				default:
					port = 0
				}
			}
		}

		family := 0
		if len(args) > 2 {
			family = int(args[2].AsInt64())
		}
		sockType := 0
		if len(args) > 3 {
			sockType = int(args[3].AsInt64())
		}
		proto := 0
		if len(args) > 4 {
			proto = int(args[4].AsInt64())
		}
		flags := 0
		if len(args) > 5 {
			flags = int(args[5].AsInt64())
		}

		var results []val.Value

		makeEntry := func(f, t, p int, ipStr string, port int) val.Value {
			var addrTuple val.Value
			if f == syscall.AF_INET6 {
				addrTuple = val.NewTupleVal(
					val.NewStringVal(ipStr),
					val.Int64Val(int64(port)),
					val.Int64Val(0),
					val.Int64Val(0),
				)
			} else {
				addrTuple = val.NewTupleVal(
					val.NewStringVal(ipStr),
					val.Int64Val(int64(port)),
				)
			}
			return val.NewTupleVal(
				val.Int64Val(int64(f)),
				val.Int64Val(int64(t)),
				val.Int64Val(int64(p)),
				val.NewStringVal(""),
				addrTuple,
			)
		}

		types := []int{syscall.SOCK_STREAM}
		if sockType == syscall.SOCK_DGRAM {
			types = []int{syscall.SOCK_DGRAM}
		} else if sockType == 0 {
			types = []int{syscall.SOCK_STREAM, syscall.SOCK_DGRAM}
		}

		if host == "" {
			if flags&aiPassive != 0 {
				if family == 0 || family == syscall.AF_INET {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET, st, proto, "0.0.0.0", port))
					}
				}
				if family == 0 || family == syscall.AF_INET6 {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET6, st, proto, "::", port))
					}
				}
			} else {
				if family == 0 || family == syscall.AF_INET {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET, st, proto, "127.0.0.1", port))
					}
				}
				if family == 0 || family == syscall.AF_INET6 {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET6, st, proto, "::1", port))
					}
				}
			}
			return val.NewListVal(results...)
		}

		// Try literal IP first
		if parsed := net.ParseIP(host); parsed != nil {
			if ip4 := parsed.To4(); ip4 != nil {
				if family == 0 || family == syscall.AF_INET {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET, st, proto, ip4.String(), port))
					}
				}
			} else {
				if family == 0 || family == syscall.AF_INET6 {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET6, st, proto, parsed.String(), port))
					}
				}
			}
			return val.NewListVal(results...)
		}

		// Hostname resolution
		ips, err := net.LookupIP(host)
		if err != nil {
			panic("OSError: [Errno -2] Name or service not known")
		}

		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 != nil {
				if family == 0 || family == syscall.AF_INET {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET, st, proto, ip4.String(), port))
					}
				}
			} else {
				if family == 0 || family == syscall.AF_INET6 {
					for _, st := range types {
						results = append(results, makeEntry(syscall.AF_INET6, st, proto, ip.String(), port))
					}
				}
			}
		}

		return val.NewListVal(results...)
	}))

	toInt64 := func(v val.Value) int64 {
		if v.IsInt64() {
			return v.AsInt64()
		}
		if v.IsBool() {
			if v.AsBool() {
				return 1
			}
			return 0
		}
		if v.IsInstance() {
			inst := v.AsInstance()
			if len(inst.Slots) > 0 && inst.Slots[0].IsInt64() {
				return inst.Slots[0].AsInt64()
			}
			if valVal, ok := inst.GetAttr(val.InternString("_value_")); ok && valVal.IsInt64() {
				return valVal.AsInt64()
			}
		}
		return 0
	}

	// Helper functions to parse and format addresses
	pyToSockaddr := func(family int, addrVal val.Value) syscall.Sockaddr {
		if family == syscall.AF_UNIX {
			return &syscall.SockaddrUnix{Name: addrVal.AsString()}
		}
		if family == syscall.AF_INET {
			if !addrVal.IsTuple() && !addrVal.IsList() {
				panic("TypeError: AF_INET address must be a tuple (host, port)")
			}
			items := addrVal.AsTuple().Items
			if len(items) < 2 {
				panic("TypeError: AF_INET address must be a tuple (host, port)")
			}
			hostStr := items[0].AsString()
			port := int(toInt64(items[1]))
			var ip [4]byte
			if hostStr == "" || hostStr == "0.0.0.0" || hostStr == "INADDR_ANY" {
				ip = [4]byte{0, 0, 0, 0}
			} else {
				p := net.ParseIP(hostStr)
				if p == nil {
					ips, err := net.LookupIP(hostStr)
					if err != nil || len(ips) == 0 {
						panic("OSError: [Errno -2] Name or service not known")
					}
					p = ips[0]
				}
				p4 := p.To4()
				if p4 == nil {
					panic("OSError: [Errno -2] Name or service not known")
				}
				copy(ip[:], p4)
			}
			return &syscall.SockaddrInet4{Port: port, Addr: ip}
		}
		if family == syscall.AF_INET6 {
			if !addrVal.IsTuple() && !addrVal.IsList() {
				panic("TypeError: AF_INET6 address must be a tuple")
			}
			items := addrVal.AsTuple().Items
			hostStr := items[0].AsString()
			port := int(toInt64(items[1]))
			var zone uint32
			if len(items) > 3 {
				zone = uint32(toInt64(items[3]))
			}
			var ip [16]byte
			p := net.ParseIP(hostStr)
			if p != nil {
				copy(ip[:], p.To16())
			}
			return &syscall.SockaddrInet6{Port: port, ZoneId: zone, Addr: ip}
		}
		panic("ValueError: unsupported address family")
	}

	sockaddrToPy := func(sa syscall.Sockaddr) val.Value {
		if sa == nil {
			return val.NoneVal()
		}
		switch a := sa.(type) {
		case *syscall.SockaddrInet4:
			ip := net.IP(a.Addr[:]).String()
			return val.NewTupleVal(val.NewStringVal(ip), val.Int64Val(int64(a.Port)))
		case *syscall.SockaddrInet6:
			ip := net.IP(a.Addr[:]).String()
			return val.NewTupleVal(val.NewStringVal(ip), val.Int64Val(int64(a.Port)), val.Int64Val(0), val.Int64Val(int64(a.ZoneId)))
		case *syscall.SockaddrUnix:
			return val.NewStringVal(a.Name)
		}
		return val.NoneVal()
	}

	// Helper to wait for socket readiness on timeout
	waitReady := func(fd int, forWriting bool, timeout float64) error {
		if timeout <= 0 {
			return nil
		}
		var rSet, wSet syscall.FdSet
		if forWriting {
			wSet.Bits[fd/64] |= 1 << (uint(fd) % 64)
		} else {
			rSet.Bits[fd/64] |= 1 << (uint(fd) % 64)
		}
		dur := time.Duration(timeout * float64(time.Second))
		tv := syscall.NsecToTimeval(dur.Nanoseconds())
		var rPtr, wPtr *syscall.FdSet
		if forWriting {
			wPtr = &wSet
		} else {
			rPtr = &rSet
		}
		n, err := syscall.Select(fd+1, rPtr, wPtr, nil, &tv)
		if err != nil {
			return err
		}
		if n == 0 {
			return syscall.ETIMEDOUT
		}
		return nil
	}

	// Socket class definition
	socketTypeDict := val.NewDictVal(32).AsDict()

	socketType, err := val.NewType("socket", []*val.PyType{val.ObjectType}, socketTypeDict)
	if err != nil {
		panic(err)
	}

	mod.Dict.Set(val.InternString("SocketType"), val.TypeVal(socketType))
	mod.Dict.Set(val.InternString("socket"), val.TypeVal(socketType))

	getSockState := func(self val.Value) *socketState {
		if !self.IsInstance() {
			panic("TypeError: descriptor requires a 'socket' object")
		}
		v, ok := socketInstanceMap.Load(self.AsInstance())
		if !ok {
			panic("OSError: [Errno 9] Bad file descriptor")
		}
		st := v.(*socketState)
		if st.closed {
			panic("OSError: [Errno 9] Bad file descriptor")
		}
		return st
	}

	makeProp := func(name string, getter func(st *socketState) val.Value) {
		socketTypeDict.Set(val.InternString(name), val.NewPropertyVal(
			val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) < 1 {
					panic("TypeError: descriptor '" + name + "' of 'socket' object needs an argument")
				}
				st := getSockState(args[0])
				return getter(st)
			}),
			val.NoneVal(),
			val.NoneVal(),
		))
	}

	makeProp("family", func(st *socketState) val.Value { return val.Int64Val(int64(st.family)) })
	makeProp("type", func(st *socketState) val.Value { return val.Int64Val(int64(st.typ)) })
	makeProp("proto", func(st *socketState) val.Value { return val.Int64Val(int64(st.proto)) })
	makeProp("timeout", func(st *socketState) val.Value {
		if st.timeout < 0 {
			return val.NoneVal()
		}
		return val.Float64Val(st.timeout)
	})

	// socket.__init__(family=AF_INET, type=SOCK_STREAM, proto=0, fileno=None)
	socketTypeDict.Set(val.InternString("__init__"), val.NewBuiltinFuncVal("socket.__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: socket.__init__() takes at least 1 argument")
		}
		self := args[0]

		family := syscall.AF_INET
		if len(args) > 1 && !args[1].IsNone() {
			family = int(toInt64(args[1]))
		}
		typ := syscall.SOCK_STREAM
		if len(args) > 2 && !args[2].IsNone() {
			typ = int(toInt64(args[2]))
		}
		proto := 0
		if len(args) > 3 && !args[3].IsNone() {
			proto = int(toInt64(args[3]))
		}

		var fileno *int
		if len(args) > 4 && !args[4].IsNone() {
			f := int(toInt64(args[4]))
			fileno = &f
		}

		var fd int
		if fileno != nil {
			fd = *fileno
		} else {
			newFd, err := syscall.Socket(family, typ, proto)
			if err != nil {
				raiseOSError(err, "")
			}
			fd = newFd
		}

		var initialTimeout float64 = -1.0
		globalTimeoutMu.RLock()
		if globalDefaultTimeout != nil {
			initialTimeout = *globalDefaultTimeout
		}
		globalTimeoutMu.RUnlock()

		if initialTimeout == 0 {
			_ = syscall.SetNonblock(fd, true)
		} else if initialTimeout > 0 {
			_ = syscall.SetNonblock(fd, true)
		} else {
			_ = syscall.SetNonblock(fd, false)
		}

		st := &socketState{
			fd:      fd,
			family:  family,
			typ:     typ,
			proto:   proto,
			timeout: initialTimeout,
			closed:  false,
		}
		socketRegistry.Store(fd, st)
		socketInstanceMap.Store(self.AsInstance(), st)

		return val.NoneVal()
	}))

	// fileno
	socketTypeDict.Set(val.InternString("fileno"), val.NewBuiltinFuncVal("fileno", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		return val.Int64Val(int64(st.fd))
	}))

	// bind
	socketTypeDict.Set(val.InternString("bind"), val.NewBuiltinFuncVal("bind", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: bind() takes 1 argument")
		}
		st := getSockState(args[0])
		sa := pyToSockaddr(st.family, args[1])
		err := syscall.Bind(st.fd, sa)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NoneVal()
	}))

	// listen
	socketTypeDict.Set(val.InternString("listen"), val.NewBuiltinFuncVal("listen", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		backlog := 128
		if len(args) > 1 && !args[1].IsNone() {
			backlog = int(toInt64(args[1]))
		}
		err := syscall.Listen(st.fd, backlog)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NoneVal()
	}))

	// _accept -> (client_fd, addr)
	socketTypeDict.Set(val.InternString("_accept"), val.NewBuiltinFuncVal("_accept", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		for {
			nfd, sa, err := syscall.Accept(st.fd)
			if err == nil {
				return val.NewTupleVal(val.Int64Val(int64(nfd)), sockaddrToPy(sa))
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				if st.timeout == 0 {
					panic("BlockingIOError: [Errno 11] Resource temporarily unavailable")
				} else if st.timeout > 0 {
					waitErr := waitReady(st.fd, false, st.timeout)
					if waitErr == syscall.ETIMEDOUT {
						panic("TimeoutError: timed out")
					}
					if waitErr != nil {
						raiseOSError(waitErr, "")
					}
					continue
				}
			}
			raiseOSError(err, "")
		}
	}))

	// connect
	socketTypeDict.Set(val.InternString("connect"), val.NewBuiltinFuncVal("connect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: connect() takes 1 argument")
		}
		st := getSockState(args[0])
		sa := pyToSockaddr(st.family, args[1])

		err := syscall.Connect(st.fd, sa)
		if err == nil {
			return val.NoneVal()
		}
		if err == syscall.EINPROGRESS {
			if st.timeout == 0 {
				panic("BlockingIOError: [Errno 115] Operation now in progress")
			}
			timeoutVal := st.timeout
			if timeoutVal < 0 {
				timeoutVal = 60.0
			}
			waitErr := waitReady(st.fd, true, timeoutVal)
			if waitErr == syscall.ETIMEDOUT {
				panic("TimeoutError: timed out")
			}
			if waitErr != nil {
				raiseOSError(waitErr, "")
			}
			// Check socket error
			soErr, getsockErr := syscall.GetsockoptInt(st.fd, syscall.SOL_SOCKET, syscall.SO_ERROR)
			if getsockErr != nil {
				raiseOSError(getsockErr, "")
			}
			if soErr != 0 {
				raiseOSError(syscall.Errno(soErr), "")
			}
			return val.NoneVal()
		}
		raiseOSError(err, "")
		return val.NoneVal()
	}))

	// connect_ex
	socketTypeDict.Set(val.InternString("connect_ex"), val.NewBuiltinFuncVal("connect_ex", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: connect_ex() takes 1 argument")
		}
		st := getSockState(args[0])
		sa := pyToSockaddr(st.family, args[1])

		err := syscall.Connect(st.fd, sa)
		if err == nil {
			return val.Int64Val(0)
		}
		if err == syscall.EINPROGRESS {
			timeoutVal := st.timeout
			if timeoutVal <= 0 {
				timeoutVal = 10.0
			}
			waitErr := waitReady(st.fd, true, timeoutVal)
			if waitErr != nil {
				return val.Int64Val(110) // ETIMEDOUT
			}
			soErr, _ := syscall.GetsockoptInt(st.fd, syscall.SOL_SOCKET, syscall.SO_ERROR)
			return val.Int64Val(int64(soErr))
		}
		if errno, ok := err.(syscall.Errno); ok {
			return val.Int64Val(int64(errno))
		}
		return val.Int64Val(1)
	}))

	// send
	socketTypeDict.Set(val.InternString("send"), val.NewBuiltinFuncVal("send", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: send() takes at least 1 argument")
		}
		st := getSockState(args[0])
		var data []byte
		if args[1].IsBytes() {
			data = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			data = args[1].AsByteArray().Data
		} else if args[1].IsMemoryView() {
			data = args[1].AsMemoryView().Buf
		} else {
			data = []byte(args[1].AsString())
		}

		for {
			n, err := syscall.Write(st.fd, data)
			if err == nil {
				return val.Int64Val(int64(n))
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				if st.timeout == 0 {
					panic("BlockingIOError: [Errno 11] Resource temporarily unavailable")
				} else if st.timeout > 0 {
					waitErr := waitReady(st.fd, true, st.timeout)
					if waitErr == syscall.ETIMEDOUT {
						panic("TimeoutError: timed out")
					}
					if waitErr != nil {
						raiseOSError(waitErr, "")
					}
					continue
				}
			}
			raiseOSError(err, "")
		}
	}))

	// sendall
	socketTypeDict.Set(val.InternString("sendall"), val.NewBuiltinFuncVal("sendall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: sendall() takes at least 1 argument")
		}
		st := getSockState(args[0])
		var data []byte
		if args[1].IsBytes() {
			data = args[1].AsBytes().Data
		} else if args[1].IsByteArray() {
			data = args[1].AsByteArray().Data
		} else if args[1].IsMemoryView() {
			data = args[1].AsMemoryView().Buf
		} else {
			data = []byte(args[1].AsString())
		}

		for len(data) > 0 {
			n, err := syscall.Write(st.fd, data)
			if err == nil {
				data = data[n:]
				continue
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				if st.timeout == 0 {
					panic("BlockingIOError: [Errno 11] Resource temporarily unavailable")
				} else if st.timeout > 0 {
					waitErr := waitReady(st.fd, true, st.timeout)
					if waitErr == syscall.ETIMEDOUT {
						panic("TimeoutError: timed out")
					}
					if waitErr != nil {
						raiseOSError(waitErr, "")
					}
					continue
				}
			}
			raiseOSError(err, "")
		}
		return val.NoneVal()
	}))

	// recv
	socketTypeDict.Set(val.InternString("recv"), val.NewBuiltinFuncVal("recv", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: recv() takes at least 1 argument")
		}
		st := getSockState(args[0])
		bufSize := int(toInt64(args[1]))
		if bufSize < 0 {
			panic("ValueError: negative buffersize in recv")
		}
		buf := make([]byte, bufSize)

		for {
			n, err := syscall.Read(st.fd, buf)
			if err == nil {
				return val.NewBytesVal(buf[:n])
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				if st.timeout == 0 {
					panic("BlockingIOError: [Errno 11] Resource temporarily unavailable")
				} else if st.timeout > 0 {
					waitErr := waitReady(st.fd, false, st.timeout)
					if waitErr == syscall.ETIMEDOUT {
						panic("TimeoutError: timed out")
					}
					if waitErr != nil {
						raiseOSError(waitErr, "")
					}
					continue
				}
			}
			raiseOSError(err, "")
		}
	}))

	// recv_into(buffer, [nbytes, [flags]])
	socketTypeDict.Set(val.InternString("recv_into"), val.NewBuiltinFuncVal("recv_into", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: recv_into() takes at least 1 argument")
		}
		st := getSockState(args[0])
		var target []byte
		if args[1].IsByteArray() {
			target = args[1].AsByteArray().Data
		} else if args[1].IsMemoryView() {
			target = args[1].AsMemoryView().Buf
		} else {
			panic("TypeError: a bytes-like object is required")
		}

		if len(args) > 2 && !args[2].IsNone() {
			nbytes := int(toInt64(args[2]))
			if nbytes < len(target) {
				target = target[:nbytes]
			}
		}

		for {
			n, err := syscall.Read(st.fd, target)
			if err == nil {
				return val.Int64Val(int64(n))
			}
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				if st.timeout == 0 {
					panic("BlockingIOError: [Errno 11] Resource temporarily unavailable")
				} else if st.timeout > 0 {
					waitErr := waitReady(st.fd, false, st.timeout)
					if waitErr == syscall.ETIMEDOUT {
						panic("TimeoutError: timed out")
					}
					if waitErr != nil {
						raiseOSError(waitErr, "")
					}
					continue
				}
			}
			raiseOSError(err, "")
		}
	}))

	// sendto(data, [flags,] address)
	socketTypeDict.Set(val.InternString("sendto"), val.NewBuiltinFuncVal("sendto", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: sendto() takes at least 2 arguments")
		}
		st := getSockState(args[0])
		var data []byte
		if args[1].IsBytes() {
			data = args[1].AsBytes().Data
		} else {
			data = []byte(args[1].AsString())
		}
		addrArg := args[2]
		if len(args) > 3 {
			addrArg = args[3]
		}
		sa := pyToSockaddr(st.family, addrArg)
		err := syscall.Sendto(st.fd, data, 0, sa)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(len(data)))
	}))

	// recvfrom(bufsize, [flags])
	socketTypeDict.Set(val.InternString("recvfrom"), val.NewBuiltinFuncVal("recvfrom", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: recvfrom() takes at least 1 argument")
		}
		st := getSockState(args[0])
		bufSize := int(toInt64(args[1]))
		buf := make([]byte, bufSize)
		n, sa, err := syscall.Recvfrom(st.fd, buf, 0)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NewTupleVal(val.NewBytesVal(buf[:n]), sockaddrToPy(sa))
	}))

	// settimeout
	socketTypeDict.Set(val.InternString("settimeout"), val.NewBuiltinFuncVal("settimeout", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		if len(args) < 2 || args[1].IsNone() {
			st.timeout = -1.0
			_ = syscall.SetNonblock(st.fd, false)
		} else {
			var t float64
			if args[1].IsFloat64() {
				t = args[1].AsFloat64()
			} else if args[1].IsInt64() {
				t = float64(args[1].AsInt64())
			}
			st.timeout = t
			_ = syscall.SetNonblock(st.fd, true)
		}
		return val.NoneVal()
	}))

	// gettimeout
	socketTypeDict.Set(val.InternString("gettimeout"), val.NewBuiltinFuncVal("gettimeout", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		if st.timeout < 0 {
			return val.NoneVal()
		}
		return val.Float64Val(st.timeout)
	}))

	// setblocking
	socketTypeDict.Set(val.InternString("setblocking"), val.NewBuiltinFuncVal("setblocking", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: setblocking() takes 1 argument")
		}
		st := getSockState(args[0])
		if args[1].IsTruthy() {
			st.timeout = -1.0
			_ = syscall.SetNonblock(st.fd, false)
		} else {
			st.timeout = 0.0
			_ = syscall.SetNonblock(st.fd, true)
		}
		return val.NoneVal()
	}))

	// getblocking
	socketTypeDict.Set(val.InternString("getblocking"), val.NewBuiltinFuncVal("getblocking", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		return val.BoolVal(st.timeout != 0.0)
	}))

	// setsockopt
	socketTypeDict.Set(val.InternString("setsockopt"), val.NewBuiltinFuncVal("setsockopt", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 4 {
			panic("TypeError: setsockopt() takes 3 arguments")
		}
		st := getSockState(args[0])
		level := int(toInt64(args[1]))
		opt := int(toInt64(args[2]))
		if args[3].IsInt64() || args[3].IsBool() {
			val := int(toInt64(args[3]))
			err := syscall.SetsockoptInt(st.fd, level, opt, val)
			if err != nil {
				raiseOSError(err, "")
			}
		} else if args[3].IsBytes() {
			b := args[3].AsBytes().Data
			err := syscall.SetsockoptString(st.fd, level, opt, string(b))
			if err != nil {
				raiseOSError(err, "")
			}
		}
		return val.NoneVal()
	}))

	// getsockopt
	socketTypeDict.Set(val.InternString("getsockopt"), val.NewBuiltinFuncVal("getsockopt", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: getsockopt() takes at least 2 arguments")
		}
		st := getSockState(args[0])
		level := int(toInt64(args[1]))
		opt := int(toInt64(args[2]))
		v, err := syscall.GetsockoptInt(st.fd, level, opt)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(v))
	}))

	// getsockname
	socketTypeDict.Set(val.InternString("getsockname"), val.NewBuiltinFuncVal("getsockname", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		sa, err := syscall.Getsockname(st.fd)
		if err != nil {
			raiseOSError(err, "")
		}
		return sockaddrToPy(sa)
	}))

	// getpeername
	socketTypeDict.Set(val.InternString("getpeername"), val.NewBuiltinFuncVal("getpeername", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		sa, err := syscall.Getpeername(st.fd)
		if err != nil {
			raiseOSError(err, "")
		}
		return sockaddrToPy(sa)
	}))

	// shutdown
	socketTypeDict.Set(val.InternString("shutdown"), val.NewBuiltinFuncVal("shutdown", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: shutdown() takes 1 argument")
		}
		st := getSockState(args[0])
		how := int(toInt64(args[1]))
		err := syscall.Shutdown(st.fd, how)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.NoneVal()
	}))

	// close
	socketTypeDict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if args[0].IsInstance() {
			if v, ok := socketInstanceMap.Load(args[0].AsInstance()); ok {
				st := v.(*socketState)
				if !st.closed {
					st.closed = true
					_ = syscall.Close(st.fd)
					socketRegistry.Delete(st.fd)
				}
				socketInstanceMap.Delete(args[0].AsInstance())
			}
		}
		return val.NoneVal()
	}))

	// detach
	socketTypeDict.Set(val.InternString("detach"), val.NewBuiltinFuncVal("detach", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		st := getSockState(args[0])
		fd := st.fd
		st.closed = true
		socketRegistry.Delete(fd)
		socketInstanceMap.Delete(args[0].AsInstance())
		return val.Int64Val(int64(fd))
	}))

	// socketpair
	mod.Dict.Set(val.InternString("socketpair"), val.NewModuleFuncVal("socketpair", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		family := syscall.AF_UNIX
		typ := syscall.SOCK_STREAM
		proto := 0
		if len(args) > 0 {
			family = int(toInt64(args[0]))
		}
		if len(args) > 1 {
			typ = int(toInt64(args[1]))
		}
		if len(args) > 2 {
			proto = int(toInt64(args[2]))
		}
		fds, err := syscall.Socketpair(family, typ, proto)
		if err != nil {
			raiseOSError(err, "")
		}

		makeSock := func(fd int) val.Value {
			inst := val.NewInstanceOfType(socketType)
			st := &socketState{
				fd:      fd,
				family:  family,
				typ:     typ,
				proto:   proto,
				timeout: -1.0,
			}
			socketRegistry.Store(fd, st)
			socketInstanceMap.Store(inst.AsInstance(), st)
			return inst
		}

		return val.NewTupleVal(makeSock(fds[0]), makeSock(fds[1]))
	}))

	// dup
	mod.Dict.Set(val.InternString("dup"), val.NewModuleFuncVal("dup", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: dup() takes 1 argument")
		}
		fd := int(toInt64(args[0]))
		nfd, err := syscall.Dup(fd)
		if err != nil {
			raiseOSError(err, "")
		}
		return val.Int64Val(int64(nfd))
	}))

	_ = binary.BigEndian
	return modVal
}
