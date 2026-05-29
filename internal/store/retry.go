package store

import (
	"errors"
	"strings"

	"github.com/mattn/go-sqlite3"
)

// isBusy reports whether err is a SQLITE_BUSY / SQLITE_LOCKED condition.
// Preferred path: typed sqlite3.Error code match. The string fallback catches
// errors wrapped in ways that strip the typed error (e.g., some database/sql
// paths return a bare error before reaching the driver error layer).
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code == sqlite3.ErrBusy || sqliteErr.Code == sqlite3.ErrLocked
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}
