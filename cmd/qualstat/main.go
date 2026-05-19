package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

type Report struct {
	Metadata             Metadata            `json:"metadata"`
	ByContentType        map[string]Metrics  `json:"by_content_type"`
	Overall              Metrics             `json:"overall"`
	PostProcessingDeltas []PostProcessingDelta `json:"post_processing_deltas"`
	Failures             []Failure           `json:"failures"`
}

type Metadata struct {
	Timestamp   string `json:"timestamp"`
	GitSHA      string `json:"git_sha"`
	GitBranch   string `json:"git_branch"`
	DatasetHash string `json:"dataset_hash"`
	GoVersion   string `json:"go_version"`
}

type Metrics struct {
	RecallAt1                  float64 `json:"recall_at_1"`
	RecallAt3                  float64 `json:"recall_at_3"`
	RecallAt5                  float64 `json:"recall_at_5"`
	RecallAt10                 float64 `json:"recall_at_10"`
	NDCGAt10                   float64 `json:"ndcg_at_10"`
	MRR                        float64 `json:"mrr"`
	MatchLayerAccuracy         float64 `json:"match_layer_accuracy"`
	RankCeilingPassRate        float64 `json:"rank_ceiling_pass_rate"`
	AvgCompressionRatio        float64 `json:"avg_compression_ratio"`
	AvgContextRecall           float64 `json:"avg_context_recall"`
	PerfectRecallRate          float64 `json:"perfect_recall_rate"`
	AvgEffectiveCompression    float64 `json:"avg_effective_compression"`
	CaseCount                  int     `json:"case_count"`
	// TODO(task-5): render negative-case stats once NIAH metrics land
	NegativeCaseCount          int `json:"negative_case_count"`
	NegativeFalsePositiveCount int `json:"negative_false_positive_count"`
}

type PostProcessingDelta struct {
	CaseID   string `json:"case_id"`
	PreRank  int    `json:"pre_rank"`
	PostRank int    `json:"post_rank"`
	Delta    int    `json:"delta"`
}

