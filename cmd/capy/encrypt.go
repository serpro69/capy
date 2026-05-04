package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/mattn/go-sqlite3"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/serpro69/capy/internal/terminal"
	"github.com/spf13/cobra"
)

func newEncryptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "encrypt",
		Short: "Encrypt the knowledge database or rotate its encryption key",
		Long: `Encrypt an unencrypted knowledge database, or rotate the key of an
already-encrypted one.

Initial encryption:
  1. Set CAPY_DB_KEY in your shell profile (32+ chars recommended).
  2. Run: capy encrypt
  3. When prompted for the current passphrase, press Enter (empty = unencrypted).

Key rotation:
  1. Set CAPY_DB_KEY to the NEW passphrase.
  2. Run: capy encrypt
  3. Enter the OLD passphrase when prompted.

The original database is preserved as <path>.bak before any changes.`,
		RunE: runEncrypt,
	}
}

func runEncrypt(cmd *cobra.Command, args []string) error {
	projectDir, _ := cmd.Flags().GetString("project-dir")
	if projectDir == "" {
		projectDir = config.DetectProjectRoot()
	}

	cfg, err := config.Load(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capy encrypt: warning: config load failed (%v), using defaults\n", err)
	}
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	dbPath := cfg.ResolveDBPath(projectDir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return fmt.Errorf("no knowledge base at %s", dbPath)
	}

	oldKey, err := terminal.ReadPassphrase("Current DB passphrase (empty if unencrypted): ")
	if err != nil {
		return fmt.Errorf("reading current passphrase: %w", err)
	}

	newKey := store.EncryptionKeyFromEnv()
	if newKey == "" {
		newKey, err = terminal.ReadPassphraseConfirm("New passphrase: ")
		if err != nil {
			return fmt.Errorf("reading new passphrase: %w", err)
		}
	}
	if newKey == "" {
		return fmt.Errorf("new passphrase cannot be empty")
	}
	if len(newKey) < store.MinPassphraseLength {
		slog.Warn("encryption passphrase is short — 32+ characters recommended",
			"length", len(newKey))
	}

	if oldKey == "" {
		return encryptPlain(dbPath, newKey)
	}
	return rekeyEncrypted(dbPath, oldKey, newKey)
}

// encryptPlain encrypts an unencrypted database via file copy + PRAGMA rekey.
func encryptPlain(dbPath, newKey string) error {
	srcDB, err := openUnencrypted(dbPath)
	if err != nil {
		return err
	}

	if err := checkpointDB(srcDB); err != nil {
		srcDB.Close()
		return err
	}
	if err := srcDB.Close(); err != nil {
		return fmt.Errorf("closing source database: %w", err)
	}

	tmpPath := dbPath + ".enc.tmp"
	if err := copyFile(dbPath, tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("copying database: %w", err)
	}

	tmpDB, err := openWithCipherCodec(tmpPath, "")
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("opening copy for rekey: %w", err)
	}

	// sqlite3mc does not support PRAGMA rekey in WAL journal mode
	if _, err := tmpDB.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("switching journal mode for rekey: %w", err)
	}

	// security: PRAGMA values cannot use ? placeholders; EscapeSQLString doubles single-quotes
	if _, err := tmpDB.Exec("PRAGMA rekey = '" + store.EscapeSQLString(newKey) + "'"); err != nil {
		tmpDB.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("PRAGMA rekey: %w", err)
	}
	tmpDB.Close()

	return swapAndVerify(dbPath, tmpPath, newKey)
}

// rekeyEncrypted re-encrypts an already-encrypted database using the
// SQLite backup API.
func rekeyEncrypted(dbPath, oldKey, newKey string) error {
	srcDB, err := openEncrypted(dbPath, oldKey)
	if err != nil {
		return err
	}

	if err := checkpointDB(srcDB); err != nil {
		srcDB.Close()
		return err
	}

	tmpPath := dbPath + ".enc.tmp"
	destDB, err := openEncrypted(tmpPath, newKey)
	if err != nil {
		srcDB.Close()
		return fmt.Errorf("creating target database: %w", err)
	}

	if err := backupDB(destDB, srcDB); err != nil {
		destDB.Close()
		srcDB.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("backup API: %w", err)
	}
	destDB.Close()
	if err := srcDB.Close(); err != nil {
		return fmt.Errorf("closing source database: %w", err)
	}

	return swapAndVerify(dbPath, tmpPath, newKey)
}

func openUnencrypted(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening unencrypted database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		return nil, fmt.Errorf("database is not readable (is it already encrypted?): %w", err)
	}
	return db, nil
}

func openWithCipherCodec(dbPath, key string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", store.EncryptedDSN(dbPath, key))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func openEncrypted(dbPath, key string) (*sql.DB, error) {
	dsn := store.EncryptedDSN(dbPath, key) + "&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening encrypted database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		return nil, fmt.Errorf("wrong passphrase or corrupted database: %w", err)
	}
	return db, nil
}

func checkpointDB(db *sql.DB) error {
	var busy, log, checkpointed int
	err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return fmt.Errorf("checkpoint failed: %w", err)
	}
	if busy > 0 {
		return fmt.Errorf("checkpoint incomplete: %d pages busy (is the server still running?)", busy)
	}
	return nil
}

func backupDB(destDB, srcDB *sql.DB) error {
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
			finishErr := backup.Finish()
			if err != nil {
				return fmt.Errorf("backup step: %w", err)
			}
			if finishErr != nil {
				return fmt.Errorf("backup finish: %w", finishErr)
			}
			return nil
		})
	})
}

// swapAndVerify removes WAL/SHM sidecars, backs up the original, moves
// the new file into place, and verifies the result.
func swapAndVerify(dbPath, tmpPath, newKey string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}

	bakPath := dbPath + ".bak"
	if err := os.Rename(dbPath, bakPath); err != nil {
		return fmt.Errorf("backing up original: %w", err)
	}

	if err := os.Rename(tmpPath, dbPath); err != nil {
		if rerr := os.Rename(bakPath, dbPath); rerr != nil {
			fmt.Fprintf(os.Stderr, "capy encrypt: CRITICAL: rollback failed: %v\n", rerr)
			fmt.Fprintf(os.Stderr, "capy encrypt: manual recovery: backup at %s\n", bakPath)
		}
		return fmt.Errorf("moving encrypted database into place: %w", err)
	}

	verifyDB, err := openEncrypted(dbPath, newKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capy encrypt: WARNING: verification failed (%v), restoring backup\n", err)
		if rerr := os.Rename(dbPath, tmpPath); rerr != nil {
			fmt.Fprintf(os.Stderr, "capy encrypt: CRITICAL: could not move failed db aside: %v\n", rerr)
		}
		if rerr := os.Rename(bakPath, dbPath); rerr != nil {
			fmt.Fprintf(os.Stderr, "capy encrypt: CRITICAL: rollback failed: %v\n", rerr)
			fmt.Fprintf(os.Stderr, "capy encrypt: manual recovery: backup at %s\n", bakPath)
		}
		os.Remove(tmpPath)
		return fmt.Errorf("verification failed: %w", err)
	}
	verifyDB.Close()

	fmt.Printf("capy encrypt: done. Encrypted: %s\n", dbPath)
	fmt.Printf("capy encrypt: backup at %s\n", bakPath)
	return nil
}

func copyFile(src, dst string) (err error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()

	_, err = io.Copy(out, in)
	return err
}
