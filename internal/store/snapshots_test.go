package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pp/db-compare/internal/dto"
)

func TestUnchangedCaptureReusesVersionAndPreservesMetadata(t *testing.T) {
	s, err := Open(t.TempDir(), NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := dto.SchemaArchive{Format: 1, Database: "demo", Model: "version-a"}
	record := dto.SnapshotRecord{TaskID: "original-task", ConnectionID: "conn", Database: "demo", CapturedAt: "2026-01-01T00:00:00Z"}
	// Existing versions made before the optimization must be reusable too.
	first, err := s.PutSnapshot(record, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AnnotateSnapshot(first.ID, "important", true); err != nil {
		t.Fatal(err)
	}
	if first.LastCheckedAt != first.CapturedAt {
		t.Fatal("first capture has no check time")
	}
	// Simulate a version created before LastCheckedAt was introduced.
	legacy, err := s.Snapshot(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Record.LastCheckedAt = ""
	legacyPayload, err := json.Marshal(legacy.Record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE schema_snapshots SET payload=? WHERE id=?`, string(legacyPayload), first.ID); err != nil {
		t.Fatal(err)
	}
	same := func(a, b dto.SchemaArchive) (bool, error) { return a.Model == b.Model, nil }
	record.TaskID = "another-task"
	record.CapturedAt = "2026-10-19T00:00:00Z"
	a.Warnings = []string{"metadata changed, structure unchanged"}
	got, err := s.PutSnapshotIfChanged(record, a, same)
	if err != nil || got.ID != first.ID || !got.Unchanged || got.CapturedAt != first.CapturedAt || got.LastCheckedAt != record.CapturedAt || got.TaskID != first.TaskID || !got.Pinned || got.Note != "important" {
		t.Fatalf("reuse: %+v %v", got, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM schema_snapshots`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("new record created: %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM schema_blobs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("new blob created: %d %v", n, err)
	}
	doc, err := s.Snapshot(first.ID)
	if err != nil || doc.Record.Unchanged || !doc.Record.Pinned || doc.Archive.Model != "version-a" || doc.Record.LastCheckedAt != record.CapturedAt || doc.Record.CapturedAt != first.CapturedAt {
		t.Fatalf("original changed: %+v %v", doc, err)
	}
	// A delayed concurrent capture must not move the visible time backwards.
	delayed := record
	delayed.CapturedAt = first.CapturedAt
	older, err := s.PutSnapshotIfChanged(delayed, a, same)
	if err != nil || older.LastCheckedAt != record.CapturedAt {
		t.Fatalf("check time regressed: %+v %v", older, err)
	}
	// Only compare with the latest version: a return to A after B is a new event.
	a.Model = "version-b"
	second, err := s.PutSnapshotIfChanged(record, a, same)
	if err != nil || second.ID == first.ID || second.Unchanged {
		t.Fatalf("change: %+v %v", second, err)
	}
	a.Model = "version-a"
	third, err := s.PutSnapshotIfChanged(record, a, same)
	if err != nil || third.ID == first.ID || third.ID == second.ID || third.Unchanged {
		t.Fatalf("reversion: %+v %v", third, err)
	}
	// Retention must not remove the latest baseline even when no new versions occur.
	if err := s.PruneSnapshots(record.TaskID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Snapshot(third.ID); err != nil {
		t.Fatal("latest baseline removed", err)
	}
	record.ConnectionID = "other-connection"
	other, err := s.PutSnapshotIfChanged(record, a, same)
	if err != nil || other.Unchanged || other.ID == third.ID {
		t.Fatalf("connection isolation: %+v %v", other, err)
	}
	// A comparison failure must never discard a potentially changed schema.
	var beforeBlobs int
	if err := s.db.QueryRow(`SELECT count(*) FROM schema_blobs`).Scan(&beforeBlobs); err != nil {
		t.Fatal(err)
	}
	a.Model = "version-c"
	_, err = s.PutSnapshotIfChanged(record, a, func(dto.SchemaArchive, dto.SchemaArchive) (bool, error) {
		return false, errors.New("invalid prior model")
	})
	if err == nil {
		t.Fatal("comparison error ignored")
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM schema_blobs`).Scan(&n); err != nil || n != beforeBlobs {
		t.Fatalf("failed comparison wrote content: %d %v", n, err)
	}
}

func TestConcurrentUnchangedCaptureCreatesOneVersion(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	results := make(chan dto.SnapshotRecord, 2)
	start := make(chan struct{})
	for _, st := range []*Store{a, b} {
		wg.Add(1)
		go func(st *Store) {
			defer wg.Done()
			<-start
			r, err := st.PutSnapshotIfChanged(dto.SnapshotRecord{ConnectionID: "conn", Database: "demo", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}, dto.SchemaArchive{Database: "demo", Model: "same"}, func(a, b dto.SchemaArchive) (bool, error) { return a.Model == b.Model, nil })
			if err != nil {
				t.Error(err)
			}
			results <- r
		}(st)
	}
	close(start)
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	unchanged := 0
	for r := range results {
		ids[r.ID] = true
		if r.Unchanged {
			unchanged++
		}
	}
	list, err := a.SnapshotRecords()
	if err != nil || len(list) != 1 || len(ids) != 1 || unchanged != 1 {
		t.Fatalf("concurrent versions: %+v ids=%v unchanged=%d err=%v", list, ids, unchanged, err)
	}
}

func TestSnapshotDedupPinRetentionImportAndLease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a := dto.SchemaArchive{Format: 1, Database: "demo", Model: "model", Tables: []dto.TableSnapshot{{Table: "t", DDL: "ddl"}}, Warnings: []string{}}
	record := dto.SnapshotRecord{TaskID: "job", ConnectionID: "connection", Database: "demo", CapturedAt: time.Now().UTC().AddDate(0, 0, -100).Format(time.RFC3339Nano)}
	old, err := s.PutSnapshot(record, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AnnotateSnapshot(old.ID, "keep", true); err != nil {
		t.Fatal(err)
	}
	record.CapturedAt = time.Now().UTC().AddDate(0, 0, -95).Format(time.RFC3339Nano)
	stale, err := s.PutSnapshot(record, a)
	if err != nil || !stale.Unchanged {
		t.Fatal("dedup marker", err)
	}
	record.CapturedAt = time.Now().UTC().Format(time.RFC3339Nano)
	fresh, err := s.PutSnapshot(record, a)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT count(*) FROM schema_blobs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dedup: %d %v", n, err)
	}
	if err = s.PruneSnapshots("job", 90); err != nil {
		t.Fatal(err)
	}
	list, err := s.SnapshotRecords()
	if err != nil || len(list) != 2 {
		t.Fatalf("retention/pin: %+v %v", list, err)
	}
	doc, err := s.Snapshot(fresh.ID)
	if err != nil || doc.Archive.Model != "model" {
		t.Fatal(err)
	}
	if n, err := s.ImportSnapshots([]dto.SnapshotDocument{doc}); err != nil || n != 1 {
		t.Fatal(err)
	}
	list, _ = s.SnapshotRecords()
	if list[0].ConnectionID == "connection" && list[1].ConnectionID == "connection" && list[2].ConnectionID == "connection" {
		t.Fatal("import must not bind external connection IDs")
	}
	_, err = s.PutSnapshotTask(dto.SnapshotTask{ID: "job", Enabled: true, NextRun: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for _, st := range []*Store{s, other} {
		wg.Add(1)
		go func(st *Store) {
			defer wg.Done()
			yes, err := st.ClaimSnapshotTask("job", time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), true)
			if err != nil {
				t.Error(err)
			}
			wins <- yes
		}(st)
	}
	wg.Wait()
	close(wins)
	count := 0
	for yes := range wins {
		if yes {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("cross-process claim won %d times", count)
	}
	s.ReleaseSnapshotTask("job")
	other.ReleaseSnapshotTask("job")
	yes, err := s.ClaimSnapshotTask("job", "", false)
	if err != nil || !yes {
		t.Fatal("manual run after release", err)
	}
	data, _ := json.Marshal(doc)
	if len(data) == 0 {
		t.Fatal("empty document")
	}
}

func TestExpiredLeaseCannotReleaseAnotherWorker(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir, NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	_, err = a.PutSnapshotTask(dto.SnapshotTask{ID: "lease", NextRun: "", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	yes, err := a.ClaimSnapshotTask("lease", "", false)
	if err != nil || !yes {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE snapshot_tasks SET lease_until='' WHERE id='lease'`); err != nil {
		t.Fatal(err)
	}
	yes, err = b.ClaimSnapshotTask("lease", "", false)
	if err != nil || !yes {
		t.Fatal(err)
	}
	if err = a.RenewSnapshotTask("lease"); err == nil {
		t.Fatal("expired owner renewed another worker's lease")
	}
	a.ReleaseSnapshotTask("lease")
	if err = b.RenewSnapshotTask("lease"); err != nil {
		t.Fatal("expired owner released active worker", err)
	}
}
