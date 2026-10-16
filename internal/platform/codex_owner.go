package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const codexConfigLimit = 1 << 20

// ErrCodexHookShadowed means a validated ancestor hook must leave the event to
// the nearer installation. It must not run policy or mutate either owner's state.
var ErrCodexHookShadowed = errors.New("Codex hook belongs to a shadowed ancestor installation")

// CodexBinding is a decoded generated invocation, not a shell interpretation.
type CodexBinding struct {
	ID, Mode, Event, Source string
}

// CodexOwner identifies a physical directory, even when another copy has its ID.
type CodexOwner struct {
	ProjectDir string
	ID         string
	Bindings   []CodexBinding
}

func readCodexFile(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%s must be a regular file, not a symlink or special file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, true, fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, true, fmt.Errorf("%s changed during inspection", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, true, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, true, nil
}

// InspectCodexOwners is shared by setup, runtime and registration diagnostics.
// It is read-only and deliberately knows nothing about policies or databases.
func InspectCodexOwners(cwd string) ([]CodexOwner, error) {
	return inspectCodexOwners(cwd, "")
}

// legacyMCPDir is set only by setup, to the physical project being upgraded.
// Ancestors and all runtime/doctor inspection retain strict launch validation.
func inspectCodexOwners(cwd, legacyMCPDir string) ([]CodexOwner, error) {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	var owners []CodexOwner
	for {
		owner, err := inspectCodexDirectory(dir, readCodexFile, dir == legacyMCPDir)
		if err != nil {
			return nil, fmt.Errorf("Codex installation at %s: %w; repair permissions/configuration and rerun capy setup --platform codex from the intended project", dir, err)
		}
		if owner.ID != "" {
			owners = append(owners, owner)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Different owners may nest, but copied IDs cannot identify which physical
	// definition launched us. Finish validating all adjacent bindings first.
	seen := make(map[string]string, len(owners))
	for _, owner := range owners {
		if previous, exists := seen[owner.ID]; exists {
			return nil, fmt.Errorf("overlapping Codex installations share owner ID at %s and %s; restore distinct installation configurations or use a non-overlapping checkout", previous, owner.ProjectDir)
		}
		seen[owner.ID] = owner.ProjectDir
	}
	return owners, nil
}

type codexFileReader func(string, int64) ([]byte, bool, error)

func inspectCodexDirectory(project string, read codexFileReader, allowLegacyMCP bool) (CodexOwner, error) {
	owner := CodexOwner{ProjectDir: project}
	dir := filepath.Join(project, ".codex")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return owner, nil
	}
	if err != nil {
		return owner, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return owner, fmt.Errorf("%s must be a directory, not a symlink", dir)
	}
	marker, exists, err := read(filepath.Join(dir, "capy-owner"), 80)
	if err != nil {
		return owner, err
	}
	if exists {
		owner.ID, err = parseCodexOwner(marker)
		if err != nil {
			return owner, err
		}
	}
	for _, name := range []string{"hooks.json", "config.toml"} {
		path := filepath.Join(dir, name)
		data, exists, err := read(path, codexConfigLimit)
		if err != nil {
			return owner, err
		}
		if !exists {
			continue
		}
		root := make(map[string]any)
		if name == "hooks.json" {
			root, err = decodeCodexJSON(data)
		} else {
			err = toml.Unmarshal(data, &root)
		}
		if err != nil || root == nil {
			return owner, fmt.Errorf("invalid %s: expected a configuration object (%v)", path, err)
		}
		var bindings []CodexBinding
		if name == "hooks.json" {
			bindings, err = codexHookBindings(root, path)
		} else {
			bindings, err = codexConfigBindingsWithLegacyMCP(root, path, allowLegacyMCP)
		}
		if err != nil {
			return owner, err
		}
		owner.Bindings = append(owner.Bindings, bindings...)
	}
	for _, binding := range owner.Bindings {
		if owner.ID == "" {
			return owner, fmt.Errorf("binding in %s has no adjacent .codex/capy-owner; restore the marker from committed configuration", binding.Source)
		}
		if binding.ID != owner.ID {
			return owner, fmt.Errorf("binding in %s disagrees with adjacent .codex/capy-owner; restore the matching committed configuration", binding.Source)
		}
	}
	return owner, nil
}

func codexHookBindings(root map[string]any, source string) ([]CodexBinding, error) {
	var bindings []CodexBinding
	if raw, exists := root["hooks"]; exists {
		hooks, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: hooks must be an object", source)
		}
		for event, rawGroups := range hooks {
			groups, ok := rawGroups.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: %s groups must be an array", source, event)
			}
			for _, rawGroup := range groups {
				group, ok := rawGroup.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s: %s group must be an object", source, event)
				}
				if matcher, exists := group["matcher"]; exists {
					if _, ok := matcher.(string); !ok {
						return nil, fmt.Errorf("%s: matcher must be a string", source)
					}
				}
				handlers, ok := group["hooks"].([]any)
				if !ok {
					return nil, fmt.Errorf("%s: %s handlers must be an array", source, event)
				}
				for _, rawHandler := range handlers {
					handler, ok := rawHandler.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("%s: handler must be an object", source)
					}
					handlerType, ok := handler["type"].(string)
					if !ok || handlerType == "" {
						return nil, fmt.Errorf("%s: handler type must be a nonempty string", source)
					}
					command, hasCommand := handler["command"]
					if !hasCommand && handlerType != "command" {
						continue
					}
					text, ok := command.(string)
					if !ok {
						return nil, fmt.Errorf("%s: hook command must be a string", source)
					}
					binding, owned, err := recognizeCodexHook(text)
					if err != nil {
						return nil, fmt.Errorf("%s: %w", source, err)
					}
					if owned {
						if handler["type"] != "command" || binding.Event != strings.ToLower(event) {
							return nil, fmt.Errorf("%s: generated hook event/type disagrees with its binding", source)
						}
						binding.Source = source
						bindings = append(bindings, binding)
					}
				}
			}
		}
	}
	return bindings, nil
}

