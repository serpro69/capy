package platform

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/serpro69/capy/internal/version"
)

// CheckStatus indicates the result of a diagnostic check.
type CheckStatus string

const (
	Pass CheckStatus = "pass"
	Warn CheckStatus = "warn"
	Fail CheckStatus = "fail"
)

// CheckResult holds the outcome of a single diagnostic check.
type CheckResult struct {
	Name   string
	Status CheckStatus
	Detail string
}

// Marker returns the checkbox marker for the status: [x], [-], [ ].
func (r CheckResult) Marker() string {
	switch r.Status {
	case Pass:
		return "[x]"
	case Warn:
		return "[-]"
	default:
		return "[ ]"
	}
}

// String formats the check result as a markdown checklist item.
func (r CheckResult) String() string {
	return fmt.Sprintf("- %s %s: %s", r.Marker(), r.Name, r.Detail)
}

// CheckVersion returns the capy version.
func CheckVersion() CheckResult {
	return CheckResult{
		Name:   "Version",
		Status: Pass,
		Detail: version.Version,
	}
}

// CheckRuntimes checks available language runtimes by probing the system PATH.
// It accepts a pre-detected runtime map (from executor.Runtimes()) to avoid
// coupling this package to the executor package.
func CheckRuntimes(runtimes map[string]string, totalLanguages int) CheckResult {
	if len(runtimes) == 0 {
		return CheckResult{
			Name:   "Runtimes",
			Status: Fail,
			Detail: "none detected",
		}
	}

	langs := make([]string, 0, len(runtimes))
	for lang := range runtimes {
		langs = append(langs, lang)
	}
	slices.Sort(langs)

	status := Pass
	if len(runtimes) < 2 {
		status = Warn
	}

	return CheckResult{
		Name:   "Runtimes",
		Status: status,
		Detail: fmt.Sprintf("%d/%d (%s)", len(runtimes), totalLanguages, strings.Join(langs, ", ")),
	}
}

// CheckFTS5 verifies FTS5 is available by attempting to create a virtual table
// in an in-memory database.
func CheckFTS5() CheckResult {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return CheckResult{
			Name:   "FTS5",
			Status: Fail,
			Detail: fmt.Sprintf("cannot open SQLite: %v", err),
		}
	}
	defer db.Close()

	_, err = db.Exec("CREATE VIRTUAL TABLE fts5_test USING fts5(content)")
	if err != nil {
		return CheckResult{
			Name:   "FTS5",
			Status: Fail,
			Detail: "unavailable (binary may not be built with -tags fts5)",
		}
	}

	return CheckResult{
		Name:   "FTS5",
		Status: Pass,
		Detail: "available",
	}
}

// CheckConfig verifies config loading for the given project directory.
func CheckConfig(projectDir string, dbPath string) CheckResult {
	if dbPath != "" {
		return CheckResult{
			Name:   "Config",
			Status: Pass,
			Detail: fmt.Sprintf("loaded (db path: %s)", dbPath),
		}
	}
	return CheckResult{
		Name:   "Config",
		Status: Warn,
		Detail: "using defaults",
	}
}

// CheckHookRegistration verifies that capy hooks are registered in either
// .claude/settings.json or .claude/settings.local.json.
func CheckHookRegistration(projectDir string) CheckResult {
	claudeDir := filepath.Join(projectDir, ".claude")

	// Check both settings files for capy hooks
	registered := 0
	foundIn := ""
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		n := countCapyHooks(filepath.Join(claudeDir, filename))
		if n > registered {
			registered = n
			foundIn = filename
		}
	}

	if registered == 0 {
		return CheckResult{
			Name:   "Hooks",
			Status: Fail,
			Detail: "no capy hooks found (run `capy setup`)",
		}
	}

	if registered < len(hookEvents) {
		return CheckResult{
			Name:   "Hooks",
			Status: Warn,
			Detail: fmt.Sprintf("%d/%d hook events registered in %s", registered, len(hookEvents), foundIn),
		}
	}

	return CheckResult{
		Name:   "Hooks",
		Status: Pass,
		Detail: fmt.Sprintf("%d/%d hook events registered in %s", registered, len(hookEvents), foundIn),
	}
}

