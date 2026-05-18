package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

const maxFixtureLineBytes = 1024 * 1024

type benchEntry struct {
	ID          string      `json:"id"`
	ContentType string      `json:"content_type"`
	Haystack    string      `json:"haystack"`
	SourceLabel string      `json:"source_label"`
	SourceKind  SourceKind  `json:"source_kind"`
	Cases       []benchCase `json:"cases"`
}

type benchCase struct {
	CaseID              string   `json:"case_id"`
	Query               string   `json:"query"`
	Needles             []string `json:"needles"`
	ExpectedLayer       string   `json:"expected_layer"`
	ExpectedRankCeiling int      `json:"expected_rank_ceiling"`
}

func benchFixtureDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata", "bench")
}

func loadFixtures(t testing.TB, contentType string) []benchEntry {
	t.Helper()
	path := filepath.Join(benchFixtureDir(), contentType+".jsonl")
	f, err := os.Open(path)
	require.NoError(t, err, "opening fixture file %s", path)
	defer f.Close()

	var entries []benchEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, maxFixtureLineBytes), maxFixtureLineBytes)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry benchEntry
		require.NoError(t, json.Unmarshal(line, &entry), "%s.jsonl line %d", contentType, lineNum)
		entries = append(entries, entry)
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, entries, "no entries in %s.jsonl", contentType)
	return entries
}

func hashFixtureManifest(t testing.TB) string {
	t.Helper()
	dir := benchFixtureDir()
	dirEntries, err := os.ReadDir(dir)
	require.NoError(t, err, "reading fixture directory %s", dir)

	var names []string
	for _, de := range dirEntries {
		if !de.IsDir() {
			names = append(names, de.Name())
		}
	}
	require.NotEmpty(t, names, "no fixture files in %s", dir)

	h := sha256.New()
	for _, name := range names {
		func() {
			f, err := os.Open(filepath.Join(dir, name))
			require.NoError(t, err, "opening fixture file %s", name)
			defer f.Close()
			_, err = io.Copy(h, f)
			require.NoError(t, err, "reading fixture file %s", name)
		}()
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newBenchStore(t testing.TB) *ContentStore {
	t.Helper()
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := NewContentStore(dbPath, dir, 0, 0)
	t.Cleanup(func() { s.Close() })
	return s
}

func seedStore(t testing.TB, store *ContentStore, entries []benchEntry) {
	t.Helper()
	for _, e := range entries {
		var err error
		switch e.ContentType {
		case "markdown", "curated":
			_, err = store.Index(e.Haystack, e.SourceLabel, "", e.SourceKind)
		case "json":
			_, err = store.IndexJSON(e.Haystack, e.SourceLabel, e.SourceKind)
		case "plaintext":
			_, err = store.IndexPlainText(e.Haystack, e.SourceLabel, e.SourceKind)
		case "transcript":
			_, err = store.Index(e.Haystack, e.SourceLabel, "", e.SourceKind)
		default:
			t.Fatalf("unknown content_type %q in entry %s", e.ContentType, e.ID)
		}
		require.NoError(t, err, "seeding entry %s (type=%s)", e.ID, e.ContentType)
	}
}

func benchSearchOpts() SearchOptions {
	return SearchOptions{
		IncludeKinds: []SourceKind{KindDurable, KindEphemeral, KindSession},
	}
}

func TestBenchFixtureLoad(t *testing.T) {
	for _, ct := range contentTypes {
		t.Run(ct, func(t *testing.T) {
			entries := loadFixtures(t, ct)
			for _, e := range entries {
				require.NotEmpty(t, e.ID, "entry missing ID")
				require.Equal(t, ct, e.ContentType, "entry %s content_type mismatch", e.ID)
				require.NotEmpty(t, e.Haystack, "entry %s has empty haystack", e.ID)
				require.NotEmpty(t, e.SourceLabel, "entry %s missing source_label", e.ID)
				require.True(t, e.SourceKind.Valid(), "entry %s has invalid source_kind %q", e.ID, e.SourceKind)
				require.NotEmpty(t, e.Cases, "entry %s has no cases", e.ID)
				for _, c := range e.Cases {
					require.NotEmpty(t, c.CaseID, "case missing ID in entry %s", e.ID)
					require.NotEmpty(t, c.Query, "case %s has empty query", c.CaseID)
					require.NotEmpty(t, c.ExpectedLayer, "case %s missing expected_layer", c.CaseID)
					for _, needle := range c.Needles {
						require.Contains(t, e.Haystack, needle,
							"case %s: needle %q not found in haystack of entry %s", c.CaseID, needle, e.ID)
					}
				}
			}
		})
	}
}