func codexConfigBindings(root map[string]any, source string) ([]CodexBinding, error) {
	return codexConfigBindingsWithLegacyMCP(root, source, false)
}

func codexConfigBindingsWithLegacyMCP(root map[string]any, source string, allowLegacyMCP bool) ([]CodexBinding, error) {
	// Inline TOML includes Codex-owned hooks.state trust metadata alongside
	// event arrays. It contains no executable bindings. Exclude only that
	// reserved field without mutating the config the MCP merger must preserve.
	// hooks.json continues to use the strict event-only parser above.
	hookRoot := root
	if hooks, ok := root["hooks"].(map[string]any); ok {
		events := make(map[string]any, len(hooks))
		for event, groups := range hooks {
			if event != "state" {
				events[event] = groups
			}
		}
		hookRoot = map[string]any{"hooks": events}
	}
	bindings, err := codexHookBindings(hookRoot, source)
	if err != nil {
		return nil, err
	}
	if raw, exists := root["mcp_servers"]; exists {
		servers, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: mcp_servers must be an object", source)
		}
		for alias, rawServer := range servers {
			server, ok := rawServer.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: MCP server %s must be an object", source, alias)
			}
			command, _ := server["command"].(string)
			if raw, exists := server["command"]; exists {
				if _, ok := raw.(string); !ok {
					return nil, fmt.Errorf("%s: MCP command must be a string", source)
				}
			}
			var args []string
			rawArgs, exists := server["args"]
			values, ok := rawArgs.([]any)
			if exists && !ok {
				return nil, fmt.Errorf("%s: MCP args must be an array", source)
			}
			for _, rawArg := range values {
				arg, ok := rawArg.(string)
				if !ok {
					return nil, fmt.Errorf("%s: MCP arguments must be strings", source)
				}
				args = append(args, arg)
			}
			// The pre-owner setup format has no ID to restore. Only setup may
			// replace this exact named entry; it never authorizes runtime launch.
			if allowLegacyMCP && alias == "capy" && isLegacyCodexMCP(command, args) {
				continue
			}
			binding, owned, err := recognizeCodexInvocation(command, args)
			if err != nil {
				return nil, fmt.Errorf("%s MCP %s: %w", source, alias, err)
			}
			if owned {
				if binding.Mode != "serve" {
					return nil, fmt.Errorf("%s: MCP binding must use serve mode", source)
				}
				binding.Source = source
				bindings = append(bindings, binding)
			}
		}
	}
	return bindings, nil
}

