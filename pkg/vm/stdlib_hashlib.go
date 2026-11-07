package vm

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	hashlibOnce                   sync.Once
	hashType                      *val.PyType
	hmacType                      *val.PyType
	unsupportedDigestmodErrorType *val.PyType
)

type hashConstructor func() hash.Hash

type hashAlgorithmInfo struct {
	name        string
	constructor hashConstructor
	digestSize  int
	blockSize   int
}

var supportedHashAlgorithms = map[string]hashAlgorithmInfo{
	"md5": {
		name:        "md5",
		constructor: md5.New,
		digestSize:  16,
		blockSize:   64,
	},
	"sha1": {
		name:        "sha1",
		constructor: sha1.New,
		digestSize:  20,
		blockSize:   64,
	},
	"sha224": {
		name:        "sha224",
		constructor: sha256.New224,
		digestSize:  28,
		blockSize:   64,
	},
	"sha256": {
		name:        "sha256",
		constructor: sha256.New,
		digestSize:  32,
		blockSize:   64,
	},
	"sha384": {
		name:        "sha384",
		constructor: sha512.New384,
		digestSize:  48,
		blockSize:   128,
	},
	"sha512": {
		name:        "sha512",
		constructor: sha512.New,
		digestSize:  64,
		blockSize:   128,
	},
}

func normalizeHashName(name string) string {
	lower := strings.ToLower(name)
	lower = strings.ReplaceAll(lower, "-", "")
	lower = strings.ReplaceAll(lower, "_", "")
	switch lower {
	case "md5":
		return "md5"
	case "sha1":
		return "sha1"
	case "sha224":
		return "sha224"
	case "sha256":
		return "sha256"
	case "sha384":
		return "sha384"
	case "sha512":
		return "sha512"
	}
	return lower
}

type pyHash struct {
	inst   val.PyInstance
	info   hashAlgorithmInfo
	h      hash.Hash
	chunks [][]byte
}

func newHashVal(info hashAlgorithmInfo, initialData []byte) val.Value {
	initHashlibTypes()
	ph := &pyHash{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: hashType},
			Shape:  hashType.RootShape,
		},
		info: info,
		h:    info.constructor(),
	}
	if len(initialData) > 0 {
		dataCopy := make([]byte, len(initialData))
		copy(dataCopy, initialData)
		ph.h.Write(dataCopy)
		ph.chunks = [][]byte{dataCopy}
	}
	return val.ObjectVal(&ph.inst.Header)
}

type pyHmac struct {
	inst   val.PyInstance
	info   hashAlgorithmInfo
	key    []byte
	h      hash.Hash
	chunks [][]byte
}

func newHmacVal(info hashAlgorithmInfo, key, initialData []byte) val.Value {
	initHashlibTypes()
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	ph := &pyHmac{
		inst: val.PyInstance{
			Header: val.ObjectHeader{Kind: val.KindInstance, Type: hmacType},
			Shape:  hmacType.RootShape,
		},
		info: info,
		key:  keyCopy,
		h:    hmac.New(info.constructor, keyCopy),
	}
	if len(initialData) > 0 {
		dataCopy := make([]byte, len(initialData))
		copy(dataCopy, initialData)
		ph.h.Write(dataCopy)
		ph.chunks = [][]byte{dataCopy}
	}
	return val.ObjectVal(&ph.inst.Header)
}

