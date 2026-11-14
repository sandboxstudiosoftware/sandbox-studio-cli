package config

import (
	"os"
	"path/filepath"
	"testing"
)

// useTempDir points the config package at a throwaway directory for the duration
// of a test, so tests never read or write the real user config.
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	return dir
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	useTempDir(t)
	// Ensure no env URL bleeds in from the host.
	t.Setenv(EnvURL, "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Contexts == nil {
		t.Fatal("Contexts map should be initialised, not nil")
	}
	if len(c.Contexts) != 0 {
		t.Fatalf("expected no contexts, got %d", len(c.Contexts))
	}
}

func TestSaveThenLoadRoundTrip(t *testing.T) {
	dir := useTempDir(t)

	c := &Config{
		CurrentContext: "default",
		Contexts: map[string]Context{
			"default": {URL: "https://example.com", DefaultLease: "L1"},
		},
		Output: "json",
	}
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File exists with 0600 perms.
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config perms = %o, want 600", perm)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CurrentContext != "default" || got.Output != "json" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Contexts["default"].URL != "https://example.com" || got.Contexts["default"].DefaultLease != "L1" {
		t.Errorf("context round-trip mismatch: %+v", got.Contexts["default"])
	}
}

func TestCurrentSelectsContext(t *testing.T) {
	useTempDir(t)
	t.Setenv(EnvURL, "")

	c := &Config{
		CurrentContext: "prod",
		Contexts: map[string]Context{
			"prod": {URL: "https://prod.example.com"},
			"dev":  {URL: "https://dev.example.com"},
		},
	}

	cur, err := c.Current("")
	if err != nil {
		t.Fatalf("Current(default): %v", err)
	}
	if cur.URL != "https://prod.example.com" {
		t.Errorf("default context URL = %q", cur.URL)
	}

	named, err := c.Current("dev")
	if err != nil {
		t.Fatalf("Current(dev): %v", err)
	}
	if named.URL != "https://dev.example.com" {
		t.Errorf("named context URL = %q", named.URL)
	}
}

func TestCurrentEnvURLOverrides(t *testing.T) {
	useTempDir(t)
	t.Setenv(EnvURL, "https://override.example.com")

	c := &Config{
		CurrentContext: "default",
		Contexts:       map[string]Context{"default": {URL: "https://config.example.com"}},
	}
	cur, err := c.Current("")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.URL != "https://override.example.com" {
		t.Errorf("env override not applied: URL = %q", cur.URL)
	}
}

func TestCurrentNoURLErrors(t *testing.T) {
	useTempDir(t)
	t.Setenv(EnvURL, "")

	c := &Config{Contexts: map[string]Context{}}
	if _, err := c.Current(""); err == nil {
		t.Fatal("expected an error when no URL is configured")
	}
}
