package main

import (
	"fmt"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/hook"
	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	var platform string
	cmd := &cobra.Command{
		Use:       "hook <event>",
		Short:     "Handle a Claude Code or Codex hook event",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"pretooluse", "posttooluse", "precompact", "sessionstart", "subagentstart", "sessionend", "userpromptsubmit"},
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, _ := cmd.Flags().GetString("project-dir")
			var explicitProjectDir *string
			if cmd.Flags().Changed("project-dir") {
				explicitProjectDir = &projectDir
			}

			var a adapter.HookAdapter
			switch adapter.Platform(platform) {
			case adapter.ClaudeCode:
				a = &adapter.ClaudeCodeAdapter{}
			case adapter.Codex:
				a = &adapter.CodexAdapter{}
			default:
				return fmt.Errorf("unknown hook platform %q: use claude-code or codex", platform)
			}

			return hook.Run(args[0], a, explicitProjectDir)
		},
	}
	cmd.Flags().StringVar(&platform, "platform", string(adapter.ClaudeCode), "hook platform: claude-code or codex")
	return cmd
}
