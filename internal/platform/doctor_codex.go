package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CheckPlatformRegistrations reports configured platforms independently. It
// inspects files only: no credentials, databases, trust records or hook runs.
func CheckPlatformRegistrations(projectDir string) []CheckResult {
	results := []CheckResult{}
	if claudeRegistrationPresent(projectDir) {
		hooks, mcp := CheckHookRegistration(projectDir), CheckMCPRegistration(projectDir)
		hooks.Name, mcp.Name = "Claude Code hooks", "Claude Code MCP"
		results = append(results, hooks, mcp)
	}

	owners, err := InspectCodexOwners(projectDir)
	// An uninspectable config is uncertainty, not proof that no binding exists.
	// Report the same read/shape error that would stop the launcher.
	if err != nil {
		return append(results, codexRegistrationFailure(err)...)
	}
	if len(owners) > 1 {
		dirs := make([]string, 0, len(owners))
		for _, owner := range owners {
			dirs = append(dirs, owner.ProjectDir)
		}
		err := fmt.Errorf("overlapping Codex installations: %s; consolidate to one owner or use a non-overlapping checkout", strings.Join(dirs, ", "))
		return append(results, codexRegistrationFailure(err)...)
	}
	if len(owners) == 1 {
		return append(results, checkCodexRegistration(owners[0])...)
	}
	// An unbound named MCP registration is incomplete. An unused wrapper or
	// unrelated .codex configuration does not establish an executable binding.
	config, err := readDoctorCodexConfig(projectDir)
	if err != nil {
		return append(results, codexRegistrationFailure(err)...)
	}
	servers, _ := config["mcp_servers"].(map[string]any)
	_, legacyMCP := servers["capy"]
	if legacyMCP {
		return append(results, codexRegistrationFailure(errors.New("incomplete Codex registration: missing owner binding; run capy setup --platform codex"))...)
	}
	if len(results) == 0 {
		results = append(results, CheckResult{
			Name: "Platform registration", Status: Fail,
			Detail: "no capy registration found; run `capy setup --platform claude-code` or `capy setup --platform codex`",
		})
	}
	return results
}

func registrationArtifactExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func claudeRegistrationPresent(projectDir string) bool {
	if registrationArtifactExists(filepath.Join(projectDir, capyWrapperRelPath)) {
		return true
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		if countCapyHooks(filepath.Join(projectDir, ".claude", name)) > 0 {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(projectDir, ".mcp.json"))
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	servers, _ := root["mcpServers"].(map[string]any)
	_, exists := servers["capy"]
	return exists
}

func codexRegistrationFailure(err error) []CheckResult {
	return []CheckResult{
		{Name: "Codex hooks", Status: Fail, Detail: err.Error()},
		{Name: "Codex MCP", Status: Fail, Detail: err.Error()},
	}
}

func checkCodexRegistration(owner CodexOwner) []CheckResult {
	config, err := readDoctorCodexConfig(owner.ProjectDir)
	if err != nil {
		return codexRegistrationFailure(err)
	}
	checks := []CheckResult{checkCodexHooks(owner, config), checkCodexMCP(owner)}
	return append(checks, checkCodexActivation(owner.ProjectDir, config))
}

func readDoctorCodexConfig(projectDir string) (map[string]any, error) {
	path := filepath.Join(projectDir, ".codex/config.toml")
	data, _, err := readCodexFile(path, codexConfigLimit)
	if err != nil {
		return nil, err
	}
	root := make(map[string]any)
	if err := toml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	// Share structural and invocation validation with setup and runtime. The
	// owner's adjacent-marker agreement was checked by InspectCodexOwners.
	if _, err := codexConfigBindings(root, path); err != nil {
		return nil, err
	}
	return root, nil
}

func checkCodexHooks(owner CodexOwner, config map[string]any) CheckResult {
	result := CheckResult{Name: "Codex hooks", Status: Fail}
	path := filepath.Join(owner.ProjectDir, codexHooksRelPath)
	data, exists, err := readCodexFile(path, codexConfigLimit)
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	if !exists {
		result.Detail = "missing " + path + "; run capy setup --platform codex"
		return result
	}
	root, err := decodeCodexJSON(data)
	if err == nil {
		_, err = codexHookBindings(root, path)
	}
	if err != nil {
		result.Detail = fmt.Sprintf("malformed %s: %v", path, err)
		return result
	}
	hooks, _ := root["hooks"].(map[string]any)
	inline, _ := config["hooks"].(map[string]any)
	registered := 0
	missing, invalid, duplicates := []string{}, []string{}, []string{}
	for _, event := range codexHookEvents {
		count, valid := doctorCodexEvent(hooks, event, owner.ID)
		inlineCount, _ := doctorCodexEvent(inline, event, owner.ID)
		if count == 0 {
			missing = append(missing, event.Event)
		} else {
			registered++
		}
		if !valid {
			invalid = append(invalid, event.Event)
		}
		if count+inlineCount > 1 {
			duplicates = append(duplicates, event.Event)
		}
	}
	result.Status = Pass
	result.Detail = fmt.Sprintf("%d/%d events registered in %s (owner %s)", registered, len(codexHookEvents), path, owner.ID)
	if len(missing) > 0 {
		result.Status = Warn
		if registered == 0 {
			result.Status = Fail
		}
		result.Detail += "; missing: " + strings.Join(missing, ", ")
	}
	if len(invalid) > 0 {
		result.Status = Fail
		result.Detail += "; invalid matcher/invocation: " + strings.Join(invalid, ", ")
	}
	if len(duplicates) > 0 {
		result.Status = Fail
		result.Detail += "; duplicate owned handlers: " + strings.Join(duplicates, ", ")
	}
	if result.Status != Pass {
		result.Detail += "; inspect owned definitions and rerun capy setup --platform codex (inline hooks require manual review)"
	}
	return result
}

// Inputs have passed codexHookBindings. External handlers never count toward
// installation completeness, and a valid invocation must retain its matcher.
func doctorCodexEvent(hooks map[string]any, event codexHookEvent, id string) (int, bool) {
	groups, _ := hooks[event.Event].([]any)
	count, valid := 0, true
	for _, rawGroup := range groups {
		group := rawGroup.(map[string]any)
		for _, rawHandler := range group["hooks"].([]any) {
			handler := rawHandler.(map[string]any)
			command, _ := handler["command"].(string)
			binding, owned, err := recognizeCodexHook(command)
			if !owned {
				continue
			}
			count++
			valid = valid && err == nil && binding.ID == id && group["matcher"] == event.Matcher
		}
	}
	return count, valid
}

func checkCodexMCP(owner CodexOwner) CheckResult {
	result := CheckResult{Name: "Codex MCP", Status: Fail}
	count := 0
	for _, binding := range owner.Bindings {
		if binding.Mode == "serve" {
			count++
		}
	}
	switch count {
	case 0:
		result.Detail = "missing owner-bound MCP invocation; run capy setup --platform codex"
	case 1:
		result.Status = Pass
		result.Detail = fmt.Sprintf("registered in %s (owner %s); direct PATH argv validated", filepath.Join(owner.ProjectDir, ".codex/config.toml"), owner.ID)
	default:
		result.Detail = fmt.Sprintf("duplicate owner-bound MCP registrations (%d); retain one generated server entry in .codex/config.toml", count)
	}
	return result
}

func checkCodexActivation(projectDir string, config map[string]any) CheckResult {
	parts := []string{"project trust, exact hook trust and execution are unverified; inspect /hooks and restart/resume as needed"}
	features, _ := config["features"].(map[string]any)
	for _, key := range []string{"hooks", "codex_hooks"} {
		if value, exists := features[key]; exists && value == false {
			parts = append(parts, fmt.Sprintf("visible features.%s = false in %s", key, filepath.Join(projectDir, ".codex/config.toml")))
		}
	}
	inline, _ := config["hooks"].(map[string]any)
	for _, event := range codexHookEvents {
		groups, _ := inline[event.Event].([]any)
		for _, raw := range groups {
			group := raw.(map[string]any) // validated in readDoctorCodexConfig
			if len(group["hooks"].([]any)) > 0 {
				parts = append(parts, "inline "+event.Event+" hooks also configured; potential source overlap requires review")
				break
			}
		}
	}
	parts = append(parts, "other configuration layers, feature precedence and host trust records are not inspected")
	return CheckResult{Name: "Codex activation", Status: Warn, Detail: strings.Join(parts, "; ")}
}
