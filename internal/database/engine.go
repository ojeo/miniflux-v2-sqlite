// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package database // import "miniflux.app/v2/internal/database"

import (
	"database/sql"
	"time"
)

// Engine abstracts database-specific connection and migration configuration so
// that the SQLite implementation is isolated behind a defined interface.  Today
// only SQLite is implemented, but the existence of this interface makes it
// possible to re-introduce PostgreSQL support (or add a different backend)
// without changing the storage layer.
type Engine interface {
	// DriverName returns the string used with sql.Open (e.g. "sqlite").
	DriverName() string

	// Open creates and configures a *sql.DB connection pool.  Parameters
	// match the existing NewConnectionPool signature so callers that
	// already pass minConns / maxConns / lifetime continue to work.
	Open(dsn string, minConnections, maxConnections int, connectionLifetime time.Duration) (*sql.DB, error)
}

// Ensure the SQLite implementation satisfies the Engine interface.
var _ Engine = (*SQLite)(nil)

// SQLite implements Engine for the modernc.org/sqlite driver.
type SQLite struct{}

// DriverName implements Engine.
func (s SQLite) DriverName() string { return "sqlite" }

// Open implements Engine.
func (s SQLite) Open(dsn string, minConnections, maxConnections int, connectionLifetime time.Duration) (*sql.DB, error) {
	return NewConnectionPool(dsn, minConnections, maxConnections, connectionLifetime)
}
