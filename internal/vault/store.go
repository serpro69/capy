package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/store"
)

// minUUIDPrefix is the shortest partial UUID accepted for lookups (git-style).
const minUUIDPrefix = 8

// currentIndexVersion stamps the FTS indexer logic. Bump it whenever scanner.go's
// extraction changes in a way that should re-index already-archived sessions —
// but ONLY across a released boundary. Within an unreleased version (nothing
// shipped, no durable vault holds the version yet) the indexer may be redefined in
// place without a bump, since a single reindex still produces the complete result.
//   - v1: tool_result rows indexed as result text only.
//   - v2: tool_result rows tagged with their originating call summary
//     (scanner.go collectToolUseSummaries), AND FTS-excluded result bodies
//     (scanner.go ftsExcludedResult): Read/NotebookRead file/cell dumps and
//     Edit/Write success boilerplate (its real signal — the diff — lives in
//     toolUseResult, not the indexable message body). Each excluded call stays
//     searchable on the assistant row. Edit/Write exclusion (design.md § Addenda A3)
//     was added in-place on the unreleased vault_v2 branch — no bump, since nothing
//     shipped holds v2 yet and a single reindex still produces the complete result.
//
//   - v3: chunk-granularity FTS added (vault_chunks / vault_chunks_trigram,
//     migration 0004; chunks built by chunker.go from the same scan that feeds
//     vault_fts). Sessions below v3 have empty chunk tables until
//     `capy vault reindex` backfills them.
//
// Sessions whose index_version is below this are upgraded by `capy vault reindex`
// (DB-driven, covers archived-and-deleted-from-disk sessions) or opportunistically
// by a re-`import` of a still-on-disk session (see import.go skip gate).
const currentIndexVersion = 3

// schemaSQL is the full v1 vault schema. Every table uses IF NOT EXISTS so the
// DDL is safe to run on each open. vault_migrations is created by the migration
// framework (migrations.go), not here.
const schemaSQL = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS vault_sessions (
  uuid               TEXT PRIMARY KEY,
  title              TEXT,
  start_time         DATETIME,
  end_time           DATETIME,
  message_count      INTEGER NOT NULL DEFAULT 0,
  size_bytes         INTEGER NOT NULL DEFAULT 0,
  content_hash       TEXT NOT NULL,
  machine_id         TEXT NOT NULL,
  claude_project_dir TEXT NOT NULL,
  project_path       TEXT NOT NULL,
  git_branch         TEXT,
  archived_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
  index_version      INTEGER NOT NULL DEFAULT 1,
  raw_jsonl          BLOB NOT NULL,
  encoding           TEXT
);

CREATE TABLE IF NOT EXISTS vault_files (
  session_uuid  TEXT NOT NULL REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
  relative_path TEXT NOT NULL,
  raw_content   BLOB NOT NULL,
  encoding      TEXT,
  PRIMARY KEY (session_uuid, relative_path)
);

CREATE VIRTUAL TABLE IF NOT EXISTS vault_fts USING fts5(
  content_text,
  session_uuid  UNINDEXED,
  subagent_id   UNINDEXED,
  turn_index    UNINDEXED,
  message_index UNINDEXED,
  line_index    UNINDEXED,
  role          UNINDEXED,
  tokenize='porter unicode61'
);

CREATE TABLE IF NOT EXISTS vault_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_end_time ON vault_sessions(end_time DESC);
` + chunkFTSTablesSQL

// chunkFTSTablesSQL creates the two chunk-granularity FTS5 layer tables used by
// the shared retrieval engine (internal/retrieval): a porter-stemmed layer and a
// trigram layer, mirroring knowledge.db's chunks/chunks_trigram design. It is
// shared verbatim between schemaSQL (fresh vaults) and migration 0004 (legacy
// vaults) so the two paths can never diverge.
//
// Column-order invariant: title and content_text MUST stay the first two
// declared columns, in that order — the shared layer-query skeleton hardcodes
// highlight(<table>, 1, …) and bm25(<table>, titleWeight, 1.0) around those
// positions (see retrieval.CorpusConfig).
//
// first_line_index anchors a chunk to the raw-JSONL line of its first
// constituent scanner result, so a chunk hit can open at the right line in the
// TUI / `capy vault show`. The per-line vault_fts table is unrelated and
// untouched (display/navigation).
const chunkFTSTablesSQL = `
CREATE VIRTUAL TABLE IF NOT EXISTS vault_chunks USING fts5(
  title,
  content_text,
  session_uuid     UNINDEXED,
  subagent_id      UNINDEXED,
  chunk_index      UNINDEXED,
  first_line_index UNINDEXED,
  tokenize='porter unicode61'
);

