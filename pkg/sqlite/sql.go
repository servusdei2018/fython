package sqlite

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Execute parses and runs a single SQL statement with optional positional and named parameters.
func (db *DB) Execute(sql string, params []Value, named map[string]Value) (*Result, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	trimmed := strings.TrimSpace(sql)
	for strings.HasSuffix(trimmed, ";") {
		trimmed = strings.TrimSpace(trimmed[:len(trimmed)-1])
	}
	if trimmed == "" {
		return &Result{}, nil
	}

	// Transaction control statements
	upper := strings.ToUpper(trimmed)
	if upper == "BEGIN" || upper == "BEGIN TRANSACTION" {
		if db.inTx {
			return nil, fmt.Errorf("OperationalError: cannot start a transaction within a transaction")
		}
		db.snapshot = db.cloneTables()
		db.inTx = true
		return &Result{}, nil
	}
	if upper == "COMMIT" || upper == "COMMIT TRANSACTION" {
		if db.inTx {
			db.snapshot = nil
			db.inTx = false
			if db.path != "" && db.path != ":memory:" {
				_ = db.saveFile()
			}
		}
		return &Result{}, nil
	}
	if upper == "ROLLBACK" || upper == "ROLLBACK TRANSACTION" {
		if db.inTx {
			if db.snapshot != nil {
				db.tables = db.snapshot
				db.snapshot = nil
			}
			db.inTx = false
		}
		return &Result{}, nil
	}

	tokens := tokenize(trimmed)
	if len(tokens) == 0 {
		return &Result{}, nil
	}

	first := strings.ToUpper(tokens[0])
	switch first {
	case "CREATE":
		return db.execCreateTable(tokens)
	case "DROP":
		return db.execDropTable(tokens)
	case "INSERT":
		return db.execInsert(tokens, params, named)
	case "SELECT":
		return db.execSelect(tokens, params, named)
	case "UPDATE":
		return db.execUpdate(tokens, params, named)
	case "DELETE":
		return db.execDelete(tokens, params, named)
	default:
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[0])
	}
}

// ExecuteScript executes multiple SQL statements separated by semicolons.
func (db *DB) ExecuteScript(script string) error {
	stmts := splitStatements(script)
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.Execute(stmt, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func splitStatements(script string) []string {
	var stmts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(script); i++ {
		ch := script[i]
		if inQuote {
			current.WriteByte(ch)
			if ch == quoteChar {
				if i+1 < len(script) && script[i+1] == quoteChar {
					current.WriteByte(script[i+1])
					i++
				} else {
					inQuote = false
				}
			}
		} else {
			if ch == '\'' || ch == '"' {
				inQuote = true
				quoteChar = ch
				current.WriteByte(ch)
			} else if ch == ';' {
				stmts = append(stmts, current.String())
				current.Reset()
			} else {
				current.WriteByte(ch)
			}
		}
	}
	if current.Len() > 0 {
		stmts = append(stmts, current.String())
	}
	return stmts
}

func tokenize(sql string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	quoteChar := byte(0)

	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}

	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if inQuote {
			cur.WriteByte(ch)
			if ch == quoteChar {
				if i+1 < len(sql) && sql[i+1] == quoteChar {
					cur.WriteByte(sql[i+1])
					i++
				} else {
					inQuote = false
					flush()
				}
			}
			continue
		}

		if unicode.IsSpace(rune(ch)) {
			flush()
			continue
		}

		if ch == '\'' || ch == '"' || ch == '`' {
			flush()
			inQuote = true
			quoteChar = ch
			cur.WriteByte(ch)
			continue
		}

		if ch == '(' || ch == ')' || ch == ',' || ch == ';' {
			flush()
			tokens = append(tokens, string(ch))
			continue
		}

		if ch == '=' || ch == '<' || ch == '>' || ch == '!' {
			flush()
			if i+1 < len(sql) {
				next := sql[i+1]
				if (ch == '<' && (next == '=' || next == '>')) ||
					(ch == '>' && next == '=') ||
					(ch == '!' && next == '=') ||
					(ch == '=' && next == '=') {
					tokens = append(tokens, string([]byte{ch, next}))
					i++
					continue
				}
			}
			tokens = append(tokens, string(ch))
			continue
		}

		cur.WriteByte(ch)
	}
	flush()
	return tokens
}

