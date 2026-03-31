package hook

import (
	"fmt"
	"os"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
)

// handleSessionEnd performs cleanup when a Claude Code session ends.
// Checkpoints the WAL into the main DB file so the DB is safe for git commits.
// Best-effort: errors are logged to stderr but never returned.
func handleSessionEnd(projectDir string) {
	cfg, _ := config.Load(projectDir)
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	dbPath := cfg.ResolveDBPath(projectDir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return // no DB, nothing to checkpoint
	}

	st := store.NewContentStore(dbPath, projectDir)
	if err := st.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "capy: session end checkpoint failed: %v\n", err)
	}
}