type Failure struct {
	CaseID   string `json:"case_id"`
	Type     string `json:"type"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Detail   string `json:"detail"`
}

const (
	epsilon            = 0.0001
	defaultFlexThresh  = -0.02
	maxFailuresToPrint = 10
)

func main() {
	colorFlag := flag.Bool("color", false, "enable colored output")
	markdownFlag := flag.Bool("markdown", false, "output as markdown tables (for docs)")
	flexThresh := flag.Float64("threshold", defaultFlexThresh, "configurable regression threshold for R@K/MRR/NDCG/Compression metrics")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: qualstat [flags] <report.json> [baseline.json]\n\n")
		fmt.Fprintf(os.Stderr, "Single file:  qualstat results.json\n")
		fmt.Fprintf(os.Stderr, "Comparison:   qualstat baseline.json feature.json\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 || len(args) > 2 {
		flag.Usage()
		os.Exit(2)
	}

	p := printer{color: *colorFlag}

	if len(args) == 1 {
		report, err := loadReport(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %s\n", err)
			os.Exit(2)
		}
		if *markdownFlag {
			printMarkdown(report)
		} else {
			printSingle(p, report)
		}
		return
	}

	base, err := loadReport(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading baseline: %s\n", err)
		os.Exit(2)
	}
	target, err := loadReport(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading target: %s\n", err)
		os.Exit(2)
	}

	if base.Metadata.DatasetHash != target.Metadata.DatasetHash {
		fmt.Fprintf(os.Stderr, "error: dataset hash mismatch\n  baseline: %s\n  target:   %s\n\nReports must use the same fixture dataset. Re-run benchmarks after aligning fixtures.\n",
			base.Metadata.DatasetHash, target.Metadata.DatasetHash)
		os.Exit(2)
	}

	hasRegressions := printComparison(p, base, target, *flexThresh)
	if hasRegressions {
		os.Exit(1)
	}
}

func loadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return r, nil
}

type printer struct {
	color bool
}

func (p printer) regression(s string) string {
	if !p.color {
		return s
	}
	return "\033[31m" + s + "\033[0m"
}

func (p printer) improvement(s string) string {
	if !p.color {
		return s
	}
	return "\033[32m" + s + "\033[0m"
}

func printSingle(p printer, r Report) {
	fmt.Printf("Dataset: %s\n", r.Metadata.DatasetHash)
	fmt.Printf("Branch:  %s (%s)\n", r.Metadata.GitBranch, r.Metadata.GitSHA)
	fmt.Printf("Go:      %s\n", r.Metadata.GoVersion)
	fmt.Printf("Time:    %s\n\n", r.Metadata.Timestamp)

	contentTypes := sortedKeys(r.ByContentType)

	fmt.Println("=== Retrieval Quality ===")
	printSingleHeader()
	for _, ct := range contentTypes {
		m := r.ByContentType[ct]
		printSingleRetrievalRows(ct, m)
	}
	printSingleRetrievalRows("overall", r.Overall)
	fmt.Println()

	fmt.Println("=== Context Reduction ===")
	printSingleHeader()
	for _, ct := range contentTypes {
		m := r.ByContentType[ct]
		printSingleContextRows(ct, m)
	}
	printSingleContextRows("overall", r.Overall)
	fmt.Println()

	if len(r.PostProcessingDeltas) > 0 {
		fmt.Println("=== Post-Processing Rank Deltas ===")
		fmt.Printf("%-24s  %8s  %8s  %6s\n", "Case", "Pre", "Post", "Delta")
		fmt.Println(strings.Repeat("-", 52))
		for _, d := range r.PostProcessingDeltas {
			marker := ""
			if d.Delta < 0 {
				marker = p.regression(" !")
			}
			fmt.Printf("%-24s  %8d  %8d  %+6d%s\n", d.CaseID, d.PreRank, d.PostRank, d.Delta, marker)
		}
		fmt.Println()
	}

	if len(r.Failures) > 0 {
		fmt.Printf("Failures: %d\n", len(r.Failures))
		n := min(len(r.Failures), maxFailuresToPrint)
		for _, f := range r.Failures[:n] {
			fmt.Printf("  %s -- %s: expected %s, got %s\n", f.CaseID, f.Type, f.Expected, f.Actual)
		}
		if len(r.Failures) > maxFailuresToPrint {
			fmt.Printf("  ... and %d more omitted\n", len(r.Failures)-maxFailuresToPrint)
		}
	}
}

func printSingleHeader() {
	fmt.Printf("%-36s  %10s  %6s\n", "Metric", "Value", "Cases")
	fmt.Println(strings.Repeat("-", 56))
}

func printSingleRetrievalRows(label string, m Metrics) {
	tag := "(" + label + ")"
	fmt.Printf("%-36s  %10.3f  %6d\n", "R@1 "+tag, m.RecallAt1, m.CaseCount)
	fmt.Printf("%-36s  %10.3f\n", "R@3 "+tag, m.RecallAt3)
	fmt.Printf("%-36s  %10.3f\n", "R@5 "+tag, m.RecallAt5)
	fmt.Printf("%-36s  %10.3f\n", "R@10 "+tag, m.RecallAt10)
	fmt.Printf("%-36s  %10.3f\n", "NDCG@10 "+tag, m.NDCGAt10)
	fmt.Printf("%-36s  %10.3f\n", "MRR "+tag, m.MRR)
	fmt.Printf("%-36s  %10.3f\n", "Match-Layer Accuracy "+tag, m.MatchLayerAccuracy)
	fmt.Printf("%-36s  %10.3f\n", "Rank Ceiling Pass "+tag, m.RankCeilingPassRate)
}

func printSingleContextRows(label string, m Metrics) {
	tag := "(" + label + ")"
	fmt.Printf("%-36s  %9.1f%%  %6d\n", "Compression Ratio "+tag, m.AvgCompressionRatio*100, m.CaseCount)
	fmt.Printf("%-36s  %10.3f\n", "Context Recall "+tag, m.AvgContextRecall)
	fmt.Printf("%-36s  %9.1f%%\n", "Perfect Recall Rate "+tag, m.PerfectRecallRate*100)
	fmt.Printf("%-36s  %9.1f%%\n", "Eff. Compression "+tag, m.AvgEffectiveCompression*100)
}

func printComparison(p printer, base, target Report, flexThresh float64) bool {
	fmt.Printf("Dataset: %s (verified)\n", base.Metadata.DatasetHash)
	fmt.Printf("Base:    %s (%s)\n", base.Metadata.GitBranch, base.Metadata.GitSHA)
	fmt.Printf("Target:  %s (%s)\n\n", target.Metadata.GitBranch, target.Metadata.GitSHA)

	hasRegressions := false

	allTypes := mergeKeys(base.ByContentType, target.ByContentType)

	fmt.Println("=== Retrieval Quality ===")
	printComparisonHeader()
	for _, ct := range allTypes {
		bm := base.ByContentType[ct]
		tm := target.ByContentType[ct]
		if printComparisonRetrievalRows(p, ct, bm, tm, flexThresh) {
			hasRegressions = true
		}
	}
	if printComparisonRetrievalRows(p, "overall", base.Overall, target.Overall, flexThresh) {
		hasRegressions = true
	}
	fmt.Println()

	fmt.Println("=== Context Reduction ===")
	printComparisonHeader()
	for _, ct := range allTypes {
		bm := base.ByContentType[ct]
		tm := target.ByContentType[ct]
		if printComparisonContextRows(p, ct, bm, tm, flexThresh) {
			hasRegressions = true
		}
	}
	if printComparisonContextRows(p, "overall", base.Overall, target.Overall, flexThresh) {
		hasRegressions = true
	}
	fmt.Println()

	newFailures, resolvedFailures := diffFailures(base.Failures, target.Failures)
	if len(newFailures) > 0 || len(resolvedFailures) > 0 {
		fmt.Printf("Failures diff: %d new, %d resolved\n", len(newFailures), len(resolvedFailures))
		printed := 0
		for _, f := range newFailures {
			if printed >= maxFailuresToPrint {
				break
			}
			fmt.Printf("  NEW: %s -- %s: expected %s, got %s\n", f.CaseID, f.Type, f.Expected, f.Actual)
			printed++
		}
		for _, f := range resolvedFailures {
			if printed >= maxFailuresToPrint {
				break
			}
			fmt.Printf("  RESOLVED: %s -- was: %s\n", f.CaseID, f.Type)
			printed++
		}
		total := len(newFailures) + len(resolvedFailures)
		if total > maxFailuresToPrint {
			fmt.Printf("  ... and %d more omitted\n", total-maxFailuresToPrint)
		}
		if len(newFailures) > 0 {
			hasRegressions = true
		}
	}

	return hasRegressions
}

func printComparisonHeader() {
	fmt.Printf("%-36s  %10s  %10s  %10s\n", "Metric", "Base", "Target", "delta")
	fmt.Println(strings.Repeat("-", 72))
}

type metricDef struct {
	name   string
	base   float64
	target float64
	strict bool
	pct    bool
}

func printComparisonRetrievalRows(p printer, label string, bm, tm Metrics, flexThresh float64) bool {
	tag := "(" + label + ")"
	metrics := []metricDef{
		{"R@1 " + tag, bm.RecallAt1, tm.RecallAt1, false, false},
		{"R@3 " + tag, bm.RecallAt3, tm.RecallAt3, false, false},
		{"R@5 " + tag, bm.RecallAt5, tm.RecallAt5, false, false},
		{"R@10 " + tag, bm.RecallAt10, tm.RecallAt10, false, false},
		{"NDCG@10 " + tag, bm.NDCGAt10, tm.NDCGAt10, false, false},
		{"MRR " + tag, bm.MRR, tm.MRR, false, false},
		{"Match-Layer Accuracy " + tag, bm.MatchLayerAccuracy, tm.MatchLayerAccuracy, true, false},
		{"Rank Ceiling Pass " + tag, bm.RankCeilingPassRate, tm.RankCeilingPassRate, false, false},
	}
	return printMetricRows(p, metrics, flexThresh)
}

func printComparisonContextRows(p printer, label string, bm, tm Metrics, flexThresh float64) bool {
	tag := "(" + label + ")"
	metrics := []metricDef{
		{"Compression Ratio " + tag, bm.AvgCompressionRatio, tm.AvgCompressionRatio, false, true},
		{"Context Recall " + tag, bm.AvgContextRecall, tm.AvgContextRecall, true, false},
		{"Perfect Recall Rate " + tag, bm.PerfectRecallRate, tm.PerfectRecallRate, true, true},
		{"Eff. Compression " + tag, bm.AvgEffectiveCompression, tm.AvgEffectiveCompression, false, true},
	}
	return printMetricRows(p, metrics, flexThresh)
}

func printMetricRows(p printer, metrics []metricDef, flexThresh float64) bool {
	hasRegressions := false
	for _, m := range metrics {
		delta := m.target - m.base
		threshold := flexThresh
		if m.strict {
			threshold = 0.0
		}

		marker := ""
		if delta < threshold-epsilon {
			marker = " !"
			hasRegressions = true
		}

		var baseStr, targetStr, deltaStr string
		if m.pct {
			baseStr = fmt.Sprintf("%9.1f%%", m.base*100)
			targetStr = fmt.Sprintf("%9.1f%%", m.target*100)
			deltaStr = fmt.Sprintf("%+9.1f%%", delta*100)
		} else {
			baseStr = fmt.Sprintf("%10.3f", m.base)
			targetStr = fmt.Sprintf("%10.3f", m.target)
			deltaStr = fmt.Sprintf("%+10.3f", delta)
		}

		if math.Abs(delta) < epsilon {
			deltaStr = fmt.Sprintf("%*s", len(baseStr), "~")
			marker = ""
		}

		line := fmt.Sprintf("%-36s  %s  %s  %s%s", m.name, baseStr, targetStr, deltaStr, marker)
		if marker != "" {
			line = p.regression(line)
		} else if delta > epsilon {
			line = p.improvement(line)
		}
		fmt.Println(line)
	}
	return hasRegressions
}

func diffFailures(baseFailures, targetFailures []Failure) (newFailures, resolved []Failure) {
	baseSet := make(map[string]Failure, len(baseFailures))
	for _, f := range baseFailures {
		baseSet[f.CaseID+"|"+f.Type] = f
	}

	targetSet := make(map[string]Failure, len(targetFailures))
	for _, f := range targetFailures {
		targetSet[f.CaseID+"|"+f.Type] = f
	}

	for key, f := range targetSet {
		if _, exists := baseSet[key]; !exists {
			newFailures = append(newFailures, f)
		}
	}

	for key, f := range baseSet {
		if _, exists := targetSet[key]; !exists {
			resolved = append(resolved, f)
		}
	}

	sort.Slice(newFailures, func(i, j int) bool { return newFailures[i].CaseID < newFailures[j].CaseID })
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].CaseID < resolved[j].CaseID })
	return newFailures, resolved
}

func sortedKeys(m map[string]Metrics) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func capitalize(s string) string {
	if s == "json" {
		return "JSON"
	}
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func printMarkdown(r Report) {
	contentTypes := sortedKeys(r.ByContentType)

	fmt.Println("### Retrieval Quality — Overall")
	fmt.Println()
	fmt.Println("| Metric | Score | Cases |")
	fmt.Println("|--------|-------|-------|")
	printMarkdownRetrievalRow("R@1", r.Overall.RecallAt1, r.Overall.CaseCount)
	printMarkdownRetrievalRow("R@3", r.Overall.RecallAt3, 0)
	printMarkdownRetrievalRow("R@5", r.Overall.RecallAt5, 0)
	printMarkdownRetrievalRow("R@10", r.Overall.RecallAt10, 0)
	printMarkdownRetrievalRow("NDCG@10", r.Overall.NDCGAt10, 0)
	printMarkdownRetrievalRow("MRR", r.Overall.MRR, 0)
	printMarkdownRetrievalRow("Rank Ceiling Pass", r.Overall.RankCeilingPassRate, 0)
	fmt.Println()

	fmt.Println("### Retrieval Quality — By Content Type")
	fmt.Println()
	fmt.Println("| Content Type | R@1 | R@5 | R@10 | NDCG@10 | MRR | Cases |")
	fmt.Println("|---|---|---|---|---|---|---|")
	for _, ct := range contentTypes {
		m := r.ByContentType[ct]
		fmt.Printf("| %s | %.3f | %.3f | %.3f | %.3f | %.3f | %d |\n",
			capitalize(ct), m.RecallAt1, m.RecallAt5, m.RecallAt10, m.NDCGAt10, m.MRR, m.CaseCount)
	}
	fmt.Println()

	fmt.Println("### Context Reduction — Overall")
	fmt.Println()
	fmt.Println("| Metric | Score | Cases |")
	fmt.Println("|--------|-------|-------|")
	fmt.Printf("| Compression Ratio | %.1f%% | %d |\n", r.Overall.AvgCompressionRatio*100, r.Overall.CaseCount)
	printMarkdownRetrievalRow("Context Recall", r.Overall.AvgContextRecall, 0)
	fmt.Printf("| Perfect Recall Rate | %.1f%% | |\n", r.Overall.PerfectRecallRate*100)
	fmt.Printf("| Effective Compression | %.1f%% | |\n", r.Overall.AvgEffectiveCompression*100)
	fmt.Println()

	fmt.Println("### Context Reduction — By Content Type")
	fmt.Println()
	fmt.Println("| Content Type | Compression | Context Recall | Perfect Recall | Eff. Compression | Cases |")
	fmt.Println("|---|---|---|---|---|---|")
	for _, ct := range contentTypes {
		m := r.ByContentType[ct]
		fmt.Printf("| %s | %.1f%% | %.3f | %.1f%% | %.1f%% | %d |\n",
			capitalize(ct), m.AvgCompressionRatio*100, m.AvgContextRecall,
			m.PerfectRecallRate*100, m.AvgEffectiveCompression*100, m.CaseCount)
	}
	fmt.Println()

	if len(r.Failures) > 0 {
		fmt.Printf("### Failures (%d total)\n\n", len(r.Failures))
		n := min(len(r.Failures), maxFailuresToPrint)
		for _, f := range r.Failures[:n] {
			fmt.Printf("- `%s` — %s: expected %s, got %s\n", f.CaseID, f.Type, f.Expected, f.Actual)
		}
		if len(r.Failures) > maxFailuresToPrint {
			fmt.Printf("- ... and %d more\n", len(r.Failures)-maxFailuresToPrint)
		}
	}
}

func printMarkdownRetrievalRow(name string, val float64, cases int) {
	if cases > 0 {
		fmt.Printf("| %s | %.3f | %d |\n", name, val, cases)
	} else {
		fmt.Printf("| %s | %.3f | |\n", name, val)
	}
}

func mergeKeys(a, b map[string]Metrics) []string {
	seen := make(map[string]bool)
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
