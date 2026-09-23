package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// MergeOptions tunes a merge run.
type MergeOptions struct {
	// Project, when non-empty, restricts the merge to source sessions whose
	// effective project contains this literal substring (ASCII case-insensitive).
	// Unlike disk import, location hints are not project aliases. A legacy source
	// without project state falls back to its imported project_path.
	Project string
	// MinSessionBytes has the same admission-only semantics as ImportOptions.
	MinSessionBytes int64
	// DryRun computes every skip/insert/replace decision without writing.
	DryRun bool
}

// MergeFrom imports the sessions of the source vault at srcPath (opened with
// srcKey) into dest, non-destructively — the cross-machine union v1's destructive
// replace could not do. It reuses the disk-import idempotent decision per session
// (dest.SessionDigest → skip a same-hash or smaller variant, else insert/replace),
// carries the source row's metadata verbatim, and rebuilds FTS with the
// destination's CURRENT indexer (re-scanning the decoded blobs rather than copying
// source FTS rows, so a v1 source's index is upgraded as a side effect — the same
// posture as `capy vault reindex`).
//
// The source is opened via sqliteutil.OpenSourceForMerge, which does NOT migrate
// it, so MergeFrom feature-detects the source schema: a v1 source predates the
// `encoding` column (migration 0001), and a blind `SELECT encoding` would raise
// "no such column" against it — absent ⇒ every source blob is treated as raw.
//
// Per-session read/scan/write failures are logged and recorded as StatusError
// without aborting the run; MergeFrom returns an error only for setup failures
// (opening or probing the source). The 0-message exclusion (Task 11) applies, so
// a source's empty shells are not carried over.
//
// Capy-owned session names (vault_session_names) reconcile INDEPENDENTLY of the
// transcript decision: for every source session whose UUID exists in the
// destination — including excluded empty shells and same-hash/smaller skips —
// the latest (renamed_at_ns, machine_id) state wins (value tie-break on equal
// tuples) and is written verbatim, so repeated merges are idempotent and all
// vaults converge. A source without the table is a supported legacy vault and
// contributes no name state; the reverse (an older binary merging from a
// current vault) silently carries none — accepted, non-destructive, and
// recoverable by re-running the merge with an upgraded binary.
// Project assignments use the same total order on an independent timestamp.
// Both metadata fields reconcile in one transaction, regardless of transcript
// selection; a pre-project source contributes no project state.
//
// Concurrency: MergeFrom writes only the destination (batched BeginImmediate), so
// a concurrent server-startup sweep on the same vault.db is absorbed by
// busy_timeout + retry, exactly like disk import — no "stop the server first"
// requirement (unlike rekey's file swap) and no busy pre-check (unlike compact's
// VACUUM). The source is a different file the server does not normally hold open.
//
// srcKeyEnv names where srcKey came from (e.g. "CAPY_VAULT_MERGE_KEY", "--key"),
// used only for the wrong-passphrase error message.
func MergeFrom(ctx context.Context, dest *VaultStore, srcPath, srcKey, srcKeyEnv string, opts MergeOptions) (ImportResult, error) {
	var res ImportResult

	srcDB, err := sqliteutil.OpenSourceForMerge(ctx, srcPath, srcKey, srcKeyEnv)
	if err != nil {
		return res, fmt.Errorf("opening source vault: %w", err)
	}
	defer srcDB.Close()

	// Refuse a source whose on-disk format is newer than this binary supports —
	// its blobs could be encoded in a way decodeBlob cannot read. A v1/unmarked
	// source carries no marker and is unconstrained. vault_meta exists since v1, so
	// this read is safe against the un-migrated source.
	if err := checkReaderVersion(ctx, srcDB); err != nil {
		return res, fmt.Errorf("source vault: %w", err)
	}

	// Feature-detect the source's encoding columns (added together by migration
	// 0001, so both-or-neither in practice — probed independently to stay robust).
	srcSessEnc, err := columnExists(ctx, srcDB, "vault_sessions", "encoding")
	if err != nil {
		return res, fmt.Errorf("probing source vault_sessions schema: %w", err)
	}
	srcFileEnc, err := columnExists(ctx, srcDB, "vault_files", "encoding")
	if err != nil {
		return res, fmt.Errorf("probing source vault_files schema: %w", err)
	}
	// Feature-detect the capy-owned name table (migration 0005) the same way: a
	// legacy source without it merges cleanly and contributes no name state.
	srcHasNames, err := tableExists(ctx, srcDB, "vault_session_names")
	if err != nil {
		return res, fmt.Errorf("probing source vault_session_names schema: %w", err)
	}
	srcHasProjects, err := tableExists(ctx, srcDB, "vault_session_projects")
	if err != nil {
		return res, fmt.Errorf("probing source vault_session_projects schema: %w", err)
	}
	// Feature-detect the multi-platform columns (migration 0006, added together).
	// An ABSENT column means a pre-0006 source: every row is Claude with no parent
	// by construction and is NEVER sniffed — 41 of 467 real Claude sessions open
	// with a file-history-snapshot line, so a first-line sniff would misjudge
	// legacy rows (design § Format Identification). A present value is carried
	// verbatim.
	srcPlatform, err := columnExists(ctx, srcDB, "vault_sessions", "platform")
	if err != nil {
		return res, fmt.Errorf("probing source vault_sessions platform column: %w", err)
	}
	srcParent, err := columnExists(ctx, srcDB, "vault_sessions", "parent_uuid")
	if err != nil {
		return res, fmt.Errorf("probing source vault_sessions parent_uuid column: %w", err)
	}
	srcCols := sourceColumns{encoding: srcSessEnc, platform: srcPlatform, parentUUID: srcParent}

	uuids, err := sourceSessionUUIDs(ctx, srcDB, opts.Project, srcHasProjects)
	if err != nil {
		return res, fmt.Errorf("listing source sessions: %w", err)
	}

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
			if err := dest.WriteBatch(ctx, batch); err != nil {
				slog.Warn("vault merge: batch write failed, retrying per-session",
					"count", len(batch), "error", err)
				for i := range batch {
					if err := dest.writeOne(ctx, batch[i]); err != nil {
						slog.Warn("vault merge: session write failed",
							"uuid", batch[i].Record.Session.UUID, "error", err)
						pending[i].Status = StatusError
						pending[i].Err = err
					}
				}
			}
		}
		// Successful writes keep StatusNew/Updated unless the committed metadata
		// cannot be read for reporting; every write/read failure stays visible.
		for _, p := range pending {
			if p.Status != StatusError {
				// Report committed winners, including a local edit that landed
				// after the prospective snapshot used to queue this write.
				sess, err := dest.sessionMetadata(ctx, p.UUID)
				if err != nil {
					p.Status, p.Err = StatusError, err
				} else {
					p = mergeReportMetadata(p, sess)
				}
			}
			res.record(p)
		}
		batch, pending, batchBytes = nil, nil, 0
	}

	for _, uuid := range uuids {
		// Cooperative cancellation at the session boundary (matches Import): the
		// final flush still persists whatever is already batched.
		if ctx.Err() != nil {
			slog.Debug("vault merge: cancelled before completion",
				"imported", res.Imported, "updated", res.Updated,
				"skipped", res.Skipped, "excluded", res.Excluded, "errors", res.Errors)
			break
		}

		src, err := readSourceSession(ctx, srcDB, uuid, srcCols)
		if err != nil {
			slog.Warn("vault merge: reading source session failed", "uuid", uuid, "error", err)
			res.record(ImportedSession{UUID: uuid, Status: StatusError, Err: err})
			continue
		}

		// Resolve the carried platform once, up front, so every decision below
		// (reporting, exclusion, skip, scan and the written row) sees the same
		// valid value. A pre-0006 source arrives here as the 'claude-code' literal
		// and is never sniffed; a present but unrecognized value is corruption and
		// is resolved from the blob with a warning (design § Format Identification)
		// — the RESOLVED platform is what the destination row stores, because merge
		// writes a full row (unlike reindex, which leaves the stored column alone).
		// A value the sniff cannot resolve either is a per-session error: the row
		// is never scanned as Claude by default.
		platform, err := resolveStoredPlatform("vault merge", uuid, src.platform, src.rawJSONL)
		if err != nil {
			slog.Warn("vault merge: cannot resolve source platform", "uuid", uuid, "error", err)
			res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
			continue
		}
		src.platform = string(platform)

		var srcName *SessionName
		if srcHasNames {
			srcName, err = readSourceName(ctx, srcDB, uuid)
			if err != nil {
				slog.Warn("vault merge: reading source session name failed", "uuid", uuid, "error", err)
				res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
				continue
			}
		}
		var srcProject *SessionProject
		if srcHasProjects {
			srcProject, err = readSourceProject(ctx, srcDB, uuid)
			if err != nil {
				slog.Warn("vault merge: reading source session project failed", "uuid", uuid, "error", err)
				res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
				continue
			}
		}

		// The digest lookup precedes the zero-message exclusion because metadata
		// reconciles independently of transcript content (design §Cross-Machine
		// Merge): the exclusion and skip branches below must still know whether a
		// destination session exists to reconcile its title and project against.
		existingHash, existingSize, _, _, found, err := dest.SessionDigest(ctx, uuid)
		if err != nil {
			slog.Warn("vault merge: digest lookup failed", "uuid", uuid, "error", err)
			res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
			continue
		}

		if !found && src.sizeBytes < opts.MinSessionBytes {
			res.record(ImportedSession{
				UUID: uuid, Platform: platform, Title: src.title, ProjectPath: src.projectPath,
				SizeBytes: src.sizeBytes, Status: StatusExcluded,
				Reason: minimumSizeReason(src.sizeBytes, opts.MinSessionBytes),
			})
			continue
		}

		// Exclude empty sessions exactly as disk import does (Task 11). A v2 source
		// already dropped these at its own import, but a v1 source predates the
		// guard, so re-apply it here. message_count is the source's stored scan
		// result (stable: a session with turns is never counted as 0). A populated
		// destination with the same UUID still reconciles title/project state —
		// dropping the shell must not discard newer edits. Without a destination
		// row the metadata has no FK parent and is dropped with the shell.
		if src.messageCount == 0 {
			entry := ImportedSession{
				UUID: uuid, Platform: Platform(src.platform), Title: src.title, ProjectPath: src.projectPath,
				SizeBytes: src.sizeBytes, Status: StatusExcluded,
				Reason: "no messages",
			}
			if found {
				entry = reconcileMergeMetadata(ctx, dest, entry, srcName, srcProject, opts.DryRun)
			}
			res.record(entry)
			continue
		}

		// Same idempotent decision as disk import (design §3): an unchanged (same
		// content_hash) or smaller divergent variant never overwrites the dest; a
		// different variant of equal-or-larger total size replaces in place. Unlike
		// import there is no FTS-only upgrade branch — `capy vault reindex` owns
		// version upgrades of already-present sessions; merge only brings in NEW or
		// larger content. Both skip cases still reconcile title/project state:
		// either metadata change reports the session as updated, exactly once.
		replace := false
		if found {
			switch {
			// Same-hash (idempotent re-merge) and smaller-divergent-variant skips
			// share one arm because their metadata handling is identical.
			case src.contentHash == existingHash, src.sizeBytes < existingSize:
				entry := ImportedSession{UUID: uuid, Platform: Platform(src.platform), SizeBytes: src.sizeBytes, Status: StatusSkipped}
				if srcName != nil || srcProject != nil {
					entry = reconcileMergeMetadata(ctx, dest, entry, srcName, srcProject, opts.DryRun)
				}
				res.record(entry)
				continue
			default:
				replace = true
			}
		}

		files, err := readSourceFiles(ctx, srcDB, uuid, srcFileEnc)
		if err != nil {
			slog.Warn("vault merge: reading source files failed", "uuid", uuid, "error", err)
			res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
			continue
		}

		// Rebuild FTS + chunks with the current indexer over the decoded source
		// blobs (not the source's stored index rows) so the destination index is
		// schema-current regardless of the source's indexer version. The decoder
		// is the destination's own for the resolved platform: a Codex row is
		// re-scanned with the Codex decoder even when the source binary was older.
		_, fts, chunks, err := scanSessionAndSubagents(uuid, platform, "", src.rawJSONL, files)
		if err != nil {
			// Recorded as StatusError but NOT yet batched, so any existing
			// destination row is left UNCHANGED (this scan happens before the write
			// — distinct from a post-batch write failure, which the flush retry
			// handles). Mirrors import.go's buildRecord-failure posture.
			slog.Warn("vault merge: scan failed", "uuid", uuid, "error", err)
			res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
			continue
		}

		rec := src.toRecord(files, fts, chunks)
		status := StatusNew
		prospective := &Session{Title: src.title, ProjectPath: src.projectPath}
		if replace {
			status = StatusUpdated
			prospective, err = dest.sessionMetadata(ctx, uuid)
			if err != nil {
				res.record(ImportedSession{UUID: uuid, SizeBytes: src.sizeBytes, Status: StatusError, Err: err})
				continue
			}
			// The transcript replacement changes imported values, independently
			// of whichever title and project states win.
			prospective.Title, prospective.ProjectPath = src.title, src.projectPath
		}
		mergeMetadataState(prospective, srcName, srcProject)
		// Platform is the resolved source value (reporting only — the CLI groups
		// its summary by it); the write itself re-validates it in writeRecord.
		entry := ImportedSession{
			UUID: uuid, Platform: Platform(src.platform),
			SizeBytes: src.sizeBytes, Status: status,
		}
		entry = mergeReportMetadata(entry, prospective)

		if opts.DryRun {
			res.record(entry)
			continue
		}

		batch = append(batch, SessionWrite{Record: rec, Replace: replace, Name: srcName, Project: srcProject})
		pending = append(pending, entry)
		batchBytes += src.sizeBytes
		if len(batch) >= maxBatchSessions || batchBytes >= maxBatchBytes {
			flush()
		}
	}
	flush()
	return res, nil
}

