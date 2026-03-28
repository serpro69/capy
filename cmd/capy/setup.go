package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/platform"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure capy for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			binaryPath, _ := cmd.Flags().GetString("binary")

			p, _ := cmd.Flags().GetString("platform")
			if p != "claude-code" {
				return fmt.Errorf("unsupported platform: %s (only claude-code is supported)", p)
			}

			fmt.Printf("capy: setting up for project %s\n", projectDir)

			if err := platform.SetupClaudeCode(binaryPath, projectDir); err != nil {
				return fmt.Errorf("setup failed: %w", err)
			}

			fmt.Println("capy: setup complete")
			fmt.Println("  - hooks registered in .claude/settings.json")
			fmt.Println("  - MCP server registered in .mcp.json")
			fmt.Println("  - routing instructions appended to CLAUDE.md")
			fmt.Println("  - .capy/ added to .gitignore")
			fmt.Println("\nRun `capy doctor` to verify the installation.")
			return nil
		},
	}
	cmd.Flags().String("platform", "claude-code", "target platform")
	cmd.Flags().String("binary", "", "path to capy binary")
	return cmd
}
