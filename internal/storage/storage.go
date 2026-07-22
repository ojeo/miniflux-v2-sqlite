// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Storage handles all operations related to the database.
type Storage struct {
	db *sql.DB
}

// NewStorage returns a new Storage.
func NewStorage(db *sql.DB) *Storage {
	return &Storage{db}
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
