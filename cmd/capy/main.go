package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/serpro69/capy/internal/version"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "capy",
		Short: "Context-aware MCP server for LLM context reduction",
		RunE:  serveRunE,
	}

	root.PersistentFlags().String("project-dir", "", "override project directory")
	root.Flags().Bool("version", false, "print version and exit")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		v, _ := cmd.Flags().GetBool("version")
		if v {
			fmt.Println(version.Version)
			os.Exit(0)
		}
		return nil
	}

	root.AddCommand(
		newServeCmd(),
		newHookCmd(),
		newSetupCmd(),
		newDoctorCmd(),
		newCleanupCmd(),
		newCheckpointCmd(),
		newEncryptCmd(),
		newWhichCmd(),
		newSweepCmd(),
		newDBSizeCmd(),
		newVaultCmd(),
	)

	if err := root.Execute(); err != nil {
		// A command may request a specific exit code (e.g. `vault resume`
		// propagating claude's own status); otherwise fall back to 1.
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}
