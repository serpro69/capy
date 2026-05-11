package store

import (
	"fmt"
	"os"
)

type TableSize struct {
	Name  string
	Pages int64
	Bytes int64
}

type KindSize struct {
	Kind         string
	Sources      int64
	Chunks       int64
	ContentBytes int64
}

type TopSource struct {
	Label        string
	Kind         string
	Chunks       int64
	ContentBytes int64
}

type DiskUsageBreakdown struct {
	DBFileSize    int64
	PageSize      int64
	TotalPages    int64
	FreelistPages int64
	VocabTerms    int64
	Tables        []TableSize
	Kinds         []KindSize
	TopSources    []TopSource
}

func (s *ContentStore) DiskUsage() (*DiskUsageBreakdown, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	b := &DiskUsageBreakdown{}

	if fi, err := os.Stat(s.dbPath); err == nil {
		b.DBFileSize = fi.Size()
	}

	db.QueryRow("PRAGMA page_size").Scan(&b.PageSize)
	db.QueryRow("PRAGMA page_count").Scan(&b.TotalPages)
	db.QueryRow("PRAGMA freelist_count").Scan(&b.FreelistPages)
	db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&b.VocabTerms)

	rows, err := db.Query(`
		SELECT name, SUM(pageno) as pages
		FROM dbstat
		GROUP BY name
		ORDER BY pages DESC`)
	if err != nil {
		b.Tables = s.tableSizesFallback()
	} else {
		defer rows.Close()
		for rows.Next() {
			var t TableSize
			if err := rows.Scan(&t.Name, &t.Pages); err != nil {
				continue
			}
			t.Bytes = t.Pages * b.PageSize
			b.Tables = append(b.Tables, t)
		}
	}

	kindRows, err := db.Query(`
		SELECT s.kind,
			COUNT(DISTINCT s.id) as sources,
			COUNT(c.rowid) as chunks,
			COALESCE(SUM(LENGTH(c.content)), 0) as content_bytes
		FROM sources s
		LEFT JOIN chunks c ON CAST(c.source_id AS INTEGER) = s.id
		GROUP BY s.kind
		ORDER BY content_bytes DESC`)
	if err != nil {
		return b, fmt.Errorf("kind breakdown: %w", err)
	}
	defer kindRows.Close()
	for kindRows.Next() {
		var k KindSize
		if err := kindRows.Scan(&k.Kind, &k.Sources, &k.Chunks, &k.ContentBytes); err != nil {
			continue
		}
		b.Kinds = append(b.Kinds, k)
	}

	topRows, err := db.Query(`
		SELECT s.label, s.kind,
			COUNT(c.rowid) as chunks,
			COALESCE(SUM(LENGTH(c.content)), 0) as bytes
		FROM sources s
		LEFT JOIN chunks c ON CAST(c.source_id AS INTEGER) = s.id
		GROUP BY s.id
		ORDER BY bytes DESC
		LIMIT 15`)
	if err != nil {
		return b, fmt.Errorf("top sources: %w", err)
	}
	defer topRows.Close()
	for topRows.Next() {
		var t TopSource
		if err := topRows.Scan(&t.Label, &t.Kind, &t.Chunks, &t.ContentBytes); err != nil {
			continue
		}
		b.TopSources = append(b.TopSources, t)
	}

	return b, nil
}

func (s *ContentStore) tableSizesFallback() []TableSize {
	db, _ := s.getDB()
	var tables []TableSize
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type IN ('table', 'shadow') ORDER BY name")
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		rows.Scan(&name)
		var count int64
		db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %q", name)).Scan(&count)
		tables = append(tables, TableSize{Name: name, Pages: count})
	}
	return tables
}
