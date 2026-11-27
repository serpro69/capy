package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The brief workload (docs/feat/wip/vault-session-brief/implementation.md
// § Verification) measures the finished encrypted read path on 1,000 sessions
// × 100 indexed rows × 256 text bytes. It takes tens of seconds to build, so it
// runs only when CAPY_VAULT_BRIEF_WORKLOAD=1.
const (
	workloadSessions = 1000
	workloadRows     = 100
	workloadText     = 256
)

func workloadUUID(i int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", i+1, i+1)
}

// workloadRecord alternates user/assistant rows: row r is in turn r/2, so the
// final user row is line 98 and its assistant reply line 99.
func workloadRecord(i int) *SessionRecord {
	uuid := workloadUUID(i)
	rec := &SessionRecord{Session: Session{
		UUID:             uuid,
		Title:            fmt.Sprintf("Workload session %d", i),
		StartTime:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
		EndTime:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Minute + time.Second),
		MessageCount:     workloadRows,
		ContentHash:      "hash-" + uuid,
		MachineID:        "machine-a",
		ClaudeProjectDir: fmt.Sprintf("-home-user-project-%d", i%10),
		ProjectPath:      fmt.Sprintf("/home/user/project-%d", i%10),
		IndexVersion:     currentIndexVersion,
		RawJSONL:         []byte(`{"type":"user"}` + "\n"),
	}}
	for r := range workloadRows {
		role := roleUser
		if r%2 == 1 {
			role = roleAssistant
		}
		prefix := fmt.Sprintf("s%d r%d %s ", i, r, role)
		text := prefix + strings.Repeat("lorem ipsum dolor ", workloadText)[:workloadText-len(prefix)]
		rec.FTS = append(rec.FTS, FTSRow{
			SessionUUID: uuid, TurnIndex: r / 2, MessageIndex: r % 2, LineIndex: r, Role: role, ContentText: text,
		})
	}
	return rec
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func TestBriefWorkload(t *testing.T) {
	if os.Getenv("CAPY_VAULT_BRIEF_WORKLOAD") != "1" {
		t.Skip("set CAPY_VAULT_BRIEF_WORKLOAD=1 to run the encrypted brief workload")
	}
	t.Setenv(vaultKeyEnv, testVaultKey)
	path := filepath.Join(t.TempDir(), "vault.db")
	writer := NewVaultStore(path)
	ctx := context.Background()

	built := time.Now()
	const batch = 50
	for start := 0; start < workloadSessions; start += batch {
		writes := make([]SessionWrite, 0, batch)
		for i := start; i < start+batch; i++ {
			writes = append(writes, SessionWrite{Record: workloadRecord(i)})
		}
		_, err := writer.WriteBatch(ctx, writes)
		require.NoError(t, err)
	}
	t.Logf("fixture built in %s", time.Since(built).Round(time.Millisecond))

	target := workloadUUID(0) // oldest session, rowids 1..100
	check := func(label string) time.Duration {
		t.Helper()
		started := time.Now()
		b, err := NewVaultStore(path).ReadBrief(ctx, target[:8], BriefScope{})
		require.NoError(t, err, label)
		out := RenderBrief(b)
		elapsed := time.Since(started)
		require.Equal(t, int64(0), b.Opening.Line, label)
		require.Equal(t, int64(98), b.Latest.Line, label)
		require.NotNil(t, b.Assistant, label)
		require.Equal(t, int64(99), b.Assistant.Line, label)
		require.LessOrEqual(t, len(out), BriefMaxBytes, label)
		require.Less(t, elapsed, briefDeadline, label)
		t.Logf("%-28s %s (db %d B, wal %d B)", label, elapsed.Round(time.Millisecond), fileSize(path), fileSize(path+"-wal"))
		return elapsed
	}

	db, err := openBriefDB(ctx, path, testVaultKey)
	require.NoError(t, err)
	var totalBytes, totalRows int64
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*), sum(length(CAST(content_text AS BLOB))) FROM vault_fts`).Scan(&totalRows, &totalBytes))
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+briefPositionsSQL, 1)
	require.NoError(t, err)
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.NoError(t, db.Close())
	t.Logf("indexed rows %d, indexed text bytes %d; scan plan: %s", totalRows, totalBytes, strings.Join(plan, " | "))
	t.Logf("OS cache: not purged (warm after fixture build)")

	live := check("live WAL (writer open)")
	require.NoError(t, writer.Close())
	check("first reader after close")
	var last time.Duration
	for i := range 3 {
		last = check(fmt.Sprintf("fresh reader %d", i+1))
	}

	// A shorter caller deadline expiring mid-read returns an error and no brief.
	for _, deadline := range []time.Duration{max(last/2, time.Millisecond), max(live/2, time.Millisecond)} {
		dctx, cancel := context.WithTimeout(ctx, deadline)
		b, err := NewVaultStore(path).ReadBrief(dctx, target[:8], BriefScope{})
		cancel()
		require.Nil(t, b)
		require.Error(t, err)
		require.True(t, errors.Is(err, context.DeadlineExceeded), "deadline %s: %v", deadline, err)
		// Each deadline is shorter than a measured run, so it expires after the
		// session resolves: the error names the safe ID for `vault show`.
		var timeout *BriefTimeoutError
		require.ErrorAs(t, err, &timeout, "deadline %s: %v", deadline, err)
		require.Equal(t, target, timeout.SessionID)
		t.Logf("deadline %s: %v", deadline, err)
	}
}
