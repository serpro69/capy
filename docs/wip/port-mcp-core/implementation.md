# Implementation Guide: Port MCP Core to Go

This document provides implementation-level details for each component of the capy port. It is meant to be read alongside `design.md` and used during implementation sessions.

Each section includes:
- Exact behavior to implement (with edge cases)
- Reference to the context-mode source code
- Go-specific implementation notes
- Testing strategy

---

## 1. Project Bootstrap

### 1.1 Go Module Init

```bash
go mod init github.com/serpro69/capy
```

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
```

### 1.4 CGO Build Tags

`mattn/go-sqlite3` requires CGO and must be built with FTS5 enabled:

```bash
CGO_ENABLED=1 go build -tags "fts5" ./cmd/capy/
```

Add to `Makefile` or build script. The `fts5` build tag enables FTS5 virtual table support in the SQLite compilation.

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
        newHookCmd(),
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

---

## 2. ContentStore Implementation

**Reference files:**
- `context-mode/src/store.ts` — complete ContentStore implementation
- `context-mode/src/db-base.ts` — SQLite base class (WAL mode, prepared statements)
- `context-mode/docs/llms-full.txt` lines 231-378

### 2.1 Store Struct

```go
// internal/store/store.go
package store

type ContentStore struct {
    db          *sql.DB
    dbPath      string
    initialized bool
    mu          sync.RWMutex

    // Prepared statements (cached after first use)
    stmtInsertSource    *sql.Stmt
    stmtInsertChunk     *sql.Stmt
    stmtInsertTrigram   *sql.Stmt
    stmtInsertVocab     *sql.Stmt
    stmtSearchPorter    *sql.Stmt
    stmtSearchTrigram   *sql.Stmt
    // ... etc
}
```

**Lazy initialization:** The store must not open the DB file until the first operation that needs it. Implement via a `getDB()` method that initializes on first call.

### 2.2 Schema Creation

Run all `CREATE TABLE IF NOT EXISTS` and `CREATE VIRTUAL TABLE IF NOT EXISTS` statements from the design doc's schema section. Execute pragmas first:

```go
func (s *ContentStore) init() error {
    db, err := sql.Open("sqlite3", s.dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
    // ...
    _, err = db.Exec(`CREATE TABLE IF NOT EXISTS sources (...)`)
    _, err = db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(...)`)
    _, err = db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_trigram USING fts5(...)`)
    _, err = db.Exec(`CREATE TABLE IF NOT EXISTS vocabulary (...)`)
    // ...
}
```

**Important:** The `_busy_timeout=5000` connection parameter prevents `SQLITE_BUSY` errors when multiple processes access the same persistent DB (which can now happen since capy's DB is per-project, not per-process).

### 2.3 Indexing

#### IndexMarkdown

Port `#chunkMarkdown()` from `context-mode/src/store.ts`.

Key behavior:
1. Split input into lines
2. Track heading stack (H1-H4 depth) for breadcrumb titles
3. Track code fence state (inside/outside code block)
4. On heading encounter (outside code block): flush accumulated content as a chunk, update heading stack
5. On horizontal rule: flush accumulated content
6. At end of input: flush remaining content
7. For each chunk: if byte size > 4096, split at paragraph boundaries (`\n\n`) with numbered suffixes

```go
func chunkMarkdown(content string) []Chunk {
    // heading regex: ^(#{1,4})\s+(.+)$
    // horizontal rule regex: ^[-_*]{3,}\s*$
    // code fence regex: ^```
    // ...
}
```

**Edge cases:**
- Empty input → return single chunk titled "Output" with empty content
- Content with no headings → single chunk titled "Output"
- Code blocks with ```` ``` ```` inside must not be split by heading regex
- Oversized chunks: split at `\n\n`, if no `\n\n` found, split at byte boundary respecting UTF-8

#### IndexPlainText

Port `#chunkPlainText()`. Two-phase strategy:

1. Try blank-line splitting (`\n\s*\n`). Use if result has 3-200 sections with each < 5000 bytes.
2. Fallback: 20-line groups with 2-line overlap. Step size = 18 lines.

#### IndexJSON

Port `#walkJSON()`. Recursive walk of parsed JSON:

