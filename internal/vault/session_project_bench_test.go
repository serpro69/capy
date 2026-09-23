package vault

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func checkProjectMetadataRead(t testing.TB, s *VaultStore, ctx context.Context, operation string, count int) {
	t.Helper()
	const project = "group-099"
	wantHits := min(20, count/100)
	switch operation {
	case "list":
		rows, err := s.ListSessions(ctx, ListOptions{Project: project, Limit: 20})
		require.NoError(t, err)
		require.Len(t, rows, wantHits)
	case "stats":
		stats, err := s.Stats(ctx)
		require.NoError(t, err)
		require.Equal(t, count, stats.Sessions)
		require.Len(t, stats.ByEffectiveProject, 100)
	case "exists", "absent":
		query := project
		if operation == "absent" {
			query = "missing project"
		}
		found, err := s.HasSessionsInProject(ctx, query, "")
		require.NoError(t, err)
		require.Equal(t, operation == "exists", found)
	case "search", "chunks":
		opts := SearchOptions{Query: "brontosaurus", Project: project, Limit: 20}
		var hits []SearchResult
		var err error
		if operation == "search" {
			hits, err = s.Search(ctx, opts)
		} else {
			hits, err = s.SearchChunks(ctx, opts)
		}
		require.NoError(t, err)
		require.Len(t, hits, wantHits)
	default:
		t.Fatalf("unknown metadata operation %q", operation)
	}
}

// Synthetic metadata workload: 10,000 sessions, 100 equally sized projects,
// one line and one chunk in each FTS layer per session. Both variants select
// the same 100 sessions. Setup/encryption/index writes are outside timing.
// Run with -run '^$' -bench '^BenchmarkSessionProjectMetadata$' -benchmem -count=6.
func BenchmarkSessionProjectMetadata(b *testing.B) {
	for _, overrides := range []bool{false, true} {
		b.Run(fmt.Sprintf("overrides=%t", overrides), func(b *testing.B) {
			s := projectMetadataFixture(b, 10000, overrides)
			ctx := b.Context()
			for _, operation := range []string{"list", "stats", "exists", "absent", "search", "chunks"} {
				b.Run(operation, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						checkProjectMetadataRead(b, s, ctx, operation, 10000)
					}
				})
			}
		})
	}
}

func projectMetadataFixture(t testing.TB, count int, overrides bool) *VaultStore {
	t.Helper()
	t.Setenv(vaultKeyEnv, testVaultKey)
	s := NewVaultStore(filepath.Join(t.TempDir(), "vault.db"))
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := t.Context()
	for start := 0; start < count; start += 100 {
		var writes []SessionWrite
		for i := start; i < min(start+100, count); i++ {
			uuid := fmt.Sprintf("%08d-1010-4000-8000-000000000003", i)
			group := fmt.Sprintf("group-%03d", i%100)
			rec := &SessionRecord{
				Session: Session{
					UUID: uuid, Title: "Metadata benchmark", ProjectPath: "/physical/" + group,
					ClaudeProjectDir: "-physical-" + group, MachineID: "benchmark",
					StartTime: time.Unix(1700000000, 0), EndTime: time.Unix(1700000000+int64(i), 0),
					MessageCount: 1, IndexVersion: currentIndexVersion,
					RawJSONL: []byte("synthetic archive\n"), SizeBytes: 18, ContentHash: uuid,
				},
				FTS:    []FTSRow{{SessionUUID: uuid, Role: "user", ContentText: "brontosaurus metadata measurement"}},
				Chunks: []Chunk{{Title: "Metadata benchmark", ContentText: "brontosaurus metadata measurement"}},
			}
			write := SessionWrite{Record: rec}
			if overrides {
				write.Project = &SessionProject{CustomProject: &group, UpdatedAtNS: 1, MachineID: "benchmark"}
			}
			writes = append(writes, write)
		}
		require.NoError(t, s.WriteBatch(ctx, writes))
	}
	return s
}

func TestSessionProject_ReadsDoNotDecodeBlobs(t *testing.T) {
	s := projectMetadataFixture(t, 100, true)
	db, err := s.getDB(t.Context())
	require.NoError(t, err)
	// These bytes cannot be decoded as zstd. Every query below must still work,
	// proving that none reads an archived blob just to resolve project metadata.
	_, err = db.ExecContext(t.Context(), `UPDATE vault_sessions SET raw_jsonl = X'00', encoding = 'zstd'`)
	require.NoError(t, err)
	for _, operation := range []string{"list", "stats", "exists", "absent", "search", "chunks"} {
		t.Run(operation, func(t *testing.T) {
			checkProjectMetadataRead(t, s, t.Context(), operation, 100)
		})
	}
	_, err = s.GetSession(t.Context(), "00000099-1010-4000-8000-000000000003")
	require.ErrorContains(t, err, "decoding raw_jsonl", "the control must actually exercise an invalid blob")
}
