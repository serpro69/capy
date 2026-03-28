package adapter

// HookAdapter abstracts platform-specific hook input parsing and response formatting.
type HookAdapter interface {
	ParsePreToolUse(input []byte) (*PreToolUseEvent, error)
	FormatBlock(reason string) ([]byte, error)
	FormatAllow(guidance string) ([]byte, error)
	FormatModify(updatedInput map[string]any) ([]byte, error)
	FormatAsk() ([]byte, error)
	FormatSessionStart(context string) ([]byte, error)
	Capabilities() PlatformCapabilities
}

// PreToolUseEvent is the parsed hook input for a PreToolUse event.
type PreToolUseEvent struct {
	ToolName   string
	ToolInput  map[string]any
	SessionID  string
	ProjectDir string
}

// PlatformCapabilities describes what a platform's hook system supports.
type PlatformCapabilities struct {
	PreToolUse             bool
	PostToolUse            bool
	PreCompact             bool
	SessionStart           bool
	CanModifyArgs          bool
	CanModifyOutput        bool
	CanInjectSessionContext bool
}
