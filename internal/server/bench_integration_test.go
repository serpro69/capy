package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type thresholdResult struct {
	SizeBytes        int     `json:"size_bytes"`
	LatencyMs        float64 `json:"latency_ms"`
	OutputLen        int     `json:"output_len"`
	WasIndexed       bool    `json:"was_indexed"`
	CompressionRatio float64 `json:"compression_ratio,omitempty"`
}

func TestBench5000ByteThreshold(t *testing.T) {
	resultsPath := os.Getenv("CAPY_BENCH_RESULTS")
	if resultsPath == "" {
		t.Skip("CAPY_BENCH_RESULTS not set; skipping quality benchmarks")
	}

	srv := newTestServer(t, nil)

	sizes := []int{4999, 5001, 10000, 50000}
	results := make([]thresholdResult, 0, len(sizes))

	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dbytes", sz), func(t *testing.T) {
			// Generate output of exactly sz bytes.
			line := "benchmark data: configuration loaded successfully status=ok\n"
			repeat := sz / len(line)
			remainder := sz - repeat*len(line)
			output := strings.Repeat(line, repeat) + strings.Repeat("x", remainder)
			require.Equal(t, sz, len(output))

			code := fmt.Sprintf("printf '%%s' '%s'", strings.ReplaceAll(output, "'", "'\\''"))

			start := time.Now()
			r := callTool(t, srv, map[string]any{
				"language": "shell",
				"code":     code,
				"intent":   "configuration status",
			})
			elapsed := time.Since(start)
			require.False(t, r.IsError)

			text := resultText(r)
			wasIndexed := strings.Contains(text, "Indexed") || strings.Contains(text, "knowledge base")

			tr := thresholdResult{
				SizeBytes:  sz,
				LatencyMs:  float64(elapsed.Microseconds()) / 1000.0,
				OutputLen:  len(text),
				WasIndexed: wasIndexed,
			}
			if wasIndexed && sz > 0 {
				tr.CompressionRatio = 1.0 - float64(len(text))/float64(sz)
			}
			results = append(results, tr)

			t.Logf("size=%d latency=%.1fms indexed=%v output_len=%d",
				sz, tr.LatencyMs, wasIndexed, tr.OutputLen)
		})
	}

	appendThresholdResults(t, resultsPath, results)
}

func appendThresholdResults(t *testing.T, path string, results []thresholdResult) {
	t.Helper()

	var report map[string]json.RawMessage

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err, "reading existing report")
	}
	if err != nil {
		report = make(map[string]json.RawMessage)
	} else {
		require.NoError(t, json.Unmarshal(data, &report), "parsing existing report")
	}

	thresholdJSON, err := json.Marshal(results)
	require.NoError(t, err)
	report["threshold_5000"] = thresholdJSON

	out, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, out, 0o644))
	t.Logf("Threshold results appended to %s", path)
}
