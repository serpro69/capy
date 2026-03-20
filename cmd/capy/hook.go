package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "hook <event>",
		Short:     "Handle a Claude Code hook event",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"pretooluse", "posttooluse", "precompact", "sessionstart", "userpromptsubmit"},
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("capy: hook %s not yet implemented\n", args[0])
			return nil
		},
	}
}
