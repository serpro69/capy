package executor

// Language identifies a programming language for execution.
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

	// TotalLanguages is the count of supported languages.
	TotalLanguages = 11
)

// ExecRequest describes a code execution request.
type ExecRequest struct {
	Language   Language
	Code       string
	FilePath   string // for ExecuteFile
	Background bool
	TimeoutSec int // 0 = use default
}

// ExecResult describes the outcome of an execution.
type ExecResult struct {
	Stdout       string
	Stderr       string
	ExitCode     int
	TimedOut     bool
	Killed       bool // hard cap exceeded
	Backgrounded bool
	PID          int // only set if backgrounded
}

// ExitClassification describes whether a non-zero exit is an error.
type ExitClassification struct {
	IsError bool
	Output  string
}
