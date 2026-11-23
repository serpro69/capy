package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/vault"
	"github.com/serpro69/capy/internal/vault/tui"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const deleteTestID = "abcd1234-aaaa-4000-8000-000000000001"
const deleteTestProject = "/proj\tRED\x1b[0m\u2028next"
const deleteTestTitle = "first line\nsecond\rline\u2029end"

func deleteFixture(t *testing.T, ids ...string) string {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "vault.db")
	st := vault.NewVaultStore(path)
	defer st.Close()
	for _, id := range ids {
		rec := &vault.SessionRecord{Session: vault.Session{UUID: id, Title: deleteTestTitle, ProjectPath: deleteTestProject, RawJSONL: []byte("{}\n")}}
		require.NoError(t, st.InsertSession(t.Context(), rec))
	}
	return path
}

func runDelete(t *testing.T, path, input string, out, stderr io.Writer, args ...string) error {
	t.Helper()
	cmd := newVaultDeleteCmd(&vaultEnv{dbPath: path})
	cmd.Flags().String("project-dir", t.TempDir(), "")
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(out)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.ExecuteContext(t.Context())
}

func TestDeleteCLIReviewsAliasesAndEscapesFields(t *testing.T) {
	for _, mode := range []string{"prompt", "yes", "decline", "eof"} {
		t.Run(mode, func(t *testing.T) {
			path := deleteFixture(t, deleteTestID)
			args := []string{deleteTestID[:8], deleteTestID}
			input := " YES \n"
			switch mode {
			case "yes":
				args = append(args, "--yes")
			case "decline":
				input = "n\n"
			case "eof":
				input = ""
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, runDelete(t, path, input, &stdout, &stderr, args...))
			require.Contains(t, stderr.String(), "1 explicit, 0 added")
			require.Equal(t, 1, strings.Count(stderr.String(), deleteTestID))
			require.Contains(t, stderr.String(), `first line\nsecond\rline\u2029end`)
			require.Contains(t, stderr.String(), `/proj\tRED\x1b[0m\u2028next`)
			require.NotContains(t, stderr.String(), deleteTestProject)
			db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testVaultKey))
			require.NoError(t, err)
			defer db.Close()
			var project string
			err = db.QueryRow("SELECT effective_project FROM vault_session_deletions WHERE session_uuid=?", deleteTestID).Scan(&project)
			if mode == "decline" || mode == "eof" {
				require.Equal(t, "aborted\n", stdout.String())
				require.ErrorIs(t, err, sql.ErrNoRows)
				require.NoError(t, db.QueryRow("SELECT project_path FROM vault_sessions WHERE uuid=?", deleteTestID).Scan(&project))
			} else {
				require.NoError(t, err)
				require.Equal(t, "deleted 1 session(s)\n"+deleteTestID+"\n", stdout.String())
			}
			require.Equal(t, deleteTestProject, project)
		})
	}
}

func TestDeleteCLIReportsBadArgumentsInOrder(t *testing.T) {
	path := deleteFixture(t, deleteTestID, "abcd1234-bbbb-4000-8000-000000000002")
	var stdout, stderr bytes.Buffer
	err := runDelete(t, path, "", &stdout, &stderr, deleteTestID, "short", "missing1", "abcd1234", "--yes")
	require.Error(t, err)
	msg := err.Error()
	pos := -1
	for _, part := range []string{`"short": session id must be at least 8`, `no session matches "missing1"`, `ambiguous session id "abcd1234" matches 2 sessions`, `first line\nsecond`} {
		i := strings.Index(msg, part)
		require.Greater(t, i, pos, part)
		pos = i
	}
	require.NotContains(t, msg, deleteTestProject)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())
	st := vault.NewVaultStore(path)
	defer st.Close()
	rows, err := st.ListSessions(t.Context(), vault.ListOptions{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestDeleteCLISingleLookupDiagnostics(t *testing.T) {
	path := deleteFixture(t, deleteTestID, "abcd1234-bbbb-4000-8000-000000000002")
	for _, tc := range []struct {
		id, message string
		candidates  int
	}{
		{"abcd1234", `ambiguous session id "abcd1234" (2 matches) — use more characters`, 2},
		{"missing1", `no session matches "missing1"`, 0},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runDelete(t, path, "", &stdout, &stderr, tc.id, "--yes")
			require.EqualError(t, err, tc.message)
			require.Empty(t, stdout.String())
			if tc.candidates == 0 {
				require.Empty(t, stderr.String())
			} else {
				require.Contains(t, stderr.String(), `ambiguous session id "abcd1234" matches 2 sessions:`)
				require.Equal(t, tc.candidates, strings.Count(stderr.String(), `first line\nsecond`))
			}
		})
	}
}

func TestDeleteCLIShortPrefixBeforeVaultOpen(t *testing.T) {
	t.Setenv("CAPY_VAULT_KEY", testVaultKey)
	path := filepath.Join(t.TempDir(), "missing.db")
	err := runDelete(t, path, "", io.Discard, io.Discard, "short")
	require.EqualError(t, err, "session id must be at least 8 characters")
	require.NoFileExists(t, path)
}

