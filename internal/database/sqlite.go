// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package database // import "miniflux.app/v2/internal/database"

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// NewConnectionPool configures the database connection pool for SQLite.
//
// SQLite is a single-writer / multi-reader engine, so the connection pool is
// configured through PRAGMAs expressed in the DSN. Setting them here guarantees
// every pooled connection inherits the same behaviour:
//   - journal_mode(WAL):   allows concurrent readers while a writer is active and
//     is crash-safe; required for decent concurrency with the RSS fetcher.
//   - busy_timeout(30000): makes SQLite wait up to 30 seconds instead of
//     immediately returning "database is locked" under the single-writer model.
//   - foreign_keys(1):     enforces the ON DELETE CASCADE constraints declared in
//     the schema (SQLite disables FK enforcement by default).
//   - synchronous(NORMAL): safe with WAL and much faster than FULL.
//
// The pool is pinned to a single connection (MaxOpenConns=1) because SQLite
// serialises all writes through a global lock.  Opening multiple connections
// only causes goroutines on different connections to fight over that lock,
// triggering SQLITE_BUSY even with a generous busy_timeout.  A single
// connection lets SQLite handle serialisation internally with zero contention.
func NewConnectionPool(dsn string, minConnections, maxConnections int, connectionLifetime time.Duration) (*sql.DB, error) {
	sqliteDSN := buildSQLiteDSN(dsn)

	db, err := sql.Open("sqlite", sqliteDSN)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	return db, nil
}

// buildSQLiteDSN appends the required PRAGMAs to the user supplied DSN. The DSN
// is normally a simple file path (e.g. "miniflux.db") or ":memory:".
func buildSQLiteDSN(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	}
	return dsn + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
}
