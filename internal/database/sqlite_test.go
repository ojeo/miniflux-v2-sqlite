// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package database

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

// newTestDB boots an on-disk SQLite database with the full schema so the
// read-only reader pool can be exercised against a real migrated database.
func newTestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "sqlite_reader_test.db")
	db, err := NewConnectionPool(dsn, 1, 1, 0)
	if err != nil {
		t.Fatalf("unable to open sqlite database: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dsn, db
}

func TestNewReaderConnectionPool(t *testing.T) {
	dsn, db := newTestDB(t)

	reader, err := NewReaderConnectionPool(dsn, 8)
	if err != nil {
		t.Fatalf("NewReaderConnectionPool: %v", err)
	}
	defer reader.Close()

	if err := reader.Ping(); err != nil {
		t.Fatalf("reader ping: %v", err)
	}

	// The reader must be able to read the migrated schema.
	var version int
	if err := reader.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("reader query: %v", err)
	}
	if version == 0 {
		t.Fatal("expected a non-zero schema version")
	}

	// The writer must still be able to write while the reader pool exists.
	if _, err := db.Exec(`INSERT INTO users (username, password) VALUES ('reader-test', 'x')`); err != nil {
		t.Fatalf("writer exec: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO categories (user_id, title, hide_globally) VALUES (1, 'reader-test', 0)`); err != nil {
		t.Fatalf("writer exec: %v", err)
	}
}

func TestNewReaderConnectionPoolIsReadOnly(t *testing.T) {
	dsn, _ := newTestDB(t)

	reader, err := NewReaderConnectionPool(dsn, 2)
	if err != nil {
		t.Fatalf("NewReaderConnectionPool: %v", err)
	}
	defer reader.Close()

	// A write through the reader pool must fail (mode=ro + query_only).
	if _, err := reader.Exec(`INSERT INTO categories (user_id, title, hide_globally) VALUES (1, 'nope', 0)`); err == nil {
		t.Fatal("expected the read-only reader pool to reject a write")
	}
}

func TestReaderPoolConcurrentReads(t *testing.T) {
	dsn, db := newTestDB(t)

	reader, err := NewReaderConnectionPool(dsn, 8)
	if err != nil {
		t.Fatalf("NewReaderConnectionPool: %v", err)
	}
	defer reader.Close()

	// Seed some data through the writer.
	if _, err := db.Exec(`INSERT INTO users (username, password) VALUES ('seed', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for i := 0; i < 50; i++ {
		if _, err := db.Exec(
			`INSERT INTO categories (user_id, title, hide_globally) VALUES (1, ?, 0)`,
			"cat-"+string(rune('0'+i)),
		); err != nil {
			t.Fatalf("seed write: %v", err)
		}
	}

	// Concurrent readers across the multi-connection reader pool must all
	// observe the committed rows without SQLITE_BUSY.
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count int
			if err := reader.QueryRow(`SELECT count(*) FROM categories`).Scan(&count); err != nil {
				errs <- err
				return
			}
			if count < 50 {
				errs <- &countMismatchError{expected: 50, got: count}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent reader: %v", err)
	}
}

type countMismatchError struct {
	expected int
	got      int
}

func (e *countMismatchError) Error() string {
	return "unexpected category count"
}