func TestDeleteCLIBrokenStdoutPipe(t *testing.T) {
	path := deleteFixture(t, deleteTestID)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, r.Close())
	defer w.Close()
	cmd := exec.CommandContext(t.Context(), "go", "run", "-tags", "fts5", ".", "vault", "delete", deleteTestID, "--yes", "--path", path, "--project-dir", t.TempDir())
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	require.Error(t, cmd.Run())
	require.Contains(t, stderr.String(), "deletion committed for 1 session")
	st := vault.NewVaultStore(path)
	defer st.Close()
	_, err = st.GetSession(t.Context(), deleteTestID)
	require.ErrorIs(t, err, vault.ErrSessionNotFound)
}

type failDeleteWrite struct{ after int }

func (w *failDeleteWrite) Write(p []byte) (int, error) {
	if w.after == 0 {
		return 0, errors.New("writer closed")
	}
	w.after--
	return len(p), nil
}

func TestDeleteCLIWriteFailures(t *testing.T) {
	for _, stage := range []string{"preview", "prompt", "success"} {
		t.Run(stage, func(t *testing.T) {
			path := deleteFixture(t, deleteTestID)
			out, stderr := io.Discard, io.Discard
			switch stage {
			case "preview":
				stderr = &failDeleteWrite{}
			case "prompt":
				stderr = &failDeleteWrite{after: 1}
			case "success":
				out = &failDeleteWrite{}
			}
			err := runDelete(t, path, "y\n", out, stderr, deleteTestID[:8])
			require.ErrorContains(t, err, "writer closed")
			st := vault.NewVaultStore(path)
			defer st.Close()
			_, lookup := st.GetSession(t.Context(), deleteTestID)
			if stage == "success" {
				require.ErrorContains(t, err, "committed for 1 session")
				require.ErrorIs(t, lookup, vault.ErrSessionNotFound)
			} else {
				require.NoError(t, lookup)
			}
		})
	}
}

func TestDeleteCLIWithDescendants(t *testing.T) {
	const leaf = "leaf0001-aaaa-4000-8000-000000000001"
	path := deleteFixture(t, deleteTestID, leaf)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runDelete(t, path, "", &stdout, &stderr, "--help"))
	require.Contains(t, stdout.String(), "By default, child archives survive")
	require.Contains(t, stdout.String(), "capy vault delete abcd1234 --with-descendants")
	st := vault.NewVaultStore(path)
	const child = "child001-aaaa-4000-8000-000000000002"
	rec := &vault.SessionRecord{Session: vault.Session{UUID: child, ParentUUID: deleteTestID, ProjectPath: "foreign project", RawJSONL: []byte("{}\n")}}
	require.NoError(t, st.InsertSession(t.Context(), rec))
	require.NoError(t, st.Close())
	stdout.Reset()
	require.NoError(t, runDelete(t, path, "yes\n", &stdout, &stderr, deleteTestID, "--with-descendants"))
	require.Contains(t, stderr.String(), "1 explicit, 1 added")
	require.Contains(t, stderr.String(), "descendant  "+child+"\n  Parent: "+deleteTestID)
	require.Contains(t, stderr.String(), "  Project: foreign project")
	require.Equal(t, "deleted 2 session(s)\n"+deleteTestID+"\n"+child+"\n", stdout.String())
	stderr.Reset()
	stdout.Reset()
	require.NoError(t, runDelete(t, path, "", &stdout, &stderr, leaf, "--with-descendants", "--yes"))
	require.Contains(t, stderr.String(), "1 explicit, 0 added")
	require.Equal(t, "deleted 1 session(s)\n"+leaf+"\n", stdout.String())
}

func TestVaultTUIDeleteUsesInvocationProjectPolicy(t *testing.T) {
	path := deleteFixture(t, deleteTestID)
	project := t.TempDir()
	cmd := &cobra.Command{Use: "list"}
	cmd.SetContext(t.Context())
	cmd.Flags().String("project-dir", project, "")
	err := runVaultTUI(cmd, &vaultEnv{dbPath: path}, tui.Options{Mode: "list"}, func(ctx context.Context, st *vault.VaultStore, opts tui.Options) (tui.Action, error) {
		// Edit the invocation project's file after launch, as an operator can
		// before opening a preview in a long-lived browser.
		require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[vault]\nremember_deletions = false\n"), 0600))
		remember, err := opts.ResolveRememberDeletions()
		require.NoError(t, err)
		require.False(t, remember)
		plan, err := st.PrepareDelete(ctx, []string{deleteTestID}, vault.DeleteOptions{ExactIDs: true, Remember: remember})
		require.NoError(t, err)
		return tui.Action{}, st.ApplyDelete(ctx, plan)
	})
	require.NoError(t, err)
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testVaultKey))
	require.NoError(t, err)
	defer db.Close()
	for _, table := range []string{"vault_sessions", "vault_session_deletions"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
}
