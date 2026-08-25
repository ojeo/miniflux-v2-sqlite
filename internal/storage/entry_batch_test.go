// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"
	"time"

	"miniflux.app/v2/internal/model"
)

func TestRefreshFeedEntriesBatched(t *testing.T) {
	store := newTestStorage(t)

	user, err := store.CreateUser(&model.UserCreationRequest{
		Username: "batcher",
		Password: "secret",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	category, err := store.CreateCategory(user.ID, &model.CategoryCreationRequest{Title: "default"})
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	feed := &model.Feed{
		FeedURL:  "http://example.com/feed.xml",
		SiteURL:  "http://example.com/",
		Title:    "Batch feed",
		Category: category,
		UserID:   user.ID,
	}
	if err := store.CreateFeed(feed); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}

	// 250 entries with the default batch size of 200 → at least two batches
	// are committed within a single feed refresh.
	entries := make(model.Entries, 0, 250)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 250; i++ {
		entries = append(entries, &model.Entry{
			Hash:        "hash-batch-" + itoa(i),
			Title:       "entry-" + itoa(i),
			URL:         "http://example.com/" + itoa(i),
			Date:        now,
			Content:     "body " + itoa(i),
			Author:      "author",
			Tags:        []string{"tag"},
			Language:    "en",
			Starred:     false,
			ReadingTime: 1,
		})
	}

	newEntries, err := store.RefreshFeedEntries(user.ID, feed.ID, entries, false)
	if err != nil {
		t.Fatalf("RefreshFeedEntries: %v", err)
	}
	if len(newEntries) != 250 {
		t.Fatalf("expected 250 new entries, got %d", len(newEntries))
	}

	count, err := store.CountAllEntries()
	if err != nil {
		t.Fatalf("CountAllEntries: %v", err)
	}
	if total := count["total"]; total != 250 {
		t.Fatalf("expected 250 total entries, got %d", total)
	}

	// Re-running the same feed must not duplicate entries.
	newEntries2, err := store.RefreshFeedEntries(user.ID, feed.ID, entries, false)
	if err != nil {
		t.Fatalf("RefreshFeedEntries (2nd): %v", err)
	}
	if len(newEntries2) != 0 {
		t.Fatalf("expected no new entries on re-run, got %d", len(newEntries2))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
