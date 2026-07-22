// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"miniflux.app/v2/internal/crypto"
	"miniflux.app/v2/internal/model"
)

// ErrEntryTombstoned is returned when an entry cannot be created because its
// (feed_id, hash) pair has a tombstone recording a prior deletion.
var ErrEntryTombstoned = errors.New("store: entry is tombstoned")

// inClauseInt64 builds a comma-separated list of `?N` placeholders and the
// matching argument slice for an IN (...) condition, starting at placeholder
// number `start`. This replaces PostgreSQL's `= ANY($N)` with a pq.Array.
func inClauseInt64(start int, ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?" + strconv.Itoa(start+i)
		args[i] = id
	}
	return strings.Join(placeholders, ", "), args
}

// CountAllEntries returns the number of entries for each status in the database.
func (s *Storage) CountAllEntries() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT status, count(*) FROM entries GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("storage: unable to count entries: %w", err)
	}
	defer rows.Close()

	results := make(map[string]int64)
	results[model.EntryStatusUnread] = 0
	results[model.EntryStatusRead] = 0

	for rows.Next() {
		var status string
		var count int64

		if err := rows.Scan(&status, &count); err != nil {
			continue
		}

		results[status] = count
	}

	results["total"] = results[model.EntryStatusUnread] + results[model.EntryStatusRead]
	return results, nil
}

// UpdateEntryTitleAndContent updates entry title and content.
func (s *Storage) UpdateEntryTitleAndContent(entry *model.Entry) error {
	query := `
		UPDATE
			entries
		SET
			title=?1,
			content=?2,
			reading_time=?3
		WHERE
			id=?4 AND user_id=?5
	`

	if _, err := s.db.Exec(
		query,
		entry.Title,
		entry.Content,
		entry.ReadingTime,
		entry.ID,
		entry.UserID); err != nil {
		return fmt.Errorf(`store: unable to update entry #%d: %v`, entry.ID, err)
	}

	return nil
}

// createEntry add a new entry.
func (s *Storage) createEntry(tx *sql.Tx, entry *model.Entry) error {
	// The WHERE NOT EXISTS guard makes the tombstone check atomic with the insert, so a
	// concurrent archive committing between an earlier existence check and this statement
	// cannot bring a deleted entry back as unread.
	//
	// published_at is stored as RFC3339 UTC TEXT.  Time comparisons use
	// CAST(strftime('%s', published_at) AS INTEGER) so that both RFC3339
	// and time.Time.String() formats produce the correct Unix timestamp.
	query := `
		INSERT INTO entries
			(
				title,
				hash,
				url,
				comments_url,
				published_at,
				content,
				author,
				user_id,
				feed_id,
			reading_time,
			changed_at,
			tags,
			language,
			starred
		)
	SELECT
		?1,
		?2,
		?3,
		?4,
		?5,
		?6,
		?7,
		?8,
		?9,
		?10,
		strftime('%Y-%m-%dT%H:%M:%SZ','now'),
		?11,
		?12,
		?13
		WHERE NOT EXISTS (
			SELECT 1 FROM entry_tombstones WHERE feed_id=?9 AND hash=?2
		)
		RETURNING
			id, status, created_at, changed_at
	`
	// Normalise the published date to UTC so that TEXT comparisons against
	// the stored column are reliable.  The original timezone is preserved
	// via the user's tz setting when formatting for display.
	publishedAt := entry.Date.UTC().Format(time.RFC3339)

	var createdAt, changedAt model.Time
	err := tx.QueryRow(
		query,
		entry.Title,
		entry.Hash,
		entry.URL,
		entry.CommentsURL,
		publishedAt,
		entry.Content,
		entry.Author,
		entry.UserID,
		entry.FeedID,
		entry.ReadingTime,
		model.TagsValue(entry.Tags),
		entry.Language,
		entry.Starred,
	).Scan(
		&entry.ID,
		&entry.Status,
		&createdAt,
		&changedAt,
	)
	entry.CreatedAt = createdAt.Time
	entry.ChangedAt = changedAt.Time
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEntryTombstoned
	}
	if err != nil {
		return fmt.Errorf(`store: unable to create entry %q (feed #%d): %v`, entry.URL, entry.FeedID, err)
	}

	for _, enclosure := range entry.Enclosures {
		enclosure.EntryID = entry.ID
		enclosure.UserID = entry.UserID
		err := s.createEnclosure(tx, enclosure)
		if err != nil {
			return err
		}
	}

	return nil
}

