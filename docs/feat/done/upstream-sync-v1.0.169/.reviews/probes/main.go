// Review probe for the existing implementation, not the proposed sync.
// Run from the repository root with: go run -tags fts5 ./docs/feat/wip/upstream-sync-v1.0.169/.reviews/probes
// All database content and credentials are synthetic; the database is temporary.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	for _, tc := range []struct{ name, path, pattern, project string }{
		{"host absolute allow", "/tmp/capy-review-outside/note.txt", "//tmp/capy-review-outside/**", "/tmp/capy-review-project"},
		{"single-slash treated as absolute", "/tmp/capy-review-outside/note.txt", "/tmp/capy-review-outside/**", "/tmp/capy-review-project"},
		{"relative deny on direct input", "secrets/note.txt", "secrets/**", "/tmp/capy-review-project"},
		{"relative deny on walker input", "/tmp/capy-review-project/secrets/note.txt", "secrets/**", "/tmp/capy-review-project"},
	} {
		matched, _ := security.EvaluateFilePath(tc.path, [][]string{{tc.pattern}}, tc.project)
		fmt.Printf("permission: %s: matched=%t\n", tc.name, matched)
	}

	dir, err := os.MkdirTemp("", "capy-upstream-review-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st := store.NewContentStore(filepath.Join(dir, "probe.db"), dir, 2, store.DefaultMaxSourceBytes,
		store.WithEncryptionKey("synthetic-upstream-design-review-key-2026", "review probe"))
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			err = fmt.Errorf("closing probe store: %w", closeErr)
		}
	}()
	const label = "review-cache|https://example.invalid/docs"
	if _, err := st.IndexPlainText("unchanged synthetic documentation", label, store.KindEphemeral); err != nil {
		return err
	}
	before, err := st.GetSourceMeta(label)
	if err != nil || before == nil {
		return fmt.Errorf("reading initial metadata: %v", err)
	}
	// Cross a full SQLite timestamp unit, making renewal (or its absence) visible.
	time.Sleep(1100 * time.Millisecond)
	r, err := st.IndexPlainText("unchanged synthetic documentation", label, store.KindEphemeral)
	if err != nil {
		return err
	}
	after, err := st.GetSourceMeta(label)
	if err != nil || after == nil {
		return fmt.Errorf("reading refreshed metadata: %v", err)
	}
	fmt.Printf("cache: same_hash=%t indexed_at_advanced=%t immediately_fresh_at_1s=%t\n",
		r.AlreadyIndexed, after.IndexedAt.After(before.IndexedAt), time.Since(after.IndexedAt) < time.Second)
	fmt.Printf("cache: timestamp_nanoseconds=%d dedup_result_chunks=%d persisted_chunks=%d\n",
		after.IndexedAt.Nanosecond(), r.TotalChunks, after.ChunkCount)

	var content strings.Builder
	for i := 0; i < 8000; i++ {
		fmt.Fprintf(&content, "# section-%04d\n\nmarker\n\n", i)
	}
	batch, err := st.Index(content.String(), "batch:review-inventory", "markdown", store.KindEphemeral)
	if err != nil {
		return err
	}
	sections, err := st.GetChunksBySource(batch.SourceID)
	if err != nil {
		return err
	}
	inventory := len("## Indexed Sections\n\n")
	for _, section := range sections {
		inventory += len(fmt.Sprintf("- %s (%.1fKB)\n", section.Title, float64(len(section.Content))/1024))
	}
	fmt.Printf("batch: admitted_source_bytes=%d section_count=%d section_inventory_bytes=%d query_cap_bytes=%d\n",
		content.Len(), len(sections), inventory, 80*1024)
	return nil
}
