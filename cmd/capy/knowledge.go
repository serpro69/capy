package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

// knowledgeTarget keeps target selection separate from credential resolution
// and lazy store construction. A caller can inspect an absent DB without
// requiring credentials or creating its directory/marker.
type knowledgeTarget struct {
	cfg          *config.Config
	projectDir   string
	dbProjectDir string
	dbPath       string
}

func commandProjectDir(cmd *cobra.Command) (string, error) {
	projectDir, err := cmd.Flags().GetString("project-dir")
	if err != nil {
		return "", fmt.Errorf("reading project directory flag: %w", err)
	}
	if projectDir == "" {
		projectDir = config.DetectProjectRoot()
	}
	return projectDir, nil
}

func loadKnowledgeTarget(cmd *cobra.Command) (*knowledgeTarget, error) {
	projectDir, err := commandProjectDir(cmd)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(projectDir)
	if err != nil {
		return nil, fmt.Errorf("loading configuration: %w", err)
	}
	return &knowledgeTarget{
		cfg:          cfg,
		projectDir:   projectDir,
		dbProjectDir: cfg.DBProjectDir(projectDir),
		dbPath:       cfg.ResolveDBPath(projectDir),
	}, nil
}

func resolveKnowledgeKey(target *knowledgeTarget) (string, config.KeySource, error) {
	key, source, err := target.cfg.ResolveStoreKey(target.projectDir)
	if err != nil {
		return "", source, fmt.Errorf("resolving knowledge credential for %q: %w", target.dbPath, err)
	}
	return key, source, nil
}

func newKnowledgeStore(target *knowledgeTarget, key, source string) *store.ContentStore {
	return store.NewContentStore(
		target.dbPath,
		target.dbProjectDir,
		target.cfg.Store.TitleWeight,
		target.cfg.Store.MaxSourceBytes,
		store.WithEncryptionKey(key, source),
	)
}
