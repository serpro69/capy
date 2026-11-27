package vault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
)

// The session brief reads the opening request and the latest indexed exchange
// of one archived session (docs/feat/wip/vault-session-brief). It deliberately
// bypasses getDB: the normal open creates, migrates and may back up a corrupt
// file, while the brief must leave an absent, outdated or damaged vault exactly
// as it found it. Each call opens a dedicated short-lived read-only connection,
// reads one committed snapshot and closes it.

const (
	// briefDeadline bounds the whole read (open, selection, fetch, close). A
	// shorter caller deadline wins.
	briefDeadline = 2 * time.Second
	// briefFieldReadLimit is the largest complete field value read into Go. A
	// larger value is omitted whole: redacting a prefix could expose part of a
	// secret that straddles the cut.
	briefFieldReadLimit = 64 << 10
	// briefBusyTimeout keeps a reader from queueing behind a writer's lock for
	// long; WAL readers normally never wait.
	briefBusyTimeout = "50"
)

// ErrVaultNotFound reports that the selected vault database file does not exist.
// The brief never creates it.
var ErrVaultNotFound = errors.New("no vault database")

// ErrBriefAmbiguous reports that a session prefix matches more than one session
// in the requested scope. It deliberately carries no candidates or count: the
// lookup stops at the second match and must not reveal out-of-scope sessions.
var ErrBriefAmbiguous = errors.New("ambiguous session id")

// IncompatibleVaultError reports a vault file this binary cannot read without
// changing it: a newer reader-version marker, a layout predating a required
// migration, an inconsistent migration ledger, an unrecognized layout or an
// unrecognized stored platform.
type IncompatibleVaultError struct {
	Detail string
	Err    error
}

func (e *IncompatibleVaultError) Error() string {
	return "incompatible vault: " + e.Detail
}

func (e *IncompatibleVaultError) Unwrap() error { return e.Err }

// BriefTimeoutError reports that the brief's deadline expired, or its context
// was canceled, after the session was resolved. SessionID is set only for an
// intact, shell-safe ID (briefHintID), so the operator can be pointed at
// `capy vault show`.
type BriefTimeoutError struct {
	SessionID string
	Err       error
}

func (e *BriefTimeoutError) Error() string {
	msg := "reading the session brief timed out"
	if errors.Is(e.Err, context.Canceled) {
		msg = "reading the session brief was canceled"
	}
	if e.SessionID != "" {
		msg += fmt.Sprintf("; run capy vault show %s with the same vault selection for the full transcript", e.SessionID)
	}
	return msg
}

func (e *BriefTimeoutError) Unwrap() error { return e.Err }

// briefHintID returns the session ID a diagnostic may suggest as a command
// argument, or "" when it is not both intact and shell-safe.
func briefHintID(f BriefField) string {
	id, intact := briefIntactID(f)
	if !intact || !shellSafeID.MatchString(id) {
		return ""
	}
	return id
}

// shellSafeID matches IDs that can be pasted into a suggested shell command
// unquoted. Claude IDs come from transcript filenames, so an otherwise intact
// ID can still hold spaces or shell metacharacters.
var shellSafeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// BriefScope restricts session lookup. At most one of Project (effective
// project substring) and ProjectPath (imported path substring) may be set; both
// empty means the whole vault.
type BriefScope struct {
	Project     string
	ProjectPath string
}

// BriefField is one untrusted stored value read under briefFieldReadLimit.
type BriefField struct {
	Value   string // complete stored value; empty when absent or omitted
	Present bool   // false for SQL NULL
	Omitted bool   // the stored value exceeded briefFieldReadLimit and was not read
}

// BriefEntry is one cited transcript entry: its stored role, zero-based
// message-start line in the main transcript and its indexed text.
type BriefEntry struct {
	Role string
	Line int64
	Text BriefField
}

