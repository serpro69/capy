package hook

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuidanceAtomicClaim(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	const workers = 32
	start := make(chan struct{})
	results := make(chan bool, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "session")
			results <- len(output) != 0
			errors <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	winners := 0
	for emitted := range results {
		if emitted {
			winners++
		}
	}
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, winners)
	marker := filepath.Join(project, ".capy", "guidance-session-read.once")
	info, err := os.Lstat(marker)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.EqualValues(t, 0, info.Size())
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoFileExists(t, filepath.Join(project, ".capy", "guidance-session.json"))
	for _, guidanceType := range []string{"bash", "grep"} {
		output, err := guidanceOnce(guidanceType, "advisory", ccAdapter(), project, "session")
		require.NoError(t, err)
		require.NotEmpty(t, output, "types cannot suppress one another")
	}
	t.Run("existing directory mode", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, ".capy")
		require.NoError(t, os.Mkdir(dir, 0o750))
		before, err := os.Stat(dir)
		require.NoError(t, err)
		_, err = guidanceOnce("bash", BASH_GUIDANCE, ccAdapter(), root, "s")
		require.NoError(t, err)
		after, err := os.Stat(dir)
		require.NoError(t, err)
		require.Equal(t, before.Mode(), after.Mode())
	})
	t.Run("only fixed types", func(t *testing.T) {
		for _, kind := range []string{"", "READ", "other", "read/../../outside"} {
			root := t.TempDir()
			output, err := guidanceOnce(kind, "advisory", ccAdapter(), root, "s")
			require.NoError(t, err)
			require.NotEmpty(t, output)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Empty(t, entries, "invalid types must not create any state")
		}
	})
}

func TestGuidanceLegacyState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, data string
		valid      bool
		claimed    []string
	}{
		{"all keys", `{"bash":true,"read":true,"grep":true}`, true, []string{"bash", "read", "grep"}},
		{"false is membership", `{"bash":false,"read":false,"grep":false}`, true, []string{"bash", "read", "grep"}},
		{"one key", `{"read":false}`, true, []string{"read"}},
		{"unrelated key", `{"other":true}`, true, nil},
		{"empty object", `{}`, true, nil},
		{"exact bound", `{"read":true}` + strings.Repeat(" ", maxLegacyGuidanceBytes-len(`{"read":true}`)), true, []string{"read"}},
		{"empty", "", false, nil},
		{"corrupt", "secret payload {", false, nil},
		{"trailing JSON", `{"read":true}{}`, false, nil},
		{"trailing junk", `{"read":true}x`, false, nil},
		{"oversized", `{"read":true}` + strings.Repeat(" ", maxLegacyGuidanceBytes), false, nil},
		{"null map", `null`, false, nil},
		{"null value", `{"read":null}`, false, nil},
		{"array", `[]`, false, nil},
		{"non boolean", `{"read":"true"}`, false, nil},
		{"unrelated non boolean", `{"read":true,"other":42}`, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project := t.TempDir()
			dir := filepath.Join(project, ".capy")
			require.NoError(t, os.Mkdir(dir, 0o755))
			legacy := filepath.Join(dir, "guidance-s.json")
			require.NoError(t, os.WriteFile(legacy, []byte(tc.data), 0o600))
			for _, guidanceType := range []string{"bash", "read", "grep"} {
				claimed := false
				for _, known := range tc.claimed {
					claimed = claimed || known == guidanceType
				}
				for call := range 2 {
					output, err := guidanceOnce(guidanceType, "advisory", ccAdapter(), project, "s")
					require.NoError(t, err)
					if claimed || tc.valid && call == 1 {
						require.Empty(t, output)
					} else {
						require.NotEmpty(t, output)
					}
				}
				marker := filepath.Join(dir, "guidance-s-"+guidanceType+".once")
				if tc.valid && !claimed {
					require.FileExists(t, marker)
				} else {
					require.NoFileExists(t, marker)
				}
			}
			data, err := os.ReadFile(legacy)
			require.NoError(t, err)
			require.Equal(t, tc.data, string(data), "legacy state must stay byte-identical")
		})
	}
}

