// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"miniflux.app/v2/internal/model"
)

// entryPaginationBuilder is a builder for entry prev/next queries.
type entryPaginationBuilder struct {
	db         *sql.DB
	conditions []string
	args       []any
	entryID    int64
	order      string
	direction  string
}

// WithSearchQuery adds full-text search query to the condition.
func (e *entryPaginationBuilder) WithSearchQuery(query string) *entryPaginationBuilder {
	if query != "" {
		e.conditions = append(e.conditions, fmt.Sprintf("e.id IN (SELECT rowid FROM entries_fts WHERE entries_fts MATCH ?%d)", len(e.args)+1))
		e.args = append(e.args, query)
	}

	return e
}

// WithStarred adds starred to the condition.
func (e *entryPaginationBuilder) WithStarred() *entryPaginationBuilder {
	e.conditions = append(e.conditions, "e.starred is 1")

	return e
}

// WithFeedID adds feed_id to the condition.
func (e *entryPaginationBuilder) WithFeedID(feedID int64) *entryPaginationBuilder {
	if feedID != 0 {
		e.conditions = append(e.conditions, "e.feed_id = ?"+strconv.Itoa(len(e.args)+1))
		e.args = append(e.args, feedID)
	}

	return e
}

// WithCategoryID adds category_id to the condition.
func (e *entryPaginationBuilder) WithCategoryID(categoryID int64) *entryPaginationBuilder {
	if categoryID != 0 {
		e.conditions = append(e.conditions, "f.category_id = ?"+strconv.Itoa(len(e.args)+1))
		e.args = append(e.args, categoryID)
	}

	return e
}

// WithStatus adds status to the condition.
func (e *entryPaginationBuilder) WithStatus(status string) *entryPaginationBuilder {
	if status != "" {
		e.conditions = append(e.conditions, "e.status = ?"+strconv.Itoa(len(e.args)+1))
		e.args = append(e.args, status)
	}

	return e
}

// WithStatusOrEntryID adds a status condition that always includes a specific entry ID.
func (e *entryPaginationBuilder) WithStatusOrEntryID(status string, entryID int64) *entryPaginationBuilder {
	if status == "" {
		return e
	}

	if entryID == 0 {
		e.WithStatus(status)
		return e
	}

	statusArg := len(e.args) + 1
	entryArg := len(e.args) + 2
	e.conditions = append(e.conditions, fmt.Sprintf("(e.status = ?%d OR e.id = ?%d)", statusArg, entryArg))
	e.args = append(e.args, status, entryID)

	return e
}

func (e *entryPaginationBuilder) WithTags(tags []string) *entryPaginationBuilder {
	if len(tags) > 0 {
		for _, tag := range tags {
			e.conditions = append(e.conditions, fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(e.tags) WHERE json_each.value = ?%d)", len(e.args)+1))
			e.args = append(e.args, tag)
		}
	}

	return e
}

// WithGloballyVisible adds global visibility to the condition.
func (e *entryPaginationBuilder) WithGloballyVisible() *entryPaginationBuilder {
	e.conditions = append(e.conditions, "not c.hide_globally")
	e.conditions = append(e.conditions, "not f.hide_globally")

	return e
}

// Entries returns previous and next entries.
func (e *entryPaginationBuilder) Entries() (*model.Entry, *model.Entry, error) {
	tx, err := e.db.Begin()
	if err != nil {
		return nil, nil, fmt.Errorf("begin transaction for entry pagination: %v", err)
	}

	prevID, nextID, err := e.getPrevNextID(tx)
	if err != nil {
		tx.Rollback()
		return nil, nil, err
	}

	prevEntry, err := e.getEntry(tx, prevID)
	if err != nil {
		tx.Rollback()
		return nil, nil, err
	}

	nextEntry, err := e.getEntry(tx, nextID)
	if err != nil {
		tx.Rollback()
		return nil, nil, err
	}

	tx.Commit()

	if e.direction == "desc" {
		return nextEntry, prevEntry, nil
	}

	return prevEntry, nextEntry, nil
}

