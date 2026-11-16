// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"miniflux.app/v2/internal/config"
)

// fullTextSearchTable is the FTS5 virtual table backing full-text search.
// Its shadow tables (entries_fts_data, entries_fts_idx, entries_fts_docsize,
// entries_fts_config) are created and dropped automatically by SQLite.
const fullTextSearchTable = "entries_fts"

// createFullTextSearchStatements recreates the FTS5 virtual table and the
// triggers that keep it in sync with the entries table. They mirror the
// definitions used by the initial migration.
var createFullTextSearchStatements = []string{
	`CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
		title, content, author, tags,
		content='entries', content_rowid='id',
		tokenize='unicode61'
	)`,

	`CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
		INSERT INTO entries_fts(rowid, title, content, author, tags)
		VALUES (new.id, new.title, new.content, new.author, new.tags);
	END`,

	`CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
		INSERT INTO entries_fts(entries_fts, rowid, title, content, author, tags)
		VALUES ('delete', old.id, old.title, old.content, old.author, old.tags);
	END`,

	`CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
		INSERT INTO entries_fts(entries_fts, rowid, title, content, author, tags)
		VALUES ('delete', old.id, old.title, old.content, old.author, old.tags);
		INSERT INTO entries_fts(rowid, title, content, author, tags)
		VALUES (new.id, new.title, new.content, new.author, new.tags);
	END`,
}

// FullTextSearchIndexExists reports whether the FTS5 virtual table exists.
func (s *Storage) FullTextSearchIndexExists() (bool, error) {
	var one int
	err := s.db.QueryRow(
		`SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = ?1`,
		fullTextSearchTable,
	).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("storage: unable to check full-text search index: %w", err)
	}
	return true, nil
}

// DropFullTextSearchIndex removes the FTS triggers and the FTS5 virtual table
// (cascading to its shadow tables), then runs VACUUM to reclaim the freed disk
// space. SQLite does not shrink the database file on its own.
func (s *Storage) DropFullTextSearchIndex() error {
	statements := []string{
		`DROP TRIGGER IF EXISTS entries_ai`,
		`DROP TRIGGER IF EXISTS entries_ad`,
		`DROP TRIGGER IF EXISTS entries_au`,
		`DROP TABLE IF EXISTS entries_fts`,
	}

	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("storage: unable to drop full-text search index: %w", err)
		}
	}

	// Reclaiming disk space is best-effort: a failed VACUUM must not prevent
	// the application from starting, the index itself is already gone.
	if err := s.Vacuum(); err != nil {
		slog.Warn("Unable to vacuum database after dropping full-text search index",
			slog.Any("error", err),
		)
	}

	return nil
}

// CreateFullTextSearchIndex creates the FTS5 virtual table and the sync
// triggers, then rebuilds the index from the entries table. It is used when
// full-text search is (re-)enabled.
func (s *Storage) CreateFullTextSearchIndex() error {
	for _, stmt := range createFullTextSearchStatements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("storage: unable to create full-text search index: %w", err)
		}
	}

	// Populate the index from the external content table.
	if _, err := s.db.Exec(`INSERT INTO entries_fts(entries_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("storage: unable to rebuild full-text search index: %w", err)
	}

	return nil
}

// ReconcileFullTextSearchIndex makes the database match the configured
// DISABLE_FULL_TEXT_SEARCH setting:
//
//   - when full-text search is disabled and the index exists, it is dropped
//     (and VACUUM runs to reclaim disk space);
//   - when full-text search is enabled and the index is missing, it is created
//     and rebuilt from the entries table.
//
// It is idempotent and safe to call at startup. When the configuration is not
// initialised (e.g. in unit tests), it is a no-op.
func (s *Storage) ReconcileFullTextSearchIndex() error {
	if config.Opts == nil {
		return nil
	}

	exists, err := s.FullTextSearchIndexExists()
	if err != nil {
		return err
	}

	switch {
	case !config.Opts.HasFullTextSearch() && exists:
		slog.Info("Full-text search is disabled; dropping the FTS index")
		if err := s.DropFullTextSearchIndex(); err != nil {
			return err
		}
		slog.Info("Full-text search index dropped")
	case config.Opts.HasFullTextSearch() && !exists:
		slog.Info("Full-text search is enabled; creating the FTS index")
		if err := s.CreateFullTextSearchIndex(); err != nil {
			return err
		}
		slog.Info("Full-text search index created")
	}

	return nil
}
