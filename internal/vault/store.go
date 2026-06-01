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
  raw_jsonl          BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS vault_files (
  session_uuid  TEXT NOT NULL REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
  relative_path TEXT NOT NULL,
  raw_content   BLOB NOT NULL,
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
`

// sessionMetaColumns is the column list returned for list/lookup queries; it
// omits the (potentially large) raw_jsonl blob.
const sessionMetaColumns = `uuid, title, start_time, end_time, message_count, size_bytes, ` +
	`content_hash, machine_id, claude_project_dir, project_path, git_branch, archived_at`

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

// SessionRecord bundles a session row with its associated files and FTS rows
// for one transactional write.
type SessionRecord struct {
	Session Session
	Files   []File
	FTS     []FTSRow
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
// line_index) plus enough session metadata for display.
type SearchResult struct {
	SessionUUID string
	SubagentID  string
	LineIndex   int
	Role        string
	Snippet     string
	Title       string
	ProjectPath string
	EndTime     time.Time
}

// VaultStore manages the encrypted vault SQLite database. The DB is opened
// lazily on first use (getDB). It mirrors store.ContentStore's connection
// lifecycle: WAL mode, a canary-verified open, and a WAL checkpoint on Close.
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
	stmtDeleteSession        *sql.Stmt
	stmtSessionsByPrefix     *sql.Stmt
	stmtFilesBySession       *sql.Stmt
}

// NewVaultStore creates a VaultStore for the database at dbPath. The DB is not
// opened until the first operation.
func NewVaultStore(dbPath string) *VaultStore {
	return &VaultStore{dbPath: dbPath}
}

func (s *VaultStore) ctx() context.Context { return context.Background() }

// Open eagerly opens (lazily creating) the vault DB so a wrong key or corrupt
// file surfaces immediately. The CLI calls it before a bulk Import: without the
// probe, Import would hit the same open error once per session and report N
// identical failures instead of one clean abort (see import.go and the Task 3
// follow-up in docs/wip/vault/tasks.md).
func (s *VaultStore) Open() error {
	_, err := s.getDB()
	return err
}

// getDB returns the connection, opening it on first call. On corruption it backs
// up the corrupt file and retries once — but a wrong passphrase on a real
// encrypted DB is never treated as corruption (no destructive recovery on a key
// typo). Mirrors store.ContentStore.getDB.
func (s *VaultStore) getDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db, nil
	}

	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating vault DB directory: %w", err)
	}

	db, err := s.openDB()
	if err != nil && sqliteutil.IsWrongPassphrase(err) && !sqliteutil.IsGarbageFile(s.dbPath) {
		return nil, err
	}
	if err != nil && sqliteutil.IsSQLiteCorruption(err) {
		slog.Warn("corrupt vault database detected, backing up and recreating", "path", s.dbPath, "error", err)
		sqliteutil.BackupCorruptDB(s.dbPath)
		db, err = s.openDB()
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

func (s *VaultStore) openDB() (*sql.DB, error) {
	key, err := RequireVaultKey()
	if err != nil {
		return nil, err
	}

	dsn := store.EncryptedDSN(s.dbPath, key) +
		"&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sqliteutil.OpenWithCanary(s.ctx(), dsn, s.dbPath, vaultKeyEnv)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing vault schema: %w", err)
	}

	if err := migrateVault(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying vault migrations: %w", err)
	}

	if err := s.prepareStatements(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("preparing vault statements: %w", err)
	}

	return db, nil
}

func (s *VaultStore) prepareStatements(db *sql.DB) error {
	var err error

	if s.stmtInsertSession, err = db.Prepare(`
		INSERT INTO vault_sessions
			(uuid, title, start_time, end_time, message_count, size_bytes, content_hash,
			 machine_id, claude_project_dir, project_path, git_branch, raw_jsonl)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	// Replacement UPDATE: overwrites metadata, location, and blob in place.
	// archived_at is deliberately omitted so the original archival time survives.
	if s.stmtUpdateSession, err = db.Prepare(`
		UPDATE vault_sessions SET
			title = ?, start_time = ?, end_time = ?, message_count = ?, size_bytes = ?,
			content_hash = ?, machine_id = ?, claude_project_dir = ?, project_path = ?,
			git_branch = ?, raw_jsonl = ?
		WHERE uuid = ?`); err != nil {
		return err
	}

	if s.stmtInsertFile, err = db.Prepare(`
		INSERT INTO vault_files (session_uuid, relative_path, raw_content) VALUES (?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtDeleteFilesBySession, err = db.Prepare(`DELETE FROM vault_files WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtInsertFTS, err = db.Prepare(`
		INSERT INTO vault_fts
			(content_text, session_uuid, subagent_id, turn_index, message_index, line_index, role)
		VALUES (?, ?, ?, ?, ?, ?, ?)`); err != nil {
		return err
	}

	if s.stmtDeleteFTSBySession, err = db.Prepare(`DELETE FROM vault_fts WHERE session_uuid = ?`); err != nil {
		return err
	}

	if s.stmtDeleteSession, err = db.Prepare(`DELETE FROM vault_sessions WHERE uuid = ?`); err != nil {
		return err
	}

	if s.stmtSessionsByPrefix, err = db.Prepare(`
		SELECT ` + sessionMetaColumns + `, raw_jsonl
		FROM vault_sessions WHERE uuid LIKE ? ORDER BY end_time DESC`); err != nil {
		return err
	}

	if s.stmtFilesBySession, err = db.Prepare(`
		SELECT relative_path, raw_content FROM vault_files
		WHERE session_uuid = ? ORDER BY relative_path`); err != nil {
		return err
	}

	return nil
}

func (s *VaultStore) statements() []*sql.Stmt {
	return []*sql.Stmt{
		s.stmtInsertSession, s.stmtUpdateSession, s.stmtInsertFile,
		s.stmtDeleteFilesBySession, s.stmtInsertFTS, s.stmtDeleteFTSBySession,
		s.stmtDeleteSession, s.stmtSessionsByPrefix, s.stmtFilesBySession,
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
func (s *VaultStore) InsertSession(rec *SessionRecord) error {
	return s.writeOne(SessionWrite{Record: rec, Replace: false})
}

// ReplaceSession overwrites an existing session in place (UPDATE, not
// DELETE+INSERT) so archived_at is preserved, then rebuilds its files and FTS
// rows. All within one transaction.
func (s *VaultStore) ReplaceSession(rec *SessionRecord) error {
	return s.writeOne(SessionWrite{Record: rec, Replace: true})
}

// writeOne applies a single SessionWrite in its own transaction.
func (s *VaultStore) writeOne(w SessionWrite) error {
	db, err := s.getDB()
	if err != nil {
		return err
	}
	tx, err := beginImmediate(db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.writeRecord(tx, w); err != nil {
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
func (s *VaultStore) WriteBatch(writes []SessionWrite) error {
	if len(writes) == 0 {
		return nil
	}
	db, err := s.getDB()
	if err != nil {
		return err
	}
	tx, err := beginImmediate(db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, w := range writes {
		if err := s.writeRecord(tx, w); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// writeRecord writes one session record within tx. A replace UPDATEs the row in
// place (preserving archived_at) and clears its files/FTS before rebuilding;
// an insert adds a fresh row. Children (files + FTS) are written in both cases.
func (s *VaultStore) writeRecord(tx *sql.Tx, w SessionWrite) error {
	sess := &w.Record.Session
	if w.Replace {
		if _, err := tx.Stmt(s.stmtUpdateSession).Exec(
			nullString(sess.Title), writeTime(sess.StartTime), writeTime(sess.EndTime),
			sess.MessageCount, sess.SizeBytes, sess.ContentHash, sess.MachineID,
			sess.ClaudeProjectDir, sess.ProjectPath, nullString(sess.GitBranch), sess.RawJSONL,
			sess.UUID,
		); err != nil {
			return fmt.Errorf("update session: %w", err)
		}
		if _, err := tx.Stmt(s.stmtDeleteFilesBySession).Exec(sess.UUID); err != nil {
			return fmt.Errorf("delete files: %w", err)
		}
		if _, err := tx.Stmt(s.stmtDeleteFTSBySession).Exec(sess.UUID); err != nil {
			return fmt.Errorf("delete fts: %w", err)
		}
	} else {
		if _, err := tx.Stmt(s.stmtInsertSession).Exec(
			sess.UUID, nullString(sess.Title), writeTime(sess.StartTime), writeTime(sess.EndTime),
			sess.MessageCount, sess.SizeBytes, sess.ContentHash, sess.MachineID,
			sess.ClaudeProjectDir, sess.ProjectPath, nullString(sess.GitBranch), sess.RawJSONL,
		); err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
	}
	return s.writeChildren(tx, w.Record)
}

// writeChildren inserts the file and FTS rows for rec within tx.
func (s *VaultStore) writeChildren(tx *sql.Tx, rec *SessionRecord) error {
	insFile := tx.Stmt(s.stmtInsertFile)
	for _, f := range rec.Files {
		if _, err := insFile.Exec(rec.Session.UUID, f.RelativePath, f.RawContent); err != nil {
			return fmt.Errorf("insert file %q: %w", f.RelativePath, err)
		}
	}

	insFTS := tx.Stmt(s.stmtInsertFTS)
	for _, r := range rec.FTS {
		if _, err := insFTS.Exec(
			r.ContentText, rec.Session.UUID, r.SubagentID,
			r.TurnIndex, r.MessageIndex, r.LineIndex, r.Role,
		); err != nil {
			return fmt.Errorf("insert fts row: %w", err)
		}
	}
	return nil
}

// DeleteSession removes a session and its FTS rows transactionally; vault_files
// cascade via the foreign key. Returns false if no session matched the exact UUID.
func (s *VaultStore) DeleteSession(uuid string) (bool, error) {
	db, err := s.getDB()
	if err != nil {
		return false, err
	}
	tx, err := beginImmediate(db)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Stmt(s.stmtDeleteFTSBySession).Exec(uuid); err != nil {
		return false, fmt.Errorf("delete fts: %w", err)
	}
	res, err := tx.Stmt(s.stmtDeleteSession).Exec(uuid)
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
func (s *VaultStore) GetSession(prefix string) (*Session, error) {
	if len(prefix) < minUUIDPrefix {
		return nil, fmt.Errorf("session id must be at least %d characters", minUUIDPrefix)
	}
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	rows, err := s.stmtSessionsByPrefix.Query(prefix + "%")
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
func (s *VaultStore) GetFiles(sessionUUID string) ([]File, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}
	rows, err := s.stmtFilesBySession.Query(sessionUUID)
	if err != nil {
		return nil, fmt.Errorf("querying files: %w", err)
	}
	defer rows.Close()

	files := make([]File, 0)
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.RelativePath, &f.RawContent); err != nil {
			return nil, fmt.Errorf("scanning file: %w", err)
		}
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
func (s *VaultStore) ListSessions(opts ListOptions) ([]Session, error) {
	db, err := s.getDB()
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

	rows, err := db.Query(query, args...)
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

// SessionDigest returns the stored content_hash and total size_bytes for an
// exact UUID, used by the import pipeline's idempotency check. found is false
// (with a nil error) when the session is not yet archived.
func (s *VaultStore) SessionDigest(uuid string) (hash string, size int64, found bool, err error) {
	db, err := s.getDB()
	if err != nil {
		return "", 0, false, err
	}
	err = db.QueryRow(`SELECT content_hash, size_bytes FROM vault_sessions WHERE uuid = ?`, uuid).
		Scan(&hash, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("querying session digest: %w", err)
	}
	return hash, size, true, nil
}

// MachineSummary reports the total session count and how many were archived by
// machineID. The import pipeline uses it to warn before overwriting a vault.db
// that holds only other machines' sessions (total > 0 && matching == 0).
func (s *VaultStore) MachineSummary(machineID string) (total, matching int, err error) {
	db, err := s.getDB()
	if err != nil {
		return 0, 0, err
	}
	err = db.QueryRow(`
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
func (s *VaultStore) Search(opts SearchOptions) ([]SearchResult, error) {
	match := opts.Query
	if !opts.Raw {
		match = autoQuoteFTS(opts.Query)
	}
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}

	db, err := s.getDB()
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

	rows, err := db.Query(query, args...)
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
}

// Stats returns the session count, summed content size, oldest/newest activity,
// and per-project breakdown. start_time/end_time are stored as fixed-width
// RFC3339 UTC strings, so MIN/MAX over them is chronological.
func (s *VaultStore) Stats() (*VaultStats, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	var st VaultStats
	var oldest, newest sql.NullString
	if err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(size_bytes), 0), MIN(start_time), MAX(end_time) FROM vault_sessions`,
	).Scan(&st.Sessions, &st.TotalBytes, &oldest, &newest); err != nil {
		return nil, fmt.Errorf("querying vault stats: %w", err)
	}
	st.Oldest = parseTime(oldest)
	st.Newest = parseTime(newest)

	rows, err := db.Query(
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

// scanSessionMeta scans the sessionMetaColumns into sess. If raw is non-nil, an
// extra trailing raw_jsonl column is scanned into it.
func scanSessionMeta(rows *sql.Rows, sess *Session, raw *[]byte) error {
	var title, gitBranch, archivedAt sql.NullString
	var startTime, endTime sql.NullString
	dest := []any{
		&sess.UUID, &title, &startTime, &endTime, &sess.MessageCount, &sess.SizeBytes,
		&sess.ContentHash, &sess.MachineID, &sess.ClaudeProjectDir, &sess.ProjectPath,
		&gitBranch, &archivedAt,
	}
	if raw != nil {
		dest = append(dest, raw)
	}
	if err := rows.Scan(dest...); err != nil {
		return fmt.Errorf("scanning session: %w", err)
	}
	sess.Title = title.String
	sess.GitBranch = gitBranch.String
	sess.ArchivedAt = archivedAt.String
	sess.StartTime = parseTime(startTime)
	sess.EndTime = parseTime(endTime)
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
