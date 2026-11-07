package sqlite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteMemoryBasic(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// CREATE TABLE
	_, err = db.Execute("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, balance REAL)", nil, nil)
	if err != nil {
		t.Fatalf("CREATE TABLE failed: %v", err)
	}

	// Positional insert
	_, err = db.Execute("INSERT INTO users (name, balance) VALUES (?, ?)", []Value{"Alice", 100.5}, nil)
	if err != nil {
		t.Fatalf("INSERT positional failed: %v", err)
	}

	// Named insert
	_, err = db.Execute("INSERT INTO users (name, balance) VALUES (:name, :balance)", nil, map[string]Value{
		"name":    "Bob",
		"balance": 200.0,
	})
	if err != nil {
		t.Fatalf("INSERT named failed: %v", err)
	}

	// SELECT
	res, err := db.Execute("SELECT id, name, balance FROM users ORDER BY id", nil, nil)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}
	if res.Rows[0][1] != "Alice" || res.Rows[1][1] != "Bob" {
		t.Fatalf("unexpected rows: %v", res.Rows)
	}

	// Aggregates
	res, err = db.Execute("SELECT COUNT(*), SUM(balance), AVG(balance) FROM users", nil, nil)
	if err != nil {
		t.Fatalf("aggregate SELECT failed: %v", err)
	}
	if res.Rows[0][0] != int64(2) {
		t.Fatalf("expected count 2, got %v", res.Rows[0][0])
	}
}

func TestSQLiteTransactions(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	_, err = db.Execute("CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT)", nil, nil)
	if err != nil {
		t.Fatalf("CREATE TABLE failed: %v", err)
	}

	_, err = db.Execute("INSERT INTO items (title) VALUES (?)", []Value{"Item 1"}, nil)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}
	if err := db.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Start transaction and rollback
	if err := db.Begin(); err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	_, err = db.Execute("INSERT INTO items (title) VALUES (?)", []Value{"Item 2"}, nil)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}
	if err := db.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	res, err := db.Execute("SELECT COUNT(*) FROM items", nil, nil)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if res.Rows[0][0] != int64(1) {
		t.Fatalf("expected 1 item after rollback, got %v", res.Rows[0][0])
	}
}

func TestSQLiteFilePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 1 failed: %v", err)
	}
	_, err = db1.Execute("CREATE TABLE data (key TEXT, val TEXT)", nil, nil)
	if err != nil {
		t.Fatalf("CREATE TABLE failed: %v", err)
	}
	_, err = db1.Execute("INSERT INTO data (key, val) VALUES (?, ?)", []Value{"k1", "v1"}, nil)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}
	if err := db1.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	db1.Close()

	// Verify file was written
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("file not found: %v", err)
	}

	// Reopen
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open 2 failed: %v", err)
	}
	defer db2.Close()

	res, err := db2.Execute("SELECT key, val FROM data", nil, nil)
	if err != nil {
		t.Fatalf("SELECT failed: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "k1" || res.Rows[0][1] != "v1" {
		t.Fatalf("unexpected persisted data: %v", res.Rows)
	}
}
