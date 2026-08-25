// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"miniflux.app/v2/internal/config"
	"miniflux.app/v2/internal/database"
)

// Storage handles all operations related to the database.
type Storage struct {
	// db is the single-writer pool. All INSERT/UPDATE/DELETE and transactions
	// go through it (SQLite single-writer model).
	db *sql.DB

	// readerDB is an optional dedicated read-only pool used for page reads,
	// COUNT and navigation queries. It exploits WAL's "one writer + many
	// readers" model so that reads do not queue behind background RSS refresh
	// writes. It is only created for the SQLite backend.
	readerDB *sql.DB

	// writerMu serialises the batched feed-refresh write paths. The single
	// writer connection already serialises writes internally, but the lock
	// makes the queueing explicit and predictable: many RSS workers may fetch
	// concurrently, yet only one write transaction enters SQLite at a time.
	// Reads are unaffected (they use readerDB).
	writerMu sync.Mutex
}

// NewStorage returns a new Storage.
//
// The SQLite backend additionally opens a dedicated read-only connection pool
// (see SQLITE_READER_MAX_CONNS). This is an isolated SQLite optimisation: the
// caller keeps passing a single *sql.DB (the writer) and the Storage method
// signatures are unchanged. If the reader pool cannot be created (e.g. an
// in-memory database or an uninitialised config), the storage falls back to
// using the writer for reads, preserving the previous single-connection
// behaviour.
func NewStorage(db *sql.DB) *Storage {
	s := &Storage{db: db}
	s.initSQLiteReader(db)
	return s
}

// initSQLiteReader opens the read-only reader pool for SQLite. It is a no-op
// when config.Opts is not initialised or the DSN is an in-memory database.
func (s *Storage) initSQLiteReader(db *sql.DB) {
	if config.Opts == nil {
		return
	}

	dsn := config.Opts.DatabaseURL()
	readerConns := config.Opts.SQLiteReaderMaxConns()
	if readerConns <= 0 {
		// Read/write split disabled via SQLITE_READER_MAX_CONNS=0.
		return
	}
	if dsn == "" || dsn == ":memory:" {
		return
	}

	reader, err := database.NewReaderConnectionPool(dsn, readerConns)
	if err != nil {
		// Do not break startup: fall back to the writer for reads.
		slog.Warn("Unable to open SQLite reader pool; falling back to single connection",
			slog.Any("error", err),
		)
		return
	}

	// Sanity check that the reader can actually query the schema.
	if err := reader.Ping(); err != nil {
		reader.Close()
		slog.Warn("Unable to ping SQLite reader pool; falling back to single connection",
			slog.Any("error", err),
		)
		return
	}

	s.readerDB = reader
	slog.Info("SQLite read/write connection split enabled",
		slog.Int("reader_max_conns", config.Opts.SQLiteReaderMaxConns()),
	)
}

// Close releases the read-only reader pool. The writer pool is owned by the
// caller (CLI) and is closed separately. Close is safe to call even when the
// reader pool was not created.
func (s *Storage) Close() error {
	if s.readerDB != nil {
		if err := s.readerDB.Close(); err != nil {
			return err
		}
		s.readerDB = nil
	}
	return nil
}

// reader returns the connection to use for read-only queries: the dedicated
// reader pool when available, otherwise the writer pool.
func (s *Storage) reader() *sql.DB {
	if s.readerDB != nil {
		return s.readerDB
	}
	return s.db
}

// HasReaderDB reports whether the read/write split is active (i.e. a dedicated
// read-only pool was successfully opened). It is mainly useful for diagnostics
// and tests.
func (s *Storage) HasReaderDB() bool {
	return s.readerDB != nil
}

// DatabaseVersion returns the version of the database which is in use.
func (s *Storage) DatabaseVersion() string {
	var dbVersion string
	err := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&dbVersion)
	if err != nil {
		return err.Error()
	}

	return dbVersion
}

// Ping checks if the database connection works.
func (s *Storage) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return s.db.PingContext(ctx)
}

// DBStats returns database statistics.
func (s *Storage) DBStats() sql.DBStats {
	return s.db.Stats()
}

// DBSize returns how much size the database is using in a pretty way.
func (s *Storage) DBSize() (string, error) {
	var bytes int64

	err := s.db.QueryRow("SELECT (SELECT page_count FROM pragma_page_count) * (SELECT page_size FROM pragma_page_size)").Scan(&bytes)
	if err != nil {
		return "", err
	}

	return formatBytes(bytes), nil
}

// Vacuum rebuilds the database file to reclaim free space left after
// deletions. SQLite does not shrink files automatically.
func (s *Storage) Vacuum() error {
	_, err := s.db.Exec(`VACUUM`)
	return err
}

// VacuumIfNeeded conditionally runs VACUUM only when the freelist exceeds the
// given fraction of total pages.  threshold is in range (0, 1), e.g. 0.2 means
// "vacuum only when ≥ 20 % of pages are free".
//
// This avoids expensive full-db rebuilds after minor deletions while still
// reclaiming disk space when bulk cleanup has left significant free pages.
func (s *Storage) VacuumIfNeeded(threshold float64) error {
	if threshold <= 0 || threshold >= 1 {
		return s.Vacuum()
	}

	var pageCount, freelistCount int64
	if err := s.db.QueryRow("SELECT page_count FROM pragma_page_count").Scan(&pageCount); err != nil {
		return fmt.Errorf("storage: unable to read page_count: %w", err)
	}
	if err := s.db.QueryRow("SELECT freelist_count FROM pragma_freelist_count").Scan(&freelistCount); err != nil {
		return fmt.Errorf("storage: unable to read freelist_count: %w", err)
	}

	if pageCount == 0 || float64(freelistCount)/float64(pageCount) < threshold {
		return nil // not worth the I/O cost
	}

	return s.Vacuum()
}

// formatBytes renders a byte count as a human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
