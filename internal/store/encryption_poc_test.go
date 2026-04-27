package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mattn/go-sqlite3"
)

// Encryption PoC — Driver Evaluation
//
// RESULT: Option 3 (jgiannuzzi fork with sqlite3mc) using URI-parameter encryption.
//
// Option 1 (system SQLCipher + PRAGMA key via ConnectHook) is NOT viable:
// mattn/go-sqlite3 v1.14.37 always executes PRAGMA synchronous = NORMAL
// (sqlite3.go:1746) before ConnectHook. On an encrypted DB without a key,
// this pragma reads the file header and fails with "file is not a database".
// PRAGMA key in ConnectHook never gets a chance to run.
//
// Option 3 avoids this entirely via sqlite3mc URI parameters (?cipher=...&key=...)
// which apply the key at sqlite3_open_v2 time, before any driver pragmas execute.
//
// sqlite3mc does NOT provide sqlcipher_export(). Migration mechanisms:
//   - Initial encryption (unencrypted→encrypted): PRAGMA rekey (in-place).
//     capy encrypt copies the file first for backup safety, then rekeys the copy.
//   - Re-key (encrypted→encrypted): SQLite backup API (sqlite3_backup_init/step/finish).
//     Backup API works across same-cipher connections with different keys.
//     It does NOT work across the unencrypted/encrypted boundary.
//
// Cipher: sqlcipher (SQLCipher v4 compat mode in sqlite3mc). Provides AES-256-CBC
// with HMAC-SHA512 and PBKDF2-HMAC-SHA512 KDF (256000 iterations).
//
// Run:
//   go test -tags fts5 -run TestEncryptionPoC -v -count=1 ./internal/store/

// pocEncryptedDSN builds a DSN with sqlite3mc URI-parameter encryption.
// The file: prefix ensures mattn/go-sqlite3 passes the full URI (including
// cipher/key params) through to sqlite3_open_v2 (see sqlite3.go:1451-1453).
func pocEncryptedDSN(dbPath, passphrase string) string {
	return fmt.Sprintf("file:%s?cipher=sqlcipher&legacy=4&key=%s",
		dbPath, url.QueryEscape(passphrase))
}

// pocBackup copies all pages from srcConn to destConn using the SQLite backup API.
// Both connections must already be open and keyed (via URI params or otherwise).
func pocBackup(destDB, srcDB *sql.DB) error {
	destConn, err := destDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting dest connection: %w", err)
	}
	defer destConn.Close()

	srcConn, err := srcDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting src connection: %w", err)
	}
	defer srcConn.Close()

	return destConn.Raw(func(destRaw any) error {
		return srcConn.Raw(func(srcRaw any) error {
			destSC := destRaw.(*sqlite3.SQLiteConn)
			srcSC := srcRaw.(*sqlite3.SQLiteConn)

			backup, err := destSC.Backup("main", srcSC, "main")
			if err != nil {
				return fmt.Errorf("starting backup: %w", err)
			}
			_, err = backup.Step(-1)
			backupErr := backup.Finish()
			if err != nil && err != (*sqlite3.ErrNoExtended)(nil) {
				return fmt.Errorf("backup step: %w", err)
			}
			if backupErr != nil {
				return fmt.Errorf("backup finish: %w", backupErr)
			}
			return nil
		})
	})
}

