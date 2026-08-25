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
//   - cache_size(-65536):  64 MiB page cache (negative value = KiB) to keep
//     hot entries/feed pages in memory and reduce disk reads.
//   - mmap_size(268435456): allow up to 256 MiB of the database to be
//     memory-mapped for faster reads.
//   - temp_store(memory):   keep temporary tables/indexes in RAM.
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

// NewReaderConnectionPool opens a dedicated read-only connection pool for
// SQLite.
//
// The read/write split exploits WAL's "one writer + many readers" model: the
// single writer pool handles all INSERT/UPDATE/DELETE/transactions, while this
// pool serves SELECT / COUNT / page reads concurrently from WAL snapshots.
//
// The pool is opened with mode=ro so it can never acquire the write lock, and
// query_only(1) as an extra safety net against accidental writes.  Multiple
// connections are allowed because readers never contend with each other or
// with the single writer under WAL.
func NewReaderConnectionPool(dsn string, maxConns int) (*sql.DB, error) {
	readerDSN := buildReaderSQLiteDSN(dsn)

	db, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		return nil, err
	}

	if maxConns <= 0 {
		maxConns = 1
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(0)

	return db, nil
}

// basePragmas are applied to every SQLite connection via the DSN. They are
// shared by the single-writer pool and, when read/write splitting is enabled,
// the read-only reader pool.
const basePragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(memory)"

// buildSQLiteDSN appends the required PRAGMAs to the user supplied DSN. The DSN
// is normally a simple file path (e.g. "miniflux.db") or ":memory:".
func buildSQLiteDSN(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + basePragmas
	}
	return dsn + "?" + basePragmas
}

// buildReaderSQLiteDSN builds the DSN for the read-only reader pool.
//
// journal_mode(WAL) and synchronous are intentionally omitted: the journal mode
// is a persistent property of the database file (set by the writer pool) and a
// read-only connection cannot change it.  mode=ro is a URI parameter (not a
// PRAGMA) that forbids acquiring the write lock, enabling genuine concurrent
// reads with the writer.
func buildReaderSQLiteDSN(dsn string) string {
	const readerPragmas = "_pragma=query_only(1)&_pragma=busy_timeout(30000)&_pragma=cache_size(-65536)&_pragma=mmap_size(268435456)&_pragma=temp_store(memory)"

	if dsn == ":memory:" {
		// An in-memory database is per-connection; there is no shared file to
		// read concurrently, so fall back to the plain DSN.
		return dsn
	}

	// If the DSN is already a file: URI (e.g. "file:miniflux.db" or
	// "file:/data/db?param"), append mode=ro to its query string.
	if strings.HasPrefix(dsn, "file:") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "mode=ro&" + readerPragmas
	}

	// Plain path (e.g. "miniflux.db" or "/data/miniflux.db"). Normalise it to a
	// file: URI so that the mode=ro URI parameter is honoured. Existing query
	// parameters, if any, are preserved.
	path := dsn
	sep := "?"
	if i := strings.Index(dsn, "?"); i >= 0 {
		path = dsn[:i]
		sep = "?" + dsn[i+1:] + "&"
	}
	return "file:" + path + sep + "mode=ro&" + readerPragmas
}
