package security

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RoutineOptions supplies output exceptions and independent security rules.
// ReadCodeFile must perform checked, bounded file admission using the selected
// CLI owner (including ProjectDir), and enforce matched Read asks. A nil reader
// rejects code-file inspection; it never certifies an unchecked program.
// Code-file calls in compound requests, shell/mise or directory-changing
// wrappers, or another captured program are rejected: their launch owner needs
// runtime shell state (including shell startup and mise directory options).
type RoutineOptions struct {
	NativeAllow  []string
	Policies     []SecurityPolicy
	ReadCodeFile func(CaptureInvocation) (string, error)
}

// RoutineInspection is a static classification, not an execution permission.
// Callers must handle errors and Security before consulting Required. Task 15
// wires this into Codex routing; neither this API nor config enables a hook gate.
type RoutineInspection struct {
	Required []string
	Captures []CaptureInvocation
	Security CommandDecision
}

// ValidateRoutineNativeAllow accepts only the existing Bash(pattern) grammar.
func ValidateRoutineNativeAllow(patterns []string) error {
	for _, pattern := range patterns {
		if strings.TrimSpace(parseBashPattern(pattern)) == "" {
			return fmt.Errorf("routing.routine.native_allow entries must be Bash(pattern) rules")
		}
	}
	return nil
}

// InspectRoutineCommand recognizes literal catalogue commands, wrappers and
// capture invocations without executing anything or resolving credentials.
// Scanner or recursive-work overflow returns no partial classification.
func InspectRoutineCommand(command string, options RoutineOptions) (RoutineInspection, error) {
	if err := ValidateRoutineNativeAllow(options.NativeAllow); err != nil {
		return RoutineInspection{}, err
	}
	i := routineInspector{options: options, remaining: 8*len(command) + 4096}
	if err := i.shell(command, false, 0); err != nil {
		return RoutineInspection{}, err
	}
	i.result.Security = evaluateSegments(i.policy, options.Policies)
	return i.result, nil
}

type routineInspector struct {
	options            RoutineOptions
	result             RoutineInspection
	policy             []string
	remaining          int
	elements           int
	fileBytes          int
	directoryUncertain bool
}

func (i *routineInspector) charge(size, depth int) error {
	if depth > MaxShellFrames {
		return &ShellScanLimitError{Limit: "frames", Maximum: MaxShellFrames}
	}
	i.remaining -= size
	if i.remaining < 0 {
		return &ShellScanLimitError{Limit: "routine work", Maximum: MaxShellInputBytes}
	}
	return nil
}

func (i *routineInspector) shell(code string, captured bool, depth int) error {
	if err := i.charge(len(code), depth); err != nil {
		return err
	}
	elements, err := SplitChainedCommands(code)
	if err != nil {
		return err
	}
	// Do not resolve a later code-file against the initial cwd when preceding
	// shell elements could change it. We do not interpret shell state/control flow.
	if len(elements) > 1 {
		i.directoryUncertain = true
	}
	for _, element := range elements {
		i.elements++
		if i.elements > MaxShellElements {
			return &ShellScanLimitError{Limit: "elements", Maximum: MaxShellElements}
		}
		if err := i.charge(len(element), depth); err != nil {
			return err
		}
		i.policy = append(i.policy, element)
		words, err := literalShellWords(element)
		if err != nil {
			return err
		}
		if err := i.command(element, words, captured, depth); err != nil {
			return err
		}
	}
	return nil
}

func (i *routineInspector) command(element string, words []shellWord, captured bool, depth int) error {
	for len(words) > 0 {
		if err := i.charge(1, depth); err != nil {
			return err
		}
		if !words[0].literal {
			return nil
		}
		// Keep every wrapper spelling as a policy candidate, as well as the
		// decoded argv. Quoting a literal executable must not hide a deny/ask.
		suffix := strings.TrimSpace(element[words[0].start:])
		if err := i.charge(len(suffix), depth); err != nil {
			return err
		}
		i.policy = append(i.policy, suffix)
		if words[0].assignment {
			words = words[1:]
			depth++
			continue
		}
		name := filepath.Base(words[0].value)
		switch name {
		case "env":
			var changesDirectory bool
			words, changesDirectory = envCommand(words[1:])
			i.directoryUncertain = i.directoryUncertain || changesDirectory
			depth++
			continue
		case "command":
			words = words[1:]
			for len(words) > 0 && words[0].literal && (words[0].value == "-p" || words[0].value == "--") {
				words = words[1:]
			}
			if len(words) > 0 && strings.HasPrefix(words[0].value, "-") {
				return nil // command -v/-V probes do not execute argv.
			}
			depth++
			continue
		case "mise":
			if len(words) > 2 && literalValue(words[1], "exec", "x") {
				for n := 2; n < len(words); n++ {
					if !words[n].literal {
						return nil
					}
					if words[n].value == "--" {
						i.directoryUncertain = true
						words = words[n+1:]
						depth++
						name = ""
						break
					}
				}
				if name == "" {
					continue
				}
			}
		}
		var decoded []string
		for _, word := range words {
			if !word.literal {
				decoded = nil
				break
			}
			decoded = append(decoded, word.value)
		}
		if decoded != nil {
			i.policy = append(i.policy, strings.Join(decoded, " "))
		}
		if script, ok := literalShellScript(words); ok {
			i.directoryUncertain = true
			return i.shell(script, captured, depth+1)
		}
		if name == "capy" {
			invocation, ok, err := parseCaptureInvocation(words)
			if err != nil || !ok {
				return err
			}
			i.result.Captures = append(i.result.Captures, invocation)
			code := invocation.Code
			if invocation.CodeFile != "" {
				if captured || i.directoryUncertain {
					return fmt.Errorf("capture code-file needs a standalone invocation with a known launch directory")
				}
				if i.options.ReadCodeFile == nil {
					return fmt.Errorf("capture code-file requires checked program inspection")
				}
				code, err = i.options.ReadCodeFile(invocation)
				if err != nil {
					return err
				}
				i.fileBytes += len(code)
				if i.fileBytes > MaxShellInputBytes {
					return &ShellScanLimitError{Limit: "code-file bytes", Maximum: MaxShellInputBytes}
				}
				i.remaining += 8 * len(code)
			}
			if code == "" || len(code) > MaxShellInputBytes {
				return fmt.Errorf("capture program must be nonempty and at most 1 MiB")
			}
			if invocation.Language == "shell" {
				return i.shell(code, true, depth+1)
			}
			if err := i.charge(len(code), depth+1); err != nil {
				return err
			}
			for _, embedded := range ExtractEmbeddedCommands(code, invocation.Language) {
				if embedded.argv == nil {
					if err := i.shell(embedded.text, true, depth+1); err != nil {
						return err
					}
					continue
				}
				// Direct argv stays data when scanned, but an actual nested capy
				// or shell invocation still needs its program checked.
				if err := i.shell(joinArgs(embedded.argv), true, depth+1); err != nil {
					return err
				}
				i.policy = append(i.policy, embedded.text)
			}
			return nil
		}
		if !captured && routineCatalogue(name, words[1:]) && matchesAnyBashPattern(suffix, i.options.NativeAllow, false) == "" {
			i.result.Required = append(i.result.Required, suffix)
		}
		return nil
	}
	return nil
}