func TestGuidanceStateFailureFallback(t *testing.T) {
	// Deliberately sequential: capture the process-wide logger to prove that
	// state diagnostics cannot expose caller strings, paths or legacy bytes.
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	for _, name := range []string{
		"directory symlink", "directory file", "legacy symlink", "legacy dangling symlink", "legacy directory", "legacy fifo",
		"marker symlink", "marker dangling symlink", "marker directory", "marker fifo", "marker nonempty", "invalid type",
		"unwritable directory", "unreadable legacy",
	} {
		t.Run(name, func(t *testing.T) {
			if (name == "unwritable directory" || name == "unreadable legacy") && os.Geteuid() == 0 {
				t.Skip("requires unprivileged filesystem permissions")
			}
			logs.Reset()
			project, outside := t.TempDir(), t.TempDir()
			dir := filepath.Join(project, ".capy")
			legacy, marker := filepath.Join(dir, "guidance-secret-session.json"), filepath.Join(dir, "guidance-secret-session-read.once")
			sentinel := filepath.Join(outside, "sentinel")
			sentinelContent := `{"read":true,"secret":false}`
			if name == "marker symlink" {
				sentinelContent = "" // following this target would wrongly suppress the advisory
			}
			require.NoError(t, os.WriteFile(sentinel, []byte(sentinelContent), 0o600))
			target := sentinel
			if strings.Contains(name, "dangling") {
				target = filepath.Join(outside, "missing")
			}
			if name == "directory symlink" {
				require.NoError(t, os.Symlink(outside, dir))
			} else if name == "directory file" {
				require.NoError(t, os.WriteFile(dir, []byte("sentinel"), 0o600))
			} else {
				require.NoError(t, os.Mkdir(dir, 0o755))
			}
			kind := "read"
			switch name {
			case "legacy symlink", "legacy dangling symlink":
				require.NoError(t, os.Symlink(target, legacy))
			case "legacy directory":
				require.NoError(t, os.Mkdir(legacy, 0o700))
			case "legacy fifo":
				require.NoError(t, syscall.Mkfifo(legacy, 0o600))
			case "marker symlink", "marker dangling symlink":
				require.NoError(t, os.Symlink(target, marker))
			case "marker directory":
				require.NoError(t, os.Mkdir(marker, 0o700))
			case "marker fifo":
				require.NoError(t, syscall.Mkfifo(marker, 0o600))
			case "marker nonempty":
				require.NoError(t, os.WriteFile(marker, []byte("secret"), 0o600))
			case "invalid type":
				kind = "../secret-type"
			case "unwritable directory":
				require.NoError(t, os.Chmod(dir, 0o500))
				defer os.Chmod(dir, 0o755)
			case "unreadable legacy":
				require.NoError(t, os.WriteFile(legacy, []byte(`{"read":true}`), 0o000))
				defer os.Chmod(legacy, 0o600)
			}
			before, err := os.ReadDir(outside)
			require.NoError(t, err)
			for range 2 {
				output, err := guidanceOnce(kind, "secret-content", ccAdapter(), project, "secret-session")
				require.NoError(t, err)
				require.Equal(t, "secret-content", ccParse(t, output)["additionalContext"])
				require.NotContains(t, ccParse(t, output), "permissionDecision")
			}
			require.Equal(t, 2, strings.Count(logs.String(), "could not claim session guidance"))
			require.Less(t, logs.Len(), 512)
			require.NotContains(t, logs.String(), "secret")
			require.NotContains(t, logs.String(), project)
			after, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Equal(t, len(before), len(after))
			data, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, sentinelContent, string(data))
			if strings.HasPrefix(name, "legacy") || name == "unwritable directory" || name == "unreadable legacy" || name == "invalid type" {
				require.NoFileExists(t, marker)
			}
		})
	}
}

type failingGuidanceAdapter struct{ adapter.ClaudeCodeAdapter }

func (*failingGuidanceAdapter) FormatAllow(string) ([]byte, error) {
	return nil, errors.New("format failed")
}

