package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/serpro69/capy/internal/config"
)

// Per-session import outcomes, surfaced in ImportResult.Sessions for table output.
const (
	StatusNew     = "new"     // a UUID not previously archived → inserted
	StatusUpdated = "updated" // an existing UUID whose total content grew → replaced
	StatusSkipped = "skipped" // unchanged (same hash) or a smaller divergent variant
	StatusError   = "error"   // read/scan/write failure; see ImportedSession.Err
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
	// Project, when non-empty, restricts the import to sessions whose mangled
	// project directory name contains this substring. The match is against the
	// Claude project dir name (e.g. "-home-user-capy"), not the resolved
	// project_path, so it needs no scan and works pre-filter.
	Project string
	// DryRun computes every skip/insert/replace decision without writing.
	DryRun bool
}

// ImportedSession is the per-session outcome of an import run.
type ImportedSession struct {
	UUID        string
	Title       string // populated for new/updated; empty for skipped (not scanned)
	ProjectPath string // populated for new/updated; empty for skipped
	SizeBytes   int64  // total content size (main JSONL + sidecars)
	Status      string // StatusNew | StatusUpdated | StatusSkipped | StatusError
	Err         error  // set only when Status == StatusError
}

// ImportResult aggregates an import run.
type ImportResult struct {
	Imported int
	Updated  int
	Skipped  int
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
func Import(store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult {
	var res ImportResult
	if len(sessions) == 0 {
		// Nothing to import — skip the machine-mismatch probe so an empty run
		// doesn't open the DB just to warn about a vault it isn't touching.
		return res
	}
	machineID := MachineID()

	warnOnMachineMismatch(store, machineID)

	var (
		batch      []SessionWrite
		pending    []ImportedSession // aligned with batch; finalized after flush
		batchBytes int64
	)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if !opts.DryRun {
			if err := store.WriteBatch(batch); err != nil {
				slog.Warn("vault import: batch write failed, retrying per-session",
					"count", len(batch), "error", err)
				for i := range batch {
					if err := store.writeOne(batch[i]); err != nil {
						slog.Warn("vault import: session write failed",
							"uuid", batch[i].Record.Session.UUID, "error", err)
						pending[i].Status = StatusError
						pending[i].Err = err
					}
				}
			}
		}
		for _, p := range pending {
			res.record(p)
		}
		batch, pending, batchBytes = nil, nil, 0
	}

	for i := range sessions {
		sf := &sessions[i]

		if opts.Project != "" && !strings.Contains(sf.ProjectDir, opts.Project) {
			continue // filtered out — not part of this run
		}

		mainBytes, err := os.ReadFile(sf.Path)
		if err != nil {
			slog.Warn("vault import: cannot read session file", "path", sf.Path, "error", err)
			res.record(ImportedSession{UUID: sf.UUID, Status: StatusError, Err: err})
			continue
		}

		files, contents := readSidecars(sf, mainBytes)
		hash, size := computeContentHash(contents)

		existingHash, existingSize, found, err := store.SessionDigest(sf.UUID)
		if err != nil {
			slog.Warn("vault import: digest lookup failed", "uuid", sf.UUID, "error", err)
			res.record(ImportedSession{UUID: sf.UUID, SizeBytes: size, Status: StatusError, Err: err})
			continue
		}

		// Idempotency (design §Idempotent Import Logic): skip unchanged sessions
		// (same hash) and smaller divergent variants (likely a compacted copy —
		// never overwrite the fuller archive). A different-hash variant of
		// equal-or-larger total size replaces in place (incoming size >= existing
		// → replace; equal size + different content counts as larger-or-equal).
		replace := false
		if found {
			if hash == existingHash || size < existingSize {
				res.record(ImportedSession{UUID: sf.UUID, SizeBytes: size, Status: StatusSkipped})
				continue
			}
			replace = true
		}

		rec, err := buildRecord(sf, mainBytes, files, hash, size, machineID)
		if err != nil {
			slog.Warn("vault import: scan failed", "uuid", sf.UUID, "error", err)
			res.record(ImportedSession{UUID: sf.UUID, SizeBytes: size, Status: StatusError, Err: err})
			continue
		}

		status := StatusNew
		if replace {
			status = StatusUpdated
		}
		entry := ImportedSession{
			UUID:        sf.UUID,
			Title:       rec.Session.Title,
			ProjectPath: rec.Session.ProjectPath,
			SizeBytes:   size,
			Status:      status,
		}

		if opts.DryRun {
			res.record(entry)
			continue
		}

		batch = append(batch, SessionWrite{Record: rec, Replace: replace})
		pending = append(pending, entry)
		batchBytes += size
		if len(batch) >= maxBatchSessions || batchBytes >= maxBatchBytes {
			flush()
		}
	}
	flush()

	return res
}

// warnOnMachineMismatch prints a prominent warning when the vault already holds
// sessions but none were archived by this machine — the signal that copying a
// vault.db here is about to bury unarchived local sessions.
func warnOnMachineMismatch(store *VaultStore, machineID string) {
	total, matching, err := store.MachineSummary(machineID)
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

// buildRecord scans the main JSONL and any subagent transcripts into FTS rows
// and assembles the full SessionRecord for one insert/replace.
func buildRecord(sf *SessionFile, mainBytes []byte, files []File, hash string, size int64, machineID string) (*SessionRecord, error) {
	scanOut, err := ScanSession(bytes.NewReader(mainBytes))
	if err != nil {
		return nil, err
	}

	fts := make([]FTSRow, 0, len(scanOut.Results))
	for _, r := range scanOut.Results {
		fts = append(fts, ftsRow(sf.UUID, r))
	}

	// Subagent transcripts are scanned too, so their content is searchable and
	// their results carry the subagent_id anchor the TUI uses to open them.
	for _, f := range files {
		id := subagentID(f.RelativePath)
		if id == "" {
			continue
		}
		results, serr := ScanSubagent(bytes.NewReader(f.RawContent), id)
		if serr != nil {
			slog.Warn("vault import: subagent scan failed, skipping",
				"uuid", sf.UUID, "subagent", id, "error", serr)
			continue
		}
		for _, r := range results {
			fts = append(fts, ftsRow(sf.UUID, r))
		}
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
		ClaudeProjectDir: sf.ProjectDir,
		ProjectPath:      resolveProjectPath(scanOut.CWD, sf.ProjectDir),
		GitBranch:        scanOut.Branch,
		RawJSONL:         mainBytes,
	}
	return &SessionRecord{Session: sess, Files: files, FTS: fts}, nil
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