// sourceSession holds one source vault_sessions row: its metadata, carried into
// the destination verbatim (the source already resolved project_path for paths
// that may not exist on this machine — do NOT recompute), plus the decoded main
// transcript bytes. Only FTS is rebuilt; everything else is the source's own.
type sourceSession struct {
	uuid             string
	title            string
	startTime        sql.NullString
	endTime          sql.NullString
	messageCount     int
	sizeBytes        int64
	contentHash      string
	machineID        string
	claudeProjectDir string
	projectPath      string
	gitBranch        string
	platform         string // stored value as read ('claude-code' for a pre-0006 source); MergeFrom replaces it with the resolved value before use
	parentUUID       string // "" == NULL / pre-0006 source
	rawJSONL         []byte // decoded
}

// sourceColumns records which feature-detected vault_sessions columns the source
// has. Merge sources are never migrated, so an absent column is substituted with
// the literal a migrated vault would read for a legacy row.
type sourceColumns struct {
	encoding   bool // migration 0001; absent ⇒ every blob is raw
	platform   bool // migration 0006; absent ⇒ 'claude-code'
	parentUUID bool // migration 0006; absent ⇒ NULL
}

// toRecord assembles the destination SessionRecord. The location/metadata columns
// (parent included) are carried verbatim and platform is the value MergeFrom
// resolved (verbatim when recognized, sniffed otherwise); index_version is
// stamped to currentIndexVersion because fts + chunks were rebuilt with the
// current indexer (matching reindex's version bump). writeRecord re-validates
// the platform as a backstop.
func (s *sourceSession) toRecord(files []File, fts []FTSRow, chunks []Chunk) *SessionRecord {
	return &SessionRecord{
		Session: Session{
			UUID:             s.uuid,
			Title:            s.title,
			StartTime:        parseTime(s.startTime),
			EndTime:          parseTime(s.endTime),
			MessageCount:     s.messageCount,
			SizeBytes:        s.sizeBytes,
			ContentHash:      s.contentHash,
			MachineID:        s.machineID,
			ClaudeProjectDir: s.claudeProjectDir,
			ProjectPath:      s.projectPath,
			GitBranch:        s.gitBranch,
			IndexVersion:     currentIndexVersion,
			Platform:         Platform(s.platform),
			ParentUUID:       s.parentUUID,
			RawJSONL:         s.rawJSONL,
		},
		Files:  files,
		FTS:    fts,
		Chunks: chunks,
	}
}

