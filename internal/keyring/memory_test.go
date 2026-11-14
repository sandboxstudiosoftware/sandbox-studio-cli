package keyring

import (
	"errors"
	"testing"
)

func TestMemoryRoundTrip(t *testing.T) {
	m := NewMemory()

	if _, err := m.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing): want ErrNotFound, got %v", err)
	}

	if err := m.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := m.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "v" {
		t.Fatalf("Get = %q, want %q", got, "v")
	}

	// Overwrite.
	if err := m.Set("k", "v2"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if got, _ := m.Get("k"); got != "v2" {
		t.Fatalf("after overwrite Get = %q, want %q", got, "v2")
	}
}

func TestMemoryDelete(t *testing.T) {
	m := NewMemory()
	_ = m.Set("k", "v")

	if err := m.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
	if err := m.Delete("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
}

// Memory must satisfy the Keyring interface.
var _ Keyring = (*Memory)(nil)