// countCapyHooks returns how many capy hook events are registered in the given settings file.
func countCapyHooks(settingsPath string) int {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return 0
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return 0
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return 0
	}

	count := 0
	for _, he := range hookEvents {
		entries, _ := hooks[he.Event].([]any)
		if findHookEntry(entries, "hook "+he.CLIArg) >= 0 {
			count++
		}
	}
	return count
}

// CheckMCPRegistration verifies that capy is registered as an MCP server in .mcp.json.
func CheckMCPRegistration(projectDir string) CheckResult {
	mcpPath := filepath.Join(projectDir, ".mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		return CheckResult{
			Name:   "MCP",
			Status: Fail,
			Detail: ".mcp.json not found (run `capy setup`)",
		}
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return CheckResult{
			Name:   "MCP",
			Status: Fail,
			Detail: fmt.Sprintf("cannot parse .mcp.json: %v", err),
		}
	}

	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return CheckResult{
			Name:   "MCP",
			Status: Fail,
			Detail: "no MCP servers configured (run `capy setup`)",
		}
	}

	if _, ok := servers["capy"]; !ok {
		return CheckResult{
			Name:   "MCP",
			Status: Fail,
			Detail: "capy not registered as MCP server (run `capy setup`)",
		}
	}

	// Verify the command path exists
	serverCfg, _ := servers["capy"].(map[string]any)
	command, _ := serverCfg["command"].(string)
	if command != "" {
		if _, err := exec.LookPath(command); err != nil {
			return CheckResult{
				Name:   "MCP",
				Status: Warn,
				Detail: fmt.Sprintf("registered but binary not found at: %s", command),
			}
		}
	}

	return CheckResult{
		Name:   "MCP",
		Status: Pass,
		Detail: "registered",
	}
}

// CheckSecurity reports on loaded security policies.
func CheckSecurity(denyCount, policyFileCount int) CheckResult {
	if policyFileCount == 0 {
		return CheckResult{
			Name:   "Security",
			Status: Warn,
			Detail: "no deny policies loaded",
		}
	}
	return CheckResult{
		Name:   "Security",
		Status: Pass,
		Detail: fmt.Sprintf("%d policy files, %d deny patterns", policyFileCount, denyCount),
	}
}

// CheckKnowledgeBase checks if the knowledge base exists and reports stats.
func CheckKnowledgeBase(dbPath string) CheckResult {
	info, err := os.Stat(dbPath)
	if err != nil {
		return CheckResult{
			Name:   "Knowledge base",
			Status: Warn,
			Detail: "not initialized (will be created on first use)",
		}
	}

	return CheckResult{
		Name:   "Knowledge base",
		Status: Pass,
		Detail: fmt.Sprintf("exists (%s, %d bytes)", dbPath, info.Size()),
	}
}

// CheckKnowledgeBaseStats reports a readable knowledge base by its live row
// counts. Shared by `capy doctor` and the capy_doctor MCP tool so both surfaces
// describe the store the same way.
func CheckKnowledgeBaseStats(sourceCount, chunkCount int) CheckResult {
	return CheckResult{
		Name:   "Knowledge base",
		Status: Pass,
		Detail: fmt.Sprintf("%d sources, %d chunks", sourceCount, chunkCount),
	}
}

// CheckKnowledgeBaseError reports a knowledge base that exists on disk but
// whose stats could not be read (missing CAPY_DB_KEY, wrong key, corruption).
func CheckKnowledgeBaseError(err error) CheckResult {
	return CheckResult{
		Name:   "Knowledge base",
		Status: Warn,
		Detail: fmt.Sprintf("error reading stats (%v)", err),
	}
}

// CheckLegacySessions warns about leftover `kind='session'` rows in the
// knowledge base. The knowledge.db session sweep was retired (ADR-027) — the
// vault is the session store — so any such rows are pre-removal leftovers
// draining by TTL. reclaimCmd names the surface-appropriate reclaim command
// (`capy cleanup --kind session --force` on the CLI, `capy_cleanup
// purge_session` over MCP) so the hint is actionable where it is shown.
//
// TODO(legacy-sessions): "reclaim now" overstates what the session purge does —
// PurgeSession honours the session TTL (60d default), so rows younger than that
// survive it. Since nothing writes new session rows they drain on their own; to
// make the hint literally true, PurgeSession would need an ignore-TTL mode.
func CheckLegacySessions(rows int, reclaimCmd string) CheckResult {
	return CheckResult{
		Name:   "Legacy sessions",
		Status: Warn,
		Detail: fmt.Sprintf("%d legacy knowledge.db session row(s) — reclaim now with `%s` (the vault is the session store)",
			rows, reclaimCmd),
	}
}

