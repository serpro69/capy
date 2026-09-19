package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/config"
)

// Per-session import outcomes, surfaced in ImportResult.Sessions for table output.
//
// TODO(deferred, review P3): these are untyped strings, so ImportedSession.Status
// and record()'s switch accept any string and silently fall through on a typo. A
// `type ImportStatus string` would make the switch exhaustiveness-checkable. Left
// out of the V2.4 0-msg-exclusion change because it touches every switch + caller
// type annotation across the package — a cross-cutting refactor, not this task.
const (
	StatusNew      = "new"      // a UUID not previously archived → inserted
	StatusUpdated  = "updated"  // an existing UUID whose total content grew → replaced
	StatusSkipped  = "skipped"  // unchanged (same hash) or a smaller divergent variant
	StatusExcluded = "excluded" // empty session or new session below the minimum size
	StatusError    = "error"    // read/scan/write failure; see ImportedSession.Err
)

const (
	// maxBatchSessions / maxBatchBytes bound one write transaction during a bulk
	// import — whichever limit hits first flushes the batch. Batching amortizes
	// the write-lock acquisition across many sessions (see store.WriteBatch).
	maxBatchSessions = 50
	maxBatchBytes    = 100 * 1024 * 1024
)

// ImportOptions tunes an import run.
type ImportOptions struct {
	// Project, when non-empty, restricts the import to sessions whose location
	// matches this substring: the mangled Claude project dir name (e.g.
	// "-home-user-capy", SessionFile.ProjectDir) OR the Codex project hint
	// (session_meta.cwd from discovery's first-line read, SessionFile.ProjectPath).
	// Both are known before any scan, so the filter works pre-read.
	Project string
	// DryRun computes every skip/insert/replace/updated decision without writing.
	DryRun bool
	// Platform, when non-empty, restricts the run to sessions discovered for that
	// platform ("" == every platform in the list).
	Platform Platform
	// MinSessionBytes excludes new sessions smaller than this uncompressed total
	// (main transcript + sidecars). Zero disables; callers validate non-negative.
	// Existing archives still update/reindex, and excluded files can qualify later.
	MinSessionBytes int64
}

// ImportedSession is the per-session outcome of an import run.
type ImportedSession struct {
	UUID        string
	Platform    Platform // the platform the session was discovered (or, for merge, stored) as
	Title       string   // populated for new/updated; empty for skipped (not scanned)
	ProjectPath string   // populated for new/updated; empty for skipped
	SizeBytes   int64    // total content size (main JSONL + sidecars)
	Status      string   // StatusNew | StatusUpdated | StatusSkipped | StatusExcluded | StatusError
	Reason      string   // transcript exclusion reason; may accompany a name-only merge update
	Err         error    // set only when Status == StatusError
}

// seenSession is import's in-run record of one uuid — every uuid the run has
// decided on, whatever the decision (design § Import — same-run reconciliation).
// Import decides against COMMITTED state (SessionDigest) and queues writes into
// batched transactions, so without this map two files for one uuid in the same
// run (a Codex thread present under both sessions/ and archived_sessions/)
// would both queue as inserts and the second would fail on the primary key.
type seenSession struct {
	hash string
	size int64
	// gen is the batch generation the uuid was queued in; it is pending (not yet
	// committed) while gen == the importer's current generation. A flushed write
	// is committed, so a later copy reconciles against the DB like any other.
	gen int
	// wouldWrite is set when a dry run reported the copy as new/updated — the
	// dry-run stand-in for "pending", so a later larger copy reports `updated`
	// exactly as the real run (which replaces the committed row) would.
	wouldWrite bool
}

// ImportResult aggregates an import run.
type ImportResult struct {
	Imported int
	Updated  int
	Skipped  int
	Excluded int
	Errors   int
	Sessions []ImportedSession
}

func (r *ImportResult) record(s ImportedSession) {
	switch s.Status {
	case StatusNew:
		r.Imported++
	case StatusUpdated:
		r.Updated++
	case StatusSkipped:
		r.Skipped++
	case StatusExcluded:
		r.Excluded++
	case StatusError:
		r.Errors++
	}
	r.Sessions = append(r.Sessions, s)
}

