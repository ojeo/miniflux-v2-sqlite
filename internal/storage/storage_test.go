// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"path/filepath"
	"testing"

	"miniflux.app/v2/internal/database"
	"miniflux.app/v2/internal/model"
)

// newTestStorage boots an on-disk SQLite database and runs the full migration
// so the storage layer is exercised against the real SQLite engine (not mocks).
func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "miniflux_test.db")
	db, err := database.NewConnectionPool(dsn, 1, 1, 0)
	if err != nil {
		t.Fatalf("unable to open sqlite database: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStorage(db)
}

func TestSQLiteMigrationAndEntryFlow(t *testing.T) {
	store := newTestStorage(t)

	// --- user ---
	user, err := store.CreateUser(&model.UserCreationRequest{
		Username: "alice",
		Password: "secret",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("expected a non-zero user ID from RETURNING")
	}

	// --- category (required by feeds) ---
	category, err := store.CreateCategory(user.ID, &model.CategoryCreationRequest{Title: "news"})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	// --- feed (exercises bool columns: crawler/disabled) ---
	feed := &model.Feed{
		UserID:  user.ID,
		FeedURL: "https://example.com/feed.xml",
		SiteURL: "https://example.com",
		Title:   "Example Feed",
		Crawler: true,
	}
	feed.WithCategoryID(category.ID)
	if err := store.CreateFeed(feed); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if feed.ID == 0 {
		t.Fatal("expected a non-zero feed ID from RETURNING")
	}

	// --- entries with tags + starred flag ---
	entryOne := model.NewEntry()
	entryOne.Hash = "hash-one"
	entryOne.Title = "Go 1.24 released"
	entryOne.URL = "https://example.com/1"
	entryOne.Content = "The SQLite migration is now complete and stable."
	entryOne.Author = "bob"
	entryOne.Status = model.EntryStatusUnread
	entryOne.Tags = []string{"golang", "database"}
	entryOne.Starred = true

	entryTwo := model.NewEntry()
	entryTwo.Hash = "hash-two"
	entryTwo.Title = "Unrelated headline"
	entryTwo.URL = "https://example.com/2"
	entryTwo.Content = "A story about cooking pasta."
	entryTwo.Status = model.EntryStatusUnread
	entryTwo.Tags = []string{"misc"}

	if _, err := store.InsertEntryForFeed(user.ID, feed.ID, entryOne); err != nil {
		t.Fatalf("InsertEntryForFeed (one): %v", err)
	}
	if _, err := store.InsertEntryForFeed(user.ID, feed.ID, entryTwo); err != nil {
		t.Fatalf("InsertEntryForFeed (two): %v", err)
	}
	if entryOne.ID == 0 || entryTwo.ID == 0 {
		t.Fatal("expected non-zero entry IDs from RETURNING")
	}

	// --- FTS5 full-text search (replacement for PG tsvector) ---
	searchResults, err := store.NewEntryQueryBuilder(user.ID).WithSearchQuery("migration").GetEntries()
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(searchResults) != 1 {
		t.Fatalf("FTS5 search expected 1 match, got %d", len(searchResults))
	}
	if searchResults[0].Title != entryOne.Title {
		t.Fatalf("FTS5 returned wrong entry: %q", searchResults[0].Title)
	}

	// --- tags filter via json_each (replacement for PG text[] @>) ---
	tagResults, err := store.NewEntryQueryBuilder(user.ID).WithTags("golang").GetEntries()
	if err != nil {
		t.Fatalf("tags filter: %v", err)
	}
	if len(tagResults) != 1 {
		t.Fatalf("tag filter expected 1 match, got %d", len(tagResults))
	}
	if len(tagResults[0].Tags) != 2 {
		t.Fatalf("StringArrayScanner did not round-trip tags, got %v", tagResults[0].Tags)
	}

	// --- starred filter + BoolScanner (INTEGER -> bool) ---
	starredResults, err := store.NewEntryQueryBuilder(user.ID).WithStarred(true).GetEntries()
	if err != nil {
		t.Fatalf("starred filter: %v", err)
	}
	if len(starredResults) != 1 {
		t.Fatalf("starred filter expected 1 match, got %d", len(starredResults))
	}
	if !starredResults[0].Starred {
		t.Fatal("BoolScanner did not decode starred=1 as true")
	}

	// --- ToggleStarred exercises `starred = 1 - starred` rewrite ---
	if err := store.ToggleStarred(user.ID, entryOne.ID); err != nil {
		t.Fatalf("ToggleStarred: %v", err)
	}
	afterToggle, err := store.NewEntryQueryBuilder(user.ID).WithStarred(true).GetEntries()
	if err != nil {
		t.Fatalf("post-toggle starred filter: %v", err)
	}
	if len(afterToggle) != 0 {
		t.Fatalf("after toggle, expected 0 starred entries, got %d", len(afterToggle))
	}
}
