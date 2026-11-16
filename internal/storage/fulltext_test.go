// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

	"miniflux.app/v2/internal/model"
)

func TestFullTextSearchIndexLifecycle(t *testing.T) {
	store := newTestStorage(t)

	user, err := store.CreateUser(&model.UserCreationRequest{
		Username: "fts-user",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	category, err := store.CreateCategory(user.ID, &model.CategoryCreationRequest{Title: "news"})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	feed := &model.Feed{
		UserID:  user.ID,
		FeedURL: "https://example.com/feed.xml",
		SiteURL: "https://example.com",
		Title:   "FTS Feed",
	}
	feed.WithCategoryID(category.ID)
	if err := store.CreateFeed(feed); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}

	entry := model.NewEntry()
	entry.Hash = "fts-hash"
	entry.Title = "SQLite full text search"
	entry.URL = "https://example.com/fts"
	entry.Content = "A searchable body about the unicode61 tokenizer."
	entry.Status = model.EntryStatusUnread

	if _, err := store.InsertEntryForFeed(user.ID, feed.ID, entry); err != nil {
		t.Fatalf("InsertEntryForFeed: %v", err)
	}

	// The initial migration creates the FTS index.
	if exists, err := store.FullTextSearchIndexExists(); err != nil || !exists {
		t.Fatalf("expected the FTS index to exist after migration (exists=%v err=%v)", exists, err)
	}

	// Simulate DISABLE_FULL_TEXT_SEARCH=1.
	if err := store.DropFullTextSearchIndex(); err != nil {
		t.Fatalf("DropFullTextSearchIndex: %v", err)
	}
	if exists, err := store.FullTextSearchIndexExists(); err != nil || exists {
		t.Fatalf("expected the FTS index to be dropped (exists=%v err=%v)", exists, err)
	}

	// The triggers must be gone as well: inserting an entry must not fail.
	newEntry := model.NewEntry()
	newEntry.Hash = "fts-hash-2"
	newEntry.Title = "Another entry"
	newEntry.URL = "https://example.com/fts-2"
	newEntry.Content = "Body inserted after the index was dropped."
	newEntry.Status = model.EntryStatusUnread
	if _, err := store.InsertEntryForFeed(user.ID, feed.ID, newEntry); err != nil {
		t.Fatalf("InsertEntryForFeed after drop: %v", err)
	}

	// Simulate re-enabling full-text search.
	if err := store.CreateFullTextSearchIndex(); err != nil {
		t.Fatalf("CreateFullTextSearchIndex: %v", err)
	}
	if exists, err := store.FullTextSearchIndexExists(); err != nil || !exists {
		t.Fatalf("expected the FTS index to be recreated (exists=%v err=%v)", exists, err)
	}

	results, err := store.NewEntryQueryBuilder(user.ID).WithSearchQuery("searchable").GetEntries()
	if err != nil {
		t.Fatalf("FTS search after rebuild: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result after rebuild, got %d", len(results))
	}

	// Rebuilding must also index entries inserted while the index was dropped.
	results, err = store.NewEntryQueryBuilder(user.ID).WithSearchQuery("dropped").GetEntries()
	if err != nil {
		t.Fatalf("FTS search for post-drop entry: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result for the post-drop entry, got %d", len(results))
	}
}
