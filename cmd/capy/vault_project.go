package main

import (
	"errors"
	"fmt"

	"github.com/serpro69/capy/internal/vault"
	"github.com/spf13/cobra"
)

func newVaultProjectCmd(env *vaultEnv) *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "project <session-id> [<name>]",
		Short: "Set or clear an archived session's custom project (partial UUID, 8+ chars)",
		Long: `Assign a capy-owned project label to one archived session.
The imported project path and archived transcript remain unchanged.

Pass --clear instead of a name to restore the latest imported project path.
Quote labels containing whitespace. Labels are trimmed, recognized credentials
are redacted, and the result must contain at most 120 Unicode code points.
Path-looking labels are literal: they never change restore or resume locations.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := guardTUI(cmd); err != nil {
				return err
			}
			opts, err := projectOptions(args, clear)
			if err != nil {
				return err
			}
			st := vault.NewVaultStore(env.dbPath)
			defer st.Close()

			sess, err := st.SetSessionProject(cmd.Context(), args[0], opts)
			if err != nil {
				return handleLookupError(args[0], err)
			}
			if opts.Clear {
				fmt.Printf("cleared custom project for %s — project is now %q\n", shortUUID(sess.UUID, sess.Platform), sess.EffectiveProject())
			} else {
				fmt.Printf("set project for %s to %q\n", shortUUID(sess.UUID, sess.Platform), sess.EffectiveProject())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the custom project (restores the imported path)")
	return cmd
}

func projectOptions(args []string, clear bool) (vault.ProjectOptions, error) {
	if clear {
		if len(args) > 1 {
			return vault.ProjectOptions{}, errors.New("a project name and --clear are mutually exclusive")
		}
		return vault.ProjectOptions{Clear: true}, nil
	}
	if len(args) < 2 {
		return vault.ProjectOptions{}, errors.New("provide a project name, or pass --clear to remove the custom project")
	}
	// SetSessionProject validates and normalizes before opening the database.
	return vault.ProjectOptions{Name: args[1]}, nil
}