// sourceSessionUUIDs collects keys before any blob loading or destination write.
// Filtered enumeration reads metadata only and normalizes foreign empty project
// values exactly as readSourceProject does. Unfiltered enumeration stays UUID-only.
func sourceSessionUUIDs(ctx context.Context, srcDB *sql.DB, project string, hasProjects bool) ([]string, error) {
	query := `SELECT uuid FROM vault_sessions`
	if project != "" {
		query = `SELECT s.uuid, s.project_path, NULL FROM vault_sessions s`
		if hasProjects {
			query = `SELECT s.uuid, s.project_path, p.custom_project FROM vault_sessions s
				LEFT JOIN vault_session_projects p ON p.session_uuid = s.uuid`
		}
	}
	query += ` ORDER BY uuid`

	rows, err := srcDB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var uuids []string
	for rows.Next() {
		var u string
		var imported string
		var custom sql.NullString
		cols := []any{&u}
		if project != "" {
			cols = append(cols, &imported, &custom)
		}
		if err := rows.Scan(cols...); err != nil {
			return nil, fmt.Errorf("scanning uuid: %w", err)
		}
		if project != "" && !containsProjectASCII(effectiveProject(imported, sourceCustomProject(custom)), project) {
			continue
		}
		uuids = append(uuids, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating uuids: %w", err)
	}
	return uuids, nil
}

// containsProjectASCII matches the literal SQLite LIKE predicate's ASCII-only
// folding, without introducing the Unicode folding used by the title finder.
func containsProjectASCII(value, query string) bool {
	fold := func(s string) string {
		b := []byte(s)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + ('a' - 'A')
			}
		}
		return string(b)
	}
	return strings.Contains(fold(value), fold(query))
}

