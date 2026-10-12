package store

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SectionMeta contains display metadata only, never the chunk's content.
type SectionMeta struct {
	Title string
	Bytes int
}

const (
	MaxInventorySections   = 40
	MaxInventoryTitleBytes = 160
)

// GetSectionInventory returns the stored total and at most 40 section summaries.
// Bound titles in SQL as well as Go so neither huge headings nor chunk bodies
// are loaded into the caller merely to render an inventory. No content is changed.
func (s *ContentStore) GetSectionInventory(ctx context.Context, sourceID int64) (int, []SectionMeta, error) {
	db, err := s.getDB()
	if err != nil {
		return 0, nil, err
	}
	var total int
	if err := s.stmtSourceChunkCount.QueryRowContext(ctx, sourceID).Scan(&total); err != nil {
		return 0, nil, fmt.Errorf("source section count: %w", err)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT substr(CAST(title AS BLOB), 1, ?), length(CAST(content AS BLOB))
		FROM chunks WHERE source_id = ? ORDER BY rowid LIMIT ?`,
		MaxInventoryTitleBytes+1, sourceID, MaxInventorySections)
	if err != nil {
		return total, nil, fmt.Errorf("section inventory: %w", err)
	}
	defer rows.Close()
	var sections []SectionMeta
	for rows.Next() {
		var section SectionMeta
		if err := rows.Scan(&section.Title, &section.Bytes); err != nil {
			return total, nil, fmt.Errorf("scan section metadata: %w", err)
		}
		if len(section.Title) > MaxInventoryTitleBytes {
			end := MaxInventoryTitleBytes - len("…")
			for end > 0 && !utf8.RuneStart(section.Title[end]) {
				end--
			}
			section.Title = section.Title[:end] + "…"
		}
		section.Title = strings.ToValidUTF8(section.Title, "?")
		sections = append(sections, section)
	}
	if err := rows.Err(); err != nil {
		return total, nil, fmt.Errorf("iterate section metadata: %w", err)
	}
	return total, sections, nil
}
