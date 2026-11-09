package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

var (
	completedNoneFuture     val.Value
	completedEmptyReqFuture val.Value
)

func (s *Server) handleASGI(w http.ResponseWriter, r *http.Request, v *vm.VM) {
	// Construct ASGI 3.0 scope dictionary with pre-interned keys
	scope := val.NewDictVal(16).AsDict()
	scope.Set(keys.keyType, keys.keyHttp)
	scope.Set(keys.keyAsgi, keys.valAsgiSpec)

	if r.ProtoMajor == 2 {
		scope.Set(keys.keyHttpVersion, keys.valHttp20)
	} else {
		scope.Set(keys.keyHttpVersion, keys.valHttp11)
	}

	scope.Set(keys.keyMethod, val.InternString(r.Method))
	scope.Set(keys.keyScheme, keys.valHttp)
	scope.Set(keys.keyPath, val.NewStringVal(r.URL.Path))

	rawPath := r.URL.RawPath
	if rawPath == "" {
		rawPath = r.URL.Path
	}
	scope.Set(keys.keyRawPath, val.NewBytesVal([]byte(rawPath)))
	scope.Set(keys.keyQueryString, val.NewBytesVal([]byte(r.URL.RawQuery)))
	scope.Set(keys.keyRootPath, keys.valEmptyStr)
	scope.Set(keys.keyState, val.DictVal(val.NewDictVal(0).AsDict()))

	// Zero-copy header tuples: list of (bytes, bytes)
	var headerTuples []val.Value
	for k, vv := range r.Header {
		lowerK := strings.ToLower(k)
		kBytes := val.NewBytesVal([]byte(lowerK))
		for _, vItem := range vv {
			headerTuples = append(headerTuples, val.NewTupleVal(kBytes, val.NewBytesVal([]byte(vItem))))
		}
	}
	scope.Set(keys.keyHeaders, val.NewListVal(headerTuples...))

	clientHost, clientPort := splitHostPort(r.RemoteAddr)
	serverHost, serverPort := splitHostPort(r.Host)
	scope.Set(keys.keyClient, val.NewTupleVal(val.NewStringVal(clientHost), val.Int64Val(clientPort)))
	scope.Set(keys.keyServer, val.NewTupleVal(val.NewStringVal(serverHost), val.Int64Val(serverPort)))

	// Prepare request body
	bodyBytes, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	bodyRead := false

	// Native Go async receive() callable
	receiveCallable := val.NewBuiltinFuncVal("receive", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if bodyRead || len(bodyBytes) == 0 {
			return completedEmptyReqFuture
		}
		bodyRead = true
		futVal := val.NewFutureVal()
		fut := futVal.AsFuture()

		eventDict := val.NewDictVal(4).AsDict()
		eventDict.Set(keys.keyType, keys.keyHttpRequest)
		eventDict.Set(keys.keyBody, val.NewBytesVal(bodyBytes))
		eventDict.Set(keys.keyMoreBody, val.BoolVal(false))
		fut.SetResult(val.DictVal(eventDict))
		return futVal
	})

	statusCode := http.StatusOK
	headersSent := false
	flusher, _ := w.(http.Flusher)

	// Native Go async send(message) callable
	sendCallable := val.NewBuiltinFuncVal("send", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 || !args[0].IsDict() {
			return completedNoneFuture
		}
		msg := args[0].AsDict()
		typeVal, ok := msg.Get(keys.keyType)
		if !ok || !typeVal.IsAnyString() {
			return completedNoneFuture
		}
		typeStr := typeVal.AsString()

		switch typeStr {
		case "http.response.start":
			if stVal, ok := msg.Get(keys.keyStatus); ok && stVal.IsInt64() {
				statusCode = int(stVal.AsInt64())
			}
			if hdrsVal, ok := msg.Get(keys.keyHeaders); ok && hdrsVal.IsList() {
				for _, it := range hdrsVal.AsList().Items {
					if it.IsTuple() {
						tup := it.AsTuple()
						if len(tup.Items) >= 2 {
							k := valueToString(tup.Items[0])
							valStr := valueToString(tup.Items[1])
							w.Header().Add(k, valStr)
						}
					}
				}
			}
		case "http.response.body":
			if !headersSent {
				w.WriteHeader(statusCode)
				headersSent = true
			}
			if bodyVal, ok := msg.Get(keys.keyBody); ok {
				raw := valueToBytes(bodyVal)
				if len(raw) > 0 {
					_, _ = w.Write(raw)
				}
			}
			moreBody := false
			if mbVal, ok := msg.Get(keys.keyMoreBody); ok {
				moreBody = mbVal.IsTruthy()
			}
			if moreBody && flusher != nil {
				flusher.Flush()
			}
		}

		return completedNoneFuture
	})

	// Invoke app(scope, receive, send)
	args := []val.Value{val.DictVal(scope), receiveCallable, sendCallable}
	coroVal := v.Vectorcall(s.app, args, nil)

	if coroVal.IsCoroutine() {
		coro := coroVal.AsCoroutine()
		_, status := v.StepCoroutine(coro, val.NoneVal())
		switch status {
		case val.IterYielded:
			// If suspended by a non-immediate future or timer, advance on EventLoop
			loop := vm.NewEventLoop(s.engine, vm.AsyncModeSingleThread)
			_, err := loop.RunUntilComplete(coroVal)
			if err != nil {
				if !headersSent {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				return
			}
		case val.IterError:
			exc := v.UnhandledException()
			if !exc.IsNone() && exc.HeaderPtr() != nil {
				fmt.Fprintf(os.Stderr, "ASGI application error:\n%s\n", v.FormatException(exc))
				v.ClearUnhandledException()
			}
			v.ReleaseCoroutine(coro)
			if !headersSent {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
			return
		}
		v.ReleaseCoroutine(coro)
	}

	exc := v.UnhandledException()
	if !exc.IsNone() && exc.HeaderPtr() != nil {
		fmt.Fprintf(os.Stderr, "ASGI application error:\n%s\n", v.FormatException(exc))
		v.ClearUnhandledException()
		if !headersSent {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	if !headersSent {
		w.WriteHeader(statusCode)
	}
}