func cleanIdentifier(s string) string {
	s = strings.TrimSpace(s)
	if (strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) ||
		(strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return s[1 : len(s)-1]
	}
	return s
}

// CREATE TABLE [IF NOT EXISTS] name (col1 type, col2 type ...)
func (db *DB) execCreateTable(tokens []string) (*Result, error) {
	if len(tokens) < 3 || strings.ToUpper(tokens[1]) != "TABLE" {
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[1])
	}
	idx := 2
	ifNotExists := false
	if idx+2 < len(tokens) && strings.ToUpper(tokens[idx]) == "IF" && strings.ToUpper(tokens[idx+1]) == "NOT" && strings.ToUpper(tokens[idx+2]) == "EXISTS" {
		ifNotExists = true
		idx += 3
	}

	if idx >= len(tokens) {
		return nil, fmt.Errorf("OperationalError: missing table name in CREATE TABLE")
	}
	tableName := cleanIdentifier(tokens[idx])
	lowerName := strings.ToLower(tableName)
	idx++

	if _, exists := db.tables[lowerName]; exists {
		if ifNotExists {
			return &Result{}, nil
		}
		return nil, fmt.Errorf("OperationalError: table %s already exists", tableName)
	}

	if idx >= len(tokens) || tokens[idx] != "(" {
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[idx])
	}
	idx++

	// Parse column definitions
	var cols []Column
	colIdx := make(map[string]int)

	for idx < len(tokens) && tokens[idx] != ")" {
		if tokens[idx] == "," {
			idx++
			continue
		}
		cName := cleanIdentifier(tokens[idx])
		idx++
		cType := "TEXT"
		pk := false
		autoInc := false

		for idx < len(tokens) && tokens[idx] != "," && tokens[idx] != ")" {
			u := strings.ToUpper(tokens[idx])
			if u == "PRIMARY" && idx+1 < len(tokens) && strings.ToUpper(tokens[idx+1]) == "KEY" {
				pk = true
				idx += 2
			} else if u == "AUTOINCREMENT" {
				autoInc = true
				idx++
			} else if u == "NOT" && idx+1 < len(tokens) && strings.ToUpper(tokens[idx+1]) == "NULL" {
				idx += 2
			} else {
				cType = u
				idx++
			}
		}

		if pk && strings.Contains(strings.ToUpper(cType), "INT") {
			autoInc = true
		}

		colIdx[strings.ToLower(cName)] = len(cols)
		cols = append(cols, Column{
			Name:       cName,
			Type:       cType,
			PrimaryKey: pk,
			AutoInc:    autoInc,
		})
	}

	db.tables[lowerName] = &Table{
		Name:        tableName,
		Columns:     cols,
		ColIndex:    colIdx,
		Rows:        nil,
		NextAutoInc: 1,
	}

	return &Result{}, nil
}

// DROP TABLE [IF EXISTS] name
func (db *DB) execDropTable(tokens []string) (*Result, error) {
	if len(tokens) < 3 || strings.ToUpper(tokens[1]) != "TABLE" {
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[1])
	}
	idx := 2
	ifExists := false
	if idx+1 < len(tokens) && strings.ToUpper(tokens[idx]) == "IF" && strings.ToUpper(tokens[idx+1]) == "EXISTS" {
		ifExists = true
		idx += 2
	}
	if idx >= len(tokens) {
		return nil, fmt.Errorf("OperationalError: missing table name in DROP TABLE")
	}
	tblName := cleanIdentifier(tokens[idx])
	lowerName := strings.ToLower(tblName)

	if _, ok := db.tables[lowerName]; !ok {
		if ifExists {
			return &Result{}, nil
		}
		return nil, fmt.Errorf("OperationalError: no such table: %s", tblName)
	}

	delete(db.tables, lowerName)
	return &Result{}, nil
}

