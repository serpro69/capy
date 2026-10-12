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
const codexLaunchVersion = "capy-codex-launch-v1"

// Shared by the wrapper and bootstrap. Discovery never executes a project file.
const capyBinarySearch = `p=""
for candidate in "$(command -v capy 2>/dev/null || true)" "$HOME/.local/bin/capy" "/opt/homebrew/bin/capy" "/usr/local/bin/capy" "$HOME/go/bin/capy" "capy"; do
  if [ -n "$candidate" ] && [ -f "$candidate" ] && [ -x "$candidate" ]; then
    p="$candidate"
    break
  fi
done
`

// Backgrounding with an explicit stdin preserves event/MCP input while allowing
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
` + capyBinarySearch + capySuperviseShell + `if [ -z "$p" ]; then
  echo "capy: launcher binary not found; install/upgrade capy from an external terminal, then rerun capy setup --platform codex" >&2
  status=127
else
  capy_run "$p" codex-launch "$1" "$2" "$3"
  status=$?
fi
if [ "$status" -ne 0 ]; then
  echo "capy: Codex launch failed; repair the reported installation or install/upgrade capy, then rerun capy setup --platform codex from the intended project" >&2
  if [ "$2" = "hook" ] && [ "$3" = "pretooluse" ]; then exit 2; fi
fi
exit "$status"
`

func codexLaunchArgs(id, mode, event string) []string {
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
	if len(owners) == 1 {
		return owners[0].ID, nil
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
	owners, err := InspectCodexOwners(projectDir)
	if err != nil {
		return "", nil, err
	}
	for _, owner := range owners {
		if owner.ProjectDir != projectDir {
			return "", nil, fmt.Errorf("overlapping Codex installations: %s and %s; consolidate to one owner or use a non-overlapping checkout", projectDir, owner.ProjectDir)
		}
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
