// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package model // import "miniflux.app/v2/internal/model"

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// BoolScanner scans a SQLite INTEGER (0/1) column into a Go *bool.
//
// SQLite has no boolean type: booleans are stored as INTEGER 0/1. The
// database/sql default converter cannot assign an int64 to a *bool, so every
// boolean column read from SQLite must use this scanner.
type BoolScanner struct {
	Target *bool
}

// Scan implements sql.Scanner.
func (s BoolScanner) Scan(src any) error {
	if s.Target == nil {
		return errors.New("model: BoolScanner has nil target")
	}
	switch v := src.(type) {
	case nil:
		*s.Target = false
	case bool:
		*s.Target = v
	case int64:
		*s.Target = v != 0
	case int:
		*s.Target = v != 0
	case float64:
		*s.Target = v != 0
	case string:
		*s.Target = v == "1" || v == "true" || v == "t"
	default:
		return errors.New("model: unsupported source type for BoolScanner")
	}
	return nil
}

// StringArrayScanner scans a SQLite TEXT column containing a JSON array of
// strings (our representation of a PostgreSQL text[] column) into a Go *[]string.
type StringArrayScanner struct {
	Target *[]string
}

// Scan implements sql.Scanner.
func (s StringArrayScanner) Scan(src any) error {
	if s.Target == nil {
		return errors.New("model: StringArrayScanner has nil target")
	}
	if src == nil {
		*s.Target = nil
		return nil
	}

	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return errors.New("model: unsupported source type for StringArrayScanner")
	}

	if len(data) == 0 {
		*s.Target = []string{}
		return nil
	}

	return json.Unmarshal(data, s.Target)
}

// TagsValue converts a string slice into the JSON-array TEXT representation
// expected by SQLite for the entries.tags column. It returns a single driver
// value so it can be passed directly as a query argument.
func TagsValue(tags []string) driver.Value {
	if tags == nil {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// JSONValue marshals any value to its JSON TEXT representation for SQLite storage.
func JSONValue(v any) (driver.Value, error) {
	if v == nil {
		return "null", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// doubleTZOffset matches the trailing "+0800 +0800" pattern produced by
// Go's time.Time.String() when the timezone abbreviation is itself a numeric
// offset (e.g. Asia/Shanghai → "CST" → "+0800").
var doubleTZOffset = regexp.MustCompile(` [+-]\d{4} [+-]\d{4}$`)

// timeScanLayouts lists the layouts a SQLite TEXT timestamp may use. The
// modernc.org/sqlite driver stores a time.Time parameter using time.Time.String
// (e.g. "0001-01-01 00:00:00 +0000 UTC") while our schema defaults use
// strftime('%Y-%m-%dT%H:%M:%SZ','now') (RFC3339). Both must be accepted.
var timeScanLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02",
}

// Time is a sql.Scanner that reads a SQLite TEXT column into a Go time.Time.
// The modernc.org/sqlite driver returns TEXT columns as plain strings, and
// database/sql cannot assign a string to a *time.Time, so every timestamp
// column read from SQLite must be scanned through this type.
//
// It is used as a temporary scan target in the storage layer; the underlying
// model fields keep the standard time.Time type to avoid rippling changes
// across the whole application.
type Time struct {
	Time time.Time
}

// cachedLayout protects the cached layout index.  Almost all timestamps in the
// database use RFC3339, so trying the last-successful layout first makes the
// common case a single time.Parse call instead of walking 8 layouts.
var (
	cachedLayoutMu sync.Mutex
	cachedLayout   int
)

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		t.Time = time.Time{}
	case time.Time:
		t.Time = v
	case string:
		if v == "" {
			t.Time = time.Time{}
			return nil
		}
		// Go's time.Time.String() appends a monotonic clock reading
		// ("m=+163.786780201") which time.Parse rejects.  Strip it.
		if idx := strings.Index(v, " m="); idx >= 0 {
			v = strings.TrimSpace(v[:idx])
		}
		// Go's time.Time.String() may produce "+0800 +0800" (numeric
		// offset + zone abbreviation) for timezones where the
		// abbreviation itself looks like a numeric offset.  Strip the
		// second one, keeping only the first.
		v = doubleTZOffset.ReplaceAllStringFunc(v, func(s string) string {
			return s[:6] // " +0800 +0800" → " +0800"
		})

		// Try the last-successful layout first.  Under normal operation
		// almost every timestamp is RFC3339, so this avoids iterating.
		cachedLayoutMu.Lock()
		cached := cachedLayout
		cachedLayoutMu.Unlock()
		if parsed, err := time.Parse(timeScanLayouts[cached], v); err == nil {
			t.Time = parsed
			return nil
		}

		for i, layout := range timeScanLayouts {
			if i == cached {
				continue // already tried
			}
			if parsed, err := time.Parse(layout, v); err == nil {
				t.Time = parsed
				cachedLayoutMu.Lock()
				cachedLayout = i
				cachedLayoutMu.Unlock()
				return nil
			}
		}
		return fmt.Errorf("model.Time.Scan: cannot parse %q as time", v)
	case []byte:
		return t.Scan(string(v))
	default:
		return fmt.Errorf("model.Time.Scan: unsupported source type %T", src)
	}
	return nil
}

// Value implements driver.Valuer, formatting the timestamp as RFC3339 TEXT.
func (t Time) Value() (driver.Value, error) {
	if t.Time.IsZero() {
		return nil, nil
	}
	return t.Time.UTC().Format(time.RFC3339), nil
}
