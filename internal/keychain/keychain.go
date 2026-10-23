// Package keychain stores the API client secret in the OS credential store
// (macOS Keychain, Windows Credential Manager, Linux Secret Service).
package keychain

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// Service is the keychain service name for all ccx items.
const Service = "ccx"

// ErrNotFound means no secret is stored for the account.
var ErrNotFound = errors.New("no client secret in the keychain: run `ccx login`")

// Store reads and writes client secrets.
type Store interface {
	Get(cloudID, clientID string) (string, error)
	Set(cloudID, clientID, secret string) error
	Delete(cloudID, clientID string) error
}

// Account returns the keychain user for a credential: "<cloud_id>/<client_id>".
func Account(cloudID, clientID string) string { return cloudID + "/" + clientID }

// OS is the real OS keychain.
type OS struct{}

// Get returns the stored secret.
func (OS) Get(cloudID, clientID string) (string, error) {
	s, err := keyring.Get(Service, Account(cloudID, clientID))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read keychain: %w", err)
	}
	return s, nil
}

// Set stores or replaces the secret.
func (OS) Set(cloudID, clientID, secret string) error {
	if err := keyring.Set(Service, Account(cloudID, clientID), secret); err != nil {
		return fmt.Errorf("write keychain: %w", err)
	}
	return nil
}

// Delete removes the secret. A missing item is not an error.
func (OS) Delete(cloudID, clientID string) error {
	err := keyring.Delete(Service, Account(cloudID, clientID))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete keychain item: %w", err)
	}
	return nil
}

// Memory is an in-memory Store for tests.
type Memory struct {
	mu    sync.Mutex
	items map[string]string
	// Err, if set, is returned by every call (simulates a locked keychain).
	Err error
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{items: map[string]string{}} }

// Get implements Store.
func (m *Memory) Get(cloudID, clientID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	s, ok := m.items[Account(cloudID, clientID)]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

// Set implements Store.
func (m *Memory) Set(cloudID, clientID, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.items[Account(cloudID, clientID)] = secret
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(cloudID, clientID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	delete(m.items, Account(cloudID, clientID))
	return nil
}

// Len reports how many secrets are stored.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}
