package runstore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

const testKey = "synthetic-run-store-test-credential-1234567890"

func TestCaptureAndReopen(t *testing.T) {
	root := t.TempDir()
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	program := Program{Owner: root, Request: executor.ExecRequest{Language: executor.Shell, Code: "synthetic private program"}, TTLHours: 24, MaxRunBytes: 1 << 20}
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, program)
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.Error(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants), "a run may be authorized once only")
	require.NoError(t, st.Started(t.Context(), row.ID, "run"))
	out := []byte{0, 255, 'a', '\n', 'b'}
	require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: out}))
	require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 2, Phase: "run", Stream: "stderr", Data: []byte("failure marker")}))
	code := 7
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, ExitCode: &code, ObservedBytes: int64(len(out) + len("failure marker")), ElapsedMS: 15}))
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, st.Close())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, secret := range []string{"SQLite format 3", "synthetic private program", "failure marker", testKey} {
		require.NotContains(t, string(raw), secret)
	}
	st, err = Open(t.Context(), path, testKey, false)
	require.NoError(t, err)
	defer st.Close()
	got, err := st.Load(t.Context(), row.ID)
	require.NoError(t, err)
	require.Equal(t, 7, *got.Result.ExitCode)
	require.Equal(t, "complete", got.Result.Capture.State)
	require.Equal(t, "retired", string(got.Cleanup.State))
	var collected []byte
	cursor := ""
	for {
		page, err := st.ReadRaw(t.Context(), row.ID, "stdout", cursor, 2)
		require.NoError(t, err)
		for _, f := range page.Fragments {
			collected = append(collected, f.Data...)
		}
		if page.EOF {
			break
		}
		require.NotNil(t, page.NextCursor)
		cursor = *page.NextCursor
	}
	require.Equal(t, out, collected)
	_, err = st.ReadRaw(t.Context(), row.ID, "stderr", cursor, 2)
	require.Error(t, err, "cross-stream cursor must fail")
	_, err = st.db.ExecContext(t.Context(), "SELECT count(*) FROM sources")
	require.Error(t, err, "run storage must not contain knowledge schema")
}

func TestAdmissionFailuresHaveNoEffects(t *testing.T) {
	for _, mode := range []string{"empty", "wrong", "symlink directory", "symlink database", "bad mode", "nonregular", "missing query"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path, err := config.ResolveRunDBPath(root)
			require.NoError(t, err)
			key := testKey
			create := true
			var before []byte
			var beforeLocks []os.DirEntry
			switch mode {
			case "empty":
				key = ""
			case "wrong":
				st, err := Open(t.Context(), path, key, true)
				require.NoError(t, err)
				require.NoError(t, st.Close())
				before, err = os.ReadFile(path)
				require.NoError(t, err)
				beforeLocks, err = os.ReadDir(filepath.Join(filepath.Dir(path), "locks"))
				require.NoError(t, err)
				key = "wrong-synthetic-credential"
			case "symlink directory":
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, ".capy")))
			case "symlink database", "bad mode", "nonregular":
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				if mode == "symlink database" {
					require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "target"), path))
				}
				if mode == "bad mode" {
					require.NoError(t, os.WriteFile(path, []byte("untouched"), 0o644))
				}
				if mode == "nonregular" {
					require.NoError(t, os.Mkdir(path, 0o700))
				}
			case "missing query":
				create = false
			}
			st, err := Open(t.Context(), path, key, create)
			require.Error(t, err)
			require.Nil(t, st)
			if mode == "empty" || mode == "missing query" {
				require.NoDirExists(t, filepath.Join(root, ".capy"))
			}
			if mode == "wrong" {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.True(t, bytes.Equal(before, after))
				entries, err := os.ReadDir(filepath.Join(filepath.Dir(path), "locks"))
				require.NoError(t, err)
				require.Equal(t, beforeLocks, entries, "wrong-key access must not alter the fixed lock registry")
			}
		})
	}
}

func TestIncompleteOwnerIsNotReused(t *testing.T) {
	root := t.TempDir()
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	defer st.Close()
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{Owner: root, TTLHours: 24, MaxRunBytes: 1 << 20})
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, owner.Lease.Close())
	_, err = st.ClaimOwner(t.Context())
	require.ErrorContains(t, err, "unverified cleanup")
}

func TestWriteDeadline(t *testing.T) {
	root := t.TempDir()
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, err = st.ClaimOwner(ctx)
	require.Error(t, err)
}

func TestLiteralPercentOwnerPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "owner%2Fname #?")
	require.NoError(t, os.Mkdir(root, 0o700))
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	require.FileExists(t, path)
	require.NoDirExists(t, filepath.Join(filepath.Dir(root), "owner"))
}

func TestAppendBatchRollbackPreservesExactPrefix(t *testing.T) {
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	st, err := OpenWithPolicy(t.Context(), path, testKey, true, PolicyConfig{TTLHours: 24, MaxRunBytes: 20, MaxRetainedBytes: 512 << 20})
	require.NoError(t, err)
	defer st.Close()
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	defer owner.Lease.Close()
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{TTLHours: 24, MaxRunBytes: 20})
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, st.Started(t.Context(), row.ID, "run"))
	event := func(seq uint64, data string) executor.OutputEvent {
		return executor.OutputEvent{Sequence: seq, Phase: "run", Stream: "stdout", Data: []byte(data)}
	}
	require.NoError(t, st.Append(t.Context(), row.ID, event(1, "prefix")))
	require.Error(t, st.AppendBatch(t.Context(), row.ID, []executor.OutputEvent{event(2, "rollback"), event(3, "over capture limit")}))
	require.Error(t, st.Append(t.Context(), row.ID, event(4, "hole")))
	page, err := st.ReadRaw(t.Context(), row.ID, "merged", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Fragments, 1)
	require.Equal(t, "prefix", string(page.Fragments[0].Data))
	require.EqualValues(t, 6, page.Run.Capture.CapturedBytes)
}
