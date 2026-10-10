package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func observationFixture(t *testing.T) (observationStore, *time.Time) {
	t.Helper()
	s := newObservationStore(t.TempDir())
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	return s, &now
}

func observationFile(s observationStore) string {
	return filepath.Join(s.dir, "tool-observations.json")
}

func TestObservationsExpiryAndConsumption(t *testing.T) {
	s, now := observationFixture(t)
	require.NoError(t, s.record("session", "child", observedExecute))
	*now = now.Add(59 * time.Second)
	require.NoError(t, s.record("session", "child", observedFetch))
	*now = now.Add(time.Second)
	tool, err := s.consume("session", "child", false)
	require.NoError(t, err)
	assert.Empty(t, tool, "fetch must not extend execute's own 60-second expiry")
	tool, err = s.consume("session", "child", true)
	require.NoError(t, err)
	assert.Equal(t, observedFetch, tool)
	tool, err = s.consume("session", "child", true)
	require.NoError(t, err)
	assert.Empty(t, tool, "observation can only authorize one redirect")

	require.NoError(t, s.record("session", "child", observedExecute))
	require.NoError(t, s.record("session", "child", observedFetch))
	*now = now.Add(observationTTL - time.Millisecond)
	tool, err = s.consume("session", "child", false)
	require.NoError(t, err)
	assert.Equal(t, observedExecute, tool, "one millisecond before expiry remains valid")
	tool, err = s.consume("session", "child", true)
	require.NoError(t, err)
	assert.Empty(t, tool, "the redirect consumes the alternative fetch observation too")

	require.NoError(t, s.record("session", "child", observedExecute))
	*now = now.Add(observationTTL)
	tool, err = s.consume("session", "child", true)
	require.NoError(t, err)
	assert.Empty(t, tool, "expiry is strict at 60 seconds")
}

func TestObservationsCapsAndIdentity(t *testing.T) {
	s, now := observationFixture(t)
	for i := range maxObservationEntries + 2 {
		*now = now.Add(time.Millisecond)
		require.NoError(t, s.record(fmt.Sprintf("%0128d", i), strings.Repeat("a", 128), observedExecute))
	}
	data, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(data), maxObservationBytes)
	state, err := readObservations(observationFile(s), now.UnixMilli())
	require.NoError(t, err)
	require.Len(t, state.Entries, maxObservationEntries)
	assert.Equal(t, fmt.Sprintf("%0128d", 2), state.Entries[0].Session, "oldest entries are evicted first")
	info, err := os.Stat(observationFile(s))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	*now = now.Add(observationTTL)
	for _, agent := range []string{"a/b", `a\b`, "a_b", "a\x00b", strings.Repeat("long", 100)} {
		require.NoError(t, s.record("session/../one", agent, observedFetch))
	}
	state, err = readObservations(observationFile(s), now.UnixMilli())
	require.NoError(t, err)
	require.Len(t, state.Entries, 5, "expired entries are removed before admission")
	seen := make(map[string]bool)
	for _, entry := range state.Entries {
		assert.Equal(t, sessionIDComponent("session/../one"), entry.Session)
		assert.False(t, seen[entry.Agent], "unsafe IDs must not be collapsed by path cleaning")
		seen[entry.Agent] = true
		assert.Equal(t, entry.Agent, sessionIDComponent(entry.Agent))
	}
	files, err := os.ReadDir(s.dir)
	require.NoError(t, err)
	assert.Len(t, files, 2, "only one state file and its permanent lock are retained")
	// A killed writer can leave at most one staging file; the next writer reuses
	// its name without a directory scan or an ever-growing set of temp files.
	require.NoError(t, os.WriteFile(observationFile(s)+".tmp", []byte("abandoned write"), 0o600))
	require.NoError(t, s.record("session", "child", observedFetch))
	assert.NoFileExists(t, observationFile(s)+".tmp")
}

