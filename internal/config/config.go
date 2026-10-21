// Package config stores the ctlog API key between runs, in
// $XDG_CONFIG_HOME/ctlog/config.json (default ~/.config/ctlog/config.json,
// %APPDATA%\ctlog\config.json on Windows) with 0600 permissions.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvAPIKey overrides the stored key, for CI and other non-interactive use.
const EnvAPIKey = "SSLBOARD_API_KEY"

// Config is the on-disk file. Email is informational, for `ctlog status`
// when the API is unreachable.
type Config struct {
	APIKey string `json:"api_key,omitempty"`
	Email  string `json:"email,omitempty"`
}

// Source says where a key came from.
type Source string

const (
	SourceNone Source = ""
	SourceEnv  Source = "env"
	SourceFile Source = "file"
)

// Path returns the config file location, resolved only from getenv so tests
// never touch the real home directory.
func Path(getenv func(string) string) (string, error) {
	if dir := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "ctlog", "config.json"), nil
	}
	if runtime.GOOS == "windows" {
		if dir := strings.TrimSpace(getenv("APPDATA")); dir != "" {
			return filepath.Join(dir, "ctlog", "config.json"), nil
		}
	}
	if home := strings.TrimSpace(getenv("HOME")); home != "" {
		return filepath.Join(home, ".config", "ctlog", "config.json"), nil
	}
	return "", errors.New("cannot find a config directory (HOME is not set)")
}

// Load reads the config file. A missing file is an empty config, not an error.
func Load(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return cfg, nil
}

// Save writes cfg atomically with owner-only permissions.
func Save(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Remove deletes the config file. A missing file is not an error.
func Remove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// ResolveKey returns the API key to use: SSLBOARD_API_KEY first, then the
// config file. A config file that cannot be read is reported, since silently
// falling back to anonymous access would be confusing.
func ResolveKey(getenv func(string) string) (string, Source, error) {
	if key := strings.TrimSpace(getenv(EnvAPIKey)); key != "" {
		return key, SourceEnv, nil
	}
	path, err := Path(getenv)
	if err != nil {
		return "", SourceNone, nil
	}
	cfg, err := Load(path)
	if err != nil {
		return "", SourceNone, err
	}
	if cfg.APIKey == "" {
		return "", SourceNone, nil
	}
	return cfg.APIKey, SourceFile, nil
}