1. Parse JSON string into `interface{}` (Go's `json.Unmarshal`)
2. For objects: recurse into each key
3. For arrays: batch items by accumulated byte size up to 4096. Check identity fields (`id`, `name`, `title`, `slug`, `key`, `label`) for meaningful titles
4. For flat small objects (< 4096 bytes, no nested objects/arrays): emit as single chunk
5. On parse failure: fall back to `IndexPlainText`

#### Vocabulary Extraction

During indexing, extract words for the fuzzy search vocabulary:

```go
func extractVocabulary(content string) []string {
    words := strings.Fields(content)
    var result []string
    for _, w := range words {
        w = strings.ToLower(w)
        if len(w) >= 3 && !isStopword(w) {
            result = append(result, w)
        }
    }
    return result
}
```

Insert with `INSERT OR IGNORE INTO vocabulary (word) VALUES (?)`.

**Reference:** `context-mode/src/store.ts` — search for `vocabulary` and `INSERT OR IGNORE`.

### 2.4 Search

#### searchWithFallback

Port the three-tier search. Each tier runs a SQL query and returns if results found:

```go
func (s *ContentStore) SearchWithFallback(query string, limit int, source string) ([]SearchResult, string, error) {
    // Layer 1: Porter
    results, err := s.searchPorter(query, limit, source)
    if err == nil && len(results) > 0 {
        return results, "porter", nil
    }
    // Layer 2: Trigram
    results, err = s.searchTrigram(query, limit, source)
    if err == nil && len(results) > 0 {
        return results, "trigram", nil
    }
    // Layer 3: Fuzzy
    corrected := s.fuzzyCorrect(query)
    if corrected != query {
        results, err = s.searchPorter(corrected, limit, source)
        if err == nil && len(results) > 0 {
            return results, "fuzzy", nil
        }
        results, err = s.searchTrigram(corrected, limit, source)
        if err == nil && len(results) > 0 {
            return results, "fuzzy", nil
        }
    }
    return nil, "", nil
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

#### Trigram Search SQL

Same structure but against `chunks_trigram` table. **Important:** Trigram queries must be sanitized — remove all characters except alphanumeric, spaces, underscores, hyphens. This prevents FTS5 syntax errors from special characters.

```go
func sanitizeTrigram(query string) string {
    // Keep only [a-zA-Z0-9 _-]
    var b strings.Builder
    for _, r := range query {
        if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '_' || r == '-' {
            b.WriteRune(r)
        }
    }
    return b.String()
}
```

#### Levenshtein Distance

Standard DP implementation. Port from `context-mode/src/store.ts` — `levenshteinDistance()`.

```go
func levenshteinDistance(a, b string) int {
    a = strings.ToLower(a)
    b = strings.ToLower(b)
    la, lb := len(a), len(b)
    d := make([][]int, la+1)
    for i := range d {
        d[i] = make([]int, lb+1)
        d[i][0] = i
    }
    for j := 0; j <= lb; j++ {
        d[0][j] = j
    }
    for i := 1; i <= la; i++ {
        for j := 1; j <= lb; j++ {
            cost := 0
            if a[i-1] != b[j-1] {
                cost = 1
            }
            d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
        }
    }
    return d[la][lb]
}
```

#### maxEditDistance

```go
func maxEditDistance(wordLen int) int {
    switch {
    case wordLen <= 4:
        return 1
    case wordLen <= 12:
        return 2
    default:
        return 3
    }
}
```

#### fuzzyCorrect

For each word in the query:
1. Query vocabulary: `SELECT word FROM vocabulary WHERE length(word) BETWEEN ? AND ?`
2. Compute Levenshtein distance for each candidate
3. Return closest match within `maxEditDistance` threshold, or original word if no match

**Performance note:** This can be expensive for large vocabularies. Context-mode does this for every query word. Consider caching corrections for the session lifetime.

### 2.5 Smart Snippet Extraction

Port from context-mode. Each search result gets a snippet of up to 1500 bytes centered on match positions.

```go
func extractSnippet(content, highlighted string, maxBytes int) string {
    // 1. Find match positions from highlighted text (char(2)/char(3) markers)
    // 2. For each match position, create a 300-char window centered on it
    // 3. Merge overlapping windows
    // 4. Collect merged windows until maxBytes limit
    // 5. Fallback: if no highlight markers, use strings.Index on raw query terms
}
```

### 2.6 Distinctive Terms

Port `getDistinctiveTerms()`:

```go
func (s *ContentStore) GetDistinctiveTerms(sourceID int64, maxTerms int) []string {
    // 1. Get total chunk count across all sources
    // 2. For each word in chunks belonging to sourceID:
    //    score = log(totalChunks / countChunksContainingWord) + lengthBonus + identifierBonus
    // 3. identifierBonus: reward words with underscores or camelCase
    // 4. Sort by score descending, return top maxTerms
}
```

### 2.7 Progressive Throttling

Track search calls in a sliding 60-second window:

```go
type throttle struct {
    mu    sync.Mutex
    calls []time.Time
}

func (t *throttle) check() (maxResults int, err error) {
    t.mu.Lock()
    defer t.mu.Unlock()
    now := time.Now()
    cutoff := now.Add(-60 * time.Second)
    // Remove calls older than 60s
    // Count remaining calls
    // 1-3: return 2, nil
    // 4-8: return 1, nil (+ warning)
    // 9+: return 0, error("use batch_execute")
}
```

### 2.8 Freshness Metadata

On every search hit, update the source's freshness:

```sql
UPDATE sources SET last_accessed_at = CURRENT_TIMESTAMP, access_count = access_count + 1 WHERE id = ?
```

On re-index of a source with the same label:
1. Compute SHA-256 hash of new content
2. Compare with stored `content_hash`
3. If different: delete old chunks, re-index, update hash and `indexed_at`
4. If same: skip re-indexing (content unchanged)

### 2.9 Cleanup

```go
func (s *ContentStore) Cleanup(maxAgeDays int, dryRun bool) ([]PrunedSource, error) {
    cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
    // SELECT id, label, chunk_count, last_accessed_at FROM sources
    // WHERE last_accessed_at < cutoff AND access_count = 0
    if !dryRun {
        // DELETE FROM chunks WHERE source_id = ?
        // DELETE FROM chunks_trigram WHERE source_id = ?
        // DELETE FROM sources WHERE id = ?
    }
    return pruned, nil
}
```

---

## 3. PolyglotExecutor Implementation

**Reference files:**
- `context-mode/src/executor.ts` — PolyglotExecutor class
- `context-mode/src/runtime.ts` — runtime detection
- `context-mode/src/truncate.ts` — smart truncation
- `context-mode/docs/llms-full.txt` lines 380-460

### 3.1 Executor Struct

```go
// internal/executor/executor.go
package executor

type PolyglotExecutor struct {
    runtimes   map[Language]string // detected runtime paths
    projectDir string
    mu         sync.RWMutex
    detected   bool
}

type ExecResult struct {
    Stdout     string
    Stderr     string
    ExitCode   int
    TimedOut   bool
    Killed     bool // hard cap exceeded
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
```

### 3.2 Runtime Detection

```go
// internal/executor/runtime.go

type runtimeCandidate struct {
    name     string
    binaries []string // try in order
}

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
    e.mu.Lock()
    defer e.mu.Unlock()
    e.runtimes = make(map[Language]string)
    for lang, candidates := range runtimeCandidates {
        for _, bin := range candidates {
            if path, err := exec.LookPath(bin); err == nil {
                e.runtimes[lang] = path
                break
            }
        }
    }
    e.detected = true
}
```

Cache results for server lifetime. Call `detectRuntimes()` lazily on first `Execute()` call.

### 3.3 Process Spawning

```go
func (e *PolyglotExecutor) Execute(ctx context.Context, lang Language, code string, opts ExecOptions) (*ExecResult, error) {
    runtime, ok := e.runtimes[lang]
    if !ok {
        return nil, fmt.Errorf("no runtime available for %s", lang)
    }

    // 1. Create temp directory
    tmpDir, _ := os.MkdirTemp("", "capy-exec-*")
    defer os.RemoveAll(tmpDir)

    // 2. Apply auto-wrapping
    code = autoWrap(lang, code, e.projectDir)

    // 3. Write script file
    scriptPath := filepath.Join(tmpDir, scriptFilename(lang))
    os.WriteFile(scriptPath, []byte(code), 0644)

    // 4. Build command
    cmd := buildCommand(lang, runtime, scriptPath)

    // 5. Set working directory
    if lang == Shell {
        cmd.Dir = e.projectDir
    } else {
        cmd.Dir = tmpDir
    }

    // 6. Set environment
    cmd.Env = buildEnv()

    // 7. Set process group (Unix)
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

    // 8. Capture output with hard cap monitoring
    // 9. Handle timeout
    // 10. Return result
}
```

#### Script Filenames

| Language | Filename | Invocation |
|----------|----------|------------|
| JavaScript | `script.js` | `bun script.js` or `node script.js` |
| TypeScript | `script.ts` | `bun script.ts` or `tsx script.ts` |
| Python | `script.py` | `python3 script.py` |
| Shell | `script.sh` | `bash script.sh` |
| Ruby | `script.rb` | `ruby script.rb` |
| Go | `main.go` | `go run main.go` |
| Rust | `main.rs` | `rustc main.rs -o main && ./main` |
| PHP | `script.php` | `php script.php` |
| Perl | `script.pl` | `perl script.pl` |
| R | `script.R` | `Rscript script.R` |
| Elixir | `script.exs` | `elixir script.exs` |

**Rust special case:** Two-step — compile with `rustc` to a temp binary, then execute. Not interpreted.

### 3.4 Auto-Wrapping

```go
// internal/executor/wrap.go

func autoWrap(lang Language, code, projectDir string) string {
    switch lang {
    case Go:
        if !strings.Contains(code, "package ") {
            return fmt.Sprintf("package main\nimport \"fmt\"\nfunc main() {\n%s\n}", code)
        }
    case PHP:
        if !strings.HasPrefix(strings.TrimSpace(code), "<?") {
            return "<?php\n" + code
        }
    case Elixir:
        if mixExists(projectDir) {
            // Prepend Path.wildcard to add compiled BEAM paths
            return `Path.wildcard("_build/*/lib/*/ebin") |> Enum.each(&Code.append_path/1)` + "\n" + code
        }
    }
    return code
}
```

### 3.5 FILE_CONTENT Injection (execute_file)

For `capy_execute_file`, prepend language-specific file loading code:

```go
func injectFileContent(lang Language, code, filePath string) string {
    absPath, _ := filepath.Abs(filePath)
    switch lang {
    case JavaScript, TypeScript:
        return fmt.Sprintf(`const FILE_CONTENT = require("fs").readFileSync(%q, "utf-8");\nconst FILE_CONTENT_PATH = %q;\n%s`, absPath, absPath, code)
    case Python:
        return fmt.Sprintf("FILE_CONTENT = open(%q, 'r', encoding='utf-8').read()\nFILE_CONTENT_PATH = %q\n%s", absPath, absPath, code)
    case Shell:
        return fmt.Sprintf("FILE_CONTENT=$(cat %q)\nFILE_CONTENT_PATH=%q\n%s", absPath, absPath, code)
    // ... etc for each language (see design.md Section 4.4 for full table)
    }
    return code
}
```

### 3.6 Smart Truncation

```go
// internal/executor/truncate.go

const (
    MaxOutputBytes = 102400    // 100 KB
    HardCapBytes   = 104857600 // 100 MB
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
    var headLines []string
    headBytes := 0
    for _, line := range lines {
        lineBytes := len(line) + 1 // +1 for newline
        if headBytes+lineBytes > headBudget {
            break
        }
        headLines = append(headLines, line)
        headBytes += lineBytes
    }

    // Collect tail lines (from end) until tailBudget exhausted
    var tailLines []string
    tailBytes := 0
    for i := len(lines) - 1; i >= len(headLines); i-- {
        lineBytes := len(lines[i]) + 1
        if tailBytes+lineBytes > tailBudget {
            break
        }
        tailLines = append([]string{lines[i]}, tailLines...)
        tailBytes += lineBytes
    }

    truncatedLines := len(lines) - len(headLines) - len(tailLines)
    truncatedBytes := len(output) - headBytes - tailBytes

    separator := fmt.Sprintf("... [%d lines / %.1fKB truncated -- showing first %d + last %d lines] ...",
        truncatedLines, float64(truncatedBytes)/1024, len(headLines), len(tailLines))

    return strings.Join(headLines, "\n") + "\n" + separator + "\n" + strings.Join(tailLines, "\n")
}
```

### 3.7 Hard Cap Streaming

Monitor combined stdout+stderr byte count during execution. Kill process group if threshold exceeded:

```go
func (e *PolyglotExecutor) executeWithHardCap(cmd *exec.Cmd, timeout time.Duration) (*ExecResult, error) {
    var stdout, stderr bytes.Buffer
    var totalBytes int64

    // Use io.MultiWriter with counting wrapper
    stdoutPipe, _ := cmd.StdoutPipe()
    stderrPipe, _ := cmd.StderrPipe()

    cmd.Start()

    // Read with hard cap check
    go func() {
        buf := make([]byte, 32*1024)
        for {
            n, err := stdoutPipe.Read(buf)
            stdout.Write(buf[:n])
            atomic.AddInt64(&totalBytes, int64(n))
            if atomic.LoadInt64(&totalBytes) > HardCapBytes {
                syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
                return
            }
            if err != nil { return }
        }
    }()
    // Similar goroutine for stderr

    // Timeout handling via context or timer
    // ...
}
```

### 3.8 Environment Passthrough

```go
// internal/executor/env.go

var passthroughVars = []string{
    // Authentication
    "GITHUB_TOKEN", "GH_TOKEN", "ANTHROPIC_API_KEY",
    "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
    "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE",
    "GOOGLE_APPLICATION_CREDENTIALS",
    // Infrastructure
    "DOCKER_HOST", "KUBECONFIG", "NPM_TOKEN", "NODE_AUTH_TOKEN", "npm_config_registry",
    // Network
    "SSH_AUTH_SOCK", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
    "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS",
    // Configuration
    "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
    "GOROOT", "GOPATH",
}

var forcedVars = map[string]string{
    "PYTHONDONTWRITEBYTECODE": "1",
    "PYTHONUNBUFFERED":        "1",
    "PYTHONUTF8":             "1",
}

func buildEnv() []string {
    env := []string{
        "PATH=" + os.Getenv("PATH"),
        "HOME=" + os.Getenv("HOME"),
        "USER=" + os.Getenv("USER"),
    }
    for _, key := range passthroughVars {
        if val := os.Getenv(key); val != "" {
            env = append(env, key+"="+val)
        }
    }
    for key, val := range forcedVars {
        env = append(env, key+"="+val)
    }
    return env
}
```

---

## 4. Security Implementation

**Reference files:**
- `context-mode/src/security.ts` — full implementation
- `context-mode/docs/llms-full.txt` lines 462-553

### 4.1 Rule Parsing

```go
// internal/security/settings.go

type Settings struct {
    Permissions struct {
        Deny  []string `json:"deny"`
        Allow []string `json:"allow"`
    } `json:"permissions"`
}

func LoadSettings() (*Settings, error) {
    // Load in precedence order, merge:
    // 1. .claude/settings.local.json
    // 2. .claude/settings.json
    // 3. ~/.claude/settings.json
    // Deny rules from all levels are combined
    // Allow rules from all levels are combined
}
```

### 4.2 Pattern Matching

```go
// internal/security/glob.go

type PatternType int

const (
    BashColon PatternType = iota // Bash(command:argsGlob)
    BashSpace                     // Bash(command argsGlob)
    BashPlain                     // Bash(glob)
    ToolPath                      // ToolName(fileGlob)
)

func ParsePattern(raw string) (PatternType, *regexp.Regexp, error) {
    // Parse Tool(pattern) format
    // Convert glob to regex:
    //   * → [^\s]*
    //   ** → .*
    //   ? → .
    //   Escape other regex metacharacters
}
```

**Reference:** `context-mode/src/security.ts` — `globToRegex()`, `fileGlobToRegex()`, `parseToolPattern()`.

### 4.3 Command Splitting

```go
// internal/security/split.go

func SplitChainedCommands(command string) []string {
    // Split on &&, ||, ;, | operators
    // Respect single quotes, double quotes, backticks
    // "echo 'hello && world' && sudo rm" → ["echo 'hello && world'", "sudo rm"]
}
```

**Tricky edge cases:**
- Nested quotes: `echo "it's a 'test'" && sudo rm`
- Backtick commands: `` echo `hostname` && sudo rm ``
- Pipes: `cat file | grep pattern` → both segments checked
- Escaped operators inside quotes should not split

### 4.4 Shell-Escape Detection

```go
// internal/security/security.go

var shellEscapePatterns = map[Language][]regexp.Regexp{
    Python: {
        regexp.MustCompile(`os\.system\(\s*(['"])(.*?)\1\s*\)`),
        regexp.MustCompile(`subprocess\.(?:run|call|Popen|check_output|check_call)\(\s*(['"])(.*?)\1`),
    },
    JavaScript: {
        regexp.MustCompile(`exec(?:Sync|File|FileSync)?\(\s*(['"\x60])(.*?)\1`),
        regexp.MustCompile(`spawn(?:Sync)?\(\s*(['"\x60])(.*?)\1`),
    },
    // ... etc (see design.md Section 6.4 for full table)
}

func ExtractShellCommands(lang Language, code string) []string {
    patterns, ok := shellEscapePatterns[lang]
    if !ok {
        return nil // Languages without patterns pass through
    }
    var commands []string
    for _, pat := range patterns {
        matches := pat.FindAllStringSubmatch(code, -1)
        for _, m := range matches {
            commands = append(commands, m[2]) // capture group 2 = command string
        }
    }
    return commands
}
```

**Python subprocess list form:** Additionally detect `subprocess.run(["rm", "-rf", "/"])` and join args into `"rm -rf /"`.

### 4.5 Evaluation Function

```go
func Evaluate(rules *Settings, command string) PermissionDecision {
    segments := SplitChainedCommands(command)
    for _, seg := range segments {
        seg = strings.TrimSpace(seg)
        // Check deny rules first
        for _, pattern := range rules.Permissions.Deny {
            if matchesBashPattern(pattern, seg) {
                return Deny
            }
        }
    }
    // If no deny matched, check allow (or default allow)
    return Allow
}
```

**Key invariant:** Deny always wins. If any segment of a chained command matches any deny rule, the entire command is blocked.

---

## 5. MCP Server Implementation

**Reference files:**
- `context-mode/src/server.ts` — full server implementation
- `context-mode/docs/llms-full.txt` lines 26-230
- `mcp-go` documentation for tool registration API

### 5.1 Server Struct

```go
// internal/server/server.go
package server

type Server struct {
    mcpServer  *mcp.Server
    store      *store.ContentStore
    executor   *executor.PolyglotExecutor
    security   *security.Settings
    config     *config.Config
    stats      *SessionStats
    throttle   *searchThrottle
    storeMu    sync.Once // lazy store init
}
```

### 5.2 Tool Registration

Using `mcp-go`'s tool registration API. Each tool is registered with a JSON Schema for parameter validation.

```go
func (s *Server) registerTools() {
    s.mcpServer.AddTool(mcp.Tool{
        Name: "capy_execute",
        Description: "Run code in an isolated subprocess. Only stdout enters context.",
        InputSchema: mcp.ToolInputSchema{
            Type: "object",
            Properties: map[string]interface{}{
                "language": map[string]interface{}{
                    "type": "string",
                    "enum": []string{"javascript", "typescript", "python", "shell", "ruby", "go", "rust", "php", "perl", "r", "elixir"},
                    "description": "Programming language to execute",
                },
                "code": map[string]interface{}{
                    "type": "string",
                    "description": "Source code to execute",
                },
                // ... timeout, intent
            },
            Required: []string{"language", "code"},
        },
    }, s.handleExecute)
    // ... register remaining 8 tools
}
```

### 5.3 Tool Handlers

Each handler follows the same pattern:

```go
func (s *Server) handleExecute(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // 1. Parse parameters from request.Params.Arguments
    // 2. Security check (for execute/execute_file/batch_execute)
    // 3. Execute via s.executor
    // 4. Auto-index if intent provided and output > 5KB
    // 5. Track stats
    // 6. Return result
}
```

#### Auto-indexing flow (execute with intent)

```go
func (s *Server) handleExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // ... parse params, security check ...

    result, err := s.executor.Execute(ctx, lang, code, opts)
    if err != nil {
        return nil, err
    }

    output := result.Stdout
    if result.ExitCode != 0 {
        output = result.Stdout + "\n" + result.Stderr
    }

    // Auto-index + search if intent provided and output is large
    if intent != "" && len(output) > 5000 {
        st := s.getStore()
        indexResult, _ := st.IndexPlainText(output, fmt.Sprintf("execute:%s", lang))
        s.stats.AddBytesIndexed(int64(len(output)))

        searchResults, _, _ := st.SearchWithFallback(intent, 5, indexResult.Label)
        if len(searchResults) > 0 {
            // Return search results instead of raw output
            return formatIntentResults(searchResults, indexResult, len(output)), nil
        }
        // No matches — return distinctive terms for follow-up
        terms := st.GetDistinctiveTerms(indexResult.SourceID, 40)
        return formatNoMatchResults(indexResult, len(output), terms), nil
    }

    // Apply smart truncation
    output = executor.SmartTruncate(output, executor.MaxOutputBytes)
    s.stats.AddBytesReturned("capy_execute", int64(len(output)))
    return &mcp.CallToolResult{Content: []mcp.Content{{Type: "text", Text: output}}}, nil
}
```

#### batch_execute flow

```go
func (s *Server) handleBatchExecute(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // 1. Parse commands array and queries array
    // 2. Security check each command individually
    // 3. Execute all commands sequentially in shell, collecting output
    //    Each command's output prefixed with "## label\n"
    // 4. Index combined output via store.IndexMarkdown()
    // 5. Build section inventory (all sections with byte sizes)
    // 6. Search each query with three-tier fallback
    //    - Scoped to batch source label first
    //    - Global fallback if no scoped results
    // 7. Return inventory + search results (80 KB output cap)
}
```

### 5.4 Lazy Store Initialization

```go
func (s *Server) getStore() *store.ContentStore {
    s.storeMu.Do(func() {
        dbPath := s.config.ResolveDBPath()
        s.store = store.New(dbPath)
    })
    return s.store
}
```

### 5.5 fetch_and_index Implementation

The fetch is executed as a subprocess to keep raw HTML/JSON out of the server process memory:

```go
func (s *Server) handleFetchAndIndex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    url := req.Params.Arguments["url"].(string)
    source := req.Params.Arguments["source"]

    // Execute fetch in subprocess
    // Use shell: curl or a Go HTTP client in-process
    // Option A: subprocess (matches context-mode behavior)
    // Option B: in-process Go HTTP (simpler, doesn't need curl)
    // Recommend Option B since Go HTTP is excellent and we're already in Go

    resp, err := http.Get(url)
    // ...

    contentType := resp.Header.Get("Content-Type")
    body, _ := io.ReadAll(resp.Body)

    var content string
    var indexFn func(string, string) (*store.IndexResult, error)

    switch {
    case strings.Contains(contentType, "json"):
        content = string(body)
        indexFn = s.getStore().IndexJSON
    case strings.Contains(contentType, "text/plain"):
        content = string(body)
        indexFn = s.getStore().IndexPlainText
    default: // HTML
        content = htmlToMarkdown(string(body))
        indexFn = s.getStore().IndexMarkdown
    }

    result, _ := indexFn(content, source)
    s.stats.AddBytesIndexed(int64(len(body)))

    // Return preview (first 3072 bytes)
    preview := content
    if len(preview) > 3072 {
        preview = preview[:3072] + "\n...[truncated -- use search() for full content]"
    }

    return formatIndexResult(result, preview), nil
}
```

**Implementation note:** Unlike context-mode which uses a subprocess for fetch isolation, capy can use Go's native `net/http` since there's no risk of raw data entering context — the Go server process is separate from the LLM context window. The subprocess isolation in context-mode is because Node.js shares memory with the MCP server; in Go, this isn't a concern.

However, tracking network bytes for `bytesSandboxed` stats still applies — count response body size.

---

## 6. Hook Implementation

**Reference files:**
- `context-mode/hooks/pretooluse.mjs` — main PreToolUse hook
- `context-mode/hooks/routing-block.mjs` — routing instructions XML
- `context-mode/hooks/core/routing.mjs` — shared routing logic
- `context-mode/docs/llms-full.txt` lines 556-637

### 6.1 Hook Dispatcher

```go
// internal/hook/hook.go
package hook