// likeContains builds the `LIKE ? ESCAPE '\'` pattern that matches s as a
// literal substring: LIKE's own metacharacters (`%`, `_`) and the escape
// character are escaped, then the result is wrapped in `%`. Without this a
// user's `--project a_b` would also match "axb" — LIKE semantics leaking into
// what the flag documents as a plain substring filter.
func likeContains(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// readSourceSession loads one source session row and decodes its main transcript.
// Each column the source may predate is substituted with a literal when absent
// (cols): NULL for encoding (blob read as raw), 'claude-code' for platform and
// NULL for parent_uuid (a pre-0006 row is a top-level Claude session — never
// sniffed, see MergeFrom).
func readSourceSession(ctx context.Context, srcDB *sql.DB, uuid string, cols sourceColumns) (*sourceSession, error) {
	encCol := "NULL" // v1 source: no encoding column → read as raw
	if cols.encoding {
		encCol = "encoding"
	}
	platformCol := `'` + string(PlatformClaudeCode) + `'`
	if cols.platform {
		platformCol = "platform"
	}
	parentCol := "NULL"
	if cols.parentUUID {
		parentCol = "parent_uuid"
	}
	query := `SELECT title, start_time, end_time, message_count, size_bytes, content_hash,
		machine_id, claude_project_dir, project_path, git_branch, ` + encCol + `, raw_jsonl, ` +
		platformCol + `, ` + parentCol + `
		FROM vault_sessions WHERE uuid = ?` //nolint:gosec // the substituted columns are trusted internal constants, never user input

	var (
		s        sourceSession
		title    sql.NullString
		branch   sql.NullString
		encoding sql.NullString
		parent   sql.NullString
		raw      []byte
	)
	s.uuid = uuid
	err := srcDB.QueryRowContext(ctx, query, uuid).Scan(
		&title, &s.startTime, &s.endTime, &s.messageCount, &s.sizeBytes, &s.contentHash,
		&s.machineID, &s.claudeProjectDir, &s.projectPath, &branch, &encoding, &raw,
		&s.platform, &parent,
	)
	if err != nil {
		return nil, fmt.Errorf("querying source session: %w", err)
	}
	s.title = title.String
	s.gitBranch = branch.String
	s.parentUUID = parent.String

	decoded, err := decodeBlob(encoding.String, raw)
	if err != nil {
		return nil, fmt.Errorf("decoding source raw_jsonl: %w", err)
	}
	s.rawJSONL = decoded
	return &s, nil
}

// tableExists reports whether the DB has a table named table, via a
// parameterized sqlite_master probe. Merge uses it to feature-detect
// metadata tables on a source deliberately opened WITHOUT running migrations —
// a legacy source is supported and contributes no state for absent tables.
// table is bound as an ordinary WHERE value (sqlite_master.name), never spliced
// into the statement as an identifier, so no injection surface exists here.
func tableExists(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("probing table %s: %w", table, err)
	}
	return n > 0, nil
}

