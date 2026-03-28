package main

import (
	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/hook"
	"github.com/serpro69/capy/internal/security"
	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "hook <event>",
		Short:     "Handle a Claude Code hook event",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"pretooluse", "posttooluse", "precompact", "sessionstart", "userpromptsubmit"},
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			if projectDir == "" {
				projectDir = config.DetectProjectRoot()
			}

			policies := security.ReadBashPolicies(projectDir, "")
			a := &adapter.ClaudeCodeAdapter{}

			return hook.Run(args[0], a, policies, projectDir)
		},
	}
}
