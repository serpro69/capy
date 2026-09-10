# Implementation Guide: Port MCP Core to Go

> Design: [./design.md](./design.md)
> Reference implementation: `context-mode/` (TypeScript)

This document provides implementation-level details for each component of the capy port. It is meant to be read alongside `design.md` and used during implementation sessions.

Each section includes:
- Exact behavior to implement (with edge cases)
- Reference to the context-mode source code
- Go-specific implementation notes
- Files to create

---

## 1. Project Bootstrap

### 1.1 Go Module Init

```bash
go mod init github.com/serpro69/capy
```

Go version: 1.23+ (for `slices`, `maps`, `slog` packages).

### 1.2 Directory Structure

Create the full directory skeleton:

```
cmd/capy/main.go
internal/server/
internal/store/
internal/executor/
internal/security/
internal/hook/
internal/adapter/
internal/config/
internal/session/     # empty placeholder
```

### 1.3 Core Dependencies

```bash
go get github.com/mark3labs/mcp-go
go get github.com/mattn/go-sqlite3
go get github.com/pelletier/go-toml/v2
go get github.com/spf13/cobra
go get github.com/stretchr/testify  # dev dependency
```

### 1.4 CGO Build Tags

`mattn/go-sqlite3` requires CGO and must be built with FTS5 enabled:

```bash
CGO_ENABLED=1 go build -tags "fts5" ./cmd/capy/
```

Add to `Makefile` with targets: `build` (with CGO and fts5 tag), `test`, `vet`, `clean`.

### 1.5 Main Entry Point

```go
// cmd/capy/main.go
package main

import (
    "os"
    "github.com/spf13/cobra"
)

func main() {
    root := &cobra.Command{
        Use:   "capy",
        Short: "Context-aware MCP server for LLM context reduction",
        // Default behavior: run MCP server (for "command": "capy" in MCP config)
        RunE: serveCmd,
    }
    root.AddCommand(
        newServeCmd(),
        newHookCmd(),    // positional arg: pretooluse, posttooluse, precompact, sessionstart, userpromptsubmit
        newSetupCmd(),
        newDoctorCmd(),
        newCleanupCmd(),
    )
    if err := root.Execute(); err != nil {
        os.Exit(1)
    }
}
```

The default `RunE` maps to `serve` so that `"command": "capy"` in MCP config starts the server without requiring `capy serve`.

**Global flags on root:**
- `--project-dir` (string) — override project directory
- `--version` (bool) — print version and exit

**Version:** Use `-ldflags` at build time or `internal/version/version.go` with a `Version` variable.

### 1.6 Build Verification

The scaffolding task is complete when:
- `make build` produces a binary
- `./capy --version` prints the version
- `./capy serve`, `./capy hook pretooluse`, `./capy setup`, `./capy doctor`, `./capy cleanup` all run without panic (stubs are fine)
- `make vet` and `make test` pass

### Files to create

```
cmd/capy/main.go
internal/version/version.go
Makefile
go.mod
```

---

## 2. Configuration System

### 2.1 Config Struct

Create `internal/config/config.go`:

```go
type Config struct {
    Store    StoreConfig    `toml:"store"`
    Executor ExecutorConfig `toml:"executor"`
    Server   ServerConfig   `toml:"server"`
}

type StoreConfig struct {
    Path    string        `toml:"path"`
    Cleanup CleanupConfig `toml:"cleanup"`
}

type CleanupConfig struct {
    ColdThresholdDays int  `toml:"cold_threshold_days"` // default 30
    AutoPrune         bool `toml:"auto_prune"`          // default false
}

type ExecutorConfig struct {
    Timeout        int `toml:"timeout"`          // seconds, default 30 (converted to ms internally)
    MaxOutputBytes int `toml:"max_output_bytes"` // default 102400
}

type ServerConfig struct {
    LogLevel string `toml:"log_level"` // default "info"
}
```

Implement `DefaultConfig()` returning sensible defaults.

### 2.2 Loading with Precedence

Create `internal/config/loader.go`:

```go
func Load(projectDir string) (*Config, error) {
    cfg := DefaultConfig()

    // Load in reverse precedence order (lower priority first, higher overwrites)
    xdgPath := xdgConfigPath() // $XDG_CONFIG_HOME or ~/.config
    loadAndMerge(cfg, filepath.Join(xdgPath, "capy", "config.toml"))
    loadAndMerge(cfg, filepath.Join(projectDir, ".capy", "config.toml"))
    loadAndMerge(cfg, filepath.Join(projectDir, ".capy.toml"))

    return cfg, nil
}
```

**Merging:** Only non-zero values from higher-priority configs override lower-priority ones. Use `pelletier/go-toml/v2` for unmarshaling.

Missing files are silently skipped. Malformed TOML returns an error.

### 2.3 Project Root Detection

Create `internal/config/paths.go`:

```go
func DetectProjectRoot() string {
    // 1. CLAUDE_PROJECT_DIR env var (set by Claude Code)
    if dir := os.Getenv("CLAUDE_PROJECT_DIR"); dir != "" {
        return dir
    }
    // 2. git rev-parse --show-toplevel
    // 3. Walk up from cwd looking for .git/, .capy.toml, .capy/
    // 4. Fallback: cwd
}
```

### 2.4 DB Path Resolution

```go
func (c *Config) ResolveDBPath(projectDir string) string {
    if c.Store.Path != "" {
        if filepath.IsAbs(c.Store.Path) {
            return c.Store.Path
        }
        return filepath.Join(projectDir, c.Store.Path)
    }
    // Default: XDG
    dataHome := os.Getenv("XDG_DATA_HOME")
    if dataHome == "" {
        home, _ := os.UserHomeDir()
        dataHome = filepath.Join(home, ".local", "share")
    }
    hash := ProjectHash(projectDir)
    return filepath.Join(dataHome, "capy", hash, "knowledge.db")
}

func ProjectHash(dir string) string {
    abs, _ := filepath.Abs(dir)
    h := sha256.Sum256([]byte(abs))
    return hex.EncodeToString(h[:8]) // 16 hex chars (8 bytes)
}
```

### Files to create

```
internal/config/config.go   — Config struct, defaults
internal/config/loader.go   — Load, merge
internal/config/paths.go    — ProjectHash, ResolveDBPath, DetectProjectRoot
```

---

## 3. ContentStore Implementation

**Reference files:**
- `context-mode/src/store.ts` — complete ContentStore implementation
- `context-mode/src/db-base.ts` — SQLite base class (WAL mode, prepared statements)
- `context-mode/docs/llms-full.txt` lines 231-378

### 3.1 Store Struct

