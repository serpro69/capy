package security

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutineCatalogue(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		count         int
	}{
		{"issue mise chain", "mise exec -- node cli.mjs tools && mise run test", 1},
		{"issue virtualenv install", "python3 -m venv /tmp/pay516-task14-docs && /tmp/pay516-task14-docs/bin/python -m pip install -r requirements.txt", 1},
		{"go test", "go test -tags fts5 ./...", 1},
		{"go build", "go -C project build ./cmd/capy", 1},
		{"cargo verbs", "cargo test && cargo +stable build && cargo check", 3},
		{"pytest", "pytest -q tests && python3.12 -I -m pytest tests", 2},
		{"pip", "pip install a && pip3 --no-input install b && python -m pip install c", 3},
		{"node verbs", "npm ci && pnpm add x && yarn install && npm test && pnpm build && yarn run lint", 6},
		{"node options", "npm --prefix app run test && pnpm -C app test && yarn", 3},
		{"make", "make && gmake -C src all", 2},
		{"jvm", "gradle test && ./gradlew build && mvn verify && ./mvnw test && sbt test", 5},
		{"wrappers", "CI=1 env -i -u TOKEN GOFLAGS='-count=1' command -p -- mise exec go@1 -- go test ./...", 1},
		{"shell wrappers", `bash -lc 'go test ./...; sh -c "make test"'`, 2},
		{"shell option value", `bash -o pipefail -c 'cargo test'`, 1},
		{"quoted executable", `'go' 'test' ./...`, 1},
		{"escaped executable", "g\\o te\\st ./...", 1},
		{"line continuation", "go te\\\nst ./...", 1},
		{"redirections", "2>err go test ./... >out; make test 2>&1", 2},
		{"descriptor close", "2>&- go test; <&- make test", 2},
		{"substitution", `printf '%s' "$(go test ./...)"`, 1},
		{"backtick", "echo `make test`", 1},
		{"process substitution", "cat <(cargo test)", 1},
		{"heredoc expansion", "cat <<EOF\n$(pytest)\nEOF", 1},
		{"array expansion", "a=($(make test))", 1},
		{"quoted mentions", `echo 'go test'; printf '%s' "capy execute --code pytest"`, 0},
		{"quoted heredoc", "cat <<'EOF'\ngo test\n$(make test)\nEOF", 0},
		{"unquoted heredoc literal", "cat <<EOF\ngo test\nEOF", 0},
		{"array data", "a=(go test make test)", 0},
		{"array later routine data", "args=(ignored go test ./...)", 0},
		{"array multiline data", "args=(ignored\ngo test ./...\n)", 0},
		{"array later expansion", "args=(ignored $(go test ./...))", 1},
		{"quoted assignment executable", `'CI=1' go test`, 0},
		{"quoted assignment in env", `env 'CI=1' go test`, 1},
		{"dynamic program", "$runner test; ${runner} test; ${runner:-go} test", 0},
		{"dynamic verb", `go "$verb"; python -m "$module"; npm run-test`, 0},
		{"aliases and scripts", "gt; ./test.sh; bash test.sh; python test.py", 0},
		{"comment", "echo ok # go test\n# pytest", 0},
		{"probes", "go --version; cargo --help; pytest --help; python -m pytest --version; pip --version; npm --version; make --help; gradle --version; mvn -v; command -v pytest", 0},
		{"data that looks dynamic", `echo '${go test}' '$((make test))'`, 0},
		{"data after shell script", `sh -c 'echo ok' 'go test'`, 0},
		{"unknown env option", "env -S 'go test'", 0},
		{"dynamic shell source", `bash -c "$script"`, 0},
		{"probe plus actual command", "go --help; make test", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := InspectRoutineCommand(tc.command, RoutineOptions{})
			require.NoError(t, err)
			require.Len(t, got.Required, tc.count, "%+v", got)
		})
	}
}

