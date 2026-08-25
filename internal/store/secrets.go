package store

import (
	"sync"

	"github.com/zalando/go-keyring"
)

// KeyringStore uses the OS keychain.
type KeyringStore struct{}

func (KeyringStore) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (KeyringStore) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}
func (KeyringStore) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

// MemorySecrets is an in-memory fallback (tests / headless CI).
type MemorySecrets struct {
	mu   sync.Mutex
	data map[string]string
}

func NewMemorySecrets() *MemorySecrets {
	return &MemorySecrets{data: map[string]string{}}
}

func key(service, user string) string { return service + "\x00" + user }

func (m *MemorySecrets) Set(service, user, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key(service, user)] = password
	return nil
}

func (m *MemorySecrets) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key(service, user)]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (m *MemorySecrets) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key(service, user))
	return nil
}

// FallbackStore tries the OS keychain first, falls back to in-memory on failure.
type FallbackStore struct {
	primary  SecretStore
	fallback *MemorySecrets
	mu       sync.Mutex
	useMem   map[string]bool // keys that failed on primary
}

func (f *FallbackStore) Set(service, user, password string) error {
	k := key(service, user)
	if err := f.primary.Set(service, user, password); err != nil {
		f.mu.Lock()
		f.useMem[k] = true
		f.mu.Unlock()
		return f.fallback.Set(service, user, password)
	}
	f.mu.Lock()
	delete(f.useMem, k)
	f.mu.Unlock()
	return nil
}

func (f *FallbackStore) Get(service, user string) (string, error) {
	k := key(service, user)
	f.mu.Lock()
	inMem := f.useMem[k]
	f.mu.Unlock()
	if inMem {
		return f.fallback.Get(service, user)
	}
	pw, err := f.primary.Get(service, user)
	if err != nil {
		return f.fallback.Get(service, user)
	}
	return pw, nil
}

func (f *FallbackStore) Delete(service, user string) error {
	_ = f.fallback.Delete(service, user)
	_ = f.primary.Delete(service, user)
	f.mu.Lock()
	delete(f.useMem, key(service, user))
	f.mu.Unlock()
	return nil
}

// NewSecretStore prefers OS keyring, falls back to memory if unavailable.
func NewSecretStore() SecretStore {
	return &FallbackStore{
		primary:  KeyringStore{},
		fallback: NewMemorySecrets(),
		useMem:   map[string]bool{},
	}
}
