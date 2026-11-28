package val

import (
	"math"
	"unsafe"
)

type sortPair struct {
	val Value
	key Value
}

type timRun struct {
	base   int
	length int
}

const timMinGallop = 7

func computeMinRun(n int) int {
	r := 0
	for n >= 64 {
		r |= n & 1
		n >>= 1
	}
	return n + r
}

func reverseSlice[T any](a []T) {
	for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
}

type fastCmp[T any] interface {
	Less(a, b T) bool
}

type fallibleCmp[T any] interface {
	Less(a, b T) (bool, bool)
}

// --- Zero-dispatch non-raising comparators ---

type int64ValCmp struct{}

func (int64ValCmp) Less(a, b Value) bool { return int64(a.num) < int64(b.num) }

type int64PairCmp struct{}

func (int64PairCmp) Less(a, b sortPair) bool { return int64(a.key.num) < int64(b.key.num) }

type strValCmp struct{}

func (strValCmp) Less(a, b Value) bool { return StringCompare(a, b) < 0 }

type strPairCmp struct{}

func (strPairCmp) Less(a, b sortPair) bool { return StringCompare(a.key, b.key) < 0 }

type float64ValCmp struct{}

func (float64ValCmp) Less(a, b Value) bool {
	return math.Float64frombits(a.num) < math.Float64frombits(b.num)
}

type float64PairCmp struct{}

func (float64PairCmp) Less(a, b sortPair) bool {
	return math.Float64frombits(a.key.num) < math.Float64frombits(b.key.num)
}

// --- Fallible comparators (tuples, uniform instances, generic RichCompare) ---

type genericValCmp struct {
	ctx *ExecContext
}