// updateEntry updates an entry when a feed is refreshed.
// Note: we do not update the published date because some feeds do not contains any date,
// it default to time.Now() which could change the order of items on the history page.
func (s *Storage) updateEntry(tx *sql.Tx, entry *model.Entry) error {
	query := `
		UPDATE
			entries
		SET
			title=?1,
			url=?2,
			comments_url=?3,
			content=?4,
			author=?5,
			reading_time=?6,
			tags=?10,
			language=?11,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE
			user_id=?7 AND feed_id=?8 AND hash=?9
		RETURNING
			id
	`
	err := tx.QueryRow(
		query,
		entry.Title,
		entry.URL,
		entry.CommentsURL,
		entry.Content,
		entry.Author,
		entry.ReadingTime,
		entry.UserID,
		entry.FeedID,
		entry.Hash,
		model.TagsValue(entry.Tags),
		entry.Language,
	).Scan(&entry.ID)
	if err != nil {
		return fmt.Errorf(`store: unable to update entry %q: %v`, entry.URL, err)
	}

	for _, enclosure := range entry.Enclosures {
		enclosure.UserID = entry.UserID
		enclosure.EntryID = entry.ID
	}

	return s.updateEnclosures(tx, entry)
}

// entryExists checks if an entry already exists based on its hash when refreshing a feed.
func (s *Storage) entryExists(tx *sql.Tx, entry *model.Entry) (bool, error) {
	var result int

	// Note: This query uses entries_feed_id_hash_key index (filtering on user_id is not necessary).
	err := tx.QueryRow(`SELECT 1 FROM entries WHERE feed_id=?1 AND hash=?2 LIMIT 1`, entry.FeedID, entry.Hash).Scan(&result)

	if err != nil && err != sql.ErrNoRows {
		return result != 0, fmt.Errorf(`store: unable to check if entry exists: %v`, err)
	}

	return result != 0, nil
}

func (s *Storage) getEntryIDByHash(tx *sql.Tx, feedID int64, entryHash string) (int64, error) {
	var entryID int64

	err := tx.QueryRow(
		`SELECT id FROM entries WHERE feed_id=?1 AND hash=?2 LIMIT 1`,
		feedID,
		entryHash,
	).Scan(&entryID)

	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf(`store: unable to fetch entry ID: %v`, err)
	}

	return entryID, nil
}

// InsertEntryForFeed inserts a single entry into a feed, optionally updating if it already exists.
// Returns true if a new entry was created, false if an existing one was reused.
func (s *Storage) InsertEntryForFeed(userID, feedID int64, entry *model.Entry) (bool, error) {
	entry.UserID = userID
	entry.FeedID = feedID

	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("store: unable to start transaction: %v", err)
	}
	defer tx.Rollback()

	entryID, err := s.getEntryIDByHash(tx, entry.FeedID, entry.Hash)
	if err != nil {
		return false, err
	}
	alreadyExistingEntry := entryID > 0

	if alreadyExistingEntry {
		entry.ID = entryID
	} else {
		if err := s.createEntry(tx, entry); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}

	return !alreadyExistingEntry, nil
}

func (s *Storage) IsNewEntry(feedID int64, entryHash string) bool {
	// An entry is new only if it is neither stored nor tombstoned; otherwise
	// callers (such as the crawler) would do expensive work on every refresh
	// for items that will be discarded.
	query := `
		SELECT
			EXISTS (
				SELECT 1 FROM entries WHERE feed_id=?1 AND hash=?2
			) OR EXISTS (
				SELECT 1 FROM entry_tombstones WHERE feed_id=?1 AND hash=?2
			)
	`
	var known bool
	s.db.QueryRow(query, feedID, entryHash).Scan(&known)
	return !known
}