// INSERT INTO table [(col1, ...)] VALUES (val1, ...)
func (db *DB) execInsert(tokens []string, params []Value, named map[string]Value) (*Result, error) {
	idx := 1
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "INTO" {
		idx++
	}
	if idx >= len(tokens) {
		return nil, fmt.Errorf("OperationalError: near INSERT: syntax error")
	}

	tblName := cleanIdentifier(tokens[idx])
	lowerName := strings.ToLower(tblName)
	tbl, ok := db.tables[lowerName]
	if !ok {
		return nil, fmt.Errorf("OperationalError: no such table: %s", tblName)
	}
	idx++

	var colNames []string
	if idx < len(tokens) && tokens[idx] == "(" {
		idx++
		for idx < len(tokens) && tokens[idx] != ")" {
			if tokens[idx] == "," {
				idx++
				continue
			}
			colNames = append(colNames, cleanIdentifier(tokens[idx]))
			idx++
		}
		if idx < len(tokens) && tokens[idx] == ")" {
			idx++
		}
	} else {
		for _, c := range tbl.Columns {
			colNames = append(colNames, c.Name)
		}
	}

	if idx >= len(tokens) || strings.ToUpper(tokens[idx]) != "VALUES" {
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[idx])
	}
	idx++

	if idx >= len(tokens) || tokens[idx] != "(" {
		return nil, fmt.Errorf("OperationalError: near %q: syntax error", tokens[idx])
	}
	idx++

	paramIdx := 0
	var valTokens []string
	for idx < len(tokens) && tokens[idx] != ")" {
		if tokens[idx] == "," {
			idx++
			continue
		}
		valTokens = append(valTokens, tokens[idx])
		idx++
	}

	rowVals := make([]Value, len(tbl.Columns))
	// Initialize default values (nil)
	for i := range rowVals {
		rowVals[i] = nil
	}

	for i, cName := range colNames {
		targetColIdx, found := tbl.ColIndex[strings.ToLower(cName)]
		if !found {
			return nil, fmt.Errorf("OperationalError: table %s has no column named %s", tblName, cName)
		}
		if i >= len(valTokens) {
			break
		}
		token := valTokens[i]
		val, err := resolveValue(token, params, named, &paramIdx)
		if err != nil {
			return nil, err
		}
		rowVals[targetColIdx] = val
	}

	// Handle autoincrement / primary key
	var lastID int64
	for i, c := range tbl.Columns {
		if c.AutoInc {
			if rowVals[i] == nil {
				rowVals[i] = tbl.NextAutoInc
				lastID = tbl.NextAutoInc
				tbl.NextAutoInc++
			} else {
				if id, ok := toInt64(rowVals[i]); ok {
					lastID = id
					if id >= tbl.NextAutoInc {
						tbl.NextAutoInc = id + 1
					}
				}
			}
		}
	}

	tbl.Rows = append(tbl.Rows, rowVals)
	db.lastInsertID = lastID
	db.rowsAffected = 1

	return &Result{
		LastInsertID: lastID,
		RowsAffected: 1,
	}, nil
}

