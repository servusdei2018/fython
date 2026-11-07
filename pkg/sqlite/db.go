package sqlite

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Value represents a dynamic SQLite value: nil, int64, float64, string, []byte, bool.
type Value = any

// Column represents a column metadata in a table.
type Column struct {
	Name       string
	Type       string
	PrimaryKey bool
	AutoInc    bool
}

// Table holds in-memory rows and schema for a relational table.
type Table struct {
	Name        string
	Columns     []Column
	ColIndex    map[string]int
	Rows        [][]Value
	NextAutoInc int64
}

// DB represents an embedded SQLite database instance (in-memory or file-backed).
type DB struct {
	mu           sync.RWMutex
	path         string
	tables       map[string]*Table
	snapshot     map[string]*Table // for transaction rollback
	inTx         bool
	autoCommit   bool
	lastInsertID int64
	rowsAffected int64
}

// Result represents the outcome of an executed SQL query or command.
type Result struct {
	Columns      []string
	Rows         [][]Value
	LastInsertID int64
	RowsAffected int64
}

// Open creates or opens a database at path (":memory:" for transient in-memory).
func Open(path string) (*DB, error) {
	db := &DB{
		path:       path,
		tables:     make(map[string]*Table),
		autoCommit: true,
	}

	if path != "" && path != ":memory:" {
		if err := db.loadFile(); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	return db, nil
}

// Close flushes data to disk if file-backed and closes the database.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.path != "" && db.path != ":memory:" {
		return db.saveFile()
	}
	return nil
}

type fileStorage struct {
	Tables map[string]*TableData `json:"tables"`
}

type TableData struct {
	Name        string    `json:"name"`
	Columns     []Column  `json:"columns"`
	Rows        [][]Value `json:"rows"`
	NextAutoInc int64     `json:"next_auto_inc"`
}

func (db *DB) loadFile() error {
	data, err := os.ReadFile(db.path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var storage fileStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return err
	}
	for name, td := range storage.Tables {
		tbl := &Table{
			Name:        td.Name,
			Columns:     td.Columns,
			ColIndex:    make(map[string]int),
			Rows:        td.Rows,
			NextAutoInc: td.NextAutoInc,
		}
		for i, c := range td.Columns {
			tbl.ColIndex[strings.ToLower(c.Name)] = i
		}
		db.tables[strings.ToLower(name)] = tbl
	}
	return nil
}

func (db *DB) saveFile() error {
	storage := fileStorage{Tables: make(map[string]*TableData)}
	for name, tbl := range db.tables {
		storage.Tables[name] = &TableData{
			Name:        tbl.Name,
			Columns:     tbl.Columns,
			Rows:        tbl.Rows,
			NextAutoInc: tbl.NextAutoInc,
		}
	}
	data, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(db.path, data, 0644)
}

func (db *DB) cloneTables() map[string]*Table {
	clone := make(map[string]*Table, len(db.tables))
	for name, tbl := range db.tables {
		newTbl := &Table{
			Name:        tbl.Name,
			Columns:     append([]Column(nil), tbl.Columns...),
			ColIndex:    make(map[string]int, len(tbl.ColIndex)),
			Rows:        make([][]Value, len(tbl.Rows)),
			NextAutoInc: tbl.NextAutoInc,
		}
		for k, v := range tbl.ColIndex {
			newTbl.ColIndex[k] = v
		}
		for i, r := range tbl.Rows {
			newTbl.Rows[i] = append([]Value(nil), r...)
		}
		clone[name] = newTbl
	}
	return clone
}

// Begin starts an explicit transaction.
func (db *DB) Begin() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.inTx {
		return fmt.Errorf("OperationalError: cannot start a transaction within a transaction")
	}
	db.snapshot = db.cloneTables()
	db.inTx = true
	return nil
}

// Commit commits the active transaction.
func (db *DB) Commit() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if !db.inTx {
		return nil
	}
	db.snapshot = nil
	db.inTx = false
	if db.path != "" && db.path != ":memory:" {
		return db.saveFile()
	}
	return nil
}

// Rollback rolls back changes made since the transaction started.
func (db *DB) Rollback() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if !db.inTx {
		return nil
	}
	if db.snapshot != nil {
		db.tables = db.snapshot
		db.snapshot = nil
	}
	db.inTx = false
	return nil
}

// InTransaction reports whether a transaction is active.
func (db *DB) InTransaction() bool {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.inTx
}

// LastInsertID returns the ROWID of the most recently inserted row.
func (db *DB) LastInsertID() int64 {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.lastInsertID
}

// RowsAffected returns the number of rows modified by the most recent statement.
func (db *DB) RowsAffected() int64 {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.rowsAffected
}
