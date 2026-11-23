package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/serpro69/capy/internal/vault/tui"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestVaultListLaunchOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	st := vault.NewVaultStore(path)
	for i := range 51 {
		rec := &vault.SessionRecord{Session: vault.Session{UUID: fmt.Sprintf("child%03d", i), Platform: vault.PlatformCodex, ParentUUID: "absent01", ProjectPath: "alpha", Title: "Keep", RawJSONL: []byte("{}\n")}}
		require.NoError(t, st.InsertSession(t.Context(), rec))
	}
	require.NoError(t, st.Close())
	flags := []string{"--children-only", "--project=alpha", "--platform=codex", "--name=keep"}
	for _, tc := range []struct {
		name  string
		flags []string
		limit int
	}{
		{"plain omitted", nil, 50}, {"interactive omitted", []string{"--tui"}, 0},
		{"plain zero", []string{"--limit", "0"}, 0}, {"interactive zero", []string{"--tui", "--limit=0"}, 0},
		{"plain capped", []string{"--limit=3"}, 3}, {"interactive capped", []string{"--tui", "--limit=3"}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, flags...), tc.flags...)
			if len(tc.flags) > 0 && tc.flags[0] == "--tui" {
				var launched tui.Options
				cmd := newVaultListCmd(&vaultEnv{dbPath: path}, func(_ *cobra.Command, _ *vaultEnv, o tui.Options) error { launched = o; return nil })
				cmd.Flags().Bool("tui", false, "")
				cmd.SetArgs(args)
				require.NoError(t, cmd.ExecuteContext(t.Context()))
				require.Equal(t, tui.Options{Mode: "list", List: vault.ListOptions{Project: "alpha", Platform: vault.PlatformCodex, Name: "keep", Limit: tc.limit, ChildrenOnly: true}}, launched)
				return
			}
			out, diag, code := capy(t, append([]string{"vault", "list", "--path", path, "--json"}, args...)...)
			require.Zero(t, code, diag)
			var rows []sessionJSON
			require.NoError(t, json.Unmarshal([]byte(out), &rows))
			want := tc.limit
			if want == 0 {
				want = 51
			}
			require.Len(t, rows, want)
			require.Equal(t, "child000", rows[0].UUID)
		})
	}
}

func TestVaultListRejectsInvalidFlagsBeforeOpen(t *testing.T) {
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	for _, tc := range []struct {
		name  string
		flags []string
		want  string
	}{
		{"negative plain", []string{"--limit=-1"}, "--limit"},
		{"negative interactive", []string{"--tui", "--limit=-1"}, "--limit"},
		{"conflict plain", []string{"--children-only", "--include-children"}, "mutually exclusive"},
		{"conflict interactive", []string{"--tui", "--children-only", "--include-children"}, "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unopened.db")
			cmd := newVaultCmd()
			cmd.SetArgs(append([]string{"list", "--path", path}, tc.flags...))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			require.ErrorContains(t, cmd.ExecuteContext(t.Context()), tc.want)
			require.NoFileExists(t, path)
		})
	}
}
