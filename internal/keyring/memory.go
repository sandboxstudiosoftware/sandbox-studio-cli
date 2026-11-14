// Package keyring — in-memory fake for tests.
package keyring

import "sync"

// Memory is an in-memory Keyring for use in tests.
type Memory struct {
	mu   sync.Mutex
	data map[string]string
}

// NewMemory returns an empty in-memory keyring.
func NewMemory() *Memory { return &Memory{data: map[string]string{}} }

func (m *Memory) Get(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *Memory) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *Memory) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; !ok {
		return ErrNotFound
	}
	delete(m.data, key)
	return nil
}