func initHashlibTypes() {
	hashlibOnce.Do(func() {
		unsupportedDigestmodErrorType = val.MustNewType("UnsupportedDigestmodError", []*val.PyType{val.ValueErrorType}, nil)
		unsupportedDigestmodErrorType.Dict.Set(val.InternString("__module__"), val.InternString("_hashlib"))

		hashType = val.MustNewType("HASH", []*val.PyType{val.ObjectType}, nil)
		hashType.Dict.Set(val.InternString("__module__"), val.InternString("_hashlib"))

		hashType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			ph := (*pyHash)(val.AsPointer(obj))
			switch name.AsString() {
			case "name":
				return val.InternString(ph.info.name)
			case "digest_size":
				return val.Int64Val(int64(ph.info.digestSize))
			case "block_size":
				return val.Int64Val(int64(ph.info.blockSize))
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		hashType.Dict.Set(val.InternString("update"), val.NewBuiltinFuncVal("update", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: update() takes 1 positional argument")
			}
			ph := (*pyHash)(val.AsPointer(args[0]))
			data, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: a bytes-like object is required")
			}
			dataCopy := make([]byte, len(data))
			copy(dataCopy, data)
			ph.h.Write(dataCopy)
			ph.chunks = append(ph.chunks, dataCopy)
			return val.NoneVal()
		}))

		hashType.Dict.Set(val.InternString("digest"), val.NewBuiltinFuncVal("digest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: digest() takes no arguments")
			}
			ph := (*pyHash)(val.AsPointer(args[0]))
			res := ph.h.Sum(nil)
			return val.NewBytesVal(res)
		}))

		hashType.Dict.Set(val.InternString("hexdigest"), val.NewBuiltinFuncVal("hexdigest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: hexdigest() takes no arguments")
			}
			ph := (*pyHash)(val.AsPointer(args[0]))
			res := ph.h.Sum(nil)
			return val.NewStringVal(hex.EncodeToString(res))
		}))

		hashType.Dict.Set(val.InternString("copy"), val.NewBuiltinFuncVal("copy", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: copy() takes no arguments")
			}
			ph := (*pyHash)(val.AsPointer(args[0]))
			clone := newHashVal(ph.info, nil)
			clonePh := (*pyHash)(val.AsPointer(clone))
			for _, chunk := range ph.chunks {
				chunkCopy := make([]byte, len(chunk))
				copy(chunkCopy, chunk)
				clonePh.h.Write(chunkCopy)
				clonePh.chunks = append(clonePh.chunks, chunkCopy)
			}
			return clone
		}))

		// HMAC Type
		hmacType = val.MustNewType("HMAC", []*val.PyType{val.ObjectType}, nil)
		hmacType.Dict.Set(val.InternString("__module__"), val.InternString("_hashlib"))

		hmacType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			ph := (*pyHmac)(val.AsPointer(obj))
			switch name.AsString() {
			case "name":
				return val.InternString(fmt.Sprintf("hmac-%s", ph.info.name))
			case "digest_size":
				return val.Int64Val(int64(ph.info.digestSize))
			case "block_size":
				return val.Int64Val(int64(ph.info.blockSize))
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		hmacType.Dict.Set(val.InternString("update"), val.NewBuiltinFuncVal("update", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				panic("TypeError: update() takes 1 positional argument")
			}
			ph := (*pyHmac)(val.AsPointer(args[0]))
			data, ok := getBytesFromValue(args[1])
			if !ok {
				panic("TypeError: a bytes-like object is required")
			}
			dataCopy := make([]byte, len(data))
			copy(dataCopy, data)
			ph.h.Write(dataCopy)
			ph.chunks = append(ph.chunks, dataCopy)
			return val.NoneVal()
		}))

		hmacType.Dict.Set(val.InternString("digest"), val.NewBuiltinFuncVal("digest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: digest() takes no arguments")
			}
			ph := (*pyHmac)(val.AsPointer(args[0]))
			res := ph.h.Sum(nil)
			return val.NewBytesVal(res)
		}))

		hmacType.Dict.Set(val.InternString("hexdigest"), val.NewBuiltinFuncVal("hexdigest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: hexdigest() takes no arguments")
			}
			ph := (*pyHmac)(val.AsPointer(args[0]))
			res := ph.h.Sum(nil)
			return val.NewStringVal(hex.EncodeToString(res))
		}))

		hmacType.Dict.Set(val.InternString("copy"), val.NewBuiltinFuncVal("copy", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				panic("TypeError: copy() takes no arguments")
			}
			ph := (*pyHmac)(val.AsPointer(args[0]))
			clone := newHmacVal(ph.info, ph.key, nil)
			clonePh := (*pyHmac)(val.AsPointer(clone))
			for _, chunk := range ph.chunks {
				chunkCopy := make([]byte, len(chunk))
				copy(chunkCopy, chunk)
				clonePh.h.Write(chunkCopy)
				clonePh.chunks = append(clonePh.chunks, chunkCopy)
			}
			return clone
		}))
	})
}

