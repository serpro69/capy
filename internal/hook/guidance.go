package hook

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/serpro69/capy/internal/adapter"
)

// sessionIDComponent preserves short ASCII IDs for compatibility when embedded
// in state filenames. Other nonempty IDs use a stable digest across hook
// processes; an empty ID continues to mean that state must not be persisted.
func sessionIDComponent(sessionID string) string {
	if len(sessionID) <= 128 {
		safe := true
		for i := 0; i < len(sessionID); i++ {
			c := sessionID[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
				c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				safe = false
				break
			}
		}
		if safe {
			return sessionID
		}
	}
	return fmt.Sprintf("sha256-%x", sha256.Sum256([]byte(sessionID)))
}

const maxLegacyGuidanceBytes = 4 * 1024

// guidanceOnce atomically claims one advisory per type/session across hook
// processes. A killed winner may lose its optional advisory after claiming it;
// invalid or unavailable state instead falls back to nonpersistent guidance.
func guidanceOnce(guidanceType, content string, a adapter.HookAdapter, projectDir, sessionID string) ([]byte, error) {
	// Format before claiming: serialization failure must not consume guidance.
	output, err := a.FormatAllow(content)
	if err != nil || len(output) == 0 {
		return output, err
	}
	if projectDir == "" || sessionID == "" {
		return output, nil
	}
	claimed, err := claimGuidance(projectDir, sessionID, guidanceType)
	if err != nil {
		// Do not log the OS error: paths, session IDs and state bytes can contain
		// caller data. A state failure never changes the permission decision.
		slog.Warn("could not claim session guidance; showing advisory without persistence")
		return output, nil
	}
	if !claimed {
		return nil, nil
	}
	return output, nil
}

func validGuidanceType(guidanceType string) bool {
	switch guidanceType {
	case "bash", "read", "grep":
		return true
	}
	return false
}

func claimGuidance(projectDir, sessionID, guidanceType string) (bool, error) {
	if !validGuidanceType(guidanceType) {
		return false, errors.New("unsupported guidance type")
	}
	dir := filepath.Join(projectDir, ".capy")
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("create guidance directory: %w", err)
	}
	if err := validateGuidanceDir(dir); err != nil {
		return false, err
	}
	base := filepath.Join(dir, "guidance-"+sessionIDComponent(sessionID))
	shown, err := readGuidanceState(base + ".json")
	if err != nil {
		return false, err
	}
	if _, exists := shown[guidanceType]; exists {
		return false, nil
	}
	path := base + "-" + guidanceType + ".once"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		// Exclusive creation publishes the entire zero-byte claim. No write or
		// rename is needed, so contenders cannot observe a partial payload.
		if err := f.Close(); err != nil {
			return false, fmt.Errorf("close guidance claim: %w", err)
		}
		return true, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return false, fmt.Errorf("create guidance claim: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect guidance claim: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return false, errors.New("guidance claim must be an empty regular file")
	}
	return false, nil
}

func validateGuidanceDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect guidance directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("guidance directory must not be a symlink or other file")
	}
	return nil
}

// readGuidanceState recognizes older boolean-map state without modifying it.
// Missing state is normal. All other read/validation errors prevent a new claim.
func readGuidanceState(path string) (map[string]*bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open legacy guidance: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect legacy guidance: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxLegacyGuidanceBytes {
		return nil, errors.New("legacy guidance must be a regular file of at most 4096 bytes")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLegacyGuidanceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read legacy guidance: %w", err)
	}
	if len(data) > maxLegacyGuidanceBytes {
		return nil, errors.New("legacy guidance exceeded byte limit")
	}
	var state map[string]*bool
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode legacy guidance: %w", err)
	}
	if state == nil {
		return nil, errors.New("legacy guidance must be a boolean-map object")
	}
	for _, value := range state {
		if value == nil {
			return nil, errors.New("legacy guidance values must be boolean")
		}
	}
	return state, nil
}

// ResetGuidanceThrottle is a no-op kept for test compatibility.
// Tests should use ResetGuidanceFile instead.
func ResetGuidanceThrottle() {}

// ResetGuidanceFile removes only this session's legacy file and three known
// markers. This is a quiescent test helper, not a concurrent production reset.
func ResetGuidanceFile(projectDir, sessionID string) {
	if projectDir == "" || sessionID == "" {
		return
	}
	dir := filepath.Join(projectDir, ".capy")
	if validateGuidanceDir(dir) != nil {
		return
	}
	base := filepath.Join(dir, "guidance-"+sessionIDComponent(sessionID))
	for _, suffix := range []string{".json", "-bash.once", "-read.once", "-grep.once"} {
		os.Remove(base + suffix)
	}
}