func (s *Storage) GetReadTime(feedID int64, entryHash string) int {
	var result int

	// Note: This query uses entries_feed_id_hash_key index
	s.db.QueryRow(
		`SELECT
			reading_time
		FROM
			entries
		WHERE
			feed_id=?1 AND
			hash=?2
		`,
		feedID,
		entryHash,
	).Scan(&result)
	return result
}

// RefreshFeedEntries updates feed entries while refreshing a feed.
func (s *Storage) RefreshFeedEntries(userID, feedID int64, entries model.Entries, updateExistingEntries bool) (newEntries model.Entries, err error) {
	for _, entry := range entries {
		entry.UserID = userID
		entry.FeedID = feedID

		tx, err := s.db.Begin()
		if err != nil {
			return nil, fmt.Errorf(`store: unable to start transaction: %v`, err)
		}

		entryExists, err := s.entryExists(tx, entry)
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, fmt.Errorf(`store: unable to rollback transaction: %v (rolled back due to: %v)`, rollbackErr, err)
			}
			return nil, err
		}

		if entryExists {
			if updateExistingEntries {
				err = s.updateEntry(tx, entry)
			}
		} else {
			err = s.createEntry(tx, entry)
			switch {
			case errors.Is(err, ErrEntryTombstoned):
				err = nil
			case err == nil:
				newEntries = append(newEntries, entry)
			}
		}

		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				return nil, fmt.Errorf(`store: unable to rollback transaction: %v (rolled back due to: %v)`, rollbackErr, err)
			}
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf(`store: unable to commit transaction: %v`, err)
		}
	}

	return newEntries, nil
}

// ArchiveEntries deletes entries older than the given interval and records tombstones so they are not re-ingested.
//
// The original PostgreSQL implementation used a data-modifying CTE
// (WITH ... AS (DELETE ... RETURNING feed_id, hash) INSERT INTO tombstones ...)
// which SQLite does not support.  We split it into three statements wrapped in
// a single transaction so the SELECT → DELETE → INSERT is de-facto atomic
// under SQLite's single-writer model.
func (s *Storage) ArchiveEntries(status string, interval time.Duration, limit int) (int64, error) {
	if interval < 0 || limit <= 0 {
		return 0, nil
	}

	cutoff := time.Now().UTC().Add(-interval).Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf(`store: unable to start archive transaction: %v`, err)
	}
	defer tx.Rollback()

	// Step 1 – select candidates
	selectQuery := `
		SELECT id, feed_id, hash
		FROM entries
		WHERE
			status=?1 AND
			starred IS 0 AND
			share_code='' AND
			CAST(strftime('%s', created_at) AS INTEGER) < ?2
		ORDER BY created_at ASC
		LIMIT ?3
	`
	rows, err := tx.Query(selectQuery, status, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf(`store: unable to select %s entries to archive: %v`, status, err)
	}
	defer rows.Close()

	type tomb struct {
		feedID int64
		hash   string
	}
	var entryIDs []int64
	var tombstones []tomb
	for rows.Next() {
		var entryID int64
		var ts tomb
		if err := rows.Scan(&entryID, &ts.feedID, &ts.hash); err != nil {
			return 0, fmt.Errorf(`store: unable to scan entry for archiving: %v`, err)
		}
		entryIDs = append(entryIDs, entryID)
		if ts.hash != "" {
			tombstones = append(tombstones, ts)
		}
	}
	rows.Close()

	if len(entryIDs) == 0 {
		return 0, nil
	}

	// Step 2 – delete by primary key
	ph, idArgs := inClauseInt64(1, entryIDs)
	deleteQuery := `DELETE FROM entries WHERE id IN (` + ph + `)`
	result, err := tx.Exec(deleteQuery, idArgs...)
	if err != nil {
		return 0, fmt.Errorf(`store: unable to delete %s entries: %v`, status, err)
	}
	count, _ := result.RowsAffected()

	// Step 3 – insert tombstones (idempotent, entry_tombstones has a unique PK)
	for _, ts := range tombstones {
		tx.Exec(`INSERT OR IGNORE INTO entry_tombstones (feed_id, hash) VALUES (?1, ?2)`, ts.feedID, ts.hash)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf(`store: unable to commit archive transaction: %v`, err)
	}

	return count, nil
}

