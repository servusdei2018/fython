package sre

import (
	"encoding/binary"
	"math/bits"
)

const (
	swarHiBits uint64 = 0x8080808080808080
	swarLo7F   uint64 = 0x7F7F7F7F7F7F7F7F
	swarMul01  uint64 = 0x0101010101010101
)

func scanCharSetBytesScalar(b []byte, cs *charSet) int {
	tbl := &cs.table
	for i, c := range b {
		if !tbl[c] {
			return i
		}
	}
	return len(b)
}

func scanCharSetBytesSWAR(b []byte, cs *charSet) int {
	n := len(b)
	tbl := &cs.table
	i := 0
	for ; i+8 <= n; i += 8 {
		w := binary.LittleEndian.Uint64(b[i : i+8])
		if !tbl[byte(w)] {
			return i
		}
		if !tbl[byte(w>>8)] {
			return i + 1
		}
		if !tbl[byte(w>>16)] {
			return i + 2
		}
		if !tbl[byte(w>>24)] {
			return i + 3
		}
		if !tbl[byte(w>>32)] {
			return i + 4
		}
		if !tbl[byte(w>>40)] {
			return i + 5
		}
		if !tbl[byte(w>>48)] {
			return i + 6
		}
		if !tbl[byte(w>>56)] {
			return i + 7
		}
	}
	for ; i < n; i++ {
		if !tbl[b[i]] {
			return i
		}
	}
	return n
}

func scanByteEqScalar(b []byte, want byte) int {
	for i, c := range b {
		if c != want {
			return i
		}
	}
	return len(b)
}

func scanByteEqSWAR(b []byte, want byte) int {
	n := len(b)
	want64 := uint64(want) * swarMul01
	i := 0
	for ; i+8 <= n; i += 8 {
		x := binary.LittleEndian.Uint64(b[i:]) ^ want64
		if x != 0 {
			m := (x | ((x & swarLo7F) + swarLo7F)) & swarHiBits
			return i + (bits.TrailingZeros64(m) >> 3)
		}
	}
	for ; i < n; i++ {
		if b[i] != want {
			return i
		}
	}
	return n
}

func scanCharSetRunesScalar(rs []rune, cs *charSet) int {
	tbl := &cs.table
	for i, r := range rs {
		if uint32(r) < 128 {
			if !tbl[r] {
				return i
			}
		} else if !cs.slow(r) {
			return i
		}
	}
	return len(rs)
}

func scanCharSetRunesSWAR(rs []rune, cs *charSet) int {
	n := len(rs)
	tbl := &cs.table
	i := 0
	for ; i+4 <= n; i += 4 {
		r0, r1, r2, r3 := rs[i], rs[i+1], rs[i+2], rs[i+3]
		if uint32(r0|r1|r2|r3) < 128 {
			if !tbl[r0] {
				return i
			}
			if !tbl[r1] {
				return i + 1
			}
			if !tbl[r2] {
				return i + 2
			}
			if !tbl[r3] {
				return i + 3
			}
			continue
		}
		return i + scanCharSetRunesScalar(rs[i:], cs)
	}
	return i + scanCharSetRunesScalar(rs[i:], cs)
}

func indexRuneScalar(rs []rune, want rune) int {
	for i, r := range rs {
		if r == want {
			return i
		}
	}
	return len(rs)
}

func indexRuneSWAR(rs []rune, want rune) int {
	n := len(rs)
	i := 0
	for ; i+4 <= n; i += 4 {
		if rs[i] == want {
			return i
		}
		if rs[i+1] == want {
			return i + 1
		}
		if rs[i+2] == want {
			return i + 2
		}
		if rs[i+3] == want {
			return i + 3
		}
	}
	for ; i < n; i++ {
		if rs[i] == want {
			return i
		}
	}
	return n
}