func recognizeCodexInvocation(command string, args []string) (CodexBinding, bool, error) {
	var b CodexBinding
	recognizable := strings.Contains(command, "capy-codex-launch-") || (command == "capy" && len(args) > 0 && args[0] == "codex-launch")
	for _, arg := range args {
		recognizable = recognizable || strings.Contains(arg, "capy-codex-launch-")
		// Reject the obsolete project-script binding without interpreting or
		// converting its grammar. Unused files at that path remain irrelevant.
		recognizable = recognizable || strings.Contains(arg, ".codex/scripts/capy.sh")
	}
	if !recognizable {
		return b, false, nil
	}
	switch {
	case command == "capy" && len(args) == 5 && args[0] == "codex-launch" && args[1] == codexLaunchVersion && args[3] == "serve":
		b = CodexBinding{ID: args[2], Mode: args[3], Event: args[4]}
	case command == "bash" && len(args) == 6 && args[0] == "-c" && args[1] == codexBootstrap && args[2] == codexLaunchVersion && args[4] == "hook":
		b = CodexBinding{ID: args[3], Mode: args[4], Event: args[5]}
	default:
		return b, true, errors.New("invalid or unsupported capy-codex-launch invocation; stop affected sessions, restore final generated configuration and use compatible capy on PATH")
	}
	if err := validateCodexLaunch(b.ID, b.Mode, b.Event); err != nil {
		return b, true, err
	}
	return b, true, nil
}

func recognizeCodexHook(command string) (CodexBinding, bool, error) {
	if !strings.Contains(command, "capy-codex-launch-") {
		return CodexBinding{}, false, nil
	}
	// Only the generator's literal grammar is accepted. No shell evaluation or
	// whitespace splitting of the script (which itself contains quoted strings).
	prefix := "bash '-c' '" + shellEscapePath(codexBootstrap) + "' '" + codexLaunchVersion + "' '"
	rest, ok := strings.CutPrefix(command, prefix)
	parts := strings.Split(strings.TrimSuffix(rest, "'"), "' '")
	if !ok || len(parts) != 3 {
		return CodexBinding{}, true, errors.New("invalid or unsupported capy-codex-launch hook; restore generated configuration or upgrade capy")
	}
	b, _, err := recognizeCodexInvocation("bash", codexLaunchArgs(parts[0], parts[1], parts[2]))
	if err == nil && (b.Mode != "hook" || command != codexHookCommand(b.ID, b.Event)) {
		err = errors.New("invalid capy-codex-launch hook shape")
	}
	return b, true, err
}

func validateCodexLaunch(id, mode, event string) error {
	if !validCodexID(id) {
		return errors.New("invalid Codex owner ID; restore committed configuration")
	}
	if mode == "serve" && event == "" {
		return nil
	}
	if mode == "hook" {
		switch event {
		case "pretooluse", "sessionstart", "subagentstart", "posttooluse", "sessionend":
			return nil
		}
	}
	return errors.New("invalid Codex launch mode/event; rerun capy setup --platform codex")
}

// ResolveCodexOwner validates the invoked binding before selecting the nearest
// owner. Ancestor hooks are suppressed; ancestor MCP commands fail rather than
// silently opening either the ancestor's database or a different owner's database.
func ResolveCodexOwner(cwd, id, mode, event string) (string, error) {
	if err := validateCodexLaunch(id, mode, event); err != nil {
		return "", err
	}
	owners, err := InspectCodexOwners(cwd)
	if err != nil {
		return "", err
	}
	for _, owner := range owners {
		if owner.ID != id || !owner.hasBinding(mode, event) {
			continue
		}
		nearest := owners[0]
		if !nearest.hasBinding(mode, event) {
			return "", fmt.Errorf("nearest Codex installation at %s has no %s/%s binding; rerun capy setup --platform codex there and reload Codex", nearest.ProjectDir, mode, event)
		}
		if owner.ProjectDir == nearest.ProjectDir {
			return owner.ProjectDir, nil
		}
		if mode == "hook" {
			return "", ErrCodexHookShadowed
		}
		return "", fmt.Errorf("Codex MCP binding belongs to %s but nearer installation %s owns this directory; reload Codex with the nearer project's MCP registration", owner.ProjectDir, nearest.ProjectDir)
	}
	return "", errors.New("no matching Codex owner and mode/event binding above cwd; restore committed configuration and launch inside the intended project")
}

func (o CodexOwner) hasBinding(mode, event string) bool {
	for _, binding := range o.Bindings {
		if binding.Mode == mode && binding.Event == event {
			return true
		}
	}
	return false
}
