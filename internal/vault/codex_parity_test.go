package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codexParityBaselineEnv = "CAPY_CODEX_PARITY_BASELINE"

var codexCompatibilityReaders = []string{"scan.json", "text.txt", "markdown.md"}

// Marshal the actual ScanOutput, not goldenScan's Claude-only projection: every
// field (including platform/parent metadata and ToolNames) belongs to this gate.
// JSON struct field order and time.Time's JSON encoding are deterministic. Never
// sanitize, truncate, sort rows, or include the intentionally changing viewer.
func codexCompatibilityOutputs(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	scan, err := ScanSession(PlatformCodex, bytes.NewReader(raw))
	require.NoError(t, err)
	return map[string][]byte{
		"scan.json":   marshalGolden(t, scan),
		"text.txt":    []byte(RenderText(PlatformCodex, raw)),
		"markdown.md": []byte(RenderMarkdown(PlatformCodex, raw)),
	}
}

func codexParityHash(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func codexOutputDigest(outputs map[string][]byte) string {
	// Fixed names plus fixed-size hex hashes provide unambiguous framing even
	// when bodies contain reader names, separators, or another reader's output.
	var framed strings.Builder
	for _, name := range codexCompatibilityReaders {
		fmt.Fprintf(&framed, "%s\t%s\n", name, codexParityHash(outputs[name]))
	}
	return codexParityHash([]byte(framed.String()))
}

type codexParityInput struct {
	Size       int      `json:"size"`
	Categories []string `json:"categories"`
}

// The companion manifest preserves capture-time category coverage and lengths
// (to distinguish a true append by hashing the original-length prefix). It is
// bound to the TSV digest so a stale/missing companion cannot bless a baseline.
// Both files contain private root-relative paths and stay in bench-results/.
type codexParityManifest struct {
	Version        int                         `json:"version"`
	BaselineSHA256 string                      `json:"baseline_sha256"`
	Inputs         map[string]codexParityInput `json:"inputs"`
}

type codexParityRecording struct {
	digest   parityEntry
	input    codexParityInput
	appended bool
}

type codexParityCounts struct {
	Compared, Appended, Changed, Added, Removed, Vanished, Mismatched int
}

// TestCodexParityCanary captures once, then compares without rewriting either
// baseline file. Opt in with an ABSOLUTE path under ignored bench-results/:
//
//	CAPY_CODEX_PARITY_BASELINE=$PWD/bench-results/codex-vault-viewer-parity.tsv \
//	  go test -tags fts5 -count=1 -run '^TestCodexParityCanary$' -v ./internal/vault
//
// Raw recordings are read once per run, decompressed if necessary, and never
// copied or logged. Main/child sessions share the same discovery rules. Only
// aggregate counts escape the machine-local baseline and coverage manifest.
func TestCodexParityCanary(t *testing.T) {
	baselinePath := os.Getenv(codexParityBaselineEnv)
	if baselinePath == "" {
		t.Skipf("%s not set", codexParityBaselineEnv)
	}
	benchDir, err := filepath.Abs("../../bench-results")
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(baselinePath), "baseline path must be absolute")
	rel, err := filepath.Rel(benchDir, baselinePath)
	require.NoError(t, err)
	require.True(t, rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)), "baseline must be under ignored bench-results/")

	baseline, manifest, err := readCodexParityBaseline(baselinePath)
	require.NoError(t, err)
	home := codexCanaryHome()
	if home == "" {
		t.Skip("no Codex home directory")
	}
	files, err := discoverCodexParityFiles(home)
	require.NoError(t, err)
	if len(files) == 0 && baseline == nil {
		t.Skip("no local Codex corpus; synthetic compatibility tests still run")
	}
	// Decoder warnings can contain local identifiers. Shape/drift diagnostics
	// belong to TestCodexCanary; this gate reports only hashes and aggregate counts.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(previous) })

	current := map[string]codexParityRecording{}
	vanished := map[string]bool{}
	compressed, reverts := 0, 0
	for _, file := range files {
		if file.revert {
			reverts++
			continue
		}
		logical := strings.TrimSuffix(relTo(t, home, file.path), ".zst")
		_, duplicate := current[logical]
		require.False(t, duplicate || vanished[logical], "duplicate logical rollout (raw and compressed copies)")
		raw, err := os.ReadFile(file.path)
		if errors.Is(err, fs.ErrNotExist) {
			vanished[logical] = true
			continue
		}
		require.NoError(t, codexParityIOError("reading rollout", err))
		if file.compressed {
			compressed++
			raw, err = decodeBlob(encodingZstd, raw)
			require.NoError(t, err, "decompressing Codex rollout")
		}
		current[logical] = codexParityRecord(t, raw, baseline[logical], manifest.Inputs[logical])
	}
	coverage := map[string]int{}
	for _, record := range current {
		for _, category := range record.input.Categories {
			coverage[category]++
		}
	}
	t.Logf("corpus: recordings=%d compressed=%d reverts=%d vanished=%d categories=%v", len(current), compressed, reverts, len(vanished), coverage)
	if baseline == nil {
		require.NotEmpty(t, current, "cannot capture an empty baseline")
		require.Zero(t, len(vanished), "retry capture after the corpus settles; no partial baseline")
		require.NoError(t, writeCodexParityBaseline(baselinePath, current))
		t.Logf("captured %d recordings; re-run to verify unchanged inputs", len(current))
		return
	}
	counts, comparedCoverage, err := compareCodexParity(baseline, manifest, current, vanished)
	t.Logf("parity: %+v; unchanged category coverage=%v", counts, comparedCoverage)
	require.NoError(t, err)
}

