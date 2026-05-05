package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
)

func TestManglePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "unix absolute path",
			path: "/home/sergio/Projects/personal/capy",
			want: "-home-sergio-Projects-personal-capy",
		},
		{
			name: "path with dots",
			path: "/home/sergio/.config/capy",
			want: "-home-sergio--config-capy",
		},
		{
			name: "root only",
			path: "/",
			want: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := manglePath(tt.path)
			if got != tt.want {
				t.Errorf("manglePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestSessionDir_NotExists(t *testing.T) {
	_, err := SessionDir("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestExtractUUIDFromLabel(t *testing.T) {
	tests := []struct {
		label string
		want  string
	}{
		{"session:2026-04-05T12:06:26Z:abc-123-def", "abc-123-def"},
		{"session:2026-04-05T12:06:26Z:simple", "simple"},
		{"durable:something", ""},
		{"session:onlytwoparts", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := extractUUIDFromLabel(tt.label)
		if got != tt.want {
			t.Errorf("extractUUIDFromLabel(%q) = %q, want %q", tt.label, got, tt.want)
		}
	}
}

func TestBuildLabel(t *testing.T) {
	s := &ParsedSession{
		SessionID: "102ad512-759a-43ad-8805-353ce341f65c",
		StartTime: time.Date(2026, 4, 5, 12, 6, 26, 0, time.UTC),
	}

	got := buildLabel(s)
	want := "session:2026-04-05T12:06:26Z:102ad512-759a-43ad-8805-353ce341f65c"
	if got != want {
		t.Errorf("buildLabel() = %q, want %q", got, want)
	}
}

func TestShouldSkip(t *testing.T) {
	tmp := t.TempDir()
	uuid := "test-session"

	// Create a JSONL file.
	jsonlPath := filepath.Join(tmp, uuid+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var jsonlEntry os.DirEntry
	for _, e := range entries {
		if e.Name() == uuid+".jsonl" {
			jsonlEntry = e
			break
		}
	}
	if jsonlEntry == nil {
		t.Fatal("could not find test JSONL entry")
	}

	t.Run("new file not in map", func(t *testing.T) {
		m := map[string]time.Time{}
		if shouldSkip(tmp, uuid, jsonlEntry, m) {
			t.Error("new file should not be skipped")
		}
	})

	t.Run("unchanged file", func(t *testing.T) {
		m := map[string]time.Time{
			uuid: time.Now().Add(time.Hour),
		}
		if !shouldSkip(tmp, uuid, jsonlEntry, m) {
			t.Error("unchanged file should be skipped")
		}
	})

	t.Run("modified file", func(t *testing.T) {
		m := map[string]time.Time{
			uuid: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		if shouldSkip(tmp, uuid, jsonlEntry, m) {
			t.Error("modified file should not be skipped")
		}
	})

	t.Run("subagent dir newer", func(t *testing.T) {
		info, _ := jsonlEntry.Info()
		m := map[string]time.Time{
			uuid: info.ModTime().Add(time.Minute),
		}
		// Without subagent dir — should skip (file older than indexed_at).
		if !shouldSkip(tmp, uuid, jsonlEntry, m) {
			t.Error("should skip when file is older and no subagent dir")
		}

		// Create a subagent dir with future mtime.
		subDir := filepath.Join(tmp, uuid, "subagents")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		futureTime := time.Now().Add(time.Hour)
		if err := os.Chtimes(subDir, futureTime, futureTime); err != nil {
			t.Fatal(err)
		}

		if shouldSkip(tmp, uuid, jsonlEntry, m) {
			t.Error("should not skip when subagent dir is newer")
		}
	})
}

// writeTestSession creates a minimal valid session JSONL file with enough
// content to pass the IsIndexable gate.
func writeTestSession(t *testing.T, dir, uuid string) string {
	t.Helper()

	jsonlPath := filepath.Join(dir, uuid+".jsonl")
	ts := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	var lines []string
	for i := range 3 {
		userContent, _ := json.Marshal(map[string]any{
			"type":      "user",
			"uuid":      "u" + string(rune('1'+i)),
			"timestamp": ts,
			"sessionId": uuid,
			"message": map[string]any{
				"id":      "um" + string(rune('1'+i)),
				"role":    "user",
				"content": strings.Repeat("This is a test question about session indexing. ", 5),
			},
		})
		lines = append(lines, string(userContent))

		assistContent, _ := json.Marshal(map[string]any{
			"type":      "assistant",
			"uuid":      "a" + string(rune('1'+i)),
			"timestamp": ts,
			"sessionId": uuid,
			"message": map[string]any{
				"id":   "am" + string(rune('1'+i)),
				"role": "assistant",
				"content": []map[string]any{
					{"type": "text", "text": strings.Repeat("This is a detailed assistant response about the topic. ", 5)},
				},
			},
		})
		lines = append(lines, string(assistContent))
	}

	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return jsonlPath
}

func TestSweep_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// We need a known project dir that maps to a temp session directory.
	// Since SessionDir uses os.UserHomeDir, we'll test Sweep by constructing
	// a scenario where the session dir exists at the expected path.
	//
	// Instead, we test the indexSession function directly which doesn't
	// depend on home directory layout.
	tmp := t.TempDir()
	uuid1 := "session-aaa-111"
	uuid2 := "session-bbb-222"
	uuid3 := "session-trivial"

	writeTestSession(t, tmp, uuid1)
	writeTestSession(t, tmp, uuid2)

	// Write a trivial session that should be gated out.
	trivialPath := filepath.Join(tmp, uuid3+".jsonl")
	if err := os.WriteFile(trivialPath, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a test store.
	dbPath := filepath.Join(tmp, "test.db")
	t.Setenv("CAPY_ENCRYPTION_KEY", "test-key-for-sweep-integration-32b")

	cs := store.NewContentStore(dbPath, tmp, 2.0)
	defer cs.Close()

	ctx := context.Background()

	// Index session 1.
	err := indexSession(ctx, cs, tmp, uuid1)
	if err != nil {
		t.Fatalf("indexSession(uuid1) failed: %v", err)
	}

	// Index session 2.
	err = indexSession(ctx, cs, tmp, uuid2)
	if err != nil {
		t.Fatalf("indexSession(uuid2) failed: %v", err)
	}

	// Index trivial session — should be silently skipped (not an error).
	err = indexSession(ctx, cs, tmp, uuid3)
	if err != nil {
		t.Fatalf("indexSession(trivial) failed: %v", err)
	}

	// Verify indexed sessions are searchable.
	results, err := cs.SearchWithFallback("test question session indexing", 10, store.SearchOptions{
		IncludeKinds: []store.SourceKind{store.KindSession},
	})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results for indexed sessions")
	}

	// Verify labels follow expected format.
	for _, r := range results {
		if !strings.HasPrefix(r.Label, "session:") {
			t.Errorf("unexpected label format: %s", r.Label)
		}
	}

	// Verify the indexed-at map works for mtime gating.
	m, err := buildIndexedAtMap(cs)
	if err != nil {
		t.Fatalf("buildIndexedAtMap failed: %v", err)
	}
	if _, ok := m[uuid1]; !ok {
		t.Errorf("uuid1 not in indexed map")
	}
	if _, ok := m[uuid2]; !ok {
		t.Errorf("uuid2 not in indexed map")
	}
	if _, ok := m[uuid3]; ok {
		t.Error("trivial session should not be in indexed map")
	}
}

func TestSweep_ContextCancellation(t *testing.T) {
	tmp := t.TempDir()

	// Create multiple session files.
	for i := range 5 {
		writeTestSession(t, tmp, "session-cancel-"+string(rune('a'+i)))
	}

	dbPath := filepath.Join(tmp, "test.db")
	t.Setenv("CAPY_ENCRYPTION_KEY", "test-key-for-sweep-cancel-test-32")

	cs := store.NewContentStore(dbPath, tmp, 2.0)
	defer cs.Close()

	// Cancel immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// indexSession should bail out on cancelled context.
	err := indexSession(ctx, cs, tmp, "session-cancel-a")
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestSweep_NonTrivialZeroTurns(t *testing.T) {
	// Test the format-degradation detection: a >1KB file that produces 0 turns.
	tmp := t.TempDir()
	uuid := "session-degraded"
	jsonlPath := filepath.Join(tmp, uuid+".jsonl")

	// Write a >1KB file of unrecognized types.
	var lines []string
	for range 50 {
		line, _ := json.Marshal(map[string]any{
			"type":    "unknown-future-type",
			"content": strings.Repeat("x", 100),
		})
		lines = append(lines, string(line))
	}
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tmp, "test.db")
	t.Setenv("CAPY_ENCRYPTION_KEY", "test-key-for-degradation-test-32")

	cs := store.NewContentStore(dbPath, tmp, 2.0)
	defer cs.Close()

	// Should not error — just silently skip the non-indexable session.
	err := indexSession(context.Background(), cs, tmp, uuid)
	if err != nil {
		t.Fatalf("expected no error for non-indexable session, got: %v", err)
	}

	// Verify nothing was indexed.
	m, err := buildIndexedAtMap(cs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m[uuid]; ok {
		t.Error("degraded session should not be indexed")
	}
}

func TestSessionDir_WithRealHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping unix-specific path test on windows")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	// Test that SessionDir correctly constructs the path even if
	// the directory doesn't exist.
	_, err = SessionDir("/tmp/nonexistent-project-for-test")
	if err == nil {
		// This is fine if the .claude directory happens to exist.
		// The point is it didn't panic.
		return
	}

	// Verify the error mentions the expected path pattern.
	expectedMangled := "-tmp-nonexistent-project-for-test"
	expectedDir := filepath.Join(home, ".claude", "projects", expectedMangled)
	if !strings.Contains(err.Error(), expectedDir) && !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