// readSourceName loads the source's capy-owned name state for uuid, or nil when
// the source has never named the session. An empty or whitespace-only
// custom_title — which no supported writer produces — is normalized to a clear
// tombstone, preserving the NULL-or-non-empty invariant before comparison and
// destination write. Everything else is carried verbatim: merge does not
// re-validate a foreign vault's names.
func readSourceName(ctx context.Context, srcDB *sql.DB, uuid string) (*SessionName, error) {
	var (
		customTitle sql.NullString
		renamedAtNS int64
		machineID   string
	)
	err := srcDB.QueryRowContext(ctx,
		`SELECT custom_title, renamed_at_ns, machine_id FROM vault_session_names WHERE session_uuid = ?`,
		uuid).Scan(&customTitle, &renamedAtNS, &machineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying source session name: %w", err)
	}
	name := &SessionName{RenamedAtNS: renamedAtNS, MachineID: machineID}
	if customTitle.Valid && strings.TrimSpace(customTitle.String) != "" {
		name.CustomTitle = &customTitle.String
	}
	return name, nil
}

// sourceCustomProject normalizes only unsupported empty overrides. Other foreign
// values remain verbatim, just like source titles (ADR-030).
func sourceCustomProject(custom sql.NullString) *string {
	if !custom.Valid || strings.TrimSpace(custom.String) == "" {
		return nil
	}
	return &custom.String
}

