package vm

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/servusdei2018/fython/pkg/val"
)

var (
	DequeType       *val.PyType
	DefaultDictType *val.PyType
	TupleGetterType *val.PyType
	collectionsOnce sync.Once
)

type pyDeque struct {
	Header val.ObjectHeader
	buf    []val.Value // length is 0 or a power of 2
	head   int         // index of logical element 0
	count  int         // active element count
	mask   int         // len(buf) - 1 (0 when len(buf) == 0)
	MaxLen int         // -1 for unbounded
	ver    uint64      // bumped on every structural mutation
	Mu     sync.Mutex
}

func nextDequePow2(n int) int {
	c := 8
	for c < n {
		c <<= 1
	}
	return c
}

func (d *pyDeque) growLocked(minCap int) {
	newCap := max(len(d.buf)*2, 8)
	for newCap < minCap {
		newCap <<= 1
	}
	newBuf := make([]val.Value, newCap)
	if d.count > 0 {
		first := len(d.buf) - d.head
		if first >= d.count {
			copy(newBuf, d.buf[d.head:d.head+d.count])
		} else {
			copy(newBuf, d.buf[d.head:])
			copy(newBuf[first:], d.buf[:d.count-first])
		}
	}
	d.buf = newBuf
	d.head = 0
	d.mask = newCap - 1
}

func (d *pyDeque) snapshotLocked() []val.Value {
	if d.count == 0 {
		return nil
	}
	out := make([]val.Value, d.count)
	first := len(d.buf) - d.head
	if first >= d.count {
		copy(out, d.buf[d.head:d.head+d.count])
	} else {
		copy(out, d.buf[d.head:])
		copy(out[first:], d.buf[:d.count-first])
	}
	return out
}

func (d *pyDeque) appendRightLocked(v val.Value) {
	if d.MaxLen == 0 {
		d.ver++
		return
	}
	if d.MaxLen > 0 && d.count >= d.MaxLen {
		if d.count == len(d.buf) {
			d.buf[d.head] = v
		} else {
			d.buf[d.head] = val.Value{}
			d.buf[(d.head+d.count)&d.mask] = v
		}
		d.head = (d.head + 1) & d.mask
		d.ver++
		return
	}
	if d.count == len(d.buf) {
		d.growLocked(d.count + 1)
	}
	d.buf[(d.head+d.count)&d.mask] = v
	d.count++
	d.ver++
}

func (d *pyDeque) appendLeftLocked(v val.Value) {
	if d.MaxLen == 0 {
		d.ver++
		return
	}
	if d.MaxLen > 0 && d.count >= d.MaxLen {
		if d.count == len(d.buf) {
			d.head = (d.head - 1) & d.mask
			d.buf[d.head] = v
		} else {
			tail := (d.head + d.count - 1) & d.mask
			d.buf[tail] = val.Value{}
			d.head = (d.head - 1) & d.mask
			d.buf[d.head] = v
		}
		d.ver++
		return
	}
	if d.count == len(d.buf) {
		d.growLocked(d.count + 1)
	}
	d.head = (d.head - 1) & d.mask
	d.buf[d.head] = v
	d.count++
	d.ver++
}

func (d *pyDeque) appendRightFast(v val.Value) {
	d.Mu.Lock()
	d.appendRightLocked(v)
	d.Mu.Unlock()
}

func (d *pyDeque) extendRightSlice(items []val.Value) {
	if len(items) == 0 {
		return
	}
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if d.MaxLen == 0 {
		d.ver++
		return
	}
	if d.MaxLen < 0 {
		need := d.count + len(items)
		if need > len(d.buf) {
			d.growLocked(need)
		}
		for _, v := range items {
			d.buf[(d.head+d.count)&d.mask] = v
			d.count++
		}
		d.ver++
		return
	}
	if len(items) >= d.MaxLen {
		tail := items[len(items)-d.MaxLen:]
		if len(d.buf) < d.MaxLen {
			newCap := nextDequePow2(d.MaxLen)
			d.buf = make([]val.Value, newCap)
			d.mask = newCap - 1
		} else {
			clear(d.buf)
		}
		copy(d.buf, tail)
		d.head = 0
		d.count = d.MaxLen
		d.ver++
		return
	}
	need := min(d.count+len(items), d.MaxLen)
	if need > len(d.buf) {
		d.growLocked(need)
	}
	for _, v := range items {
		d.appendRightLocked(v)
	}
}

