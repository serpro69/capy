package main

import (
	"errors"
	"fmt"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/hook"
	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	var platform string
	cmd := &cobra.Command{
		Use:   "hook <event>",
		Short: "Handle a Claude Code or Codex hook event",
		Long: `Handle one JSON hook event from stdin and write the platform response to stdout.
The default platform is claude-code. Codex supports sessionstart, subagentstart,
pretooluse, posttooluse and sessionend; generated commands select it explicitly.
Hooks do not require database credentials. Use setup to register owner-bound
Codex commands; direct invocation is intended for controlled manual probes.`,
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

			return hookCommandError(cmd, args[0], a.Platform(), hook.Run(args[0], a, explicitProjectDir))
		},
	}
	cmd.Flags().StringVar(&platform, "platform", string(adapter.ClaudeCode), "hook platform: claude-code or codex")
	return cmd
}

// Both entry paths classify transport errors identically. The launch command
// additionally maps owner/bootstrap failures to the established pre-tool exit 2.
func hookCommandError(cmd *cobra.Command, event string, platform adapter.Platform, err error) error {
	var inputErr *hook.InputError
	if platform == adapter.Codex && errors.As(err, &inputErr) {
		cmd.SilenceUsage = true
		if event == "pretooluse" {
			return &exitError{code: 2, err: err}
		}
	}
	return err
}
