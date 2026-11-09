package vm

import (
	"strings"
	"sync"

	"github.com/servusdei2018/fython/pkg/sqlite"
	"github.com/servusdei2018/fython/pkg/val"
)

type sqliteConnState struct {
	db         *sqlite.DB
	rowFactory val.Value
	closed     bool
}

type sqliteCursorState struct {
	conn        val.Value
	connState   *sqliteConnState
	lastRowID   int64
	rowCount    int64
	description val.Value
	rows        []val.Value
	fetchIdx    int
	arraySize   int
	closed      bool
}

type sqliteRowState struct {
	colNames []string
	values   []val.Value
}

var (
	sqliteConnMap   sync.Map // *val.PyInstance -> *sqliteConnState
	sqliteCursorMap sync.Map // *val.PyInstance -> *sqliteCursorState
	sqliteRowMap    sync.Map // *val.PyInstance -> *sqliteRowState

	sqliteConnType      *val.PyType
	sqliteCursorType    *val.PyType
	sqliteRowType       *val.PyType
	sqlitePrepProtoType *val.PyType
	sqliteOnce          sync.Once

	sqliteErrorType             *val.PyType
	sqliteWarningType           *val.PyType
	sqliteInterfaceErrorType    *val.PyType
	sqliteDatabaseErrorType     *val.PyType
	sqliteInternalErrorType     *val.PyType
	sqliteOperationalErrorType  *val.PyType
	sqliteProgrammingErrorType  *val.PyType
	sqliteIntegrityErrorType    *val.PyType
	sqliteDataErrorType         *val.PyType
	sqliteNotSupportedErrorType *val.PyType
)

func (e *Engine) createSqlite3Module() val.Value {
	sqliteOnce.Do(func() {
		initSqliteTypes()
	})

	modVal := val.NewModuleVal("_sqlite3", "DB-API 2.0 interface for SQLite")
	mod := modVal.AsModule()

	// Types
	mod.Dict.Set(val.InternString("Connection"), val.TypeVal(sqliteConnType))
	mod.Dict.Set(val.InternString("Cursor"), val.TypeVal(sqliteCursorType))
	mod.Dict.Set(val.InternString("Row"), val.TypeVal(sqliteRowType))
	mod.Dict.Set(val.InternString("PrepareProtocol"), val.TypeVal(sqlitePrepProtoType))

	// Exceptions
	mod.Dict.Set(val.InternString("Error"), val.TypeVal(sqliteErrorType))
	mod.Dict.Set(val.InternString("Warning"), val.TypeVal(sqliteWarningType))
	mod.Dict.Set(val.InternString("InterfaceError"), val.TypeVal(sqliteInterfaceErrorType))
	mod.Dict.Set(val.InternString("DatabaseError"), val.TypeVal(sqliteDatabaseErrorType))
	mod.Dict.Set(val.InternString("InternalError"), val.TypeVal(sqliteInternalErrorType))
	mod.Dict.Set(val.InternString("OperationalError"), val.TypeVal(sqliteOperationalErrorType))
	mod.Dict.Set(val.InternString("ProgrammingError"), val.TypeVal(sqliteProgrammingErrorType))
	mod.Dict.Set(val.InternString("IntegrityError"), val.TypeVal(sqliteIntegrityErrorType))
	mod.Dict.Set(val.InternString("DataError"), val.TypeVal(sqliteDataErrorType))
	mod.Dict.Set(val.InternString("NotSupportedError"), val.TypeVal(sqliteNotSupportedErrorType))

	// Module constants
	mod.Dict.Set(val.InternString("sqlite_version"), val.NewStringVal("3.45.1"))
	mod.Dict.Set(val.InternString("version"), val.NewStringVal("2.6.0"))
	mod.Dict.Set(val.InternString("PARSE_DECLTYPES"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("PARSE_COLNAMES"), val.Int64Val(2))
	mod.Dict.Set(val.InternString("SQLITE_OK"), val.Int64Val(0))
	mod.Dict.Set(val.InternString("SQLITE_DENY"), val.Int64Val(1))
	mod.Dict.Set(val.InternString("SQLITE_IGNORE"), val.Int64Val(2))

	// Module functions
	mod.Dict.Set(val.InternString("connect"), val.NewModuleFuncVal("connect", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: connect() missing required argument 'database'")
		}
		path := args[0].AsString()
		db, err := sqlite.Open(path)
		if err != nil {
			panic("OperationalError: " + err.Error())
		}

		connInst := val.NewInstanceOfType(sqliteConnType)
		st := &sqliteConnState{
			db:         db,
			rowFactory: val.NoneVal(),
		}
		sqliteConnMap.Store(connInst.AsInstance(), st)
		return connInst
	}))

	mod.Dict.Set(val.InternString("register_adapter"), val.NewModuleFuncVal("register_adapter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("register_converter"), val.NewModuleFuncVal("register_converter", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	mod.Dict.Set(val.InternString("complete_statement"), val.NewModuleFuncVal("complete_statement", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			return val.BoolVal(false)
		}
		s := strings.TrimSpace(args[0].AsString())
		return val.BoolVal(strings.HasSuffix(s, ";"))
	}))

	mod.Dict.Set(val.InternString("enable_callback_tracebacks"), val.NewModuleFuncVal("enable_callback_tracebacks", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NoneVal()
	}))

	return modVal
}