func (c genericValCmp) Less(a, b Value) (bool, bool) {
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	res := RichCompare(c.ctx, a, b, CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type genericPairCmp struct {
	ctx *ExecContext
}

func (c genericPairCmp) Less(a, b sortPair) (bool, bool) {
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	res := RichCompare(c.ctx, a.key, b.key, CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type uniformInstValCmp struct {
	ctx         *ExecContext
	tp          *PyType
	expectedVer uint64
	dunderLt    Value
}

func (c uniformInstValCmp) Less(a, b Value) (bool, bool) {
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	if c.tp.Version() == c.expectedVer && c.dunderLt.IsFunction() {
		res := c.ctx.Invoke2(c.dunderLt, a, b)
		if c.ctx != nil && c.ctx.HasException() {
			return false, false
		}
		if !res.IsNotImplemented() {
			return res.IsTruthy(), true
		}
	}
	res := RichCompare(c.ctx, a, b, CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type uniformInstPairCmp struct {
	inner uniformInstValCmp
}

func (c uniformInstPairCmp) Less(a, b sortPair) (bool, bool) {
	return c.inner.Less(a.key, b.key)
}

type tupleElem0Int64ValCmp struct {
	ctx *ExecContext
}

func (c tupleElem0Int64ValCmp) Less(a, b Value) (bool, bool) {
	ta := (*PyTuple)(unsafe.Pointer(a.ptr)).Items
	tb := (*PyTuple)(unsafe.Pointer(b.ptr)).Items
	ai, bi := int64(ta[0].num), int64(tb[0].num)
	if ai != bi {
		return ai < bi, true
	}
	if len(ta) == 1 {
		return false, true
	}
	res := compareSliceRich(c.ctx, ta[1:], tb[1:], CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type tupleElem0Int64PairCmp struct {
	inner tupleElem0Int64ValCmp
}

func (c tupleElem0Int64PairCmp) Less(a, b sortPair) (bool, bool) {
	return c.inner.Less(a.key, b.key)
}

type tupleElem0StrValCmp struct {
	ctx *ExecContext
}

func (c tupleElem0StrValCmp) Less(a, b Value) (bool, bool) {
	ta := (*PyTuple)(unsafe.Pointer(a.ptr)).Items
	tb := (*PyTuple)(unsafe.Pointer(b.ptr)).Items
	if cmp := StringCompare(ta[0], tb[0]); cmp != 0 {
		return cmp < 0, true
	}
	if len(ta) == 1 {
		return false, true
	}
	res := compareSliceRich(c.ctx, ta[1:], tb[1:], CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type tupleElem0StrPairCmp struct {
	inner tupleElem0StrValCmp
}

func (c tupleElem0StrPairCmp) Less(a, b sortPair) (bool, bool) {
	return c.inner.Less(a.key, b.key)
}

type tupleElem0Float64ValCmp struct {
	ctx *ExecContext
}

func (c tupleElem0Float64ValCmp) Less(a, b Value) (bool, bool) {
	ta := (*PyTuple)(unsafe.Pointer(a.ptr)).Items
	tb := (*PyTuple)(unsafe.Pointer(b.ptr)).Items
	af, bf := math.Float64frombits(ta[0].num), math.Float64frombits(tb[0].num)
	if af != bf {
		return af < bf, true
	}
	if len(ta) == 1 {
		return false, true
	}
	res := compareSliceRich(c.ctx, ta[1:], tb[1:], CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type tupleElem0Float64PairCmp struct {
	inner tupleElem0Float64ValCmp
}

func (c tupleElem0Float64PairCmp) Less(a, b sortPair) (bool, bool) {
	return c.inner.Less(a.key, b.key)
}

type tupleElem0InstValCmp struct {
	ctx         *ExecContext
	tp          *PyType
	expectedVer uint64
	dunderEq    Value
	dunderLt    Value
}

func (c tupleElem0InstValCmp) Less(a, b Value) (bool, bool) {
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	ta := (*PyTuple)(unsafe.Pointer(a.ptr)).Items
	tb := (*PyTuple)(unsafe.Pointer(b.ptr)).Items
	a0, b0 := ta[0], tb[0]
	// CPython's PyObject_RichCompareBool short-circuits on pointer identity for
	// Py_EQ inside tuple comparison (a0 is KindInstance, never float64).
	if a0 == b0 {
		if len(ta) == 1 {
			return false, true
		}
		res := compareSliceRich(c.ctx, ta[1:], tb[1:], CmpLT)
		if c.ctx != nil && c.ctx.HasException() {
			return false, false
		}
		return res.IsTruthy(), true
	}
	if c.tp.Version() == c.expectedVer && c.dunderEq.IsFunction() && c.dunderLt.IsFunction() {
		eqRes := c.ctx.Invoke2(c.dunderEq, a0, b0)
		if c.ctx != nil && c.ctx.HasException() {
			return false, false
		}
		if !eqRes.IsNotImplemented() {
			if eqRes.IsTruthy() {
				if len(ta) == 1 {
					return false, true
				}
				res := compareSliceRich(c.ctx, ta[1:], tb[1:], CmpLT)
				if c.ctx != nil && c.ctx.HasException() {
					return false, false
				}
				return res.IsTruthy(), true
			}
			ltRes := c.ctx.Invoke2(c.dunderLt, a0, b0)
			if c.ctx != nil && c.ctx.HasException() {
				return false, false
			}
			if !ltRes.IsNotImplemented() {
				return ltRes.IsTruthy(), true
			}
		}
	}
	res := compareSliceRich(c.ctx, ta, tb, CmpLT)
	if c.ctx != nil && c.ctx.HasException() {
		return false, false
	}
	return res.IsTruthy(), true
}

type tupleElem0InstPairCmp struct {
	inner tupleElem0InstValCmp
}

func (c tupleElem0InstPairCmp) Less(a, b sortPair) (bool, bool) {
	return c.inner.Less(a.key, b.key)
}

// --- O(N) Key Classification ---

type sortKeyKind uint8

const (
	sortKindGeneric sortKeyKind = iota
	sortKindInt64
	sortKindStr
	sortKindFloat64
	sortKindUniformInst
	sortKindTupleInt64
	sortKindTupleStr
	sortKindTupleFloat64
	sortKindTupleInst
)

type sortClassification struct {
	kind        sortKeyKind
	instType    *PyType
	expectedVer uint64
	dunderEq    Value
	dunderLt    Value
}

func isExactStr(v Value) bool {
	return v.ptr == &tagInlineStr || (v.ptr != nil && v.ptr.Kind == KindString && v.ptr.Type == StrType)
}

func classifyKeys(n int, keyAt func(int) Value) sortClassification {
	if n < 2 {
		return sortClassification{kind: sortKindGeneric}
	}
	first := keyAt(0)
	switch {
	case first.ptr == &tagInt64:
		for i := 1; i < n; i++ {
			if keyAt(i).ptr != &tagInt64 {
				return sortClassification{kind: sortKindGeneric}
			}
		}
		return sortClassification{kind: sortKindInt64}

	case isExactStr(first):
		for i := 1; i < n; i++ {
			if !isExactStr(keyAt(i)) {
				return sortClassification{kind: sortKindGeneric}
			}
		}
		return sortClassification{kind: sortKindStr}

	case first.ptr == &tagFloat64:
		if math.IsNaN(math.Float64frombits(first.num)) {
			return sortClassification{kind: sortKindGeneric}
		}
		for i := 1; i < n; i++ {
			v := keyAt(i)
			if v.ptr != &tagFloat64 || math.IsNaN(math.Float64frombits(v.num)) {
				return sortClassification{kind: sortKindGeneric}
			}
		}
		return sortClassification{kind: sortKindFloat64}

	case first.ptr != nil && first.ptr.Kind == KindInstance:
		tp := first.ptr.Type
		if tp == nil || !tp.dunderLt.IsFunction() {
			return sortClassification{kind: sortKindGeneric}
		}
		for i := 1; i < n; i++ {
			v := keyAt(i)
			if v.ptr == nil || v.ptr.Kind != KindInstance || v.ptr.Type != tp {
				return sortClassification{kind: sortKindGeneric}
			}
		}
		return sortClassification{
			kind:        sortKindUniformInst,
			instType:    tp,
			expectedVer: tp.Version(),
			dunderLt:    tp.dunderLt,
		}

	case first.ptr != nil && first.ptr.Kind == KindTuple && first.ptr.Type == TupleType:
		t0 := (*PyTuple)(unsafe.Pointer(first.ptr)).Items
		k := len(t0)
		if k == 0 {
			return sortClassification{kind: sortKindGeneric}
		}
		e0 := t0[0]
		switch {
		case e0.ptr == &tagInt64:
			for i := 1; i < n; i++ {
				v := keyAt(i)
				if v.ptr == nil || v.ptr.Kind != KindTuple || v.ptr.Type != TupleType {
					return sortClassification{kind: sortKindGeneric}
				}
				ti := (*PyTuple)(unsafe.Pointer(v.ptr)).Items
				if len(ti) != k || ti[0].ptr != &tagInt64 {
					return sortClassification{kind: sortKindGeneric}
				}
			}
			return sortClassification{kind: sortKindTupleInt64}

		case isExactStr(e0):
			for i := 1; i < n; i++ {
				v := keyAt(i)
				if v.ptr == nil || v.ptr.Kind != KindTuple || v.ptr.Type != TupleType {
					return sortClassification{kind: sortKindGeneric}
				}
				ti := (*PyTuple)(unsafe.Pointer(v.ptr)).Items
				if len(ti) != k || !isExactStr(ti[0]) {
					return sortClassification{kind: sortKindGeneric}
				}
			}
			return sortClassification{kind: sortKindTupleStr}

		case e0.ptr == &tagFloat64 && !math.IsNaN(math.Float64frombits(e0.num)):
			for i := 1; i < n; i++ {
				v := keyAt(i)
				if v.ptr == nil || v.ptr.Kind != KindTuple || v.ptr.Type != TupleType {
					return sortClassification{kind: sortKindGeneric}
				}
				ti := (*PyTuple)(unsafe.Pointer(v.ptr)).Items
				if len(ti) != k || ti[0].ptr != &tagFloat64 || math.IsNaN(math.Float64frombits(ti[0].num)) {
					return sortClassification{kind: sortKindGeneric}
				}
			}
			return sortClassification{kind: sortKindTupleFloat64}

		case e0.ptr != nil && e0.ptr.Kind == KindInstance:
			tp := e0.ptr.Type
			if tp == nil || !tp.dunderLt.IsFunction() || !tp.dunderEq.IsFunction() {
				return sortClassification{kind: sortKindGeneric}
			}
			for i := 1; i < n; i++ {
				v := keyAt(i)
				if v.ptr == nil || v.ptr.Kind != KindTuple || v.ptr.Type != TupleType {
					return sortClassification{kind: sortKindGeneric}
				}
				ti := (*PyTuple)(unsafe.Pointer(v.ptr)).Items
				if len(ti) != k || ti[0].ptr == nil || ti[0].ptr.Kind != KindInstance || ti[0].ptr.Type != tp {
					return sortClassification{kind: sortKindGeneric}
				}
			}
			return sortClassification{
				kind:        sortKindTupleInst,
				instType:    tp,
				expectedVer: tp.Version(),
				dunderEq:    tp.dunderEq,
				dunderLt:    tp.dunderLt,
			}
		}
	}
	return sortClassification{kind: sortKindGeneric}
}

// --- Non-raising Timsort (stenciled per comparator struct C) ---

type timStateFast[T any, C fastCmp[T]] struct {
	a         []T
	cmp       C
	minGallop int
	runs      [85]timRun
	nRuns     int
	tmp       []T
}

func timsortFast[T any, C fastCmp[T]](a []T, cmp C) {
	n := len(a)
	if n < 2 {
		return
	}
	minRun := computeMinRun(n)
	if n < 64 {
		runLen := countRunFast(a, cmp)
		binarySortFast(a, runLen, cmp)
		return
	}
	st := timStateFast[T, C]{
		a:         a,
		cmp:       cmp,
		minGallop: timMinGallop,
	}
	lo := 0
	rem := n
	for rem > 0 {
		runLen := countRunFast(a[lo:lo+rem], cmp)
		if runLen < minRun {
			force := min(rem, minRun)
			binarySortFast(a[lo:lo+force], runLen, cmp)
			runLen = force
		}
		st.runs[st.nRuns] = timRun{base: lo, length: runLen}
		st.nRuns++
		st.mergeCollapse()
		lo += runLen
		rem -= runLen
	}
	st.mergeForceCollapse()
	clear(st.tmp)
}

func countRunFast[T any, C fastCmp[T]](a []T, cmp C) int {
	n := len(a)
	if n < 2 {
		return n
	}
	runHi := 2
	if cmp.Less(a[1], a[0]) {
		// Strictly descending run; reverse to preserve stability.
		for runHi < n && cmp.Less(a[runHi], a[runHi-1]) {
			runHi++
		}
		reverseSlice(a[:runHi])
	} else {
		for runHi < n && !cmp.Less(a[runHi], a[runHi-1]) {
			runHi++
		}
	}
	return runHi
}

func binarySortFast[T any, C fastCmp[T]](a []T, start int, cmp C) {
	if start == 0 {
		start = 1
	}
	for ; start < len(a); start++ {
		pivot := a[start]
		lo, hi := 0, start
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if cmp.Less(pivot, a[mid]) {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		if lo < start {
			copy(a[lo+1:start+1], a[lo:start])
			a[lo] = pivot
		}
	}
}

func gallopLeftFast[T any, C fastCmp[T]](key T, a []T, hint int, cmp C) int {
	n := len(a)
	lastOfs := 0
	ofs := 1
	if cmp.Less(a[hint], key) {
		maxOfs := n - hint
		for ofs < maxOfs && cmp.Less(a[hint+ofs], key) {
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		lastOfs += hint
		ofs += hint
	} else {
		maxOfs := hint + 1
		for ofs < maxOfs && !cmp.Less(a[hint-ofs], key) {
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - ofs
		ofs = hint - tmp
	}
	lastOfs++
	for lastOfs < ofs {
		m := lastOfs + int(uint(ofs-lastOfs)>>1)
		if cmp.Less(a[m], key) {
			lastOfs = m + 1
		} else {
			ofs = m
		}
	}
	return ofs
}

func gallopRightFast[T any, C fastCmp[T]](key T, a []T, hint int, cmp C) int {
	n := len(a)
	ofs := 1
	lastOfs := 0
	if cmp.Less(key, a[hint]) {
		maxOfs := hint + 1
		for ofs < maxOfs && cmp.Less(key, a[hint-ofs]) {
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - ofs
		ofs = hint - tmp
	} else {
		maxOfs := n - hint
		for ofs < maxOfs && !cmp.Less(key, a[hint+ofs]) {
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		lastOfs += hint
		ofs += hint
	}
	lastOfs++
	for lastOfs < ofs {
		m := lastOfs + int(uint(ofs-lastOfs)>>1)
		if cmp.Less(key, a[m]) {
			ofs = m
		} else {
			lastOfs = m + 1
		}
	}
	return ofs
}

func (st *timStateFast[T, C]) ensureTmp(n int) []T {
	if cap(st.tmp) < n {
		newCap := max(n, cap(st.tmp)*2)
		if newCap < 256 {
			newCap = max(n, 256)
		}
		st.tmp = make([]T, newCap)
	}
	return st.tmp[:n]
}

func (st *timStateFast[T, C]) mergeCollapse() {
	for st.nRuns > 1 {
		n := st.nRuns - 2
		switch {
		case (n > 0 && st.runs[n-1].length <= st.runs[n].length+st.runs[n+1].length) ||
			(n > 1 && st.runs[n-2].length <= st.runs[n-1].length+st.runs[n].length):
			if st.runs[n-1].length < st.runs[n+1].length {
				n--
			}
			st.mergeAt(n)
		case st.runs[n].length <= st.runs[n+1].length:
			st.mergeAt(n)
		default:
			return
		}
	}
}

func (st *timStateFast[T, C]) mergeForceCollapse() {
	for st.nRuns > 1 {
		n := st.nRuns - 2
		if n > 0 && st.runs[n-1].length < st.runs[n+1].length {
			n--
		}
		st.mergeAt(n)
	}
}

func (st *timStateFast[T, C]) mergeAt(i int) {
	base1 := st.runs[i].base
	len1 := st.runs[i].length
	base2 := st.runs[i+1].base
	len2 := st.runs[i+1].length
	st.runs[i].length = len1 + len2
	if i == st.nRuns-3 {
		st.runs[i+1] = st.runs[i+2]
	}
	st.nRuns--

	k := gallopRightFast(st.a[base2], st.a[base1:base1+len1], 0, st.cmp)
	base1 += k
	len1 -= k
	if len1 == 0 {
		return
	}
	len2 = gallopLeftFast(st.a[base1+len1-1], st.a[base2:base2+len2], len2-1, st.cmp)
	if len2 == 0 {
		return
	}
	if len1 <= len2 {
		st.mergeLo(base1, len1, base2, len2)
	} else {
		st.mergeHi(base1, len1, base2, len2)
	}
}

func (st *timStateFast[T, C]) mergeLo(base1, len1, base2, len2 int) {
	if len1 <= 64 {
		var buf [64]T
		tmp := buf[:len1]
		st.mergeLoTmp(base1, len1, base2, len2, tmp)
		clear(buf[:len1])
		return
	}
	st.mergeLoTmp(base1, len1, base2, len2, st.ensureTmp(len1))
}

func (st *timStateFast[T, C]) mergeLoTmp(base1, len1, base2, len2 int, tmp []T) {
	a := st.a
	copy(tmp, a[base1:base1+len1])

	cursor1 := 0
	cursor2 := base2
	dest := base1

	a[dest] = a[cursor2]
	dest++
	cursor2++
	len2--
	if len2 == 0 {
		copy(a[dest:dest+len1], tmp[cursor1:cursor1+len1])
		return
	}
	if len1 == 1 {
		copy(a[dest:dest+len2], a[cursor2:cursor2+len2])
		a[dest+len2] = tmp[cursor1]
		return
	}

	minGallop := st.minGallop
outer:
	for {
		count1 := 0
		count2 := 0
		for {
			if st.cmp.Less(a[cursor2], tmp[cursor1]) {
				a[dest] = a[cursor2]
				dest++
				cursor2++
				count2++
				count1 = 0
				len2--
				if len2 == 0 {
					break outer
				}
			} else {
				a[dest] = tmp[cursor1]
				dest++
				cursor1++
				count1++
				count2 = 0
				len1--
				if len1 == 1 {
					break outer
				}
			}
			if (count1 | count2) >= minGallop {
				break
			}
		}
		for {
			count1 = gallopRightFast(a[cursor2], tmp[cursor1:cursor1+len1], 0, st.cmp)
			if count1 != 0 {
				copy(a[dest:dest+count1], tmp[cursor1:cursor1+count1])
				dest += count1
				cursor1 += count1
				len1 -= count1
				if len1 <= 1 {
					break outer
				}
			}
			a[dest] = a[cursor2]
			dest++
			cursor2++
			len2--
			if len2 == 0 {
				break outer
			}
			count2 = gallopLeftFast(tmp[cursor1], a[cursor2:cursor2+len2], 0, st.cmp)
			if count2 != 0 {
				copy(a[dest:dest+count2], a[cursor2:cursor2+count2])
				dest += count2
				cursor2 += count2
				len2 -= count2
				if len2 == 0 {
					break outer
				}
			}
			a[dest] = tmp[cursor1]
			dest++
			cursor1++
			len1--
			if len1 == 1 {
				break outer
			}
			minGallop--
			if count1 < timMinGallop && count2 < timMinGallop {
				break
			}
		}
		if minGallop < 0 {
			minGallop = 0
		}
		minGallop += 2
	}
	if minGallop < 1 {
		minGallop = 1
	}
	st.minGallop = minGallop
	if len1 == 1 {
		copy(a[dest:dest+len2], a[cursor2:cursor2+len2])
		a[dest+len2] = tmp[cursor1]
	} else if len1 > 0 {
		copy(a[dest:dest+len1], tmp[cursor1:cursor1+len1])
	}
}

func (st *timStateFast[T, C]) mergeHi(base1, len1, base2, len2 int) {
	if len2 <= 64 {
		var buf [64]T
		tmp := buf[:len2]
		st.mergeHiTmp(base1, len1, base2, len2, tmp)
		clear(buf[:len2])
		return
	}
	st.mergeHiTmp(base1, len1, base2, len2, st.ensureTmp(len2))
}

func (st *timStateFast[T, C]) mergeHiTmp(base1, len1, base2, len2 int, tmp []T) {
	a := st.a
	copy(tmp, a[base2:base2+len2])

	cursor1 := base1 + len1 - 1
	cursor2 := len2 - 1
	dest := base2 + len2 - 1

	a[dest] = a[cursor1]
	dest--
	cursor1--
	len1--
	if len1 == 0 {
		copy(a[dest-(len2-1):dest+1], tmp[:len2])
		return
	}
	if len2 == 1 {
		dest -= len1
		cursor1 -= len1
		copy(a[dest+1:dest+1+len1], a[cursor1+1:cursor1+1+len1])
		a[dest] = tmp[cursor2]
		return
	}

	minGallop := st.minGallop
outer:
	for {
		count1 := 0
		count2 := 0
		for {
			if st.cmp.Less(tmp[cursor2], a[cursor1]) {
				a[dest] = a[cursor1]
				dest--
				cursor1--
				count1++
				count2 = 0
				len1--
				if len1 == 0 {
					break outer
				}
			} else {
				a[dest] = tmp[cursor2]
				dest--
				cursor2--
				count2++
				count1 = 0
				len2--
				if len2 == 1 {
					break outer
				}
			}
			if (count1 | count2) >= minGallop {
				break
			}
		}
		for {
			count1 = len1 - gallopRightFast(tmp[cursor2], a[base1:base1+len1], len1-1, st.cmp)
			if count1 != 0 {
				dest -= count1
				cursor1 -= count1
				len1 -= count1
				copy(a[dest+1:dest+1+count1], a[cursor1+1:cursor1+1+count1])
				if len1 == 0 {
					break outer
				}
			}
			a[dest] = tmp[cursor2]
			dest--
			cursor2--
			len2--
			if len2 == 1 {
				break outer
			}
			count2 = len2 - gallopLeftFast(a[cursor1], tmp[:len2], len2-1, st.cmp)
			if count2 != 0 {
				dest -= count2
				cursor2 -= count2
				len2 -= count2
				copy(a[dest+1:dest+1+count2], tmp[cursor2+1:cursor2+1+count2])
				if len2 <= 1 {
					break outer
				}
			}
			a[dest] = a[cursor1]
			dest--
			cursor1--
			len1--
			if len1 == 0 {
				break outer
			}
			minGallop--
			if count1 < timMinGallop && count2 < timMinGallop {
				break
			}
		}
		if minGallop < 0 {
			minGallop = 0
		}
		minGallop += 2
	}
	if minGallop < 1 {
		minGallop = 1
	}
	st.minGallop = minGallop
	if len2 == 1 {
		dest -= len1
		cursor1 -= len1
		copy(a[dest+1:dest+1+len1], a[cursor1+1:cursor1+1+len1])
		a[dest] = tmp[cursor2]
	} else if len2 > 0 {
		copy(a[dest-(len2-1):dest+1], tmp[:len2])
	}
}

// --- Fallible Timsort (aborts cleanly on Python exceptions) ---

type timStateFallible[T any, C fallibleCmp[T]] struct {
	a         []T
	cmp       C
	minGallop int
	runs      [85]timRun
	nRuns     int
	tmp       []T
}

func timsortFallible[T any, C fallibleCmp[T]](a []T, cmp C) bool {
	n := len(a)
	if n < 2 {
		return true
	}
	minRun := computeMinRun(n)
	if n < 64 {
		runLen, ok := countRunFallible(a, cmp)
		if !ok {
			return false
		}
		return binarySortFallible(a, runLen, cmp)
	}
	st := timStateFallible[T, C]{
		a:         a,
		cmp:       cmp,
		minGallop: timMinGallop,
	}
	lo := 0
	rem := n
	for rem > 0 {
		runLen, ok := countRunFallible(a[lo:lo+rem], cmp)
		if !ok {
			clear(st.tmp)
			return false
		}
		if runLen < minRun {
			force := min(rem, minRun)
			if !binarySortFallible(a[lo:lo+force], runLen, cmp) {
				clear(st.tmp)
				return false
			}
			runLen = force
		}
		st.runs[st.nRuns] = timRun{base: lo, length: runLen}
		st.nRuns++
		if !st.mergeCollapse() {
			clear(st.tmp)
			return false
		}
		lo += runLen
		rem -= runLen
	}
	ok := st.mergeForceCollapse()
	clear(st.tmp)
	return ok
}

func countRunFallible[T any, C fallibleCmp[T]](a []T, cmp C) (int, bool) {
	n := len(a)
	if n < 2 {
		return n, true
	}
	lt, ok := cmp.Less(a[1], a[0])
	if !ok {
		return 0, false
	}
	runHi := 2
	if lt {
		for runHi < n {
			lt, ok = cmp.Less(a[runHi], a[runHi-1])
			if !ok {
				return 0, false
			}
			if !lt {
				break
			}
			runHi++
		}
		reverseSlice(a[:runHi])
	} else {
		for runHi < n {
			lt, ok = cmp.Less(a[runHi], a[runHi-1])
			if !ok {
				return 0, false
			}
			if lt {
				break
			}
			runHi++
		}
	}
	return runHi, true
}

func binarySortFallible[T any, C fallibleCmp[T]](a []T, start int, cmp C) bool {
	if start == 0 {
		start = 1
	}
	for ; start < len(a); start++ {
		pivot := a[start]
		lo, hi := 0, start
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			lt, ok := cmp.Less(pivot, a[mid])
			if !ok {
				return false
			}
			if lt {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		if lo < start {
			copy(a[lo+1:start+1], a[lo:start])
			a[lo] = pivot
		}
	}
	return true
}

func gallopLeftFallible[T any, C fallibleCmp[T]](key T, a []T, hint int, cmp C) (int, bool) {
	n := len(a)
	lastOfs := 0
	ofs := 1
	lt, ok := cmp.Less(a[hint], key)
	if !ok {
		return 0, false
	}
	if lt {
		maxOfs := n - hint
		for ofs < maxOfs {
			lt, ok = cmp.Less(a[hint+ofs], key)
			if !ok {
				return 0, false
			}
			if !lt {
				break
			}
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		lastOfs += hint
		ofs += hint
	} else {
		maxOfs := hint + 1
		for ofs < maxOfs {
			lt, ok = cmp.Less(a[hint-ofs], key)
			if !ok {
				return 0, false
			}
			if lt {
				break
			}
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - ofs
		ofs = hint - tmp
	}
	lastOfs++
	for lastOfs < ofs {
		m := lastOfs + int(uint(ofs-lastOfs)>>1)
		lt, ok = cmp.Less(a[m], key)
		if !ok {
			return 0, false
		}
		if lt {
			lastOfs = m + 1
		} else {
			ofs = m
		}
	}
	return ofs, true
}

func gallopRightFallible[T any, C fallibleCmp[T]](key T, a []T, hint int, cmp C) (int, bool) {
	n := len(a)
	ofs := 1
	lastOfs := 0
	lt, ok := cmp.Less(key, a[hint])
	if !ok {
		return 0, false
	}
	if lt {
		maxOfs := hint + 1
		for ofs < maxOfs {
			lt, ok = cmp.Less(key, a[hint-ofs])
			if !ok {
				return 0, false
			}
			if !lt {
				break
			}
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		tmp := lastOfs
		lastOfs = hint - ofs
		ofs = hint - tmp
	} else {
		maxOfs := n - hint
		for ofs < maxOfs {
			lt, ok = cmp.Less(key, a[hint+ofs])
			if !ok {
				return 0, false
			}
			if lt {
				break
			}
			lastOfs = ofs
			ofs = (ofs << 1) + 1
			if ofs <= 0 {
				ofs = maxOfs
			}
		}
		if ofs > maxOfs {
			ofs = maxOfs
		}
		lastOfs += hint
		ofs += hint
	}
	lastOfs++
	for lastOfs < ofs {
		m := lastOfs + int(uint(ofs-lastOfs)>>1)
		lt, ok = cmp.Less(key, a[m])
		if !ok {
			return 0, false
		}
		if lt {
			ofs = m
		} else {
			lastOfs = m + 1
		}
	}
	return ofs, true
}

func (st *timStateFallible[T, C]) ensureTmp(n int) []T {
	if cap(st.tmp) < n {
		newCap := max(n, cap(st.tmp)*2)
		if newCap < 256 {
			newCap = max(n, 256)
		}
		st.tmp = make([]T, newCap)
	}
	return st.tmp[:n]
}

func (st *timStateFallible[T, C]) mergeCollapse() bool {
	for st.nRuns > 1 {
		n := st.nRuns - 2
		switch {
		case (n > 0 && st.runs[n-1].length <= st.runs[n].length+st.runs[n+1].length) ||
			(n > 1 && st.runs[n-2].length <= st.runs[n-1].length+st.runs[n].length):
			if st.runs[n-1].length < st.runs[n+1].length {
				n--
			}
			if !st.mergeAt(n) {
				return false
			}
		case st.runs[n].length <= st.runs[n+1].length:
			if !st.mergeAt(n) {
				return false
			}
		default:
			return true
		}
	}
	return true
}

func (st *timStateFallible[T, C]) mergeForceCollapse() bool {
	for st.nRuns > 1 {
		n := st.nRuns - 2
		if n > 0 && st.runs[n-1].length < st.runs[n+1].length {
			n--
		}
		if !st.mergeAt(n) {
			return false
		}
	}
	return true
}

func (st *timStateFallible[T, C]) mergeAt(i int) bool {
	base1 := st.runs[i].base
	len1 := st.runs[i].length
	base2 := st.runs[i+1].base
	len2 := st.runs[i+1].length
	st.runs[i].length = len1 + len2
	if i == st.nRuns-3 {
		st.runs[i+1] = st.runs[i+2]
	}
	st.nRuns--

	k, ok := gallopRightFallible(st.a[base2], st.a[base1:base1+len1], 0, st.cmp)
	if !ok {
		return false
	}
	base1 += k
	len1 -= k
	if len1 == 0 {
		return true
	}
	len2, ok = gallopLeftFallible(st.a[base1+len1-1], st.a[base2:base2+len2], len2-1, st.cmp)
	if !ok {
		return false
	}
	if len2 == 0 {
		return true
	}
	if len1 <= len2 {
		return st.mergeLo(base1, len1, base2, len2)
	}
	return st.mergeHi(base1, len1, base2, len2)
}

func (st *timStateFallible[T, C]) mergeLo(base1, len1, base2, len2 int) bool {
	if len1 <= 64 {
		var buf [64]T
		tmp := buf[:len1]
		ok := st.mergeLoTmp(base1, len1, base2, len2, tmp)
		clear(buf[:len1])
		return ok
	}
	return st.mergeLoTmp(base1, len1, base2, len2, st.ensureTmp(len1))
}

func (st *timStateFallible[T, C]) mergeLoTmp(base1, len1, base2, len2 int, tmp []T) bool {
	a := st.a
	copy(tmp, a[base1:base1+len1])

	cursor1 := 0
	cursor2 := base2
	dest := base1

	a[dest] = a[cursor2]
	dest++
	cursor2++
	len2--
	if len2 == 0 {
		copy(a[dest:dest+len1], tmp[cursor1:cursor1+len1])
		return true
	}
	if len1 == 1 {
		copy(a[dest:dest+len2], a[cursor2:cursor2+len2])
		a[dest+len2] = tmp[cursor1]
		return true
	}

	minGallop := st.minGallop
outer:
	for {
		count1 := 0
		count2 := 0
		for {
			lt, ok := st.cmp.Less(a[cursor2], tmp[cursor1])
			if !ok {
				return false
			}
			if lt {
				a[dest] = a[cursor2]
				dest++
				cursor2++
				count2++
				count1 = 0
				len2--
				if len2 == 0 {
					break outer
				}
			} else {
				a[dest] = tmp[cursor1]
				dest++
				cursor1++
				count1++
				count2 = 0
				len1--
				if len1 == 1 {
					break outer
				}
			}
			if (count1 | count2) >= minGallop {
				break
			}
		}
		for {
			var ok bool
			count1, ok = gallopRightFallible(a[cursor2], tmp[cursor1:cursor1+len1], 0, st.cmp)
			if !ok {
				return false
			}
			if count1 != 0 {
				copy(a[dest:dest+count1], tmp[cursor1:cursor1+count1])
				dest += count1
				cursor1 += count1
				len1 -= count1
				if len1 <= 1 {
					break outer
				}
			}
			a[dest] = a[cursor2]
			dest++
			cursor2++
			len2--
			if len2 == 0 {
				break outer
			}
			count2, ok = gallopLeftFallible(tmp[cursor1], a[cursor2:cursor2+len2], 0, st.cmp)
			if !ok {
				return false
			}
			if count2 != 0 {
				copy(a[dest:dest+count2], a[cursor2:cursor2+count2])
				dest += count2
				cursor2 += count2
				len2 -= count2
				if len2 == 0 {
					break outer
				}
			}
			a[dest] = tmp[cursor1]
			dest++
			cursor1++
			len1--
			if len1 == 1 {
				break outer
			}
			minGallop--
			if count1 < timMinGallop && count2 < timMinGallop {
				break
			}
		}
		if minGallop < 0 {
			minGallop = 0
		}
		minGallop += 2
	}
	if minGallop < 1 {
		minGallop = 1
	}
	st.minGallop = minGallop
	if len1 == 1 {
		copy(a[dest:dest+len2], a[cursor2:cursor2+len2])
		a[dest+len2] = tmp[cursor1]
	} else if len1 > 0 {
		copy(a[dest:dest+len1], tmp[cursor1:cursor1+len1])
	}
	return true
}

func (st *timStateFallible[T, C]) mergeHi(base1, len1, base2, len2 int) bool {
	if len2 <= 64 {
		var buf [64]T
		tmp := buf[:len2]
		ok := st.mergeHiTmp(base1, len1, base2, len2, tmp)
		clear(buf[:len2])
		return ok
	}
	return st.mergeHiTmp(base1, len1, base2, len2, st.ensureTmp(len2))
}

func (st *timStateFallible[T, C]) mergeHiTmp(base1, len1, base2, len2 int, tmp []T) bool {
	a := st.a
	copy(tmp, a[base2:base2+len2])

	cursor1 := base1 + len1 - 1
	cursor2 := len2 - 1
	dest := base2 + len2 - 1

	a[dest] = a[cursor1]
	dest--
	cursor1--
	len1--
	if len1 == 0 {
		copy(a[dest-(len2-1):dest+1], tmp[:len2])
		return true
	}
	if len2 == 1 {
		dest -= len1
		cursor1 -= len1
		copy(a[dest+1:dest+1+len1], a[cursor1+1:cursor1+1+len1])
		a[dest] = tmp[cursor2]
		return true
	}

	minGallop := st.minGallop
outer:
	for {
		count1 := 0
		count2 := 0
		for {
			lt, ok := st.cmp.Less(tmp[cursor2], a[cursor1])
			if !ok {
				return false
			}
			if lt {
				a[dest] = a[cursor1]
				dest--
				cursor1--
				count1++
				count2 = 0
				len1--
				if len1 == 0 {
					break outer
				}
			} else {
				a[dest] = tmp[cursor2]
				dest--
				cursor2--
				count2++
				count1 = 0
				len2--
				if len2 == 1 {
					break outer
				}
			}
			if (count1 | count2) >= minGallop {
				break
			}
		}
		for {
			pos1, ok := gallopRightFallible(tmp[cursor2], a[base1:base1+len1], len1-1, st.cmp)
			if !ok {
				return false
			}
			count1 = len1 - pos1
			if count1 != 0 {
				dest -= count1
				cursor1 -= count1
				len1 -= count1
				copy(a[dest+1:dest+1+count1], a[cursor1+1:cursor1+1+count1])
				if len1 == 0 {
					break outer
				}
			}
			a[dest] = tmp[cursor2]
			dest--
			cursor2--
			len2--
			if len2 == 1 {
				break outer
			}
			pos2, ok := gallopLeftFallible(a[cursor1], tmp[:len2], len2-1, st.cmp)
			if !ok {
				return false
			}
			count2 = len2 - pos2
			if count2 != 0 {
				dest -= count2
				cursor2 -= count2
				len2 -= count2
				copy(a[dest+1:dest+1+count2], tmp[cursor2+1:cursor2+1+count2])
				if len2 <= 1 {
					break outer
				}
			}
			a[dest] = a[cursor1]
			dest--
			cursor1--
			len1--
			if len1 == 0 {
				break outer
			}
			minGallop--
			if count1 < timMinGallop && count2 < timMinGallop {
				break
			}
		}
		if minGallop < 0 {
			minGallop = 0
		}
		minGallop += 2
	}
	if minGallop < 1 {
		minGallop = 1
	}
	st.minGallop = minGallop
	if len2 == 1 {
		dest -= len1
		cursor1 -= len1
		copy(a[dest+1:dest+1+len1], a[cursor1+1:cursor1+1+len1])
		a[dest] = tmp[cursor2]
	} else if len2 > 0 {
		copy(a[dest-(len2-1):dest+1], tmp[:len2])
	}
	return true
}

// --- Dispatch helpers & Public Entrypoints ---

// BatchApplyHook1 applies a single-argument Python function to every element
// of items in one batched VM entry, reporting each key through setKey. It is
// set by pkg/vm. Returning false without an exception on ctx declines the
// fast path (unsupported callable shape) and the caller falls back to the
// generic per-element loop; false with an exception set reports a key-function
// failure.
var BatchApplyHook1 func(ctx *ExecContext, fn Value, items []Value, setKey func(int, Value)) bool

func evalSortKeys(ctx *ExecContext, items []Value, keyFn Value) ([]sortPair, bool) {
	pairs := make([]sortPair, len(items))
	var argBuf [1]Value
	if keyFn.IsBuiltinFunc() {
		bf := keyFn.AsBuiltinFunc().Fn
		for i, itm := range items {
			argBuf[0] = itm
			k := bf(ctx, keyFn, argBuf[:], nil)
			if ctx != nil && ctx.HasException() {
				clear(pairs)
				return nil, false
			}
			pairs[i] = sortPair{val: itm, key: k}
		}
		return pairs, true
	}
	if keyFn.IsFunction() && BatchApplyHook1 != nil {
		for i, itm := range items {
			pairs[i].val = itm
		}
		if BatchApplyHook1(ctx, keyFn, items, func(i int, k Value) { pairs[i].key = k }) {
			return pairs, true
		}
		if ctx != nil && ctx.HasException() {
			clear(pairs)
			return nil, false
		}
	}
	if tp := keyFn.Type(); tp != nil && tp.Slots.TpCall != nil && !keyFn.IsFunction() && !keyFn.IsType() {
		tpCall := tp.Slots.TpCall
		for i, itm := range items {
			argBuf[0] = itm
			k := tpCall(ctx, keyFn, argBuf[:], nil)
			if ctx != nil && ctx.HasException() {
				clear(pairs)
				return nil, false
			}
			pairs[i] = sortPair{val: itm, key: k}
		}
		return pairs, true
	}
	for i, itm := range items {
		k := ctx.Invoke1(keyFn, itm)
		if ctx != nil && ctx.HasException() {
			clear(pairs)
			return nil, false
		}
		pairs[i] = sortPair{val: itm, key: k}
	}
	return pairs, true
}

func sortPairsClassified(ctx *ExecContext, pairs []sortPair, cls sortClassification, reverse bool) bool {
	if len(pairs) < 2 {
		return true
	}
	if reverse {
		reverseSlice(pairs)
	}
	ok := true
	switch cls.kind {
	case sortKindInt64:
		timsortFast(pairs, int64PairCmp{})
	case sortKindStr:
		timsortFast(pairs, strPairCmp{})
	case sortKindFloat64:
		timsortFast(pairs, float64PairCmp{})
	case sortKindUniformInst:
		ok = timsortFallible(pairs, uniformInstPairCmp{
			inner: uniformInstValCmp{
				ctx:         ctx,
				tp:          cls.instType,
				expectedVer: cls.expectedVer,
				dunderLt:    cls.dunderLt,
			},
		})
	case sortKindTupleInt64:
		ok = timsortFallible(pairs, tupleElem0Int64PairCmp{inner: tupleElem0Int64ValCmp{ctx: ctx}})
	case sortKindTupleStr:
		ok = timsortFallible(pairs, tupleElem0StrPairCmp{inner: tupleElem0StrValCmp{ctx: ctx}})
	case sortKindTupleFloat64:
		ok = timsortFallible(pairs, tupleElem0Float64PairCmp{inner: tupleElem0Float64ValCmp{ctx: ctx}})
	case sortKindTupleInst:
		ok = timsortFallible(pairs, tupleElem0InstPairCmp{
			inner: tupleElem0InstValCmp{
				ctx:         ctx,
				tp:          cls.instType,
				expectedVer: cls.expectedVer,
				dunderEq:    cls.dunderEq,
				dunderLt:    cls.dunderLt,
			},
		})
	default:
		ok = timsortFallible(pairs, genericPairCmp{ctx: ctx})
	}
	if !ok {
		return false
	}
	if reverse {
		reverseSlice(pairs)
	}
	return true
}

func sortValuesClassified(ctx *ExecContext, items []Value, cls sortClassification, reverse bool) bool {
	if len(items) < 2 {
		return true
	}
	if reverse {
		reverseSlice(items)
	}
	ok := true
	switch cls.kind {
	case sortKindInt64:
		timsortFast(items, int64ValCmp{})
	case sortKindStr:
		timsortFast(items, strValCmp{})
	case sortKindFloat64:
		timsortFast(items, float64ValCmp{})
	case sortKindUniformInst:
		ok = timsortFallible(items, uniformInstValCmp{
			ctx:         ctx,
			tp:          cls.instType,
			expectedVer: cls.expectedVer,
			dunderLt:    cls.dunderLt,
		})
	case sortKindTupleInt64:
		ok = timsortFallible(items, tupleElem0Int64ValCmp{ctx: ctx})
	case sortKindTupleStr:
		ok = timsortFallible(items, tupleElem0StrValCmp{ctx: ctx})
	case sortKindTupleFloat64:
		ok = timsortFallible(items, tupleElem0Float64ValCmp{ctx: ctx})
	case sortKindTupleInst:
		ok = timsortFallible(items, tupleElem0InstValCmp{
			ctx:         ctx,
			tp:          cls.instType,
			expectedVer: cls.expectedVer,
			dunderEq:    cls.dunderEq,
			dunderLt:    cls.dunderLt,
		})
	default:
		ok = timsortFallible(items, genericValCmp{ctx: ctx})
	}
	if !ok {
		return false
	}
	if reverse {
		reverseSlice(items)
	}
	return true
}

// SortList sorts l in-place using CPython-compatible Timsort with detach/restore
// list-mutation protection and mid-sort exception rollback.
func SortList(ctx *ExecContext, l *PyList, keyFn Value, reverse bool) {
	l.mu.Lock()
	orig := l.Items
	l.Items = nil
	l.mu.Unlock()

	hasKey := keyFn.ptr != nil && !keyFn.IsNone()
	if len(orig) == 0 || (len(orig) == 1 && !hasKey) {
		l.mu.Lock()
		if len(l.Items) == 0 {
			l.Items = orig
		}
		l.mu.Unlock()
		return
	}

	restoreOrig := func() {
		l.mu.Lock()
		if len(l.Items) == 0 {
			l.Items = orig
		} else {
			l.Items = append(orig, l.Items...)
		}
		l.mu.Unlock()
	}

	var result []Value
	if hasKey {
		pairs, ok := evalSortKeys(ctx, orig, keyFn)
		if !ok {
			restoreOrig()
			return
		}
		cls := classifyKeys(len(pairs), func(i int) Value { return pairs[i].key })
		if !sortPairsClassified(ctx, pairs, cls, reverse) {
			clear(pairs)
			restoreOrig()
			return
		}
		// Check list mutation before mutating orig in place.
		l.mu.Lock()
		modified := len(l.Items) != 0
		if modified {
			l.Items = append(orig, l.Items...)
			l.mu.Unlock()
			clear(pairs)
			if ctx != nil {
				ctx.Raise(NewException(ValueErrorType, "list modified during sort"))
			}
			return
		}
		for i, p := range pairs {
			orig[i] = p.val
		}
		l.Items = orig
		l.mu.Unlock()
		clear(pairs)
		return
	}

	cls := classifyKeys(len(orig), func(i int) Value { return orig[i] })
	// Non-raising homogeneous primitives can safely sort orig in place with 0 copy.
	// Raising comparators sort a working copy so mid-sort exceptions leave orig intact.
	switch cls.kind {
	case sortKindInt64, sortKindStr, sortKindFloat64:
		sortValuesClassified(ctx, orig, cls, reverse)
		result = orig
	default:
		work := make([]Value, len(orig))
		copy(work, orig)
		if !sortValuesClassified(ctx, work, cls, reverse) {
			clear(work)
			restoreOrig()
			return
		}
		result = work
	}

	l.mu.Lock()
	modified := len(l.Items) != 0
	if modified {
		l.Items = append(orig, l.Items...)
	} else {
		l.Items = result
	}
	l.mu.Unlock()
	if modified && ctx != nil {
		ctx.Raise(NewException(ValueErrorType, "list modified during sort"))
	}
}

// SortValues sorts items in-place (used by builtin sorted() on a fresh slice)
// and returns false if a Python exception was raised during key evaluation or comparison.
func SortValues(ctx *ExecContext, items []Value, keyFn Value, reverse bool) bool {
	hasKey := keyFn.ptr != nil && !keyFn.IsNone()
	if len(items) == 0 || (len(items) == 1 && !hasKey) {
		return true
	}
	if hasKey {
		pairs, ok := evalSortKeys(ctx, items, keyFn)
		if !ok {
			return false
		}
		cls := classifyKeys(len(pairs), func(i int) Value { return pairs[i].key })
		if !sortPairsClassified(ctx, pairs, cls, reverse) {
			clear(pairs)
			return false
		}
		for i, p := range pairs {
			items[i] = p.val
		}
		clear(pairs)
		return true
	}
	cls := classifyKeys(len(items), func(i int) Value { return items[i] })
	return sortValuesClassified(ctx, items, cls, reverse)
}