func TestGuidanceFormatBeforeClaim(t *testing.T) {
	project := t.TempDir()
	_, err := guidanceOnce("read", "advisory", &failingGuidanceAdapter{}, project, "s")
	require.EqualError(t, err, "format failed")
	require.NoDirExists(t, filepath.Join(project, ".capy"))
	output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "s")
	require.NoError(t, err)
	require.NotEmpty(t, output)
}

func TestGuidanceMissingProject(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	for range 2 {
		output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), "", "session")
		require.NoError(t, err)
		require.NotEmpty(t, output)
	}
	ResetGuidanceFile("", "session")
	entries, err := os.ReadDir(project)
	require.NoError(t, err)
	require.Empty(t, entries, "missing project must not create state under process cwd")
}

func TestGuidanceResetIsolation(t *testing.T) {
	t.Parallel()
	for _, session := range []string{"s", "s-read", "../s", "", "codex:s"} {
		t.Run(fmt.Sprintf("session=%q", session), func(t *testing.T) {
			project := t.TempDir()
			dir := filepath.Join(project, ".capy")
			require.NoError(t, os.Mkdir(dir, 0o755))
			component := sessionIDComponent(session)
			expected := map[string]bool{}
			for _, id := range []string{component, component + "-other"} {
				for _, suffix := range []string{".json", "-bash.once", "-read.once", "-grep.once", "-other.once"} {
					name := "guidance-" + id + suffix
					require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
					expected[name] = session == "" || id != component || suffix == "-other.once"
				}
			}
			for _, name := range []string{"tool-observations.json", "tool-observations.lock", "codex-main-observations.json", "codex-main-observations.lock", "unrelated"} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("unchanged"), 0o600))
				expected[name] = true
			}
			lock := filepath.Join(dir, "tool-observations.lock")
			before, err := os.Stat(lock)
			require.NoError(t, err)
			ResetGuidanceFile(project, session)
			for name, keep := range expected {
				_, err := os.Lstat(filepath.Join(dir, name))
				if keep {
					require.NoError(t, err, name)
				} else {
					require.ErrorIs(t, err, os.ErrNotExist, name)
				}
			}
			after, err := os.Stat(lock)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			if session != "" {
				for _, kind := range []string{"read", "bash", "grep"} {
					output, err := guidanceOnce(kind, "advisory", ccAdapter(), project, session)
					require.NoError(t, err)
					require.NotEmpty(t, output)
					output, err = guidanceOnce(kind, "advisory", ccAdapter(), project, session)
					require.NoError(t, err)
					require.Empty(t, output)
				}
			}
		})
	}
	t.Run("symlink directory", func(t *testing.T) {
		project, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(project, ".capy")))
		for _, suffix := range []string{".json", "-bash.once", "-read.once", "-grep.once"} {
			require.NoError(t, os.WriteFile(filepath.Join(outside, "guidance-s"+suffix), nil, 0o600))
		}
		ResetGuidanceFile(project, "s")
		entries, err := os.ReadDir(outside)
		require.NoError(t, err)
		assert.Len(t, entries, 4)
	})
}

func TestGuidanceKilledWinner(t *testing.T) {
	if project := os.Getenv("CAPY_TEST_GUIDANCE_WINNER"); project != "" {
		output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "killed")
		if err != nil || len(output) == 0 {
			os.Exit(3)
		}
		// Signal that the claim exists, but never deliver the advisory bytes.
		fmt.Fprintln(os.Stdout, "claimed")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(4)
	}
	project := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGuidanceKilledWinner$")
	cmd.Env = append(os.Environ(), "CAPY_TEST_GUIDANCE_WINNER="+project)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		cancel()
		cmd.Wait()
		t.Fatalf("claim handshake: %v, %s", err, stderr.String())
	}
	require.Equal(t, "claimed\n", ready)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NoError(t, ctx.Err())
	output, err := guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "killed")
	require.NoError(t, err)
	require.Empty(t, output, "a killed winner leaves its optional advisory claimed")
	ResetGuidanceFile(project, "killed")
	output, err = guidanceOnce("read", READ_GUIDANCE, ccAdapter(), project, "killed")
	require.NoError(t, err)
	require.NotEmpty(t, output)
}
