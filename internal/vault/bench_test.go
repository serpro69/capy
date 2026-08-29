package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// bench_test.go is the A2 parity gate (design docs/wip/vault-session-search):
// it replays the knowledge store's transcript retrieval/NIAH fixtures against
// the VAULT chunk corpus (SearchChunks) so the two session-search paths can be
// compared. Each fixture haystack is synthesized into a Claude Code JSONL
// session and pushed through the real import path (scanner → chunker → chunk
// FTS tables) — no shortcut seeding — then every fixture case runs through
// SearchChunks and the same metrics the store bench computes are written to
// the shared report under by_content_type."vault_session", next to the
// knowledge.db "transcript" baseline qualstat renders beside it.
//
// The metric helpers deliberately mirror internal/store/bench_test.go (they
// live in another package's test files and cannot be imported); keep the two
// in sync when the scoring changes.

// vaultBenchEntry / vaultBenchCase mirror the fixture schema of
// internal/store/benchutil_test.go (benchEntry/benchCase).
type vaultBenchEntry struct {
	ID       string           `json:"id"`
	Haystack string           `json:"haystack"`
	Cases    []vaultBenchCase `json:"cases"`
}

type vaultBenchCase struct {
	CaseID              string   `json:"case_id"`
	Query               string   `json:"query"`
	Needles             []string `json:"needles"`
	ExpectedLayer       string   `json:"expected_layer"`
	ExpectedRankCeiling int      `json:"expected_rank_ceiling"`
}

// vaultBenchMetrics matches the JSON shape of the store bench's benchMetrics
// so qualstat parses the vault_session entry like any other content type.
// Compression metrics don't apply to this corpus and stay zero.
type vaultBenchMetrics struct {
	RecallAt1                  float64 `json:"recall_at_1"`
	RecallAt3                  float64 `json:"recall_at_3"`
	RecallAt5                  float64 `json:"recall_at_5"`
	RecallAt10                 float64 `json:"recall_at_10"`
	NDCGAt10                   float64 `json:"ndcg_at_10"`
	MRR                        float64 `json:"mrr"`
	MatchLayerAccuracy         float64 `json:"match_layer_accuracy"`
	RankCeilingPassRate        float64 `json:"rank_ceiling_pass_rate"`
	CaseCount                  int     `json:"case_count"`
	NegativeCaseCount          int     `json:"negative_case_count"`
	NegativeFalsePositiveCount int     `json:"negative_false_positive_count"`
}

func TestBench(t *testing.T) {
	resultsPath := os.Getenv("CAPY_BENCH_RESULTS")
	if resultsPath == "" {
		t.Skip("CAPY_BENCH_RESULTS not set; skipping quality benchmarks")
	}

	entries := loadTranscriptFixtures(t)
	s := newTestVault(t)
	root := t.TempDir()
	projectDir := filepath.Join(root, "-bench-proj")

	for i, e := range entries {
		uuid := fmt.Sprintf("%08d-0000-4000-8000-000000000000", i)
		writeSession(t, projectDir, uuid, transcriptToJSONL(t, e.Haystack, i), nil)
	}
	res := importFixture(t, s, root, ImportOptions{})
	require.Equal(t, len(entries), res.Imported, "every fixture session imports")

	requireNeedlesIndexed(t, s, entries)

	var m vaultBenchMetrics
	var rankCeilCases, rankCeilPass int
	for _, e := range entries {
		for _, c := range e.Cases {
			results, err := s.SearchChunks(context.Background(), SearchOptions{Query: c.Query, Limit: 10})
			require.NoError(t, err, "search failed for case %s", c.CaseID)

			if len(c.Needles) == 0 {
				m.NegativeCaseCount++
				if len(results) > 0 {
					m.NegativeFalsePositiveCount++
				}
				continue
			}

			m.CaseCount++
			m.RecallAt1 += vaultRecallAtK(results, c.Needles, 1)
			m.RecallAt3 += vaultRecallAtK(results, c.Needles, 3)
			m.RecallAt5 += vaultRecallAtK(results, c.Needles, 5)
			m.RecallAt10 += vaultRecallAtK(results, c.Needles, 10)
			m.NDCGAt10 += vaultNDCG(results, c.Needles, 10)

			rank := vaultFirstRelevantRank(results, c.Needles)
			if rank > 0 {
				m.MRR += 1.0 / float64(rank)
			} else {
				t.Logf("case %s: no relevant result in top 10 for query %q (needles %q)", c.CaseID, c.Query, c.Needles)
			}
			if len(results) > 0 && results[0].MatchLayer == c.ExpectedLayer {
				m.MatchLayerAccuracy++
			}
			if c.ExpectedRankCeiling > 0 {
				rankCeilCases++
				if rank > 0 && rank <= c.ExpectedRankCeiling {
					rankCeilPass++
				}
			}
		}
	}

	require.Positive(t, m.CaseCount, "no positive fixture cases ran")
	n := float64(m.CaseCount)
	m.RecallAt1 /= n
	m.RecallAt3 /= n
	m.RecallAt5 /= n
	m.RecallAt10 /= n
	m.NDCGAt10 /= n
	m.MRR /= n
	m.MatchLayerAccuracy /= n
	if rankCeilCases > 0 {
		m.RankCeilingPassRate = float64(rankCeilPass) / float64(rankCeilCases)
	}

	t.Logf("vault_session: R@1=%.3f R@5=%.3f R@10=%.3f NDCG@10=%.3f MRR=%.3f layer=%.3f ceil=%.3f cases=%d negFP=%d/%d",
		m.RecallAt1, m.RecallAt5, m.RecallAt10, m.NDCGAt10, m.MRR,
		m.MatchLayerAccuracy, m.RankCeilingPassRate, m.CaseCount,
		m.NegativeFalsePositiveCount, m.NegativeCaseCount)

	mergeVaultMetrics(t, resultsPath, m)
	assertTranscriptParity(t, resultsPath, m)
}

