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
Codex commands; direct invocation is intended for controlled manual probes.
Codex input requires EOF: ordinary events allow 2s idle, 10s total and 8 MiB;
sessionend allows 100ms idle, 250ms total and 64 KiB. Pipe stalls are bounded;
regular files have checks between reads. Transport failures use stderr and no
stdout (pretooluse exits 2); completed malformed pretooluse JSON denies at exit 0.
Codex main HTTP routing is advisory without recent successful capy_execute
evidence; one success permits one redirect within 60s. Security checks run first.`,
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