// Unlike the shape canary's root probe, permission errors must fail this gate.
func discoverCodexParityFiles(home string) ([]codexCanaryFile, error) {
	var files []codexCanaryFile
	for _, sub := range codexRolloutRoots {
		root := filepath.Join(home, sub)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			name, ok := parseRolloutFilename(d.Name())
			if ok {
				files = append(files, codexCanaryFile{path: path, uuid: name.UUID, compressed: name.Compressed, revert: name.RolloutID != ""})
			}
			return nil
		})
		if err != nil {
			return nil, codexParityIOError("discovering Codex parity corpus", err)
		}
	}
	return files, nil
}

// File errors carry the private rollout path. Preserve the underlying cause
// (including errors.Is semantics), but never print the path through testing.T.
func codexParityIOError(operation string, err error) error {
	if err == nil {
		return nil
	}
	for {
		var pathErr *fs.PathError
		if !errors.As(err, &pathErr) {
			break
		}
		err = pathErr.Err
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func codexParityRecord(t *testing.T, raw []byte, baseline parityEntry, input codexParityInput) codexParityRecording {
	t.Helper()
	return codexParityRecording{
		digest: parityEntry{input: codexParityHash(raw), output: codexOutputDigest(codexCompatibilityOutputs(t, raw))},
		input:  codexParityInput{Size: len(raw), Categories: codexParityCategories(raw)},
		appended: baseline.input != "" && len(raw) > input.Size && input.Size >= 0 &&
			codexParityHash(raw[:input.Size]) == baseline.input,
	}
}

func compareCodexParity(baseline map[string]parityEntry, manifest codexParityManifest, current map[string]codexParityRecording, vanished map[string]bool) (codexParityCounts, map[string]int, error) {
	counts := codexParityCounts{Vanished: len(vanished)}
	coverage := map[string]int{}
	required := map[string]bool{}
	for path, input := range manifest.Inputs {
		for _, category := range input.Categories {
			required[category] = true
		}
		if _, exists := current[path]; !exists && !vanished[path] {
			counts.Removed++
		}
	}
	for path, got := range current {
		want, exists := baseline[path]
		switch {
		case !exists:
			counts.Added++
		case want.input != got.digest.input:
			if got.appended {
				counts.Appended++
			} else {
				counts.Changed++
			}
		default:
			counts.Compared++
			if want.output != got.digest.output {
				counts.Mismatched++
			}
			for _, category := range manifest.Inputs[path].Categories {
				coverage[category]++
			}
		}
	}
	var failures []error
	if counts.Compared == 0 {
		failures = append(failures, errors.New("no unchanged recordings compared"))
	}
	if counts.Mismatched > 0 {
		failures = append(failures, fmt.Errorf("%d unchanged recordings have output mismatches; preserve the baseline and investigate", counts.Mismatched))
	}
	for _, category := range sortedKeys(required) {
		if coverage[category] == 0 {
			failures = append(failures, fmt.Errorf("no unchanged representative for captured category %s", category))
		}
	}
	return counts, coverage, errors.Join(failures...)
}

func writeCodexParityBaseline(path string, current map[string]codexParityRecording) error {
	entries := make(map[string]parityEntry, len(current))
	manifest := codexParityManifest{Version: 1, Inputs: make(map[string]codexParityInput, len(current))}
	for name, record := range current {
		entries[name] = record.digest
		manifest.Inputs[name] = record.input
	}
	if err := writeParityBaseline(path, entries); err != nil {
		return fmt.Errorf("writing Codex parity baseline: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading written baseline for manifest hash: %w", err)
	}
	manifest.BaselineSHA256 = codexParityHash(raw)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline manifest: %w", err)
	}
	if err := os.WriteFile(path+".json", append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing baseline manifest: %w", err)
	}
	return nil
}

func readCodexParityBaseline(path string) (map[string]parityEntry, codexParityManifest, error) {
	var manifest codexParityManifest
	baseline, err := readParityBaseline(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, metaErr := os.Stat(path + ".json"); !errors.Is(metaErr, fs.ErrNotExist) {
			return nil, manifest, errors.New("baseline missing but companion manifest exists or cannot be read; preserve both files and investigate")
		}
		return nil, manifest, nil
	}
	if err != nil {
		return nil, manifest, fmt.Errorf("reading Codex parity baseline: %w", err)
	}
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		return nil, manifest, fmt.Errorf("reading baseline companion (never regenerate a partial baseline): %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, manifest, fmt.Errorf("decoding baseline manifest: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, manifest, fmt.Errorf("reading baseline for hash verification: %w", err)
	}
	if manifest.Version != 1 || manifest.BaselineSHA256 != codexParityHash(raw) || len(manifest.Inputs) != len(baseline) || len(baseline) == 0 {
		return nil, manifest, errors.New("invalid or mismatched Codex parity baseline/manifest")
	}
	for name := range baseline {
		if input, exists := manifest.Inputs[name]; !exists || input.Size < 0 {
			return nil, manifest, errors.New("baseline manifest is missing a recording or has an invalid size")
		}
	}
	return baseline, manifest, nil
}

// Read category evidence from wire records, independently of the decoder being
// changed. This is coverage accounting, not another FileChange implementation.
// Malformed lines contribute no evidence; their bytes still enter input/output
// digests and the existing shape canary owns their diagnostics.
func codexParityCategories(raw []byte) []string {
	categories := map[string]bool{}
	patchCalls, patchResults, legacyEvents := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &envelope) != nil {
			continue
		}
		var payload struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			CallID string          `json:"call_id"`
			Item   json.RawMessage `json:"item"`
		}
		if json.Unmarshal(envelope.Payload, &payload) != nil {
			continue
		}
		if envelope.Type == "response_item" {
			if (payload.Type == "custom_tool_call" || payload.Type == "function_call") && payload.Name == "apply_patch" && payload.CallID != "" {
				patchCalls[payload.CallID] = true
			}
			if payload.Type == "custom_tool_call_output" || payload.Type == "function_call_output" {
				patchResults[payload.CallID] = true
			}
			continue
		}
		if envelope.Type != "event_msg" {
			continue
		}
		if payload.Type == "patch_apply_end" && payload.CallID != "" {
			legacyEvents[payload.CallID] = true
		}
		if payload.Type != "item_completed" {
			continue
		}
		var item struct {
			Type    string                     `json:"type"`
			Status  string                     `json:"status"`
			Changes map[string]json.RawMessage `json:"changes"`
		}
		if json.Unmarshal(payload.Item, &item) != nil || item.Type != "FileChange" {
			continue
		}
		switch item.Status {
		case "completed", "failed", "declined":
			categories["paginated-"+item.Status] = true
		}
		for _, change := range item.Changes {
			var update struct {
				Type     string `json:"type"`
				MovePath string `json:"move_path"`
			}
			if json.Unmarshal(change, &update) == nil && update.Type == "update" && update.MovePath != "" && item.Status == "completed" {
				categories["real-move"] = true
			}
		}
	}
	for id := range legacyEvents {
		if patchCalls[id] && patchResults[id] {
			categories["legacy-direct"] = true
		}
	}
	return sortedKeys(categories)
}