func initSqliteTypes() {
	sqliteConnType = val.MustNewType("Connection", []*val.PyType{val.ObjectType}, nil)
	sqliteCursorType = val.MustNewType("Cursor", []*val.PyType{val.ObjectType}, nil)
	sqliteRowType = val.MustNewType("Row", []*val.PyType{val.ObjectType}, nil)
	sqlitePrepProtoType = val.MustNewType("PrepareProtocol", []*val.PyType{val.ObjectType}, nil)

	excBase := val.ExceptionType
	if excBase == nil {
		excBase = val.ObjectType
	}
	sqliteErrorType = val.MustNewType("Error", []*val.PyType{excBase}, nil)
	sqliteWarningType = val.MustNewType("Warning", []*val.PyType{excBase}, nil)
	sqliteInterfaceErrorType = val.MustNewType("InterfaceError", []*val.PyType{sqliteErrorType}, nil)
	sqliteDatabaseErrorType = val.MustNewType("DatabaseError", []*val.PyType{sqliteErrorType}, nil)
	sqliteInternalErrorType = val.MustNewType("InternalError", []*val.PyType{sqliteDatabaseErrorType}, nil)
	sqliteOperationalErrorType = val.MustNewType("OperationalError", []*val.PyType{sqliteDatabaseErrorType}, nil)
	sqliteProgrammingErrorType = val.MustNewType("ProgrammingError", []*val.PyType{sqliteDatabaseErrorType}, nil)
	sqliteIntegrityErrorType = val.MustNewType("IntegrityError", []*val.PyType{sqliteDatabaseErrorType}, nil)
	sqliteDataErrorType = val.MustNewType("DataError", []*val.PyType{sqliteDatabaseErrorType}, nil)
	sqliteNotSupportedErrorType = val.MustNewType("NotSupportedError", []*val.PyType{sqliteDatabaseErrorType}, nil)

	var cursorExecFn, cursorExecManyFn, cursorExecScriptFn, cursorFetchOneFn val.Value

	cursorFn := val.NewBuiltinFuncVal("cursor", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 1 {
			panic("TypeError: cursor() missing self")
		}
		self := args[0]
		st, ok := getConnState(self)
		if !ok || st.closed {
			panic("ProgrammingError: Cannot operate on a closed database.")
		}

		curInst := val.NewInstanceOfType(sqliteCursorType)
		curState := &sqliteCursorState{
			conn:        self,
			connState:   st,
			description: val.NoneVal(),
			arraySize:   1,
			rowCount:    -1,
		}
		sqliteCursorMap.Store(curInst.AsInstance(), curState)
		return curInst
	})
	sqliteConnType.Dict.Set(val.InternString("cursor"), cursorFn)

	sqliteConnType.Dict.Set(val.InternString("execute"), val.NewBuiltinFuncVal("execute", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: execute() takes at least 1 argument")
		}
		self := args[0]
		curVal := cursorFn.AsBuiltinFunc().Fn(ctx, cursorFn, []val.Value{self}, nil)
		curArgs := append([]val.Value{curVal}, args[1:]...)
		return cursorExecFn.AsBuiltinFunc().Fn(ctx, cursorExecFn, curArgs, kwnames)
	}))

	sqliteConnType.Dict.Set(val.InternString("executemany"), val.NewBuiltinFuncVal("executemany", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: executemany() takes at least 2 arguments")
		}
		self := args[0]
		curVal := cursorFn.AsBuiltinFunc().Fn(ctx, cursorFn, []val.Value{self}, nil)
		curArgs := append([]val.Value{curVal}, args[1:]...)
		return cursorExecManyFn.AsBuiltinFunc().Fn(ctx, cursorExecManyFn, curArgs, kwnames)
	}))

	sqliteConnType.Dict.Set(val.InternString("executescript"), val.NewBuiltinFuncVal("executescript", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: executescript() takes at least 1 argument")
		}
		self := args[0]
		curVal := cursorFn.AsBuiltinFunc().Fn(ctx, cursorFn, []val.Value{self}, nil)
		curArgs := append([]val.Value{curVal}, args[1:]...)
		return cursorExecScriptFn.AsBuiltinFunc().Fn(ctx, cursorExecScriptFn, curArgs, kwnames)
	}))

	sqliteConnType.Dict.Set(val.InternString("commit"), val.NewBuiltinFuncVal("commit", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		st, ok := getConnState(self)
		if !ok || st.closed {
			panic("ProgrammingError: Cannot operate on a closed database.")
		}
		if err := st.db.Commit(); err != nil {
			panic("OperationalError: " + err.Error())
		}
		return val.NoneVal()
	}))

	sqliteConnType.Dict.Set(val.InternString("rollback"), val.NewBuiltinFuncVal("rollback", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		st, ok := getConnState(self)
		if !ok || st.closed {
			panic("ProgrammingError: Cannot operate on a closed database.")
		}
		if err := st.db.Rollback(); err != nil {
			panic("OperationalError: " + err.Error())
		}
		return val.NoneVal()
	}))

	sqliteConnType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		st, ok := getConnState(self)
		if ok && !st.closed {
			st.closed = true
			_ = st.db.Close()
		}
		return val.NoneVal()
	}))

	sqliteConnType.Dict.Set(val.InternString("__enter__"), val.NewBuiltinFuncVal("__enter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		st, ok := getConnState(self)
		if !ok || st.closed {
			panic("ProgrammingError: Cannot operate on a closed database.")
		}
		if !st.db.InTransaction() {
			_ = st.db.Begin()
		}
		return self
	}))

	sqliteConnType.Dict.Set(val.InternString("__exit__"), val.NewBuiltinFuncVal("__exit__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		st, ok := getConnState(self)
		if !ok || st.closed {
			return val.NoneVal()
		}
		if len(args) > 1 && !args[1].IsNone() {
			_ = st.db.Rollback()
		} else {
			_ = st.db.Commit()
		}
		return val.NoneVal()
	}))

	sqliteConnType.Dict.Set(val.InternString("row_factory"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("row_factory_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			st, _ := getConnState(args[0])
			if st != nil {
				return st.rowFactory
			}
			return val.NoneVal()
		}),
		val.NewBuiltinFuncVal("row_factory_set", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if len(args) > 1 {
				st, _ := getConnState(args[0])
				if st != nil {
					st.rowFactory = args[1]
				}
			}
			return val.NoneVal()
		}),
		val.NoneVal(),
	))

	sqliteConnType.Dict.Set(val.InternString("total_changes"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("total_changes_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			st, _ := getConnState(args[0])
			if st != nil {
				return val.Int64Val(st.db.RowsAffected())
			}
			return val.Int64Val(0)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// Cursor
	cursorExecFn = val.NewBuiltinFuncVal("execute", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: execute() takes at least 1 argument")
		}
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed || cst.connState.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}

		sqlStr := args[1].AsString()
		var params []sqlite.Value
		var named map[string]sqlite.Value

		if len(args) >= 3 && !args[2].IsNone() {
			pArg := args[2]
			switch {
			case pArg.IsTuple():
				for _, it := range pArg.AsTuple().Items {
					params = append(params, pyToSqlValue(it))
				}
			case pArg.IsList():
				for _, it := range pArg.AsList().Items {
					params = append(params, pyToSqlValue(it))
				}
			case pArg.IsDict():
				named = make(map[string]sqlite.Value)
				for _, entry := range pArg.AsDict().Entries {
					if entry.Active {
						kStr := entry.Key.AsString()
						named[kStr] = pyToSqlValue(entry.Value)
					}
				}
			}
		}

		res, err := cst.connState.db.Execute(sqlStr, params, named)
		if err != nil {
			panic(err.Error())
		}

		cst.lastRowID = res.LastInsertID
		cst.rowCount = res.RowsAffected

		if len(res.Columns) > 0 {
			var descItems []val.Value
			for _, col := range res.Columns {
				colDesc := val.NewTupleVal(
					val.NewStringVal(col),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
					val.NoneVal(),
				)
				descItems = append(descItems, colDesc)
			}
			cst.description = val.NewTupleVal(descItems...)

			var pyRows []val.Value
			for _, r := range res.Rows {
				rowVals := make([]val.Value, len(r))
				for i, v := range r {
					rowVals[i] = sqlToPyValue(v)
				}
				pyRows = append(pyRows, formatRow(ctx, self, cst, res.Columns, rowVals))
			}
			cst.rows = pyRows
			cst.fetchIdx = 0
		} else {
			cst.description = val.NoneVal()
			cst.rows = nil
			cst.fetchIdx = 0
		}

		return self
	})
	sqliteCursorType.Dict.Set(val.InternString("execute"), cursorExecFn)

	cursorExecManyFn = val.NewBuiltinFuncVal("executemany", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 3 {
			panic("TypeError: executemany() takes at least 2 arguments")
		}
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed || cst.connState.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}

		sqlStr := args[1].AsString()
		seq := args[2]
		var items []val.Value
		if seq.IsList() {
			items = seq.AsList().Items
		} else if seq.IsTuple() {
			items = seq.AsTuple().Items
		} else if tpIter := seq.Type().Slots.TpIter; tpIter != nil {
			itVal := tpIter(ctx, seq)
			if itNext := itVal.Type().Slots.TpIterNext; itNext != nil {
				for {
					item, status := itNext(ctx, itVal)
					if status == val.IterExhausted {
						break
					}
					items = append(items, item)
				}
			}
		}

		var totalAffected int64
		for _, paramItem := range items {
			_ = cursorExecFn.AsBuiltinFunc().Fn(ctx, cursorExecFn, []val.Value{self, val.NewStringVal(sqlStr), paramItem}, nil)
			if cst.rowCount > 0 {
				totalAffected += cst.rowCount
			}
		}
		cst.rowCount = totalAffected
		return self
	})
	sqliteCursorType.Dict.Set(val.InternString("executemany"), cursorExecManyFn)

	cursorExecScriptFn = val.NewBuiltinFuncVal("executescript", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: executescript() takes at least 1 argument")
		}
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed || cst.connState.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}
		script := args[1].AsString()
		if err := cst.connState.db.ExecuteScript(script); err != nil {
			panic(err.Error())
		}
		return self
	})
	sqliteCursorType.Dict.Set(val.InternString("executescript"), cursorExecScriptFn)

	cursorFetchOneFn = val.NewBuiltinFuncVal("fetchone", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}
		if cst.fetchIdx >= len(cst.rows) {
			return val.NoneVal()
		}
		r := cst.rows[cst.fetchIdx]
		cst.fetchIdx++
		return r
	})
	sqliteCursorType.Dict.Set(val.InternString("fetchone"), cursorFetchOneFn)

	sqliteCursorType.Dict.Set(val.InternString("fetchall"), val.NewBuiltinFuncVal("fetchall", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}
		if cst.fetchIdx >= len(cst.rows) {
			return val.NewListVal()
		}
		remaining := cst.rows[cst.fetchIdx:]
		cst.fetchIdx = len(cst.rows)
		return val.NewListVal(remaining...)
	}))

	sqliteCursorType.Dict.Set(val.InternString("fetchmany"), val.NewBuiltinFuncVal("fetchmany", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		cst, ok := getCursorState(self)
		if !ok || cst.closed {
			panic("ProgrammingError: Cannot operate on a closed cursor.")
		}
		size := cst.arraySize
		if len(args) > 1 && args[1].IsInt64() {
			size = int(args[1].AsInt64())
		}
		if size <= 0 || cst.fetchIdx >= len(cst.rows) {
			return val.NewListVal()
		}
		end := min(cst.fetchIdx+size, len(cst.rows))
		res := cst.rows[cst.fetchIdx:end]
		cst.fetchIdx = end
		return val.NewListVal(res...)
	}))

	sqliteCursorType.Dict.Set(val.InternString("close"), val.NewBuiltinFuncVal("close", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		self := args[0]
		cst, ok := getCursorState(self)
		if ok {
			cst.closed = true
			cst.rows = nil
		}
		return val.NoneVal()
	}))

	sqliteCursorType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
		return o
	}
	sqliteCursorType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return args[0]
	}))
	sqliteCursorType.Dict.Set(val.InternString("__next__"), val.NewBuiltinFuncVal("__next__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		res := cursorFetchOneFn.AsBuiltinFunc().Fn(ctx, cursorFetchOneFn, args, nil)
		if res.IsNone() {
			ctx.Raise(val.NewStopIteration(val.NoneVal()))
		}
		return res
	}))

	sqliteCursorType.Dict.Set(val.InternString("description"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("description_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if cst, ok := getCursorState(args[0]); ok {
				return cst.description
			}
			return val.NoneVal()
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	sqliteCursorType.Dict.Set(val.InternString("lastrowid"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("lastrowid_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if cst, ok := getCursorState(args[0]); ok {
				return val.Int64Val(cst.lastRowID)
			}
			return val.NoneVal()
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	sqliteCursorType.Dict.Set(val.InternString("rowcount"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("rowcount_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if cst, ok := getCursorState(args[0]); ok {
				return val.Int64Val(cst.rowCount)
			}
			return val.Int64Val(-1)
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	sqliteCursorType.Dict.Set(val.InternString("connection"), val.NewPropertyVal(
		val.NewBuiltinFuncVal("connection_get", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
			if cst, ok := getCursorState(args[0]); ok {
				return cst.conn
			}
			return val.NoneVal()
		}),
		val.NoneVal(),
		val.NoneVal(),
	))

	// Row (sqlite3.Row)
	sqliteRowType.Slots.SqLen = func(ctx *val.ExecContext, o val.Value) int64 {
		if rst, ok := getRowState(o); ok {
			return int64(len(rst.values))
		}
		return 0
	}
	sqliteRowType.Slots.MpSubscript = func(ctx *val.ExecContext, o, key val.Value) val.Value {
		rst, ok := getRowState(o)
		if !ok {
			panic("TypeError: descriptor requires a 'Row' object")
		}
		if key.IsInt64() {
			idx := int(key.AsInt64())
			if idx < 0 {
				idx += len(rst.values)
			}
			if idx < 0 || idx >= len(rst.values) {
				panic("IndexError: Row index out of range")
			}
			return rst.values[idx]
		}
		if key.IsAnyString() {
			kLower := strings.ToLower(key.AsString())
			for i, c := range rst.colNames {
				if strings.ToLower(c) == kLower {
					return rst.values[i]
				}
			}
			panic("IndexError: No item with that key")
		}
		panic("IndexError: Row index must be integer or string")
	}

	sqliteRowType.Dict.Set(val.InternString("keys"), val.NewBuiltinFuncVal("keys", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		rst, ok := getRowState(args[0])
		if !ok {
			return val.NewListVal()
		}
		var keyVals []val.Value
		for _, k := range rst.colNames {
			keyVals = append(keyVals, val.NewStringVal(k))
		}
		return val.NewListVal(keyVals...)
	}))

	sqliteRowType.Slots.TpIter = func(ctx *val.ExecContext, o val.Value) val.Value {
		rst, ok := getRowState(o)
		if !ok {
			return val.NewIterVal(val.NewTupleVal())
		}
		return val.NewIterVal(val.NewTupleVal(rst.values...))
	}
	sqliteRowType.Dict.Set(val.InternString("__iter__"), val.NewBuiltinFuncVal("__iter__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return sqliteRowType.Slots.TpIter(ctx, args[0])
	}))

	sqliteRowType.Dict.Set(val.InternString("__getitem__"), val.NewBuiltinFuncVal("__getitem__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: __getitem__() takes exactly 1 argument")
		}
		return sqliteRowType.Slots.MpSubscript(ctx, args[0], args[1])
	}))

	sqliteRowType.Dict.Set(val.InternString("__len__"), val.NewBuiltinFuncVal("__len__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.Int64Val(sqliteRowType.Slots.SqLen(ctx, args[0]))
	}))

	sqliteRowType.Dict.Set(val.InternString("__repr__"), val.NewBuiltinFuncVal("__repr__", func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		return val.NewStringVal("<sqlite3.Row object>")
	}))

	sqliteRowType.Slots.TpCall = func(ctx *val.ExecContext, callable val.Value, args []val.Value, kwnames []val.Value) val.Value {
		if len(args) < 2 {
			panic("TypeError: Row() takes at least 2 arguments")
		}
		cur := args[0]
		tup := args[1]
		var colNames []string
		if cst, ok := getCursorState(cur); ok {
			if cst.description.IsTuple() {
				for _, d := range cst.description.AsTuple().Items {
					if d.IsTuple() && len(d.AsTuple().Items) > 0 {
						colNames = append(colNames, d.AsTuple().Items[0].AsString())
					}
				}
			}
		}
		var values []val.Value
		if tup.IsTuple() {
			values = append([]val.Value(nil), tup.AsTuple().Items...)
		} else if tup.IsList() {
			values = append([]val.Value(nil), tup.AsList().Items...)
		}
		rowInst := val.NewInstanceOfType(sqliteRowType)
		sqliteRowMap.Store(rowInst.AsInstance(), &sqliteRowState{
			colNames: colNames,
			values:   values,
		})
		return rowInst
	}
}