```go
// internal/store/store.go
package store

type ContentStore struct {
    db          *sql.DB
    dbPath      string
    initialized bool
    mu          sync.RWMutex

    // Prepared statements (cached after first use)
    stmtInsertSource        *sql.Stmt
    stmtInsertSourceEmpty   *sql.Stmt
    stmtInsertChunk         *sql.Stmt
    stmtInsertTrigram       *sql.Stmt
    stmtInsertVocab         *sql.Stmt
    stmtDeleteChunksByLabel *sql.Stmt
    stmtDeleteTrigramByLabel *sql.Stmt
    stmtDeleteSourcesByLabel *sql.Stmt
    stmtSearchPorter        *sql.Stmt
    stmtSearchPorterFiltered *sql.Stmt
    stmtSearchTrigram       *sql.Stmt
    stmtSearchTrigramFiltered *sql.Stmt
    stmtFuzzyVocab          *sql.Stmt
    stmtListSources         *sql.Stmt
    stmtChunksBySource      *sql.Stmt
    stmtSourceChunkCount    *sql.Stmt
    stmtChunkContent        *sql.Stmt
    stmtStats               *sql.Stmt
}
```

**Lazy initialization:** The store must not open the DB file until the first operation that needs it. Implement via a `getDB()` method that initializes on first call. The MCP server uses `sync.Once` to guard this.

### 3.2 Schema Creation

Open SQLite with pragmas via connection string:

```go
func (s *ContentStore) init() error {
    db, err := sql.Open("sqlite3", s.dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=ON")
    // ...
}
```

Run all `CREATE TABLE IF NOT EXISTS` and `CREATE VIRTUAL TABLE IF NOT EXISTS` statements from the design doc's schema. Schema includes:
- `sources` table with `content_type`, `last_accessed_at`, `access_count`, `content_hash` columns
- `chunks` FTS5 table (Porter tokenizer)
- `chunks_trigram` FTS5 table (trigram tokenizer)
- `vocabulary` table (word TEXT PRIMARY KEY, no frequency column)

**Important:** `_busy_timeout=5000` prevents `SQLITE_BUSY` errors when multiple processes access the same persistent DB.

### 3.3 Indexing

#### Index (markdown)

Port `#chunkMarkdown()` from `context-mode/src/store.ts`.

```go
func (s *ContentStore) Index(content, label, contentType string) (*IndexResult, error)
```

1. Compute `content_hash` (SHA-256 of content)
2. Check if source with same `label` and `content_hash` exists — if so, update `last_accessed_at` and return (dedup)
3. If same label but different hash — delete old source + chunks (re-index)
4. Auto-detect content type if not provided
5. Chunk the content using the appropriate strategy
6. Insert source row, chunks into both FTS5 tables (in transaction)
7. Extract vocabulary
8. Return `IndexResult{SourceID, Label, TotalChunks, CodeChunks}`

**Dedup:** Atomic delete + re-insert within a transaction. Delete previous source with same label before inserting new content. Prevents stale results in iterative workflows.

Reference: `context-mode/src/store.ts` — `index()`, `#insertChunks()`.

#### IndexPlainText

Port `#chunkPlainText()`. Two-phase strategy:

1. Try blank-line splitting (`\n\s*\n`). Use if result has 3–200 sections with each < 5000 bytes. Title = first line (up to 80 chars) or "Section N".
2. Fallback: 20-line groups with 2-line overlap. Step size = 18 lines.
3. Single chunk titled "Output" if total lines ≤ linesPerChunk.

#### IndexJSON

Port `#walkJSON()`. Recursive walk of parsed JSON:

1. Parse JSON string into `interface{}` (Go's `json.Unmarshal`)
2. For objects with nested structure: always recurse (even if subtree fits in one chunk) so key paths become searchable titles
3. For flat small objects (< 4096 bytes, no nested objects/arrays): emit as single chunk
4. For arrays: batch items by accumulated byte size up to 4096. Check identity fields (`id`, `name`, `title`, `path`, `slug`, `key`, `label`) for meaningful titles
5. On parse failure: fall back to `IndexPlainText`

#### Vocabulary Extraction

During indexing, extract words for fuzzy search:

```go
func (s *ContentStore) extractAndStoreVocabulary(content string) {
    // Split on [^\p{L}\p{N}_-]+ (Unicode-aware word boundaries)
    // Filter: len >= 3, not in STOPWORDS
    // Deduplicate
    // INSERT OR IGNORE INTO vocabulary (word) VALUES (?)
}
```

#### Content Type Detection

```go
func detectContentType(content string) string {
    // 1. Check for valid JSON first (json.Valid)
    // 2. Check for markdown indicators (headings, code fences, links)
    // 3. Default to "plaintext"
}
```

#### Code Block Detection

After building each chunk, scan for fenced code blocks via `` ```\w*\n[\s\S]*?``` `` regex. Set `content_type = "code"` if found, `"prose"` otherwise. Track `code_chunk_count` in `IndexResult`.

### 3.4 Search

#### searchWithFallback

Port the 8-layer search. Each layer runs a SQL query and returns if results found:

```go
func (s *ContentStore) SearchWithFallback(query string, limit int, source string) []SearchResult {
    // Layer 1a: Porter + AND
    // Layer 1b: Porter + OR
    // Layer 2a: Trigram + AND
    // Layer 2b: Trigram + OR
    // Layer 3: Fuzzy correction → re-search all 4 above
    // Stop at first layer returning results
}
```

#### Porter Search SQL

```sql
SELECT s.label, c.title, c.content, c.source_id, c.content_type,
       highlight(chunks, 1, char(2), char(3)) AS highlighted,
       bm25(chunks, 2.0, 1.0) AS rank
FROM chunks c
JOIN sources s ON s.id = c.source_id
WHERE chunks MATCH ?
ORDER BY rank
LIMIT ?
```

When `source` filter is provided, add: `AND s.label LIKE '%' || ? || '%'`

**AND mode:** `sanitizeQuery` quotes each word: `"word1" "word2"`
**OR mode:** `sanitizeQuery` joins with OR: `"word1" OR "word2"`

#### Trigram Search SQL

Same structure but against `chunks_trigram` table. **Important:** Trigram queries must be sanitized — keep only `[a-zA-Z0-9 _-]`. Minimum 3-char words.

#### Levenshtein Distance

Standard DP implementation. Port from `context-mode/src/store.ts`:

```go
func levenshteinDistance(a, b string) int {
    a = strings.ToLower(a)
    b = strings.ToLower(b)
    // Standard 2D DP matrix
}

func maxEditDistance(wordLen int) int {
    switch {
    case wordLen <= 4:  return 1
    case wordLen <= 12: return 2
    default:            return 3
    }
}
```

#### fuzzyCorrect

For each word in the query:
1. Query vocabulary: `SELECT word FROM vocabulary WHERE length(word) BETWEEN ? AND ?`
2. If exact match found in vocabulary, return nil (no correction needed)
3. Compute Levenshtein distance for each candidate
4. Return closest match within `maxEditDistance` threshold, or original word if no match

### 3.5 Smart Snippet Extraction

Create `internal/server/snippet.go`:

```go
func ExtractSnippet(content, query string, maxLen int, highlighted string) string {
    // 1. If content <= maxLen, return as-is
    // 2. Parse FTS5 STX/ETX markers (char(2)/char(3)) from highlighted text
    //    to find match positions in the clean (marker-free) content
    // 3. Fallback: find positions via strings.Index on lowercase query terms
    // 4. Build 300-character windows around each position
    // 5. Merge overlapping windows
    // 6. Collect windows until maxLen budget reached
    // 7. Add "…" markers for truncated regions
}

func positionsFromHighlight(highlighted string) []int {
    // Walk highlighted string, track clean offset
    // STX marks start of match, ETX marks end
    // Record clean offset at each STX
}
```

Reference: `context-mode/src/server.ts` — `extractSnippet()`, `positionsFromHighlight()`.

### 3.6 Distinctive Terms

```go
func (s *ContentStore) GetDistinctiveTerms(sourceID int64, maxTerms int) []string {
    // 1. Get chunk_count for source. If < 3, return empty.
    // 2. Stream chunks via stmtChunkContent, count document frequency per word
    //    (split on [^\p{L}\p{N}_-]+, filter 3+ chars, not stopword)
    // 3. Filter: 2 <= appearances <= 40% of chunks
    // 4. Score: IDF + lengthBonus + identifierBonus
    //    - IDF: log(totalChunks / count)
    //    - lengthBonus: min(wordLength / 20, 0.5)
    //    - identifierBonus: 1.5 for underscores, 0.8 for length >= 12
    // 5. Sort by score descending, return top maxTerms
}
```

### 3.7 Progressive Throttling

Track search calls in a sliding 60-second window:

```go
type searchThrottle struct {
    mu          sync.Mutex
    calls       int
    windowStart time.Time
}

func (t *searchThrottle) check() (effectiveLimit int, err error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    now := time.Now()
    if now.Sub(t.windowStart) > 60*time.Second {
        t.calls = 0
        t.windowStart = now
    }
    t.calls++
    switch {
    case t.calls <= 3:  return 2, nil
    case t.calls <= 8:  return 1, nil  // + warning in response
    default:            return 0, errors.New("use batch_execute")
    }
}
```

### 3.8 Freshness Metadata

On every search hit, update the source's freshness in a background goroutine:

```sql
UPDATE sources SET last_accessed_at = CURRENT_TIMESTAMP, access_count = access_count + 1 WHERE id = ?
```

On re-index of a source with same label:
1. Compute SHA-256 hash of new content
2. Compare with stored `content_hash`
3. If different: delete old chunks, re-index, update hash and `indexed_at`
4. If same: update `last_accessed_at` only (content unchanged)

### 3.9 Cleanup

```go
func (s *ContentStore) Cleanup(maxAgeDays int, dryRun bool) ([]SourceInfo, error) {
    cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
    // SELECT sources WHERE last_accessed_at < cutoff AND access_count = 0
    if !dryRun {
        // DELETE FROM chunks WHERE source_id = ?
        // DELETE FROM chunks_trigram WHERE source_id = ?
        // DELETE FROM sources WHERE id = ?
        // (vocabulary is shared across sources — do not delete)
    }
    return pruned, nil
}

func (s *ContentStore) Stats() (*StoreStats, error) {
    // source count, chunk count, DB file size, per-tier counts (hot/warm/cold)
}
```

### 3.10 Close

```go
func (s *ContentStore) Close() {
    // Finalize all prepared statements
    // PRAGMA wal_checkpoint(TRUNCATE)
    // Close DB
}
```

### Files to create

```
internal/store/store.go       — ContentStore struct, constructor, getDB(), Close()
internal/store/schema.go      — initSchema(), SQL DDL
internal/store/index.go       — Index(), IndexPlainText(), IndexJSON(), insertChunks()
internal/store/chunk.go       — chunkMarkdown(), chunkPlainText(), walkJSON(), chunkJSONArray()
internal/store/search.go      — search(), searchTrigram(), SearchWithFallback()
internal/store/vocabulary.go  — extractAndStoreVocabulary(), fuzzyCorrect(), levenshteinDistance()
internal/store/terms.go       — GetDistinctiveTerms()
internal/store/cleanup.go     — Cleanup(), ClassifySources(), Stats()
internal/store/detect.go      — detectContentType()
internal/store/stopwords.go   — STOPWORDS set, IsStopword()
internal/store/types.go       — SearchResult, SourceInfo, StoreStats, IndexResult, Chunk
```

---

## 4. PolyglotExecutor Implementation

**Reference files:**
- `context-mode/src/executor.ts` — PolyglotExecutor class
- `context-mode/src/runtime.ts` — runtime detection
- `context-mode/src/truncate.ts` — smart truncation
- `context-mode/src/exit-classify.ts` — exit code classification

### 4.1 Executor Struct

```go
// internal/executor/executor.go
package executor

type PolyglotExecutor struct {
    runtimes        map[Language]string // detected runtime paths
    projectDir      string
    maxOutputBytes  int                // default 102400
    hardCapBytes    int64              // default 100 * 1024 * 1024
    mu              sync.RWMutex
    detected        bool
    backgroundPids  map[int]struct{}   // tracked for cleanup
    bgMu            sync.Mutex
}

type Language string

const (
    JavaScript Language = "javascript"
    TypeScript Language = "typescript"
    Python     Language = "python"
    Shell      Language = "shell"
    Ruby       Language = "ruby"
    Go         Language = "go"
    Rust       Language = "rust"
    PHP        Language = "php"
    Perl       Language = "perl"
    R          Language = "r"
    Elixir     Language = "elixir"
)

type ExecResult struct {
    Stdout       string
    Stderr       string
    ExitCode     int
    TimedOut     bool
    Killed       bool   // hard cap exceeded
    Backgrounded bool
    PID          int    // only set if backgrounded
}
```

### 4.2 Runtime Detection

```go
// internal/executor/runtime.go

var runtimeCandidates = map[Language][]string{
    JavaScript: {"bun", "node"},
    TypeScript: {"bun", "tsx", "ts-node"},
    Python:     {"python3", "python"},
    Shell:      {"bash", "sh"},
    Ruby:       {"ruby"},
    Go:         {"go"},
    Rust:       {"rustc"},
    PHP:        {"php"},
    Perl:       {"perl"},
    R:          {"Rscript", "r"},
    Elixir:     {"elixir"},
}

func (e *PolyglotExecutor) detectRuntimes() {
    // For each language, try exec.LookPath on candidates in order
    // First match wins. Cache in e.runtimes.
    // Call lazily via sync.Once on first Execute().
}
```

### 4.3 Process Spawning

```go
func (e *PolyglotExecutor) Execute(ctx context.Context, req ExecRequest) (*ExecResult, error) {
    // 1. Detect runtimes if not yet done
    // 2. Validate language is supported
    // 3. Create temp directory (os.MkdirTemp("", "capy-exec-*"))
    // 4. Apply auto-wrapping (Go: package main, PHP: <?php, Elixir: BEAM paths)
    // 5. Write script file with correct extension
    //    Shell scripts get 0o700 permissions
    // 6. Build command (Rust: special two-step compile+run)
    // 7. Set working directory: projectDir for shell, tmpDir for others
    // 8. Set environment via buildSafeEnv() (denylist approach)
    // 9. Set process group: cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
    // 10. Capture stdout/stderr with hard cap monitoring (100 MB)
    // 11. Handle timeout via context.WithTimeout
    //     - Normal: kill process group on timeout
    //     - Background: detach process, record PID, return partial output
    // 12. Apply smart truncation to output
    // 13. Clean up temp directory (defer, skip if backgrounded)
    // 14. Return ExecResult
}
```

#### Script Filenames

| Language | Filename | Invocation |
|----------|----------|------------|
| JavaScript | `script.js` | `bun run script.js` or `node script.js` |
| TypeScript | `script.ts` | `bun run script.ts` or `tsx script.ts` |
| Python | `script.py` | `python3 script.py` |
| Shell | `script.sh` | `bash script.sh` |
| Ruby | `script.rb` | `ruby script.rb` |
| Go | `main.go` | `go run main.go` |
| Rust | `main.rs` | `rustc main.rs -o main && ./main` |
| PHP | `script.php` | `php script.php` |
| Perl | `script.pl` | `perl script.pl` |
| R | `script.R` | `Rscript script.R` |
| Elixir | `script.exs` | `elixir script.exs` |

### 4.4 Auto-Wrapping

```go
// internal/executor/wrap.go

func autoWrap(lang Language, code, projectDir string) string {
    switch lang {
    case Go:
        if !strings.Contains(code, "package ") {
            return fmt.Sprintf("package main\n\nimport \"fmt\"\n\nfunc main() {\n%s\n}\n", code)
        }
    case PHP:
        if !strings.HasPrefix(strings.TrimSpace(code), "<?") {
            return "<?php\n" + code
        }
    case Elixir:
        if mixExists(projectDir) {
            return `Path.wildcard("_build/dev/lib/*/ebin") |> Enum.each(&Code.prepend_path/1)` + "\n" + code
        }
    }
    return code
}
```

### 4.5 FILE_CONTENT Injection (execute_file)

```go
func injectFileContent(lang Language, code, absPath string) string {
    escaped := strconv.Quote(absPath) // JSON-safe quoting
    switch lang {
    case JavaScript, TypeScript:
        return fmt.Sprintf("const FILE_CONTENT_PATH = %s;\nconst file_path = FILE_CONTENT_PATH;\nconst FILE_CONTENT = require(\"fs\").readFileSync(FILE_CONTENT_PATH, \"utf-8\");\n%s", escaped, code)
    case Python:
        return fmt.Sprintf("FILE_CONTENT_PATH = %s\nfile_path = FILE_CONTENT_PATH\nwith open(FILE_CONTENT_PATH, \"r\", encoding=\"utf-8\") as _f:\n    FILE_CONTENT = _f.read()\n%s", escaped, code)
    case Shell:
        // Single-quote the path to prevent expansion
        sq := "'" + strings.ReplaceAll(absPath, "'", "'\\''") + "'"
        return fmt.Sprintf("FILE_CONTENT_PATH=%s\nfile_path=%s\nFILE_CONTENT=$(cat %s)\n%s", sq, sq, sq, code)
    case Ruby:
        return fmt.Sprintf("FILE_CONTENT_PATH = %s\nfile_path = FILE_CONTENT_PATH\nFILE_CONTENT = File.read(FILE_CONTENT_PATH, encoding: \"utf-8\")\n%s", escaped, code)
    case Go:
        return fmt.Sprintf("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar FILE_CONTENT_PATH = %s\nvar file_path = FILE_CONTENT_PATH\n\nfunc main() {\n\tb, _ := os.ReadFile(FILE_CONTENT_PATH)\n\tFILE_CONTENT := string(b)\n\t_ = FILE_CONTENT\n\t_ = fmt.Sprint()\n%s\n}\n", escaped, code)
    case Rust:
        return fmt.Sprintf("#![allow(unused_variables)]\nuse std::fs;\n\nfn main() {\n    let file_content_path = %s;\n    let file_path = file_content_path;\n    let file_content = fs::read_to_string(file_content_path).unwrap();\n%s\n}\n", escaped, code)
    case PHP:
        return fmt.Sprintf("<?php\n$FILE_CONTENT_PATH = %s;\n$file_path = $FILE_CONTENT_PATH;\n$FILE_CONTENT = file_get_contents($FILE_CONTENT_PATH);\n%s", escaped, code)
    case Perl:
        return fmt.Sprintf("my $FILE_CONTENT_PATH = %s;\nmy $file_path = $FILE_CONTENT_PATH;\nopen(my $fh, '<:encoding(UTF-8)', $FILE_CONTENT_PATH) or die \"Cannot open: $!\";\nmy $FILE_CONTENT = do { local $/; <$fh> };\nclose($fh);\n%s", escaped, code)
    case R:
        return fmt.Sprintf("FILE_CONTENT_PATH <- %s\nfile_path <- FILE_CONTENT_PATH\nFILE_CONTENT <- readLines(FILE_CONTENT_PATH, warn=FALSE, encoding=\"UTF-8\")\nFILE_CONTENT <- paste(FILE_CONTENT, collapse=\"\\n\")\n%s", escaped, code)
    case Elixir:
        return fmt.Sprintf("file_content_path = %s\nfile_path = file_content_path\nfile_content = File.read!(file_content_path)\n%s", escaped, code)
    }
    return code
}
```

Reference: `context-mode/src/executor.ts` — `#wrapWithFileContent()`. Port the exact boilerplate for each language.

### 4.6 Smart Truncation

```go
// internal/executor/truncate.go

const (
    MaxOutputBytes = 102400      // 100 KB
    HardCapBytes   = 100 * 1024 * 1024 // 100 MB
    HeadRatio      = 0.6
    TailRatio      = 0.4
)

func SmartTruncate(output string, maxBytes int) string {
    if len(output) <= maxBytes {
        return output
    }

    lines := strings.Split(output, "\n")
    headBudget := int(float64(maxBytes) * HeadRatio)
    tailBudget := maxBytes - headBudget

    // Collect head lines until headBudget exhausted
    // Collect tail lines (from end) until tailBudget exhausted
    // Insert separator with truncation stats
    // Return head + separator + tail
}
```

Line-boundary splitting ensures no UTF-8 corruption. Go's `len()` returns byte length for strings, which is correct for UTF-8 byte budgets.

### 4.7 Hard Cap Streaming

Monitor combined stdout+stderr byte count during execution. Kill process group if threshold exceeded:

```go
// Read stdout/stderr via goroutines with byte counting
// If atomic.LoadInt64(&totalBytes) > HardCapBytes:
//   syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
//   Append "[output capped at 100MB — process killed]" to stderr
```

### 4.8 Exit Code Classification

```go
// internal/executor/exit_classify.go

type ExitClassification struct {
    IsError bool
    Output  string
}

func ClassifyNonZeroExit(language string, exitCode int, stdout, stderr string) ExitClassification {
    // Shell exit 1 with non-empty stdout = soft failure (e.g., grep no matches)
    isSoftFail := language == "shell" && exitCode == 1 && strings.TrimSpace(stdout) != ""
    if isSoftFail {
        return ExitClassification{IsError: false, Output: stdout}
    }
    return ExitClassification{
        IsError: true,
        Output:  fmt.Sprintf("Exit code: %d\n\nstdout:\n%s\n\nstderr:\n%s", exitCode, stdout, stderr),
    }
}
```

### 4.9 Sandbox Environment (Denylist)

```go
// internal/executor/env.go

func BuildSafeEnv(tmpDir string) []string {
    realHome := os.Getenv("HOME")

    // DENIED: ~50 env vars organized by category (see design.md § 4.7)
    denied := map[string]bool{
        "BASH_ENV": true, "ENV": true, "PROMPT_COMMAND": true, /* ... full list ... */
    }

    // Start with parent env, strip dangerous vars and BASH_FUNC_* prefixed vars
    env := make(map[string]string)
    for _, entry := range os.Environ() {
        key, val, _ := strings.Cut(entry, "=")
        if !denied[key] && !strings.HasPrefix(key, "BASH_FUNC_") {
            env[key] = val
        }
    }

    // Apply sandbox overrides
    env["TMPDIR"] = tmpDir
    env["HOME"] = realHome
    env["LANG"] = "en_US.UTF-8"
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    env["PYTHONUNBUFFERED"] = "1"
    env["PYTHONUTF8"] = "1"
    env["NO_COLOR"] = "1"

    if env["PATH"] == "" {
        env["PATH"] = "/usr/local/bin:/usr/bin:/bin"
    }

    // SSL cert detection
    if env["SSL_CERT_FILE"] == "" {
        for _, p := range []string{"/etc/ssl/cert.pem", "/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt"} {
            if _, err := os.Stat(p); err == nil {
                env["SSL_CERT_FILE"] = p
                break
            }
        }
    }

    // Convert to []string for exec.Cmd.Env
    result := make([]string, 0, len(env))
    for k, v := range env {
        result = append(result, k+"="+v)
    }
    return result
}
```

Port the exact DENIED set from `context-mode/src/executor.ts` lines 325-394.

### 4.10 Background Mode

```go
func (e *PolyglotExecutor) CleanupBackgrounded() {
    e.bgMu.Lock()
    defer e.bgMu.Unlock()
    for pid := range e.backgroundPids {
        syscall.Kill(-pid, syscall.SIGTERM)
    }
    e.backgroundPids = make(map[int]struct{})
}
```

### Files to create

```
internal/executor/executor.go      — PolyglotExecutor struct, Execute(), ExecuteFile()
internal/executor/runtime.go       — detectRuntimes(), buildCommand(), Language type
internal/executor/truncate.go      — SmartTruncate()
internal/executor/wrap.go          — autoWrap(), injectFileContent()
internal/executor/env.go           — BuildSafeEnv()
internal/executor/exit_classify.go — ClassifyNonZeroExit()
internal/executor/types.go         — ExecRequest, ExecResult
```

---

## 5. Security Implementation

**Reference files:**
- `context-mode/src/security.ts` — full implementation
- `context-mode/docs/llms-full.txt` lines 462-553

### 5.1 Settings Parsing

```go
// internal/security/settings.go

type SecurityPolicy struct {
    Deny  []string
    Allow []string
    Ask   []string
}

func ReadBashPolicies(projectDir, globalSettingsPath string) []SecurityPolicy {
    // Load in precedence order (most local first):
    // 1. .claude/settings.local.json
    // 2. .claude/settings.json
    // 3. ~/.claude/settings.json (or globalSettingsPath if non-empty)
    // Extract only Bash(...) patterns from permissions.deny/allow/ask
    // Missing/invalid files silently skipped
}

func ReadToolDenyPatterns(toolName, projectDir, globalSettingsPath string) [][]string {
    // Same 3-tier settings files
    // Extract globs from ToolName(glob) patterns matching toolName
    // Returns array of arrays (one per settings file)
}
```

### 5.2 Pattern Matching

```go
// internal/security/glob.go

func globToRegex(glob string, caseInsensitive bool) *regexp.Regexp {
    // Colon format: "command:argsGlob" → /^command(\sargsRegex)?$/
    // Plain glob: "sudo *" → /^sudo .*$/
    // * → .* (matches anything including whitespace), other regex chars escaped
}

func fileGlobToRegex(glob string, caseInsensitive bool) *regexp.Regexp {
    // ** → (.*/)?  (zero or more directory segments)
    // * → [^/]*    (anything except path separator)
    // ? → [^/]     (single non-separator char)
}

func parseBashPattern(pattern string) string {
    // Extract glob from "Bash(glob)" → returns glob, or "" if not Bash pattern
}

func parseToolPattern(pattern string) (tool, glob string) {
    // Extract from "ToolName(glob)" → returns tool name and glob
}
```

### 5.3 Command Splitting

```go
// internal/security/split.go

func SplitChainedCommands(command string) []string {
    // Split on &&, ||, ;, | operators
    // Quote-aware: respect single quotes, double quotes, backticks
    // "echo 'hello && world' && sudo rm" → ["echo 'hello && world'", "sudo rm"]
}
```

### 5.4 Shell-Escape Detection

```go
// internal/security/shell_escape.go

var shellEscapePatterns = map[string][]*regexp.Regexp{
    "python": {
        regexp.MustCompile(`os\.system\(\s*(['"])(.*?)\1\s*\)`),
        regexp.MustCompile(`subprocess\.(?:run|call|Popen|check_output|check_call)\(\s*(['"])(.*?)\1`),
    },
    "javascript": { /* execSync, spawn patterns */ },
    "typescript": { /* same as javascript */ },
    "ruby":       { /* system, backtick patterns */ },
    "go":         { /* exec.Command pattern */ },
    "php":        { /* shell_exec, exec, system, passthru, proc_open */ },
    "rust":       { /* Command::new pattern */ },
}

func ExtractShellCommands(code, language string) []string {
    // Apply patterns for the language
    // Python: also extract subprocess list form ["rm", "-rf", "/"] → "rm -rf /"
    // Return extracted command strings
}
```

### 5.5 Evaluation Functions

```go
// internal/security/eval.go

type CommandDecision struct {
    Decision       string // "allow", "deny", or "ask"
    MatchedPattern string
}

// Server-side: deny-only (no "ask" prompting in MCP server)
func EvaluateCommandDenyOnly(command string, policies []SecurityPolicy) CommandDecision {
    segments := SplitChainedCommands(command)
    for _, seg := range segments {
        for _, policy := range policies {
            if match := matchesAnyBashPattern(seg, policy.Deny); match != "" {
                return CommandDecision{Decision: "deny", MatchedPattern: match}
            }
        }
    }
    return CommandDecision{Decision: "allow"}
}

// Hook-side: full deny > ask > allow evaluation
func EvaluateCommand(command string, policies []SecurityPolicy) CommandDecision {
    // Check deny (on each chained segment) → ask (on full command) → allow
    // Default: CommandDecision{Decision: "ask"}
}

// File path checking
func EvaluateFilePath(filePath string, denyGlobs [][]string) (denied bool, matchedPattern string) {
    // Normalize backslashes to forward slashes
    // Match against fileGlobToRegex patterns
}
```

### Files to create

```
internal/security/settings.go     — ReadBashPolicies(), ReadToolDenyPatterns()
internal/security/glob.go         — globToRegex(), fileGlobToRegex(), parseBashPattern(), parseToolPattern()
internal/security/split.go        — SplitChainedCommands()
internal/security/shell_escape.go — ExtractShellCommands()
internal/security/eval.go         — EvaluateCommandDenyOnly(), EvaluateCommand(), EvaluateFilePath()
```

---

## 6. MCP Server Implementation

**Reference files:**
- `context-mode/src/server.ts` — full server implementation
- `mcp-go` documentation for tool registration API

### 6.1 Server Struct

```go
// internal/server/server.go
package server

type Server struct {
    mcpServer  *mcpserver.MCPServer  // github.com/mark3labs/mcp-go/server
    store      *store.ContentStore
    executor   *executor.PolyglotExecutor
    security   []security.SecurityPolicy
    config     *config.Config
    stats      *SessionStats
    throttle   *searchThrottle
    storeMu    sync.Once
    projectDir string
}
```

### 6.2 Tool Registration

Register all 9 tools with `mcp-go` tool registration API (JSON Schema for each tool's parameters). Reference `context-mode/src/server.ts` for exact schema definitions.

### 6.3 Tool Handlers

Each handler follows the pattern:

```go
func (s *Server) handleExecute(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // 1. Parse parameters from request.Params.Arguments (with input coercion)
    // 2. Security check (shell: EvaluateCommandDenyOnly, non-shell: ExtractShellCommands + check each)
    // 3. Execute via s.executor
    // 4. Classify non-zero exit codes
    // 5. Auto-index if intent provided and output > 5KB (INTENT_SEARCH_THRESHOLD)
    // 6. Track stats (bytesReturned, bytesIndexed)
    // 7. Return result
}
```

#### Auto-indexing flow (execute with intent)

```go
if intent != "" && len(output) > 5000 {
    st := s.getStore()
    indexed := st.IndexPlainText(output, fmt.Sprintf("execute:%s", language))
    s.stats.AddBytesIndexed(int64(len(output)))

    results := st.SearchWithFallback(intent, 5, indexed.Label)
    if len(results) > 0 {
        // Return section titles + first-line previews + distinctive terms
        return formatIntentResults(results, indexed, len(output)), nil
    }
    // No matches — return distinctive terms for follow-up
    terms := st.GetDistinctiveTerms(indexed.SourceID, 40)
    return formatNoMatchResults(indexed, len(output), terms), nil
}
```

#### batch_execute flow

```go
func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // 1. Parse commands array and queries array (with input coercion)
    // 2. Security check each command individually
    // 3. Execute each command SEPARATELY (not concatenated)
    //    - Each gets own smartTruncate budget
    //    - Each gets remaining timeout (totalTimeout - elapsed)
    //    - Shell only, with "2>&1" appended
    //    - Output prefixed with "# label\n"
    // 4. Index combined output via store.Index() (markdown chunking)
    // 5. Build section inventory via store.GetChunksBySource()
    // 6. Search each query with three-tier fallback
    //    - Scoped to batch source label first
    //    - Global fallback if no scoped results (with cross-source warning)
    //    - 3000-byte snippets (larger than search's 1500)
    // 7. Return inventory + search results (80 KB output cap)
    //    - Include distinctive terms for follow-up
}
```

#### fetch_and_index flow

```go
func (s *Server) handleFetchAndIndex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // Native Go HTTP (no subprocess needed):
    // 1. http.Get(url) with timeout, redirect limit, User-Agent
    // 2. Read response body, detect Content-Type from header
    // 3. Route:
    //    - HTML → markdown via JohannesKaufmann/html-to-markdown (strip script, style, nav, header, footer) → store.Index()
    //    - JSON → store.IndexJSON()
    //    - Everything else → store.IndexPlainText()
    // 4. Track bytesIndexed (response body size)
    // 5. Return 3072-byte preview + "use search() for full content"
}
```

#### Input coercion

```go
func coerceStringArray(val interface{}) []string {
    // If val is already []string, return
    // If val is a JSON string, try json.Unmarshal into []string
    // If val is []interface{}, convert elements to strings
}