func TestCodexParity_Digests(t *testing.T) {
	for _, fixture := range codexEditCompatibilityCases(t) {
		t.Run(fixture.name, func(t *testing.T) {
			first := codexParityRecord(t, fixture.raw, parityEntry{}, codexParityInput{})
			assert.Equal(t, first, codexParityRecord(t, fixture.raw, parityEntry{}, codexParityInput{}))
			outputs := codexCompatibilityOutputs(t, fixture.raw)
			for _, reader := range codexCompatibilityReaders {
				t.Run(reader, func(t *testing.T) {
					changed := codexCompatibilityOutputs(t, fixture.raw)
					changed[reader] = append(changed[reader], ' ')
					assert.NotEqual(t, codexOutputDigest(outputs), codexOutputDigest(changed))
				})
			}
			var scan ScanOutput
			require.NoError(t, json.Unmarshal(outputs["scan.json"], &scan))
			actual, err := ScanSession(PlatformCodex, bytes.NewReader(fixture.raw))
			require.NoError(t, err)
			assert.Equal(t, actual, &scan, "every ScanOutput field survives serialization")
		})
	}
}

func TestCodexParity_Compare(t *testing.T) {
	raw := codexEditCompatibilityCases(t)[0].raw
	record := codexParityRecord(t, raw, parityEntry{}, codexParityInput{})
	baseline := map[string]parityEntry{"stable": record.digest}
	manifest := codexParityManifest{Inputs: map[string]codexParityInput{"stable": record.input}}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]codexParityRecording, map[string]bool)
		want   codexParityCounts
		err    string
	}{
		{name: "identical", want: codexParityCounts{Compared: 1}},
		{name: "output mismatch", mutate: func(m map[string]codexParityRecording, _ map[string]bool) {
			r := m["stable"]
			r.digest.output = "different"
			m["stable"] = r
		}, want: codexParityCounts{Compared: 1, Mismatched: 1}, err: "output mismatches"},
		{name: "appended input", mutate: func(m map[string]codexParityRecording, _ map[string]bool) {
			m["stable"] = codexParityRecord(t, append(bytes.Clone(raw), '\n'), record.digest, record.input)
		}, want: codexParityCounts{Appended: 1}, err: "no unchanged recordings"},
		{name: "rewritten input", mutate: func(m map[string]codexParityRecording, _ map[string]bool) {
			m["stable"] = codexParityRecord(t, append([]byte{'\n'}, raw...), record.digest, record.input)
		}, want: codexParityCounts{Changed: 1}, err: "no unchanged recordings"},
		{name: "new input", mutate: func(m map[string]codexParityRecording, _ map[string]bool) {
			m["new"] = record
		}, want: codexParityCounts{Compared: 1, Added: 1}},
		{name: "removed input", mutate: func(m map[string]codexParityRecording, _ map[string]bool) {
			delete(m, "stable")
		}, want: codexParityCounts{Removed: 1}, err: "no unchanged recordings"},
		{name: "vanished input", mutate: func(m map[string]codexParityRecording, v map[string]bool) {
			delete(m, "stable")
			v["stable"] = true
		}, want: codexParityCounts{Vanished: 1}, err: "no unchanged recordings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := map[string]codexParityRecording{"stable": record}
			vanished := map[string]bool{}
			if tc.mutate != nil {
				tc.mutate(current, vanished)
			}
			counts, _, err := compareCodexParity(baseline, manifest, current, vanished)
			assert.Equal(t, tc.want, counts)
			if tc.err == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.err)
			}
		})
	}
	// Comparing a different category cannot hide loss of the sole representative.
	baseline["lost"] = record.digest
	manifest.Inputs["lost"] = codexParityInput{Categories: []string{"paginated-declined"}}
	_, _, err := compareCodexParity(baseline, manifest, map[string]codexParityRecording{"stable": record}, nil)
	require.ErrorContains(t, err, "no unchanged representative for captured category paginated-declined")
}

