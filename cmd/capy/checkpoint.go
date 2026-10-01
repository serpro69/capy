package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newCheckpointCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checkpoint",
		Short: "Flush WAL into the main database file for safe git commits",
		Long: `Checkpoint merges the SQLite WAL (write-ahead log) into the main
knowledge.db file and removes the WAL and SHM sidecar files.

Run this before committing the knowledge DB to git. Without it,
the WAL/SHM files (which git doesn't track) can desync from the
main DB on branch switches, corrupting the database.

Capy must not be running when you checkpoint — if another process
has the DB open, the WAL cannot be fully truncated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := loadKnowledgeTarget(cmd)
			if err != nil {
				return err
			}
			dbPath := target.dbPath

			// Check the DB file exists before trying to open it.
			if _, err := os.Stat(dbPath); err != nil {
				if os.IsNotExist(err) {
					fmt.Printf("capy checkpoint: no knowledge base at %s\n", dbPath)
					return nil
				}
				return fmt.Errorf("accessing knowledge database %q: %w", dbPath, err)
			}

			key, source, err := resolveKnowledgeKey(target)
			if err != nil {
				return err
			}
			// SQLite follows database symlinks, so sidecars live beside the real
			// file. Resolve once for both the connection and verification while
			// retaining the selected project's credential ownership (ADR-032).
			dbPath, err = filepath.EvalSymlinks(dbPath)
			if err != nil {
				return fmt.Errorf("resolving knowledge database %q: %w", target.dbPath, err)
			}
			target.dbPath = dbPath
			// Checkpoint uses a dedicated connection with the captured key;
			// the store's lazy pool stays unopened so it cannot hold the WAL.
			st := newKnowledgeStore(target, key, source.String())
			if err := st.Checkpoint(); err != nil {
				return fmt.Errorf("checkpoint failed: %w", err)
			}

			if err := verifyCheckpointSidecars(dbPath); err != nil {
				return err
			}

			fmt.Printf("capy checkpoint: %s — WAL flushed, safe to commit\n", dbPath)
			return nil
		},
	}
}

// verifyCheckpointSidecars applies the DB-repo guard's commit-safety rule:
// tolerate an empty WAL, but reject any SHM file (an open connection may remain
// even when SQLite reports a successful TRUNCATE). Never delete live sidecars.
func verifyCheckpointSidecars(dbPath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		path := dbPath + suffix
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking checkpoint sidecar %q: %w", path, err)
		}
		if !info.Mode().IsRegular() || suffix == "-shm" || info.Size() > 0 {
			return fmt.Errorf("checkpoint incomplete: %s remains — stop processes using the database and retry", path)
		}
	}
	return nil
}
