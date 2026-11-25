package compiler

import (
	"bytes"
	"math"
	"slices"
	"strings"

	"github.com/servusdei2018/fython/pkg/val"
)

const (
	// maxFoldSeqBytes caps compile-time str/bytes concatenation and repetition
	// to avoid bloating bytecode constants or memory at compile time.
	maxFoldSeqBytes = 4096
	// maxFoldTupleLen caps compile-time constant tuple hoisting length.
	maxFoldTupleLen = 256
)

// evalConstExpr evaluates a pure compile-time constant expression without side
// effects. Returns (v, true) if id folds safely to a constant Value.
func (c *Compiler) evalConstExpr(id ExprID) (val.Value, bool) {
	if id == ExprNone {
		return val.Value{}, false
	}
	expr := c.arena.GetExpr(id)
	if expr == nil {
		return val.Value{}, false
	}
	switch expr.Kind {
	case ExprKindInt:
		return val.Int64Val(c.arena.IntConsts[expr.Extra]), true
	case ExprKindFloat:
		return val.Float64Val(c.arena.FloatConsts[expr.Extra]), true
	case ExprKindImag:
		return val.NewComplexVal(0, c.arena.FloatConsts[expr.Extra]), true
	case ExprKindBool:
		return val.BoolVal(expr.Op != 0), true
	case ExprKindNoneLit:
		return val.NoneVal(), true
	case ExprKindString:
		rawStr := string(c.src[expr.Start:expr.End])
		if expr.Op&1 == 0 && isBytesToken(rawStr) {
			return val.NewBytesVal(parseBytesLiteral(rawStr)), true
		}
		var parsedStr string
		if expr.Op&1 != 0 {
			parsedStr = decodeFStringPart(rawStr, expr.Op&2 != 0)
		} else {
			parsedStr = parseStringLiteral(rawStr)
		}
		return val.NewStringVal(parsedStr), true
	case ExprKindUnary:
		return c.evalConstUnary(expr)
	case ExprKindBinary:
		return c.evalConstBinary(expr)
	case ExprKindTuple:
		slice := c.arena.ExprSlices[expr.Extra]
		return c.tryBuildConstTuple(c.arena.GetExprSlice(slice))
	default:
		return val.Value{}, false
	}
}

func (c *Compiler) evalConstUnary(expr *ASTExpr) (val.Value, bool) {
	v, ok := c.evalConstExpr(expr.Left)
	if !ok {
		return val.Value{}, false
	}
	switch TokenType(expr.Op) {
	case TOKEN_MINUS:
		switch v.Kind() {
		case val.KindInt64:
			n := v.AsInt64()
			if n == math.MinInt64 {
				return val.Value{}, false
			}
			return val.Int64Val(-n), true
		case val.KindBool:
			if v.AsBool() {
				return val.Int64Val(-1), true
			}
			return val.Int64Val(0), true
		case val.KindFloat64:
			// Negating float64 preserves IEEE-754 sign bit (0.0 -> -0.0).
			return val.Float64Val(-v.AsFloat64()), true
		default:
			return val.Value{}, false
		}
	case TOKEN_PLUS:
		switch v.Kind() {
		case val.KindInt64:
			return v, true
		case val.KindBool:
			if v.AsBool() {
				return val.Int64Val(1), true
			}
			return val.Int64Val(0), true
		case val.KindFloat64:
			return v, true
		default:
			return val.Value{}, false
		}
	case TOKEN_TILDE:
		switch v.Kind() {
		case val.KindInt64:
			return val.Int64Val(^v.AsInt64()), true
		case val.KindBool:
			if v.AsBool() {
				return val.Int64Val(-2), true
			}
			return val.Int64Val(-1), true
		default:
			return val.Value{}, false
		}
	case TOKEN_NOT:
		switch v.Kind() {
		case val.KindNone, val.KindBool, val.KindInt64, val.KindFloat64,
			val.KindInlineStr, val.KindString, val.KindBytes, val.KindTuple:
			return val.BoolVal(!v.IsTruthy()), true
		default:
			return val.Value{}, false
		}
	default:
		return val.Value{}, false
	}
}

