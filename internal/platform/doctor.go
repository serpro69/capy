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

// FormatDiagnostics formats a list of check results as a markdown report.
func FormatDiagnostics(results []CheckResult) string {
	var lines []string
	lines = append(lines, "## capy doctor", "")
	for _, r := range results {
		lines = append(lines, r.String())
	}
	return strings.Join(lines, "\n")
}