func (e *Engine) createHashlibModule() val.Value {
	initHashlibTypes()
	modVal := val.NewModuleVal("_hashlib", "Accelerated hashing module")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("HASH"), val.TypeVal(hashType))
	mod.Dict.Set(val.InternString("HMAC"), val.TypeVal(hmacType))
	mod.Dict.Set(val.InternString("UnsupportedDigestmodError"), val.TypeVal(unsupportedDigestmodErrorType))

	methNames := []val.Value{
		val.InternString("md5"), val.InternString("MD5"),
		val.InternString("sha1"), val.InternString("SHA1"),
		val.InternString("sha224"), val.InternString("SHA224"),
		val.InternString("sha256"), val.InternString("SHA256"),
		val.InternString("sha384"), val.InternString("SHA384"),
		val.InternString("sha512"), val.InternString("SHA512"),
	}
	mod.Dict.Set(val.InternString("openssl_md_meth_names"), val.NewSetVal(methNames...))

	set := func(name string, fn val.VectorcallFunc) {
		mod.Dict.Set(val.InternString(name), val.NewModuleFuncVal(name, fn))
	}

	createNamedHashFunc := func(name string, info hashAlgorithmInfo) val.VectorcallFunc {
		return func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			pa := posArgs(args, kwnames)
			var data []byte
			if len(pa) > 0 && !pa[0].IsNone() {
				b, ok := getBytesFromValue(pa[0])
				if !ok {
					panic("TypeError: a bytes-like object is required")
				}
				data = b
			}
			return newHashVal(info, data)
		}
	}

	for name, info := range supportedHashAlgorithms {
		set("openssl_"+name, createNamedHashFunc(name, info))
	}

	// new(name, data=b'', *, usedforsecurity=True)
	set("new", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: new() takes at least 1 argument")
		}
		if !pa[0].IsAnyString() {
			panic(fmt.Sprintf("TypeError: new() argument 'name' must be str, not %s", pa[0].Type().Name))
		}
		norm := normalizeHashName(pa[0].AsString())
		info, ok := supportedHashAlgorithms[norm]
		if !ok {
			panic(fmt.Sprintf("ValueError: unsupported hash algorithm %s", pa[0].AsString()))
		}
		var data []byte
		if len(pa) > 1 && !pa[1].IsNone() {
			b, ok := getBytesFromValue(pa[1])
			if !ok {
				panic("TypeError: a bytes-like object is required")
			}
			data = b
		}
		return newHashVal(info, data)
	})

	// compare_digest(a, b)
	set("compare_digest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 2 {
			panic("TypeError: compare_digest() takes exactly 2 arguments")
		}
		a, b := pa[0], pa[1]
		if a.IsAnyString() && b.IsAnyString() {
			sa, sb := a.AsString(), b.AsString()
			return val.BoolVal(subtle.ConstantTimeCompare([]byte(sa), []byte(sb)) == 1)
		}
		ba, aOk := getBytesFromValue(a)
		bb, bOk := getBytesFromValue(b)
		if aOk && bOk {
			return val.BoolVal(subtle.ConstantTimeCompare(ba, bb) == 1)
		}
		panic(fmt.Sprintf("TypeError: unsupported operand types(s) or combination of types for compare_digest(): '%s' and '%s'", a.Type().Name, b.Type().Name))
	})

	// hmac_digest(key, msg, digest)
	set("hmac_digest", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 3 {
			panic("TypeError: hmac_digest() takes exactly 3 arguments")
		}
		key, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: key must be bytes-like")
		}
		msg, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: msg must be bytes-like")
		}
		var algName string
		if pa[2].IsAnyString() {
			algName = pa[2].AsString()
		} else if pa[2].IsBuiltinFunc() {
			algName = pa[2].AsBuiltinFunc().Name
			if strings.HasPrefix(algName, "openssl_") {
				algName = algName[len("openssl_"):]
			}
		} else {
			algName = pa[2].Type().Name
		}
		norm := normalizeHashName(algName)
		info, exists := supportedHashAlgorithms[norm]
		if !exists {
			exc := val.NewException(unsupportedDigestmodErrorType, fmt.Sprintf("Unsupported digestmod %s", algName))
			panic(exc)
		}
		mac := hmac.New(info.constructor, key)
		mac.Write(msg)
		return val.NewBytesVal(mac.Sum(nil))
	})

	// hmac_new(key, msg=None, digestmod=None)
	set("hmac_new", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 1 {
			panic("TypeError: hmac_new() takes at least 1 argument")
		}
		key, ok := getBytesFromValue(pa[0])
		if !ok {
			panic("TypeError: key must be bytes-like")
		}
		var msg []byte
		if len(pa) > 1 && !pa[1].IsNone() {
			b, ok := getBytesFromValue(pa[1])
			if !ok {
				panic("TypeError: msg must be bytes-like")
			}
			msg = b
		}
		var dmodVal val.Value
		if v, ok := kwArg(args, kwnames, "digestmod"); ok {
			dmodVal = v
		} else if len(pa) > 2 {
			dmodVal = pa[2]
		}
		var algName string
		if dmodVal.IsAnyString() {
			algName = dmodVal.AsString()
		} else if dmodVal.IsBuiltinFunc() {
			algName = dmodVal.AsBuiltinFunc().Name
			if strings.HasPrefix(algName, "openssl_") {
				algName = algName[len("openssl_"):]
			}
		} else {
			algName = dmodVal.Type().Name
		}
		norm := normalizeHashName(algName)
		info, exists := supportedHashAlgorithms[norm]
		if !exists {
			exc := val.NewException(unsupportedDigestmodErrorType, fmt.Sprintf("Unsupported digestmod %s", algName))
			panic(exc)
		}
		return newHmacVal(info, key, msg)
	})

	// pbkdf2_hmac(hash_name, password, salt, iterations, dklen=None)
	set("pbkdf2_hmac", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		pa := posArgs(args, kwnames)
		if len(pa) < 4 {
			panic("TypeError: pbkdf2_hmac() takes at least 4 arguments")
		}
		if !pa[0].IsAnyString() {
			panic("TypeError: hash_name must be a string")
		}
		norm := normalizeHashName(pa[0].AsString())
		info, ok := supportedHashAlgorithms[norm]
		if !ok {
			panic(fmt.Sprintf("ValueError: unsupported hash algorithm %s", pa[0].AsString()))
		}
		password, ok := getBytesFromValue(pa[1])
		if !ok {
			panic("TypeError: password must be bytes-like")
		}
		salt, ok := getBytesFromValue(pa[2])
		if !ok {
			panic("TypeError: salt must be bytes-like")
		}
		if !pa[3].IsInt64() {
			panic("TypeError: iterations must be an integer")
		}
		iter := int(pa[3].AsInt64())
		if iter < 1 {
			panic("ValueError: iterations must be positive")
		}
		dklen := info.digestSize
		if len(pa) > 4 && !pa[4].IsNone() {
			if !pa[4].IsInt64() {
				panic("TypeError: dklen must be an integer")
			}
			dklen = int(pa[4].AsInt64())
			if dklen < 1 {
				panic("ValueError: dklen must be positive")
			}
		} else if v, ok := kwArg(args, kwnames, "dklen"); ok && !v.IsNone() {
			if !v.IsInt64() {
				panic("TypeError: dklen must be an integer")
			}
			dklen = int(v.AsInt64())
			if dklen < 1 {
				panic("ValueError: dklen must be positive")
			}
		}

		// Standard PKCS#5 PBKDF2 implementation
		numBlocks := (dklen + info.digestSize - 1) / info.digestSize
		dk := make([]byte, 0, numBlocks*info.digestSize)
		u := make([]byte, info.digestSize)
		block := make([]byte, info.digestSize)

		for i := 1; i <= numBlocks; i++ {
			// U_1 = PRF(password, salt || INT_32_BE(i))
			mac := hmac.New(info.constructor, password)
			mac.Write(salt)
			var ibuf [4]byte
			binary.BigEndian.PutUint32(ibuf[:], uint32(i))
			mac.Write(ibuf[:])
			u = mac.Sum(u[:0])
			copy(block, u)

			for j := 2; j <= iter; j++ {
				mac.Reset()
				mac.Write(u)
				u = mac.Sum(u[:0])
				for k := 0; k < len(block); k++ {
					block[k] ^= u[k]
				}
			}
			dk = append(dk, block...)
		}

		return val.NewBytesVal(dk[:dklen])
	})

	return modVal
}
