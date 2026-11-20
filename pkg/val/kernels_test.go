package val

import (
	"bytes"
	"math"
	"testing"
)

func TestKernelParityAndBoundaries(t *testing.T) {
	specials := []byte{0x00, 0x01, 0x08, 0x09, 0x0a, 0x0c, 0x0d, 0x1f, '"', '\\', 0x7f, 0x80, 0xc3, 0xff}

	for length := range 130 {
		clean := bytes.Repeat([]byte{'a'}, length)
		if IsASCIIBytes(clean) != isASCIIBytesScalar(clean) || !isASCIIBytesSWAR(clean) {
			t.Fatalf("clean length %d: IsASCII mismatch", length)
		}
		if got := FindJSONEscape(clean); got != length || got != findJSONEscapeScalar(clean) || findJSONEscapeSWAR(clean) != length {
			t.Fatalf("clean length %d: FindJSONEscape=%d want %d", length, got, length)
		}
		if got := FindJSONEscapeASCII(clean); got != length || got != findJSONEscapeASCIIScalar(clean) || findJSONEscapeASCIISWAR(clean) != length {
			t.Fatalf("clean length %d: FindJSONEscapeASCII=%d want %d", length, got, length)
		}

		for _, sp := range specials {
			for pos := range length {
				if length > 40 && pos > 5 && pos < length-5 && pos != 7 && pos != 8 && pos != 15 && pos != 16 && pos != 31 && pos != 32 && pos != 33 && pos != 63 && pos != 64 {
					continue
				}
				buf := make([]byte, length)
				copy(buf, clean)
				buf[pos] = sp

				wantASCII := isASCIIBytesScalar(buf)
				if got := IsASCIIBytes(buf); got != wantASCII {
					t.Fatalf("len=%d pos=%d byte=0x%02x IsASCIIBytes=%v want %v", length, pos, sp, got, wantASCII)
				}
				if got := isASCIIBytesSWAR(buf); got != wantASCII {
					t.Fatalf("len=%d pos=%d byte=0x%02x isASCIIBytesSWAR=%v want %v", length, pos, sp, got, wantASCII)
				}

				wantEsc := findJSONEscapeScalar(buf)
				if got := FindJSONEscape(buf); got != wantEsc {
					t.Fatalf("len=%d pos=%d byte=0x%02x FindJSONEscape=%d want %d", length, pos, sp, got, wantEsc)
				}
				if got := findJSONEscapeSWAR(buf); got != wantEsc {
					t.Fatalf("len=%d pos=%d byte=0x%02x findJSONEscapeSWAR=%d want %d", length, pos, sp, got, wantEsc)
				}

				wantEscASCII := findJSONEscapeASCIIScalar(buf)
				if got := FindJSONEscapeASCII(buf); got != wantEscASCII {
					t.Fatalf("len=%d pos=%d byte=0x%02x FindJSONEscapeASCII=%d want %d", length, pos, sp, got, wantEscASCII)
				}
				if got := findJSONEscapeASCIISWAR(buf); got != wantEscASCII {
					t.Fatalf("len=%d pos=%d byte=0x%02x findJSONEscapeASCIISWAR=%d want %d", length, pos, sp, got, wantEscASCII)
				}
			}
		}
	}

	// Verify multi-byte UTF-8 inputs across inline and heap string constructors.
	utf8Samples := []string{
		"hello",
		"café",
		"😀🚀",
		"1234567",
		"12345678",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaé",
	}
	for _, s := range utf8Samples {
		v := NewStringVal(s)
		if IsASCIIString(v) != isASCIIBytesScalar([]byte(s)) {
			t.Fatalf("IsASCIIString(%q) mismatch", s)
		}
	}

	floatSamples := []float64{0, math.Copysign(0, -1), 1.0, -1.0, 3.141592653589793, 1e-5, 1e-4, 1e15, 1e16, 1e17, -1e20, 0.00012345, 123456.789}
	for _, f := range floatSamples {
		got := string(AppendFloatRepr(nil, f))
		want := FloatRepr(f)
		if got != want {
			t.Fatalf("AppendFloatRepr(%g) = %q, want %q", f, got, want)
		}
	}
}
