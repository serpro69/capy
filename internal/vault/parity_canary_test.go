package vault

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/stretchr/testify/require"
)

// parityBaselineEnv names the file holding the real-corpus digest baseline.
// Unset → the canary skips. The baseline is machine-local (it lists session
// paths under the Claude projects dir) and must not be committed; bench-results/
// is gitignored and durable, so `$REPO/bench-results/vault-parity-baseline.tsv`
// is the conventional location. Use an ABSOLUTE path: `go test` runs the test
// with the package directory as cwd, so a relative path lands under
// internal/vault/.
const parityBaselineEnv = "CAPY_VAULT_PARITY_BASELINE"

// parityEntry is one baseline row: a digest of the reader INPUT (raw bytes plus
// sidecar ids and bytes) and a digest of the four readers' OUTPUT. Recording the
// input lets the compare run tell "the session grew since the baseline" (a live
// session, appended between runs) from "the readers changed" — only the latter
// is a parity failure.
type parityEntry struct {
	input  string
	output string
}

// TestParityCanary is the real-corpus half of the byte-identical gate for the
// transcript-model refactor (docs/feat/done/codex-vault-sessions/, Slices 3–4).
// It walks every session JSONL and subagent sidecar under
// config.ClaudeProjectsDir(), runs the four public readers over each file via
// readerOutputs (golden_test.go) and digests the concatenated outputs.
//
// Run once on `master` to produce the baseline, then on the branch to compare:
//
//	CAPY_VAULT_PARITY_BASELINE=$PWD/bench-results/vault-parity-baseline.tsv \
//	  go test -tags fts5 -run TestParityCanary ./internal/vault/
//
// The first run writes the baseline and passes. Later runs fail on any file
// whose input is byte-identical to the baseline but whose reader output differs.
// Files whose input changed (a session appended since the baseline), files only
// in the baseline (deleted sessions) and files only on disk (created since) are
// counted and logged, not failed — the corpus grows while you work, and the gate
// is about reader behaviour, not census. A baseline taken while a session is
// live therefore still compares cleanly; only that session is excluded.
func TestParityCanary(t *testing.T) {
	baselinePath := os.Getenv(parityBaselineEnv)
	if baselinePath == "" {
		t.Skipf("%s not set", parityBaselineEnv)
	}
	root, err := config.ClaudeProjectsDir()
	require.NoError(t, err)
	sessions, err := DiscoverSessions(context.Background(), root)
	switch {
	case err == nil && len(sessions) == 0,
		errors.Is(err, fs.ErrNotExist),
		// detectProjectDirs reports an existing-but-empty projects root as an
		// error rather than an empty slice; that is "no corpus", not a failure.
		err != nil && strings.Contains(err.Error(), "no session files found"):
		t.Skipf("no Claude sessions under %s (%v)", root, err)
	case err != nil:
		// Any other discovery error (permissions, a broken walk) is a real
		// regression in code later slices touch — surface it, don't skip.
		t.Fatalf("discovering Claude sessions under %s: %v", root, err)
	}

	// The scanner warns on every malformed line it meets; a real corpus has
	// plenty. Silence slog for the walk so the run output is the verdict only.
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	// A live corpus can lose files between discovery and read (session cleanup
	// while the canary walks). That is not a reader regression: such files are
	// counted as vanished and skipped, mirroring how changed inputs are treated.
	vanished := 0
	readOrSkip := func(path string) ([]byte, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				vanished++
				return nil, false
			}
			t.Fatalf("reading %s: %v", path, err)
		}
		return b, true
	}

	current := map[string]parityEntry{}
	for _, sf := range sessions {
		raw, ok := readOrSkip(sf.Path)
		if !ok {
			continue
		}

		sidecars := map[string][]byte{}
		for _, f := range sf.AssociatedFiles {
			id := subagentID(f.RelativePath)
			if id == "" {
				continue
			}
			sraw, ok := readOrSkip(f.AbsPath)
			if !ok {
				continue
			}
			sidecars[id] = sraw
			// Each sidecar is also digested standalone through the same four
			// readers (ScanSession, not ScanSubagent — the two differ only by the
			// SubagentID stamp, which the sidecar-aware main digest below pins), as
			// RenderText / ParseTranscript(raw, nil) see it when opened on its own.
			current[relTo(t, root, f.AbsPath)] = parityDigest(t, sraw, nil)
		}
		// The main file's digest includes the sidecar-aware outputs (Openable
		// launch markers depend on the sidecar id list; ScanSubagent per sidecar).
		current[relTo(t, root, sf.Path)] = parityDigest(t, raw, sidecars)
	}
	if vanished > 0 {
		t.Logf("parity canary: %d file(s) vanished between discovery and read; skipped", vanished)
	}

	baseline, err := readParityBaseline(baselinePath)
	if os.IsNotExist(err) {
		require.NoError(t, writeParityBaseline(baselinePath, current))
		t.Logf("wrote parity baseline for %d files to %s", len(current), baselinePath)
		return
	}
	require.NoError(t, err)

	var mismatched []string
	added, removed, inputChanged := 0, 0, 0
	for path, got := range current {
		want, ok := baseline[path]
		switch {
		case !ok:
			added++
		case want.input != got.input:
			inputChanged++
		case want.output != got.output:
			mismatched = append(mismatched, path)
		}
	}
	for path := range baseline {
		if _, ok := current[path]; !ok {
			removed++
		}
	}
	sort.Strings(mismatched)
	t.Logf("parity canary: %d files compared, %d skipped (input changed since baseline), %d new since baseline, %d gone since baseline",
		len(current)-added-inputChanged, inputChanged, added, removed)
	if len(mismatched) > 0 {
		const show = 50
		listed := mismatched
		if len(listed) > show {
			listed = listed[:show]
		}
		t.Fatalf("%d file(s) produce different reader output than the baseline (first %d):\n  %s",
			len(mismatched), len(listed), strings.Join(listed, "\n  "))
	}
}

