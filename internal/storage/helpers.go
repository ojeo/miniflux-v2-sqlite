// SPDX-FileCopyrightText: Copyright The Miniflux Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package storage // import "miniflux.app/v2/internal/storage"

import (
	"strconv"
	"strings"
)

// inClauseString builds a comma-separated list of `?N` placeholders and the
// matching argument slice for an IN (...) condition, starting at placeholder
// number `start`. This replaces PostgreSQL's `= ANY($N)` / `<> ALL($N)` with a
// pq.Array of strings.
func inClauseString(start int, values []string) (string, []any) {
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		placeholders[i] = "?" + strconv.Itoa(start+i)
		args[i] = v
	}
	return strings.Join(placeholders, ", "), args
}
