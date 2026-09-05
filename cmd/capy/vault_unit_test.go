package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDateFlag(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		endOfDay bool
		wantErr  bool
		zero     bool
	}{
		{name: "empty is zero, no error", in: "", zero: true},
		{name: "date only", in: "2026-05-01"},
		{name: "rfc3339", in: "2026-05-01T10:30:00Z"},
		{name: "garbage", in: "not-a-date", wantErr: true},
		{name: "out of range", in: "2026-13-99", wantErr: true},
		{name: "endOfDay ignored when empty", in: "", endOfDay: true, zero: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateFlag(tt.in, tt.endOfDay)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.zero, got.IsZero())
		})
	}
}

func TestParseDateFlag_EndOfDaySemantics(t *testing.T) {
	// A date-only --before must cover the whole target day (inclusive).
	before, err := parseDateFlag("2026-05-01", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 23, 59, 59, 0, time.UTC), before)

	// A date-only --after stays at start-of-day (already inclusive of the day).
	after, err := parseDateFlag("2026-05-01", false)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), after)

	// An explicit RFC3339 timestamp is used verbatim regardless of endOfDay.
	exact, err := parseDateFlag("2026-05-01T08:15:00Z", true)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 5, 1, 8, 15, 0, 0, time.UTC), exact)
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "shorter than max", in: "abc", max: 5, want: "abc"},
		{name: "equal to max", in: "abcde", max: 5, want: "abcde"},
		{name: "longer truncates with ellipsis", in: "abcdef", max: 5, want: "abcd…"},
		{name: "multibyte counts runes", in: "héllo wörld", max: 6, want: "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncate(tt.in, tt.max))
		})
	}
}

func TestSubagentDisplayID(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		want string
	}{
		{name: "agent jsonl", rel: "subagents/agent-abc123.jsonl", want: "abc123"},
		{name: "non-agent subagent jsonl", rel: "subagents/other.jsonl", want: "other"},
		{name: "subagent meta json not rendered", rel: "subagents/agent-x.meta.json", want: ""},
		{name: "tool result not a subagent", rel: "tool-results/t1.json", want: ""},
		{name: "loose file", rel: "notes.txt", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, subagentDisplayID(tt.rel))
		})
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range []string{"user", "assistant", "tool", "system"} {
		assert.True(t, validRole(r), r)
	}
	assert.False(t, validRole("bogus"))
	assert.False(t, validRole(""))
}

func TestShortUUID(t *testing.T) {
	assert.Equal(t, "abcd1234", shortUUID("abcd1234-5678-90ab"))
	assert.Equal(t, "short", shortUUID("short"))
}

func TestDisplayPath(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	assert.Equal(t, "-", displayPath(""))
	assert.Equal(t, "~/proj/capy", displayPath("/home/tester/proj/capy"))
	assert.Equal(t, "/var/data/x", displayPath("/var/data/x"))
}

func TestOneLine(t *testing.T) {
	assert.Equal(t, "a b c", oneLine("a\nb  c"))
	assert.Equal(t, "x y", oneLine("  x\t\ny  "))
}

func TestHandleLookupError(t *testing.T) {
	t.Run("ambiguous lists candidates", func(t *testing.T) {
		amb := &vault.AmbiguousUUIDError{
			Prefix: "abcd1234",
			Candidates: []vault.Session{
				{UUID: "abcd1234-1111", Title: "one", ProjectPath: "/p", EndTime: time.Now()},
				{UUID: "abcd1234-2222", Title: "two", ProjectPath: "/p", EndTime: time.Now()},
			},
		}
		err := handleLookupError("abcd1234", amb)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ambiguous")
	})

	t.Run("not found", func(t *testing.T) {
		err := handleLookupError("zzzzzzzz", vault.ErrSessionNotFound)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no session matches")
	})

	t.Run("other error passes through", func(t *testing.T) {
		orig := assert.AnError
		err := handleLookupError("x", orig)
		assert.Equal(t, orig, err)
	})
}

func TestRenameOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		clear   bool
		want    vault.RenameOptions
		wantErr string
	}{
		{name: "name argument", args: []string{"abcd1234", "New name"}, want: vault.RenameOptions{Name: "New name"}},
		{name: "clear", args: []string{"abcd1234"}, clear: true, want: vault.RenameOptions{Clear: true}},
		{name: "name and clear are exclusive", args: []string{"abcd1234", "New name"}, clear: true, wantErr: "mutually exclusive"},
		{name: "missing name", args: []string{"abcd1234"}, wantErr: "provide a name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renameOptions(tt.args, tt.clear)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// buildVaultWithSession creates an encrypted vault at vaultPath under key and
// imports one fixture session, returning its UUID. It sets CAPY_VAULT_KEY (which
// VaultStore.Open consumes) to key, so callers flip the env var afterward to
// assert which key opens the rotated DB.
func buildVaultWithSession(t *testing.T, vaultPath, key string) string {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", key)
	t.Setenv("CAPY_MACHINE_ID", "test-machine") // avoid touching ~/.config/capy

	root := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.MkdirAll(root, 0o755))
	uuid := "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","cwd":"/home/user/proj","gitBranch":"main","message":{"role":"user","content":"Fix the brontosaurus timeout"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-01T10:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
		`{"type":"ai-title","aiTitle":"Fix the brontosaurus timeout","sessionId":"s1"}`,
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, uuid+".jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644))

	ctx := context.Background()
	sessions, err := vault.DiscoverSessions(root)
	require.NoError(t, err)
	st := vault.NewVaultStore(vaultPath)
	require.NoError(t, st.Open(ctx))
	res := vault.Import(ctx, st, sessions, vault.ImportOptions{})
	require.NoError(t, st.Close())
	require.Equal(t, 1, res.Imported, "fixture session should import")
	return uuid
}

// listVaultWith opens the vault under key and returns its sessions. The returned
// error is the open error, so callers can assert that a given key does/does not
// open the rotated DB.
func listVaultWith(t *testing.T, vaultPath, key string) ([]vault.Session, error) {
	t.Helper()
	t.Setenv("CAPY_VAULT_KEY", key)
	ctx := context.Background()
	st := vault.NewVaultStore(vaultPath)
	defer st.Close()
	if err := st.Open(ctx); err != nil {
		return nil, err
	}
	return st.ListSessions(ctx, vault.ListOptions{})
}

func TestRunVaultRekey_RoundTrip(t *testing.T) {
	const oldKey = "old-vault-key-at-least-32-characters-long!!"
	const newKey = "new-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	uuid := buildVaultWithSession(t, vaultPath, oldKey)

	require.NoError(t, runVaultRekey(vaultPath, oldKey, newKey, false))
	assert.FileExists(t, vaultPath+".bak", ".bak must be preserved by default")

	// The new key opens the rotated vault and the session is intact.
	list, err := listVaultWith(t, vaultPath, newKey)
	require.NoError(t, err, "new key must open the rotated vault")
	require.Len(t, list, 1)
	assert.Equal(t, uuid, list[0].UUID)

	// The old key no longer opens the rotated vault.
	_, err = listVaultWith(t, vaultPath, oldKey)
	require.Error(t, err, "old key must not open the rotated vault")
}

func TestRunVaultRekey_RemoveBackup(t *testing.T) {
	const oldKey = "old-vault-key-at-least-32-characters-long!!"
	const newKey = "new-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	buildVaultWithSession(t, vaultPath, oldKey)

	require.NoError(t, runVaultRekey(vaultPath, oldKey, newKey, true))
	assert.NoFileExists(t, vaultPath+".bak", "--remove-backup must unlink the old-key .bak")

	// The rotation still succeeded: the new key opens the vault.
	list, err := listVaultWith(t, vaultPath, newKey)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestRunVaultRekey_RejectsNewEqualsOld(t *testing.T) {
	const key = "same-vault-key-at-least-32-characters-long!!"

	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.db")
	buildVaultWithSession(t, vaultPath, key)

	before, err := os.ReadFile(vaultPath)
	require.NoError(t, err)

	err = runVaultRekey(vaultPath, key, key, false)
	require.Error(t, err, "rekey must reject a new key identical to the old")
	assert.Contains(t, err.Error(), "identical")
	assert.NoFileExists(t, vaultPath+".bak", "a rejected rekey must not touch the vault")

	after, err := os.ReadFile(vaultPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a rejected rekey must leave the vault byte-identical")

	// The vault still opens with its unchanged key.
	list, err := listVaultWith(t, vaultPath, key)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestResolveMergeKey(t *testing.T) {
	// --key flag wins over both env vars.
	t.Setenv("CAPY_VAULT_MERGE_KEY", "mergeenv")
	t.Setenv("CAPY_VAULT_KEY", "vaultenv")
	key, src := resolveMergeKey("flagkey")
	assert.Equal(t, "flagkey", key)
	assert.Equal(t, "--key", src)

	// No flag → CAPY_VAULT_MERGE_KEY.
	key, src = resolveMergeKey("")
	assert.Equal(t, "mergeenv", key)
	assert.Equal(t, "CAPY_VAULT_MERGE_KEY", src)

	// No flag, no merge env → fall back to CAPY_VAULT_KEY (shared-passphrase case).
	t.Setenv("CAPY_VAULT_MERGE_KEY", "")
	key, src = resolveMergeKey("")
	assert.Equal(t, "vaultenv", key)
	assert.Equal(t, "CAPY_VAULT_KEY", src)
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "vault.db")
	b := filepath.Join(dir, "other.db")

	same, err := samePath(a, a)
	require.NoError(t, err)
	assert.True(t, same, "identical paths are the same vault")

	same, err = samePath(a, b)
	require.NoError(t, err)
	assert.False(t, same, "distinct paths are different vaults")

	// A relative spelling of the same file resolves to the same absolute path.
	rel, err := filepath.Rel(mustGetwd(t), a)
	require.NoError(t, err)
	same, err = samePath(rel, a)
	require.NoError(t, err)
	assert.True(t, same, "a relative path to the same file is the same vault")
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return wd
}