func coerceCommandsArray(val interface{}) []CommandInput {
    // Parse stringified JSON arrays
    // Coerce plain string commands into {label: "cmd_N", command: string}
}
```

### 6.4 Lazy Store Initialization

```go
func (s *Server) getStore() *store.ContentStore {
    s.storeMu.Do(func() {
        dbPath := s.config.ResolveDBPath(s.projectDir)
        s.store = store.NewContentStore(dbPath, s.projectDir)
    })
    return s.store
}
```

### 6.5 Session Statistics

```go
// internal/server/stats.go

type SessionStats struct {
    SessionStart   time.Time
    Calls          map[string]int
    BytesReturned  map[string]int64
    BytesIndexed   int64
    BytesSandboxed int64
    mu             sync.Mutex
}

func (s *SessionStats) TrackResponse(toolName string, responseBytes int64) { /* ... */ }
func (s *SessionStats) AddBytesIndexed(bytes int64) { /* ... */ }
```

### 6.6 Lifecycle Guard

```go
// internal/server/lifecycle.go

func StartLifecycleGuard(onShutdown func()) func() {
    originalPpid := os.Getppid()
    var once sync.Once

    shutdown := func() {
        once.Do(onShutdown)
    }

    // P0: Periodic parent PID check (every 30s)
    ticker := time.NewTicker(30 * time.Second)
    go func() {
        for range ticker.C {
            ppid := os.Getppid()
            if ppid != originalPpid || ppid == 0 || ppid == 1 {
                shutdown()
                return
            }
        }
    }()

    // P0: OS signals
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
    go func() {
        <-sigCh
        shutdown()
    }()

    // Return cleanup function (signal goroutine exits via done channel)
    done := make(chan struct{})
    // ... signal goroutine uses select { case <-sigCh: ... case <-done: }
    return func() {
        close(done)
        ticker.Stop()
        signal.Stop(sigCh)
    }
}
// NOTE: Stdin close detection is handled by the mcp-go stdio transport
// (StdioServer.Listen returns when stdin closes), not by the lifecycle guard.

