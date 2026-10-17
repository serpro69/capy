package runstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/stretchr/testify/require"
)

func projectionFixture(t *testing.T) (*Store, Admitted) {
	t.Helper()
	path, err := config.ResolveRunDBPath(t.TempDir())
	require.NoError(t, err)
	st, err := Open(t.Context(), path, testKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	row, err := st.Admit(t.Context(), owner, Program{TTLHours: 24, MaxRunBytes: 4 << 20})
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID))
	require.NoError(t, st.Started(t.Context(), row.ID))
	return st, row
}
func pageText(page ReadPage) string {
	var b strings.Builder
	for _, f := range page.Fragments {
		b.WriteString(f.Text)
	}
	return b.String()
}

func TestProjectionMergedBarrierAndIndependentStreams(t *testing.T) {
	st, row := projectionFixture(t)
	appendEvent := func(seq uint64, stream, data string) {
		require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: seq, Phase: "run", Stream: stream, Data: []byte(data)}))
	}
	appendEvent(1, "stdout", "safe <priv")
	appendEvent(2, "stderr", "diagnostic!\n")
	first, err := st.ReadProjection(t.Context(), row.ID, "merged", "", 100)
	require.NoError(t, err)
	require.Equal(t, "safe ", pageText(first))
	require.True(t, first.RedactionPending)
	require.NotNil(t, first.NextCursor)
	independent, err := st.ReadProjection(t.Context(), row.ID, "stderr", "", 100)
	require.NoError(t, err)
	require.Equal(t, "diagnostic!\n", pageText(independent))
	require.False(t, independent.RedactionPending)
	appendEvent(3, "stdout", "ate>SECRET\nHIDDEN</private> public!\n")
	code := 0
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code}))
	rest, err := st.ReadProjection(t.Context(), row.ID, "merged", *first.NextCursor, 1024)
	require.NoError(t, err)
	require.Equal(t, sanitize.RedactedPrivate+"diagnostic!\n"+sanitize.RedactedPrivate+" public!\n", pageText(rest))
	require.True(t, rest.EOF)
	raw, err := st.ReadRaw(t.Context(), row.ID, "stdout", "", 1024)
	require.NoError(t, err)
	var got []byte
	for _, f := range raw.Fragments {
		got = append(got, f.Data...)
	}
	require.Equal(t, "safe <private>SECRET\nHIDDEN</private> public!\n", string(got))
	inside, err := st.Read(t.Context(), row.ID, ReadOptions{Stream: "stdout", StartLine: 2, MaxBytes: 100})
	require.NoError(t, err)
	require.NotContains(t, pageText(inside), "HIDDEN")
	require.Contains(t, pageText(inside), sanitize.RedactedPrivate)
}

func TestProjectionPaginationVersionsAndHugeLines(t *testing.T) {
	st, row := projectionFixture(t)
	original := []byte(strings.Repeat("Z€", 60000) + "!\n")
	seq := uint64(1)
	for i := 0; i < len(original); i += 16384 {
		require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: seq, Phase: "run", Stream: "stdout", Data: original[i:min(i+16384, len(original))]}))
		seq++
	}
	code := 0
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code, ObservedBytes: int64(len(original))}))
	cursor := ""
	var got strings.Builder
	for pages := 0; pages < 2000; pages++ {
		page, err := st.ReadProjection(t.Context(), row.ID, "stdout", cursor, 257)
		require.NoError(t, err)
		got.WriteString(pageText(page))
		if page.EOF {
			break
		}
		require.NotNil(t, page.NextCursor)
		require.NotEqual(t, cursor, *page.NextCursor)
		cursor = *page.NextCursor
	}
	actual := got.String()
	if actual != string(original) {
		mismatch := 0
		for mismatch < min(len(original), len(actual)) && original[mismatch] == actual[mismatch] {
			mismatch++
		}
		t.Fatalf("projection mismatch at %d: raw=%d projected=%d", mismatch, len(original), len(actual))
	}
	first, err := st.ReadProjection(t.Context(), row.ID, "stdout", "", 1)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	second, err := st.ReadProjection(t.Context(), row.ID, "stdout", *first.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "€", pageText(second))
	c, err := st.decodeProjectionCursor(*first.NextCursor, row.ID, "stdout")
	require.NoError(t, err)
	c.Redaction++
	b, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = st.ReadProjection(t.Context(), row.ID, "stdout", base64.RawURLEncoding.EncodeToString(b), 10)
	require.ErrorContains(t, err, "redaction version")
	_, err = st.ReadRaw(t.Context(), row.ID, "stdout", *first.NextCursor, 10)
	require.Error(t, err)
	oldID := st.id
	st.id = "replacement-store"
	_, err = st.ReadProjection(t.Context(), row.ID, "stdout", *first.NextCursor, 10)
	st.id = oldID
	require.ErrorContains(t, err, "another store")
	_, err = st.ReadRaw(t.Context(), row.ID, "stdout", base64.RawURLEncoding.EncodeToString([]byte(`{}`)), 10)
	require.Error(t, err)
}

