package main

import (
	"strings"
	"testing"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/auth"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/config"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/keyring"
)

// TestCmdConfigureStoresUrlAndSecrets drives `configure` non-interactively by
// supplying the URL and client-id via flags and injecting the secret reader, then
// asserts the config file and keyring were written as designed (URL on disk,
// secrets in the keyring).
func TestCmdConfigureStoresUrlAndSecrets(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	// Clear env overrides so Load/keyring reflect only what configure writes.
	t.Setenv(config.EnvURL, "")
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")

	mem := keyring.NewMemory()
	newKeyring = func() keyring.Keyring { return mem }
	t.Cleanup(func() { newKeyring = keyring.New })

	// Inject a non-interactive secret reader.
	secretReader = func(string) (string, error) { return "the-secret", nil }
	t.Cleanup(func() { secretReader = promptSecret })

	_, _, err := captureStd(func() error {
		flagContext, flagURL, flagOutput, flagDebug = "", "", "", false
		root := newRootCmd()
		root.SetArgs([]string{"configure", "--url", "https://cfg.example.com", "--client-id", "cid"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	// URL persisted to the (non-secret) config file.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cur, err := cfg.Current("")
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if cur.URL != "https://cfg.example.com" {
		t.Errorf("stored URL = %q", cur.URL)
	}

	// Secrets persisted to the keyring, NOT the config file.
	if id, _ := mem.Get(auth.KeyClientID); id != "cid" {
		t.Errorf("client id in keyring = %q", id)
	}
	if sec, _ := mem.Get(auth.KeyClientSecret); sec != "the-secret" {
		t.Errorf("client secret in keyring = %q", sec)
	}
}

func TestCmdConfigureRejectsEmptySecret(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	newKeyring = func() keyring.Keyring { return keyring.NewMemory() }
	t.Cleanup(func() { newKeyring = keyring.New })
	secretReader = func(string) (string, error) { return "", nil }
	t.Cleanup(func() { secretReader = promptSecret })

	_, _, err := captureStd(func() error {
		flagContext, flagURL, flagOutput, flagDebug = "", "", "", false
		root := newRootCmd()
		root.SetArgs([]string{"configure", "--url", "https://x", "--client-id", "cid"})
		return root.Execute()
	})
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected an empty-secret error, got %v", err)
	}
}