```

### 6.7 Serve Command Integration

Wire into `cmd/capy/main.go`:
1. Load config (using project dir detection)
2. Load security rules
3. Create executor
4. Create server (store is lazy)
5. Start lifecycle guard
6. Start MCP server on stdin/stdout (blocks)
7. On shutdown: close DB, kill backgrounded processes

### Files to create

```
internal/server/server.go         — Server struct, constructor, getStore(), Serve()
internal/server/stats.go          — SessionStats
internal/server/tools.go          — tool registration (JSON Schema definitions)
internal/server/tool_execute.go   — capy_execute handler
internal/server/tool_execute_file.go — capy_execute_file handler
internal/server/tool_batch.go     — capy_batch_execute handler
internal/server/tool_index.go     — capy_index handler
internal/server/tool_search.go    — capy_search handler
internal/server/tool_fetch.go     — capy_fetch_and_index handler
internal/server/tool_stats.go     — capy_stats handler
internal/server/tool_doctor.go    — capy_doctor handler
internal/server/tool_cleanup.go   — capy_cleanup handler
internal/server/snippet.go        — ExtractSnippet(), positionsFromHighlight()
internal/server/lifecycle.go      — StartLifecycleGuard()
```

---

## 7. Hook Implementation

**Reference files:**
- `context-mode/hooks/core/routing.mjs` — pure routing logic
- `context-mode/hooks/core/formatters.mjs` — platform response formatters
- `context-mode/hooks/pretooluse.mjs` — Claude Code pretooluse
- `context-mode/hooks/routing-block.mjs` — routing instructions XML

### 7.1 Hook Dispatcher

```go
// internal/hook/hook.go
package hook