func asConstInt64(v val.Value) (int64, bool) {
	switch v.Kind() {
	case val.KindInt64:
		return v.AsInt64(), true
	case val.KindBool:
		if v.AsBool() {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func asConstFloat64(v val.Value) (float64, bool) {
	switch v.Kind() {
	case val.KindFloat64:
		return v.AsFloat64(), true
	case val.KindInt64:
		return float64(v.AsInt64()), true
	case val.KindBool:
		if v.AsBool() {
			return 1.0, true
		}
		return 0.0, true
	default:
		return 0, false
	}
}

func (c *Compiler) evalConstBinary(expr *ASTExpr) (val.Value, bool) {
	op := TokenType(expr.Op)
	if op == TOKEN_AND || op == TOKEN_OR || op == TOKEN_AT || op == TOKEN_SLASH {
		// Leave short-circuit ops, matmul, and true-div to runtime (or handle true-div only if needed).
		return val.Value{}, false
	}
	lv, lok := c.evalConstExpr(expr.Left)
	if !lok {
		return val.Value{}, false
	}
	rv, rok := c.evalConstExpr(expr.Right)
	if !rok {
		return val.Value{}, false
	}

	// String concatenation & repetition (including adjacent string literals).
	if lv.IsAnyString() && rv.IsAnyString() && op == TOKEN_PLUS {
		ls, rs := lv.AsString(), rv.AsString()
		if len(ls)+len(rs) > maxFoldSeqBytes {
			return val.Value{}, false
		}
		return val.NewStringVal(ls + rs), true
	}
	if op == TOKEN_STAR {
		if lv.IsAnyString() {
			if n, ok := asConstInt64(rv); ok {
				return foldRepeatStr(lv.AsString(), n)
			}
		}
		if rv.IsAnyString() {
			if n, ok := asConstInt64(lv); ok {
				return foldRepeatStr(rv.AsString(), n)
			}
		}
		if lv.IsBytes() {
			if n, ok := asConstInt64(rv); ok {
				return foldRepeatBytes(lv.AsBytes().Data, n)
			}
		}
		if rv.IsBytes() {
			if n, ok := asConstInt64(lv); ok {
				return foldRepeatBytes(rv.AsBytes().Data, n)
			}
		}
	}

	// Bytes concatenation (including adjacent bytes literals).
	if lv.IsBytes() && rv.IsBytes() && op == TOKEN_PLUS {
		lb, rb := lv.AsBytes().Data, rv.AsBytes().Data
		if len(lb)+len(rb) > maxFoldSeqBytes {
			return val.Value{}, false
		}
		out := make([]byte, len(lb)+len(rb))
		copy(out, lb)
		copy(out[len(lb):], rb)
		return val.NewBytesVal(out), true
	}

	// Integer / bool arithmetic and bitwise ops (bool promotes to int64 per Python semantics).
	if li, lok := asConstInt64(lv); lok {
		if ri, rok := asConstInt64(rv); rok {
			return foldInt64Binary(op, li, ri)
		}
	}

	// Float arithmetic (when at least one side is float64 and the other is int64/bool/float64).
	if lv.IsFloat64() || rv.IsFloat64() {
		lf, lok := asConstFloat64(lv)
		rf, rok := asConstFloat64(rv)
		if lok && rok {
			return foldFloat64Binary(op, lf, rf)
		}
	}

	return val.Value{}, false
}

func foldRepeatStr(s string, n int64) (val.Value, bool) {
	if n <= 0 || len(s) == 0 {
		return val.NewStringVal(""), true
	}
	if n > maxFoldSeqBytes || int64(len(s))*n > maxFoldSeqBytes {
		return val.Value{}, false
	}
	return val.NewStringVal(strings.Repeat(s, int(n))), true
}

func foldRepeatBytes(b []byte, n int64) (val.Value, bool) {
	if n <= 0 || len(b) == 0 {
		return val.NewBytesVal(nil), true
	}
	if n > maxFoldSeqBytes || int64(len(b))*n > maxFoldSeqBytes {
		return val.Value{}, false
	}
	return val.NewBytesVal(bytes.Repeat(b, int(n))), true
}

func foldInt64Binary(op TokenType, a, b int64) (val.Value, bool) {
	switch op {
	case TOKEN_PLUS:
		res := a + b
		if (b > 0 && res < a) || (b < 0 && res > a) {
			return val.Value{}, false
		}
		return val.Int64Val(res), true
	case TOKEN_MINUS:
		res := a - b
		if (b < 0 && res < a) || (b > 0 && res > a) {
			return val.Value{}, false
		}
		return val.Int64Val(res), true
	case TOKEN_STAR:
		if a == 0 || b == 0 {
			return val.Int64Val(0), true
		}
		if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
			return val.Value{}, false
		}
		res := a * b
		if res/b != a {
			return val.Value{}, false
		}
		return val.Int64Val(res), true
	case TOKEN_SLASHSLASH:
		if b == 0 || (a == math.MinInt64 && b == -1) {
			return val.Value{}, false
		}
		q := a / b
		r := a % b
		if r != 0 && (a < 0) != (b < 0) {
			q--
		}
		return val.Int64Val(q), true
	case TOKEN_PERCENT:
		if b == 0 || (a == math.MinInt64 && b == -1) {
			return val.Value{}, false
		}
		r := a % b
		if r != 0 && (a < 0) != (b < 0) {
			r += b
		}
		return val.Int64Val(r), true
	case TOKEN_PIPE:
		return val.Int64Val(a | b), true
	case TOKEN_AMP:
		return val.Int64Val(a & b), true
	case TOKEN_CARET:
		return val.Int64Val(a ^ b), true
	case TOKEN_LSHIFT:
		if b < 0 || b >= 63 {
			return val.Value{}, false
		}
		if a == 0 || b == 0 {
			return val.Int64Val(a), true
		}
		res := a << uint(b)
		if (res >> uint(b)) != a {
			return val.Value{}, false
		}
		return val.Int64Val(res), true
	case TOKEN_RSHIFT:
		if b < 0 {
			return val.Value{}, false
		}
		if b >= 63 {
			if a < 0 {
				return val.Int64Val(-1), true
			}
			return val.Int64Val(0), true
		}
		return val.Int64Val(a >> uint(b)), true
	case TOKEN_STARSTAR:
		if b < 0 || b > 62 {
			return val.Value{}, false
		}
		res, ok := checkedInt64Pow(a, b)
		if !ok {
			return val.Value{}, false
		}
		return val.Int64Val(res), true
	default:
		return val.Value{}, false
	}
}

func checkedInt64Pow(base, exp int64) (int64, bool) {
	if exp == 0 {
		return 1, true
	}
	if base == 0 {
		return 0, true
	}
	if base == 1 {
		return 1, true
	}
	if base == -1 {
		if exp&1 == 0 {
			return 1, true
		}
		return -1, true
	}
	result := int64(1)
	b := base
	e := exp
	for e > 0 {
		if e&1 != 0 {
			if (result == math.MinInt64 && b == -1) || (b == math.MinInt64 && result == -1) {
				return 0, false
			}
			next := result * b
			if next/b != result {
				return 0, false
			}
			result = next
		}
		e >>= 1
		if e > 0 {
			if b == math.MinInt64 {
				return 0, false
			}
			nextB := b * b
			if nextB/b != b {
				return 0, false
			}
			b = nextB
		}
	}
	return result, true
}

func foldFloat64Binary(op TokenType, a, b float64) (val.Value, bool) {
	if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return val.Value{}, false
	}
	var res float64
	switch op {
	case TOKEN_PLUS:
		res = a + b
	case TOKEN_MINUS:
		res = a - b
	case TOKEN_STAR:
		res = a * b
	case TOKEN_SLASHSLASH:
		if b == 0 {
			return val.Value{}, false
		}
		res = math.Floor(a / b)
	case TOKEN_PERCENT:
		if b == 0 {
			return val.Value{}, false
		}
		rem := math.Mod(a, b)
		if rem != 0 && (rem < 0) != (b < 0) {
			rem += b
		}
		res = rem
	case TOKEN_STARSTAR:
		if a < 0 && math.Trunc(b) != b {
			// Negative base with fractional exponent produces complex in Python.
			return val.Value{}, false
		}
		if a == 0 && b < 0 {
			return val.Value{}, false
		}
		res = math.Pow(a, b)
	default:
		return val.Value{}, false
	}
	if math.IsNaN(res) || math.IsInf(res, 0) {
		return val.Value{}, false
	}
	return val.Float64Val(res), true
}

// tryBuildConstTuple evaluates all elements of a display into a constant PyTuple
// if none are starred and all fold to compile-time constants.
func (c *Compiler) tryBuildConstTuple(elemIDs []ExprID) (val.Value, bool) {
	if len(elemIDs) > maxFoldTupleLen {
		return val.Value{}, false
	}
	if len(elemIDs) == 0 {
		return val.NewTupleVal(), true
	}
	items := make([]val.Value, len(elemIDs))
	for i, id := range elemIDs {
		e := c.arena.GetExpr(id)
		if e == nil || e.Kind == ExprKindStarred {
			return val.Value{}, false
		}
		v, ok := c.evalConstExpr(id)
		if !ok {
			return val.Value{}, false
		}
		items[i] = v
	}
	return val.NewTupleVal(items...), true
}

func isHashableConst(v val.Value) bool {
	switch v.Kind() {
	case val.KindNone, val.KindBool, val.KindInt64, val.KindInlineStr, val.KindString, val.KindBytes:
		return true
	case val.KindFloat64:
		return !math.IsNaN(v.AsFloat64())
	case val.KindTuple:
		for _, item := range v.AsTuple().Items {
			if !isHashableConst(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// tryBuildConstSet builds a constant PySet for O(1) membership tests when all
// elements are non-starred hashable compile-time constants.
func (c *Compiler) tryBuildConstSet(elemIDs []ExprID) (val.Value, bool) {
	if len(elemIDs) > maxFoldTupleLen {
		return val.Value{}, false
	}
	items := make([]val.Value, len(elemIDs))
	for i, id := range elemIDs {
		e := c.arena.GetExpr(id)
		if e == nil || e.Kind == ExprKindStarred {
			return val.Value{}, false
		}
		v, ok := c.evalConstExpr(id)
		if !ok || !isHashableConst(v) {
			return val.Value{}, false
		}
		items[i] = v
	}
	return val.NewSetVal(items...), true
}

// stmtsHaveScopeDecls reports whether any statement in stmts contains scope
// declarations, variable bindings, or generator/coroutine expressions (yield,
// yield from, await) that could affect symbol resolution, UnboundLocalError
// semantics, or CO_GENERATOR/CO_COROUTINE flags.
func (c *Compiler) stmtsHaveScopeDecls(stmts []StmtID) bool {
	return slices.ContainsFunc(stmts, c.stmtHasScopeDecls)
}

func (c *Compiler) stmtHasScopeDecls(sID StmtID) bool {
	stmt := c.arena.GetStmt(sID)
	if stmt == nil {
		return false
	}
	switch stmt.Kind {
	case StmtKindGlobal, StmtKindNonlocal,
		StmtKindAssign, StmtKindAugAssign, StmtKindAnnAssign, StmtKindDelete,
		StmtKindImport, StmtKindImportFrom,
		StmtKindFuncDef, StmtKindAsyncFuncDef, StmtKindClassDef,
		StmtKindTypeAlias, StmtKindMatch:
		return true
	case StmtKindExpr, StmtKindReturn, StmtKindRaise, StmtKindAssert:
		return c.exprHasScopeOrSuspend(stmt.Expr1) || c.exprHasScopeOrSuspend(stmt.Expr2)
	case StmtKindIf:
		if c.exprHasScopeOrSuspend(stmt.Expr1) {
			return true
		}
		if c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Body)) {
			return true
		}
		elifsStart := stmt.Extra >> 16
		elifsCount := stmt.Extra & 0xFFFF
		for i := range elifsCount {
			elif := &c.arena.Elifs[elifsStart+i]
			if c.exprHasScopeOrSuspend(elif.Cond) {
				return true
			}
			if c.stmtsHaveScopeDecls(c.arena.StmtIDs[elif.Body.Start : elif.Body.Start+elif.Body.Count]) {
				return true
			}
		}
		if stmt.Else != 0 && c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Else)) {
			return true
		}
		return false
	case StmtKindWhile:
		if c.exprHasScopeOrSuspend(stmt.Expr1) {
			return true
		}
		if c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Body)) {
			return true
		}
		if stmt.Else != 0 && c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Else)) {
			return true
		}
		return false
	case StmtKindFor, StmtKindAsyncFor, StmtKindWith, StmtKindAsyncWith:
		// Loops and with-as statements bind targets or may suspend (async).
		return true
	case StmtKindTry:
		if c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Body)) {
			return true
		}
		hStart := stmt.Extra >> 16
		hCount := stmt.Extra & 0xFFFF
		for i := range hCount {
			h := &c.arena.ExceptHandlers[hStart+i]
			if h.Name != "" || c.exprHasScopeOrSuspend(h.Type) {
				return true
			}
			if c.stmtsHaveScopeDecls(c.arena.StmtIDs[h.Body.Start : h.Body.Start+h.Body.Count]) {
				return true
			}
		}
		if stmt.Else != 0 && c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(stmt.Else)) {
			return true
		}
		if stmt.Expr1 != ExprNone && c.stmtsHaveScopeDecls(c.arena.GetStmtSlice(uint32(stmt.Expr1))) {
			return true
		}
		return false
	default:
		return false
	}
}

