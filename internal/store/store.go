package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

// ContentStore manages the FTS5 knowledge base.
type ContentStore struct {
	dbPath     string
	projectDir string

	mu sync.Mutex
	db *sql.DB

	// Prepared statements (populated on init).
	stmtInsertSource         *sql.Stmt
	stmtInsertChunk          *sql.Stmt
	stmtInsertTrigram        *sql.Stmt
	stmtInsertVocab          *sql.Stmt
	stmtDeleteChunksBySource *sql.Stmt
	stmtDeleteTrigramBySource *sql.Stmt
	stmtDeleteSource         *sql.Stmt
	stmtFindSourceByLabel    *sql.Stmt
	stmtUpdateSourceAccess   *sql.Stmt
}

// NewContentStore creates a new ContentStore. The database is not opened
// until the first operation (lazy initialization via getDB).
func NewContentStore(dbPath, projectDir string) *ContentStore {
	return &ContentStore{
		dbPath:     dbPath,
		projectDir: projectDir,
	}
}

// getDB returns the database connection, initializing it on first call.
func (s *ContentStore) getDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		return s.db, nil
	}

	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating DB directory: %w", err)
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

	s.stmtDeleteChunksBySource, err = db.Prepare(`
		DELETE FROM chunks WHERE source_id = ?`)
	if err != nil {
		return err
	}

	s.stmtDeleteTrigramBySource, err = db.Prepare(`
		DELETE FROM chunks_trigram WHERE source_id = ?`)
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

	return nil
}

// Close finalizes statements, checkpoints WAL, and closes the database.
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
	}
	for _, stmt := range stmts {
		if stmt != nil {
			stmt.Close()
		}
	}

	// WAL checkpoint before closing.
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	err := s.db.Close()
	s.db = nil
	return err
}
