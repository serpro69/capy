package vault

import (
	"context"
	"log/slog"
)

// ReindexResult aggregates a reindex run.
type ReindexResult struct {
	Reindexed int // sessions whose FTS was rebuilt and version bumped
	Errors    int // sessions that failed to re-scan or write (logged, not fatal)
}

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

	for _, uuid := range uuids {
		if ctx.Err() != nil {
			slog.Debug("vault reindex: cancelled before completion",
				"reindexed", res.Reindexed, "errors", res.Errors)
			break
		}

		fts, err := rebuildSessionFTS(store, uuid)
		if err != nil {
			slog.Warn("vault reindex: re-scan failed, skipping", "uuid", uuid, "error", err)
			res.Errors++
			continue
		}
		if err := store.UpdateSessionFTS(uuid, currentIndexVersion, fts); err != nil {
			slog.Warn("vault reindex: write failed, skipping", "uuid", uuid, "error", err)
			res.Errors++
			continue
		}
		res.Reindexed++
	}
	return res, nil
}

// rebuildSessionFTS loads a session's stored transcript + subagent sidecars from
// the DB and re-scans them into FTS rows with the current indexer.
func rebuildSessionFTS(store *VaultStore, uuid string) ([]FTSRow, error) {
	sess, err := store.GetSession(uuid)
	if err != nil {
		return nil, err
	}
	files, err := store.GetFiles(uuid)
	if err != nil {
		return nil, err
	}
	_, fts, err := scanSessionAndSubagents(uuid, sess.RawJSONL, files)
	if err != nil {
		return nil, err
	}
	return fts, nil
}
