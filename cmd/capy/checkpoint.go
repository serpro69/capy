package main

import (
	"fmt"
	"os"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
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
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			cfg, _ := config.Load(projectDir)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			dbPath := cfg.ResolveDBPath(projectDir)

			// Check the DB file exists before trying to open it.
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				fmt.Printf("capy checkpoint: no knowledge base at %s\n", dbPath)
				return nil
			}

			// Open store — this triggers getDB → schema init → prepareStatements.
			// Close() does PRAGMA wal_checkpoint(TRUNCATE) and closes the connection,
			// which removes the WAL and SHM files.
			st := store.NewContentStore(dbPath, projectDir)
			if err := st.Close(); err != nil {
				return fmt.Errorf("checkpoint failed: %w", err)
			}

			// Verify sidecar files are gone.
			walGone := true
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(dbPath + suffix); err == nil {
					walGone = false
					fmt.Fprintf(os.Stderr, "capy checkpoint: warning: %s still exists (is another process using the DB?)\n", dbPath+suffix)
				}
			}

			if walGone {
				fmt.Printf("capy checkpoint: %s — WAL flushed, safe to commit\n", dbPath)
			}

			return nil
		},
	}
}
