package security

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CaptureInvocation describes a literal capy execute call. It carries selectors
// for checked code-file resolution, never a claim that the executable/storage is
// available. Programs and selectors must not be copied into hook diagnostics.
type CaptureInvocation struct {
	Language   string
	Code       string
	CodeFile   string
	ProjectDir string
	Cwd        string
}

func parseCaptureInvocation(words []shellWord) (CaptureInvocation, bool, error) {
	inv := CaptureInvocation{Language: "shell"}
	args := words[1:]
	// The sole inherited flag may precede the subcommand.
	for len(args) > 0 && args[0].literal && (args[0].value == "--project-dir" || strings.HasPrefix(args[0].value, "--project-dir=")) {
		value, rest, err := captureFlagValue(args)
		if err != nil {
			return inv, false, err
		}
		inv.ProjectDir, args = value, rest
	}
	if len(args) == 0 || !literalValue(args[0], "execute") {
		return inv, false, nil
	}
	args = args[1:]
	codeSet, fileSet, help := false, false, false
	var intent string
	var timeoutMS int64
	var timeoutSet, cwdSet bool
	for len(args) > 0 {
		if !args[0].literal {
			return inv, false, fmt.Errorf("capture arguments must be literal for policy inspection")
		}
		name, value, equals := strings.Cut(args[0].value, "=")
		if name == "--" && len(args) == 1 {
			break
		}
		if name == "--json" || name == "--help" || name == "-h" {
			enabled := true
			if equals {
				var err error
				enabled, err = strconv.ParseBool(value)
				if err != nil {
					return inv, false, fmt.Errorf("invalid capture boolean flag")
				}
			}
			if name != "--json" {
				help = enabled
			}
			args = args[1:]
			continue
		}
		switch name {
		case "--language", "--code", "--code-file", "--cwd", "--timeout-ms", "--intent", "--project-dir":
		default:
			return inv, false, fmt.Errorf("invalid or unsupported capture argument")
		}
		var err error
		value, args, err = captureFlagValue(args)
		if err != nil {
			return inv, false, err
		}
		// Cobra/pflag uses the last value of repeated scalar flags; presence of
		// both code selectors remains an error even if one was empty.
		switch name {
		case "--language":
			inv.Language = value
		case "--code":
			inv.Code, codeSet = value, true
		case "--code-file":
			inv.CodeFile, fileSet = value, true
		case "--project-dir":
			inv.ProjectDir = value
		case "--cwd":
			inv.Cwd, cwdSet = value, true
		case "--timeout-ms":
			timeoutMS, err = strconv.ParseInt(value, 0, 64)
			if err != nil {
				return inv, false, fmt.Errorf("invalid capture timeout-ms")
			}
			timeoutSet = true
		case "--intent":
			intent = value
		}
	}
	if !help {
		if cwdSet && inv.Cwd == "" {
			return inv, false, fmt.Errorf("capture cwd must be nonempty")
		}
		if timeoutSet && (timeoutMS <= 0 || timeoutMS > math.MaxInt64/int64(time.Millisecond)) {
			return inv, false, fmt.Errorf("invalid capture timeout-ms")
		}
		if len(strings.TrimSpace(intent)) > 512 || !utf8.ValidString(intent) {
			return inv, false, fmt.Errorf("invalid capture intent")
		}
	}
	return finishCapture(inv, codeSet, fileSet, help)
}

func captureFlagValue(args []shellWord) (string, []shellWord, error) {
	if !args[0].literal {
		return "", nil, fmt.Errorf("capture flag must be literal")
	}
	if _, value, ok := strings.Cut(args[0].value, "="); ok {
		return value, args[1:], nil
	}
	if len(args) < 2 || !args[1].literal {
		return "", nil, fmt.Errorf("capture flag requires a literal value")
	}
	return args[1].value, args[2:], nil
}

func finishCapture(inv CaptureInvocation, codeSet, fileSet, help bool) (CaptureInvocation, bool, error) {
	if help {
		return inv, false, nil
	}
	if codeSet == fileSet || codeSet && (inv.Code == "" || len(inv.Code) > MaxShellInputBytes) || fileSet && inv.CodeFile == "" {
		return inv, false, fmt.Errorf("capture requires exactly one nonempty code or code-file input")
	}
	switch inv.Language {
	case "shell", "python", "go", "rust", "javascript", "typescript", "ruby", "perl", "php", "r", "elixir":
	default:
		return inv, false, fmt.Errorf("unsupported capture language")
	}
	return inv, true, nil
}