// SetEntriesStatus update the status of the given list of entries.
func (s *Storage) SetEntriesStatus(userID int64, entryIDs []int64, status string) error {
	placeholders, idArgs := inClauseInt64(3, entryIDs)
	query := `
		UPDATE
			entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE
			user_id=?2 AND
			id IN (` + placeholders + `)
	`
	args := append([]any{status, userID}, idArgs...)
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf(`store: unable to update entries statuses %v: %v`, entryIDs, err)
	}

	return nil
}

// SetEntriesStatusAndCountVisible updates the status of the given entries and returns how many are visible in global views.
//
// The original PostgreSQL implementation used a data-modifying CTE
// (WITH updated AS (UPDATE ... RETURNING feed_id) SELECT ...) which SQLite
// does not support.  We split it into two statements instead.
func (s *Storage) SetEntriesStatusAndCountVisible(userID int64, entryIDs []int64, status string) (int, error) {
	placeholders, idArgs := inClauseInt64(3, entryIDs)
	args := append([]any{status, userID}, idArgs...)

	updateQuery := `
		UPDATE entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE
			user_id=?2 AND
			id IN (` + placeholders + `)
	`
	if _, err := s.db.Exec(updateQuery, args...); err != nil {
		return 0, fmt.Errorf(`store: unable to update entries status %v: %v`, entryIDs, err)
	}

	selectQuery := `
		SELECT count(*)
		FROM entries e
			JOIN feeds f ON f.id = e.feed_id
			JOIN categories c ON c.id = f.category_id
		WHERE
			e.user_id=?2 AND
			e.id IN (` + placeholders + `) AND
			NOT f.hide_globally AND
			NOT c.hide_globally
	`
	var visible int
	if err := s.db.QueryRow(selectQuery, args...).Scan(&visible); err != nil {
		return 0, fmt.Errorf(`store: unable to count visible entries: %v`, err)
	}
	return visible, nil
}

// SetEntriesStarredState updates the starred state for the given list of entries.
func (s *Storage) SetEntriesStarredState(userID int64, entryIDs []int64, starred bool) error {
	placeholders, idArgs := inClauseInt64(3, entryIDs)
	query := `UPDATE entries SET starred=?1, changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE user_id=?2 AND id IN (` + placeholders + `)`
	args := append([]any{starred, userID}, idArgs...)
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf(`store: unable to update the starred state %v: %v`, entryIDs, err)
	}

	return nil
}

