package main

import (
	"context"
	"fmt"
	"os"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/server"
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
	projectDir, _ := cmd.Flags().GetString("project-dir")
	if projectDir == "" {
		projectDir = config.DetectProjectRoot()
	}

	cfg, err := config.Load(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "capy: config warning: %v (using defaults)\n", err)
		cfg = config.DefaultConfig()
	}

	policies := security.ReadBashPolicies(projectDir, "")
	exec := executor.NewExecutor(projectDir, cfg.Executor.MaxOutputBytes)

	srv := server.NewServer(cfg, policies, exec, projectDir)
	return srv.Serve(context.Background())
}
