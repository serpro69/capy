package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	maxKeyFileBytes = 4096
	maxDotenvBytes  = 1 << 20
)

// KeySource describes a selected credential without retaining its secret.
// Path is the resolved source path, preserving filesystem symlinks.
type KeySource struct {
	Kind string
	Path string
}

const (
	KeySourceEnvironment = "environment"
	KeySourceFile        = "key_file"
	KeySourceDotenv      = "dotenv"
)

// String returns a safe hint for diagnostics and store authentication errors.
func (s KeySource) String() string {
	switch s.Kind {
	case KeySourceEnvironment:
		return "CAPY_DB_KEY"
	case KeySourceFile:
		return fmt.Sprintf("store.key_file %q", s.Path)
	case KeySourceDotenv:
		return fmt.Sprintf("CAPY_DB_KEY in dotenv %q", s.Path)
	default:
		return "selected knowledge credential"
	}
}

// ResolveStoreKey selects a configured literal key file, an owner/main-worktree
// dotenv declaration, or the inherited CAPY_DB_KEY. It does not authenticate the key,
// touch the database, or change the process environment. Errors retain safe
// source metadata but never return a partial key or try a lower-priority source.
func (c *Config) ResolveStoreKey(projectDir string) (string, KeySource, error) {
	if c.Store.KeyFile != "" {
		source := c.storeKeyFileSource(projectDir)
		data, err := readCredentialFile(source.Path, maxKeyFileBytes, checkKeyFileMode)
		if err != nil {
			return "", source, fmt.Errorf("%s: %w", source, err)
		}
		// Remove exactly one text-file line ending; all other bytes are literal.
		if bytes.HasSuffix(data, []byte("\n")) {
			data = bytes.TrimSuffix(data, []byte("\n"))
			data = bytes.TrimSuffix(data, []byte("\r"))
		}
		if len(data) == 0 {
			return "", source, fmt.Errorf("%s: passphrase is empty", source)
		}
		if bytes.ContainsAny(data, "\x00\r\n") {
			return "", source, fmt.Errorf("%s: passphrase contains NUL or a remaining line ending", source)
		}
		return string(data), source, nil
	}

	owner := c.DBProjectDir(projectDir)
	key, source, found, err := resolveDotenvKey(owner)
	if err != nil || found {
		return key, source, err
	}
	if main := MainWorktreeDir(projectDir); main != owner {
		key, source, found, err = resolveDotenvKey(main)
		if err != nil || found {
			return key, source, err
		}
	}

	source = KeySource{Kind: KeySourceEnvironment}
	key = os.Getenv("CAPY_DB_KEY")
	if key == "" {
		return "", source, fmt.Errorf("CAPY_DB_KEY environment variable is required when no store.key_file or project dotenv credential is selected (see: capy encrypt --help)")
	}
	return key, source, nil
}

func resolveDotenvKey(dir string) (string, KeySource, bool, error) {
	path := filepath.Join(dir, ".env")
	source := KeySource{Kind: KeySourceDotenv, Path: path}
	data, err := readCredentialFile(path, maxDotenvBytes, nil)
	if errors.Is(err, os.ErrNotExist) {
		return "", source, false, nil
	}
	if err != nil {
		return "", source, false, fmt.Errorf("%s: %w; configure store.key_file with a literal passphrase file", source, err)
	}
	key, found, err := parseDotenvKey(data, path)
	return key, source, found, err
}

// readCredentialFile admits only regular targets, following symlinks. Inspect
// before opening so a pre-existing FIFO cannot block, then check the descriptor
// again. Bound the actual read independently of the reported file size.
// Missing files remain recognizable with errors.Is(err, fs.ErrNotExist), for
// optional credential sources to distinguish absence from other access errors.
func readCredentialFile(path string, limit int64, checkMode func(os.FileMode) error) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspecting credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credential file must be a regular file")
	}
	if checkMode != nil {
		if err := checkMode(info.Mode()); err != nil {
			return nil, err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening credential file: %w", err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting opened credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("opened credential file must be a regular file")
	}
	if checkMode != nil {
		if err := checkMode(info.Mode()); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading credential file: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("credential file exceeds %d-byte raw input limit", limit)
	}
	return data, nil
}
