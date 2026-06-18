package main

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/sqliteutil"
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

	if err := sqliteutil.Checkpoint(srcDB); err != nil {
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

	bakPath, err := sqliteutil.SwapAndVerify(dbPath, tmpPath, newKey)
	if err != nil {
		return err
	}
	printRekeyDone(dbPath, bakPath)
	return nil
}

// rekeyEncrypted re-encrypts an already-encrypted database by rotating its key
// via sqliteutil.Rekey (the SQLite backup-API path), then prints the
// capy-encrypt-prefixed result. The low-level rotation lives in sqliteutil so
// `capy vault rekey` can reuse it; this layer owns the user-facing output.
func rekeyEncrypted(dbPath, oldKey, newKey string) error {
	res, err := sqliteutil.Rekey(dbPath, oldKey, newKey)
	if err != nil {
		return err
	}
	printRekeyDone(dbPath, res.BackupPath)
	return nil
}

// printRekeyDone emits the success messages for `capy encrypt`. It mirrors the
// output the moved swapAndVerify helper used to print, keeping capy encrypt's
// observable stdout unchanged after the I/O moved out of sqliteutil.
func printRekeyDone(dbPath, bakPath string) {
	fmt.Printf("capy encrypt: done. Encrypted: %s\n", dbPath)
	fmt.Printf("capy encrypt: backup at %s\n", bakPath)
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
