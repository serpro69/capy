package main

import (
	"fmt"
	"os"

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
			// Checkpoint uses a dedicated connection with the captured key;
			// the store's lazy pool stays unopened so it cannot hold the WAL.
			st := newKnowledgeStore(target, key, source.String())
			if err := st.Checkpoint(); err != nil {
				return fmt.Errorf("checkpoint failed: %w", err)
			}

			// Verify sidecar files are gone or empty.
			// After PRAGMA wal_checkpoint(TRUNCATE), SQLite may leave a 0-byte
			// WAL file — that's fine. A non-empty WAL means another process
			// held the DB open and the checkpoint was incomplete.
			incomplete := false
			for _, suffix := range []string{"-wal", "-shm"} {
				if info, err := os.Stat(dbPath + suffix); err == nil && info.Size() > 0 {
					incomplete = true
					fmt.Fprintf(os.Stderr, "capy checkpoint: warning: %s still has data (%d bytes) — is another process using the DB?\n", dbPath+suffix, info.Size())
				}
			}

			if !incomplete {
				fmt.Printf("capy checkpoint: %s — WAL flushed, safe to commit\n", dbPath)
			}

			return nil
		},
	}
}