func readSourceProject(ctx context.Context, db *sql.DB, uuid string) (*SessionProject, error) {
	var custom sql.NullString
	var state SessionProject
	err := db.QueryRowContext(ctx,
		`SELECT custom_project, updated_at_ns, machine_id FROM vault_session_projects WHERE session_uuid = ?`,
		uuid).Scan(&custom, &state.UpdatedAtNS, &state.MachineID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying source session project: %w", err)
	}
	state.CustomProject = sourceCustomProject(custom)
	return &state, nil
}

// mergeMetadataState projects the independent winners for dry-run reporting.
// A real write compares each field again inside its immediate transaction.
func mergeMetadataState(sess *Session, name *SessionName, project *SessionProject) bool {
	changed := false
	if name != nil && sessionNameSupersedes(*name, sess.Name) {
		sess.Name, changed = name, true
	}
	if project != nil && sessionProjectSupersedes(*project, sess.ProjectOverride) {
		sess.ProjectOverride, changed = project, true
	}
	return changed
}

func mergeReportMetadata(entry ImportedSession, sess *Session) ImportedSession {
	entry.Title = sess.EffectiveTitle()
	entry.ProjectPath, entry.Project = sess.ProjectPath, sess.EffectiveProject()
	entry.CustomProject = nil
	if sess.ProjectOverride != nil {
		entry.CustomProject = sess.ProjectOverride.CustomProject
	}
	return entry
}

