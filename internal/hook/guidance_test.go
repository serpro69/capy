package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionIDComponent(t *testing.T) {
	t.Parallel()
	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"uuid", "a1234567-89ab-cdef-0123-456789abcdef", "a1234567-89ab-cdef-0123-456789abcdef"},
		{"alphabet", alphabet, alphabet},
		{"dot", ".", "."},
		{"dot dot", "..", ".."},
		{"128 bytes", strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{"129 bytes", strings.Repeat("x", 129), "sha256-0ec9eb33e74510bcdd1f2ea55206e82f21649c5c2becbf2b433eb475b34c01bd"},
		{"last byte invalid", strings.Repeat("x", 127) + "!", "sha256-40f52af8e1fe01959201348e1d1fdb2edb67384589ea136930ebfe500d10fbd2"},
		{"slash", "a/b", "sha256-c14cddc033f64b9dea80ea675cf280a015e672516090a5626781153dc68fea11"},
		{"backslash", `a\b`, "sha256-c62016d0f8ee333350283fd879b50b692932e932794e5d686f7d37d67484e199"},
		{"NUL", "a\x00b", "sha256-59b271ae1bbcb1d31d41929817f4b16fb439eb4f31520b5ad1d5ce98920a7138"},
		{"unicode", "é", "sha256-4a99557e4033c3539de2eb65472017cad5f9557f7a0625a09f1c3f6e2ba69c4c"},
		{"space", "a b", "sha256-c8687a08aa5d6ed2044328fa6a697ab8e96dc34291e8c2034ae8c38e6fcc6d65"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sessionIDComponent(tt.id))
		})
	}
}

func TestGuidanceSessionFilenames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		id        string
		component string
	}{
		{"normal", "session-A_1.2", "session-A_1.2"},
		{"dot dot", "..", ".."},
		{"128 bytes", strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{"129 bytes", strings.Repeat("x", 129), "sha256-0ec9eb33e74510bcdd1f2ea55206e82f21649c5c2becbf2b433eb475b34c01bd"},
		{"escape capy", "x/../../outside", "sha256-caea68833ead84d1172dc8847cc9ed3a3f58e0bb5c5791f374c78c2eb9143264"},
		{"escape project", "x/../../../outside", "sha256-4172eacfd1c818a862e6f3cce28d8c72c000a13be806ccc8602c9b682cc43455"},
		{"backslash", `a\b`, "sha256-c62016d0f8ee333350283fd879b50b692932e932794e5d686f7d37d67484e199"},
		{"NUL", "a\x00b", "sha256-59b271ae1bbcb1d31d41929817f4b16fb439eb4f31520b5ad1d5ce98920a7138"},
		{"unicode", "é", "sha256-4a99557e4033c3539de2eb65472017cad5f9557f7a0625a09f1c3f6e2ba69c4c"},
		{"1 MiB", strings.Repeat("x", 1<<20), "sha256-8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent := t.TempDir()
			project := filepath.Join(parent, "project")
			require.NoError(t, os.Mkdir(project, 0o755))
			// Both traversal targets contain a different guidance type. Reading,
			// overwriting or removing either file must not be possible via the ID.
			sentinel := []byte(`{"grep":true}`)
			outside := []string{filepath.Join(parent, "outside.json"), filepath.Join(project, "outside.json")}
			for _, path := range outside {
				require.NoError(t, os.WriteFile(path, sentinel, 0o600))
			}

			for _, tool := range []string{"Read", "Grep"} {
				assert.NotEmpty(t, guidanceHookCall(t, project, tt.id, tool))
				assert.Empty(t, guidanceHookCall(t, project, tt.id, tool), "a fresh hook adapter should reuse persisted state")
			}
			stateDir := filepath.Join(project, ".capy")
			entries, err := os.ReadDir(stateDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "ID must produce one bounded filename, without nested directories")
			assert.False(t, entries[0].IsDir())
			assert.Equal(t, "guidance-"+tt.component+".json", entries[0].Name())
			stateFile := filepath.Join(stateDir, entries[0].Name())
			data, err := os.ReadFile(stateFile)
			require.NoError(t, err)
			assert.JSONEq(t, `{"read":true,"grep":true}`, string(data))

			ResetGuidanceFile(project, tt.id)
			_, err = os.Stat(stateFile)
			assert.ErrorIs(t, err, os.ErrNotExist)
			assert.NotEmpty(t, guidanceHookCall(t, project, tt.id, "Read"), "reset should allow guidance again")
			ResetGuidanceFile(project, tt.id)
			entries, err = os.ReadDir(stateDir)
			require.NoError(t, err)
			assert.Empty(t, entries)
			for _, path := range outside {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, sentinel, data, "outside state must remain untouched")
			}
			entries, err = os.ReadDir(project)
			require.NoError(t, err)
			assert.Len(t, entries, 2, "only .capy and the existing sentinel belong in the project")
			entries, err = os.ReadDir(parent)
			require.NoError(t, err)
			assert.Len(t, entries, 2, "only the project and existing sentinel belong outside .capy")
		})
	}
}

func TestGuidanceSessionIsolation(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	// These IDs would collide under common replacement/cleaning/truncation schemes.
	ids := []string{"a/b", `a\b`, "a_b", "a\x00b", "a b", "a/../b", "b", strings.Repeat("x", 129), strings.Repeat("x", 128) + "y"}
	for _, id := range ids {
		assert.NotEmpty(t, guidanceHookCall(t, project, id, "Read"))
	}
	for _, id := range ids {
		assert.Empty(t, guidanceHookCall(t, project, id, "Read"))
	}
	ResetGuidanceFile(project, ids[0])
	assert.NotEmpty(t, guidanceHookCall(t, project, ids[0], "Read"))
	for _, id := range ids[1:] {
		assert.Empty(t, guidanceHookCall(t, project, id, "Read"), "reset must leave other sessions alone")
	}
}

func TestGuidanceEmptySessionDoesNotPersist(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	for range 2 {
		output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "")
		require.NoError(t, err)
		assert.NotEmpty(t, output)
	}
	ResetGuidanceFile(project, "")
	entries, err := os.ReadDir(project)
	require.NoError(t, err)
	assert.Empty(t, entries, "empty IDs must not create .capy or a state file")

	// Even a preexisting empty-ID filename must not be reset.
	stateDir := filepath.Join(project, ".capy")
	require.NoError(t, os.Mkdir(stateDir, 0o755))
	stateFile := filepath.Join(stateDir, "guidance-.json")
	require.NoError(t, os.WriteFile(stateFile, []byte(`{"read":true}`), 0o600))
	ResetGuidanceFile(project, "")
	data, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"read":true}`, string(data))
}

func guidanceHookCall(t *testing.T, project, sessionID, tool string) []byte {
	t.Helper()
	input, err := json.Marshal(map[string]any{
		"tool_name": tool, "tool_input": map[string]any{}, "session_id": sessionID,
	})
	require.NoError(t, err)
	// Supply the temporary project directly to keep filename tests independent
	// of the production entry point's environment/project selection.
	output, err := handlePreToolUse(input, &testAdapter{}, nil, project)
	require.NoError(t, err)
	return output
}