func (d *pyDeque) extendLeftSlice(items []val.Value) {
	if len(items) == 0 {
		return
	}
	d.Mu.Lock()
	defer d.Mu.Unlock()
	if d.MaxLen == 0 {
		d.ver++
		return
	}
	need := d.count + len(items)
	if d.MaxLen > 0 && need > d.MaxLen {
		need = d.MaxLen
	}
	if need > len(d.buf) {
		d.growLocked(need)
	}
	for _, v := range items {
		d.appendLeftLocked(v)
	}
}

func (d *pyDeque) extendFromIterable(ctx *val.ExecContext, iterable val.Value) {
	if iterable.HeaderPtr() == &d.Header {
		// Self-extension: snapshot under lock, then extend outside lock.
		d.Mu.Lock()
		snap := d.snapshotLocked()
		d.Mu.Unlock()
		d.extendRightSlice(snap)
		return
	}
	if iterable.IsGenerator() {
		if ctx != nil && ctx.VM != nil {
			if vm, ok := ctx.VM.(*VM); ok && vm != nil {
				vm.drainGeneratorInto(ctx, iterable.AsGenerator(), d.appendRightFast)
				return
			}
		}
	}
	items := val.ExtractIterableItems(ctx, iterable)
	if ctx != nil && ctx.HasException() {
		return
	}
	d.extendRightSlice(items)
}

func (d *pyDeque) extendLeftFromIterable(ctx *val.ExecContext, iterable val.Value) {
	if iterable.HeaderPtr() == &d.Header {
		d.Mu.Lock()
		snap := d.snapshotLocked()
		d.Mu.Unlock()
		d.extendLeftSlice(snap)
		return
	}
	items := val.ExtractIterableItems(ctx, iterable)
	if ctx != nil && ctx.HasException() {
		return
	}
	d.extendLeftSlice(items)
}

func (d *pyDeque) appendRight(v val.Value) {
	d.Mu.Lock()
	d.appendRightLocked(v)
	d.Mu.Unlock()
}

func (d *pyDeque) appendLeft(v val.Value) {
	d.Mu.Lock()
	d.appendLeftLocked(v)
	d.Mu.Unlock()
}

func (d *pyDeque) popRight(ctx *val.ExecContext) val.Value {
	d.Mu.Lock()
	if d.count == 0 {
		d.Mu.Unlock()
		return val.RaiseIndexError(ctx, "pop from an empty deque")
	}
	idx := (d.head + d.count - 1) & d.mask
	v := d.buf[idx]
	d.buf[idx] = val.Value{}
	d.count--
	d.ver++
	d.Mu.Unlock()
	return v
}

func (d *pyDeque) popLeft(ctx *val.ExecContext) val.Value {
	d.Mu.Lock()
	if d.count == 0 {
		d.Mu.Unlock()
		return val.RaiseIndexError(ctx, "pop from an empty deque")
	}
	v := d.buf[d.head]
	d.buf[d.head] = val.Value{}
	d.head = (d.head + 1) & d.mask
	d.count--
	d.ver++
	d.Mu.Unlock()
	return v
}

func (d *pyDeque) removeAtLocked(idx int) {
	if idx == 0 {
		d.buf[d.head] = val.Value{}
		d.head = (d.head + 1) & d.mask
		d.count--
		d.ver++
		return
	}
	if idx == d.count-1 {
		tail := (d.head + d.count - 1) & d.mask
		d.buf[tail] = val.Value{}
		d.count--
		d.ver++
		return
	}
	if idx < d.count-1-idx {
		for i := idx; i > 0; i-- {
			d.buf[(d.head+i)&d.mask] = d.buf[(d.head+i-1)&d.mask]
		}
		d.buf[d.head] = val.Value{}
		d.head = (d.head + 1) & d.mask
	} else {
		for i := idx; i < d.count-1; i++ {
			d.buf[(d.head+i)&d.mask] = d.buf[(d.head+i+1)&d.mask]
		}
		tail := (d.head + d.count - 1) & d.mask
		d.buf[tail] = val.Value{}
	}
	d.count--
	d.ver++
}

func (d *pyDeque) rotate(n int) {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	l := d.count
	if l <= 1 {
		return
	}
	n %= l
	if n < 0 {
		n += l
	}
	if n == 0 {
		return
	}
	// When the ring buffer is completely full, rotating is O(1): shift head.
	if l == len(d.buf) {
		d.head = (d.head - n) & d.mask
		d.ver++
		return
	}
	if n <= l-n {
		// Move n elements from right to left.
		for range n {
			tail := (d.head + l - 1) & d.mask
			v := d.buf[tail]
			d.buf[tail] = val.Value{}
			d.head = (d.head - 1) & d.mask
			d.buf[d.head] = v
		}
	} else {
		// Move (l - n) elements from left to right.
		steps := l - n
		for range steps {
			v := d.buf[d.head]
			d.buf[d.head] = val.Value{}
			tail := (d.head + l) & d.mask
			d.buf[tail] = v
			d.head = (d.head + 1) & d.mask
		}
	}
	d.ver++
}