// getPrevNextID finds the previous and next entry IDs using key-based
// pagination instead of lag()/lead() window functions.
//
// The original implementation used a CTE with lag()/lead() OVER (ORDER BY
// ...) which forced SQLite to materialise the entire result set of all
// matching entries (e.g. 12 k+ read entries) in a temporary B-tree before
// looking up the single row of interest.
//
// The key-based approach:
//  1. Fetches the current entry's sort-key values (the order column,
//     created_at, id) with a single indexed lookup.
//  2. Runs two independent LIMIT 1 queries — one for the predecessor
//     and one for the successor — that use the composite
//     user_id + status + order index to seek directly to the correct
//     neighbourhood without scanning the full table.
func (e *entryPaginationBuilder) getPrevNextID(tx *sql.Tx) (prevID int64, nextID int64, err error) {
	// Step 1 – retrieve the sort-key values of the current entry.
	var orderVal, createdAt string
	var curID int64
	keyQuery := fmt.Sprintf(`SELECT e.%s, e.created_at, e.id FROM entries e WHERE e.id = ?1`, e.order)
	if err := tx.QueryRow(keyQuery, e.entryID).Scan(&orderVal, &createdAt, &curID); err != nil {
		return 0, 0, fmt.Errorf("entry pagination: unable to get sort keys for entry %d: %v", e.entryID, err)
	}

	subCondition := strings.Join(e.conditions, " AND ")

	// Step 2 – predecessor: the closest entry whose sort key is strictly
	// smaller than the current entry's (in the ORDER BY used by the old
	// CTE: e.{order} ASC, e.created_at ASC, e.id DESC).
	//
	// The three OR branches express the lexicographic comparison:
	//   (a < A) OR (a = A AND b < B) OR (a = A AND b = B AND c (> for prev) C)
	prevSQL := fmt.Sprintf(`
		SELECT e.id FROM entries AS e
		JOIN feeds AS f ON f.id = e.feed_id
		JOIN categories c ON c.id = f.category_id
		WHERE %s AND (
			e.%s < ?%d
			OR (e.%s = ?%d AND e.created_at < ?%d)
			OR (e.%s = ?%d AND e.created_at = ?%d AND e.id > ?%d)
		)
		ORDER BY e.%s DESC, e.created_at DESC, e.id ASC
		LIMIT 1`,
		subCondition,
		e.order, len(e.args)+1,
		e.order, len(e.args)+2, len(e.args)+3,
		e.order, len(e.args)+4, len(e.args)+5, len(e.args)+6,
		e.order)

	prevArgs := append(e.args,
		orderVal,
		orderVal, createdAt,
		orderVal, createdAt, curID,
	)

	var pID sql.NullInt64
	if err := tx.QueryRow(prevSQL, prevArgs...).Scan(&pID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("entry pagination: prev query: %v", err)
	}

	// Step 3 – successor: the closest entry whose sort key is strictly
	// greater than the current entry's.
	nextSQL := fmt.Sprintf(`
		SELECT e.id FROM entries AS e
		JOIN feeds AS f ON f.id = e.feed_id
		JOIN categories c ON c.id = f.category_id
		WHERE %s AND (
			e.%s > ?%d
			OR (e.%s = ?%d AND e.created_at > ?%d)
			OR (e.%s = ?%d AND e.created_at = ?%d AND e.id < ?%d)
		)
		ORDER BY e.%s ASC, e.created_at ASC, e.id DESC
		LIMIT 1`,
		subCondition,
		e.order, len(e.args)+1,
		e.order, len(e.args)+2, len(e.args)+3,
		e.order, len(e.args)+4, len(e.args)+5, len(e.args)+6,
		e.order)

	var nID sql.NullInt64
	if err := tx.QueryRow(nextSQL, prevArgs...).Scan(&nID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("entry pagination: next query: %v", err)
	}

	if pID.Valid {
		prevID = pID.Int64
	}
	if nID.Valid {
		nextID = nID.Int64
	}

	return prevID, nextID, nil
}

func (e *entryPaginationBuilder) getEntry(tx *sql.Tx, entryID int64) (*model.Entry, error) {
	var entry model.Entry

	err := tx.QueryRow(`SELECT id, title FROM entries WHERE id = ?1`, entryID).Scan(
		&entry.ID,
		&entry.Title,
	)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("fetching sibling entry: %v", err)
	}

	return &entry, nil
}

// NewEntryPaginationBuilder returns a new EntryPaginationBuilder.
func (s *Storage) NewEntryPaginationBuilder(userID, entryID int64, order, direction string) *entryPaginationBuilder {
	return &entryPaginationBuilder{
		db:         s.reader(),
		args:       []any{userID},
		conditions: []string{"e.user_id = ?1"},
		entryID:    entryID,
		order:      quoteIdent(order),
		direction:  direction,
	}
}