func TestRoutineExceptionsAndSecurity(t *testing.T) {
	policies := []SecurityPolicy{{SourcePath: "project/settings.json", Deny: []string{"Bash(blocked:*)", "Bash(go test:*)"}, Ask: []string{"Bash(confirm:*)"}}}
	for _, tc := range []struct {
		name, command string
		allow         []string
		count         int
		decision      string
		pattern       string
	}{
		{"exception covers one", "make test; cargo test", []string{"Bash(make:*)"}, 1, "ask", ""},
		{"all elements covered", "make test; cargo test", []string{"Bash(make:*)", "Bash(cargo:*)"}, 0, "ask", ""},
		{"wrapper exception", "CI=1 env command go test ./...", []string{"Bash(go test:*)"}, 0, "deny", "Bash(go test:*)"},
		{"captured sibling", `capy execute --code 'make test'; cargo test`, nil, 1, "ask", ""},
		{"capture plus deny", `capy execute --code 'go test ./...'`, []string{"Bash(*)"}, 0, "deny", "Bash(go test:*)"},
		{"array data is not denied", `args=(ignored go test ./...)`, nil, 0, "ask", ""},
		{"captured array data is not denied", `capy execute --code 'args=(ignored go test ./...)'`, nil, 0, "ask", ""},
		{"capture ask", `capy execute --code 'confirm this'`, nil, 0, "ask", "Bash(confirm:*)"},
		{"deny follows ask", `capy execute --code 'confirm this'; capy execute --code 'blocked that'`, nil, 0, "deny", "Bash(blocked:*)"},
		{"native deny follows capture ask", `capy execute --code 'confirm this'; blocked that`, nil, 0, "deny", "Bash(blocked:*)"},
		{"underlying wrapper", `capy execute --code 'env X=1 bash -c "blocked arg"'`, nil, 0, "deny", "Bash(blocked:*)"},
		{"quoted underlying executable", `capy execute --code '"blocked" arg'`, nil, 0, "deny", "Bash(blocked:*)"},
		{"python embedded", `capy execute --language python --code 'import os; os.system("blocked arg")'`, nil, 0, "deny", "Bash(blocked:*)"},
		{"python nested capture argv", `capy execute --language python --code 'subprocess.run(["capy", "execute", "--code", "blocked arg"])'`, nil, 0, "deny", "Bash(blocked:*)"},
		{"python argv data", `capy execute --language python --code 'subprocess.run(["echo", "hello; blocked arg"])'`, nil, 0, "ask", ""},
		{"capture outer expansion", `capy execute --code 'echo ok' --intent "$(go test ./...)"`, nil, 0, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InspectRoutineCommand(tc.command, RoutineOptions{NativeAllow: tc.allow, Policies: policies})
			if tc.decision == "" {
				require.Error(t, err) // Dynamic capture arguments are never certified.
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Required, tc.count)
			require.Equal(t, tc.decision, got.Security.Decision)
			require.Equal(t, tc.pattern, got.Security.MatchedPattern)
			if tc.pattern != "" {
				require.Equal(t, "project/settings.json", got.Security.SourcePath)
			}
		})
	}
}

func TestCaptureCLIRecognition(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		captures      int
		invalid       bool
	}{
		{"ordinary", `capy execute --code 'go test ./...'`, 1, false},
		{"equals and selectors", `/usr/bin/capy --project-dir '/repo x' execute --language=shell --code='make test' --cwd src --timeout-ms 0x100 --intent failures --json=false`, 1, false},
		{"escaped code", `capy execute --code "printf '%s' \"hello\"" --json`, 1, false},
		{"literal dollar code", `capy execute --code 'printf "%s" "$HOME"'`, 1, false},
		{"repeated final code", `capy execute --code blocked --code 'echo safe' --language shell --language python`, 1, false},
		{"terminator", `capy execute --code 'echo ok' --`, 1, false},
		{"wrapped", `env CI=1 command capy execute --code 'pytest'`, 1, false},
		{"help", `capy execute --help`, 0, false},
		{"help false", `capy execute --help=false --code 'pytest'`, 1, false},
		{"lookalike", `capy-other execute --code 'go test'`, 0, false},
		{"wrong subcommand", `capy execution --code 'go test'`, 0, false},
		{"mention", `echo capy execute --code 'go test'`, 0, false},
		{"no code", `capy execute --json`, 0, true},
		{"two selectors", `capy execute --code 'echo ok' --code-file a`, 0, true},
		{"unknown flag", `capy execute --code 'go test' --wait-ms 0`, 0, true},
		{"background", `capy execute --code 'go test' --background`, 0, true},
		{"dynamic code", `capy execute --code "$program"`, 0, true},
		{"dynamic language", `capy execute --language "$lang" --code 'go test'`, 0, true},
		{"missing flag value", `capy execute --code`, 0, true},
		{"empty code", `capy execute --code ''`, 0, true},
		{"empty cwd", `capy execute --code 'echo ok' --cwd ''`, 0, true},
		{"invalid bool", `capy execute --code 'go test' --json=yes`, 0, true},
		{"positional", `capy execute --code 'go test' argument`, 0, true},
		{"invalid language", `capy execute --language bash --code 'go test'`, 0, true},
		{"zero timeout", `capy execute --code 'go test' --timeout-ms 0`, 0, true},
		{"overflow timeout", `capy execute --code 'go test' --timeout-ms 9223372036854775807`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InspectRoutineCommand(tc.command, RoutineOptions{})
			if tc.invalid {
				require.Error(t, err)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Empty(t, got.Required)
			require.Len(t, got.Captures, tc.captures)
		})
	}
}

