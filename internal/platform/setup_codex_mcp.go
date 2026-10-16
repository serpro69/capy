package platform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func codexMCPBlock(id string) string {
	args, _ := json.Marshal(codexLaunchArgs(id, "serve", "")) // strings only
	return codexMCPMarker + "\ncommand = \"capy\"\nargs = " + string(args) + "\nenv_vars = [\"CAPY_DB_KEY\", \"CAPY_VAULT_KEY\"]\n"
}

// isLegacyCodexMCP recognizes the exact wrapper argv emitted before owner-bound
// launch. Setup replaces its registration without reading or running the script.
func isLegacyCodexMCP(command string, args []string) bool {
	if command != "bash" || len(args) != 2 {
		return false
	}
	return args[0] == ".codex/scripts/capy.sh" && args[1] == "serve"
}

// Preserve unrelated TOML bytes, including comments and multiline strings.
// The existing pinned parser finds real table boundaries; string searches can
// mistake a header inside a string/comment for the registration to replace.
func mergeCodexMCPServer(configPath, id string) error {
	data, _, err := readCodexFile(configPath, codexConfigLimit)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	bindings, err := codexConfigBindingsWithLegacyMCP(config, configPath, true)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.ID != id {
			return fmt.Errorf("%s: binding disagrees with owner marker", configPath)
		}
	}
	start, end := -1, len(data)
	var parser unstable.Parser
	parser.Reset(data)
	for parser.NextExpression() {
		expr := parser.Expression()
		if expr.Kind != unstable.Table && expr.Kind != unstable.ArrayTable {
			continue
		}
		var key []string
		it := expr.Key()
		firstOffset := -1
		for it.Next() {
			if firstOffset < 0 {
				firstOffset = int(it.Node().Raw.Offset)
			}
			key = append(key, string(it.Node().Data))
		}
		offset := bytes.LastIndexByte(data[:firstOffset], '\n') + 1
		if start >= 0 {
			end = offset
			break
		}
		if len(key) == 2 && key[0] == "mcp_servers" && key[1] == "capy" && expr.Kind == unstable.Table {
			start = offset
		}
	}
	if err := parser.Error(); err != nil {
		return fmt.Errorf("locate MCP table: %w", err)
	}
	block := codexMCPBlock(id)
	var updated string
	if start < 0 {
		if servers, ok := config["mcp_servers"].(map[string]any); ok && servers["capy"] != nil {
			return fmt.Errorf("%s: express the capy server as a [mcp_servers.capy] table before setup", configPath)
		}
		updated = string(data)
		if len(data) > 0 {
			updated = strings.TrimRight(updated, "\n") + "\n\n"
		}
		updated += block
	} else {
		if strings.TrimSpace(string(data[start:end])) == strings.TrimSpace(block) {
			return nil
		}
		if end < len(data) {
			block += "\n"
		}
		updated = string(data[:start]) + block + string(data[end:])
	}
	// Also catch dotted/subtable conflicts before writing; invalid files survive.
	if len(updated) > codexConfigLimit {
		return fmt.Errorf("%s would exceed %d bytes after merging; reduce the configuration before setup", configPath, codexConfigLimit)
	}
	// Decode into a fresh map: the pinned parser cannot reuse nested array-table
	// values (inline hooks) already stored in a map[string]any.
	var merged map[string]any
	if err := toml.Unmarshal([]byte(updated), &merged); err != nil {
		return fmt.Errorf("cannot safely merge %s: %w", configPath, err)
	}
	return os.WriteFile(configPath, []byte(updated), 0o644)
}
