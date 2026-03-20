package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
)

// Index indexes content into the knowledge base. It auto-detects
// content type if contentType is empty. Duplicate content (same label
// and hash) is skipped; changed content replaces the old source.
func (s *ContentStore) Index(content, label, contentType string) (*IndexResult, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	hash := contentHash(content)

	if contentType == "" {
		contentType = DetectContentType(content)
	}

	// Chunk content before entering the transaction to minimize lock hold time.
	chunks := chunkContent(content, contentType)
	codeChunks := 0
	for i := range chunks {
		if chunks[i].HasCode {
			codeChunks++
			chunks[i].ContentType = "code"
		} else {
			chunks[i].ContentType = "prose"
		}
	}

	// BEGIN IMMEDIATE acquires a write lock immediately, preventing
	// concurrent writers from interleaving dedup check + insert.
	tx, err := db.BeginTx(s.ctx(), &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Acquire write lock via a dummy write before the read check.
	// SQLite deferred transactions only acquire the write lock on the
	// first write statement; we force it here so the SELECT + INSERT
	// sequence is atomic.
	if _, err := tx.Exec("DELETE FROM sources WHERE 0"); err != nil {
		return nil, fmt.Errorf("acquiring write lock: %w", err)
	}

	// Check for existing source with same label (inside transaction).
	var existingID int64
	var existingHash sql.NullString
	err = tx.Stmt(s.stmtFindSourceByLabel).QueryRow(label).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash.Valid && existingHash.String == hash {
			// Same content — update access time and return.
			if _, err := tx.Stmt(s.stmtUpdateSourceAccess).Exec(label, hash); err != nil {
				return nil, fmt.Errorf("updating access time: %w", err)
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("committing transaction: %w", err)
			}
			return &IndexResult{
				SourceID:       existingID,
				Label:          label,
				AlreadyIndexed: true,
			}, nil
		}
		// Different content — delete old source + chunks.
		if err := s.deleteSourceTx(tx, existingID); err != nil {
			return nil, fmt.Errorf("deleting old source: %w", err)
		}
	}

	// Insert source and chunks.
	res, err := tx.Stmt(s.stmtInsertSource).Exec(label, contentType, len(chunks), codeChunks, hash)
	if err != nil {
		return nil, fmt.Errorf("inserting source: %w", err)
	}
	sourceID, _ := res.LastInsertId()

	stmtChunk := tx.Stmt(s.stmtInsertChunk)
	stmtTrigram := tx.Stmt(s.stmtInsertTrigram)
	for _, c := range chunks {
		if _, err := stmtChunk.Exec(c.Title, c.Content, sourceID, c.ContentType); err != nil {
			return nil, fmt.Errorf("inserting chunk: %w", err)
		}
		if _, err := stmtTrigram.Exec(c.Title, c.Content, sourceID, c.ContentType); err != nil {
			return nil, fmt.Errorf("inserting trigram chunk: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	// Extract vocabulary outside the main transaction.
	if err := s.extractAndStoreVocabulary(content); err != nil {
		slog.Warn("vocabulary extraction failed", "label", label, "error", err)
	}

	return &IndexResult{
		SourceID:    sourceID,
		Label:       label,
		TotalChunks: len(chunks),
		CodeChunks:  codeChunks,
		ContentType: contentType,
	}, nil
}

// IndexPlainText is a convenience entry point that forces plaintext chunking.
func (s *ContentStore) IndexPlainText(content, label string) (*IndexResult, error) {
	return s.Index(content, label, "plaintext")
}

// IndexJSON is a convenience entry point that forces JSON chunking.
func (s *ContentStore) IndexJSON(content, label string) (*IndexResult, error) {
	return s.Index(content, label, "json")
}

func chunkContent(content, contentType string) []Chunk {
	var chunks []Chunk
	switch contentType {
	case "markdown":
		chunks = chunkMarkdown(content, maxChunkBytes)
	case "json":
		chunks = chunkJSON(content, maxChunkBytes)
	default:
		chunks = chunkPlainText(content, 20)
	}
	if len(chunks) == 0 {
		chunks = []Chunk{{Title: "Content", Content: content}}
	}
	return chunks
}

func (s *ContentStore) deleteSourceTx(tx *sql.Tx, sourceID int64) error {
	if _, err := tx.Stmt(s.stmtDeleteChunksBySource).Exec(sourceID); err != nil {
		return err
	}
	if _, err := tx.Stmt(s.stmtDeleteTrigramBySource).Exec(sourceID); err != nil {
		return err
	}
	if _, err := tx.Stmt(s.stmtDeleteSource).Exec(sourceID); err != nil {
		return err
	}
	return nil
}

// chunkJSON parses JSON and chunks it. Falls back to plaintext on parse error.
func chunkJSON(content string, maxBytes int) []Chunk {
	var parsed any
	if err := jsonUnmarshal([]byte(content), &parsed); err != nil {
		return chunkPlainText(content, 20)
	}
	chunks := walkJSON(parsed, nil, maxBytes)
	if len(chunks) == 0 {
		return chunkPlainText(content, 20)
	}
	return chunks
}

func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// jsonUnmarshal wraps json.Unmarshal.
var jsonUnmarshal = json.Unmarshal
