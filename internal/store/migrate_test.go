package store

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preMigrationDDL is the sources table schema before the kind column was added.
const preMigrationDDL = `
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  label TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'plaintext',
  chunk_count INTEGER NOT NULL DEFAULT 0,
  code_chunk_count INTEGER NOT NULL DEFAULT 0,
  indexed_at TEXT DEFAULT CURRENT_TIMESTAMP,
  last_accessed_at TEXT DEFAULT CURRENT_TIMESTAMP,
  access_count INTEGER NOT NULL DEFAULT 0,
  content_hash TEXT
);
`

// openPreMigrationDB returns an in-memory DB with the pre-migration schema
// and the given rows seeded into the sources table.
func openPreMigrationDB(t *testing.T, labels []string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(preMigrationDDL)
	require.NoError(t, err)

	for _, label := range labels {
		_, err := db.Exec(`INSERT INTO sources (label) VALUES (?)`, label)
		require.NoError(t, err)
	}
	return db
}

// hasColumn checks whether the given table has a column with the given name.
func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		require.NoError(t, rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk))
		if name == column {
			return true
		}
	}
	return false
}

func TestMigration017_AddsKindColumn(t *testing.T) {
	labels := []string{
		"execute:shell",
		"file:/tmp/foo.go",
		"batch:cmd1+cmd2",
		"docs-react",
		"my-notes",
	}
	db := openPreMigrationDB(t, labels)

	// Column should not exist before migration.
	assert.False(t, hasColumn(t, db, "sources", "kind"))

	require.NoError(t, applyMigrations(db))

	// Column must exist after migration.
	assert.True(t, hasColumn(t, db, "sources", "kind"))

	// Prefixed rows should be ephemeral.
	for _, label := range []string{"execute:shell", "file:/tmp/foo.go", "batch:cmd1+cmd2"} {
		var kind string
		err := db.QueryRow("SELECT kind FROM sources WHERE label = ?", label).Scan(&kind)
		require.NoError(t, err, "label: %s", label)
		assert.Equal(t, "ephemeral", kind, "label %q should be ephemeral", label)
	}

	// Non-prefixed rows should stay durable.
	for _, label := range []string{"docs-react", "my-notes"} {
		var kind string
		err := db.QueryRow("SELECT kind FROM sources WHERE label = ?", label).Scan(&kind)
		require.NoError(t, err, "label: %s", label)
		assert.Equal(t, "durable", kind, "label %q should be durable", label)
	}
}

func TestMigration017_Idempotent(t *testing.T) {
	labels := []string{"execute:shell", "docs-react"}
	db := openPreMigrationDB(t, labels)

	// Run migration twice.
	require.NoError(t, applyMigrations(db))
	require.NoError(t, applyMigrations(db))

	// Verify state is correct after second run.
	var ephCount, durCount int
	err := db.QueryRow("SELECT COUNT(*) FROM sources WHERE kind = 'ephemeral'").Scan(&ephCount)
	require.NoError(t, err)
	err = db.QueryRow("SELECT COUNT(*) FROM sources WHERE kind = 'durable'").Scan(&durCount)
	require.NoError(t, err)

	assert.Equal(t, 1, ephCount)
	assert.Equal(t, 1, durCount)
}

func TestMigration017_Concurrent(t *testing.T) {
	labels := []string{"execute:go", "file:main.go", "batch:a+b", "reference-docs", "user-notes"}

	// Use a file-backed DB because ":memory:" creates a separate DB per connection,
	// which breaks concurrent-goroutine tests with database/sql's connection pool.
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(preMigrationDDL)
	require.NoError(t, err)
	for _, label := range labels {
		_, err := db.Exec(`INSERT INTO sources (label) VALUES (?)`, label)
		require.NoError(t, err)
	}

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	start := make(chan struct{})

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = applyMigrations(db)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d", i)
	}

	// Column must exist.
	assert.True(t, hasColumn(t, db, "sources", "kind"))

	// Correct row distribution: 3 ephemeral (execute:, file:, batch:), 2 durable.
	var ephCount, durCount int
	err = db.QueryRow("SELECT COUNT(*) FROM sources WHERE kind = 'ephemeral'").Scan(&ephCount)
	require.NoError(t, err)
	err = db.QueryRow("SELECT COUNT(*) FROM sources WHERE kind = 'durable'").Scan(&durCount)
	require.NoError(t, err)

	assert.Equal(t, 3, ephCount, "ephemeral count")
	assert.Equal(t, 2, durCount, "durable count")
}

func TestMigration017_PostMigrationDBIsNoOp(t *testing.T) {
	// Simulate a DB that already has the kind column (created with current schema).
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(schemaSQL)
	require.NoError(t, err)

	// Seed some rows.
	_, err = db.Exec(`INSERT INTO sources (label, kind) VALUES ('execute:shell', 'ephemeral')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sources (label, kind) VALUES ('docs', 'durable')`)
	require.NoError(t, err)

	// Migration should be a no-op on a post-migration DB.
	require.NoError(t, applyMigrations(db))

	var total int
	err = db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&total)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
}