func (c *Compiler) exprHasScopeOrSuspend(id ExprID) bool {
	if id == ExprNone {
		return false
	}
	expr := c.arena.GetExpr(id)
	if expr == nil {
		return false
	}
	switch expr.Kind {
	case ExprKindNamedExpr, ExprKindYield, ExprKindYieldFrom, ExprKindAwait:
		return true
	case ExprKindUnary, ExprKindStarred, ExprKindAttribute:
		return c.exprHasScopeOrSuspend(expr.Left)
	case ExprKindBinary, ExprKindCompare, ExprKindSubscript:
		return c.exprHasScopeOrSuspend(expr.Left) || c.exprHasScopeOrSuspend(expr.Right)
	case ExprKindIfExp:
		return c.exprHasScopeOrSuspend(ExprID(expr.Extra)) ||
			c.exprHasScopeOrSuspend(expr.Left) ||
			c.exprHasScopeOrSuspend(expr.Right)
	case ExprKindSlice:
		return c.exprHasScopeOrSuspend(expr.Left) ||
			c.exprHasScopeOrSuspend(expr.Right) ||
			c.exprHasScopeOrSuspend(ExprID(expr.Extra))
	case ExprKindCall:
		call := &c.arena.Calls[expr.Extra]
		if c.exprHasScopeOrSuspend(call.Func) {
			return true
		}
		if slices.ContainsFunc(c.arena.GetExprSlice(call.PosArgs), c.exprHasScopeOrSuspend) {
			return true
		}
		return slices.ContainsFunc(c.arena.GetExprSlice(call.KWArgs), c.exprHasScopeOrSuspend)
	case ExprKindList, ExprKindTuple, ExprKindSet:
		return slices.ContainsFunc(c.arena.GetExprSlice(c.arena.ExprSlices[expr.Extra]), c.exprHasScopeOrSuspend)
	case ExprKindDict:
		start := expr.Extra
		count := uint32(expr.Right)
		for i := range count {
			pair := &c.arena.DictPairs[start+i]
			if c.exprHasScopeOrSuspend(pair.Key) || c.exprHasScopeOrSuspend(pair.Value) {
				return true
			}
		}
		return false
	case ExprKindJoinedStr, ExprKindTemplateStr:
		fs := c.arena.GetFString(expr.Extra)
		if fs != nil {
			return slices.ContainsFunc(c.arena.GetExprSlice(fs.Pieces), c.exprHasScopeOrSuspend)
		}
		return false
	case ExprKindFormattedValue:
		fv := c.arena.GetFormattedValue(expr.Extra)
		if fv != nil {
			return c.exprHasScopeOrSuspend(fv.Value) || c.exprHasScopeOrSuspend(fv.FormatSpec)
		}
		return false
	case ExprKindListComp, ExprKindDictComp, ExprKindSetComp, ExprKindGeneratorExp:
		// Comprehensions may contain walrus/yield/await; treat conservatively.
		return true
	default:
		return false
	}
}