CREATE VIRTUAL TABLE IF NOT EXISTS vault_chunks_trigram USING fts5(
  title,
  content_text,
  session_uuid     UNINDEXED,
  subagent_id      UNINDEXED,
  chunk_index      UNINDEXED,
  first_line_index UNINDEXED,
  tokenize='trigram'
);
`

// sessionMetaColumns is the column list returned for list/lookup queries; it
// omits the (potentially large) raw_jsonl blob.
const sessionMetaColumns = `uuid, title, start_time, end_time, message_count, size_bytes, ` +
	`content_hash, machine_id, claude_project_dir, project_path, git_branch, archived_at, index_version`

// ErrSessionNotFound is returned when no session matches a lookup.
var ErrSessionNotFound = errors.New("session not found")

// AmbiguousUUIDError is returned when a partial UUID matches more than one
// session. Candidates carries the matches so callers can help the user
// disambiguate (date, project, title).
type AmbiguousUUIDError struct {
	Prefix     string
	Candidates []Session
}

func (e *AmbiguousUUIDError) Error() string {
	return fmt.Sprintf("ambiguous session id %q matches %d sessions", e.Prefix, len(e.Candidates))
}

// Session is one archived session row (vault_sessions). RawJSONL is populated
// only by lookups that request it (GetSession); list queries leave it nil.
type Session struct {
	UUID             string
	Title            string
	StartTime        time.Time
	EndTime          time.Time
	MessageCount     int
	SizeBytes        int64
	ContentHash      string
	MachineID        string
	ClaudeProjectDir string
	ProjectPath      string
	GitBranch        string // empty == NULL
	ArchivedAt       string // DB-managed timestamp; opaque string
	IndexVersion     int    // FTS indexer version stamp (see currentIndexVersion)
	RawJSONL         []byte
}

// File is one preserved sidecar from a session directory (vault_files).
type File struct {
	RelativePath string
	RawContent   []byte
}

// FTSRow is one searchable message (vault_fts). SubagentID is "" for main-session
// rows (empty-string sentinel, never NULL).
type FTSRow struct {
	SessionUUID  string
	SubagentID   string
	TurnIndex    int
	MessageIndex int
	LineIndex    int
	Role         string
	ContentText  string
}

// SessionRecord bundles a session row with its associated files, per-line FTS
// rows, and semantic chunks for one transactional write.
type SessionRecord struct {
	Session Session
	Files   []File
	FTS     []FTSRow
	Chunks  []Chunk
}

// ListOptions filters and bounds ListSessions.
type ListOptions struct {
	Project string // substring match on project_path; "" == no filter
	Limit   int    // <= 0 == no limit
}

// SearchOptions controls Search.
type SearchOptions struct {
	Query   string
	Raw     bool      // true == raw FTS5 MATCH syntax; false == plain keyword (auto-quoted)
	Project string    // substring match on project_path
	Role    string    // "", or user|assistant|tool|system
	After   time.Time // filter on end_time >= After
	Before  time.Time // filter on end_time <= Before
	Limit   int       // <= 0 == default (20)
}

const defaultSearchLimit = 20

// SearchResult is one FTS hit, carrying the navigation anchors (subagent_id +
// line_index) plus enough session metadata for display. It is shared by the
// per-line Search (vault_fts) and the chunk-granularity SearchChunks
// (vault_chunks); fields that only one flavor can populate are documented
// below.
type SearchResult struct {
	SessionUUID string
	SubagentID  string
	// LineIndex is the raw-JSONL anchor: the matched line for per-line hits,
	// the chunk's first_line_index for chunk hits.
	LineIndex int
	// Role is set on per-line hits only; "" for chunk hits — a semantic chunk
	// spans mixed user/assistant/tool lines, so role is undefined at chunk
	// granularity (design vault-session-search, Not Doing).
	Role        string
	Snippet     string
	Title       string
	ProjectPath string
	EndTime     time.Time
	// Content and MatchLayer are populated by SearchChunks only: the full
	// chunk text (so callers can run their own snippet extraction, and the
	// retrieval benchmark can test needle recall) and the retrieval layer
	// that matched ("porter", "trigram", "rrf(porter+trigram)", optionally
	// "fuzzy+"-prefixed). Per-line Search leaves both empty.
	Content    string
	MatchLayer string
}

// VaultStore manages the encrypted vault SQLite database. The DB is opened
// lazily on first use (getDB). It mirrors store.ContentStore's connection
// lifecycle: WAL mode, a canary-verified open, and a WAL checkpoint on Close.
//
// VaultStore is not safe for concurrent use from multiple goroutines. All
// current callers (CLI commands, server sweep) are single-goroutine.
type VaultStore struct {
	dbPath string

	mu sync.Mutex
	db *sql.DB

	stmtInsertSession        *sql.Stmt
	stmtUpdateSession        *sql.Stmt
	stmtInsertFile           *sql.Stmt
	stmtDeleteFilesBySession *sql.Stmt
	stmtInsertFTS            *sql.Stmt
	stmtDeleteFTSBySession   *sql.Stmt
	stmtInsertChunk          *sql.Stmt // vault_chunks (porter layer)
	stmtInsertChunkTrigram   *sql.Stmt // vault_chunks_trigram (trigram layer)
	stmtDeleteChunksPorter   *sql.Stmt
	stmtDeleteChunksTrigram  *sql.Stmt
	stmtDeleteSession        *sql.Stmt
	stmtSessionsByPrefix     *sql.Stmt
	stmtFilesBySession       *sql.Stmt
	stmtUpdateIndexVersion   *sql.Stmt
}

// NewVaultStore creates a VaultStore for the database at dbPath. The DB is not
// opened until the first operation.
func NewVaultStore(dbPath string) *VaultStore {
	return &VaultStore{dbPath: dbPath}
}

// Open eagerly opens (lazily creating) the vault DB so a wrong key or corrupt
// file surfaces immediately. The CLI calls it before a bulk Import: without the
// probe, Import would hit the same open error once per session and report N
// identical failures instead of one clean abort (see import.go and the Task 3
// follow-up in docs/wip/vault/tasks.md). ctx cancels the open + canary probe.
func (s *VaultStore) Open(ctx context.Context) error {
	_, err := s.getDB(ctx)
	return err
}

// getDB returns the connection, opening it on first call. On corruption it backs
// up the corrupt file and retries once — but a wrong passphrase on a real
// encrypted DB is never treated as corruption (no destructive recovery on a key
// typo). Mirrors store.ContentStore.getDB. ctx cancels the (first-call) open;
// once the connection is cached, ctx is unused on subsequent calls.
func (s *VaultStore) getDB(ctx context.Context) (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db, nil
	}

	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating vault DB directory: %w", err)
	}

	db, err := s.openDB(ctx)
	if err != nil && sqliteutil.IsWrongPassphrase(err) && !sqliteutil.IsGarbageFile(s.dbPath) {
		return nil, err
	}
	if err != nil && sqliteutil.IsSQLiteCorruption(err) {
		slog.Warn("corrupt vault database detected, backing up and recreating", "path", s.dbPath, "error", err)
		sqliteutil.BackupCorruptDB(s.dbPath)
		db, err = s.openDB(ctx)
		if err != nil {
			return nil, fmt.Errorf("opening vault database after recovery: %w", err)
		}
	}
	if err != nil {
		return nil, err
	}

	s.db = db
	return db, nil
}

func (s *VaultStore) openDB(ctx context.Context) (*sql.DB, error) {
	key, err := RequireVaultKey()
	if err != nil {
		return nil, err
	}

	dsn := store.EncryptedDSN(s.dbPath, key) +
		"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sqliteutil.OpenWithCanary(ctx, dsn, s.dbPath, vaultKeyEnv)
	if err != nil {
		return nil, err
	}

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing vault schema: %w", err)
	}

	if err := migrateVault(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying vault migrations: %w", err)
	}

	// Refuse a vault whose on-disk format is newer than this binary understands
	// (a future version's compressed blobs would otherwise be mis-read). Runs
	// after migrations so vault_meta exists; an unmarked (v1) vault passes.
	if err := checkReaderVersion(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	if err := s.prepareStatements(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("preparing vault statements: %w", err)
	}

	return db, nil
}

func (s *VaultStore) prepareStatements(ctx context.Context, db *sql.DB) error {
	var err error

	if s.stmtInsertSession, err = db.PrepareContext(ctx, `
		INSERT INTO vault_sessions
			(uuid, title, start_time, end_time, message_count, size_bytes, content_hash,
			 machine_id, claude_project_dir, project_path, git_branch, index_version, raw_jsonl, encoding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	// Replacement UPDATE: overwrites metadata, location, and blob in place.
	// archived_at is deliberately omitted so the original archival time survives.
	if s.stmtUpdateSession, err = db.PrepareContext(ctx, `
		UPDATE vault_sessions SET
			title = ?, start_time = ?, end_time = ?, message_count = ?, size_bytes = ?,
			content_hash = ?, machine_id = ?, claude_project_dir = ?, project_path = ?,
			git_branch = ?, index_version = ?, raw_jsonl = ?, encoding = ?
		WHERE uuid = ?`); err != nil {
		return err
	}

	if s.stmtInsertFile, err = db.PrepareContext(ctx, `
		INSERT INTO vault_files (session_uuid, relative_path, raw_content, encoding) VALUES (?, ?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtDeleteFilesBySession, err = db.PrepareContext(ctx, `DELETE FROM vault_files WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtInsertFTS, err = db.PrepareContext(ctx, `
		INSERT INTO vault_fts
			(content_text, session_uuid, subagent_id, turn_index, message_index, line_index, role)
		VALUES (?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtDeleteFTSBySession, err = db.PrepareContext(ctx, `DELETE FROM vault_fts WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtInsertChunk, err = db.PrepareContext(ctx, `
		INSERT INTO vault_chunks
			(title, content_text, session_uuid, subagent_id, chunk_index, first_line_index)
		VALUES (?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtInsertChunkTrigram, err = db.PrepareContext(ctx, `
		INSERT INTO vault_chunks_trigram
			(title, content_text, session_uuid, subagent_id, chunk_index, first_line_index)
		VALUES (?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtDeleteChunksPorter, err = db.PrepareContext(ctx, `DELETE FROM vault_chunks WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtDeleteChunksTrigram, err = db.PrepareContext(ctx, `DELETE FROM vault_chunks_trigram WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtDeleteSession, err = db.PrepareContext(ctx, `DELETE FROM vault_sessions WHERE uuid = ?`); err != nil {
		return err
	}

	if s.stmtSessionsByPrefix, err = db.PrepareContext(ctx, `
		SELECT `+sessionMetaColumns+`, encoding, raw_jsonl
		FROM vault_sessions WHERE uuid LIKE ? ORDER BY end_time DESC`); err != nil {
		return err
	}

	if s.stmtFilesBySession, err = db.PrepareContext(ctx, `
		SELECT relative_path, encoding, raw_content FROM vault_files
		WHERE session_uuid = ? ORDER BY relative_path`); err != nil {
		return err
	}

	if s.stmtUpdateIndexVersion, err = db.PrepareContext(ctx, `
		UPDATE vault_sessions SET index_version = ? WHERE uuid = ?`); err != nil {
		return err
	}

	return nil
}

func (s *VaultStore) statements() []*sql.Stmt {
	return []*sql.Stmt{
		s.stmtInsertSession, s.stmtUpdateSession, s.stmtInsertFile,
		s.stmtDeleteFilesBySession, s.stmtInsertFTS, s.stmtDeleteFTSBySession,
		s.stmtInsertChunk, s.stmtInsertChunkTrigram,
		s.stmtDeleteChunksPorter, s.stmtDeleteChunksTrigram,
		s.stmtDeleteSession, s.stmtSessionsByPrefix, s.stmtFilesBySession,
		s.stmtUpdateIndexVersion,
	}
}

// Close finalizes statements, closes the connection pool, and checkpoints the
// WAL into the main DB file. Like store.ContentStore.Close, the pool must close
// first so wal_checkpoint(TRUNCATE) gets exclusive access (see ADR-016).
func (s *VaultStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	for _, stmt := range s.statements() {
		if stmt != nil {
			stmt.Close()
		}
	}
	s.stmtInsertSession = nil
	s.stmtUpdateSession = nil
	s.stmtInsertFile = nil
	s.stmtDeleteFilesBySession = nil
	s.stmtInsertFTS = nil
	s.stmtDeleteFTSBySession = nil
	s.stmtInsertChunk = nil
	s.stmtInsertChunkTrigram = nil
	s.stmtDeleteChunksPorter = nil
	s.stmtDeleteChunksTrigram = nil
	s.stmtDeleteSession = nil
	s.stmtSessionsByPrefix = nil
	s.stmtFilesBySession = nil
	s.stmtUpdateIndexVersion = nil

	err := s.db.Close()
	s.db = nil

	if cpErr := s.Checkpoint(); cpErr != nil && err == nil {
		err = cpErr
	}
	return err
}

// Checkpoint flushes the WAL into the main DB file using a dedicated single
// connection (not the pool), mirroring store.ContentStore.Checkpoint. It is the
// correct way to checkpoint from outside the running server — e.g. `capy vault
// checkpoint`, or Close after the pool is closed. Reports an error if another
// process still holds the DB open (busy pages remain, so the WAL can't be fully
// truncated).
func (s *VaultStore) Checkpoint() error {
	key, err := RequireVaultKey()
	if err != nil {
		return err
	}
	dsn := store.EncryptedDSN(s.dbPath, key) + "&_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("opening vault for checkpoint: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var busy, logFrames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("vault checkpoint pragma failed: %w", err)
	}
	if busy > 0 {
		return fmt.Errorf("vault checkpoint incomplete: %d pages busy (another process has the DB open)", busy)
	}
	return nil
}

// SessionWrite pairs a record with whether it overwrites an existing row
// (UPDATE) or inserts a new one (INSERT). WriteBatch applies a slice of these in
// a single transaction.
type SessionWrite struct {
	Record  *SessionRecord
	Replace bool
}

// InsertSession writes a new session, its files, and its FTS rows in one
// transaction. Use ReplaceSession to overwrite an existing UUID.
func (s *VaultStore) InsertSession(ctx context.Context, rec *SessionRecord) error {
	return s.writeOne(ctx, SessionWrite{Record: rec, Replace: false})
}

// ReplaceSession overwrites an existing session in place (UPDATE, not
// DELETE+INSERT) so archived_at is preserved, then rebuilds its files and FTS
// rows. All within one transaction.
func (s *VaultStore) ReplaceSession(ctx context.Context, rec *SessionRecord) error {
	return s.writeOne(ctx, SessionWrite{Record: rec, Replace: true})
}

// writeOne applies a single SessionWrite in its own transaction.
func (s *VaultStore) writeOne(ctx context.Context, w SessionWrite) error {
	db, err := s.getDB(ctx)
	if err != nil {
		return err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.writeRecord(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit()
}

// WriteBatch applies multiple SessionWrites in a single transaction so a bulk
// import amortizes one write-lock acquisition (beginImmediate) across the batch
// instead of contending per session with a concurrent writer (e.g. the
// server-startup sweep). On any error the whole batch rolls back; the caller is
// expected to retry the batch's records individually via InsertSession/
// ReplaceSession (see import.go). A nil/empty batch is a no-op.
func (s *VaultStore) WriteBatch(ctx context.Context, writes []SessionWrite) error {
	if len(writes) == 0 {
		return nil
	}
	db, err := s.getDB(ctx)
	if err != nil {
		return err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, w := range writes {
		if err := s.writeRecord(ctx, tx, w); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// writeRecord writes one session record within tx. A replace UPDATEs the row in
// place (preserving archived_at) and clears its files/FTS before rebuilding;
// an insert adds a fresh row. Children (files + FTS) are written in both cases.
func (s *VaultStore) writeRecord(ctx context.Context, tx *sql.Tx, w SessionWrite) error {
	sess := &w.Record.Session
	// Compress at the blob seam only — content_hash/size_bytes/FTS were already
	// computed on the uncompressed bytes upstream (import.go), so encoding never
	// affects idempotency, the larger-wins merge tiebreaker, or search.
	rawData, rawEnc := encodeBlob(sess.RawJSONL)
	if w.Replace {
		if _, err := tx.StmtContext(ctx, s.stmtUpdateSession).ExecContext(ctx,
			nullString(sess.Title), writeTime(sess.StartTime), writeTime(sess.EndTime),
			sess.MessageCount, sess.SizeBytes, sess.ContentHash, sess.MachineID,
			sess.ClaudeProjectDir, sess.ProjectPath, nullString(sess.GitBranch),
			sess.IndexVersion, rawData, rawEnc,
			sess.UUID,
		); err != nil {
			return fmt.Errorf("update session: %w", err)
		}
		if _, err := tx.StmtContext(ctx, s.stmtDeleteFilesBySession).ExecContext(ctx, sess.UUID); err != nil {
			return fmt.Errorf("delete files: %w", err)
		}
		if _, err := tx.StmtContext(ctx, s.stmtDeleteFTSBySession).ExecContext(ctx, sess.UUID); err != nil {
			return fmt.Errorf("delete fts: %w", err)
		}
		if err := s.deleteChunks(ctx, tx, sess.UUID); err != nil {
			return err
		}
	} else {
		if _, err := tx.StmtContext(ctx, s.stmtInsertSession).ExecContext(ctx,
			sess.UUID, nullString(sess.Title), writeTime(sess.StartTime), writeTime(sess.EndTime),
			sess.MessageCount, sess.SizeBytes, sess.ContentHash, sess.MachineID,
			sess.ClaudeProjectDir, sess.ProjectPath, nullString(sess.GitBranch),
			sess.IndexVersion, rawData, rawEnc,
		); err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
	}
	childCompressed, err := s.writeChildren(ctx, tx, w.Record)
	if err != nil {
		return err
	}
	// Stamp the forward-compat marker at most ONCE per write tx, when any blob
	// (main or sidecar) was zstd-encoded. markMinReaderVersion is INSERT OR IGNORE,
	// so doing it once here avoids a redundant ExecContext on the common path where
	// both the transcript and its sidecars compress.
	if rawEnc == encodingZstd || childCompressed {
		return markMinReaderVersion(ctx, tx)
	}
	return nil
}

// writeChildren inserts the file and FTS rows for rec within tx. File blobs are
// compressed at the same seam as the main transcript; it reports whether any file
// blob was zstd-encoded so the caller can stamp min_reader_version once per tx.
func (s *VaultStore) writeChildren(ctx context.Context, tx *sql.Tx, rec *SessionRecord) (bool, error) {
	insFile := tx.StmtContext(ctx, s.stmtInsertFile)
	anyCompressed := false
	for _, f := range rec.Files {
		data, enc := encodeBlob(f.RawContent)
		if _, err := insFile.ExecContext(ctx, rec.Session.UUID, f.RelativePath, data, enc); err != nil {
			return false, fmt.Errorf("insert file %q: %w", f.RelativePath, err)
		}
		if enc == encodingZstd {
			anyCompressed = true
		}
	}

	insFTS := tx.StmtContext(ctx, s.stmtInsertFTS)
	for _, r := range rec.FTS {
		if _, err := insFTS.ExecContext(ctx,
			r.ContentText, rec.Session.UUID, r.SubagentID,
			r.TurnIndex, r.MessageIndex, r.LineIndex, r.Role,
		); err != nil {
			return false, fmt.Errorf("insert fts row: %w", err)
		}
	}
	if err := s.insertChunks(ctx, tx, rec.Session.UUID, rec.Chunks); err != nil {
		return false, err
	}
	return anyCompressed, nil
}

// insertChunks writes a session's chunk rows into BOTH layer tables (porter +
// trigram) within tx — the two-layer mirror of knowledge.db's chunks/
// chunks_trigram that the shared retrieval engine fuses with RRF.
func (s *VaultStore) insertChunks(ctx context.Context, tx *sql.Tx, uuid string, chunks []Chunk) error {
	insPorter := tx.StmtContext(ctx, s.stmtInsertChunk)
	insTrigram := tx.StmtContext(ctx, s.stmtInsertChunkTrigram)
	for _, c := range chunks {
		for _, ins := range []*sql.Stmt{insPorter, insTrigram} {
			if _, err := ins.ExecContext(ctx,
				c.Title, c.ContentText, uuid, c.SubagentID, c.ChunkIndex, c.FirstLineIndex,
			); err != nil {
				return fmt.Errorf("insert chunk row: %w", err)
			}
		}
	}
	return nil
}

// deleteChunks clears a session's rows from both chunk tables within tx.
// Like vault_fts, session_uuid is UNINDEXED so each delete is a full table
// scan — batch paths (RebuildFTSBatch) use one IN-scan instead.
func (s *VaultStore) deleteChunks(ctx context.Context, tx *sql.Tx, uuid string) error {
	if _, err := tx.StmtContext(ctx, s.stmtDeleteChunksPorter).ExecContext(ctx, uuid); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	if _, err := tx.StmtContext(ctx, s.stmtDeleteChunksTrigram).ExecContext(ctx, uuid); err != nil {
		return fmt.Errorf("delete trigram chunks: %w", err)
	}
	return nil
}

// FTSRebuild bundles one session's freshly-scanned FTS rows and semantic
// chunks with the index_version they were produced at, for a batched
// index-only rebuild (RebuildFTSBatch). It carries no blob — a rebuild never
// rewrites raw_jsonl.
type FTSRebuild struct {
	UUID       string
	NewVersion int
	FTS        []FTSRow
	Chunks     []Chunk
}

// UpdateSessionFTS rebuilds a single session's FTS rows + chunks and bumps its
// index_version. It is the single-session convenience wrapper over
// RebuildFTSBatch (see there for the full semantics). A missing session is a safe
// no-op (returns nil).
func (s *VaultStore) UpdateSessionFTS(ctx context.Context, uuid string, newVersion int, fts []FTSRow, chunks []Chunk) error {
	_, err := s.RebuildFTSBatch(ctx, []FTSRebuild{{UUID: uuid, NewVersion: newVersion, FTS: fts, Chunks: chunks}})
	return err
}

// RebuildFTSBatch rebuilds the FTS rows for many sessions and bumps each one's
// index_version, in a single transaction. Unlike ReplaceSession it touches ONLY
// the search index and the version stamp — it does NOT rewrite raw_jsonl or any
// sidecar blob, so a full-vault reindex avoids massive WAL bloat / write
// amplification (the stored content is unchanged; only the derived index is
// rebuilt). Used by Reindex and by import's version-stale upgrade path.
//
// The per-session delete is collapsed into ONE `WHERE session_uuid IN (...)` pass
// over vault_fts instead of one delete per session. This matters because
// vault_fts.session_uuid is UNINDEXED (a deliberate schema choice — no external
// content table), so a `WHERE session_uuid = ?` delete is a full scan of the whole
// FTS index. Doing that once per session across a large vault is what turned
// reindex/import into an effective hang; one IN-scan per batch bounds it.
//
// Versions are bumped FIRST and RowsAffected is used as a per-session existence
// check: vault_fts has no foreign key to vault_sessions, so a session deleted
// concurrently (e.g. `capy vault delete` racing a reindex) is dropped from the
// batch before its FTS rows are deleted/reinserted — avoiding orphaned rows.
// Returns the number of sessions actually rebuilt (survivors); callers treat
// items - survivors as silently-skipped (vanished), not errors.
func (s *VaultStore) RebuildFTSBatch(ctx context.Context, items []FTSRebuild) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	db, err := s.getDB(ctx)
	if err != nil {
		return 0, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Bump every version; keep only sessions that still exist (RowsAffected > 0).
	bump := tx.StmtContext(ctx, s.stmtUpdateIndexVersion)
	survivors := make([]FTSRebuild, 0, len(items))
	for _, it := range items {
		res, err := bump.ExecContext(ctx, it.NewVersion, it.UUID)
		if err != nil {
			return 0, fmt.Errorf("update index_version %s: %w", it.UUID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			survivors = append(survivors, it)
		}
	}
	if len(survivors) == 0 {
		return 0, nil // every session vanished — the deferred Rollback discards the empty tx
	}

	// One scan per index table deletes all survivors' existing rows (vault_fts +
	// both chunk tables — all three keep session_uuid UNINDEXED, so a per-session
	// delete would be a full scan each). The queries hold only `?` placeholders
	// (len(survivors) >= 1 here); the UUIDs are bound, never interpolated, and the
	// batch size (<= reindexBatchSessions) stays well under SQLite's
	// bind-parameter limit.
	uuids := make([]any, len(survivors))
	for i, it := range survivors {
		uuids[i] = it.UUID
	}
	placeholders := strings.Repeat("?,", len(uuids)-1) + `?`
	for _, table := range []string{"vault_fts", "vault_chunks", "vault_chunks_trigram"} {
		delQ := `DELETE FROM ` + table + ` WHERE session_uuid IN (` + placeholders + `)` //nolint:gosec // table is a trusted internal constant; UUIDs are bound params, never interpolated
		if _, err := tx.ExecContext(ctx, delQ, uuids...); err != nil {
			return 0, fmt.Errorf("delete %s: %w", table, err)
		}
	}

	insFTS := tx.StmtContext(ctx, s.stmtInsertFTS)
	for _, it := range survivors {
		for _, r := range it.FTS {
			if _, err := insFTS.ExecContext(ctx,
				r.ContentText, it.UUID, r.SubagentID,
				r.TurnIndex, r.MessageIndex, r.LineIndex, r.Role,
			); err != nil {
				return 0, fmt.Errorf("insert fts row for %s: %w", it.UUID, err)
			}
		}
		if err := s.insertChunks(ctx, tx, it.UUID, it.Chunks); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(survivors), nil
}

// checkpointWAL truncates the WAL into the main DB file on the live connection
// pool. Reindex calls it between batches so a long run does not let the WAL grow
// to the high-water mark of the largest batch and stay there. Best-effort: a
// partial checkpoint under reader contention is harmless, so only a hard error is
// surfaced (the pragma's result row is discarded by Exec).
func (s *VaultStore) checkpointWAL(ctx context.Context) error {
	db, err := s.getDB(ctx)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

// OutdatedSessionUUIDs returns the UUIDs of sessions whose index_version is below
// maxVersion (indexed by an older indexer), newest first. Reindex walks these to
// rebuild their FTS from the stored raw_jsonl + sidecars. ctx cancels the query —
// this is the one potentially heavy read in the reindex path.
func (s *VaultStore) OutdatedSessionUUIDs(ctx context.Context, maxVersion int) ([]string, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT uuid FROM vault_sessions WHERE index_version < ? ORDER BY end_time DESC`, maxVersion)
	if err != nil {
		return nil, fmt.Errorf("querying outdated sessions: %w", err)
	}
	defer rows.Close()

	uuids := make([]string, 0)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scanning uuid: %w", err)
		}
		uuids = append(uuids, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating outdated sessions: %w", err)
	}
	return uuids, nil
}

// DeleteSession removes a session and its FTS rows transactionally; vault_files
// cascade via the foreign key. Returns false if no session matched the exact UUID.
func (s *VaultStore) DeleteSession(ctx context.Context, uuid string) (bool, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return false, err
	}
	tx, err := sqliteutil.BeginImmediateContext(ctx, db, "vault_meta")
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.StmtContext(ctx, s.stmtDeleteFTSBySession).ExecContext(ctx, uuid); err != nil {
		return false, fmt.Errorf("delete fts: %w", err)
	}
	if err := s.deleteChunks(ctx, tx, uuid); err != nil {
		return false, err
	}
	res, err := tx.StmtContext(ctx, s.stmtDeleteSession).ExecContext(ctx, uuid)
	if err != nil {
		return false, fmt.Errorf("delete session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetSession resolves a partial UUID (>= 8 chars) to a single session, including
// its raw_jsonl blob. Returns ErrSessionNotFound on no match and
// *AmbiguousUUIDError when more than one session matches.
func (s *VaultStore) GetSession(ctx context.Context, prefix string) (*Session, error) {
	if len(prefix) < minUUIDPrefix {
		return nil, fmt.Errorf("session id must be at least %d characters", minUUIDPrefix)
	}
	if _, err := s.getDB(ctx); err != nil {
		return nil, err
	}

	rows, err := s.stmtSessionsByPrefix.QueryContext(ctx, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("querying sessions: %w", err)
	}
	defer rows.Close()

	var matches []Session
	for rows.Next() {
		var sess Session
		var raw []byte
		if err := scanSessionMeta(rows, &sess, &raw); err != nil {
			return nil, err
		}
		sess.RawJSONL = raw
		matches = append(matches, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating sessions: %w", err)
	}

	switch len(matches) {
	case 0:
		return nil, ErrSessionNotFound
	case 1:
		return &matches[0], nil
	default:
		// Drop the blobs from candidates — only metadata is needed to disambiguate.
		for i := range matches {
			matches[i].RawJSONL = nil
		}
		return nil, &AmbiguousUUIDError{Prefix: prefix, Candidates: matches}
	}
}

// GetFiles returns all stored sidecar files for a session, ordered by path.
func (s *VaultStore) GetFiles(ctx context.Context, sessionUUID string) ([]File, error) {
	if _, err := s.getDB(ctx); err != nil {
		return nil, err
	}
	rows, err := s.stmtFilesBySession.QueryContext(ctx, sessionUUID)
	if err != nil {
		return nil, fmt.Errorf("querying files: %w", err)
	}
	defer rows.Close()

	files := make([]File, 0)
	for rows.Next() {
		var f File
		var encoding sql.NullString
		var raw []byte
		if err := rows.Scan(&f.RelativePath, &encoding, &raw); err != nil {
			return nil, fmt.Errorf("scanning file: %w", err)
		}
		decoded, err := decodeBlob(encoding.String, raw)
		if err != nil {
			return nil, fmt.Errorf("decoding file %q: %w", f.RelativePath, err)
		}
		f.RawContent = decoded
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating files: %w", err)
	}
	return files, nil
}

// ListSessions returns sessions in reverse-chronological order. Location is 1:1
// on the row, so this is a plain SELECT (no GROUP BY). --project is a substring
// match (LIKE %...%), which no index can accelerate; a full scan over a
// single-user vault is cheap.
func (s *VaultStore) ListSessions(ctx context.Context, opts ListOptions) ([]Session, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}

	query := `SELECT ` + sessionMetaColumns + ` FROM vault_sessions`
	var args []any
	if opts.Project != "" {
		query += ` WHERE project_path LIKE ?`
		args = append(args, "%"+opts.Project+"%")
	}
	query += ` ORDER BY end_time DESC`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0)
	for rows.Next() {
		var sess Session
		if err := scanSessionMeta(rows, &sess, nil); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating sessions: %w", err)
	}
	return out, nil
}

// SessionDigest returns the stored content_hash, total size_bytes, and
// index_version for an exact UUID, used by the import pipeline's idempotency +
// reindex-gate check. found is false (with a nil error) when the session is not
// yet archived.
func (s *VaultStore) SessionDigest(ctx context.Context, uuid string) (hash string, size int64, indexVersion int, found bool, err error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return "", 0, 0, false, err
	}
	err = db.QueryRowContext(ctx, `SELECT content_hash, size_bytes, index_version FROM vault_sessions WHERE uuid = ?`, uuid).
		Scan(&hash, &size, &indexVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, 0, false, nil
	}
	if err != nil {
		return "", 0, 0, false, fmt.Errorf("querying session digest: %w", err)
	}
	return hash, size, indexVersion, true, nil
}

// MachineSummary reports the total session count and how many were archived by
// machineID. The import pipeline uses it to warn before overwriting a vault.db
// that holds only other machines' sessions (total > 0 && matching == 0).
func (s *VaultStore) MachineSummary(ctx context.Context, machineID string) (total, matching int, err error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return 0, 0, err
	}
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN machine_id = ? THEN 1 ELSE 0 END), 0)
		FROM vault_sessions`, machineID).Scan(&total, &matching)
	if err != nil {
		return 0, 0, fmt.Errorf("querying machine summary: %w", err)
	}
	return total, matching, nil
}

// Search runs a full-text query over vault_fts and joins back to session
// metadata. Plain keyword mode auto-quotes each token to neutralize FTS5
// operators; Raw passes the query through unchanged. Results carry subagent_id
// and line_index — the anchors a viewer uses to jump to the match.
func (s *VaultStore) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	match := opts.Query
	if !opts.Raw {
		match = autoQuoteFTS(opts.Query)
	}
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}

	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT f.session_uuid, f.subagent_id, f.line_index, f.role,
		       snippet(vault_fts, 0, '[', ']', '…', 16),
		       s.title, s.project_path, s.end_time
		FROM vault_fts f
		JOIN vault_sessions s ON s.uuid = f.session_uuid
		WHERE vault_fts MATCH ?`
	args := []any{match}

	if opts.Project != "" {
		query += ` AND s.project_path LIKE ?`
		args = append(args, "%"+opts.Project+"%")
	}
	if opts.Role != "" {
		query += ` AND f.role = ?`
		args = append(args, opts.Role)
	}
	if !opts.After.IsZero() {
		query += ` AND s.end_time >= ?`
		args = append(args, writeTime(opts.After))
	}
	if !opts.Before.IsZero() {
		query += ` AND s.end_time <= ?`
		args = append(args, writeTime(opts.Before))
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	query += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}
	defer rows.Close()

	out := make([]SearchResult, 0)
	for rows.Next() {
		var r SearchResult
		var title sql.NullString
		var endTime sql.NullString
		if err := rows.Scan(&r.SessionUUID, &r.SubagentID, &r.LineIndex, &r.Role,
			&r.Snippet, &title, &r.ProjectPath, &endTime); err != nil {
			return nil, fmt.Errorf("scanning search result: %w", err)
		}
		r.Title = title.String
		r.EndTime = parseTime(endTime)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating search results: %w", err)
	}
	return out, nil
}

// ProjectStat is the archived-session count for one project_path.
type ProjectStat struct {
	ProjectPath string
	Count       int
}

// VaultStats aggregates vault-wide counts for `capy vault stats`. TotalBytes is
// the summed content size (vault_sessions.size_bytes), distinct from the on-disk
// DB file size, which the CLI adds separately via os.Stat. Oldest/Newest are the
// min start_time / max end_time across all sessions (zero when the vault is empty).
type VaultStats struct {
	Sessions   int
	TotalBytes int64
	Oldest     time.Time
	Newest     time.Time
	ByProject  []ProjectStat
	// IndexVersion is the indexer version this binary writes (currentIndexVersion);
	// OutdatedSessions counts archived rows still below it — i.e. how many a
	// `capy vault reindex` would rebuild. Surfaced so the version and any reindex
	// backlog are visible in `capy vault stats`.
	IndexVersion     int
	OutdatedSessions int
}

// Stats returns the session count, summed content size, oldest/newest activity,
// and per-project breakdown. start_time/end_time are stored as fixed-width
// RFC3339 UTC strings, so MIN/MAX over them is chronological.
func (s *VaultStore) Stats(ctx context.Context) (*VaultStats, error) {
	db, err := s.getDB(ctx)
	if err != nil {
		return nil, err
	}

	var st VaultStats
	st.IndexVersion = currentIndexVersion
	var oldest, newest sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(size_bytes), 0), MIN(start_time), MAX(end_time),
		        COALESCE(SUM(CASE WHEN index_version < ? THEN 1 ELSE 0 END), 0)
		 FROM vault_sessions`, currentIndexVersion,
	).Scan(&st.Sessions, &st.TotalBytes, &oldest, &newest, &st.OutdatedSessions); err != nil {
		return nil, fmt.Errorf("querying vault stats: %w", err)
	}
	st.Oldest = parseTime(oldest)
	st.Newest = parseTime(newest)

	rows, err := db.QueryContext(ctx,
		`SELECT project_path, COUNT(*) FROM vault_sessions GROUP BY project_path ORDER BY COUNT(*) DESC, project_path`)
	if err != nil {
		return nil, fmt.Errorf("querying project stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ProjectStat
		if err := rows.Scan(&p.ProjectPath, &p.Count); err != nil {
			return nil, fmt.Errorf("scanning project stat: %w", err)
		}
		st.ByProject = append(st.ByProject, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating project stats: %w", err)
	}
	return &st, nil
}

// scanSessionMeta scans the sessionMetaColumns into sess. If raw is non-nil, two
// extra trailing columns — encoding, raw_jsonl — are scanned and the blob is
// decoded into *raw (the metadata-only callers, e.g. ListSessions, select neither,
// so they pay no decode cost). Callers that pass raw must SELECT
// `sessionMetaColumns, encoding, raw_jsonl` in that order.
func scanSessionMeta(rows *sql.Rows, sess *Session, raw *[]byte) error {
	var title, gitBranch, archivedAt sql.NullString
	var startTime, endTime sql.NullString
	dest := []any{
		&sess.UUID, &title, &startTime, &endTime, &sess.MessageCount, &sess.SizeBytes,
		&sess.ContentHash, &sess.MachineID, &sess.ClaudeProjectDir, &sess.ProjectPath,
		&gitBranch, &archivedAt, &sess.IndexVersion,
	}
	var encoding sql.NullString
	var rawBlob []byte
	if raw != nil {
		dest = append(dest, &encoding, &rawBlob)
	}
	if err := rows.Scan(dest...); err != nil {
		return fmt.Errorf("scanning session: %w", err)
	}
	sess.Title = title.String
	sess.GitBranch = gitBranch.String
	sess.ArchivedAt = archivedAt.String
	sess.StartTime = parseTime(startTime)
	sess.EndTime = parseTime(endTime)
	if raw != nil {
		decoded, err := decodeBlob(encoding.String, rawBlob)
		if err != nil {
			return fmt.Errorf("decoding raw_jsonl for %s: %w", sess.UUID, err)
		}
		*raw = decoded
	}
	return nil
}

// autoQuoteFTS wraps each whitespace-separated token in double quotes so FTS5
// treats them as literal terms (implicit AND), neutralizing operators like
// AND/OR/NEAR and column filters in user input.
func autoQuoteFTS(query string) string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " ")
}

// writeTime renders t as an RFC3339 UTC string for storage, or nil (SQL NULL)
// for the zero time.
func writeTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime parses a stored RFC3339 timestamp, returning the zero time on a
// NULL or unparseable value.
func parseTime(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// nullString maps "" to SQL NULL so nullable columns (title, git_branch) store
// NULL rather than an empty string when absent.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
