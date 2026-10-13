package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/require"
)

func mainObservationFixture(t *testing.T) (observationStore, *time.Time) {
	t.Helper()
	s, now := observationFixture(t)
	s.scope = codexMainObservationScope
	return s, now
}

func TestCodexMainObservationStore(t *testing.T) {
	t.Run("admission and absent state", func(t *testing.T) {
		s, _ := mainObservationFixture(t)
		for _, input := range []struct{ session, agent, tool string }{
			{"", "", observedExecute}, {"raw-id", "", observedExecute}, {"codex:", "", observedExecute},
			{"codex:main", "child", observedExecute}, {"codex:main", "", observedFetch},
		} {
			require.NoError(t, s.record(input.session, input.agent, input.tool))
		}
		tool, err := s.consume("codex:main", "", false)
		require.NoError(t, err)
		require.Empty(t, tool)
		require.NoError(t, s.removeSession("codex:main"))
		require.NoDirExists(t, s.dir)
	})
	t.Run("one timestamp and strict expiry", func(t *testing.T) {
		s, now := mainObservationFixture(t)
		require.NoError(t, s.record("codex:main", "", observedExecute))
		*now = now.Add(time.Second)
		require.NoError(t, s.record("codex:main", "", observedExecute))
		state, err := readObservations(observationFile(s), now.UnixMilli(), s.scope)
		require.NoError(t, err)
		require.Len(t, state.Entries, 1)
		require.Empty(t, state.Entries[0].Agent)
		require.Equal(t, map[string]int64{observedExecute: now.UnixMilli()}, state.Entries[0].Tools)
		*now = now.Add(observationTTL - time.Millisecond)
		tool, err := s.consume("codex:main", "", false)
		require.NoError(t, err)
		require.Equal(t, observedExecute, tool)
		tool, err = s.consume("codex:main", "", false)
		require.NoError(t, err)
		require.Empty(t, tool, "successes replace timestamps, never accumulate credits")
		require.NoError(t, s.record("codex:main", "", observedExecute))
		*now = now.Add(observationTTL)
		tool, err = s.consume("codex:main", "", false)
		require.NoError(t, err)
		require.Empty(t, tool, "exact 60-second expiry")
	})
	t.Run("independent caps and permanent locks", func(t *testing.T) {
		main, now := mainObservationFixture(t)
		child := newObservationStore(filepath.Dir(main.dir))
		child.now = main.now
		for i := range maxObservationEntries + 2 {
			*now = now.Add(time.Millisecond)
			require.NoError(t, main.record(fmt.Sprintf("codex:%d", i), "", observedExecute))
			require.NoError(t, child.record(fmt.Sprintf("codex:%d", i), "child", observedFetch))
		}
		for _, s := range []observationStore{main, child} {
			data, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			require.LessOrEqual(t, len(data), maxObservationBytes)
			state, err := readObservations(observationFile(s), now.UnixMilli(), s.scope)
			require.NoError(t, err)
			require.Len(t, state.Entries, maxObservationEntries)
			require.Equal(t, sessionIDComponent("codex:2"), state.Entries[0].Session)
		}
		mainLock := filepath.Join(main.dir, "codex-main-observations.lock")
		childLock := filepath.Join(main.dir, "tool-observations.lock")
		before, err := os.Stat(mainLock)
		require.NoError(t, err)
		lock, err := lockObservations(mainLock)
		require.NoError(t, err)
		defer lock.Close()
		require.NoError(t, child.record("codex:other", "child", observedExecute), "main lock cannot block child writes")
		require.NoError(t, lock.Close())
		lock, err = lockObservations(childLock)
		require.NoError(t, err)
		defer lock.Close()
		require.NoError(t, main.record("codex:other", "", observedExecute), "child lock cannot block main writes")
		require.NoError(t, main.removeSession("codex:other"))
		after, err := os.Stat(mainLock)
		require.NoError(t, err)
		require.True(t, os.SameFile(before, after))
		files, err := os.ReadDir(main.dir)
		require.NoError(t, err)
		require.Len(t, files, 4)
	})
	for _, invalid := range []string{"malformed", "null", "missing entries", "unknown field", "trailing", "oversized", "future", "zero", "negative", "child agent", "fetch", "other tool", "empty tools", "unsafe session", "duplicate", "too many"} {
		t.Run(invalid, func(t *testing.T) {
			s, now := mainObservationFixture(t)
			require.NoError(t, s.record("codex:main", "", observedExecute))
			state := observationState{Entries: []toolObservation{{Session: sessionIDComponent("codex:main"), Tools: map[string]int64{observedExecute: now.UnixMilli()}}}}
			switch invalid {
			case "future":
				state.Entries[0].Tools[observedExecute]++
			case "zero":
				state.Entries[0].Tools[observedExecute] = 0
			case "negative":
				state.Entries[0].Tools[observedExecute] = -1
			case "child agent":
				state.Entries[0].Agent = "child"
			case "fetch":
				state.Entries[0].Tools[observedFetch] = now.UnixMilli()
			case "other tool":
				state.Entries[0].Tools = map[string]int64{"capy_search": now.UnixMilli()}
			case "empty tools":
				state.Entries[0].Tools = nil
			case "unsafe session":
				state.Entries[0].Session = "../main"
			case "duplicate":
				state.Entries = append(state.Entries, state.Entries[0])
			case "too many":
				for i := range maxObservationEntries {
					state.Entries = append(state.Entries, toolObservation{Session: fmt.Sprint(i), Tools: map[string]int64{observedExecute: now.UnixMilli()}})
				}
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			switch invalid {
			case "malformed":
				data = []byte(`{broken`)
			case "null":
				data = []byte(`null`)
			case "missing entries":
				data = []byte(`{}`)
			case "unknown field":
				data = []byte(`{"entries":[],"unknown":true}`)
			case "trailing":
				data = append(data, []byte(`{}`)...)
			case "oversized":
				data = append(data, []byte(strings.Repeat(" ", maxObservationBytes))...)
			}
			require.NoError(t, os.WriteFile(observationFile(s), data, 0o600))
			tool, err := s.consume("codex:main", "", false)
			require.Error(t, err)
			require.Empty(t, tool)
			require.Error(t, s.record("codex:main", "", observedExecute))
			after, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			require.Equal(t, data, after)
		})
	}
	for _, target := range []string{"codex-main-observations.json", "codex-main-observations.lock"} {
		for _, kind := range []string{"symlink", "fifo", "directory"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				s, _ := mainObservationFixture(t)
				require.NoError(t, os.Mkdir(s.dir, 0o700))
				path := filepath.Join(s.dir, target)
				switch kind {
				case "symlink":
					require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), path))
				case "fifo":
					require.NoError(t, syscall.Mkfifo(path, 0o600))
				case "directory":
					require.NoError(t, os.Mkdir(path, 0o700))
				}
				require.Error(t, s.record("codex:main", "", observedExecute))
			})
		}
	}
}

