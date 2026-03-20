package executor

import (
	"os/exec"
	"path/filepath"
)

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

var scriptFilenames = map[Language]string{
	JavaScript: "script.js",
	TypeScript: "script.ts",
	Python:     "script.py",
	Shell:      "script.sh",
	Ruby:       "script.rb",
	Go:         "main.go",
	Rust:       "main.rs",
	PHP:        "script.php",
	Perl:       "script.pl",
	R:          "script.R",
	Elixir:     "script.exs",
}

// detectRuntimes probes the system for available language runtimes.
func detectRuntimes() map[Language]string {
	runtimes := make(map[Language]string)
	for lang, candidates := range runtimeCandidates {
		for _, bin := range candidates {
			if path, err := exec.LookPath(bin); err == nil {
				runtimes[lang] = path
				break
			}
		}
	}
	return runtimes
}

// buildCommand returns the command and args to execute a script file.
// For Rust, returns the special compile-then-run sentinel.
func buildCommand(lang Language, runtime, scriptPath string) (string, []string) {
	switch lang {
	case Go:
		return runtime, []string{"run", scriptPath}
	case Rust:
		// Special: compile then run. Caller handles two-step.
		return "__rust_compile_run__", []string{scriptPath}
	case JavaScript, TypeScript:
		base := filepath.Base(runtime)
		if base == "bun" {
			return runtime, []string{"run", scriptPath}
		}
		return runtime, []string{scriptPath}
	default:
		return runtime, []string{scriptPath}
	}
}
