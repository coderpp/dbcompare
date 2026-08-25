package store_test

import (
	"path/filepath"
	"testing"

	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/store"
)

func TestStoreConnectionsAndProfiles(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "data"), store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	saved, err := s.SaveConnection(dto.ConnConfig{
		Name: "local", Host: "127.0.0.1", Port: 3306, User: "root", Password: "secret", Database: "db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Password != "" {
		t.Fatal("password must not remain on returned config")
	}
	full, err := s.GetConnection(saved.ID, true)
	if err != nil || full.Password != "secret" {
		t.Fatalf("keyring hydrate failed: %#v %v", full, err)
	}

	p, err := s.SaveProfile(dto.SyncProfile{
		Name: "p1", SourceID: saved.ID, SourceDatabase: "src_db", TargetID: saved.ID, TargetDatabase: "dst_db",
		Normalize: dto.DefaultNormalizeOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	exp, err := s.ExportProfile(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exp == "" || contains(exp, "secret") {
		t.Fatalf("export leaked or empty: %s", exp)
	}

	_ = s.AppendAudit(dto.AuditEntry{Action: "test", SourceDSN: "u:***@h/db", TargetDSN: "u:***@h/db", SQL: "SELECT 1", OK: true})
	logs, err := s.ListAudit(10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("audit: %#v %v", logs, err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})())
}