// Brief is the selected session's current metadata and available evidence.
// Opening and Latest are nil when the session has no indexed user entry.
// Assistant is nil when the latest request has no indexed assistant entry in
// its turn. LatestIsOpening reports that both user entries are the same row.
type Brief struct {
	ID              BriefField
	Platform        Platform
	Title           BriefField
	Project         BriefField
	IndexVersion    int64
	Opening         *BriefEntry
	Latest          *BriefEntry
	LatestIsOpening bool
	Assistant       *BriefEntry
}

// StaleIndex reports that the session's per-message index predates the current
// indexer, so a reindex might recover rows this brief cannot see.
func (b *Brief) StaleIndex() bool { return b.IndexVersion < currentIndexVersion }

// ValidateBriefSession checks a session selector before any vault access: a
// literal ID or prefix of at least minUUIDPrefix bytes without embedded NUL.
func ValidateBriefSession(session string) error {
	if strings.IndexByte(session, 0) >= 0 {
		return errors.New("session id must not contain NUL")
	}
	if len(session) < minUUIDPrefix {
		return fmt.Errorf("session id must be at least %d characters", minUUIDPrefix)
	}
	return nil
}

// ReadBrief resolves session within scope and reads its brief from one
// committed snapshot of the vault at s's selected path. It uses its own
// read-only connection and never the store's lazily opened writer.
func (s *VaultStore) ReadBrief(ctx context.Context, session string, scope BriefScope) (*Brief, error) {
	if err := ValidateBriefSession(session); err != nil {
		return nil, err
	}
	if strings.IndexByte(scope.Project, 0) >= 0 || strings.IndexByte(scope.ProjectPath, 0) >= 0 {
		return nil, errors.New("project selector must not contain NUL")
	}
	pattern, err := sessionIDPrefixPattern(session)
	if err != nil {
		return nil, err
	}
	scopePredicate, scopeArg, err := projectScopePredicate(scope.Project, scope.ProjectPath)
	if err != nil {
		return nil, err
	}
	key, err := RequireVaultKey()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, briefDeadline)
	defer cancel()

	db, err := openBriefDB(ctx, s.dbPath, key)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("opening vault: %w", ctxErr)
		}
		return nil, err
	}
	// A read-only handle has nothing to flush; a close error cannot invalidate a
	// snapshot whose transaction already finished.
	defer db.Close() //nolint:errcheck

	r := briefReader{session: session, pattern: pattern, scopePredicate: scopePredicate, scopeArg: scopeArg}
	b, err := r.read(ctx, db)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if r.resolved != nil {
				return nil, &BriefTimeoutError{SessionID: briefHintID(r.resolved.ID), Err: ctxErr}
			}
			return nil, fmt.Errorf("reading vault: %w", ctxErr)
		}
		return nil, err
	}
	return b, nil
}

// openBriefDB opens an existing vault for reading. The path check gives a
// missing file its own outcome; mode=ro then makes SQLite itself refuse to
// create the database should it disappear after the check. Ordinary WAL locking
// (not immutable=1) keeps committed WAL frames visible.
func openBriefDB(ctx context.Context, path, key string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrVaultNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("accessing vault database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("vault path %s is not a regular file", path)
	}
	db, err := sqliteutil.OpenWithCanary(ctx, briefDSN(path, key), path, vaultKeyEnv)
	if err != nil {
		return nil, err
	}
	// One connection: the transaction, and therefore the snapshot, owns it.
	db.SetMaxOpenConns(1)
	return db, nil
}

// briefDSN opens read-only: SQLite refuses to create a missing main database.
func briefDSN(path, key string) string {
	return sqliteutil.EncryptedDSN(path, key) + "&mode=ro&_busy_timeout=" + briefBusyTimeout
}

// briefReader carries one lookup through its read transaction. resolved holds
// the session metadata once known, so a later timeout can name the session.
type briefReader struct {
	session        string
	pattern        string
	scopePredicate string
	scopeArg       string
	resolved       *Brief
}

