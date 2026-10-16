package platform

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const codexOwnerRelPath = ".codex/capy-owner"
const codexOwnerPrefix = "capy-codex-owner-v1:"

// CodexLaunchProtocol identifies the only supported installed launch grammar.
const CodexLaunchProtocol = "capy-codex-launch-v2"
const codexLaunchVersion = CodexLaunchProtocol

// Claude's portable wrapper retains its established fallback search.
const capyBinarySearch = `p=""
for candidate in "$(command -v capy 2>/dev/null || true)" "$HOME/.local/bin/capy" "/opt/homebrew/bin/capy" "/usr/local/bin/capy" "$HOME/go/bin/capy" "capy"; do
  if [ -n "$candidate" ] && [ -f "$candidate" ] && [ -x "$candidate" ]; then
    p="$candidate"
    break
  fi
done
`

// Backgrounding with an explicit stdin preserves hook input while allowing
// bash to forward signals immediately instead of deferring traps during wait.
const capySuperviseShell = `capy_run() {
  "$@" <&0 &
  capy_child=$!
  trap 'kill -TERM "$capy_child" 2>/dev/null; wait "$capy_child"; exit 143' TERM
  trap 'kill -INT "$capy_child" 2>/dev/null; wait "$capy_child"; exit 130' INT
  trap 'kill -HUP "$capy_child" 2>/dev/null; wait "$capy_child"; exit 129' HUP
  wait "$capy_child"
  capy_status=$?
  trap - TERM INT HUP
  return "$capy_status"
}
`

const codexBootstrap = `set -uo pipefail
` + capySuperviseShell + `capy_run command capy codex-launch "$0" "$1" "$2" "$3"
status=$?
if [ "$status" -ne 0 ]; then
  echo "capy: Codex launch failed; install/upgrade capy on PATH or repair configuration from an external terminal, then rerun capy setup --platform codex and restart Codex" >&2
  if [ "$2" = "hook" ] && [ "$3" = "pretooluse" ]; then exit 2; fi
fi
exit "$status"
`

func codexLaunchArgs(id, mode, event string) []string {
	if mode == "serve" {
		return []string{"codex-launch", codexLaunchVersion, id, mode, event}
	}
	return []string{"-c", codexBootstrap, codexLaunchVersion, id, mode, event}
}

func codexHookCommand(id, event string) string {
	var b strings.Builder
	b.WriteString("bash")
	for _, arg := range codexLaunchArgs(id, "hook", event) {
		b.WriteString(" '")
		b.WriteString(shellEscapePath(arg))
		b.WriteByte('\'')
	}
	return b.String()
}

func codexOwnerContent(id string) string { return codexOwnerPrefix + id + "\n" }

func validCodexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func parseCodexOwner(data []byte) (string, error) {
	id := strings.TrimSuffix(strings.TrimPrefix(string(data), codexOwnerPrefix), "\n")
	if !validCodexID(id) || string(data) != codexOwnerContent(id) {
		return "", errors.New("invalid owner marker; restore .codex/capy-owner from committed configuration")
	}
	return id, nil
}

// ensureCodexOwner inspects before writing anything, including ancestors beyond
// Git boundaries. Publish a complete marker using an exclusive hard link: unlike
// O_EXCL followed by Write, a racing reader cannot observe an empty marker.
func ensureCodexOwner(projectDir string) (string, error) {
	projectDir, owners, err := inspectCodexSetup(projectDir)
	if err != nil {
		return "", err
	}
	for _, owner := range owners {
		if owner.ProjectDir == projectDir {
			return owner.ID, nil
		}
	}
	return createCodexOwner(projectDir)
}

func inspectCodexSetup(projectDir string) (string, []CodexOwner, error) {
	projectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return "", nil, err
	}
	projectDir, err = filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", nil, err
	}
	owners, err := inspectCodexOwners(projectDir, projectDir)
	if err != nil {
		return "", nil, err
	}
	return projectDir, owners, nil
}

func createCodexOwner(projectDir string) (string, error) {
	dir := filepath.Join(projectDir, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate Codex owner: %w", err)
	}
	f, err := os.CreateTemp(dir, ".capy-owner-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(codexOwnerContent(hex.EncodeToString(random[:])))
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return "", err
	}
	path := filepath.Join(projectDir, codexOwnerRelPath)
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create Codex owner: %w", err)
	}
	data, _, err := readCodexFile(path, 80)
	if err != nil {
		return "", err
	}
	return parseCodexOwner(data)
}
