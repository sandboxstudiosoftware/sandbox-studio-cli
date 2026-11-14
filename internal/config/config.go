// Package config handles the non-secret CLI configuration file and the resolution
// of settings from (in priority order) command-line flags, environment variables,
// and the on-disk config.
//
// Secrets (client id/secret, cached tokens) are NEVER stored here — they live in
// the OS keyring (see internal/keyring). This file holds only the API URL, the
// default lease, output preferences, and named contexts.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnvURL, EnvClientID and EnvClientSecret are environment overrides, primarily for
// CI where an interactive keyring is not available.
const (
	EnvURL          = "SANDBOX_STUDIO_URL"
	EnvClientID     = "SANDBOX_STUDIO_CLIENT_ID"
	EnvClientSecret = "SANDBOX_STUDIO_CLIENT_SECRET"
)

// Context is a single named deployment the user can target.
type Context struct {
	URL          string `json:"url"`
	DefaultLease string `json:"defaultLease,omitempty"`
}

// Config is the persisted, non-secret configuration.
type Config struct {
	CurrentContext string             `json:"currentContext"`
	Contexts       map[string]Context `json:"contexts"`
	Output         string             `json:"output,omitempty"` // "table" | "json"
}

// EnvConfigDir overrides the base configuration directory. It is primarily for
// tests (so they do not touch the real user config) but can also be used to point
// the CLI at an alternate location. When unset, the OS default is used.
const EnvConfigDir = "SANDBOX_STUDIO_CONFIG_DIR"

// Dir returns the configuration directory. If SANDBOX_STUDIO_CONFIG_DIR is set it
// is used verbatim; otherwise it honours XDG on Unix and %AppData% on Windows via
// os.UserConfigDir.
func Dir() (string, error) {
	if override := os.Getenv(EnvConfigDir); override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sandbox-studio"), nil
}

// Path returns the full path to config.json.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config file, returning an empty (but initialised) Config if the
// file does not exist yet.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &Config{Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	return &c, nil
}

// Save writes the config file with 0600 permissions, creating the directory if
// needed.
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, "config.json")
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// Current returns the active context, applying the env URL override if set.
// contextName, when non-empty, selects a specific context instead of the current
// one.
func (c *Config) Current(contextName string) (Context, error) {
	name := contextName
	if name == "" {
		name = c.CurrentContext
	}

	ctx := c.Contexts[name]

	// Environment override always wins for the URL — useful in CI.
	if url := os.Getenv(EnvURL); url != "" {
		ctx.URL = url
	}

	if ctx.URL == "" {
		return Context{}, fmt.Errorf("no API URL configured: run `sbs configure` or set %s", EnvURL)
	}
	return ctx, nil
}
