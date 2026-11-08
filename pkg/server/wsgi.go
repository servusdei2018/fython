package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
	"github.com/servusdei2018/fython/pkg/vm"
)

func (s *Server) handleWSGI(w http.ResponseWriter, r *http.Request, v *vm.VM) {
	// Construct PEP 3333 environ dictionary with pre-interned keys
	environ := val.NewDictVal(32).AsDict()

	environ.Set(keys.keyRequestMethod, val.InternString(r.Method))
	environ.Set(keys.keyScriptName, keys.valEmptyStr)
	environ.Set(keys.keyPathInfo, val.NewStringVal(r.URL.Path))
	environ.Set(keys.keyQueryStringWsgi, val.NewStringVal(r.URL.RawQuery))

	serverHost, serverPort := splitHostPort(r.Host)
	environ.Set(keys.keyServerName, val.NewStringVal(serverHost))
	environ.Set(keys.keyServerPort, val.NewStringVal(strconv.FormatInt(serverPort, 10)))
	environ.Set(keys.keyServerProtocol, val.InternString(r.Proto))

	if ct := r.Header.Get("Content-Type"); ct != "" {
		environ.Set(keys.keyContentType, val.NewStringVal(ct))
	}
	if r.ContentLength >= 0 {
		environ.Set(keys.keyContentLength, val.NewStringVal(strconv.FormatInt(r.ContentLength, 10)))
	}

	environ.Set(keys.keyWsgiVersion, keys.valWsgiVer)
	environ.Set(keys.keyWsgiUrlScheme, keys.valHttp)
	environ.Set(keys.keyWsgiMultithread, val.BoolVal(true))
	environ.Set(keys.keyWsgiMultiprocess, val.BoolVal(false))
	environ.Set(keys.keyWsgiRunOnce, val.BoolVal(false))

	// Provide wsgi.errors (sys.stderr)
	if sysModVal, ok := v.GetModule("sys"); ok && sysModVal.IsModule() {
		if stderrVal, ok := sysModVal.AsModule().Dict.Get(val.InternString("stderr")); ok {
			environ.Set(keys.keyWsgiErrors, stderrVal)
		}
	}

	// Prepare wsgi.input
	bodyBytes, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	bytesIOVal := v.Vectorcall(val.TypeVal(vm.BytesIOType), []val.Value{val.NewBytesVal(bodyBytes)}, nil)
	environ.Set(keys.keyWsgiInput, bytesIOVal)

	// Populate HTTP headers
	for k, vv := range r.Header {
		upperK := strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
		if upperK == "CONTENT_TYPE" || upperK == "CONTENT_LENGTH" {
			continue
		}
		environ.Set(val.InternString("HTTP_"+upperK), val.NewStringVal(strings.Join(vv, ",")))
	}

	statusCode := http.StatusOK
	headersSent := false

	// PEP 3333 start_response(status, response_headers, exc_info=None)
	startResponseCallable := val.NewBuiltinFuncVal("start_response", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			return val.NoneVal()
		}
		statusStr := args[0].AsString()
		if parts := strings.SplitN(statusStr, " ", 2); len(parts) > 0 {
			if code, err := strconv.Atoi(parts[0]); err == nil {
				statusCode = code
			}
		}

		if args[1].IsList() {
			for _, it := range args[1].AsList().Items {
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

		// Return legacy write(data) callable
		return val.NewBuiltinFuncVal("write", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 0 {
				if !headersSent {
					w.WriteHeader(statusCode)
					headersSent = true
				}
				raw := valueToBytes(args[0])
				if len(raw) > 0 {
					_, _ = w.Write(raw)
				}
			}
			return val.NoneVal()
		})
	})

	// Invoke app(environ, start_response)
	respVal := v.Vectorcall(s.app, []val.Value{val.DictVal(environ), startResponseCallable}, nil)

	exc := v.UnhandledException()
	if !exc.IsNone() && exc.HeaderPtr() != nil {
		fmt.Fprintf(os.Stderr, "WSGI application error:\n%s\n", v.FormatException(exc))
		v.ClearUnhandledException()
		if !headersSent {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
		return
	}

	if !headersSent {
		w.WriteHeader(statusCode)
		headersSent = true
	}

	// Stream chunks from respVal
	if respVal.IsList() {
		for _, item := range respVal.AsList().Items {
			raw := valueToBytes(item)
			if len(raw) > 0 {
				_, _ = w.Write(raw)
			}
		}
	} else if respVal.IsTuple() {
		for _, item := range respVal.AsTuple().Items {
			raw := valueToBytes(item)
			if len(raw) > 0 {
				_, _ = w.Write(raw)
			}
		}
	} else if respVal.IsBytes() || respVal.IsAnyString() {
		raw := valueToBytes(respVal)
		if len(raw) > 0 {
			_, _ = w.Write(raw)
		}
	} else if respVal.IsInstance() || respVal.IsGenerator() {
		iterFn := val.GetAttrCtx(v.ExecContext(), respVal, val.InternString("__iter__"))
		var iterVal val.Value = respVal
		if !iterFn.IsUnbound() && !iterFn.IsNone() {
			iterVal = v.Vectorcall(iterFn, nil, nil)
		}
		nextFn := val.GetAttrCtx(v.ExecContext(), iterVal, val.InternString("__next__"))
		if !nextFn.IsUnbound() && !nextFn.IsNone() {
			for {
				chunk := v.Vectorcall(nextFn, nil, nil)
				if v.UnhandledException().IsInstance() {
					v.ClearUnhandledException()
					break
				}
				if chunk.IsNone() && !chunk.IsBytes() {
					break
				}
				raw := valueToBytes(chunk)
				if len(raw) > 0 {
					_, _ = w.Write(raw)
				}
			}
		}
	}

	// Call close() if available
	if respVal.IsInstance() {
		if closeMethod, ok := respVal.AsInstance().GetAttr(val.InternString("close")); ok && !closeMethod.IsNone() {
			v.Vectorcall(closeMethod, nil, nil)
			v.ClearUnhandledException()
		}
	}
}