// SELECT [DISTINCT] cols FROM table [WHERE ...] [ORDER BY ...] [LIMIT n] [OFFSET m]
func (db *DB) execSelect(tokens []string, params []Value, named map[string]Value) (*Result, error) {
	idx := 1
	distinct := false
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "DISTINCT" {
		distinct = true
		idx++
	}

	// Parse projection expressions up to FROM
	var projTokens []string
	for idx < len(tokens) && strings.ToUpper(tokens[idx]) != "FROM" {
		projTokens = append(projTokens, tokens[idx])
		idx++
	}

	if idx >= len(tokens) {
		// SELECT without FROM (e.g. SELECT 1)
		return db.evalSelectExprs(projTokens, params, named)
	}
	idx++ // skip FROM

	if idx >= len(tokens) {
		return nil, fmt.Errorf("OperationalError: missing table in FROM")
	}
	tblName := cleanIdentifier(tokens[idx])
	lowerName := strings.ToLower(tblName)
	tbl, ok := db.tables[lowerName]
	if !ok {
		return nil, fmt.Errorf("OperationalError: no such table: %s", tblName)
	}
	idx++

	// Optional WHERE
	var whereTokens []string
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "WHERE" {
		idx++
		for idx < len(tokens) &&
			strings.ToUpper(tokens[idx]) != "ORDER" &&
			strings.ToUpper(tokens[idx]) != "LIMIT" &&
			strings.ToUpper(tokens[idx]) != "GROUP" {
			whereTokens = append(whereTokens, tokens[idx])
			idx++
		}
	}

	// Optional ORDER BY
	var orderCol string
	orderDesc := false
	if idx+1 < len(tokens) && strings.ToUpper(tokens[idx]) == "ORDER" && strings.ToUpper(tokens[idx+1]) == "BY" {
		idx += 2
		if idx < len(tokens) {
			orderCol = cleanIdentifier(tokens[idx])
			idx++
		}
		if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "DESC" {
			orderDesc = true
			idx++
		} else if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "ASC" {
			idx++
		}
	}

	// Optional LIMIT and OFFSET
	limit := -1
	offset := 0
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "LIMIT" {
		idx++
		if idx < len(tokens) {
			if n, err := strconv.Atoi(tokens[idx]); err == nil {
				limit = n
			}
			idx++
		}
		if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "OFFSET" {
			idx++
			if idx < len(tokens) {
				if n, err := strconv.Atoi(tokens[idx]); err == nil {
					offset = n
				}
				idx++
			}
		}
	}

	// Filter rows by WHERE clause
	var filtered [][]Value
	paramIdx := 0
	for _, row := range tbl.Rows {
		if len(whereTokens) > 0 {
			pIdxCopy := paramIdx
			match, err := evalWhere(whereTokens, tbl, row, params, named, &pIdxCopy)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		filtered = append(filtered, row)
	}

	// Sort by ORDER BY
	if orderCol != "" {
		cIdx, found := tbl.ColIndex[strings.ToLower(orderCol)]
		if found {
			filtered = sortRows(filtered, cIdx, orderDesc)
		}
	}

	// Check for aggregate functions
	projItems := splitProjection(projTokens)
	isAggregate := false
	for _, item := range projItems {
		u := strings.ToUpper(item)
		if strings.HasPrefix(u, "COUNT(") || strings.HasPrefix(u, "SUM(") ||
			strings.HasPrefix(u, "AVG(") || strings.HasPrefix(u, "MIN(") || strings.HasPrefix(u, "MAX(") {
			isAggregate = true
			break
		}
	}

	if isAggregate {
		return db.evalAggregates(projItems, tbl, filtered)
	}

	// Project columns
	colHeaders, projColIndices, isStar := resolveProjections(projItems, tbl)
	var resultRows [][]Value

	for i, row := range filtered {
		if i < offset {
			continue
		}
		if limit >= 0 && len(resultRows) >= limit {
			break
		}
		var outRow []Value
		if isStar {
			outRow = append([]Value(nil), row...)
		} else {
			for _, ci := range projColIndices {
				if ci >= 0 && ci < len(row) {
					outRow = append(outRow, row[ci])
				} else {
					outRow = append(outRow, nil)
				}
			}
		}
		if distinct && containsRow(resultRows, outRow) {
			continue
		}
		resultRows = append(resultRows, outRow)
	}

	return &Result{
		Columns: colHeaders,
		Rows:    resultRows,
	}, nil
}

func (db *DB) evalSelectExprs(tokens []string, params []Value, named map[string]Value) (*Result, error) {
	items := splitProjection(tokens)
	var cols []string
	var vals []Value
	pIdx := 0
	for _, it := range items {
		val, err := resolveValue(it, params, named, &pIdx)
		if err != nil {
			return nil, err
		}
		cols = append(cols, it)
		vals = append(vals, val)
	}
	return &Result{
		Columns: cols,
		Rows:    [][]Value{vals},
	}, nil
}

func (db *DB) evalAggregates(projItems []string, tbl *Table, rows [][]Value) (*Result, error) {
	var cols []string
	var outRow []Value

	for _, item := range projItems {
		u := strings.ToUpper(item)
		cols = append(cols, item)

		if strings.HasPrefix(u, "COUNT(") {
			inner := strings.TrimSpace(item[6 : len(item)-1])
			if inner == "*" {
				outRow = append(outRow, int64(len(rows)))
			} else {
				cIdx, found := tbl.ColIndex[strings.ToLower(inner)]
				if !found {
					outRow = append(outRow, int64(0))
				} else {
					var cnt int64
					for _, r := range rows {
						if r[cIdx] != nil {
							cnt++
						}
					}
					outRow = append(outRow, cnt)
				}
			}
		} else if strings.HasPrefix(u, "SUM(") {
			inner := cleanIdentifier(strings.TrimSpace(item[4 : len(item)-1]))
			cIdx, found := tbl.ColIndex[strings.ToLower(inner)]
			if !found || len(rows) == 0 {
				outRow = append(outRow, nil)
			} else {
				var sum float64
				hasVal := false
				for _, r := range rows {
					if v := r[cIdx]; v != nil {
						hasVal = true
						sum += toFloat(v)
					}
				}
				if hasVal {
					outRow = append(outRow, sum)
				} else {
					outRow = append(outRow, nil)
				}
			}
		} else if strings.HasPrefix(u, "AVG(") {
			inner := cleanIdentifier(strings.TrimSpace(item[4 : len(item)-1]))
			cIdx, found := tbl.ColIndex[strings.ToLower(inner)]
			if !found || len(rows) == 0 {
				outRow = append(outRow, nil)
			} else {
				var sum float64
				var cnt int64
				for _, r := range rows {
					if v := r[cIdx]; v != nil {
						cnt++
						sum += toFloat(v)
					}
				}
				if cnt > 0 {
					outRow = append(outRow, sum/float64(cnt))
				} else {
					outRow = append(outRow, nil)
				}
			}
		} else if strings.HasPrefix(u, "MIN(") {
			inner := cleanIdentifier(strings.TrimSpace(item[4 : len(item)-1]))
			cIdx, found := tbl.ColIndex[strings.ToLower(inner)]
			if !found || len(rows) == 0 {
				outRow = append(outRow, nil)
			} else {
				minVal := rows[0][cIdx]
				for _, r := range rows[1:] {
					if compareValues(r[cIdx], minVal) < 0 {
						minVal = r[cIdx]
					}
				}
				outRow = append(outRow, minVal)
			}
		} else if strings.HasPrefix(u, "MAX(") {
			inner := cleanIdentifier(strings.TrimSpace(item[4 : len(item)-1]))
			cIdx, found := tbl.ColIndex[strings.ToLower(inner)]
			if !found || len(rows) == 0 {
				outRow = append(outRow, nil)
			} else {
				maxVal := rows[0][cIdx]
				for _, r := range rows[1:] {
					if compareValues(r[cIdx], maxVal) > 0 {
						maxVal = r[cIdx]
					}
				}
				outRow = append(outRow, maxVal)
			}
		} else {
			outRow = append(outRow, nil)
		}
	}

	return &Result{
		Columns: cols,
		Rows:    [][]Value{outRow},
	}, nil
}

// UPDATE table SET col1 = val1, ... [WHERE ...]
func (db *DB) execUpdate(tokens []string, params []Value, named map[string]Value) (*Result, error) {
	if len(tokens) < 4 {
		return nil, fmt.Errorf("OperationalError: syntax error near UPDATE")
	}
	tblName := cleanIdentifier(tokens[1])
	lowerName := strings.ToLower(tblName)
	tbl, ok := db.tables[lowerName]
	if !ok {
		return nil, fmt.Errorf("OperationalError: no such table: %s", tblName)
	}

	if strings.ToUpper(tokens[2]) != "SET" {
		return nil, fmt.Errorf("OperationalError: syntax error near %s", tokens[2])
	}
	idx := 3

	// Parse assignments
	type assign struct {
		colIdx int
		val    Value
	}
	var assigns []assign
	paramIdx := 0

	for idx < len(tokens) && strings.ToUpper(tokens[idx]) != "WHERE" {
		if tokens[idx] == "," {
			idx++
			continue
		}
		cName := cleanIdentifier(tokens[idx])
		ci, found := tbl.ColIndex[strings.ToLower(cName)]
		if !found {
			return nil, fmt.Errorf("OperationalError: table %s has no column named %s", tblName, cName)
		}
		idx++
		if idx >= len(tokens) || tokens[idx] != "=" {
			return nil, fmt.Errorf("OperationalError: syntax error near %s", cName)
		}
		idx++
		if idx >= len(tokens) {
			return nil, fmt.Errorf("OperationalError: syntax error near =")
		}
		val, err := resolveValue(tokens[idx], params, named, &paramIdx)
		if err != nil {
			return nil, err
		}
		assigns = append(assigns, assign{colIdx: ci, val: val})
		idx++
	}

	var whereTokens []string
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "WHERE" {
		whereTokens = tokens[idx+1:]
	}

	var affected int64
	for _, row := range tbl.Rows {
		if len(whereTokens) > 0 {
			pCopy := paramIdx
			match, err := evalWhere(whereTokens, tbl, row, params, named, &pCopy)
			if err != nil {
				return nil, err
			}
			if !match {
				continue
			}
		}
		for _, a := range assigns {
			row[a.colIdx] = a.val
		}
		affected++
	}

	db.rowsAffected = affected
	return &Result{RowsAffected: affected}, nil
}

// DELETE FROM table [WHERE ...]
func (db *DB) execDelete(tokens []string, params []Value, named map[string]Value) (*Result, error) {
	idx := 1
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "FROM" {
		idx++
	}
	if idx >= len(tokens) {
		return nil, fmt.Errorf("OperationalError: syntax error near DELETE")
	}
	tblName := cleanIdentifier(tokens[idx])
	lowerName := strings.ToLower(tblName)
	tbl, ok := db.tables[lowerName]
	if !ok {
		return nil, fmt.Errorf("OperationalError: no such table: %s", tblName)
	}
	idx++

	var whereTokens []string
	if idx < len(tokens) && strings.ToUpper(tokens[idx]) == "WHERE" {
		whereTokens = tokens[idx+1:]
	}

	var remaining [][]Value
	var affected int64
	paramIdx := 0

	for _, row := range tbl.Rows {
		if len(whereTokens) > 0 {
			pCopy := paramIdx
			match, err := evalWhere(whereTokens, tbl, row, params, named, &pCopy)
			if err != nil {
				return nil, err
			}
			if match {
				affected++
				continue
			}
		} else {
			affected++
			continue
		}
		remaining = append(remaining, row)
	}

	tbl.Rows = remaining
	db.rowsAffected = affected
	return &Result{RowsAffected: affected}, nil
}

func splitProjection(tokens []string) []string {
	var items []string
	var cur strings.Builder
	depth := 0

	for _, t := range tokens {
		if t == "(" {
			depth++
			cur.WriteString(t)
		} else if t == ")" {
			depth--
			cur.WriteString(t)
		} else if t == "," && depth == 0 {
			items = append(items, strings.TrimSpace(cur.String()))
			cur.Reset()
		} else {
			if cur.Len() > 0 && !strings.HasSuffix(cur.String(), "(") {
				cur.WriteString(" ")
			}
			cur.WriteString(t)
		}
	}
	if cur.Len() > 0 {
		items = append(items, strings.TrimSpace(cur.String()))
	}
	return items
}

func resolveProjections(items []string, tbl *Table) ([]string, []int, bool) {
	if len(items) == 1 && items[0] == "*" {
		var cols []string
		var indices []int
		for i, c := range tbl.Columns {
			cols = append(cols, c.Name)
			indices = append(indices, i)
		}
		return cols, indices, true
	}

	var cols []string
	var indices []int
	for _, it := range items {
		alias := ""
		parts := strings.Fields(it)
		if len(parts) >= 3 && strings.ToUpper(parts[len(parts)-2]) == "AS" {
			alias = parts[len(parts)-1]
			it = strings.Join(parts[:len(parts)-2], " ")
		}
		it = cleanIdentifier(it)
		cIdx, found := tbl.ColIndex[strings.ToLower(it)]
		if !found {
			cIdx = -1
		}
		if alias != "" {
			cols = append(cols, cleanIdentifier(alias))
		} else {
			cols = append(cols, it)
		}
		indices = append(indices, cIdx)
	}
	return cols, indices, false
}

func resolveValue(tok string, params []Value, named map[string]Value, paramIdx *int) (Value, error) {
	tok = strings.TrimSpace(tok)
	if tok == "?" {
		if *paramIdx >= len(params) {
			return nil, fmt.Errorf("ProgrammingError: Incorrect number of bindings supplied. The current statement uses %d, and there are %d supplied", *paramIdx+1, len(params))
		}
		v := params[*paramIdx]
		*paramIdx++
		return v, nil
	}
	if strings.HasPrefix(tok, ":") || strings.HasPrefix(tok, "@") || strings.HasPrefix(tok, "$") {
		name := tok[1:]
		if named != nil {
			if v, ok := named[name]; ok {
				return v, nil
			}
			// Fallback: check with prefix in map
			if v, ok := named[tok]; ok {
				return v, nil
			}
		}
		return nil, fmt.Errorf("ProgrammingError: You did not supply a value for binding %s", tok)
	}
	u := strings.ToUpper(tok)
	if u == "NULL" {
		return nil, nil
	}
	if u == "TRUE" {
		return true, nil
	}
	if u == "FALSE" {
		return false, nil
	}
	if (strings.HasPrefix(tok, "'") && strings.HasSuffix(tok, "'")) ||
		(strings.HasPrefix(tok, "\"") && strings.HasSuffix(tok, "\"")) {
		s := tok[1 : len(tok)-1]
		s = strings.ReplaceAll(s, "''", "'")
		return s, nil
	}
	if i, err := strconv.ParseInt(tok, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(tok, 64); err == nil {
		return f, nil
	}
	return tok, nil
}

func evalWhere(tokens []string, tbl *Table, row []Value, params []Value, named map[string]Value, paramIdx *int) (bool, error) {
	if len(tokens) == 0 {
		return true, nil
	}

	// Split by AND / OR
	var clauses [][]string
	var ops []string
	var current []string

	for _, t := range tokens {
		u := strings.ToUpper(t)
		if u == "AND" || u == "OR" {
			clauses = append(clauses, current)
			ops = append(ops, u)
			current = nil
		} else {
			current = append(current, t)
		}
	}
	if len(current) > 0 {
		clauses = append(clauses, current)
	}

	result := true
	for i, c := range clauses {
		m, err := evalSingleCondition(c, tbl, row, params, named, paramIdx)
		if err != nil {
			return false, err
		}
		if i == 0 {
			result = m
		} else {
			op := ops[i-1]
			if op == "AND" {
				result = result && m
			} else if op == "OR" {
				result = result || m
			}
		}
	}
	return result, nil
}

func evalSingleCondition(tokens []string, tbl *Table, row []Value, params []Value, named map[string]Value, paramIdx *int) (bool, error) {
	if len(tokens) < 3 {
		return false, fmt.Errorf("OperationalError: incomplete condition")
	}

	// Check for IS NULL / IS NOT NULL
	if len(tokens) >= 3 && strings.ToUpper(tokens[1]) == "IS" {
		colName := cleanIdentifier(tokens[0])
		ci, found := tbl.ColIndex[strings.ToLower(colName)]
		if !found {
			return false, fmt.Errorf("OperationalError: no such column: %s", colName)
		}
		colVal := row[ci]
		if strings.ToUpper(tokens[2]) == "NULL" {
			return colVal == nil, nil
		}
		if len(tokens) >= 4 && strings.ToUpper(tokens[2]) == "NOT" && strings.ToUpper(tokens[3]) == "NULL" {
			return colVal != nil, nil
		}
	}

	leftTok := tokens[0]
	op := tokens[1]
	rightTok := tokens[2]

	var leftVal Value
	colName := cleanIdentifier(leftTok)
	if ci, found := tbl.ColIndex[strings.ToLower(colName)]; found {
		leftVal = row[ci]
	} else {
		var err error
		leftVal, err = resolveValue(leftTok, params, named, paramIdx)
		if err != nil {
			return false, err
		}
	}

	var rightVal Value
	rightColName := cleanIdentifier(rightTok)
	if ci, found := tbl.ColIndex[strings.ToLower(rightColName)]; found {
		rightVal = row[ci]
	} else {
		var err error
		rightVal, err = resolveValue(rightTok, params, named, paramIdx)
		if err != nil {
			return false, err
		}
	}

	cmp := compareValues(leftVal, rightVal)
	switch op {
	case "=", "==":
		return cmp == 0, nil
	case "!=", "<>":
		return cmp != 0, nil
	case "<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">":
		return cmp > 0, nil
	case ">=":
		return cmp >= 0, nil
	case "LIKE", "like":
		sLeft := fmt.Sprint(leftVal)
		pattern := fmt.Sprint(rightVal)
		return matchLike(sLeft, pattern), nil
	default:
		return false, fmt.Errorf("OperationalError: unsupported operator: %s", op)
	}
}

func matchLike(text, pattern string) bool {
	tLower := strings.ToLower(text)
	pLower := strings.ToLower(pattern)
	pLower = strings.ReplaceAll(pLower, "%", "*")
	pLower = strings.ReplaceAll(pLower, "_", "?")
	return globMatch(tLower, pLower)
}

func globMatch(str, pattern string) bool {
	if pattern == "" {
		return str == ""
	}
	if pattern == "*" {
		return true
	}
	if len(str) == 0 {
		return strings.Trim(pattern, "*") == ""
	}
	if pattern[0] == '?' || pattern[0] == str[0] {
		return globMatch(str[1:], pattern[1:])
	}
	if pattern[0] == '*' {
		return globMatch(str[1:], pattern) || globMatch(str, pattern[1:])
	}
	return false
}

func compareValues(a, b Value) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}

	// Try numeric comparison
	fa, okA := toFloatVal(a)
	fb, okB := toFloatVal(b)
	if okA && okB {
		if math.Abs(fa-fb) < 1e-9 {
			return 0
		}
		if fa < fb {
			return -1
		}
		return 1
	}

	sa := fmt.Sprint(a)
	sb := fmt.Sprint(b)
	return strings.Compare(sa, sb)
}

func toFloatVal(v Value) (float64, bool) {
	switch val := v.(type) {
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func toFloat(v Value) float64 {
	f, _ := toFloatVal(v)
	return f
}

func toInt64(v Value) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case int32:
		return int64(val), true
	case float64:
		return int64(val), true
	case string:
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func sortRows(rows [][]Value, colIdx int, desc bool) [][]Value {
	sorted := append([][]Value(nil), rows...)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			cmp := compareValues(sorted[i][colIdx], sorted[j][colIdx])
			if (desc && cmp < 0) || (!desc && cmp > 0) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

func containsRow(rows [][]Value, row []Value) bool {
	for _, r := range rows {
		if len(r) != len(row) {
			continue
		}
		match := true
		for i := range r {
			if compareValues(r[i], row[i]) != 0 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