func (r *briefReader) read(ctx context.Context, db *sql.DB) (*Brief, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning vault read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// A deferred transaction's snapshot begins at its first read. Read the
	// schema table first so layout checks and data come from the same revision.
	var tables int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return nil, fmt.Errorf("reading vault schema: %w", err)
	}
	if err := checkBriefLayout(ctx, tx); err != nil {
		return nil, err
	}

	rowid, err := r.resolve(ctx, tx)
	if err != nil {
		return nil, err
	}
	b, err := readBriefMetadata(ctx, tx, rowid)
	if err != nil {
		return nil, err
	}
	r.resolved = b
	if err := readBriefExchange(ctx, tx, rowid, b); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finishing vault read: %w", err)
	}
	return b, nil
}

// resolve applies scope before deciding whether the literal prefix has zero,
// one or several matches. A scoped session whose ID equals the input wins
// outright, mirroring preferExactSession in SQL: the LIMIT 2 prefix query below
// may not return the exact row, so the helper cannot be applied to its result.
// Otherwise two rows establish ambiguity; no count is reported.
func (r *briefReader) resolve(ctx context.Context, tx *sql.Tx) (int64, error) {
	scoped := func(predicate, arg string) (string, []any) {
		query := `SELECT s.rowid FROM vault_sessions s
		LEFT JOIN vault_session_projects p ON p.session_uuid = s.uuid
		WHERE ` + predicate
		args := []any{arg}
		if r.scopePredicate != "" {
			query += " AND " + r.scopePredicate
			args = append(args, r.scopeArg)
		}
		return query, args
	}

	query, args := scoped("s.uuid = ?", r.session)
	var exact int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&exact)
	switch {
	case err == nil:
		return exact, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("resolving session: %w", err)
	}

	query, args = scoped(`s.uuid LIKE ? ESCAPE '\'`, r.pattern)
	rows, err := tx.QueryContext(ctx, query+" LIMIT 2", args...)
	if err != nil {
		return 0, fmt.Errorf("resolving session: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("resolving session: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("resolving session: %w", err)
	}
	switch len(ids) {
	case 0:
		return 0, ErrSessionNotFound
	case 1:
		return ids[0], nil
	default:
		return 0, ErrBriefAmbiguous
	}
}

// guardedColumns projects expr as its byte length and, only when within
// briefFieldReadLimit, its value. CAST AS BLOB counts bytes past an embedded NUL
// and keeps an oversized value inside SQLite. expr must be trusted SQL.
func guardedColumns(expr string) string {
	size := fmt.Sprintf("length(CAST((%s) AS BLOB))", expr)
	return fmt.Sprintf("%s, CASE WHEN %s <= %d THEN (%s) END", size, size, briefFieldReadLimit, expr)
}

func briefField(size sql.NullInt64, value sql.NullString) BriefField {
	if !size.Valid {
		return BriefField{}
	}
	if !value.Valid {
		return BriefField{Present: true, Omitted: true}
	}
	return BriefField{Value: value.String, Present: true}
}

// readBriefMetadata loads the selected session's current effective metadata,
// using the same precedence as Session.EffectiveTitle / EffectiveProject.
func readBriefMetadata(ctx context.Context, tx *sql.Tx, rowid int64) (*Brief, error) {
	query := `SELECT ` +
		guardedColumns(`s.uuid`) + `, ` +
		guardedColumns(`s.platform`) + `, ` +
		guardedColumns(`COALESCE(n.custom_title, s.title)`) + `, ` +
		guardedColumns(effectiveProjectSQL) + `,
		CAST(s.index_version AS INTEGER)
		FROM vault_sessions s
		LEFT JOIN vault_session_names n ON n.session_uuid = s.uuid
		LEFT JOIN vault_session_projects p ON p.session_uuid = s.uuid
		WHERE s.rowid = ?`
	var (
		sizes  [4]sql.NullInt64
		values [4]sql.NullString
		index  sql.NullInt64
	)
	if err := tx.QueryRowContext(ctx, query, rowid).Scan(
		&sizes[0], &values[0], &sizes[1], &values[1],
		&sizes[2], &values[2], &sizes[3], &values[3], &index,
	); err != nil {
		return nil, fmt.Errorf("reading session metadata: %w", err)
	}
	b := &Brief{
		ID:           briefField(sizes[0], values[0]),
		Title:        briefField(sizes[2], values[2]),
		Project:      briefField(sizes[3], values[3]),
		IndexVersion: index.Int64,
	}
	platform := briefField(sizes[1], values[1])
	p, err := ParsePlatform(platform.Value)
	if err != nil || platform.Omitted {
		return nil, &IncompatibleVaultError{Detail: "the selected session has an unrecognized platform value; upgrade capy", Err: ErrUnknownPlatform}
	}
	b.Platform = p
	return b, nil
}