func TestObservationsRejectMalformedState(t *testing.T) {
	for _, name := range []string{"invalid JSON", "null", "missing entries", "unknown field", "future", "negative", "unknown tool", "unsafe identity", "duplicate identity", "too many entries", "oversized", "trailing data"} {
		t.Run(name, func(t *testing.T) {
			s, now := observationFixture(t)
			require.NoError(t, s.record("session", "child", observedExecute))
			state := observationState{Entries: []toolObservation{{Session: "session", Agent: "child", Tools: map[string]int64{observedExecute: now.UnixMilli()}}}}
			switch name {
			case "future":
				state.Entries[0].Tools[observedExecute]++
			case "negative":
				state.Entries[0].Tools[observedExecute] = -1
			case "unknown tool":
				state.Entries[0].Tools = map[string]int64{"capy_search": now.UnixMilli()}
			case "unsafe identity":
				state.Entries[0].Agent = "../child"
			case "duplicate identity":
				state.Entries = append(state.Entries, state.Entries[0])
			case "too many entries":
				for i := range maxObservationEntries {
					state.Entries = append(state.Entries, toolObservation{Session: "session", Agent: fmt.Sprint(i), Tools: map[string]int64{observedExecute: now.UnixMilli()}})
				}
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			switch name {
			case "invalid JSON":
				data = []byte(`{broken`)
			case "null":
				data = []byte(`null`)
			case "missing entries":
				data = []byte(`{}`)
			case "unknown field":
				data = []byte(`{"entries":[],"surprise":true}`)
			case "oversized":
				data = append(data, []byte(strings.Repeat(" ", maxObservationBytes))...)
			case "trailing data":
				data = append(data, []byte(` {}`)...)
			}
			require.NoError(t, os.WriteFile(observationFile(s), data, 0o600))
			tool, err := s.consume("session", "child", true)
			assert.Error(t, err)
			assert.Empty(t, tool)
			assert.Error(t, s.record("session", "child", observedFetch), "invalid state must not be silently treated as evidence")
			after, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			assert.Equal(t, data, after)
		})
	}
}

func TestObservationsRejectSpecialFiles(t *testing.T) {
	for _, target := range []string{"tool-observations.json", "tool-observations.lock"} {
		for _, kind := range []string{"symlink", "fifo", "directory"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				s, _ := observationFixture(t)
				require.NoError(t, os.Mkdir(s.dir, 0o700))
				path := filepath.Join(s.dir, target)
				switch kind {
				case "symlink":
					outside := filepath.Join(t.TempDir(), "outside")
					require.NoError(t, os.WriteFile(outside, []byte("sentinel"), 0o600))
					require.NoError(t, os.Symlink(outside, path))
				case "fifo":
					require.NoError(t, syscall.Mkfifo(path, 0o600))
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				}
				assert.Error(t, s.record("session", "child", observedExecute))
			})
		}
	}
}

func TestObservationsFailedConsumptionWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires directory permissions to restrict writes")
	}
	s, _ := observationFixture(t)
	require.NoError(t, s.record("session", "child", observedExecute))
	before, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(s.dir, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(s.dir, 0o700)) })
	tool, err := s.consume("session", "child", false)
	require.Error(t, err)
	assert.Empty(t, tool, "cannot redirect unless evidence removal was committed")
	after, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NoError(t, os.Chmod(s.dir, 0o700))
	tool, err = s.consume("session", "child", false)
	require.NoError(t, err)
	assert.Equal(t, observedExecute, tool, "prior state survived the failed write")
}

func TestObservationsConcurrentUpdatesAndConsume(t *testing.T) {
	s, now := observationFixture(t)
	require.NoError(t, s.record("session", "seed", observedExecute))
	const workers = 8
	results := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- s.record("session", fmt.Sprint(i), observedExecute)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 1
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, errObservationLockTimeout)
		}
	}
	state, err := readObservations(observationFile(s), now.UnixMilli())
	require.NoError(t, err)
	assert.Len(t, state.Entries, successes, "every committed update must survive")

	tools := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tool, err := s.consume("session", "seed", false)
			if err != nil && !errors.Is(err, errObservationLockTimeout) {
				t.Errorf("consume: %v", err)
			}
			tools <- tool
		}()
	}
	wg.Wait()
	close(tools)
	consumed := 0
	for tool := range tools {
		if tool != "" {
			consumed++
		}
	}
	assert.Equal(t, 1, consumed)
}

func TestObservationsCrossProcessLockTimeout(t *testing.T) {
	s, _ := observationFixture(t)
	require.NoError(t, s.record("session", "child", observedExecute))
	lockPath := filepath.Join(s.dir, "tool-observations.lock")
	before, err := os.Stat(lockPath)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestObservationLockHelper$")
	cmd.Env = append(os.Environ(), "CAPY_TEST_OBSERVATION_LOCK="+lockPath)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		cancel()
		if cmd.ProcessState == nil {
			cmd.Wait()
		}
	})
	ready := make([]byte, len("ready"))
	_, err = io.ReadFull(stdout, ready)
	require.NoError(t, err)
	require.Equal(t, "ready", string(ready))
	started := time.Now()
	tool, err := s.consume("session", "child", false)
	require.ErrorIs(t, err, errObservationLockTimeout)
	assert.Empty(t, tool)
	assert.GreaterOrEqual(t, time.Since(started), observationLockWait)
	assert.Less(t, time.Since(started), time.Second, "contention must not wait for holder exit")
	require.NoError(t, stdin.Close())
	require.NoError(t, cmd.Wait())
	tool, err = s.consume("session", "child", false)
	require.NoError(t, err)
	assert.Equal(t, observedExecute, tool)
	after, err := os.Stat(lockPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "consumption must not replace the lock inode")
}

func TestObservationLockHelper(t *testing.T) {
	path := os.Getenv("CAPY_TEST_OBSERVATION_LOCK")
	if path == "" {
		return
	}
	lock, err := lockObservations(path)
	require.NoError(t, err)
	defer lock.Close()
	_, err = os.Stdout.WriteString("ready")
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, os.Stdin)
	require.NoError(t, err)
}
