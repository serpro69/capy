package store

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type benchReport struct {
	Metadata             benchMetadata            `json:"metadata"`
	ByContentType        map[string]benchMetrics  `json:"by_content_type"`
	Overall              benchMetrics             `json:"overall"`
	PostProcessingDeltas []benchPostProcDelta     `json:"post_processing_deltas"`
	Failures             []benchFailure           `json:"failures"`
}

type benchMetadata struct {
	Timestamp   string `json:"timestamp"`
	GitSHA      string `json:"git_sha"`
	GitBranch   string `json:"git_branch"`
	DatasetHash string `json:"dataset_hash"`
	GoVersion   string `json:"go_version"`
}

type benchMetrics struct {
	RecallAt1                  float64 `json:"recall_at_1"`
	RecallAt3                  float64 `json:"recall_at_3"`
	RecallAt5                  float64 `json:"recall_at_5"`
	RecallAt10                 float64 `json:"recall_at_10"`
	NDCGAt10                   float64 `json:"ndcg_at_10"`
	MRR                        float64 `json:"mrr"`
	MatchLayerAccuracy         float64 `json:"match_layer_accuracy"`
	RankCeilingPassRate        float64 `json:"rank_ceiling_pass_rate"`
	AvgCompressionRatio     float64 `json:"avg_compression_ratio"`
	AvgContextRecall        float64 `json:"avg_context_recall"`
	PerfectRecallRate       float64 `json:"perfect_recall_rate"`
	AvgEffectiveCompression float64 `json:"avg_effective_compression"`
	CaseCount                  int     `json:"case_count"`
	NegativeCaseCount          int     `json:"negative_case_count"`
	NegativeFalsePositiveCount int     `json:"negative_false_positive_count"`
}

type benchPostProcDelta struct {
	CaseID   string `json:"case_id"`
	PreRank  int    `json:"pre_rank"`
	PostRank int    `json:"post_rank"`
	Delta    int    `json:"delta"`
}