// loadTranscriptFixtures reads the knowledge store's transcript NIAH fixtures
// — the SAME dataset the knowledge.db session baseline runs on, so the
// comparison is apples-to-apples (and the report's dataset hash stays valid).
func loadTranscriptFixtures(t *testing.T) []vaultBenchEntry {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(thisFile), "..", "store", "testdata", "bench", "transcript.jsonl")

	data, err := os.ReadFile(path)
	require.NoError(t, err, "reading transcript fixtures")

	var entries []vaultBenchEntry
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e vaultBenchEntry
		require.NoError(t, json.Unmarshal([]byte(line), &e), "transcript.jsonl line %d", i+1)
		entries = append(entries, e)
	}
	require.NotEmpty(t, entries)
	return entries
}

// transcriptToJSONL converts a "Human: …\n\nAssistant: …" fixture haystack
// into Claude Code JSONL lines, one user/assistant message per speaker turn
// (blank lines inside a turn stay inside its message).
func transcriptToJSONL(t *testing.T, haystack string, entryIdx int) []byte {
	t.Helper()
	type msg struct {
		role string
		text string
	}
	var msgs []msg
	var cur *msg
	for _, line := range strings.Split(haystack, "\n") {
		switch {
		case strings.HasPrefix(line, "Human: "):
			msgs = append(msgs, msg{role: "user", text: strings.TrimPrefix(line, "Human: ")})
			cur = &msgs[len(msgs)-1]
		case strings.HasPrefix(line, "Assistant: "):
			msgs = append(msgs, msg{role: "assistant", text: strings.TrimPrefix(line, "Assistant: ")})
			cur = &msgs[len(msgs)-1]
		default:
			require.NotNil(t, cur, "haystack must open with a Human:/Assistant: turn")
			cur.text += "\n" + line
		}
	}
	require.NotEmpty(t, msgs, "haystack produced no messages")

	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(entryIdx) * time.Hour)
	lines := make([]map[string]any, 0, len(msgs))
	for i, mm := range msgs {
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		id := fmt.Sprintf("m%d-%d", entryIdx, i)
		if mm.role == "user" {
			lines = append(lines, userLineAt(id, "/bench/proj", ts, strings.TrimSpace(mm.text)))
		} else {
			lines = append(lines, assistantLineAt(id, "msg-"+id, ts, []map[string]any{
				{"type": "text", "text": strings.TrimSpace(mm.text)},
			}))
		}
	}
	return jsonlBytes(t, lines...)
}