func TestEncryptionPoC(t *testing.T) {
	const passphrase = "test-passphrase-at-least-32-characters-long!!"
	const wrongPass = "wrong-passphrase-definitely-not-right!!!!"

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "encrypted.db")

	// Gate check: verify the encryption extension is loaded and URI params work.
	t.Run("encryption_active", func(t *testing.T) {
		checkPath := filepath.Join(tmpDir, "gate_check.db")
		dsn := pocEncryptedDSN(checkPath, passphrase)
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}
		_, err = db.Exec("CREATE TABLE gate (id INTEGER)")
		if err != nil {
			t.Fatalf("creating table: %v", err)
		}
		db.Close()

		raw, err := os.ReadFile(checkPath)
		if err != nil {
			t.Fatalf("reading db file: %v", err)
		}
		if len(raw) >= 15 && string(raw[:15]) == "SQLite format 3" {
			t.Fatal("encryption extension NOT active — file header is plaintext. " +
				"Ensure go.mod has the jgiannuzzi/go-sqlite3 replace directive.")
		}
		t.Log("encryption extension is active — file header is encrypted")
	})

	t.Run("create_encrypted_db_with_fts5", func(t *testing.T) {
		dsn := pocEncryptedDSN(dbPath, passphrase) +
			"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}

		var journalMode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatalf("checking journal_mode: %v", err)
		}
		if journalMode != "wal" {
			t.Fatalf("expected journal_mode=wal, got %s", journalMode)
		}

		_, err = db.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS poc_fts USING fts5(title, content)")
		if err != nil {
			t.Fatalf("creating FTS5 table: %v", err)
		}

		_, err = db.Exec("INSERT INTO poc_fts (title, content) VALUES (?, ?)",
			"encryption test", "this is encrypted content for full text search verification")
		if err != nil {
			t.Fatalf("inserting into FTS5: %v", err)
		}

		var title string
		err = db.QueryRow("SELECT title FROM poc_fts WHERE poc_fts MATCH ?", "encrypted").Scan(&title)
		if err != nil {
			t.Fatalf("FTS5 search: %v", err)
		}
		if title != "encryption test" {
			t.Fatalf("unexpected title: %q", title)
		}

		db.SetMaxOpenConns(1)
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		db.Close()
		t.Log("encrypted DB creation + FTS5 search + checkpoint works")
	})

	t.Run("reopen_correct_key", func(t *testing.T) {
		dsn := pocEncryptedDSN(dbPath, passphrase) +
			"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}
		defer db.Close()

		var count int
		if err := db.QueryRow("SELECT count(*) FROM poc_fts").Scan(&count); err != nil {
			t.Fatalf("querying reopened db: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected 1 row, got %d", count)
		}
		t.Log("reopen with correct key succeeds")
	})

	t.Run("wrong_key_fails", func(t *testing.T) {
		dsn := pocEncryptedDSN(dbPath, wrongPass) +
			"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}
		defer db.Close()

		err = db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(new(int))
		if err == nil {
			t.Fatal("expected error with wrong key, got nil")
		}
		t.Logf("wrong key correctly fails: %v", err)
	})

	t.Run("wal_checkpoint", func(t *testing.T) {
		dsn := pocEncryptedDSN(dbPath, passphrase) +
			"&_journal_mode=WAL&_busy_timeout=5000"
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		t.Log("WAL checkpoint(TRUNCATE) works on encrypted DB")
	})

	t.Run("rekey_unencrypted_to_encrypted", func(t *testing.T) {
		// Create a plain unencrypted DB with test data.
		plainPath := filepath.Join(tmpDir, "plain.db")
		plainDB, err := sql.Open("sqlite3", plainPath)
		if err != nil {
			t.Fatalf("opening plain db: %v", err)
		}
		_, err = plainDB.Exec("CREATE TABLE export_test (id INTEGER PRIMARY KEY, val TEXT)")
		if err != nil {
			t.Fatalf("creating table: %v", err)
		}
		_, err = plainDB.Exec("INSERT INTO export_test VALUES (1, 'hello from plain')")
		if err != nil {
			t.Fatalf("inserting: %v", err)
		}
		plainDB.SetMaxOpenConns(1)
		plainDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		plainDB.Close()

		// For capy encrypt safety, copy the file first (so original is preserved),
		// then PRAGMA rekey the copy. Here we test the rekey mechanism directly.
		// Open unencrypted DB with cipher codec + empty key, then rekey.
		rekeyDSN := fmt.Sprintf("file:%s?cipher=sqlcipher&legacy=4&key=", plainPath)
		rekeyDB, err := sql.Open("sqlite3", rekeyDSN)
		if err != nil {
			t.Fatalf("opening with cipher codec: %v", err)
		}
		rekeyDB.SetMaxOpenConns(1)

		// Verify source is readable before rekey
		var srcCount int
		if err := rekeyDB.QueryRow("SELECT count(*) FROM export_test").Scan(&srcCount); err != nil {
			t.Fatalf("pre-rekey query: %v", err)
		}
		if srcCount != 1 {
			t.Fatalf("expected 1 row pre-rekey, got %d", srcCount)
		}

		// Encrypt in-place via PRAGMA rekey
		if _, err := rekeyDB.Exec("PRAGMA rekey = '" + passphrase + "'"); err != nil {
			t.Fatalf("PRAGMA rekey: %v", err)
		}
		rekeyDB.Close()

		// Verify the file is now encrypted (header not plaintext)
		raw, err := os.ReadFile(plainPath)
		if err != nil {
			t.Fatalf("reading rekeyed file: %v", err)
		}
		if len(raw) >= 15 && string(raw[:15]) == "SQLite format 3" {
			t.Fatal("file header is still plaintext after rekey")
		}

		// Verify the encrypted DB is readable with the correct key
		verifyDSN := pocEncryptedDSN(plainPath, passphrase)
		verifyDB, err := sql.Open("sqlite3", verifyDSN)
		if err != nil {
			t.Fatalf("opening rekeyed db: %v", err)
		}
		defer verifyDB.Close()

		var val string
		if err := verifyDB.QueryRow("SELECT val FROM export_test WHERE id = 1").Scan(&val); err != nil {
			t.Fatalf("reading rekeyed db: %v", err)
		}
		if val != "hello from plain" {
			t.Fatalf("unexpected value: %q", val)
		}
		t.Log("PRAGMA rekey (unencrypted → encrypted) works")
	})

	t.Run("backup_rekey", func(t *testing.T) {
		const newPass = "new-passphrase-at-least-32-characters-long!!!!"
		rekeyPath := filepath.Join(tmpDir, "rekeyed.db")

		srcDSN := pocEncryptedDSN(dbPath, passphrase) + "&_busy_timeout=5000"
		srcDB, err := sql.Open("sqlite3", srcDSN)
		if err != nil {
			t.Fatalf("opening encrypted db: %v", err)
		}
		srcDB.SetMaxOpenConns(1)
		if _, err := srcDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			t.Fatalf("checkpoint before rekey: %v", err)
		}

		destDSN := pocEncryptedDSN(rekeyPath, newPass)
		destDB, err := sql.Open("sqlite3", destDSN)
		if err != nil {
			t.Fatalf("opening rekey target: %v", err)
		}
		destDB.SetMaxOpenConns(1)

		if err := pocBackup(destDB, srcDB); err != nil {
			t.Fatalf("backup for rekey: %v", err)
		}
		srcDB.Close()
		destDB.Close()

		newDSN := pocEncryptedDSN(rekeyPath, newPass)
		newDB, err := sql.Open("sqlite3", newDSN)
		if err != nil {
			t.Fatalf("opening rekeyed db: %v", err)
		}
		defer newDB.Close()

		var count int
		if err := newDB.QueryRow("SELECT count(*) FROM poc_fts").Scan(&count); err != nil {
			t.Fatalf("querying rekeyed db: %v", err)
		}
		if count != 1 {
			t.Fatalf("expected 1 row after rekey, got %d", count)
		}

		oldDSN := pocEncryptedDSN(rekeyPath, passphrase)
		oldDB, err := sql.Open("sqlite3", oldDSN)
		if err != nil {
			t.Fatalf("opening rekeyed db with old key: %v", err)
		}
		defer oldDB.Close()

		err = oldDB.QueryRow("SELECT count(*) FROM sqlite_master").Scan(new(int))
		if err == nil {
			t.Fatal("expected old key to fail on rekeyed db")
		}
		t.Log("backup API rekey works (new key succeeds, old key fails)")
	})

	t.Run("pool_concurrent_access", func(t *testing.T) {
		dsn := pocEncryptedDSN(dbPath, passphrase) +
			"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatalf("opening db: %v", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(5)

		var wg sync.WaitGroup
		errs := make(chan error, 20)

		for i := range 20 {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				var count int
				if err := db.QueryRow("SELECT count(*) FROM poc_fts").Scan(&count); err != nil {
					errs <- fmt.Errorf("goroutine %d: %w", id, err)
					return
				}
				if count < 1 {
					errs <- fmt.Errorf("goroutine %d: expected rows, got %d", id, count)
				}
			}(i)
		}

		wg.Wait()
		close(errs)

		for err := range errs {
			t.Fatal(err)
		}
		t.Log("URI params apply key to all pool connections under concurrent access")
	})
}
