package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		RunE:  serveRunE,
	}
}

func serveRunE(cmd *cobra.Command, args []string) error {
	fmt.Println("capy: MCP server not yet implemented")
	return nil
}
