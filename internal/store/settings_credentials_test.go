package store

import (
	"errors"
	"github.com/pp/db-compare/internal/dto"
	"strings"
	"testing"
)

type controlledSecrets struct {
	*MemorySecrets
	writes    int
	failAt    int
	readError error
	reads     int
}

func (s *controlledSecrets) Set(service, user, password string) error {
	s.writes++
	s.MemorySecrets.Set(service, user, password)
	if s.writes == s.failAt {
		return errors.New("synthetic failure")
	}
	return nil
}
func (s *controlledSecrets) Get(service, user string) (string, error) {
	s.reads++
	if s.readError != nil {
		return "", s.readError
	}
	return s.MemorySecrets.Get(service, user)
}
func TestImportCredentialFailureCleansNewEntries(t *testing.T) {
	secrets := &controlledSecrets{MemorySecrets: NewMemorySecrets()}
	st, err := Open(t.TempDir(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	existing, err := st.SaveConnection(dto.ConnConfig{Name: "existing", Host: "old", Port: 3306, User: "u", Password: "preserved"})
	if err != nil {
		t.Fatal(err)
	}
	secrets.writes = 0
	secrets.failAt = 2
	b := dto.SettingsBundle{Version: 2, IncludesPasswords: true, Connections: []dto.SettingsConnection{{ID: "a", Name: "a", Host: "localhost", Port: 3306, User: "u", Password: "first"}, {ID: "b", Name: "b", Host: "localhost", Port: 3306, User: "u", Password: "second"}}}
	if _, err = st.ImportSettings(b); err == nil {
		t.Fatal("expected credential failure")
	}
	cs, _ := st.ListConnections()
	if len(cs) != 1 || cs[0].ID != existing.ID {
		t.Fatal("SQL did not roll back")
	}
	if len(secrets.data) != 1 {
		t.Fatal("new keychain entries were not removed, including partially written failure")
	}
	full, _ := st.GetConnection(existing.ID, true)
	if full.Password != "preserved" {
		t.Fatal("existing keychain changed")
	}
	// Read access errors must be preserved for an explicit export.
	secrets.readError = errors.New("denied")
	if _, err := st.PasswordForExport(existing.ID); err == nil {
		t.Fatal("credential access error hidden")
	}
}
func TestImportedCredentialNotStoredInSQLiteOrResults(t *testing.T) {
	st, err := Open(t.TempDir(), NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	out, err := st.ImportSettings(dto.SettingsBundle{Connections: []dto.SettingsConnection{{ID: "a", Name: "example", Host: "localhost", Port: 3306, User: "u", Password: "synthetic-private-value"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Connections[0].Password != "" || out.PasswordsImported != 1 || len(out.Warnings) != 1 {
		t.Fatal("credential response or temporary storage warning incorrect")
	}
	var values string
	err = st.db.QueryRow("SELECT id||name||host||user||database_name||params FROM connections").Scan(&values)
	if err != nil || strings.Contains(values, "synthetic-private-value") {
		t.Fatal("credential in database")
	}
}

func TestCredentialExportDoesNotUseStaleFallback(t *testing.T) {
	primary := &controlledSecrets{MemorySecrets: NewMemorySecrets(), readError: errors.New("denied")}
	fallback := NewMemorySecrets()
	fallback.Set("svc", "user", "stale")
	secrets := &FallbackStore{primary: primary, fallback: fallback, useMem: map[string]bool{}}
	if _, err := secrets.ReadForExport("svc", "user"); err == nil {
		t.Fatal("exported stale fallback while primary was unreadable")
	}
	secrets.useMem[key("svc", "user")] = true
	password, err := secrets.ReadForExport("svc", "user")
	if err != nil || password != "stale" {
		t.Fatal("explicitly active temporary credential was unavailable")
	}
}
