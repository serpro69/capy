package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check capy installation and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("capy: doctor not yet implemented")
			return nil
		},
	}
}
