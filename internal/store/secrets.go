package store

import (
	"errors"
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
	primaryErr := f.primary.Delete(service, user)
	f.mu.Lock()
	delete(f.useMem, key(service, user))
	f.mu.Unlock()
	if primaryErr != nil && !errors.Is(primaryErr, keyring.ErrNotFound) {
		return primaryErr
	}
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

// ReadForExport preserves access errors instead of silently treating them as an
// empty password; an explicit export must not claim to include unread secrets.
func (f *FallbackStore) ReadForExport(service, user string) (string, error) {
	k := key(service, user)
	f.mu.Lock()
	temporary := f.useMem[k]
	f.mu.Unlock()
	if temporary {
		return f.fallback.Get(service, user)
	}
	// An inactive fallback may contain an older password. Never export it when
	// the current primary entry cannot be read.
	return f.primary.Get(service, user)
}

func (f *FallbackStore) IsTemporary(service, user string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.useMem[key(service, user)]
}
func (m *MemorySecrets) IsTemporary(service, user string) bool { return true }

// PasswordForExport is intentionally separate from ordinary connection listing.
func (s *Store) PasswordForExport(id string) (string, error) {
	if s.keyring == nil {
		return "", nil
	}
	service, user := s.secretKey(id)
	var password string
	var err error
	if strict, ok := s.keyring.(interface {
		ReadForExport(string, string) (string, error)
	}); ok {
		password, err = strict.ReadForExport(service, user)
	} else {
		password, err = s.keyring.Get(service, user)
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return password, err
}

func isMissingSecret(err error) bool { return errors.Is(err, keyring.ErrNotFound) }
