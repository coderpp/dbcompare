package store

import (
	"github.com/pp/db-compare/internal/dto"
	"strings"
	"testing"
)

func TestRunPersistenceRedactsSecretsAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	run := &dto.SyncRun{ID: "run-1", StartedAt: "2026-10-15T00:00:00Z", Status: "running", Source: dto.ConnConfig{Password: "source-secret", Params: "custom=hidden"}, Target: dto.ConnConfig{Password: "target-secret"}, Snapshots: []dto.TableSnapshot{{Table: "t", DDL: "CREATE TABLE t(id int)"}}, Outcomes: []dto.StatementOutcome{{Status: "pending"}}}
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = "failed"
	run.Outcomes[0].Status = "failed"
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.Password != "" || got.Source.Password != "" || got.Source.Params != "" {
		t.Fatal("history leaked secrets")
	}
	if got.Status != "failed" || len(got.Snapshots) != 1 || got.Outcomes[0].Status != "failed" {
		t.Fatal("report did not survive reopen")
	}
	rows, err := s.ListRuns(100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("history: %v %v", rows, err)
	}
	var payload string
	s.db.QueryRow("SELECT payload FROM sync_runs WHERE id=?", run.ID).Scan(&payload)
	if strings.Contains(payload, "secret") || strings.Contains(payload, "hidden") {
		t.Fatal("stored JSON contains secret")
	}
}