func Run(event string, a adapter.HookAdapter, policies []security.SecurityPolicy, projectDir string) error {
    input, err := io.ReadAll(os.Stdin)
    if err != nil {
        return err
    }

    var output []byte
    switch event {
    case "pretooluse":
        output, err = handlePreToolUse(input, a, policies, projectDir)
    case "posttooluse":
        output, err = handlePostToolUse(input, a) // stub
    case "precompact":
        output, err = handlePreCompact(input, a)   // stub
    case "sessionstart":
        output, err = handleSessionStart(input, a) // routing instructions only
    case "userpromptsubmit":
        output, err = handleUserPromptSubmit(input, a) // stub
    default:
        return fmt.Errorf("unknown hook event: %s", event)
    }

    if err != nil {
        return err
    }
    if output != nil {
        os.Stdout.Write(output)
    }
    return nil
}
```

### 7.2 PreToolUse Handler

```go
// internal/hook/pretooluse.go

func handlePreToolUse(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    event, err := adapter.ParsePreToolUse(input)
    if err != nil {
        return nil, nil // pass through on parse error — don't block the tool
    }

    toolName := event.ToolName
    toolInput := event.ToolInput
    projectDir := event.ProjectDir

    // 1. Security check for capy tools
    if isCapyTool(toolName) {
        // capy_execute (shell): check code against Bash deny patterns
        // capy_execute_file: check file path + code
        // capy_batch_execute: check each command
        // If denied, return adapter.FormatBlock(reason)
        return nil, nil // allow
    }

    // 2. Route based on tool type
    switch {
    case toolName == "Bash":
        command := toolInput["command"].(string)

        // Security check against deny rules (full evaluateCommand with ask support)
        // ...

        // curl/wget detection (strip quoted content first to avoid false positives)
        // Uses FormatModify to replace command with echo guidance (matching TS reference)
        // instead of hard-deny FormatBlock — the LLM sees the guidance in stdout.
        stripped := stripQuotedContent(command)
        if isCurlOrWget(stripped) {
            return adapter.FormatModify(map[string]any{
                "command": `echo "capy: curl/wget blocked. Use capy_fetch_and_index(url, source) to fetch URLs, or capy_execute(language, code) to run HTTP calls in sandbox."`,
            })
        }

        // Inline HTTP detection (strip heredocs only)
        noHeredoc := stripHeredocs(command)
        if hasInlineHTTP(noHeredoc) {
            return adapter.FormatModify(map[string]any{
                "command": `echo "capy: Inline HTTP blocked. Use capy_execute(language, code) to run HTTP calls in sandbox, or capy_fetch_and_index(url, source) for web pages."`,
            })
        }

        // Build tools (gradle, maven) → redirect to sandbox
        if isBuildTool(stripped) {
            return adapter.FormatModify(map[string]any{
                "command": `echo "capy: Build tool redirected to sandbox. Use capy_execute(language: \"shell\", code: \"...\") to run this command."`,
            })
        }

        // Allow, but inject routing nudge (once per session via guidance throttle)
        return guidanceOnce("bash", BASH_GUIDANCE, adapter)

    case toolName == "WebFetch":
        return adapter.FormatBlock("Use capy_fetch_and_index instead. WebFetch dumps raw content into context.")

    case toolName == "Read":
        return guidanceOnce("read", READ_GUIDANCE, adapter)

    case toolName == "Grep":
        return guidanceOnce("grep", GREP_GUIDANCE, adapter)

    case toolName == "Agent" || toolName == "Task":
        // Inject routing block into subagent prompt
        return adapter.FormatModify(injectRoutingBlock(toolInput))
    }

    return nil, nil // pass through
}
```

**Guidance throttle:** Track which guidance types have been shown this session (in-memory set). Show each advisory at most once.

**stripQuotedContent:** Remove heredocs, single-quoted strings, and double-quoted strings from the command before regex matching. Prevents false positives like `gh issue edit --body "text with curl in it"`.

### 7.3 Routing Block

```go
// internal/hook/routing.go