func codexObservationHook(t *testing.T, project, event, tool string, input, metadata map[string]any) map[string]any {
	t.Helper()
	output, err := handleEvent(event, hookInput(t, tool, input, metadata), &adapter.CodexAdapter{}, &project)
	require.NoError(t, err)
	if output == nil {
		return nil
	}
	return ccParse(t, output)
}

func TestCodexMainObservationPayloads(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "")
	for _, tc := range []struct {
		name, tool       string
		metadata         map[string]any
		claude, eligible bool
	}{
		{name: "root", eligible: true},
		{name: "plain tool", tool: observedExecute, eligible: true},
		{name: "slash alias", tool: "other/capy_execute", eligible: true},
		{name: "type only", metadata: map[string]any{"agent_type": "worker"}, eligible: true},
		{name: "serialized success", metadata: map[string]any{"tool_response": `{"isError":false,"content":[{"text":"error is discussed here"}]}`}, eligible: true},
		{name: "array success", metadata: map[string]any{"tool_response": []any{map[string]any{"isError": false}}}, eligible: true},
		{name: "transcript differs", metadata: map[string]any{"transcript_path": "/rollout-a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"}, eligible: true},
		{name: "child", metadata: map[string]any{"agent_id": "child"}},
		{name: "missing session", metadata: map[string]any{"session_id": "", "transcript_path": "/rollout-a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"}},
		{name: "Claude root even matching namespace", metadata: map[string]any{"session_id": "codex:context-session"}, claude: true},
		{name: "fetch", tool: "mcp__alias__capy_fetch_and_index"},
		{name: "batch", tool: "mcp__alias__capy_batch_execute"},
		{name: "execute file", tool: "capy_execute_file"},
		{name: "other", tool: "capy_stats"},
		{name: "lookalike", tool: "notcapy_execute"},
		{name: "MCP failure", metadata: map[string]any{"tool_response": map[string]any{"isError": true}}},
		{name: "ambiguous", metadata: map[string]any{"tool_response": map[string]any{"isError": "false"}}},
		{name: "missing response", metadata: map[string]any{"tool_response": nil}},
		{name: "malformed serialized", metadata: map[string]any{"tool_response": `{"isError":false`}},
		{name: "serialized failure", metadata: map[string]any{"tool_response": `{"isError":true}`}},
		{name: "array failure", metadata: map[string]any{"tool_response": []any{map[string]any{"is_error": true}}}},
		{name: "canceled", metadata: map[string]any{"canceled": true}},
		{name: "cancelled result", metadata: map[string]any{"tool_response": map[string]any{"cancelled": true}}},
		{name: "interrupted", metadata: map[string]any{"is_interrupt": true}},
		{name: "unsuccessful", metadata: map[string]any{"success": false}},
		{name: "failure event", metadata: map[string]any{"hook_event_name": "PostToolUseFailure"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			metadata := map[string]any{"hook_event_name": "PostToolUse", "tool_response": map[string]any{"isError": false, "content": []any{}}}
			for k, v := range tc.metadata {
				metadata[k] = v
			}
			tool := tc.tool
			if tool == "" {
				tool = "mcp__arbitrary_alias__capy_execute"
			}
			a := adapter.HookAdapter(&adapter.CodexAdapter{})
			if tc.claude {
				a = ccAdapter()
			}
			invoke := func() {
				out, err := handleEvent("posttooluse", hookInput(t, tool, nil, metadata), a, &project)
				require.NoError(t, err)
				require.Empty(t, out)
			}
			invoke()
			s := newCodexMainObservationStore(project)
			if tc.eligible {
				state, err := readObservations(observationFile(s), time.Now().UnixMilli(), s.scope)
				require.NoError(t, err)
				require.Len(t, state.Entries, 1)
				require.Equal(t, sessionIDComponent("codex:context-session"), state.Entries[0].Session)
				require.Empty(t, state.Entries[0].Agent)
				require.Len(t, state.Entries[0].Tools, 1)
				require.Positive(t, state.Entries[0].Tools[observedExecute])
				require.NoFileExists(t, filepath.Join(s.dir, "tool-observations.json"))
				return
			}
			require.NoFileExists(t, observationFile(s))
			if tc.name != "child" {
				require.NoDirExists(t, s.dir)
			}
			s.now = func() time.Time { return time.Now().Add(-30 * time.Second) }
			require.NoError(t, s.record("codex:context-session", "", observedExecute))
			before, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			invoke()
			after, err := os.ReadFile(observationFile(s))
			require.NoError(t, err)
			require.Equal(t, before, after, "ineligible calls cannot renew or erase prior success")
		})
	}
}
