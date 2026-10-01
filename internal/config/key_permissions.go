package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (c *Config) storeKeyFileSource(projectDir string) KeySource {
	path := c.Store.KeyFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.DBProjectDir(projectDir), path)
	}
	return KeySource{Kind: KeySourceFile, Path: path}
}

func checkKeyFileMode(mode os.FileMode) error {
	if (mode.Perm() == 0o400 || mode.Perm() == 0o600) && mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 {
		return nil
	}
	return fmt.Errorf("unsafe key file permissions %s (%04o); require 0400 or 0600 with no special bits; run capy setup for this project or chmod 600 on the key file", mode, mode.Perm())
}

// CheckFilePermissions checks the current regular-file target of an explicit
// key source without reading its contents or changing a captured credential.
// Other credential sources do not have a key-file permission policy.
func (s KeySource) CheckFilePermissions() error {
	if s.Kind != KeySourceFile {
		return nil
	}
	info, err := os.Stat(s.Path)
	if err != nil {
		return fmt.Errorf("%s: inspecting key file permissions: %w", s, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: credential file must be a regular file", s)
	}
	if err := checkKeyFileMode(info.Mode()); err != nil {
		return fmt.Errorf("%s: %w", s, err)
	}
	return nil
}

// EnsureStoreKeyFilePermissions repairs an existing configured key file to
// 0600, preserving already-safe 0400/0600 files. Like resolution it follows
// symlinks; relative paths belong to the database owner. Setup neither creates
// missing credentials nor reads their contents, including dotenv credentials.
// Path-based repair assumes a stable path in trusted directories during setup;
// the final metadata check does not protect against concurrent replacement.
func (c *Config) EnsureStoreKeyFilePermissions(projectDir string) error {
	if c.Store.KeyFile == "" {
		return nil
	}
	source := c.storeKeyFileSource(projectDir)
	info, err := os.Stat(source.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: inspecting key file permissions: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: credential file must be a regular file", source)
	}
	if checkKeyFileMode(info.Mode()) == nil {
		return nil
	}
	if err := os.Chmod(source.Path, 0o600); err != nil {
		return fmt.Errorf("%s: setting key file permissions to 0600: %w", source, err)
	}
	return source.CheckFilePermissions()
}