func RoutingBlock() string {
    return `<context_window_protection>
  <priority_instructions>
    Raw tool output floods your context window. You MUST use capy
    MCP tools to keep raw data in the sandbox.
  </priority_instructions>
  ...
</context_window_protection>`
}
```

Full content matches design.md § 7.3.

### 7.4 Stub Hooks

```go
func handlePostToolUse(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    return nil, nil // STUB: Future session continuity
}

func handlePreCompact(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    return nil, nil // STUB: Future resume snapshot
}

func handleSessionStart(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    // Inject routing instructions only
    return adapter.FormatSessionStart(RoutingBlock())
}

func handleUserPromptSubmit(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    return nil, nil // STUB: Future user decision capture
}
```

### Files to create

```
internal/hook/hook.go             — Run() dispatcher
internal/hook/pretooluse.go       — handlePreToolUse(), routing logic
internal/hook/posttooluse.go      — stub
internal/hook/precompact.go       — stub
internal/hook/sessionstart.go     — routing instructions injection
internal/hook/userpromptsubmit.go — stub
internal/hook/routing.go          — RoutingBlock(), READ_GUIDANCE, GREP_GUIDANCE, BASH_GUIDANCE
internal/hook/guidance.go         — guidanceOnce() throttle
internal/hook/helpers.go          — stripQuotedContent(), stripHeredocs(), isCurlOrWget(), hasInlineHTTP(), isBuildTool()
```

---

## 8. Claude Code Adapter

**Reference files:**
- `context-mode/src/adapters/claude-code/index.ts` — ClaudeCodeAdapter
- `context-mode/src/adapters/types.ts` — HookAdapter interface
- `context-mode/hooks/core/formatters.mjs` — response format

### 8.1 Adapter Interface

```go
// internal/adapter/adapter.go
package adapter

type HookAdapter interface {
    ParsePreToolUse(input []byte) (*PreToolUseEvent, error)
    FormatBlock(reason string) ([]byte, error)
    FormatAllow(guidance string) ([]byte, error)
    FormatModify(updatedInput map[string]interface{}) ([]byte, error)
    FormatAsk() ([]byte, error)
    FormatSessionStart(context string) ([]byte, error)
    Capabilities() PlatformCapabilities
}

type PreToolUseEvent struct {
    ToolName  string
    ToolInput map[string]interface{}
    SessionID string
    ProjectDir string
}

type PlatformCapabilities struct {
    PreToolUse             bool
    PostToolUse            bool
    PreCompact             bool
    SessionStart           bool
    CanModifyArgs          bool
    CanModifyOutput        bool
    CanInjectSessionContext bool
}
```

### 8.2 Claude Code Adapter

```go
// internal/adapter/claudecode.go

type ClaudeCodeAdapter struct{}