func TestCaptureCodeFileInspection(t *testing.T) {
	command := `capy --project-dir /selected execute --code-file program.sh --cwd src`
	_, err := InspectRoutineCommand(command, RoutineOptions{})
	require.ErrorContains(t, err, "checked program")
	got, err := InspectRoutineCommand(command, RoutineOptions{
		Policies: []SecurityPolicy{{Ask: []string{"Bash(confirm:*)"}}},
		ReadCodeFile: func(inv CaptureInvocation) (string, error) {
			require.Equal(t, "/selected", inv.ProjectDir)
			require.Equal(t, "program.sh", inv.CodeFile)
			require.Equal(t, "src", inv.Cwd)
			return strings.Repeat("# padding\n", 1000) + "confirm arg", nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "Bash(confirm:*)", got.Security.MatchedPattern)
	require.Empty(t, got.Required)
	denied := errors.New("matched Read ask rule from fixture")
	got, err = InspectRoutineCommand(command, RoutineOptions{ReadCodeFile: func(CaptureInvocation) (string, error) { return "", denied }})
	require.ErrorIs(t, err, denied)
	require.Empty(t, got)
	for _, command := range []string{
		`CLAUDE_PROJECT_DIR=/other capy execute --code-file program.sh`,
		`env CLAUDE_PROJECT_DIR=/other capy execute --code-file program.sh`,
		`env -u CLAUDE_PROJECT_DIR capy execute --code-file program.sh`,
		`env -C /other capy execute --code-file program.sh`,
		`env --chdir=/other bash -c 'capy execute --code-file program.sh'`,
		`mise exec -C /other -- capy execute --code-file program.sh`,
		`bash -lc 'capy execute --code-file program.sh'`,
		`cd /other; capy execute --code-file program.sh`,
		`capy execute --cwd /other --code 'capy execute --code-file program.sh'`,
	} {
		t.Run(command, func(t *testing.T) {
			got, err := InspectRoutineCommand(command, RoutineOptions{ReadCodeFile: func(CaptureInvocation) (string, error) {
				t.Fatal("must not inspect a program selected using uncertain launch cwd")
				return "", nil
			}})
			require.ErrorContains(t, err, "known launch directory")
			require.Empty(t, got)
		})
	}
}

func TestRoutineScannerLimits(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"bytes", strings.Repeat("x", MaxShellInputBytes+1)},
		{"elements", strings.Repeat("go test;", MaxShellElements+1)},
		{"words", "echo " + strings.Repeat("x ", MaxShellElements)},
		{"frames", strings.Repeat("( ", MaxShellFrames+1) + ":" + strings.Repeat(")", MaxShellFrames+1)},
		{"wrapper frames", strings.Repeat("command ", MaxShellFrames+1) + "go test"},
		{"unterminated quote", "echo '"},
		{"scanner visits", strings.Repeat("echo $(", 16) + strings.Repeat("x", 4096) + strings.Repeat(")", 16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := InspectRoutineCommand(tc.command, RoutineOptions{})
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
	t.Run("recursive code file", func(t *testing.T) {
		code := `capy execute --code-file same`
		got, err := InspectRoutineCommand(code, RoutineOptions{ReadCodeFile: func(CaptureInvocation) (string, error) { return code, nil }})
		require.ErrorContains(t, err, "known launch directory")
		require.Empty(t, got)
	})
}

func FuzzRoutineInspection(f *testing.F) {
	for _, code := range []string{"go test", `capy execute --code 'make test'`, "cat <<EOF\n$(go test)\nEOF", `echo "${x:-$(pytest)}"`, "2>file make test"} {
		f.Add(code)
	}
	f.Fuzz(func(t *testing.T, code string) {
		if len(code) > MaxShellInputBytes+1 {
			t.Skip()
		}
		got, err := InspectRoutineCommand(code, RoutineOptions{})
		if err != nil {
			require.Empty(t, got)
		}
	})
}
