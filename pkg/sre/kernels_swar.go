//go:build !goexperiment.simd || !amd64

package sre

func scanCharSetBytes(b []byte, cs *charSet) int {
	return scanCharSetBytesSWAR(b, cs)
}

func scanByteEq(b []byte, want byte) int {
	return scanByteEqSWAR(b, want)
}

func scanCharSetRunes(rs []rune, cs *charSet) int {
	return scanCharSetRunesSWAR(rs, cs)
}

func indexRuneFast(rs []rune, want rune) int {
	return indexRuneSWAR(rs, want)
}
