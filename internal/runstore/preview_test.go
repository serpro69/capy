package runstore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectionTailRemainsSanitizedAndLazy(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	run := retentionRun(t, s, o, "", false)
	data := strings.Repeat("prefix\n", 20000) + "<private>TAIL_SECRET</private>\n" + strings.Repeat("€", 50) + "\nTAIL_FAILURE\n"
	var sequence uint64
	appendSearchText(t, s, run.ID, "stdout", data, &sequence)
	finishSearchRun(t, s, run.ID, len(data))
	page, err := s.ReadProjectionTail(t.Context(), run.ID, "stdout", 200)
	require.NoError(t, err)
	require.Contains(t, pageText(page), "TAIL_FAILURE")
	require.NotContains(t, pageText(page), "TAIL_SECRET")
	require.LessOrEqual(t, len(pageText(page)), 200)
	require.True(t, page.EOF)
	var count int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_search_windows").Scan(&count))
	require.Zero(t, count)
	expireRun(t, s, run.ID)
	_, err = s.ReadProjectionTail(t.Context(), run.ID, "stdout", 200)
	requireErrorCode(t, err, "run_unavailable")
}
