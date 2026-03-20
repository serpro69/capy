package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	// Check for existing source with same label.
	var existingID int64
	var existingHash sql.NullString
	err = s.stmtFindSourceByLabel.QueryRow(label).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash.Valid && existingHash.String == hash {
			// Same content — update access time and return.
			s.stmtUpdateSourceAccess.Exec(label, hash)
			return &IndexResult{
				SourceID:       existingID,
				Label:          label,
				AlreadyIndexed: true,
			}, nil
		}
		// Different content — delete old source + chunks.
		if err := s.deleteSource(existingID); err != nil {
			return nil, fmt.Errorf("deleting old source: %w", err)
		}
	}

	// Chunk the content.
	var chunks []Chunk
	switch contentType {
	case "markdown":
		chunks = chunkMarkdown(content, maxChunkBytes)
	case "json":
		chunks = chunkJSON(content, maxChunkBytes)
	case "plaintext":
		chunks = chunkPlainText(content, 20)
	default:
		chunks = chunkPlainText(content, 20)
	}

	if len(chunks) == 0 {
		chunks = []Chunk{{Title: "Content", Content: content}}
	}

	codeChunks := 0
	for i := range chunks {
		if chunks[i].HasCode {
			codeChunks++
			chunks[i].ContentType = "code"
		} else {
			chunks[i].ContentType = "prose"
		}
	}

	// Insert in transaction.
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

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

	// Extract vocabulary (outside transaction, non-critical).
	s.extractAndStoreVocabulary(content)

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

func (s *ContentStore) deleteSource(sourceID int64) error {
	if _, err := s.stmtDeleteChunksBySource.Exec(sourceID); err != nil {
		return err
	}
	if _, err := s.stmtDeleteTrigramBySource.Exec(sourceID); err != nil {
		return err
	}
	if _, err := s.stmtDeleteSource.Exec(sourceID); err != nil {
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
