package main

import (
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/security"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Use the actual execute command's flags to catch recognition/CLI drift without
// running programs or opening storage. Admission behavior has compiled CLI tests.
func TestCaptureRecognitionMatchesCLIFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"defaults", []string{"execute", "--code", "echo 'quoted'"}},
		{"selectors before and after", []string{"--project-dir", "/first", "execute", "--code-file", "program.sh", "--project-dir=/last", "--cwd", "source"}},
		{"all flags", []string{"execute", "--language=python", "--code", "print('hello')", "--intent", "failure", "--timeout-ms=0x100", "--json=false"}},
		{"last scalar wins", []string{"execute", "--code", "blocked arg", "--code", "echo ok", "--language=go", "--language=shell", "--cwd=", "--cwd=src", "--timeout-ms=0", "--timeout-ms=123", "--"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "capy"}
			root.PersistentFlags().String("project-dir", "", "")
			execute := newExecuteCmd()
			var actual security.CaptureInvocation
			execute.RunE = func(cmd *cobra.Command, _ []string) error {
				for _, field := range []struct {
					name   string
					target *string
				}{
					{"language", &actual.Language}, {"code", &actual.Code}, {"code-file", &actual.CodeFile},
					{"project-dir", &actual.ProjectDir}, {"cwd", &actual.Cwd},
				} {
					value, err := cmd.Flags().GetString(field.name)
					require.NoError(t, err)
					*field.target = value
				}
				return nil
			}
			root.AddCommand(execute)
			root.SetArgs(tc.args)
			require.NoError(t, root.Execute())
			command := "capy"
			for _, arg := range tc.args {
				command += " '" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
			}
			got, err := security.InspectRoutineCommand(command, security.RoutineOptions{
				ReadCodeFile: func(security.CaptureInvocation) (string, error) { return "echo checked", nil },
			})
			require.NoError(t, err)
			require.Equal(t, []security.CaptureInvocation{actual}, got.Captures)
			require.Empty(t, got.Required)
		})
	}
}