type pyTupleGetter struct {
	Header val.ObjectHeader
	Index  int
	Doc    val.Value
}

func initCollectionsTypes() {
	collectionsOnce.Do(func() {
		// 1. DequeType
		DequeType = val.MustNewType("deque", []*val.PyType{val.ObjectType}, nil)

		DequeType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			maxLen := -1
			posCount := len(args) - len(kwnames)
			if posCount > 1 && !args[1].IsNone() && args[1].IsInt64() {
				maxLen = int(args[1].AsInt64())
			}
			if len(kwnames) > 0 {
				kwStart := len(args) - len(kwnames)
				for i, kn := range kwnames {
					if kn.AsString() == "maxlen" {
						v := args[kwStart+i]
						if v.IsInt64() {
							maxLen = int(v.AsInt64())
						}
					}
				}
			}
			if maxLen < -1 {
				ctx.Raise(val.NewException(val.ValueErrorType, "maxlen must be non-negative"))
				return val.NoneVal()
			}
			deq := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				MaxLen: maxLen,
			}
			if posCount > 0 && !args[0].IsNone() {
				deq.extendFromIterable(ctx, args[0])
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
			}
			return val.ObjectVal(&deq.Header)
		}

		DequeType.Slots.SqLen = func(ctx *val.ExecContext, o val.Value) int64 {
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.Lock()
			n := deq.count
			deq.Mu.Unlock()
			return int64(n)
		}

		DequeType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.Lock()
			startVer := deq.ver
			deq.Mu.Unlock()
			idx := 0
			return val.NewIterValWithNext(func() (val.Value, bool) {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					if ctx != nil {
						ctx.Raise(val.NewException(val.RuntimeErrorType, "deque mutated during iteration"))
					} else {
						panic("RuntimeError: deque mutated during iteration")
					}
					return val.NoneVal(), false
				}
				if idx >= deq.count {
					deq.Mu.Unlock()
					return val.NoneVal(), false
				}
				v := deq.buf[(deq.head+idx)&deq.mask]
				idx++
				deq.Mu.Unlock()
				return v, true
			})
		}

		DequeType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			if name.AsString() == "maxlen" {
				deq := (*pyDeque)(unsafe.Pointer(obj.HeaderPtr()))
				if deq.MaxLen < 0 {
					return val.NoneVal()
				}
				return val.Int64Val(int64(deq.MaxLen))
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		DequeType.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			if name.AsString() == "maxlen" {
				ctx.Raise(val.NewException(val.AttributeErrorType, "attribute 'maxlen' of 'collections.deque' objects is not writable"))
				return val.NoneVal()
			}
			ctx.Raise(val.NewException(val.AttributeErrorType, "'collections.deque' object has no attribute '"+name.AsString()+"'"))
			return val.NoneVal()
		}

		DequeType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
			if !key.IsInt64() {
				ctx.Raise(val.NewException(val.TypeErrorType, "deque indices must be integers"))
				return val.NoneVal()
			}
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.Lock()
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += deq.count
			}
			if idx < 0 || idx >= deq.count {
				deq.Mu.Unlock()
				ctx.Raise(val.NewException(val.IndexErrorType, "deque index out of range"))
				return val.NoneVal()
			}
			v := deq.buf[(deq.head+idx)&deq.mask]
			deq.Mu.Unlock()
			return v
		}

		DequeType.Slots.MpAssSubscript = func(ctx *val.ExecContext, o, key, value val.Value) val.Value {
			if !key.IsInt64() {
				ctx.Raise(val.NewException(val.TypeErrorType, "deque indices must be integers"))
				return val.NoneVal()
			}
			deq := (*pyDeque)(unsafe.Pointer(o.HeaderPtr()))
			deq.Mu.Lock()
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += deq.count
			}
			if idx < 0 || idx >= deq.count {
				deq.Mu.Unlock()
				ctx.Raise(val.NewException(val.IndexErrorType, "deque index out of range"))
				return val.NoneVal()
			}
			if value.IsUnbound() {
				deq.removeAtLocked(idx)
				deq.Mu.Unlock()
				return val.NoneVal()
			}
			deq.buf[(deq.head+idx)&deq.mask] = value
			deq.Mu.Unlock()
			return val.NoneVal()
		}

		// Deque methods
		registerDequeMethod := func(name string, fn func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value) {
			DequeType.Dict.Set(val.InternString(name), val.NewBuiltinFuncVal(name, func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
				if len(args) == 0 {
					ctx.Raise(val.NewException(val.TypeErrorType, "descriptor '"+name+"' of 'deque' object needs an argument"))
					return val.NoneVal()
				}
				deq := (*pyDeque)(unsafe.Pointer(args[0].HeaderPtr()))
				return fn(ctx, deq, args[1:])
			}))
		}

		registerDequeMethod("append", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "append() takes exactly one argument"))
				return val.NoneVal()
			}
			deq.appendRight(args[0])
			return val.NoneVal()
		})

		registerDequeMethod("appendleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "appendleft() takes exactly one argument"))
				return val.NoneVal()
			}
			deq.appendLeft(args[0])
			return val.NoneVal()
		})

		registerDequeMethod("pop", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			return deq.popRight(ctx)
		})

		registerDequeMethod("popleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			return deq.popLeft(ctx)
		})

		registerDequeMethod("rotate", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			n := 1
			if len(args) > 0 && args[0].IsInt64() {
				n = int(args[0].AsInt64())
			}
			deq.rotate(n)
			return val.NoneVal()
		})

		registerDequeMethod("clear", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.Lock()
			if len(deq.buf) > 0 {
				clear(deq.buf)
			}
			deq.head = 0
			deq.count = 0
			deq.ver++
			deq.Mu.Unlock()
			return val.NoneVal()
		})

		registerDequeMethod("extend", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "extend() takes exactly one argument"))
				return val.NoneVal()
			}
			deq.extendFromIterable(ctx, args[0])
			return val.NoneVal()
		})

		registerDequeMethod("extendleft", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "extendleft() takes exactly one argument"))
				return val.NoneVal()
			}
			deq.extendLeftFromIterable(ctx, args[0])
			return val.NoneVal()
		})

		registerDequeMethod("copy", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.Lock()
			snap := deq.snapshotLocked()
			maxLen := deq.MaxLen
			deq.Mu.Unlock()
			newDeq := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				MaxLen: maxLen,
			}
			if len(snap) > 0 {
				newCap := nextDequePow2(len(snap))
				newDeq.buf = make([]val.Value, newCap)
				copy(newDeq.buf, snap)
				newDeq.count = len(snap)
				newDeq.mask = newCap - 1
			}
			return val.ObjectVal(&newDeq.Header)
		})

		registerDequeMethod("insert", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 2 || !args[0].IsInt64() {
				ctx.Raise(val.NewException(val.TypeErrorType, "insert() takes an integer index and a value"))
				return val.NoneVal()
			}
			idx := int(args[0].AsInt64())
			v := args[1]
			deq.Mu.Lock()
			if deq.MaxLen >= 0 && deq.count >= deq.MaxLen {
				deq.Mu.Unlock()
				ctx.Raise(val.NewException(val.IndexErrorType, "deque already at its maximum size"))
				return val.NoneVal()
			}
			if idx < 0 {
				idx += deq.count
				if idx < 0 {
					idx = 0
				}
			}
			if idx >= deq.count {
				deq.appendRightLocked(v)
				deq.Mu.Unlock()
				return val.NoneVal()
			}
			if idx == 0 {
				deq.appendLeftLocked(v)
				deq.Mu.Unlock()
				return val.NoneVal()
			}
			if deq.count == len(deq.buf) {
				deq.growLocked(deq.count + 1)
			}
			if idx <= deq.count-idx {
				deq.head = (deq.head - 1) & deq.mask
				for i := range idx {
					deq.buf[(deq.head+i)&deq.mask] = deq.buf[(deq.head+i+1)&deq.mask]
				}
			} else {
				for i := deq.count; i > idx; i-- {
					deq.buf[(deq.head+i)&deq.mask] = deq.buf[(deq.head+i-1)&deq.mask]
				}
			}
			deq.buf[(deq.head+idx)&deq.mask] = v
			deq.count++
			deq.ver++
			deq.Mu.Unlock()
			return val.NoneVal()
		})

		registerDequeMethod("remove", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "remove() takes exactly one argument"))
				return val.NoneVal()
			}
			target := args[0]
			deq.Mu.Lock()
			startVer := deq.ver
			n := deq.count
			deq.Mu.Unlock()
			for i := range n {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					ctx.Raise(val.NewException(val.IndexErrorType, "deque mutated during remove()."))
					return val.NoneVal()
				}
				it := deq.buf[(deq.head+i)&deq.mask]
				deq.Mu.Unlock()
				eq := val.RichCompare(ctx, it, target, val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if eq.IsTruthy() {
					deq.Mu.Lock()
					if deq.ver != startVer {
						deq.Mu.Unlock()
						ctx.Raise(val.NewException(val.IndexErrorType, "deque mutated during remove()."))
						return val.NoneVal()
					}
					deq.removeAtLocked(i)
					deq.Mu.Unlock()
					return val.NoneVal()
				}
			}
			ctx.Raise(val.NewException(val.ValueErrorType, "deque.remove(x): x not in deque"))
			return val.NoneVal()
		})

		registerDequeMethod("index", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "index() takes at least 1 argument"))
				return val.NoneVal()
			}
			target := args[0]
			deq.Mu.Lock()
			startVer := deq.ver
			n := deq.count
			deq.Mu.Unlock()
			start, stop := 0, n
			if len(args) >= 2 && args[1].IsInt64() {
				start = int(args[1].AsInt64())
				if start < 0 {
					start += n
					if start < 0 {
						start = 0
					}
				}
			}
			if len(args) >= 3 && args[2].IsInt64() {
				stop = int(args[2].AsInt64())
				if stop < 0 {
					stop += n
					if stop < 0 {
						stop = 0
					}
				}
				if stop > n {
					stop = n
				}
			}
			for i := start; i < stop; i++ {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					ctx.Raise(val.NewException(val.RuntimeErrorType, "deque mutated during iteration"))
					return val.NoneVal()
				}
				it := deq.buf[(deq.head+i)&deq.mask]
				deq.Mu.Unlock()
				eq := val.RichCompare(ctx, it, target, val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if eq.IsTruthy() {
					return val.Int64Val(int64(i))
				}
			}
			ctx.Raise(val.NewException(val.ValueErrorType, target.String()+" is not in deque"))
			return val.NoneVal()
		})

		registerDequeMethod("count", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "count() takes exactly one argument"))
				return val.NoneVal()
			}
			target := args[0]
			deq.Mu.Lock()
			startVer := deq.ver
			n := deq.count
			deq.Mu.Unlock()
			cnt := int64(0)
			for i := range n {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					ctx.Raise(val.NewException(val.RuntimeErrorType, "deque mutated during iteration"))
					return val.NoneVal()
				}
				it := deq.buf[(deq.head+i)&deq.mask]
				deq.Mu.Unlock()
				eq := val.RichCompare(ctx, it, target, val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if eq.IsTruthy() {
					cnt++
				}
			}
			return val.Int64Val(cnt)
		})

		registerDequeMethod("reverse", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.Lock()
			for i, j := 0, deq.count-1; i < j; i, j = i+1, j-1 {
				pi := (deq.head + i) & deq.mask
				pj := (deq.head + j) & deq.mask
				deq.buf[pi], deq.buf[pj] = deq.buf[pj], deq.buf[pi]
			}
			deq.ver++
			deq.Mu.Unlock()
			return val.NoneVal()
		})

		registerDequeMethod("__reversed__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			deq.Mu.Lock()
			startVer := deq.ver
			idx := deq.count - 1
			deq.Mu.Unlock()
			return val.NewIterValWithNext(func() (val.Value, bool) {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					if ctx != nil {
						ctx.Raise(val.NewException(val.RuntimeErrorType, "deque mutated during iteration"))
					} else {
						panic("RuntimeError: deque mutated during iteration")
					}
					return val.NoneVal(), false
				}
				if idx < 0 {
					deq.Mu.Unlock()
					return val.NoneVal(), false
				}
				v := deq.buf[(deq.head+idx)&deq.mask]
				idx--
				deq.Mu.Unlock()
				return v, true
			})
		})

		registerDequeMethod("__contains__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				return val.BoolVal(false)
			}
			target := args[0]
			deq.Mu.Lock()
			startVer := deq.ver
			n := deq.count
			deq.Mu.Unlock()
			for i := range n {
				deq.Mu.Lock()
				if deq.ver != startVer {
					deq.Mu.Unlock()
					ctx.Raise(val.NewException(val.RuntimeErrorType, "deque mutated during iteration"))
					return val.NoneVal()
				}
				it := deq.buf[(deq.head+i)&deq.mask]
				deq.Mu.Unlock()
				eq := val.RichCompare(ctx, it, target, val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if eq.IsTruthy() {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		})

		registerDequeMethod("__delitem__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 || !args[0].IsInt64() {
				ctx.Raise(val.NewException(val.TypeErrorType, "deque indices must be integers"))
				return val.NoneVal()
			}
			deq.Mu.Lock()
			idx := int(args[0].AsInt64())
			if idx < 0 {
				idx += deq.count
			}
			if idx < 0 || idx >= deq.count {
				deq.Mu.Unlock()
				ctx.Raise(val.NewException(val.IndexErrorType, "deque index out of range"))
				return val.NoneVal()
			}
			deq.removeAtLocked(idx)
			deq.Mu.Unlock()
			return val.NoneVal()
		})

		registerDequeMethod("__eq__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 || args[0].Type() != DequeType {
				return val.NotImplementedVal()
			}
			other := (*pyDeque)(unsafe.Pointer(args[0].HeaderPtr()))
			if deq == other {
				return val.BoolVal(true)
			}
			deq.Mu.Lock()
			a := deq.snapshotLocked()
			deq.Mu.Unlock()
			other.Mu.Lock()
			b := other.snapshotLocked()
			other.Mu.Unlock()
			if len(a) != len(b) {
				return val.BoolVal(false)
			}
			for i := range a {
				eq := val.RichCompare(ctx, a[i], b[i], val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if !eq.IsTruthy() {
					return val.BoolVal(false)
				}
			}
			return val.BoolVal(true)
		})

		registerDequeMethod("__ne__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 || args[0].Type() != DequeType {
				return val.NotImplementedVal()
			}
			other := (*pyDeque)(unsafe.Pointer(args[0].HeaderPtr()))
			if deq == other {
				return val.BoolVal(false)
			}
			deq.Mu.Lock()
			a := deq.snapshotLocked()
			deq.Mu.Unlock()
			other.Mu.Lock()
			b := other.snapshotLocked()
			other.Mu.Unlock()
			if len(a) != len(b) {
				return val.BoolVal(true)
			}
			for i := range a {
				eq := val.RichCompare(ctx, a[i], b[i], val.CmpEQ)
				if ctx != nil && ctx.HasException() {
					return val.NoneVal()
				}
				if !eq.IsTruthy() {
					return val.BoolVal(true)
				}
			}
			return val.BoolVal(false)
		})

		registerDequeMethod("__add__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 || args[0].Type() != DequeType {
				ctx.Raise(val.NewException(val.TypeErrorType, "can only concatenate deque to deque"))
				return val.NoneVal()
			}
			other := (*pyDeque)(unsafe.Pointer(args[0].HeaderPtr()))
			deq.Mu.Lock()
			a := deq.snapshotLocked()
			maxLen := deq.MaxLen
			deq.Mu.Unlock()
			var b []val.Value
			if other == deq {
				b = a
			} else {
				other.Mu.Lock()
				b = other.snapshotLocked()
				other.Mu.Unlock()
			}
			res := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				MaxLen: maxLen,
			}
			res.extendRightSlice(a)
			res.extendRightSlice(b)
			return val.ObjectVal(&res.Header)
		})

		registerDequeMethod("__iadd__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__iadd__() takes exactly one argument"))
				return val.NoneVal()
			}
			deq.extendFromIterable(ctx, args[0])
			return val.ObjectVal(&deq.Header)
		})

		registerDequeMethod("__mul__", func(ctx *val.ExecContext, deq *pyDeque, args []val.Value) val.Value {
			if len(args) < 1 || !args[0].IsInt64() {
				return val.NotImplementedVal()
			}
			n := int(args[0].AsInt64())
			deq.Mu.Lock()
			snap := deq.snapshotLocked()
			maxLen := deq.MaxLen
			deq.Mu.Unlock()
			res := &pyDeque{
				Header: val.ObjectHeader{Kind: val.KindNative, Type: DequeType},
				MaxLen: maxLen,
			}
			if n > 0 && len(snap) > 0 && maxLen != 0 {
				for range n {
					res.extendRightSlice(snap)
				}
			}
			return val.ObjectVal(&res.Header)
		})

		// 2. TupleGetterType
		TupleGetterType = val.MustNewType("_tuplegetter", []*val.PyType{val.ObjectType}, nil)
		TupleGetterType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 || !args[0].IsInt64() {
				ctx.Raise(val.NewException(val.TypeErrorType, "_tuplegetter index must be an integer"))
				return val.NoneVal()
			}
			idx := int(args[0].AsInt64())
			doc := val.NoneVal()
			if len(args) > 1 {
				doc = args[1]
			}
			getter := &pyTupleGetter{
				Header: val.ObjectHeader{
					Kind:  val.KindNative,
					Flags: val.FlagDataDescriptor,
					Type:  TupleGetterType,
				},
				Index: idx,
				Doc:   doc,
			}
			return val.ObjectVal((*val.ObjectHeader)(unsafe.Pointer(&getter.Header)))
		}

		TupleGetterType.Slots.TpDescrGet = func(ctx *val.ExecContext, descr, obj val.Value, owner *val.PyType) val.Value {
			if obj.IsNone() || obj.IsUnbound() {
				return descr
			}
			getter := (*pyTupleGetter)(unsafe.Pointer(descr.HeaderPtr()))
			if obj.IsTuple() {
				t := obj.AsTuple()
				if getter.Index < 0 || getter.Index >= len(t.Items) {
					ctx.Raise(val.NewException(val.IndexErrorType, "tuple index out of range"))
					return val.NoneVal()
				}
				return t.Items[getter.Index]
			}
			if sqItem := obj.Type().Slots.SqItem; sqItem != nil {
				return sqItem(ctx, obj, val.Int64Val(int64(getter.Index)))
			}
			ctx.Raise(val.NewException(val.TypeErrorType, "tuplegetter applied to non-tuple"))
			return val.NoneVal()
		}

		TupleGetterType.Slots.TpGetAttr = func(ctx *val.ExecContext, obj, name val.Value) val.Value {
			getter := (*pyTupleGetter)(unsafe.Pointer(obj.HeaderPtr()))
			if name.AsString() == "__doc__" {
				return getter.Doc
			}
			return val.ObjectGetAttr(ctx, obj, name)
		}

		TupleGetterType.Slots.TpSetAttr = func(ctx *val.ExecContext, obj, name, value val.Value) val.Value {
			getter := (*pyTupleGetter)(unsafe.Pointer(obj.HeaderPtr()))
			if name.AsString() == "__doc__" {
				getter.Doc = value
				return val.NoneVal()
			}
			ctx.Raise(val.NewException(val.AttributeErrorType, fmt.Sprintf("'_tuplegetter' object has no attribute '%s'", name.AsString())))
			return val.NoneVal()
		}

		DequeType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("deque.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			deq := (*pyDeque)(val.AsPointer(args[0]))
			deq.Mu.Lock()
			items := deq.snapshotLocked()
			maxLen := deq.MaxLen
			deq.Mu.Unlock()
			var sb strings.Builder
			sb.WriteString("deque([")
			for i, it := range items {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(val.PyRepr(ctx, it))
			}
			sb.WriteString("]")
			if maxLen >= 0 {
				sb.WriteString(", maxlen=" + itoa(maxLen))
			}
			sb.WriteString(")")
			return val.NewStringVal(sb.String())
		}))
		DequeType.InvalidateVersion()

		// 3. DefaultDictType (subclass of dict)
		DefaultDictType = val.MustNewType("defaultdict", []*val.PyType{val.DictType}, nil)
		DefaultDictType.Dict.Set(val.InternString("default_factory"), val.NoneVal())
		getDefaultFactory := func(d *val.PyDict) (val.Value, bool) {
			if d.Attrs != nil {
				if f, ok := d.Attrs.Get(nameDefaultFactory); ok {
					return f, true
				}
			}
			return d.Get(nameDefaultFactory)
		}
		defaultDictInit := func(ctx *val.ExecContext, dVal val.Value, args []val.Value, kwnames []val.Value) {
			d := dVal.AsDict()
			defaultFactory := val.NoneVal()
			posCount := len(args) - len(kwnames)
			var dictArgs []val.Value
			if posCount > 0 {
				defaultFactory = args[0]
				dictArgs = append(dictArgs, args[1:]...)
			} else if len(kwnames) > 0 {
				dictArgs = append(dictArgs, args...)
			}
			if d.Attrs == nil {
				d.Attrs = val.NewDictVal(4).AsDict()
			}
			d.Attrs.Set(nameDefaultFactory, defaultFactory)
			if len(dictArgs) > 0 {
				initFn, _, ok := val.DictType.LookupMRO(val.DunderInit)
				if ok && initFn.IsBuiltinFunc() {
					callArgs := make([]val.Value, 1+len(dictArgs))
					callArgs[0] = dVal
					copy(callArgs[1:], dictArgs)
					initFn.AsBuiltinFunc().Fn(ctx, initFn, callArgs, kwnames)
				}
			}
		}
		DefaultDictType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			dVal := val.NewDictVal(16)
			d := dVal.AsDict()
			if callable.IsType() {
				d.Header.Type = callable.AsType()
			} else {
				d.Header.Type = DefaultDictType
			}
			defaultDictInit(ctx, dVal, args, kwnames)
			return dVal
		}
		DefaultDictType.Dict.Set(val.DunderInit, val.NewBuiltinFuncVal("__init__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 1 {
				ctx.Raise(val.NewException(val.TypeErrorType, "descriptor '__init__' of 'defaultdict' object needs an argument"))
				return val.NoneVal()
			}
			defaultDictInit(ctx, args[0], args[1:], kwnames)
			return val.NoneVal()
		}))

		DefaultDictType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
			d := o.AsDict()
			if v, ok := d.Get(key); ok {
				return v
			}
			if missingFn, _, ok := o.Type().LookupMRO(nameMissing); ok {
				return ctx.Invoke2(missingFn, o, key)
			}
			ctx.Raise(val.NewException(val.KeyErrorType, key.String()))
			return val.NoneVal()
		}

		DefaultDictType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("defaultdict.__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			d := args[0].AsDict()
			factory, _ := getDefaultFactory(d)
			var sb strings.Builder
			sb.WriteString("defaultdict(" + val.PyRepr(ctx, factory) + ", {")
			first := true
			for _, e := range d.ActiveEntries() {
				if !e.Active || (e.Key.IsAnyString() && e.Key.AsString() == "default_factory") {
					continue
				}
				if !first {
					sb.WriteString(", ")
				}
				first = false
				sb.WriteString(val.PyRepr(ctx, e.Key) + ": " + val.PyRepr(ctx, e.Value))
			}
			sb.WriteString("})")
			return val.NewStringVal(sb.String())
		}))
		DefaultDictType.Dict.Set(val.InternString("__missing__"), val.NewBuiltinFuncVal("__missing__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) < 2 {
				ctx.Raise(val.NewException(val.TypeErrorType, "__missing__() takes 1 argument"))
				return val.NoneVal()
			}
			self := args[0]
			key := args[1]
			d := self.AsDict()
			factory, ok := getDefaultFactory(d)
			if !ok || factory.IsNone() {
				ctx.Raise(val.NewException(val.KeyErrorType, key.String()))
				return val.NoneVal()
			}
			newVal := ctx.Invoke0(factory)
			d.Set(key, newVal)
			return newVal
		}))
		DefaultDictType.InvalidateVersion()
	})
}

