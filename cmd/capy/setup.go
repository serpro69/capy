package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

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

			setupClaude := p == "" || p == "claude-code"
			setupCodex := p == "" || p == "codex"

			if !setupClaude && !setupCodex {
				return fmt.Errorf("unsupported platform: %s (supported: claude-code, codex)", p)
			}

			fmt.Printf("capy: setting up for project %s\n", projectDir)

			if setupClaude {
				local, _ := cmd.Flags().GetBool("local")
				project, _ := cmd.Flags().GetBool("project")

				target, err := resolveSettingsTarget(local, project)
				if err != nil {
					return err
				}

				if err := platform.SetupClaudeCode(binaryPath, projectDir, target); err != nil {
					return fmt.Errorf("Claude Code setup failed: %w", err)
				}

				fmt.Println("capy: Claude Code setup complete")
				fmt.Printf("  - hooks registered in .claude/%s\n", target.SettingsFilename())
				fmt.Println("  - MCP server registered in .mcp.json")
				fmt.Println("  - routing instructions written to .capy/AGENTS.md")
				fmt.Println("  - .capy/** added to .gitignore")
			}

			if setupCodex {
				if err := platform.SetupCodex(binaryPath, projectDir); err != nil {
					return fmt.Errorf("Codex setup failed: %w", err)
				}

				fmt.Println("capy: Codex setup complete")
				fmt.Println("  - MCP server registered in .codex/config.toml")
				fmt.Println("  - routing instructions written to .capy/AGENTS.md")
				fmt.Println("  - wrapper script written to .codex/scripts/capy.sh")
				fmt.Println("  - .capy/** added to .gitignore")
			}

			fmt.Println("\nRun `capy doctor` to verify the installation.")
			return nil
		},
	}
	cmd.Flags().String("platform", "", "target platform (claude-code, codex); default: both")
	cmd.Flags().String("binary", "", "path to capy binary")
	cmd.Flags().Bool("local", false, "write hooks to .claude/settings.local.json (personal, not committed)")
	cmd.Flags().Bool("project", false, "write hooks to .claude/settings.json (shared, synced across repos)")
	cmd.MarkFlagsMutuallyExclusive("local", "project")
	return cmd
}

// resolveSettingsTarget determines the settings target from CLI flags or interactive prompt.
func resolveSettingsTarget(local, project bool) (platform.SettingsTarget, error) {
	switch {
	case local:
		return platform.SettingsLocal, nil
	case project:
		return platform.SettingsProject, nil
	default:
		return promptSettingsTarget()
	}
}

// promptSettingsTarget interactively asks the user where to register hooks.
// Defaults to SettingsProject on empty input or non-interactive stdin (EOF).
func promptSettingsTarget() (platform.SettingsTarget, error) {
	fmt.Println("Where should capy hooks be registered?")
	fmt.Println("  [1] .claude/settings.json        (shared, synced across repos)")
	fmt.Println("  [2] .claude/settings.local.json   (personal, not committed)")
	fmt.Print("Choice [1]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		// EOF (non-interactive) — default to project
		return platform.SettingsProject, nil
	}

	switch strings.TrimSpace(input) {
	case "", "1":
		return platform.SettingsProject, nil
	case "2":
		return platform.SettingsLocal, nil
	default:
		return 0, fmt.Errorf("invalid choice: %q (use 1 or 2)", strings.TrimSpace(input))
	}
}
