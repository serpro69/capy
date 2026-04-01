package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

func (s *ContentStore) ctx() context.Context {
	return context.Background()
}

// ContentStore manages the FTS5 knowledge base.
type ContentStore struct {
	dbPath      string
	projectDir  string
	titleWeight float64

	mu sync.Mutex
	db *sql.DB

	// Prepared statements — indexing.
	stmtInsertSource          *sql.Stmt
	stmtInsertChunk           *sql.Stmt
	stmtInsertTrigram         *sql.Stmt
	stmtInsertVocab           *sql.Stmt
	stmtDeleteChunksBySource  *sql.Stmt
	stmtDeleteTrigramBySource *sql.Stmt
	stmtDeleteSource          *sql.Stmt
	stmtFindSourceByLabel     *sql.Stmt
	stmtUpdateSourceAccess    *sql.Stmt

	// Prepared statements — search.
	stmtFuzzyVocab *sql.Stmt

	// Prepared statements — queries.
	stmtGetSourceMeta    *sql.Stmt
	stmtListSources      *sql.Stmt
	stmtChunksBySource   *sql.Stmt
	stmtSourceChunkCount *sql.Stmt
	stmtChunkContent     *sql.Stmt
	stmtTrackAccess      *sql.Stmt
}

// NewContentStore creates a new ContentStore. The database is not opened
// until the first operation (lazy initialization via getDB).
// titleWeight controls the BM25 title column weight; values <= 0 default to 2.0.
func NewContentStore(dbPath, projectDir string, titleWeight float64) *ContentStore {
	if titleWeight <= 0 {
		titleWeight = 2.0
	}
	return &ContentStore{
		dbPath:      dbPath,
		projectDir:  projectDir,
		titleWeight: titleWeight,
	}
}

// getDB returns the database connection, initializing it on first call.
func (s *ContentStore) getDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db, nil
	}

	dbDir := filepath.Dir(s.dbPath)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating DB directory: %w", err)
	}

	// Write a breadcrumb so the data directory is self-documenting.
	// Errors are non-fatal — the DB works fine without it.
	if s.projectDir != "" {
		_ = os.WriteFile(filepath.Join(dbDir, ".project"), []byte(s.projectDir+"\n"), 0o644)
	}

	dsn := s.dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	if err := s.prepareStatements(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("preparing statements: %w", err)
	}

	s.db = db
	return db, nil
}