func (a *ClaudeCodeAdapter) ParsePreToolUse(input []byte) (*PreToolUseEvent, error) {
    var raw struct {
        ToolName  string                 `json:"tool_name"`
        ToolInput map[string]interface{} `json:"tool_input"`
        SessionID string                 `json:"session_id"`
        TranscriptPath string            `json:"transcript_path"`
    }
    if err := json.Unmarshal(input, &raw); err != nil {
        return nil, err
    }
    return &PreToolUseEvent{
        ToolName:  raw.ToolName,
        ToolInput: raw.ToolInput,
        SessionID: extractSessionID(raw.SessionID, raw.TranscriptPath),
        ProjectDir: os.Getenv("CLAUDE_PROJECT_DIR"),
    }, nil
}

func (a *ClaudeCodeAdapter) FormatBlock(reason string) ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "hookSpecificOutput": map[string]interface{}{
            "hookEventName":           "PreToolUse",
            "permissionDecision":      "deny",
            "permissionDecisionReason": reason,
        },
    })
}

func (a *ClaudeCodeAdapter) FormatAllow(guidance string) ([]byte, error) {
    if guidance == "" {
        return nil, nil
    }
    return json.Marshal(map[string]interface{}{
        "hookSpecificOutput": map[string]interface{}{
            "hookEventName":    "PreToolUse",
            "additionalContext": guidance,
        },
    })
}

func (a *ClaudeCodeAdapter) FormatModify(updatedInput map[string]interface{}) ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "hookSpecificOutput": map[string]interface{}{
            "hookEventName":           "PreToolUse",
            "permissionDecision":      "allow",
            "permissionDecisionReason": "Routed to capy sandbox",
            "updatedInput":             updatedInput,
        },
    })
}

func (a *ClaudeCodeAdapter) FormatAsk() ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "hookSpecificOutput": map[string]interface{}{
            "hookEventName":      "PreToolUse",
            "permissionDecision": "ask",
        },
    })
}

func (a *ClaudeCodeAdapter) FormatSessionStart(context string) ([]byte, error) {
    return json.Marshal(map[string]interface{}{
        "hookSpecificOutput": map[string]interface{}{
            "hookEventName":    "SessionStart",
            "additionalContext": context,
        },
    })
}

func (a *ClaudeCodeAdapter) Capabilities() PlatformCapabilities {
    return PlatformCapabilities{
        PreToolUse: true, PostToolUse: true, PreCompact: true, SessionStart: true,
        CanModifyArgs: true, CanModifyOutput: true, CanInjectSessionContext: true,
    }
}
```

Session ID extraction priority: transcript_path UUID > session_id field > CLAUDE_SESSION_ID env > ppid fallback.

### Files to create

```
internal/adapter/adapter.go     — HookAdapter interface, PreToolUseEvent, PlatformCapabilities
internal/adapter/claudecode.go   — ClaudeCodeAdapter implementation
```

---

## 9. Setup Command Implementation

**Reference files:**
- `context-mode/src/adapters/claude-code/index.ts` — generateHookConfig(), configureAllHooks()
- `context-mode/configs/claude-code/CLAUDE.md` — routing instructions template

### 9.1 Claude Code Setup

```go
func setupClaudeCode(binaryPath, projectDir string) error {
    // 1. Resolve binary path
    if binaryPath == "" {
        binaryPath, _ = exec.LookPath("capy")
    }

    // 2. Update .claude/settings.json (merge, don't overwrite)
    mergeHooks(filepath.Join(projectDir, ".claude", "settings.json"), binaryPath)

    // 3. Update .mcp.json
    mergeMCPServer(filepath.Join(projectDir, ".mcp.json"), binaryPath)

    // 4. Append routing instructions to CLAUDE.md
    appendRoutingInstructions(filepath.Join(projectDir, "CLAUDE.md"))

    // 5. Add .capy/ to .gitignore
    ensureGitignoreEntry(filepath.Join(projectDir, ".gitignore"), ".capy/")

    return nil
}
```

### 9.2 Idempotent JSON Merge

Read existing JSON into `map[string]interface{}`, deep-merge new entries, write back with `json.MarshalIndent`. Must preserve existing hooks, MCP servers, and permissions. Check for existing capy entries before adding to avoid duplicates.

### 9.3 Hook Registration

PreToolUse hook matcher pattern: `Bash|WebFetch|Read|Grep|Agent|Task|mcp__*capy*`

Register stubs for: PostToolUse, PreCompact, SessionStart, UserPromptSubmit.

### 9.4 Doctor Command

Diagnostic checks (reusable by both `capy doctor` CLI and `capy_doctor` MCP tool):
- Runtime availability for all 11 languages
- FTS5 availability (try creating virtual table in `:memory:` DB)
- Hook registration in `.claude/settings.json`
- MCP registration in `.mcp.json`
- Config file discovery
- Knowledge base status (path, file size, source count)
- Binary version

### Files to create

```
internal/platform/setup.go     — setupClaudeCode(), JSON merging
internal/platform/routing.go   — GenerateRoutingInstructions()
internal/platform/doctor.go    — diagnostic checks
cmd/capy/main.go               — wire setup, doctor, cleanup commands
```

---

## 10. Testing Strategy

### 10.1 Test Organization

```
internal/store/store_test.go         — ContentStore: schema, indexing, dedup, re-index
internal/store/chunk_test.go         — Chunking: markdown, plaintext, JSON
internal/store/search_test.go        — Search: Porter, trigram, fuzzy, fallback, throttling
internal/executor/executor_test.go   — Execution: languages, timeout, process group, hard cap
internal/executor/truncate_test.go   — Smart truncation: head/tail split, UTF-8 safety
internal/executor/wrap_test.go       — Auto-wrapping: Go, PHP, Elixir, file content injection
internal/security/security_test.go   — Evaluation: deny wins, chained commands, shell-escape
internal/security/glob_test.go       — Glob patterns: *, **, ?, colon format, file paths
internal/security/split_test.go      — Command splitting: quotes, backticks, edge cases
internal/hook/pretooluse_test.go     — PreToolUse: routing decisions, security integration
internal/adapter/claudecode_test.go  — Claude Code adapter: parse/format, session ID extraction
internal/config/config_test.go       — Config: defaults, loading, precedence merge
internal/config/paths_test.go        — Path resolution: DB path, project hash, project root
internal/server/server_test.go       — MCP server: integration tests
internal/server/snippet_test.go      — Snippet extraction: highlight markers, window merging
```

### 10.2 Test Dependencies

- `github.com/stretchr/testify` for assertions
- In-memory SQLite (`:memory:`) or temp files for store tests
- `testing/fstest` or temp directories for file-based tests
- `net/http/httptest` for `fetch_and_index` tests

### 10.3 Key Test Scenarios to Port

From `context-mode/tests/`:
- `store.test.ts` — chunking strategies, indexing, search, vocabulary, distinctive terms
- `security.test.ts` — glob patterns, chained commands, shell-escape, deny-wins
- `executor.test.ts` — execution, timeout, truncation, exit classification
- `core/routing.test.ts` — hook routing decisions
- `core/search.test.ts` — three-tier search, throttling, output caps
- `core/server.test.ts` — MCP tool handler behavior
