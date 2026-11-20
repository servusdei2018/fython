//go:build !goexperiment.simd || !amd64

package val

func isASCIIBytes(b []byte) bool {
	return isASCIIBytesSWAR(b)
}

func findJSONEscape(b []byte) int {
	return findJSONEscapeSWAR(b)
}

func findJSONEscapeASCII(b []byte) int {
	return findJSONEscapeASCIISWAR(b)
}
