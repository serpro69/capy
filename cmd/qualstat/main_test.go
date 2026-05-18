package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func qualstatBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "qualstat")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = pkgDir(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build qualstat: %s\n%s", err, out)
	}
	return bin
}

func pkgDir(t *testing.T) string {
	t.Helper()
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(f)
}

func testdataFile(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(pkgDir(t), "testdata", name)
}

func runQualstat(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("run qualstat: %s", err)
		}
	}
	return string(out), exitCode
}

func TestSingleFileMode(t *testing.T) {
	bin := qualstatBin(t)
	out, code := runQualstat(t, bin, testdataFile(t, "baseline.json"))

	if code != 0 {
		t.Errorf("expected exit 0, got %d\noutput:\n%s", code, out)
	}

	for _, want := range []string{
		"Dataset: sha256:e3b0c44298fc1c14",
		"Branch:  main",
		"=== Retrieval Quality ===",
		"=== Context Reduction ===",
		"R@1 (markdown)",
		"R@1 (json)",
		"R@1 (overall)",
		"Compression Ratio (markdown)",
		"Context Recall (json)",
		"0.850",
		"Failures: 1",
		"md_012_q3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestComparisonNoRegressions(t *testing.T) {
	bin := qualstatBin(t)
	f := testdataFile(t, "baseline.json")
	out, code := runQualstat(t, bin, f, f)

	if code != 0 {
		t.Errorf("expected exit 0 (no regressions), got %d\noutput:\n%s", code, out)
	}

	if !strings.Contains(out, "(verified)") {
		t.Errorf("output missing dataset verification marker\nfull output:\n%s", out)
	}

	for _, want := range []string{
		"=== Retrieval Quality ===",
		"=== Context Reduction ===",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	if strings.Contains(out, " !") {
		t.Errorf("expected no regression markers, but found '!' in output:\n%s", out)
	}
}

func TestComparisonWithRegressions(t *testing.T) {
	bin := qualstatBin(t)
	out, code := runQualstat(t, bin,
		testdataFile(t, "baseline.json"),
		testdataFile(t, "feature.json"),
	)

	if code != 1 {
		t.Errorf("expected exit 1 (regressions), got %d\noutput:\n%s", code, out)
	}

	if !strings.Contains(out, " !") {
		t.Errorf("expected regression markers in output:\n%s", out)
	}

	for _, want := range []string{
		"Match-Layer Accuracy (markdown)",
		"Context Recall (json)",
		"NEW: json_003_q2",
		"NEW: json_005_q1",
		"RESOLVED: md_012_q3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDatasetHashMismatch(t *testing.T) {
	bin := qualstatBin(t)
	_, code := runQualstat(t, bin,
		testdataFile(t, "baseline.json"),
		testdataFile(t, "mismatched_hash.json"),
	)

	if code != 2 {
		t.Errorf("expected exit 2 (hash mismatch), got %d", code)
	}
}

func TestInvalidArgs(t *testing.T) {
	bin := qualstatBin(t)

	t.Run("no args", func(t *testing.T) {
		_, code := runQualstat(t, bin)
		if code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})

	t.Run("too many args", func(t *testing.T) {
		_, code := runQualstat(t, bin, "a", "b", "c")
		if code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})

	t.Run("nonexistent file", func(t *testing.T) {
		_, code := runQualstat(t, bin, "/nonexistent/report.json")
		if code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})
}

func TestColorFlag(t *testing.T) {
	bin := qualstatBin(t)
	out, code := runQualstat(t, bin, "--color",
		testdataFile(t, "baseline.json"),
		testdataFile(t, "feature.json"),
	)

	if code != 1 {
		t.Errorf("expected exit 1 (regressions), got %d", code)
	}

	if !strings.Contains(out, "\033[31m") {
		t.Errorf("expected ANSI red color codes in output with --color flag")
	}
}

func TestFailuresDiffOutput(t *testing.T) {
	bin := qualstatBin(t)
	out, _ := runQualstat(t, bin,
		testdataFile(t, "baseline.json"),
		testdataFile(t, "feature.json"),
	)

	if !strings.Contains(out, "Failures diff: 2 new, 1 resolved") {
		t.Errorf("expected failure diff summary\nfull output:\n%s", out)
	}
}

func TestEpsilonComparison(t *testing.T) {
	dir := t.TempDir()

	writeReport := func(name string, recallAt1 float64) string {
		path := filepath.Join(dir, name)
		data := []byte(`{
			"metadata": {"timestamp":"t","git_sha":"s","git_branch":"b","dataset_hash":"h","go_version":"g"},
			"by_content_type": {},
			"overall": {
				"recall_at_1": ` + formatFloat(recallAt1) + `,
				"recall_at_3": 0, "recall_at_5": 0, "recall_at_10": 0,
				"ndcg_at_10": 0, "mrr": 0, "match_layer_accuracy": 0, "rank_ceiling_pass_rate": 0,
				"avg_compression_ratio": 0, "avg_context_recall": 0, "perfect_recall_rate": 0,
				"avg_effective_compression": 0, "case_count": 0, "negative_case_count": 0,
				"negative_false_positive_count": 0
			},
			"post_processing_deltas": [],
			"failures": []
		}`)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	bin := qualstatBin(t)

	base := writeReport("base.json", 0.900000)
	target := writeReport("target.json", 0.900005)

	out, code := runQualstat(t, bin, base, target)
	if code != 0 {
		t.Errorf("expected exit 0 (within epsilon), got %d\noutput:\n%s", code, out)
	}

	if strings.Contains(out, " !") {
		t.Errorf("expected no regression markers for within-epsilon delta:\n%s", out)
	}
}

func formatFloat(f float64) string {
	return fmt.Sprintf("%f", f)
}
