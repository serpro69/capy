package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const machineIDEnv = "CAPY_MACHINE_ID"

var (
	machineIDOnce sync.Once
	machineIDVal  string
)

// MachineID returns a stable identifier for the current machine, resolved once
// per process and cached. Each machine tags its imported sessions with this ID
// so the cross-machine merge can warn before overwriting unarchived local
// sessions. Resolution order:
//
//  1. CAPY_MACHINE_ID, if non-empty (Docker/CI)
//  2. the machine-id file, if it exists and is non-empty
//  3. a freshly generated UUIDv4, written atomically to the file
//
// If the file cannot be persisted (e.g. a read-only home), it falls back to a
// deterministic ID derived from hostname + username, logging a warning.
func MachineID() string {
	machineIDOnce.Do(func() {
		machineIDVal = resolveMachineID()
	})
	return machineIDVal
}

func resolveMachineID() string {
	if id := strings.TrimSpace(os.Getenv(machineIDEnv)); id != "" {
		return id
	}

	path, err := machineIDPath()
	if err != nil {
		slog.Warn("could not resolve machine-id path; using a derived fallback", "error", err)
		return derivedMachineID()
	}

	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}

	id := uuid.NewString()
	if err := writeMachineIDFile(path, id); err != nil {
		slog.Warn("could not persist machine-id; using a derived fallback", "path", path, "error", err)
		return derivedMachineID()
	}
	return id
}

// machineIDPath returns the machine-id file path: $XDG_CONFIG_HOME/capy/machine-id,
// defaulting to ~/.config/capy/machine-id. This is config (machine identity),
// not data, so it lives under XDG_CONFIG_HOME rather than XDG_DATA_HOME.
func machineIDPath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "capy", "machine-id"), nil
}

// writeMachineIDFile writes id to path atomically (temp file + rename) so a
// crash mid-write never leaves a partially-written identity file.
func writeMachineIDFile(path, id string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "machine-id-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	return nil
}

// derivedMachineID builds a deterministic fallback identifier from the hostname
// and username, hashed so the raw values are not stored verbatim. Used only
// when the machine-id file cannot be persisted.
func derivedMachineID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	name := "unknown-user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	sum := sha256.Sum256([]byte(host + "\x00" + name))
	return "derived-" + hex.EncodeToString(sum[:16])
}