func TestCodexParity_Baseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.tsv")
	baseline, _, err := readCodexParityBaseline(path)
	require.NoError(t, err)
	require.Nil(t, baseline)
	record := codexParityRecord(t, codexEditCompatibilityCases(t)[0].raw, parityEntry{}, codexParityInput{})
	require.NoError(t, writeCodexParityBaseline(path, map[string]codexParityRecording{"sessions/example.jsonl": record}))
	baseline, manifest, err := readCodexParityBaseline(path)
	require.NoError(t, err)
	assert.Equal(t, record.digest, baseline["sessions/example.jsonl"])
	assert.Equal(t, record.input, manifest.Inputs["sessions/example.jsonl"])
	require.NoError(t, os.WriteFile(path, []byte("tampered\tinput\toutput\n"), 0o600))
	_, _, err = readCodexParityBaseline(path)
	require.ErrorContains(t, err, "mismatched")
	require.NoError(t, os.Remove(path))
	_, _, err = readCodexParityBaseline(path)
	require.ErrorContains(t, err, "companion manifest exists")
}

func TestCodexParity_IOErrors(t *testing.T) {
	for _, operation := range []string{"reading rollout", "discovering Codex parity corpus"} {
		t.Run(operation, func(t *testing.T) {
			err := codexParityIOError(operation, &fs.PathError{
				Op: "open", Path: "/private/session/rollout.jsonl", Err: fs.ErrPermission,
			})
			require.ErrorIs(t, err, fs.ErrPermission)
			assert.ErrorContains(t, err, operation)
			assert.NotContains(t, err.Error(), "/private")
			assert.NotContains(t, err.Error(), "rollout.jsonl")
		})
	}
	assert.NoError(t, codexParityIOError("reading rollout", nil))
}