func literalValue(word shellWord, values ...string) bool {
	if !word.literal {
		return false
	}
	for _, value := range values {
		if word.value == value {
			return true
		}
	}
	return false
}

func shellAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return false
	}
	for n, ch := range name {
		if !(ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || n > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func envCommand(words []shellWord) ([]shellWord, bool) {
	changesDirectory := false
	for len(words) > 0 && words[0].literal {
		switch {
		case words[0].value == "--":
			return words[1:], changesDirectory
		case shellAssignment(words[0].value), literalValue(words[0], "-i", "--ignore-environment"):
			words = words[1:]
		case literalValue(words[0], "-u", "--unset", "-C", "--chdir"):
			if len(words) < 2 || !words[1].literal {
				return nil, changesDirectory
			}
			changesDirectory = changesDirectory || literalValue(words[0], "-C", "--chdir")
			words = words[2:]
		case strings.HasPrefix(words[0].value, "--unset="), strings.HasPrefix(words[0].value, "--chdir="):
			changesDirectory = changesDirectory || strings.HasPrefix(words[0].value, "--chdir=")
			words = words[1:]
		case strings.HasPrefix(words[0].value, "-"):
			return nil, changesDirectory // In particular, env -S requires another interpreter.
		default:
			return words, changesDirectory
		}
	}
	return nil, changesDirectory
}

func literalShellScript(words []shellWord) (string, bool) {
	args := make([]string, 0, len(words))
	for _, word := range words {
		if !word.literal {
			break
		}
		args = append(args, word.value)
	}
	return shellArgvScript(args)
}

func routineCatalogue(name string, args []shellWord) bool {
	if len(args) == 1 && literalValue(args[0], "--help", "-h", "--version", "-V", "help", "version") {
		return false
	}
	if len(args) == 1 && literalValue(args[0], "-v") && (name == "mvn" || name == "mvnw" || name == "gradle" || name == "gradlew" || name == "make" || name == "gmake") {
		return false
	}
	// Only known option arities are peeled before a subcommand. Never mistake
	// an option value or a dynamic subcommand for a catalogue verb.
	for len(args) > 0 && args[0].literal && strings.HasPrefix(args[0].value, "-") {
		flag := args[0].value
		takesValue := name == "go" && flag == "-C" || (name == "npm" || name == "pnpm" || name == "yarn") && (flag == "--prefix" || flag == "--cwd" || flag == "--dir" || flag == "-C")
		if takesValue {
			if len(args) < 2 || !args[1].literal {
				return false
			}
			args = args[2:]
			continue
		}
		if (name == "pip" || name == "pip3") && (flag == "--disable-pip-version-check" || flag == "--no-input" || flag == "--isolated") {
			args = args[1:]
			continue
		}
		break
	}
	verb := func(values ...string) bool { return len(args) > 0 && literalValue(args[0], values...) }
	switch name {
	case "go":
		return verb("test", "build")
	case "cargo":
		if len(args) > 0 && args[0].literal && strings.HasPrefix(args[0].value, "+") {
			args = args[1:]
		}
		return verb("test", "build", "check")
	case "pytest", "pytest-3":
		return true
	case "pip", "pip3":
		return verb("install")
	case "npm", "pnpm", "yarn":
		return verb("install", "ci", "add", "test", "build", "run") || name == "yarn" && len(args) == 0
	case "make", "gmake", "gradle", "gradlew", "mvn", "mvnw", "sbt":
		return true
	case "mise":
		return verb("run")
	}
	if name == "python" || name == "python3" || pythonVersionName(name) {
		for len(args) > 0 && literalValue(args[0], "-I", "-B", "-E", "-s", "-S", "-u") {
			args = args[1:]
		}
		if len(args) >= 2 && literalValue(args[0], "-m") {
			if literalValue(args[1], "pytest") {
				return !(len(args) == 3 && literalValue(args[2], "--help", "-h", "--version"))
			}
			return literalValue(args[1], "pip") && routineCatalogue("pip", args[2:])
		}
	}
	return false
}

func pythonVersionName(name string) bool {
	if !strings.HasPrefix(name, "python3.") || len(name) == len("python3.") {
		return false
	}
	for _, ch := range strings.TrimPrefix(name, "python3.") {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