func TestProjectionStateFailurePreservesRawAndFailsClosed(t *testing.T) {
	st, row := projectionFixture(t)
	_, err := st.db.ExecContext(t.Context(), "UPDATE run_streams SET checkpoint=? WHERE run_id=? AND stream='stdout'", []byte(`{"version":999}`), row.ID)
	require.NoError(t, err)
	raw := []byte("sk-" + strings.Repeat("S", 30) + " private-tail")
	require.NoError(t, st.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: raw}))
	code := 0
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code, ObservedBytes: int64(len(raw))}))
	_, err = st.ReadProjection(t.Context(), row.ID, "stdout", "", 100)
	require.ErrorContains(t, err, "projection is unavailable")
	require.NotContains(t, err.Error(), "private-tail")
	page, err := st.ReadRaw(t.Context(), row.ID, "stdout", "", 100)
	require.NoError(t, err)
	require.True(t, bytes.Equal(raw, page.Fragments[0].Data))
	require.Equal(t, "complete", page.Run.Capture.State)
	require.Equal(t, "unavailable", page.Run.Redaction.State)
}

func TestProjectionResolvedPrefixKeepsObservedEventOrder(t *testing.T) {
	st, row := projectionFixture(t)
	for i, event := range []executor.OutputEvent{
		{Phase: "compile", Stream: "stdout", Data: []byte("s")},
		{Phase: "compile", Stream: "stderr", Data: []byte("ERR!\n")},
		{Phase: "run", Stream: "stdout", Data: []byte("afe!\n")},
	} {
		event.Sequence = uint64(i + 1)
		require.NoError(t, st.Append(t.Context(), row.ID, event))
	}
	code := 0
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code}))
	page, err := st.ReadProjection(t.Context(), row.ID, "merged", "", 100)
	require.NoError(t, err)
	require.Equal(t, "sERR!\nafe!\n", pageText(page))
	require.Equal(t, []uint64{1, 2, 3}, []uint64{page.Fragments[0].Sequence, page.Fragments[1].Sequence, page.Fragments[2].Sequence})
	require.Equal(t, "compile", page.Fragments[0].Phase)
	require.Equal(t, "run", page.Fragments[2].Phase)
}

func TestProjectionCursorConcurrentCapture(t *testing.T) {
	st, row := projectionFixture(t)
	writer, err := Open(t.Context(), st.Path(), testKey, false)
	require.NoError(t, err)
	defer writer.Close()
	done := make(chan error, 1)
	const rounds = 30
	go func() {
		seq := uint64(1)
		for i := 0; i < rounds; i++ {
			for _, event := range []executor.OutputEvent{
				{Phase: "run", Stream: "stdout", Data: []byte("s")},
				{Phase: "run", Stream: "stderr", Data: []byte(fmt.Sprintf("ERR%d!\n", i))},
				{Phase: "run", Stream: "stdout", Data: []byte("afe!\n")},
			} {
				event.Sequence = seq
				seq++
				if err := writer.Append(t.Context(), row.ID, event); err != nil {
					done <- err
					return
				}
			}
		}
		code := 0
		done <- writer.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code})
	}()
	var got, want strings.Builder
	for i := 0; i < rounds; i++ {
		fmt.Fprintf(&want, "sERR%d!\nafe!\n", i)
	}
	cursor := ""
	var lastSequence uint64
	finished := false
	for pages := 0; pages < 5000; pages++ {
		page, err := st.ReadProjection(t.Context(), row.ID, "merged", cursor, 17)
		require.NoError(t, err)
		for _, f := range page.Fragments {
			require.GreaterOrEqual(t, f.Sequence, lastSequence)
			lastSequence = f.Sequence
		}
		got.WriteString(pageText(page))
		if page.EOF {
			finished = true
			break
		}
		require.NotNil(t, page.NextCursor)
		cursor = *page.NextCursor
		if page.EndOfAvailable {
			time.Sleep(time.Millisecond)
		}
	}
	require.NoError(t, <-done)
	require.True(t, finished)
	require.Equal(t, want.String(), got.String())
}

func TestProjectionUTF8AcrossInterleavedEvents(t *testing.T) {
	st, row := projectionFixture(t)
	runeBytes := []byte("€")
	events := []executor.OutputEvent{{Phase: "run", Stream: "stdout", Data: runeBytes[:1]}, {Phase: "run", Stream: "stderr", Data: []byte("ERR!\n")}, {Phase: "run", Stream: "stdout", Data: append(runeBytes[1:], []byte("!\n")...)}}
	for i, event := range events {
		event.Sequence = uint64(i + 1)
		require.NoError(t, st.Append(t.Context(), row.ID, event))
	}
	code := 0
	require.NoError(t, st.Finish(t.Context(), row.ID, Completion{Started: true, Retired: true, ExitCode: &code}))
	page, err := st.ReadProjection(t.Context(), row.ID, "merged", "", 100)
	require.NoError(t, err)
	require.Equal(t, "€ERR!\n!\n", pageText(page))
	require.EqualValues(t, 1, page.Fragments[0].Sequence)
}
