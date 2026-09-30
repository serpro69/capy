package main

import (
	"context"
	"fmt"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/server"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		RunE:  serveRunE,
	}
}

func serveRunE(cmd *cobra.Command, _ []string) error {
	target, err := loadKnowledgeTarget(cmd)
	if err != nil {
		return fmt.Errorf("capy serve: %w", err)
	}
	key, source, err := resolveKnowledgeKey(target)
	if err != nil {
		return fmt.Errorf("capy serve: %w", err)
	}
	if err := store.ValidateEncryptionReadyWithKey(target.dbPath, key, source.String()); err != nil {
		return fmt.Errorf("capy serve: %w", err)
	}

	projectDir, cfg := target.projectDir, target.cfg
	policies := security.ReadBashPolicies(projectDir, "")
	exec := executor.NewExecutor(projectDir, cfg.Executor.MaxOutputBytes)

	srv := server.NewServer(cfg, policies, exec, projectDir, server.WithKnowledgeCredentials(key, source))
	return srv.Serve(context.Background())
}