// requireNeedlesIndexed fails loudly if scanning/sanitization dropped a
// needle from the chunk corpus — metric drops would otherwise be inscrutable.
func requireNeedlesIndexed(t *testing.T, s *VaultStore, entries []vaultBenchEntry) {
	t.Helper()
	db, err := s.getDB(context.Background())
	require.NoError(t, err)
	for _, e := range entries {
		for _, c := range e.Cases {
			for _, needle := range c.Needles {
				var n int
				require.NoError(t, db.QueryRow(
					`SELECT COUNT(*) FROM vault_chunks WHERE content_text LIKE '%' || ? || '%'`, needle).Scan(&n))
				require.Positive(t, n, "needle %q (case %s) missing from the indexed chunk corpus", needle, c.CaseID)
			}
		}
	}
}

// mergeVaultMetrics inserts the vault metrics into the shared bench report as
// by_content_type."vault_session", preserving everything the store/server
// benches already wrote (they run first under `make bench-quality`).
func mergeVaultMetrics(t *testing.T, path string, m vaultBenchMetrics) {
	t.Helper()

	report := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(path); err == nil {
		require.NoError(t, json.Unmarshal(data, &report), "parsing existing report")
	} else if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err, "reading existing report")
	}

	byCT := make(map[string]json.RawMessage)
	if raw, ok := report["by_content_type"]; ok {
		require.NoError(t, json.Unmarshal(raw, &byCT), "parsing by_content_type")
	}
	mJSON, err := json.Marshal(m)
	require.NoError(t, err)
	byCT["vault_session"] = mJSON
	byCTJSON, err := json.Marshal(byCT)
	require.NoError(t, err)
	report["by_content_type"] = byCTJSON

	out, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, out, 0o644))
	t.Logf("vault_session metrics merged into %s", path)
}

// parityTolerance is the maximum acceptable drop of the vault corpus below the
// knowledge.db transcript baseline on the headline retrieval metrics —
// mirroring qualstat's default regression threshold (-0.02).
const parityTolerance = 0.02

// assertTranscriptParity enforces A2 when the same report already holds the
// knowledge.db transcript baseline (it does under `make bench-quality`, where
// the store bench runs first; standalone vault runs skip the check).
func assertTranscriptParity(t *testing.T, path string, vault vaultBenchMetrics) {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var report struct {
		ByContentType map[string]vaultBenchMetrics `json:"by_content_type"`
	}
	require.NoError(t, json.Unmarshal(data, &report))
	base, ok := report.ByContentType["transcript"]
	if !ok || base.CaseCount == 0 {
		t.Log("no transcript baseline in report — skipping parity assertion (run via `make bench-quality` for the full gate)")
		return
	}

	check := func(name string, baseV, vaultV float64) {
		delta := vaultV - baseV
		t.Logf("parity %-12s transcript=%.3f vault_session=%.3f delta=%+.3f", name, baseV, vaultV, delta)
		if delta < -parityTolerance {
			t.Errorf("A2 parity violation: %s dropped %.3f below the knowledge.db session baseline (tolerance %.2f)",
				name, math.Abs(delta), parityTolerance)
		}
	}
	check("recall@1", base.RecallAt1, vault.RecallAt1)
	check("recall@5", base.RecallAt5, vault.RecallAt5)
	check("recall@10", base.RecallAt10, vault.RecallAt10)
	check("ndcg@10", base.NDCGAt10, vault.NDCGAt10)
	check("mrr", base.MRR, vault.MRR)
}

// --- metric helpers: keep in sync with internal/store/bench_test.go ---

func vaultIsRelevant(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func vaultFirstRelevantRank(results []SearchResult, needles []string) int {
	for i, r := range results {
		if vaultIsRelevant(r.Content, needles) {
			return i + 1
		}
	}
	return 0
}

func vaultRecallAtK(results []SearchResult, needles []string, k int) float64 {
	limit := min(len(results), k)
	for _, r := range results[:limit] {
		if vaultIsRelevant(r.Content, needles) {
			return 1.0
		}
	}
	return 0.0
}

func vaultNDCG(results []SearchResult, needles []string, k int) float64 {
	limit := min(len(results), k)

	var dcg float64
	var relevantCount int
	for i, r := range results[:limit] {
		if vaultIsRelevant(r.Content, needles) {
			dcg += 1.0 / math.Log2(float64(i+2))
			relevantCount++
		}
	}
	if relevantCount == 0 {
		return 0.0
	}

	var idcg float64
	for i := range min(relevantCount, limit) {
		idcg += 1.0 / math.Log2(float64(i+2))
	}
	return dcg / idcg
}