func (e *Engine) createCollectionsModule() val.Value {
	initCollectionsTypes()

	modVal := val.NewModuleVal("_collections", "")
	mod := modVal.AsModule()

	mod.Dict.Set(val.InternString("deque"), val.TypeVal(DequeType))
	mod.Dict.Set(val.InternString("defaultdict"), val.TypeVal(DefaultDictType))
	mod.Dict.Set(val.InternString("_tuplegetter"), val.TypeVal(TupleGetterType))

	// _count_elements(mapping, iterable)
	mod.Dict.Set(val.InternString("_count_elements"), val.NewBuiltinFuncVal("_count_elements", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			ctx.Raise(val.NewException(val.TypeErrorType, "_count_elements() takes 2 arguments"))
			return val.NoneVal()
		}
		mapping := args[0]
		iterable := args[1]
		elems := val.ExtractIterableItems(ctx, iterable)
		if ctx != nil && ctx.HasException() {
			return val.NoneVal()
		}

		if mapping.IsDict() {
			d := mapping.AsDict()
			for _, elem := range elems {
				curCount, found := d.Get(elem)
				if !found || !curCount.IsInt64() {
					d.Set(elem, val.Int64Val(1))
				} else {
					d.Set(elem, val.Int64Val(curCount.AsInt64()+1))
				}
			}
			return val.NoneVal()
		}

		for _, elem := range elems {
			curVal := val.Int64Val(0)
			getFn, _, okGet := mapping.Type().LookupMRO(nameGet)
			if okGet {
				curVal = ctx.Invoke3(getFn, mapping, elem, val.Int64Val(0))
			}
			newVal := val.Int64Val(curVal.AsInt64() + 1)
			val.SetItemCtx(ctx, mapping, elem, newVal)
		}

		return val.NoneVal()
	}))

	return modVal
}