// ToggleStarred toggles entry starred value.
func (s *Storage) ToggleStarred(userID int64, entryID int64) error {
	query := `UPDATE entries SET starred = 1 - starred, changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE user_id=?1 AND id=?2`
	result, err := s.db.Exec(query, userID, entryID)
	if err != nil {
		return fmt.Errorf(`store: unable to toggle starred flag for entry #%d: %v`, entryID, err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(`store: unable to toggle starred flag for entry #%d: %v`, entryID, err)
	}

	if count == 0 {
		return errors.New(`store: nothing has been updated`)
	}

	return nil
}

// FlushHistory deletes all read entries (non-starred, non-shared) and records tombstones to prevent re-ingestion.
//
// The original PostgreSQL implementation used a data-modifying CTE
// (WITH deleted AS (DELETE ... RETURNING ...) INSERT ...) which SQLite
// does not support.  We split it into SELECT → DELETE → INSERT wrapped in
// a single transaction so the split is de-facto atomic under SQLite's
// single-writer model.
//
// VACUUM is delegated to the scheduled cleanup task (runCleanupTasks) so
// that frequent manual flushes do not trigger expensive full-db rebuilds.
func (s *Storage) FlushHistory(userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf(`store: unable to start flush transaction: %v`, err)
	}
	defer tx.Rollback()

	// Step 1 – select feed_id, hash of entries to delete
	selectQuery := `
		SELECT feed_id, hash FROM entries
		WHERE user_id=?1 AND status=?2 AND starred IS 0 AND share_code=''
	`
	rows, err := tx.Query(selectQuery, userID, model.EntryStatusRead)
	if err != nil {
		return fmt.Errorf(`store: unable to select entries for flush: %v`, err)
	}
	defer rows.Close()

	type tomb struct {
		feedID int64
		hash   string
	}
	var tombstones []tomb
	for rows.Next() {
		var ts tomb
		if err := rows.Scan(&ts.feedID, &ts.hash); err != nil {
			return fmt.Errorf(`store: unable to scan entry for flush: %v`, err)
		}
		if ts.hash != "" {
			tombstones = append(tombstones, ts)
		}
	}
	rows.Close()

	// Step 2 – delete
	deleteQuery := `DELETE FROM entries WHERE user_id=?1 AND status=?2 AND starred IS 0 AND share_code=''`
	if _, err := tx.Exec(deleteQuery, userID, model.EntryStatusRead); err != nil {
		return fmt.Errorf(`store: unable to flush history: %v`, err)
	}

	// Step 3 – insert tombstones
	for _, ts := range tombstones {
		tx.Exec(`INSERT OR IGNORE INTO entry_tombstones (feed_id, hash) VALUES (?1, ?2)`, ts.feedID, ts.hash)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(`store: unable to commit flush transaction: %v`, err)
	}

	return nil
}

// MarkAllAsRead updates all user entries to the read status.
func (s *Storage) MarkAllAsRead(userID int64) error {
	query := `UPDATE entries SET status=?1, changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE user_id=?2 AND status=?3`
	result, err := s.db.Exec(query, model.EntryStatusRead, userID, model.EntryStatusUnread)
	if err != nil {
		return fmt.Errorf(`store: unable to mark all entries as read: %v`, err)
	}

	count, _ := result.RowsAffected()
	slog.Debug("Marked all entries as read",
		slog.Int64("user_id", userID),
		slog.Int64("nb_entries", count),
	)

	return nil
}

// MarkAllAsReadBeforeDate updates all user entries to the read status before the given date.
// Uses CAST(strftime('%s', ...) AS INTEGER) so the comparison works correctly
// regardless of whether published_at was stored as RFC3339 or time.Time.String().
func (s *Storage) MarkAllAsReadBeforeDate(userID int64, before time.Time) error {
	query := `
		UPDATE
			entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE
			user_id=?2 AND status=?3 AND CAST(strftime('%s', published_at) AS INTEGER) < ?4
	`
	result, err := s.db.Exec(query, model.EntryStatusRead, userID, model.EntryStatusUnread, before.UTC().Unix())
	if err != nil {
		return fmt.Errorf(`store: unable to mark all entries as read before %s: %v`, before.Format(time.RFC3339), err)
	}
	count, _ := result.RowsAffected()
	slog.Debug("Marked all entries as read before date",
		slog.Int64("user_id", userID),
		slog.Int64("nb_entries", count),
		slog.String("before", before.Format(time.RFC3339)),
	)
	return nil
}

