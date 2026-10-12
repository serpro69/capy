package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
)

func TestFetchFreshnessStdioReopen(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	const key = "synthetic-fetch-freshness-stdio-key-12345"
	const url = "https://example.com/validation"
	const label = "freshness|" + url
	s := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "test"))
	validated := time.Now().Truncate(time.Millisecond)
	if _, err := s.IndexFetched("quokka validation marker", label, "plaintext", store.KindDurable, validated); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key}})
		p.initialize(t)
		for _, args := range []map[string]any{
			{"url": url, "source": "freshness", "kind": "durable"},
			{"requests": []any{map[string]any{"url": url, "source": "freshness"}}, "kind": "durable"},
		} {
			text := p.tool(t, "capy_fetch_and_index", args)
			if !strings.Contains(strings.ToLower(text), "cache hit") {
				t.Fatalf("expected persisted cache hit: %s", text)
			}
		}
		p.close(t)
		stdioCheckpointed(t, project)
		meta, err := s.GetSourceMeta(label)
		if err != nil {
			t.Fatal(err)
		}
		if meta == nil || meta.FetchValidatedAtMS == nil || *meta.FetchValidatedAtMS != validated.UnixMilli() {
			t.Fatalf("cache hit changed validation: %+v", meta)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Supply a binary built from the pre-021 commit to exercise actual reader/writer
// coexistence. Ordinary tests keep the encrypted legacy-schema fixtures; they do
// not require git history, a network checkout, or another compiler invocation.
func TestFetchFreshnessOldBinaryCompatibility(t *testing.T) {
	bin := os.Getenv("CAPY_TEST_OLD_BINARY")
	if bin == "" {
		t.Skip("set CAPY_TEST_OLD_BINARY to a pre-021 capy binary")
	}
	project := stdioProject(t)
	const key = "synthetic-fetch-old-reader-key-12345"
	const label = "compatibility|https://example.com/reference"
	oldCommand := stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: t.TempDir(), env: stdioEnv(t, key), secrets: []string{key}}
	// Create a real pre-migration encrypted database before the new store opens.
	old := startStdioProcess(t, oldCommand)
	old.initialize(t)
	old.tool(t, "capy_index", map[string]any{"content": "quokka compatibility marker", "source": label})
	old.close(t)
	stdioCheckpointed(t, project)
	s := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, 0, store.WithEncryptionKey(key, "test"))
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.IndexFetched("quokka compatibility marker", label, "plaintext", store.KindDurable, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Keep the new connection open while the old binary reads and writes.
	p := startStdioProcess(t, oldCommand)
	p.initialize(t)
	text := p.tool(t, "capy_search", map[string]any{"queries": []any{"quokka compatibility"}, "source": label})
	if !strings.Contains(text, "quokka") {
		t.Fatalf("old reader missed new content: %s", text)
	}
	p.tool(t, "capy_index", map[string]any{"content": "quokka compatibility marker", "source": label})
	meta, err := s.GetSourceMeta(label)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.FetchValidatedAtMS == nil {
		t.Fatal("old dedup lost unchanged metadata")
	}
	p.tool(t, "capy_index", map[string]any{"content": "wombat replacement marker", "source": label})
	meta, err = s.GetSourceMeta(label)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.FetchValidatedAtMS != nil {
		t.Fatal("old replacement must leave validation NULL")
	}
	p.close(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IndexFetched("wombat replacement marker", label, "plaintext", store.KindDurable, time.Now()); err != nil {
		t.Fatal(err)
	}
	meta, err = s.GetSourceMeta(label)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.FetchValidatedAtMS == nil {
		t.Fatal("new reader failed to revalidate after old replacement")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh old process can still read the database after the new writer closes.
	// Use MCP so this also covers releases from before the knowledge search CLI.
	old = startStdioProcess(t, oldCommand)
	old.initialize(t)
	text = old.tool(t, "capy_search", map[string]any{"queries": []any{"wombat"}, "source": label})
	if !strings.Contains(text, "wombat") {
		t.Fatalf("old reader after reopen missed replacement: %s", text)
	}
	old.close(t)
	stdioCheckpointed(t, project)
}