type benchFailure struct {
	CaseID   string `json:"case_id"`
	Type     string `json:"type"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Detail   string `json:"detail"`
}

func TestBench(t *testing.T) {
	resultsPath := os.Getenv("CAPY_BENCH_RESULTS")
	if resultsPath == "" {
		t.Skip("CAPY_BENCH_RESULTS not set; skipping quality benchmarks")
	}

	report := benchReport{
		Metadata:             buildMetadata(t),
		ByContentType:        make(map[string]benchMetrics),
		PostProcessingDeltas: make([]benchPostProcDelta, 0),
	}

	t.Run("RetrievalQuality", func(t *testing.T) {
		runRetrievalQuality(t, &report)
	})

	t.Run("ContextReduction", func(t *testing.T) {
		runContextReduction(t, &report)
	})

	data, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err, "marshaling report")
	require.NoError(t, os.WriteFile(resultsPath, data, 0o644), "writing report to %s", resultsPath)
	t.Logf("Report written to %s", resultsPath)
}

func buildMetadata(t testing.TB) benchMetadata {
	t.Helper()
	sha := "unknown"
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	branch := "unknown"
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}
	return benchMetadata{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		GitSHA:      sha,
		GitBranch:   branch,
		DatasetHash: "sha256:" + hashFixtureManifest(t),
		GoVersion:   runtime.Version(),
	}
}

var contentTypes = []string{"markdown", "json", "plaintext", "transcript", "curated"}

func runRetrievalQuality(t *testing.T, report *benchReport) {
	opts := benchSearchOpts()

	allFailures := make([]benchFailure, 0)
	var totalCases, totalNeg, totalNegFP, totalRankCeilCases int
	var totalR1, totalR3, totalR5, totalR10 float64
	var totalNDCG, totalMRR float64
	var totalLayerMatch, totalRankCeilPass float64

	for _, ct := range contentTypes {
		t.Run(ct, func(t *testing.T) {
			entries := loadFixtures(t, ct)
			store := newBenchStore(t)
			seedStore(t, store, entries)

			var ctCases, ctNeg, ctNegFP, ctRankCeilCases int
			var ctR1, ctR3, ctR5, ctR10 float64
			var ctNDCG, ctMRR float64
			var ctLayerMatch, ctRankCeilPass float64

			for _, entry := range entries {
				for _, c := range entry.Cases {
					isNegative := len(c.Needles) == 0

					results, err := store.SearchWithFallback(c.Query, 10, opts)
					require.NoError(t, err, "search failed for case %s", c.CaseID)

					if isNegative {
						ctNeg++
						if len(results) > 0 {
							ctNegFP++
							allFailures = append(allFailures, benchFailure{
								CaseID:   c.CaseID,
								Type:     "negative_false_positive",
								Expected: "none",
								Actual:   results[0].MatchLayer,
								Detail:   "negative case returned results",
							})
						}
						continue
					}

					ctCases++

					ctR1 += computeRecallAtK(results, c.Needles, 1)
					ctR3 += computeRecallAtK(results, c.Needles, 3)
					ctR5 += computeRecallAtK(results, c.Needles, 5)
					ctR10 += computeRecallAtK(results, c.Needles, 10)
					ctNDCG += computeNDCG(results, c.Needles, 10)
					ctMRR += computeMRR(results, c.Needles)

					if len(results) > 0 && results[0].MatchLayer == c.ExpectedLayer {
						ctLayerMatch++
					} else if len(results) > 0 {
						allFailures = append(allFailures, benchFailure{
							CaseID:   c.CaseID,
							Type:     "match_layer",
							Expected: c.ExpectedLayer,
							Actual:   results[0].MatchLayer,
							Detail:   "match layer mismatch",
						})
					} else {
						allFailures = append(allFailures, benchFailure{
							CaseID:   c.CaseID,
							Type:     "no_results",
							Expected: c.ExpectedLayer,
							Actual:   "none",
							Detail:   "expected results but got none",
						})
					}

					firstRelevantRank := findFirstRelevantRank(results, c.Needles)
					if c.ExpectedRankCeiling > 0 {
						ctRankCeilCases++
					}
					if firstRelevantRank > 0 && firstRelevantRank <= c.ExpectedRankCeiling {
						ctRankCeilPass++
					} else if c.ExpectedRankCeiling > 0 {
						actual := fmt.Sprintf("%d", firstRelevantRank)
						detail := "result rank exceeded ceiling"
						if firstRelevantRank == 0 {
							actual = "not_found"
							detail = "no relevant result in top-K results"
						}
						allFailures = append(allFailures, benchFailure{
							CaseID:   c.CaseID,
							Type:     "rank_ceiling",
							Expected: fmt.Sprintf("%d", c.ExpectedRankCeiling),
							Actual:   actual,
							Detail:   detail,
						})
					}
				}
			}

			if ctCases > 0 {
				n := float64(ctCases)
				rankCeilRate := 0.0
				if ctRankCeilCases > 0 {
					rankCeilRate = ctRankCeilPass / float64(ctRankCeilCases)
				}
				report.ByContentType[ct] = benchMetrics{
					RecallAt1:           ctR1 / n,
					RecallAt3:           ctR3 / n,
					RecallAt5:           ctR5 / n,
					RecallAt10:          ctR10 / n,
					NDCGAt10:            ctNDCG / n,
					MRR:                 ctMRR / n,
					MatchLayerAccuracy:  ctLayerMatch / n,
					RankCeilingPassRate: rankCeilRate,
					CaseCount:           ctCases,
					NegativeCaseCount:          ctNeg,
					NegativeFalsePositiveCount: ctNegFP,
				}
			}

			totalCases += ctCases
			totalNeg += ctNeg
			totalNegFP += ctNegFP
			totalRankCeilCases += ctRankCeilCases
			totalR1 += ctR1
			totalR3 += ctR3
			totalR5 += ctR5
			totalR10 += ctR10
			totalNDCG += ctNDCG
			totalMRR += ctMRR
			totalLayerMatch += ctLayerMatch
			totalRankCeilPass += ctRankCeilPass
		})
	}

	if totalCases > 0 {
		n := float64(totalCases)
		overallRankCeilRate := 0.0
		if totalRankCeilCases > 0 {
			overallRankCeilRate = totalRankCeilPass / float64(totalRankCeilCases)
		}
		report.Overall = benchMetrics{
			RecallAt1:           totalR1 / n,
			RecallAt3:           totalR3 / n,
			RecallAt5:           totalR5 / n,
			RecallAt10:          totalR10 / n,
			NDCGAt10:            totalNDCG / n,
			MRR:                 totalMRR / n,
			MatchLayerAccuracy:  totalLayerMatch / n,
			RankCeilingPassRate: overallRankCeilRate,
			CaseCount:           totalCases,
			NegativeCaseCount:          totalNeg,
			NegativeFalsePositiveCount: totalNegFP,
		}
	}
	report.Failures = allFailures
}

func isRelevant(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func containsAllNeedles(text string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}

func findFirstRelevantRank(results []SearchResult, needles []string) int {
	for i, r := range results {
		if isRelevant(r.Content, needles) {
			return i + 1
		}
	}
	return 0
}

func computeRecallAtK(results []SearchResult, needles []string, k int) float64 {
	limit := min(len(results), k)
	for _, r := range results[:limit] {
		if isRelevant(r.Content, needles) {
			return 1.0
		}
	}
	return 0.0
}

func computeNDCG(results []SearchResult, needles []string, k int) float64 {
	limit := min(len(results), k)

	var dcg float64
	var relevantCount int
	for i, r := range results[:limit] {
		if isRelevant(r.Content, needles) {
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

func computeMRR(results []SearchResult, needles []string) float64 {
	rank := findFirstRelevantRank(results, needles)
	if rank == 0 {
		return 0.0
	}
	return 1.0 / float64(rank)
}

func computeContextRecall(results []SearchResult, needles []string) float64 {
	if len(needles) == 0 {
		return 1.0
	}
	found := make(map[int]bool)
	for _, r := range results {
		for i, needle := range needles {
			if strings.Contains(r.Content, needle) {
				found[i] = true
			}
		}
	}
	return float64(len(found)) / float64(len(needles))
}

func formatIntentSummary(results []SearchResult, query string, haystackLen int) string {
	totalKB := fmt.Sprintf("%.1f", float64(haystackLen)/1024)

	var b strings.Builder
	b.WriteString("Indexed sections from source into knowledge base.\n")

	if len(results) == 0 {
		fmt.Fprintf(&b, "No sections matched intent %q (%sKB).\n", query, totalKB)
		b.WriteString("\nUse search() to explore the indexed content.")
		return b.String()
	}

	fmt.Fprintf(&b, "%d sections matched %q (%sKB):\n\n", len(results), query, totalKB)

	for _, r := range results {
		preview := r.Content
		if nl := strings.IndexByte(preview, '\n'); nl != -1 {
			preview = preview[:nl]
		}
		if len(preview) > 120 {
			preview = preview[:120]
		}
		fmt.Fprintf(&b, "  - %s: %s\n", r.Title, preview)
	}

	b.WriteString("\nUse search(queries: [...]) to retrieve full content of any section.")
	return b.String()
}

func runContextReduction(t *testing.T, report *benchReport) {
	opts := benchSearchOpts()

	var totalCases, totalNeg, totalNegFP int
	var totalCR, totalCtxRecall, totalPerfectRecall, totalEffComp float64

	for _, ct := range contentTypes {
		t.Run(ct, func(t *testing.T) {
			entries := loadFixtures(t, ct)
			s := newBenchStore(t)
			seedStore(t, s, entries)

			var ctCases, ctNeg, ctNegFP int
			var ctCR, ctCtxRecall, ctPerfectRecall, ctEffComp float64

			for _, entry := range entries {
				for _, c := range entry.Cases {
					isNegative := len(c.Needles) == 0

					results, err := s.SearchWithFallback(c.Query, 10, opts)
					require.NoError(t, err, "search failed for case %s", c.CaseID)

					if isNegative {
						ctNeg++
						if len(results) == 0 {
							ctCR += 1.0
							ctCtxRecall += 1.0
							ctPerfectRecall += 1.0
							ctEffComp += 1.0
						} else {
							ctNegFP++
							summary := formatIntentSummary(results, c.Query, len(entry.Haystack))
							cr := 1.0 - float64(len(summary))/float64(len(entry.Haystack))
							if cr < 0 {
								cr = 0
							}
							ctCR += cr
						}
						ctCases++
						continue
					}

					ctCases++

					summary := formatIntentSummary(results, c.Query, len(entry.Haystack))
					cr := 1.0 - float64(len(summary))/float64(len(entry.Haystack))
					if cr < 0 {
						cr = 0
					}
					ctCR += cr

					recall := computeContextRecall(results, c.Needles)
					ctCtxRecall += recall

					if recall == 1.0 {
						ctPerfectRecall += 1.0
					}

					ctEffComp += cr * recall
				}
			}

			if ctCases > 0 {
				n := float64(ctCases)
				m, ok := report.ByContentType[ct]
				if !ok {
					m = benchMetrics{}
				}
				m.AvgCompressionRatio = ctCR / n
				m.AvgContextRecall = ctCtxRecall / n
				m.PerfectRecallRate = ctPerfectRecall / n
				m.AvgEffectiveCompression = ctEffComp / n
				if m.CaseCount == 0 {
					m.CaseCount = ctCases
					m.NegativeCaseCount = ctNeg
					m.NegativeFalsePositiveCount = ctNegFP
				}
				report.ByContentType[ct] = m
			}

			totalCases += ctCases
			totalNeg += ctNeg
			totalNegFP += ctNegFP
			totalCR += ctCR
			totalCtxRecall += ctCtxRecall
			totalPerfectRecall += ctPerfectRecall
			totalEffComp += ctEffComp
		})
	}

	if totalCases > 0 {
		n := float64(totalCases)
		report.Overall.AvgCompressionRatio = totalCR / n
		report.Overall.AvgContextRecall = totalCtxRecall / n
		report.Overall.PerfectRecallRate = totalPerfectRecall / n
		report.Overall.AvgEffectiveCompression = totalEffComp / n
		report.Overall.NegativeCaseCount += totalNeg
		report.Overall.NegativeFalsePositiveCount += totalNegFP
	}
}