// MarkGloballyVisibleFeedsAsRead marks as read the unread entries that are
// visible in the global unread view, i.e. those belonging to a feed and a
// category that are both not hidden globally.
func (s *Storage) MarkGloballyVisibleFeedsAsRead(userID int64) error {
	query := `
		UPDATE
			entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		FROM
			feeds
			JOIN categories ON (categories.id = feeds.category_id)
		WHERE
			entries.feed_id = feeds.id
			AND entries.user_id=?2
			AND entries.status=?3
			AND feeds.hide_globally IS 0
			AND categories.hide_globally IS 0
	`
	result, err := s.db.Exec(query, model.EntryStatusRead, userID, model.EntryStatusUnread)
	if err != nil {
		return fmt.Errorf(`store: unable to mark globally visible feeds as read: %v`, err)
	}

	count, _ := result.RowsAffected()
	slog.Debug("Marked globally visible feed entries as read",
		slog.Int64("user_id", userID),
		slog.Int64("nb_entries", count),
	)

	return nil
}

// MarkFeedAsRead updates all feed entries to the read status.
// Uses CAST(strftime('%s', ...) AS INTEGER) for time comparison so the
// semantics are independent of the TEXT storage format.
func (s *Storage) MarkFeedAsRead(userID, feedID int64, before time.Time) error {
	query := `
		UPDATE
			entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		WHERE
			user_id=?2 AND feed_id=?3 AND status=?4 AND CAST(strftime('%s', published_at) AS INTEGER) < ?5
	`
	result, err := s.db.Exec(
		query,
		model.EntryStatusRead,
		userID,
		feedID,
		model.EntryStatusUnread,
		before.UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf(`store: unable to mark feed entries as read: %v`, err)
	}

	count, _ := result.RowsAffected()
	slog.Debug("Marked feed entries as read",
		slog.Int64("user_id", userID),
		slog.Int64("feed_id", feedID),
		slog.Int64("nb_entries", count),
	)

	return nil
}

// MarkCategoryAsRead updates all category entries to the read status.
func (s *Storage) MarkCategoryAsRead(userID, categoryID int64, before time.Time) error {
	query := `
		UPDATE
			entries
		SET
			status=?1,
			changed_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
		FROM
			feeds
		WHERE
			feed_id=feeds.id
		AND
			feeds.user_id=?2
		AND
			status=?3
		AND
			CAST(strftime('%s', published_at) AS INTEGER) < ?4
		AND
			feeds.category_id=?5
	`
	result, err := s.db.Exec(query, model.EntryStatusRead, userID, model.EntryStatusUnread, before.UTC().Unix(), categoryID)
	if err != nil {
		return fmt.Errorf(`store: unable to mark category entries as read: %v`, err)
	}

	count, _ := result.RowsAffected()
	slog.Debug("Marked category entries as read",
		slog.Int64("user_id", userID),
		slog.Int64("category_id", categoryID),
		slog.Int64("nb_entries", count),
		slog.String("before", before.Format(time.RFC3339)),
	)

	return nil
}

// EntryShareCode returns the share code of the provided entry.
// It generates a new one if not already defined.
func (s *Storage) EntryShareCode(userID int64, entryID int64) (shareCode string, err error) {
	query := `SELECT share_code FROM entries WHERE user_id=?1 AND id=?2`
	err = s.db.QueryRow(query, userID, entryID).Scan(&shareCode)
	if err != nil {
		err = fmt.Errorf(`store: unable to get share code for entry #%d: %v`, entryID, err)
		return
	}

	if shareCode == "" {
		shareCode = crypto.GenerateRandomStringHex(20)

		query = `UPDATE entries SET share_code = ?1 WHERE user_id=?2 AND id=?3`
		_, err = s.db.Exec(query, shareCode, userID, entryID)
		if err != nil {
			err = fmt.Errorf(`store: unable to set share code for entry #%d: %v`, entryID, err)
			return
		}
	}

	return
}

// UnshareEntry removes the share code for the given entry.
func (s *Storage) UnshareEntry(userID int64, entryID int64) (err error) {
	query := `UPDATE entries SET share_code='' WHERE user_id=?1 AND id=?2`
	_, err = s.db.Exec(query, userID, entryID)
	if err != nil {
		err = fmt.Errorf(`store: unable to remove share code for entry #%d: %v`, entryID, err)
	}
	return
}
