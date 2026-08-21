package vault

import (
	"context"
	"log/slog"
)

// ReindexResult aggregates a reindex run. Reindexed + Errors can be fewer than the
// outdated-session count: a session deleted between selection and rebuild is
// silently skipped (neither reindexed nor an error).
type ReindexResult struct {
	Reindexed int // sessions whose FTS was rebuilt and version bumped
	Errors    int // sessions that failed to re-scan or write (logged, not fatal)
}

const (
	// reindexBatchSessions / reindexBatchBytes bound one rebuild transaction:
	// whichever limit hits first flushes the batch. Batching collapses the
	// per-session vault_fts delete (a full scan — session_uuid is UNINDEXED) into
	// one IN-scan per batch and keeps a batch's FTS rows (held in memory until
	// flush) and the WAL bounded. Sessions, not the IN-clause param count, is the
	// real cap — kept well under SQLite's bind-parameter limit.
	reindexBatchSessions = 50
	reindexBatchBytes    = 32 * 1024 * 1024
)

// Reindex rebuilds the FTS index for every session whose index_version is below
// currentIndexVersion, reading raw_jsonl + sidecars from the DB (NOT from disk —
// this is what upgrades sessions that were archived and later deleted locally,
// which a plain `import` can never reach). Each session is re-scanned with the
// current indexer and written via UpdateSessionFTS, which bumps its version
// without rewriting any blob.
//
// ctx provides cooperative cancellation at the session boundary so a cancelled or
// timed-out caller stops cleanly with partial progress preserved (each session is
// committed independently). Per-session failures are logged and counted without
// aborting the run, so Reindex returns an error only when the initial outdated-set
// query fails.
func Reindex(ctx context.Context, store *VaultStore) (ReindexResult, error) {
	var res ReindexResult

	uuids, err := store.OutdatedSessionUUIDs(ctx, currentIndexVersion)
	if err != nil {
		// A cancelled context surfaces here as a query error; treat it as a clean
		// cooperative stop (matching Import), not a failure.
		if ctx.Err() != nil {
			slog.Debug("vault reindex: cancelled before the outdated-set query completed")
			return res, nil
		}
		return res, err
	}

	var (
		batch      []FTSRebuild
		batchBytes int64
	)
	// flush rebuilds the accumulated batch in one transaction, then truncates the
	// WAL so a long run does not accrete it. A batch write failure is not fatal to
	// the run: the batch's sessions are counted as errors and left at their old
	// version for a later retry (each batch commits independently, so completed
	// sessions stay upgraded). Sessions that vanished mid-batch (rebuilt < len)
	// are silently skipped, not errors.
	flush := func() {
		if len(batch) == 0 {
			return
		}
		rebuilt, err := store.RebuildFTSBatch(ctx, batch)
		if err != nil {
			slog.Warn("vault reindex: batch write failed, skipping",
				"count", len(batch), "error", err)
			res.Errors += len(batch)
		} else {
			res.Reindexed += rebuilt
			if err := store.checkpointWAL(ctx); err != nil {
				slog.Warn("vault reindex: wal checkpoint failed (continuing)", "error", err)
			}
		}
		batch, batchBytes = nil, 0
	}

	for _, uuid := range uuids {
		if ctx.Err() != nil {
			slog.Debug("vault reindex: cancelled before completion",
				"reindexed", res.Reindexed, "errors", res.Errors)
			break
		}

		fts, chunks, err := rebuildSessionFTS(ctx, store, uuid)
		if err != nil {
			slog.Warn("vault reindex: re-scan failed, skipping", "uuid", uuid, "error", err)
			res.Errors++
			continue
		}
		batch = append(batch, FTSRebuild{UUID: uuid, NewVersion: currentIndexVersion, FTS: fts, Chunks: chunks})
		batchBytes += ftsContentBytes(fts) + chunkContentBytes(chunks)
		if len(batch) >= reindexBatchSessions || batchBytes >= reindexBatchBytes {
			flush()
		}
	}
	flush()
	return res, nil
}

// ftsContentBytes sums the searchable text size of a session's FTS rows — the
// memory/WAL cost that reindexBatchBytes / maxBatchBytes bound per batch.
func ftsContentBytes(fts []FTSRow) int64 {
	var n int64
	for _, r := range fts {
		n += int64(len(r.ContentText))
	}
	return n
}

// rebuildSessionFTS loads a session's stored transcript + subagent sidecars from
// the DB and re-scans them into FTS rows + semantic chunks with the current
// indexer.
func rebuildSessionFTS(ctx context.Context, store *VaultStore, uuid string) ([]FTSRow, []Chunk, error) {
	sess, err := store.GetSession(ctx, uuid)
	if err != nil {
		return nil, nil, err
	}
	files, err := store.GetFiles(ctx, uuid)
	if err != nil {
		return nil, nil, err
	}
	_, fts, chunks, err := scanSessionAndSubagents(uuid, sess.RawJSONL, files)
	if err != nil {
		return nil, nil, err
	}
	return fts, chunks, nil
}
