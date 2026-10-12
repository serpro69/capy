package adapter

// Platform identifies the host explicitly; environment variables do not select it.
type Platform string

const (
	ClaudeCode Platform = "claude-code"
	Codex      Platform = "codex"
)

// HookAdapter abstracts platform-specific hook input parsing and response formatting.
type HookAdapter interface {
	Platform() Platform
	ParsePreToolUse(input []byte) (*PreToolUseEvent, error)
	FormatBlock(reason string) ([]byte, error)
	FormatAllow(guidance string) ([]byte, error)
	FormatModify(updatedInput map[string]any) ([]byte, error)
	FormatAsk(reason string) ([]byte, error)
	FormatSessionStart(context string) ([]byte, error)
	FormatSubagentStart(context string) ([]byte, error)
	Capabilities() PlatformCapabilities
}

// PreToolUseEvent is the parsed hook input for a PreToolUse event.
type PreToolUseEvent struct {
	ToolName        string
	ToolInput       map[string]any
	SessionID       string
	SessionIDStable bool // false for the process-parent fallback
	AgentID         string
	AgentType       string
	Cwd             string
	InvalidCwd      bool // supplied cwd was not a nonempty JSON string
}

// PlatformCapabilities describes the features implemented by an adapter.
// A false field does not imply that the host itself lacks that capability.
type PlatformCapabilities struct {
	PreToolUse              bool
	PostToolUse             bool
	PreCompact              bool
	SessionStart            bool
	CanModifyArgs           bool
	CanModifyOutput         bool
	CanInjectSessionContext bool
}