// CheckVaultDisabled reports the opt-in vault as off (CAPY_VAULT_KEY unset).
func CheckVaultDisabled() CheckResult {
	return CheckResult{
		Name:   "Vault",
		Status: Warn,
		Detail: "disabled (CAPY_VAULT_KEY not set) — sessions are not archived",
	}
}

// CheckVault reports the vault's health from its stats: unreadable, or enabled
// with its session count and — when any archived session predates the current
// indexer — the reindex backlog and the command that clears it. Reindex is a
// manual `capy vault reindex` (not automatic), so its pendency must be visible,
// not silent (vault-session-search design D4).
func CheckVault(sessions, outdatedSessions, indexVersion int, err error) CheckResult {
	if err != nil {
		return CheckResult{
			Name:   "Vault",
			Status: Warn,
			Detail: fmt.Sprintf("error reading stats (%v)", err),
		}
	}
	detail := fmt.Sprintf("%d sessions archived", sessions)
	if outdatedSessions > 0 {
		return CheckResult{
			Name:   "Vault",
			Status: Warn,
			Detail: fmt.Sprintf("%s; %d indexed by an older version (v%d) — run `capy vault reindex` to update them",
				detail, outdatedSessions, indexVersion),
		}
	}
	return CheckResult{Name: "Vault", Status: Pass, Detail: detail}
}

// VaultPlatformRoot is one agent CLI's session root as CheckVaultPlatforms
// reports it. Strings only, on purpose: internal/platform does not import
// internal/vault and must not start to (the vault is a heavy, key-gated store;
// the doctor is a leaf), so callers copy the fields out of vault.PlatformRoot.
type VaultPlatformRoot struct {
	Name       string // the stored platform token (`claude-code`, `codex`) — the value `--platform` accepts
	Root       string // the session root on disk (Claude: the projects dir; Codex: the Codex home)
	RootExists bool   // whether the startup sweep would walk it
	Archived   int    // sessions of this platform the vault holds
}

// CheckVaultPlatforms reports, per known platform, whether its session root
// exists on disk and how many of its sessions are archived, so a dual-tool
// user can see that Codex (or Claude Code) is being swept. err is a root
// resolution failure (an unresolvable home directory) and is reported like
// CheckVault's. An absent root is normal — a Claude-only machine has no Codex
// home — and stays Pass; the check warns only when NO root exists, since the
// sweep then has nothing to archive.
func CheckVaultPlatforms(roots []VaultPlatformRoot, err error) CheckResult {
	const name = "Vault platforms"
	if err != nil {
		return CheckResult{
			Name:   name,
			Status: Warn,
			Detail: fmt.Sprintf("error resolving platform roots (%v)", err),
		}
	}
	parts := make([]string, 0, len(roots))
	var anyRoot bool
	for _, r := range roots {
		if r.RootExists {
			anyRoot = true
			parts = append(parts, fmt.Sprintf("%s: %d archived (%s)", r.Name, r.Archived, r.Root))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d archived (%s absent — not swept)", r.Name, r.Archived, r.Root))
	}
	if !anyRoot {
		detail := "no platform session root found on disk — the startup sweep has nothing to archive"
		if len(parts) > 0 {
			detail += "; " + strings.Join(parts, "; ")
		}
		return CheckResult{Name: name, Status: Warn, Detail: detail}
	}
	return CheckResult{Name: name, Status: Pass, Detail: strings.Join(parts, "; ")}
}

// FormatDiagnostics formats a list of check results as a markdown report.
func FormatDiagnostics(results []CheckResult) string {
	var lines []string
	lines = append(lines, "## capy doctor", "")
	for _, r := range results {
		lines = append(lines, r.String())
	}
	return strings.Join(lines, "\n")
}