// parityDigest returns the input and output digests for a session (with its
// sidecars) or a standalone sidecar (nil sidecars). The input digest covers the
// raw bytes plus every sidecar id and its bytes, in id order; the output digest
// covers the four readers' serialised outputs separated by the reader name so
// two readers cannot compensate for each other's change.
func parityDigest(t *testing.T, raw []byte, sidecars map[string][]byte) parityEntry {
	t.Helper()
	in := sha256.New()
	in.Write(raw)
	for _, id := range sortedKeys(sidecars) {
		fmt.Fprintf(in, "\n--- sidecar %s ---\n", id)
		in.Write(sidecars[id])
	}

	outputs := readerOutputs(t, raw, sidecars)
	out := sha256.New()
	for _, reader := range goldenReaders {
		fmt.Fprintf(out, "--- %s ---\n", reader)
		out.Write(outputs[reader])
	}
	return parityEntry{
		input:  hex.EncodeToString(in.Sum(nil)),
		output: hex.EncodeToString(out.Sum(nil)),
	}
}

// relTo returns path relative to root with slash separators, so a baseline is
// portable across CLAUDE_CONFIG_DIR values on the same corpus.
func relTo(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	require.NoError(t, err)
	return filepath.ToSlash(rel)
}

// readParityBaseline parses `path\tinput\toutput` lines. A missing file returns
// an os.IsNotExist error so the caller can write the first baseline.
func readParityBaseline(path string) (map[string]parityEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]parityEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for ln := 1; sc.Scan(); ln++ {
		line := sc.Text()
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("parity baseline %s: line %d is not `path<TAB>input<TAB>output`", path, ln)
		}
		out[fields[0]] = parityEntry{input: fields[1], output: fields[2]}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading parity baseline: %w", err)
	}
	return out, nil
}

// writeParityBaseline writes sorted `path\tinput\toutput` lines, creating
// parents.
func writeParityBaseline(path string, digests map[string]parityEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating baseline dir: %w", err)
	}
	var sb strings.Builder
	for _, p := range sortedKeys(digests) {
		sb.WriteString(p)
		sb.WriteByte('\t')
		sb.WriteString(digests[p].input)
		sb.WriteByte('\t')
		sb.WriteString(digests[p].output)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writing parity baseline: %w", err)
	}
	return nil
}