// briefPosition is one main-transcript user/assistant row's ordering key.
type briefPosition struct {
	rowid   int64
	role    string
	turn    int64
	message int64
	line    int64
}

// briefPositionsSQL lists one session's main-transcript user/assistant rows in
// transcript order. session_uuid is UNINDEXED, so this scans vault_fts once
// (measured in brief_workload_test.go, which explains this same query).
const briefPositionsSQL = `SELECT rowid, role,
		CAST(turn_index AS INTEGER), CAST(message_index AS INTEGER), CAST(line_index AS INTEGER)
	FROM vault_fts
	WHERE session_uuid = (SELECT uuid FROM vault_sessions WHERE rowid = ?)
	  AND subagent_id = '' AND role IN ('user', 'assistant')
	ORDER BY 3, 4, 5, rowid`

// readBriefExchange scans the session's main-transcript positions once in
// transcript order and keeps constant state: the first user row, the latest
// user row, and the last assistant row in that latest user's turn. It then
// fetches at most three bodies by rowid.
func readBriefExchange(ctx context.Context, tx *sql.Tx, sessionRowid int64, b *Brief) error {
	rows, err := tx.QueryContext(ctx, briefPositionsSQL, sessionRowid)
	if err != nil {
		return fmt.Errorf("scanning session index: %w", err)
	}
	defer rows.Close()

	var opening, latest, assistant *briefPosition
	for rows.Next() {
		var p briefPosition
		var turn, message, line sql.NullInt64
		if err := rows.Scan(&p.rowid, &p.role, &turn, &message, &line); err != nil {
			return fmt.Errorf("scanning session index: %w", err)
		}
		p.turn, p.message, p.line = turn.Int64, message.Int64, line.Int64
		switch p.role {
		case roleUser:
			if opening == nil {
				opening = &p
			}
			latest = &p
			assistant = nil
		case roleAssistant:
			if latest != nil && p.turn == latest.turn {
				assistant = &p
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scanning session index: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("scanning session index: %w", err)
	}

	if opening == nil {
		return nil
	}
	if b.Opening, err = readBriefEntry(ctx, tx, opening); err != nil {
		return err
	}
	if latest.rowid == opening.rowid {
		b.Latest, b.LatestIsOpening = b.Opening, true
	} else if b.Latest, err = readBriefEntry(ctx, tx, latest); err != nil {
		return err
	}
	if assistant != nil {
		if b.Assistant, err = readBriefEntry(ctx, tx, assistant); err != nil {
			return err
		}
	}
	return nil
}

func readBriefEntry(ctx context.Context, tx *sql.Tx, p *briefPosition) (*BriefEntry, error) {
	var size sql.NullInt64
	var value sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT `+guardedColumns(`content_text`)+` FROM vault_fts WHERE rowid = ?`, p.rowid,
	).Scan(&size, &value); err != nil {
		return nil, fmt.Errorf("reading indexed entry: %w", err)
	}
	text := briefField(size, value)
	if !text.Present {
		text.Present = true // a NULL body reads as empty text
	}
	return &BriefEntry{Role: p.role, Line: p.line, Text: text}, nil
}

// briefRequirement is one structure the brief reads. migration names the
// migration that introduces it on an older vault; "" marks the base layout.
type briefRequirement struct {
	table, column, migration string
}

var briefRequirements = []briefRequirement{
	{"vault_sessions", "uuid", ""},
	{"vault_sessions", "title", ""},
	{"vault_sessions", "project_path", ""},
	{"vault_sessions", "index_version", "0003_add_index_version"},
	{"vault_sessions", "platform", "0006_platform"},
	{"vault_fts", "content_text", ""},
	{"vault_fts", "session_uuid", ""},
	{"vault_fts", "subagent_id", ""},
	{"vault_fts", "turn_index", ""},
	{"vault_fts", "message_index", ""},
	{"vault_fts", "line_index", ""},
	{"vault_fts", "role", ""},
	{"vault_meta", "key", ""},
	{"vault_meta", "value", ""},
	{"vault_session_names", "session_uuid", "0005_session_names"},
	{"vault_session_names", "custom_title", "0005_session_names"},
	{"vault_session_projects", "session_uuid", "0007_session_projects"},
	{"vault_session_projects", "custom_project", "0007_session_projects"},
}

// checkBriefLayout applies the reader-version marker and verifies every
// structure the brief reads, without changing the file. A newer marker wins
// over layout differences because a newer format may legitimately differ.
func checkBriefLayout(ctx context.Context, tx *sql.Tx) error {
	columns := map[string]map[string]bool{}
	for _, req := range briefRequirements {
		if _, ok := columns[req.table]; ok {
			continue
		}
		cols, err := tableColumns(ctx, tx, req.table)
		if err != nil {
			return err
		}
		columns[req.table] = cols
	}
	if columns["vault_meta"]["key"] && columns["vault_meta"]["value"] {
		if _, err := checkReaderVersion(ctx, tx); err != nil {
			// Only a newer format is incompatible; a failure reading the marker
			// is a storage error and keeps its own diagnosis.
			var newer *readerVersionError
			if errors.As(err, &newer) {
				return &IncompatibleVaultError{Detail: err.Error(), Err: err}
			}
			return err
		}
	}

	ledgerCols, err := tableColumns(ctx, tx, "vault_migrations")
	if err != nil {
		return err
	}
	applied := map[string]bool{}
	if ledgerCols["name"] {
		rows, err := tx.QueryContext(ctx, `SELECT name FROM vault_migrations`)
		if err != nil {
			return fmt.Errorf("reading vault migrations: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("reading vault migrations: %w", err)
			}
			applied[name] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("reading vault migrations: %w", err)
		}
	}

	var unrecognized, inconsistent, outdated string
	for _, req := range briefRequirements {
		if columns[req.table][req.column] {
			continue
		}
		missing := req.table + "." + req.column
		switch {
		case req.migration == "":
			if unrecognized == "" {
				unrecognized = missing
			}
		case applied[req.migration]:
			if inconsistent == "" {
				inconsistent = fmt.Sprintf("migration %s is recorded but %s is missing", req.migration, missing)
			}
		default:
			if outdated == "" {
				outdated = req.migration
			}
		}
	}
	switch {
	case unrecognized != "":
		return &IncompatibleVaultError{Detail: fmt.Sprintf("not a recognized vault layout (missing %s)", unrecognized)}
	case inconsistent != "":
		return &IncompatibleVaultError{Detail: "inconsistent vault layout: " + inconsistent}
	case outdated != "":
		return &IncompatibleVaultError{Detail: fmt.Sprintf(
			"the vault predates migration %s; open it once with an ordinary vault command such as "+
				"`capy vault stats` using the same vault selection (--path or CAPY_VAULT_PATH) to migrate it", outdated)}
	}
	return nil
}

// tableColumns returns table's column names, or an empty set when the table is
// absent. table is bound, never interpolated.
func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("reading vault layout: %w", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("reading vault layout: %w", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading vault layout: %w", err)
	}
	return cols, nil
}
