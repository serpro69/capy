package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newWhichCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "which",
		Short: "Print the knowledge base path for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := loadKnowledgeTarget(cmd)
			if err != nil {
				return err
			}

			fmt.Println(target.dbPath)
			return nil
		},
	}
}
