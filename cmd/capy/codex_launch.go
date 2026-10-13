package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/hook"
	"github.com/serpro69/capy/internal/platform"
	"github.com/spf13/cobra"
)

func newCodexLaunchCmd() *cobra.Command {
	var protocol string
	cmd := &cobra.Command{
		Use: "codex-launch <protocol> <owner-id> <mode> <event>", Hidden: true,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// This metadata-only probe must precede owner/config/stdin access.
			if cmd.Flags().Changed("check-protocol") {
				if len(args) != 0 || protocol != platform.CodexLaunchProtocol {
					return fmt.Errorf("unsupported Codex launch protocol %q", protocol)
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), platform.CodexLaunchProtocol)
				return err
			}
			if len(args) != 4 || args[0] != platform.CodexLaunchProtocol {
				return fmt.Errorf("unsupported Codex launch format; stop affected sessions and restore final generated configuration with compatible capy on PATH")
			}
			err := runCodexLaunch(cmd.Context(), args[1], args[2], args[3])
			if args[2] == "hook" {
				err = hookCommandError(cmd, args[3], adapter.Codex, err)
			}
			if err != nil && args[2] == "hook" && args[3] == "pretooluse" {
				return &exitError{code: 2, err: err}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&protocol, "check-protocol", "", "check launch protocol without accessing project state")
	return cmd
}

func runCodexLaunch(ctx context.Context, id, mode, event string) error {
	if mode == "hook" {
		stop := codexHookSignals()
		defer stop()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Codex launch cwd: %w; restore the working directory", err)
	}
	owner, err := platform.ResolveCodexOwner(cwd, id, mode, event)
	if err != nil {
		return err
	}
	// Keep process/event cwd separate from the validated execution/storage owner.
	if mode == "hook" {
		return hook.Run(event, &adapter.CodexAdapter{}, &owner)
	}
	return serveProject(ctx, owner)
}

func codexHookSignals() func() {
	// An asynchronous Bash child can inherit ignored SIGINT. Explicitly handle
	// all forwarded signals, including while the hook is waiting for stdin EOF.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case sig := <-signals:
			os.Exit(128 + int(sig.(syscall.Signal)))
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		<-stopped
	}
}