func TestCodexParity_CategoriesAndDiscovery(t *testing.T) {
	changes := map[string]any{"/tmp/old": map[string]any{"type": "update", "move_path": "/tmp/new", "unified_diff": "@@ -1 +1 @@\n-a\n+b\n"}}
	raw := codexRollout(t, codexPaginated,
		codexFileChangeEvent(at(1), "one", "completed", changes),
		codexFileChangeEvent(at(2), "two", "failed", changes),
		codexFileChangeEvent(at(3), "three", "declined", changes),
	)
	assert.Equal(t, []string{"paginated-completed", "paginated-declined", "paginated-failed", "real-move"}, codexParityCategories(raw))
	assert.Equal(t, []string{"legacy-direct"}, codexParityCategories(codexEditCompatibilityCases(t)[2].raw))
	home := t.TempDir()
	files, err := discoverCodexParityFiles(home)
	require.NoError(t, err)
	assert.Empty(t, files)
	filename := "rollout-2026-05-01T10-00-00-" + codexFixtureID + ".jsonl"
	writeCodexRollout(t, home, "sessions/2026/05/01/"+filename, raw, false)
	writeCodexRollout(t, home, "archived_sessions/"+filename+".zst", raw, true)
	writeCodexRollout(t, home, "sessions/rollout-2026-05-01T10-00-00-"+codexFixtureID+"_"+codexFixtureChildID+".jsonl", raw, false)
	files, err = discoverCodexParityFiles(home)
	require.NoError(t, err)
	require.Len(t, files, 3)
	compressed, reverts := 0, 0
	for _, file := range files {
		if file.revert {
			reverts++
			continue
		}
		assert.Equal(t, raw, readCodexCanaryFile(t, file))
		if file.compressed {
			compressed++
		}
	}
	assert.Equal(t, 1, compressed)
	assert.Equal(t, 1, reverts)
}