// readMergeMetadata accepts rows from either the destination pool or its write
// transaction. It closes the metadata-only cursor before its caller can commit.
func readMergeMetadata(rows *sql.Rows, err error) (*Session, error) {
	if err != nil {
		return nil, fmt.Errorf("querying session metadata: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("reading session metadata: %w", err)
		}
		return nil, ErrSessionNotFound
	}
	var sess Session
	if err := scanSessionMeta(rows, &sess, nil); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing session metadata: %w", err)
	}
	return &sess, nil
}

func (s *VaultStore) sessionMetadata(ctx context.Context, uuid string) (*Session, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}
	return readMergeMetadata(db.QueryContext(ctx,
		`SELECT `+sessionMetaColumns+sessionMetaJoin+` WHERE s.uuid = ?`, uuid))
}

// reconcileSessionMetadata keeps both metadata decisions in one transaction on
// branches that leave the transcript untouched. Errors roll back both fields;
// a concurrent delete cannot create orphan rows or silently report success.
func (s *VaultStore) reconcileSessionMetadata(ctx context.Context, uuid string, name *SessionName, project *SessionProject) (*Session, bool, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return nil, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var nameChanged, projectChanged bool
	if name != nil {
		nameChanged, err = reconcileSessionNameTx(ctx, tx, uuid, *name)
		if err != nil {
			return nil, false, err
		}
	}
	if project != nil {
		projectChanged, err = reconcileSessionProjectTx(ctx, tx, uuid, *project)
		if err != nil {
			return nil, false, err
		}
	}
	sess, err := readMergeMetadata(tx.QueryContext(ctx,
		`SELECT `+sessionMetaColumns+sessionMetaJoin+` WHERE s.uuid = ?`, uuid))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("committing session metadata reconcile: %w", err)
	}
	return sess, nameChanged || projectChanged, nil
}

func reconcileMergeMetadata(ctx context.Context, dest *VaultStore, entry ImportedSession, name *SessionName, project *SessionProject, dryRun bool) ImportedSession {
	var sess *Session
	var changed bool
	var err error
	if dryRun {
		sess, err = dest.sessionMetadata(ctx, entry.UUID)
		if err == nil {
			changed = mergeMetadataState(sess, name, project)
		}
	} else {
		sess, changed, err = dest.reconcileSessionMetadata(ctx, entry.UUID, name, project)
	}
	if err != nil {
		slog.Warn("vault merge: metadata reconcile failed", "uuid", entry.UUID, "error", err)
		entry.Status, entry.Err = StatusError, err
		return entry
	}
	if !changed && entry.Status == StatusSkipped {
		return entry // preserve the existing omitted metadata on skipped rows
	}
	if changed {
		entry.Status = StatusUpdated
	}
	// Existing excluded sessions already report metadata. Even when the source
	// has no edits or loses both comparisons, report the destination's values.
	return mergeReportMetadata(entry, sess)
}

// readSourceFiles loads and decodes a source session's sidecar files. As with the
// session row, an absent encoding column (v1 source) is handled by selecting a
// NULL literal so each blob reads as raw.
func readSourceFiles(ctx context.Context, srcDB *sql.DB, uuid string, hasEncoding bool) ([]File, error) {
	encCol := "NULL"
	if hasEncoding {
		encCol = "encoding"
	}
	query := `SELECT relative_path, ` + encCol + `, raw_content FROM vault_files
		WHERE session_uuid = ? ORDER BY relative_path` //nolint:gosec // encCol is a trusted internal constant, never user input

	rows, err := srcDB.QueryContext(ctx, query, uuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := make([]File, 0)
	for rows.Next() {
		var (
			rel      string
			encoding sql.NullString
			raw      []byte
		)
		if err := rows.Scan(&rel, &encoding, &raw); err != nil {
			return nil, fmt.Errorf("scanning source file: %w", err)
		}
		decoded, err := decodeBlob(encoding.String, raw)
		if err != nil {
			return nil, fmt.Errorf("decoding source file %q: %w", rel, err)
		}
		files = append(files, File{RelativePath: rel, RawContent: decoded})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating source files: %w", err)
	}
	return files, nil
}