func Run(event string, adapter adapter.HookAdapter) error {
    // Read JSON from stdin
    input, err := io.ReadAll(os.Stdin)
    if err != nil {
        return err
    }

    var output []byte
    switch event {
    case "pretooluse":
        output, err = handlePreToolUse(input, adapter)
    case "posttooluse":
        output, err = handlePostToolUse(input, adapter)
    case "precompact":
        output, err = handlePreCompact(input, adapter)
    case "sessionstart":
        output, err = handleSessionStart(input, adapter)
    case "userpromptsubmit":
        output, err = handleUserPromptSubmit(input, adapter)
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

### 6.2 PreToolUse Handler

```go
// internal/hook/pretooluse.go

func handlePreToolUse(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    event, err := adapter.ParsePreToolUse(input)
    if err != nil {
        return nil, nil // pass through on parse error
    }

    toolName := event.ToolName
    toolInput := event.ToolInput

    // 1. Security check for capy tools
    if isCapyTool(toolName) {
        if blocked, reason := securityCheck(toolName, toolInput); blocked {
            return adapter.FormatBlock(reason)
        }
        return nil, nil // allow
    }

    // 2. Route based on tool type
    switch {
    case toolName == "Bash":
        command := toolInput["command"].(string)

        // Check for curl/wget
        if isCurlOrWget(command) {
            return adapter.FormatBlock(
                "Use capy_fetch_and_index to fetch URLs. Raw curl/wget output floods context.")
        }

        // Check for inline HTTP (fetch(), requests.get(), etc.)
        if hasInlineHTTP(command) {
            return adapter.FormatBlock(
                "Use capy_execute to run HTTP code in sandbox.")
        }

        // Security check
        if blocked, reason := securityCheckBash(command); blocked {
            return adapter.FormatBlock(reason)
        }

    case toolName == "WebFetch":
        return adapter.FormatBlock(
            "Use capy_fetch_and_index instead. WebFetch dumps raw content into context.")

    case toolName == "Read":
        return adapter.FormatAllow(
            "For file analysis, use capy_execute_file. Use Read only for viewing/editing.")

    case toolName == "Grep":
        return adapter.FormatAllow(
            "For large searches, use capy_execute with shell. Grep output enters context directly.")

    case toolName == "Agent" || toolName == "Task":
        // Inject routing block into subagent prompt
        return adapter.FormatSubagentRouting(routingBlock())
    }

    return nil, nil // pass through
}
```

### 6.3 Routing Block

```go
// internal/hook/routing.go

func routingBlock() string {
    return `<context_window_protection>
  <priority_instructions>
    Raw tool output floods your context window. You MUST use capy
    MCP tools to keep raw data in the sandbox.
  </priority_instructions>

  <tool_selection_hierarchy>
    1. GATHER: capy_batch_execute(commands, queries)
    2. FOLLOW-UP: capy_search(queries: ["q1", "q2", ...])
    3. PROCESSING: capy_execute(language, code) | capy_execute_file(path, language, code)
  </tool_selection_hierarchy>

  <forbidden_actions>
    - DO NOT use Bash for commands producing >20 lines of output.
    - DO NOT use Read for analysis (use capy_execute_file).
    - DO NOT use WebFetch (use capy_fetch_and_index instead).
    - Bash is ONLY for git/mkdir/rm/mv/navigation.
  </forbidden_actions>

  <output_constraints>
    Keep final response under 500 words.
    Write artifacts to FILES, not inline text.
  </output_constraints>
</context_window_protection>`
}
```

### 6.4 Stub Hooks (Future)

```go
func handlePostToolUse(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    // STUB: Future session continuity — extract events from tool results
    return nil, nil
}

func handlePreCompact(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    // STUB: Future session continuity — build resume snapshot
    return nil, nil
}

func handleSessionStart(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    // For now: inject routing instructions only
    return adapter.FormatSessionStart(routingBlock())
}

func handleUserPromptSubmit(input []byte, adapter adapter.HookAdapter) ([]byte, error) {
    // STUB: Future session continuity — capture user decisions
    return nil, nil
}
```

---

## 7. Claude Code Adapter

**Reference files:**
- `context-mode/src/adapters/claude-code/index.ts` — ClaudeCodeAdapter
- `context-mode/src/adapters/claude-code/hooks.ts` — hook formatting
- `context-mode/docs/platform-support.md` lines 66-102

### 7.1 JSON Input/Output Format

Claude Code sends hook input as JSON on stdin:

```json
{
  "tool_name": "Bash",
  "tool_input": {
    "command": "curl https://example.com"
  },
  "session_id": "abc123",
  "transcript_path": "/path/to/session-uuid/transcript.json"
}
```

Response formats:

**Block a tool:**
```json
{
  "permissionDecision": "deny",
  "reason": "Use capy_fetch_and_index instead"
}
```

**Allow with guidance:**
```json
{
  "additionalContext": "For file analysis, prefer capy_execute_file"
}
```

**Inject routing into subagent:**
```json
{
  "updatedInput": {
    "prompt": "... original prompt ... <context_window_protection>...</context_window_protection>"
  }
}
```

### 7.2 Adapter Implementation

```go
// internal/adapter/claudecode.go
package adapter

type ClaudeCodeAdapter struct{}

func (a *ClaudeCodeAdapter) ParsePreToolUse(input []byte) (*PreToolUseEvent, error) {
    var raw struct {
        ToolName  string                 `json:"tool_name"`
        ToolInput map[string]interface{} `json:"tool_input"`
        SessionID string                 `json:"session_id"`
    }
    if err := json.Unmarshal(input, &raw); err != nil {
        return nil, err
    }
    return &PreToolUseEvent{
        ToolName:  raw.ToolName,
        ToolInput: raw.ToolInput,
        SessionID: raw.SessionID,
    }, nil
}

func (a *ClaudeCodeAdapter) FormatBlock(reason string) ([]byte, error) {
    return json.Marshal(map[string]string{
        "permissionDecision": "deny",
        "reason": reason,
    })
}

func (a *ClaudeCodeAdapter) FormatAllow(guidance string) ([]byte, error) {
    if guidance == "" {
        return nil, nil
    }
    return json.Marshal(map[string]string{
        "additionalContext": guidance,
    })
}

func (a *ClaudeCodeAdapter) Capabilities() PlatformCapabilities {
    return PlatformCapabilities{
        PreToolUse:             true,
        PostToolUse:            true,
        PreCompact:             true,
        SessionStart:           true,
        CanModifyArgs:          true,
        CanModifyOutput:        true,
        CanInjectSessionContext: true,
    }
}
```

---

## 8. Configuration Implementation

**Reference:** No direct context-mode equivalent — this is new to capy.

### 8.1 Config Struct

```go
// internal/config/config.go
package config

type Config struct {
    Store    StoreConfig    `toml:"store"`
    Executor ExecutorConfig `toml:"executor"`
}

type StoreConfig struct {
    Path    string       `toml:"path"`     // DB path (relative or absolute)
    Cleanup CleanupConfig `toml:"cleanup"`
}

type CleanupConfig struct {
    ColdThresholdDays int  `toml:"cold_threshold_days"` // default 30
    AutoPrune         bool `toml:"auto_prune"`          // default false
}

type ExecutorConfig struct {
    Timeout        int `toml:"timeout"`          // default 30000 (ms)
    MaxOutputBytes int `toml:"max_output_bytes"` // default 102400
}
```

### 8.2 Loading with Precedence

```go
func Load(projectDir string) (*Config, error) {
    cfg := defaults()

    // Load in reverse precedence order (lower priority first, higher overwrites)
    xdgPath := xdgConfigPath()
    if xdgPath != "" {
        loadAndMerge(cfg, filepath.Join(xdgPath, "capy", "config.toml"))
    }
    loadAndMerge(cfg, filepath.Join(projectDir, ".capy", "config.toml"))
    loadAndMerge(cfg, filepath.Join(projectDir, ".capy.toml"))

    return cfg, nil
}

func defaults() *Config {
    return &Config{
        Store: StoreConfig{
            Cleanup: CleanupConfig{
                ColdThresholdDays: 30,
                AutoPrune:         false,
            },
        },
        Executor: ExecutorConfig{
            Timeout:        30000,
            MaxOutputBytes: 102400,
        },
    }
}
```

### 8.3 DB Path Resolution

```go
// internal/config/paths.go

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
    hash := projectHash(projectDir)
    return filepath.Join(dataHome, "capy", hash, "knowledge.db")
}

func projectHash(dir string) string {
    abs, _ := filepath.Abs(dir)
    h := sha256.Sum256([]byte(abs))
    return hex.EncodeToString(h[:6]) // 12 hex chars
}
```

---

## 9. Setup Command Implementation

### 9.1 Claude Code Setup

```go
func setupClaudeCode(binaryPath, projectDir string) error {
    // 1. Resolve binary path
    if binaryPath == "" {
        binaryPath, _ = exec.LookPath("capy")
    }

    // 2. Update .claude/settings.json (merge, don't overwrite)
    settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
    mergeHooks(settingsPath, binaryPath)

    // 3. Update .mcp.json or project MCP config
    mcpPath := filepath.Join(projectDir, ".mcp.json")
    mergeMCPServer(mcpPath, binaryPath)

    // 4. Append routing instructions to CLAUDE.md
    claudeMD := filepath.Join(projectDir, "CLAUDE.md")
    appendRoutingInstructions(claudeMD)

    // 5. Add .capy/ to .gitignore
    gitignorePath := filepath.Join(projectDir, ".gitignore")
    ensureGitignoreEntry(gitignorePath, ".capy/")

    return nil
}
```

### 9.2 Idempotent Merge

The merge logic for `settings.json`:
- Read existing file (or create empty object)
- Check if `hooks.PreToolUse` already contains a capy entry
- If not, append the capy hook entry
- Write back with proper JSON formatting

Same pattern for `.mcp.json` — check if `capy` server already registered.

For `CLAUDE.md` — check if `<context_window_protection>` block already exists. If so, skip. If file has content but no block, append. If file doesn't exist, create with block.

---

## 10. Testing Strategy

### 10.1 Test Organization

```
internal/store/store_test.go         — ContentStore unit tests
internal/store/search_test.go        — Three-tier search tests
internal/store/chunk_test.go         — Chunking strategy tests
internal/executor/executor_test.go   — Execution tests (need runtimes)
internal/executor/truncate_test.go   — Smart truncation tests
internal/executor/wrap_test.go       — Auto-wrapping tests
internal/security/security_test.go   — Permission evaluation tests
internal/security/glob_test.go       — Glob-to-regex tests
internal/security/split_test.go      — Command splitting tests
internal/hook/pretooluse_test.go     — PreToolUse routing tests
internal/adapter/claudecode_test.go  — Claude Code adapter tests
internal/config/config_test.go       — Config loading tests
internal/config/paths_test.go        — Path resolution tests
internal/server/server_test.go       — MCP server integration tests
```

### 10.2 Test Fixtures

Port key test fixtures from `context-mode/tests/fixtures/`:
- Large markdown documents (for chunking tests)
- JSON API responses (for JSON chunking)
- Log files (for plain text chunking)
- Security rule sets (for permission tests)
- Hook input/output JSON samples (for adapter tests)

### 10.3 Testing Dependencies

- `github.com/stretchr/testify` for assertions
- In-memory SQLite DB for store tests (use `:memory:` or temp files)
- `testing/fstest` or temp directories for file-based tests
- Mock HTTP server (`httptest.NewServer`) for `fetch_and_index` tests

### 10.4 Integration Tests

End-to-end MCP server tests:
1. Start server in test
2. Send JSON-RPC tool call requests
3. Verify responses match expected format
4. Verify side effects (DB state, stats)

Hook integration tests:
1. Pipe JSON input to hook handler
2. Verify JSON output matches expected format
3. Test all routing decisions (curl blocking, WebFetch blocking, Read guidance, etc.)

---

## 11. Key Implementation Notes

### 11.1 Error Handling

- Tool execution errors (non-zero exit code) are NOT Go errors — they're valid results. Return the output with exit code info.
- SQLite errors during search should be handled gracefully — return empty results, not errors.
- Hook parse errors should result in pass-through (don't block the tool if we can't understand the input).

### 11.2 Concurrency

- The MCP server handles one request at a time (JSON-RPC stdio is serial), but the store may be accessed by concurrent sessions if running as a background process.
- Use `sync.RWMutex` on the store for read/write safety.
- WAL mode + `_busy_timeout=5000` handle multi-process access.

### 11.3 Graceful Shutdown

Handle SIGTERM/SIGINT:
1. Close the SQLite database (flush WAL)
2. Kill any running child processes (via process group)
3. Exit cleanly

**Reference:** `context-mode/src/lifecycle.ts` — process lifecycle guard.

### 11.4 Output Encoding

All tool output is UTF-8. When reading subprocess output, use `bytes.Buffer` and convert to string (Go strings are UTF-8 by convention). Don't attempt Latin-1 or other encoding detection — context-mode doesn't either.

### 11.5 Null/Empty Output

When a subprocess produces no output: return `"(no output)"` string. This matches context-mode behavior.

**Reference:** `context-mode/docs/llms-full.txt` line 756.