func getConnState(self val.Value) (*sqliteConnState, bool) {
	if !self.IsInstance() {
		return nil, false
	}
	if v, ok := sqliteConnMap.Load(self.AsInstance()); ok {
		return v.(*sqliteConnState), true
	}
	return nil, false
}

func getCursorState(self val.Value) (*sqliteCursorState, bool) {
	if !self.IsInstance() {
		return nil, false
	}
	if v, ok := sqliteCursorMap.Load(self.AsInstance()); ok {
		return v.(*sqliteCursorState), true
	}
	return nil, false
}

func getRowState(self val.Value) (*sqliteRowState, bool) {
	if !self.IsInstance() {
		return nil, false
	}
	if v, ok := sqliteRowMap.Load(self.AsInstance()); ok {
		return v.(*sqliteRowState), true
	}
	return nil, false
}

func formatRow(ctx *val.ExecContext, curVal val.Value, cst *sqliteCursorState, colNames []string, rowVals []val.Value) val.Value {
	tup := val.NewTupleVal(rowVals...)
	rf := cst.connState.rowFactory
	if rf.IsNone() || rf.HeaderPtr() == nil {
		return tup
	}
	if rf.IsType() && rf.AsType() == sqliteRowType {
		rowInst := val.NewInstanceOfType(sqliteRowType)
		sqliteRowMap.Store(rowInst.AsInstance(), &sqliteRowState{
			colNames: colNames,
			values:   rowVals,
		})
		return rowInst
	}
	// Custom row factory: call rf(cursor, tuple)
	if ctx != nil {
		if tpCall := rf.Type().Slots.TpCall; tpCall != nil {
			return tpCall(ctx, rf, []val.Value{curVal, tup}, nil)
		}
	}
	return tup
}

func pyToSqlValue(v val.Value) sqlite.Value {
	if v.IsNone() || v.HeaderPtr() == nil {
		return nil
	}
	if v.IsInt64() {
		return v.AsInt64()
	}
	if v.IsFloat64() {
		return v.AsFloat64()
	}
	if v.IsBool() {
		return v.AsBool()
	}
	if v.IsAnyString() {
		return v.AsString()
	}
	if v.IsBytes() {
		return v.AsBytes().Data
	}
	if v.IsByteArray() {
		return v.AsByteArray().Data
	}
	return v.String()
}

func sqlToPyValue(v sqlite.Value) val.Value {
	if v == nil {
		return val.NoneVal()
	}
	switch valTyped := v.(type) {
	case int64:
		return val.Int64Val(valTyped)
	case int:
		return val.Int64Val(int64(valTyped))
	case float64:
		return val.Float64Val(valTyped)
	case string:
		return val.NewStringVal(valTyped)
	case []byte:
		return val.NewBytesVal(valTyped)
	case bool:
		return val.BoolVal(valTyped)
	default:
		return val.NewStringVal(valTyped.(string))
	}
}
