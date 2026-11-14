// Package keyring provides a tiny cross-platform secret-storage abstraction over
// the OS keychain (macOS Keychain, Windows Credential Manager, Linux libsecret).
//
// It is defined as an interface so tests can substitute an in-memory fake and so
// we never couple command code directly to a concrete backend.
package keyring

import "github.com/zalando/go-keyring"

// Service is the keyring service name under which all secrets are grouped.
const Service = "sandbox-studio-cli"

// Keyring stores and retrieves named secrets for the current user.
type Keyring interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// ErrNotFound is returned when a key does not exist in the store.
var ErrNotFound = keyring.ErrNotFound

// OS is the default implementation backed by the operating-system keychain.
type OS struct{}

// New returns the OS-backed keyring.
func New() Keyring { return OS{} }

func (OS) Get(key string) (string, error) { return keyring.Get(Service, key) }

func (OS) Set(key, value string) error { return keyring.Set(Service, key, value) }

func (OS) Delete(key string) error { return keyring.Delete(Service, key) }
