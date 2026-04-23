package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

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

func isSQLiteCorruption(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "malformed") ||
		strings.Contains(msg, "not a database") ||
		strings.Contains(msg, "corrupt") ||
		strings.Contains(msg, "disk image is malformed")
}

func backupCorruptDB(dbPath string) {
	ts := time.Now().Format("20060102T150405")
	suffix := fmt.Sprintf(".corrupt.%s", ts)

	for _, ext := range []string{"", "-wal", "-shm"} {
		src := dbPath + ext
		dst := src + suffix
		if err := os.Rename(src, dst); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			slog.Warn("failed to back up corrupt DB file", "src", src, "error", err)
			continue
		}
		slog.Warn("backed up corrupt DB file", "src", src, "dst", dst)
	}
}