// Import archives the given discovered sessions into store, applying idempotent
// skip/insert/replace logic per session and writing in batched transactions.
// Per-session failures are logged and recorded (StatusError) without aborting
// the run, so Import returns no error — inspect ImportResult. The caller owns
// store's lifecycle (open/Close) and supplies the session list (via
// DiscoverSessions).
//
// ctx provides cooperative cancellation: it is checked at each session boundary
// so a cancelled or timed-out caller (e.g. the server-startup sweep, which must
// not block shutdown) stops processing further sessions, flushes whatever is
// already batched, and returns partial results. ctx is also threaded into the
// store's DB calls (Task 4), so a cancellation can additionally interrupt an
// in-flight query/transaction rather than only being observed at the next
// session boundary.
func Import(ctx context.Context, store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult {
	var res ImportResult
	if len(sessions) == 0 {
		// Nothing to import — skip the machine-mismatch probe so an empty run
		// doesn't open the DB just to warn about a vault it isn't touching.
		return res
	}
	machineID := MachineID()

	warnOnMachineMismatch(ctx, store, machineID)

	var (
		batch      []SessionWrite
		pending    []ImportedSession // aligned with batch; finalized after flush
		batchBytes int64

		// A version-stale but hash-identical session needs only its FTS rebuilt,
		// not a full blob rewrite (ADR-025 D4) — accumulated separately and written
		// via RebuildFTSBatch, which also collapses the per-session vault_fts delete
		// into one IN-scan per batch.
		ftsBatch      []FTSRebuild
		ftsPending    []ImportedSession // aligned with ftsBatch
		ftsBatchBytes int64

		// batchGen counts flushes; a seenSession whose gen equals it is still
		// pending in batch/ftsBatch (see the same-run reconciliation below).
		batchGen int
	)

	flush := func() {
		if len(batch) == 0 && len(ftsBatch) == 0 {
			return
		}
		if !opts.DryRun {
			if len(batch) > 0 {
				if err := store.WriteBatch(ctx, batch); err != nil {
					slog.Warn("vault import: batch write failed, retrying per-session",
						"count", len(batch), "error", err)
					for i := range batch {
						if err := store.writeOne(ctx, batch[i]); err != nil {
							slog.Warn("vault import: session write failed",
								"uuid", batch[i].Record.Session.UUID, "error", err)
							pending[i].Status = StatusError
							pending[i].Err = err
						}
					}
				}
			}
			if len(ftsBatch) > 0 {
				if _, err := store.RebuildFTSBatch(ctx, ftsBatch); err != nil {
					slog.Warn("vault import: fts upgrade batch failed, retrying per-session",
						"count", len(ftsBatch), "error", err)
					for i := range ftsBatch {
						if _, err := store.RebuildFTSBatch(ctx, ftsBatch[i:i+1]); err != nil {
							slog.Warn("vault import: session fts upgrade failed",
								"uuid", ftsBatch[i].UUID, "error", err)
							ftsPending[i].Status = StatusError
							ftsPending[i].Err = err
						}
					}
				}
			}
		}
		// On success each session keeps its pre-assigned status (StatusNew/StatusUpdated);
		// only a failed per-session retry above mutates it to StatusError.
		for _, p := range pending {
			res.record(p)
		}
		for _, p := range ftsPending {
			res.record(p)
		}
		batch, pending, batchBytes = nil, nil, 0
		ftsBatch, ftsPending, ftsBatchBytes = nil, nil, 0
		// Everything queued so far is committed: a later copy of any of those uuids
		// must reconcile against the DB, not against a pending write (seenSession).
		batchGen++
	}

	// seen is the in-run reconciliation map (seenSession). It records EVERY uuid
	// the loop decides on — skips included — and is consulted before SessionDigest.
	seen := make(map[string]seenSession, len(sessions))

	cancelled := false
	for i := range sessions {
		// Cooperative cancellation: stop at the next session boundary when the
		// caller's context is done. The final flush below still persists the
		// batch accumulated so far, so partial progress is not discarded.
		if ctx.Err() != nil {
			cancelled = true
			break
		}

		sf := &sessions[i]
		platform := importPlatform(sf)

		if opts.Platform != "" && platform != opts.Platform {
			continue // another platform — not part of this run
		}
		if opts.Project != "" && !strings.Contains(sf.ProjectDir, opts.Project) && !strings.Contains(sf.ProjectPath, opts.Project) {
			continue // filtered out — not part of this run
		}
		// Every outcome below carries the platform, so the CLI can group its
		// summary per platform without a second lookup.
		outcome := func(status string, size int64) ImportedSession {
			return ImportedSession{UUID: sf.UUID, Platform: platform, SizeBytes: size, Status: status}
		}
		fail := func(size int64, err error) {
			e := outcome(StatusError, size)
			e.Err = err
			res.record(e)
		}

		mainBytes, err := os.ReadFile(sf.Path)
		if err != nil {
			slog.Warn("vault import: cannot read session file", "platform", platform, "path", sf.Path, "error", err)
			fail(0, err)
			continue
		}
		if sf.Compressed {
			// A .jsonl.zst rollout is decompressed FIRST: raw_jsonl, content_hash,
			// size_bytes and every FTS row are always the plain JSONL bytes (vault v2
			// invariant), so an archived copy hashes equal to its compressed twin.
			mainBytes, err = decodeBlob(encodingZstd, mainBytes)
			if err != nil {
				slog.Warn("vault import: cannot decompress session file", "platform", platform, "path", sf.Path, "error", err)
				fail(0, err)
				continue
			}
		}

		files, contents := readSidecars(sf, mainBytes)
		hash, size := computeContentHash(contents)

		// Same-run reconciliation (design § Import): a uuid this run has already
		// decided on is a duplicate copy of the same thread (Codex archive is a
		// move — sessions/ is walked first, so the active copy wins). A same-hash
		// or smaller later copy is skipped without touching anything — in
		// particular it never reaches the location policy below, which is therefore
		// first-sighting-only by construction. A LARGER, divergent later copy must
		// replace the earlier one: when that earlier copy is still pending in the
		// current batch it is flushed first, so the ordinary DB path below sees the
		// committed row and replaces it (one extra transaction on a rare path beats
		// surgery inside the batch slices). A dry run has no batch; wouldWrite
		// stands in for "pending" so it reports the same `updated`.
		prev, dup := seen[sf.UUID]
		if dup {
			if hash == prev.hash || size < prev.size {
				res.record(outcome(StatusSkipped, size))
				continue
			}
			if prev.gen == batchGen && !opts.DryRun {
				flush()
			}
		}
		// Record this sighting now (whatever the decision turns out to be) so a
		// third copy reconciles against it; gen/wouldWrite are refined below.
		seen[sf.UUID] = seenSession{hash: hash, size: size, gen: -1}

		existingHash, existingSize, existingIndexVersion, existingHint, found, err := store.SessionDigest(ctx, sf.UUID)
		if err != nil {
			slog.Warn("vault import: digest lookup failed", "platform", platform, "uuid", sf.UUID, "error", err)
			fail(size, err)
			continue
		}

		// Size filtering is an admission rule, so it must follow the digest lookup
		// and leave existing archives (including stale indexes and moves) alone.
		if !found && size < opts.MinSessionBytes {
			e := outcome(StatusExcluded, size)
			e.ProjectPath = resolveProjectPath(sf.ProjectPath, sf.ProjectDir)
			e.Reason = minimumSizeReason(size, opts.MinSessionBytes)
			res.record(e)
			continue
		}

		// Location policy (design § Import — Codex only): Codex archive/unarchive
		// MOVES a rollout, so the same bytes can reappear at a new relative path.
		// A same-hash Codex file whose relative path differs from the stored hint
		// moves the hint (metadata-only, its own tx, at decision time) and reports
		// `updated`, so restore follows where Codex last kept the file. Claude
		// rows never enter the branch: their hint is the mangled project dir and
		// Claude Code never moves a session file.
		hint := locationHint(sf)
		moved := found && hash == existingHash && platform == PlatformCodex && hint != existingHint
		if moved && codexRolloutPresent(sf.Root, existingHint) {
			// Not a move: the file at the stored hint is still on disk, so this
			// is a duplicate copy (sessions/ and archived_sessions/ both holding
			// the thread). The in-run map above catches that pair only when the
			// run sees both copies; the server sweep's skip predicate drops the
			// archived-at-its-path copy UNOPENED, so the other copy would arrive
			// here as the run's first sighting and flip the hint — and the next
			// start would flip it back (one write per startup, a restore target
			// that alternates). The hint follows the file only once the old
			// location is empty; a lingering duplicate is `skipped`, real and dry
			// run alike.
			slog.Debug("vault import: duplicate codex copy, stored location still present",
				"uuid", sf.UUID, "stored", existingHint, "copy", hint)
			res.record(outcome(StatusSkipped, size))
			continue
		}
		if moved && !opts.DryRun {
			if err := store.UpdateLocationHint(ctx, sf.UUID, hint); err != nil {
				slog.Warn("vault import: location hint update failed", "platform", platform, "uuid", sf.UUID, "error", err)
				fail(size, err)
				continue
			}
		}

		// Idempotency (design §Idempotent Import Logic) + reindex gate:
		//   - Skip only when content is unchanged (same hash) AND the stored FTS
		//     index is already current — unless the file moved (above), which is
		//     an `updated` with nothing to scan.
		//   - A hash-identical but version-stale session has its FTS rebuilt only
		//     (ftsOnly) — the blob is byte-identical, so a full ReplaceSession would
		//     rewrite raw_jsonl + every sidecar for zero benefit (the write
		//     amplification ADR-025 D4 avoids). A moved ftsOnly file has already
		//     had its hint updated above and reports `updated` once.
		//   - A smaller divergent variant (likely a compacted copy) never overwrites
		//     the fuller archive, regardless of version — `capy vault reindex`
		//     upgrades those from the stored blob instead.
		//   - A different-hash variant of equal-or-larger total size replaces in place.
		replace := false
		ftsOnly := false
		if found {
			switch {
			case hash == existingHash && existingIndexVersion >= currentIndexVersion:
				if !moved {
					res.record(outcome(StatusSkipped, size))
					continue
				}
				e := outcome(StatusUpdated, size)
				e.ProjectPath = sf.ProjectPath // the discovery hint; the row is not rescanned
				res.record(e)
				continue
			case hash == existingHash:
				ftsOnly = true // unchanged content, stale index → rebuild FTS only
			case size < existingSize:
				res.record(outcome(StatusSkipped, size))
				continue
			default:
				replace = true
			}
		}

		rec, err := buildRecord(sf, mainBytes, files, hash, size, machineID)
		if err != nil {
			slog.Warn("vault import: scan failed", "platform", platform, "uuid", sf.UUID, "error", err)
			fail(size, err)
			continue
		}

		// Exclude empty sessions: a transcript with no human-text or assistant
		// turns (MessageCount == 0 — e.g. a freshly-created file carrying only
		// tool_result noise and an ai-title, or a Codex rollout aborted at startup)
		// has no archival value and would only clutter `list`. Skip without
		// batching. MessageCount is only known after buildRecord scans, so this
		// gate must follow it. A later import, once the session gains messages,
		// archives it normally (it was never written, so it reappears as
		// StatusNew). This must precede the DryRun branch so a dry run reports the
		// same exclusion a real run would.
		if rec.Session.MessageCount == 0 {
			e := outcome(StatusExcluded, size)
			e.Reason = "no messages"
			e.Title, e.ProjectPath = rec.Session.Title, rec.Session.ProjectPath
			res.record(e)
			continue
		}

		status := StatusNew
		if replace || ftsOnly {
			status = StatusUpdated
		}
		entry := outcome(status, size)
		entry.Title, entry.ProjectPath = rec.Session.Title, rec.Session.ProjectPath

		if opts.DryRun {
			if dup && prev.wouldWrite && !found {
				// The real run would have committed the earlier copy and replaced it.
				entry.Status = StatusUpdated
			}
			seen[sf.UUID] = seenSession{hash: hash, size: size, gen: -1, wouldWrite: true}
			res.record(entry)
			continue
		}
		seen[sf.UUID] = seenSession{hash: hash, size: size, gen: batchGen}

		if ftsOnly {
			ftsBatch = append(ftsBatch, FTSRebuild{
				UUID: sf.UUID, NewVersion: currentIndexVersion, FTS: rec.FTS, Chunks: rec.Chunks,
			})
			ftsPending = append(ftsPending, entry)
			ftsBatchBytes += ftsContentBytes(rec.FTS) + chunkContentBytes(rec.Chunks)
		} else {
			batch = append(batch, SessionWrite{Record: rec, Replace: replace})
			pending = append(pending, entry)
			batchBytes += size
		}
		if len(batch) >= maxBatchSessions || batchBytes >= maxBatchBytes ||
			len(ftsBatch) >= maxBatchSessions || ftsBatchBytes >= maxBatchBytes {
			flush()
		}
	}
	flush()

	if cancelled {
		// Debug, not Info: a cooperative stop on server shutdown is the designed
		// success path, not an operator-noteworthy event.
		slog.Debug("vault import: cancelled before completion",
			"imported", res.Imported, "updated", res.Updated,
			"skipped", res.Skipped, "excluded", res.Excluded, "errors", res.Errors)
	}
	return res
}

// minimumSizeReason is shared by disk import and cross-vault merge reporting.
func minimumSizeReason(size, minimum int64) string {
	return fmt.Sprintf("below minimum size (%d < %d bytes)", size, minimum)
}

// warnOnMachineMismatch prints a prominent warning when the vault already holds
// sessions but none were archived by this machine — the signal that copying a
// vault.db here is about to bury unarchived local sessions.
func warnOnMachineMismatch(ctx context.Context, store *VaultStore, machineID string) {
	total, matching, err := store.MachineSummary(ctx, machineID)
	if err != nil {
		slog.Warn("vault import: machine summary query failed", "error", err)
		return
	}
	if total > 0 && matching == 0 {
		slog.Warn("vault import: this vault.db contains sessions from other machine(s) only; "+
			"your local sessions are not yet archived — consider running import before replacing this file",
			"current_machine", machineID, "session_count", total)
	}
}

// readSidecars reads every associated file for sf, returning the File rows for
// vault_files and a content map (keyed by hash key) covering the main JSONL plus
// all readable sidecars. A sidecar that cannot be read is logged and dropped —
// the main JSONL is the critical artifact and must not be lost over a sidecar.
func readSidecars(sf *SessionFile, mainBytes []byte) (files []File, contents map[string][]byte) {
	contents = map[string][]byte{sf.UUID + ".jsonl": mainBytes}
	for _, af := range sf.AssociatedFiles {
		b, err := os.ReadFile(af.AbsPath)
		if err != nil {
			slog.Warn("vault import: cannot read sidecar, skipping", "path", af.AbsPath, "error", err)
			continue
		}
		contents[af.RelativePath] = b
		files = append(files, File{RelativePath: af.RelativePath, RawContent: b})
	}
	return files, contents
}

// importPlatform is the platform import treats sf as: SessionFile.Platform, which
// every discoverer stamps, or PlatformClaudeCode for an empty value — a
// hand-built SessionFile (tests, callers that predate the field) is a Claude
// session (Platform.OrClaude, mirroring the store's write contract).
func importPlatform(sf *SessionFile) Platform {
	return sf.Platform.OrClaude()
}

// locationHint is the value import stores in claude_project_dir (see
// Session.ClaudeProjectDir): the relative rollout path for Codex, the mangled
// project dir for everything else.
func locationHint(sf *SessionFile) string {
	if importPlatform(sf) == PlatformCodex {
		return sf.RelativePath
	}
	return sf.ProjectDir
}

// codexRolloutPresent reports whether a rollout still exists at the stored
// location hint rel (slash-separated, .zst-stripped) under the Codex home root
// — as the plain file or its compressed twin. It is the location policy's
// "was this a move or a duplicate?" test: true means the old copy is still
// there, so a same-hash file elsewhere must not steal the hint. An empty root
// (a SessionFile built without the discoverer — older callers, hand-built
// tests) cannot be checked and reads as "not present", which keeps the
// pre-check behavior of following the file.
func codexRolloutPresent(root, rel string) bool {
	if root == "" || rel == "" {
		return false
	}
	base := filepath.Join(root, filepath.FromSlash(rel))
	for _, p := range [...]string{base, base + ".zst"} {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// buildRecord scans the main JSONL (with the platform's decoder) and any
// subagent transcripts into FTS rows + chunks and assembles the full
// SessionRecord for one insert/replace.
//
// Row identity is the filename uuid (design § Import): the platform's own id
// recorded INSIDE the file (ScanOutput.PlatformID — Codex session_meta.id) is
// informational. When it is present and differs, that is logged as a warning
// naming both and the filename uuid stays the row key — it is what Codex's own
// DB-less resume scans for, and it was known before any byte was read.
func buildRecord(sf *SessionFile, mainBytes []byte, files []File, hash string, size int64, machineID string) (*SessionRecord, error) {
	platform := importPlatform(sf)
	scanOut, fts, chunks, err := scanSessionAndSubagents(sf.UUID, platform, sf.Path, mainBytes, files)
	if err != nil {
		return nil, err
	}
	if scanOut.PlatformID != "" && scanOut.PlatformID != sf.UUID {
		slog.Warn("vault import: session id recorded inside the file differs from its filename; the filename uuid is the row key",
			"platform", platform, "uuid", sf.UUID, "platform_id", scanOut.PlatformID, "path", sf.Path)
	}

	// Platform and ParentUUID come from the single decode (ScanOutput copies them
	// from Meta): Platform is the decoder that ran — DecoderFor(platform) — and
	// ParentUUID is the parent of a Codex child rollout (empty for Claude, whose
	// sidecar sub-agents are not sessions). ProjectPath prefers the transcript's
	// own cwd, then discovery's first-line hint (Codex), then the Claude unmangle
	// fallback chain.
	cwd := scanOut.CWD
	if cwd == "" {
		cwd = sf.ProjectPath
	}
	sess := Session{
		UUID:             sf.UUID,
		Title:            scanOut.Title,
		StartTime:        scanOut.StartTime,
		EndTime:          scanOut.EndTime,
		MessageCount:     scanOut.MessageCount,
		SizeBytes:        size,
		ContentHash:      hash,
		MachineID:        machineID,
		ClaudeProjectDir: locationHint(sf),
		ProjectPath:      resolveProjectPath(cwd, sf.ProjectDir),
		GitBranch:        scanOut.Branch,
		IndexVersion:     currentIndexVersion,
		Platform:         scanOut.Platform,
		ParentUUID:       scanOut.ParentUUID,
		RawJSONL:         mainBytes,
	}
	return &SessionRecord{Session: sess, Files: files, FTS: fts, Chunks: chunks}, nil
}

// scanSessionAndSubagents scans a session's main transcript and its subagent
// sidecars into FTS rows AND semantic chunks with the current indexer,
// returning the main ScanOutput (for session metadata) alongside the combined
// rows. Subagent transcripts are scanned too so their content is searchable
// and their results carry the subagent_id anchor the TUI uses to open them;
// their chunks append after the main transcript's, in sidecar order. Chunks
// derive from the SAME scan as the per-line rows (design §D3), so the two
// indexes can never disagree about what exists; ChunkIndex is stamped
// session-wide here. Shared by import (buildRecord, which uses the metadata),
// reindex, and merge (which use only the rows + chunks). platform selects the
// decoder for the main transcript (DecoderFor); sidecars are always Claude.
//
// path is the on-disk location of the main transcript when the caller has one
// (import) and "" when it decodes a stored BLOB (reindex, merge). It only feeds
// the decoders' skip warnings (withSource, logged under "file"): a malformed
// line is reported against the file path when known, else against
// "vault:<uuid>" — the label says where the bytes came from, so the user can
// find the offending file (or vault row) without a second import run.
func scanSessionAndSubagents(uuid string, platform Platform, path string, mainBytes []byte, files []File) (*ScanOutput, []FTSRow, []Chunk, error) {
	source := path
	if source == "" {
		source = "vault:" + uuid
	}
	scanOut, err := ScanSession(platform, withSource(source, bytes.NewReader(mainBytes)))
	if err != nil {
		return nil, nil, nil, err
	}

	fts := make([]FTSRow, 0, len(scanOut.Results))
	for _, r := range scanOut.Results {
		fts = append(fts, ftsRow(uuid, r))
	}
	chunks := chunkScanResults(scanOut.Results, scanOut.StartTime, "")

	for _, f := range files {
		id := subagentID(f.RelativePath)
		if id == "" {
			continue
		}
		results, serr := ScanSubagent(withSource(source+" sidecar "+f.RelativePath, bytes.NewReader(f.RawContent)), id)
		if serr != nil {
			slog.Warn("vault: subagent scan failed, skipping",
				"uuid", uuid, "subagent", id, "error", serr)
			continue
		}
		for _, r := range results {
			fts = append(fts, ftsRow(uuid, r))
		}
		// Zero start time: a subagent scan's ScanOutput is discarded, so the
		// chunker falls back to the first result's timestamp for the title.
		chunks = append(chunks, chunkScanResults(results, time.Time{}, id)...)
	}
	for i := range chunks {
		chunks[i].ChunkIndex = i
	}
	return scanOut, fts, chunks, nil
}

// resolveProjectPath picks the best-known real project path: the JSONL cwd when
// present, else the filesystem-probed unmangling of the project dir, else the
// raw mangled name as a last resort.
func resolveProjectPath(cwd, projectDir string) string {
	if cwd != "" {
		return cwd
	}
	if p := config.UnmanglePath(projectDir); p != "" {
		return p
	}
	return projectDir
}

func ftsRow(uuid string, r ScanResult) FTSRow {
	return FTSRow{
		SessionUUID:  uuid,
		SubagentID:   r.SubagentID,
		TurnIndex:    r.TurnIndex,
		MessageIndex: r.MessageIndex,
		LineIndex:    r.LineIndex,
		Role:         r.Role,
		ContentText:  r.ContentText,
	}
}

// subagentID extracts the agent id from a "subagents/agent-<id>.jsonl" relative
// path so the viewer can reconstruct the filename. Returns "" for any other path.
func subagentID(rel string) string {
	if !isSubagentJSONL(rel) {
		return ""
	}
	base := strings.TrimPrefix(rel, "subagents/")
	if !strings.HasPrefix(base, "agent-") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
}

// computeContentHash hashes a file set with length-prefix framing so that
// boundary-equivalent sets cannot collide: for each key (sorted), it writes
// len(key)||key||len(content)||content into SHA-256. It returns the hex digest
// and the total content size — the replace tiebreaker, which must cover the same
// byte set as the hash (main JSONL + every sidecar), not the main file alone.
func computeContentHash(contents map[string][]byte) (string, int64) {
	keys := make([]string, 0, len(contents))
	for k := range contents {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	var lenBuf [8]byte
	var size int64
	for _, k := range keys {
		c := contents[k]
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(k)))
		h.Write(lenBuf[:])
		h.Write([]byte(k))
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(c)))
		h.Write(lenBuf[:])
		h.Write(c)
		size += int64(len(c))
	}
	return hex.EncodeToString(h.Sum(nil)), size
}