func (s *ContentStore) prepareStatements(db *sql.DB) error {
	var err error

	// --- Indexing ---

	s.stmtInsertSource, err = db.Prepare(`
		INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	s.stmtInsertChunk, err = db.Prepare(`
		INSERT INTO chunks (title, content, source_id, content_type)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	s.stmtInsertTrigram, err = db.Prepare(`
		INSERT INTO chunks_trigram (title, content, source_id, content_type)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}

	s.stmtInsertVocab, err = db.Prepare(`INSERT OR IGNORE INTO vocabulary (word) VALUES (?)`)
	if err != nil {
		return err
	}

	s.stmtDeleteChunksBySource, err = db.Prepare(`DELETE FROM chunks WHERE source_id = ?`)
	if err != nil {
		return err
	}

	s.stmtDeleteTrigramBySource, err = db.Prepare(`DELETE FROM chunks_trigram WHERE source_id = ?`)
	if err != nil {
		return err
	}

	s.stmtDeleteSource, err = db.Prepare(`DELETE FROM sources WHERE id = ?`)
	if err != nil {
		return err
	}

	s.stmtFindSourceByLabel, err = db.Prepare(`
		SELECT id, content_hash FROM sources WHERE label = ?`)
	if err != nil {
		return err
	}

	s.stmtUpdateSourceAccess, err = db.Prepare(`
		UPDATE sources SET last_accessed_at = datetime('now') WHERE label = ? AND content_hash = ?`)
	if err != nil {
		return err
	}

	// --- Search ---

	s.stmtFuzzyVocab, err = db.Prepare(`
		SELECT word FROM vocabulary WHERE length(word) BETWEEN ? AND ?`)
	if err != nil {
		return err
	}

	// --- Queries ---

	s.stmtGetSourceMeta, err = db.Prepare(`
		SELECT label, chunk_count, indexed_at FROM sources WHERE label = ?`)
	if err != nil {
		return err
	}

	s.stmtListSources, err = db.Prepare(`
		SELECT id, label, content_type, chunk_count, code_chunk_count,
			indexed_at, last_accessed_at, access_count, content_hash
		FROM sources ORDER BY id DESC`)
	if err != nil {
		return err
	}

	s.stmtChunksBySource, err = db.Prepare(`
		SELECT c.title, c.content, c.content_type, s.label, c.source_id
		FROM chunks c
		JOIN sources s ON s.id = c.source_id
		WHERE c.source_id = ?
		ORDER BY c.rowid`)
	if err != nil {
		return err
	}

	s.stmtSourceChunkCount, err = db.Prepare(`
		SELECT chunk_count FROM sources WHERE id = ?`)
	if err != nil {
		return err
	}

	s.stmtChunkContent, err = db.Prepare(`
		SELECT content FROM chunks WHERE source_id = ?`)
	if err != nil {
		return err
	}

	s.stmtTrackAccess, err = db.Prepare(`
		UPDATE sources SET last_accessed_at = datetime('now'), access_count = access_count + 1
		WHERE id = ?`)
	if err != nil {
		return err
	}

	return nil
}

// Close finalizes statements, checkpoints WAL, and closes the database.
//
// WAL checkpoint architecture (see ADR-016):
//
// SQLite WAL mode creates sidecar files (.db-wal, .db-shm) that must be
// flushed into the main .db file before git operations — git only tracks the
// main file, and stale sidecars cause corruption on branch switches (ADR-015).
//
// Checkpoint requires exclusive WAL access. database/sql maintains a connection
// pool, and wal_checkpoint(TRUNCATE) silently degrades to passive (incomplete)
// if other pool connections hold the WAL open. To solve this:
//
//  1. Close all prepared statements
//  2. Close the connection pool (db.Close) — releases all WAL readers
//  3. Open a fresh single connection and run PRAGMA wal_checkpoint(TRUNCATE)
//
// This is the primary checkpoint mechanism. The CLI command `capy checkpoint`
// uses Checkpoint() which does step 3 standalone (for use when the server is
// not running). A git pre-commit hook calls `capy checkpoint` as a safety net.
func (s *ContentStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}

	stmts := []*sql.Stmt{
		s.stmtInsertSource, s.stmtInsertChunk, s.stmtInsertTrigram,
		s.stmtInsertVocab, s.stmtDeleteChunksBySource, s.stmtDeleteTrigramBySource,
		s.stmtDeleteSource, s.stmtFindSourceByLabel, s.stmtUpdateSourceAccess,
		s.stmtFuzzyVocab, s.stmtGetSourceMeta, s.stmtListSources, s.stmtChunksBySource,
		s.stmtSourceChunkCount, s.stmtChunkContent, s.stmtTrackAccess,
	}
	for _, stmt := range stmts {
		if stmt != nil {
			stmt.Close()
		}
	}

	// Close the connection pool FIRST. This releases all pooled connections
	// that hold the WAL open. Without this, wal_checkpoint(TRUNCATE) can't
	// get exclusive access and silently degrades to passive (incomplete).
	err := s.db.Close()
	s.db = nil

	// Now checkpoint with a fresh single connection — no other connections
	// exist, so TRUNCATE gets exclusive access and fully flushes the WAL.
	if cpErr := s.checkpoint(); cpErr != nil {
		if err == nil {
			err = cpErr
		}
	}

	return err
}

// checkpoint opens a dedicated single connection and runs
// PRAGMA wal_checkpoint(TRUNCATE). Must be called after the
// connection pool is closed so no other connections hold the WAL.
func (s *ContentStore) checkpoint() error {
	dsn := s.dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Checkpoint flushes the WAL into the main database file using a dedicated
// single connection (not the connection pool). This is the correct way to
// checkpoint from outside the running server — e.g., from `capy checkpoint`.
func (s *ContentStore) Checkpoint() error {
	dsn := s.dbPath + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("opening database for checkpoint: %w", err)
	}
	defer db.Close()

	// Force a single connection — no pool interference.
	db.SetMaxOpenConns(1)

	var busy, log, checkpointed int
	err = db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed)
	if err != nil {
		return fmt.Errorf("checkpoint pragma failed: %w", err)
	}
	if busy > 0 {
		return fmt.Errorf("checkpoint incomplete: %d pages busy (another process has the DB open)", busy)
	}

	return nil
}
