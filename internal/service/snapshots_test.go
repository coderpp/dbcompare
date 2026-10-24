package service

import (
	"context"
	"testing"
	"time"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/store"
)

func TestScheduleAndCatchUp(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, loc)
	cases := []struct {
		task dto.SnapshotTask
		want string
	}{{dto.SnapshotTask{Schedule: "daily", Time: "09:00"}, "2026-10-17 09:00"}, {dto.SnapshotTask{Schedule: "weekly", Time: "10:00", Weekdays: []int{5}}, "2026-10-23 10:00"}, {dto.SnapshotTask{Schedule: "interval", IntervalHours: 6}, "2026-10-16 16:00"}}
	for _, c := range cases {
		next, err := NextSnapshotTime(c.task, now)
		if err != nil || next.Format("2006-01-02 15:04") != c.want {
			t.Fatalf("next %s %v", next, err)
		}
	}
	if _, err := NextSnapshotTime(dto.SnapshotTask{Schedule: "weekly", Time: "02:00"}, now); err == nil {
		t.Fatal("missing weekdays accepted")
	}
	st, err := store.Open(t.TempDir(), store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &SnapshotService{App: &AppServices{Store: st, Engine: engine.New()}}
	// Missing connection fails before any network access, while the overdue schedule is advanced once.
	_, err = st.PutSnapshotTask(dto.SnapshotTask{ID: "catchup", Name: "catchup", Targets: []dto.SnapshotTarget{{ConnectionID: "missing", Database: "demo"}}, Schedule: "daily", Time: "02:00", Enabled: true, Background: true, RetentionDays: 90, NextRun: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.tick(true); err != nil {
		t.Fatal(err)
	}
	if err = svc.tick(true); err != nil {
		t.Fatal(err)
	}
	runs, err := st.SnapshotRuns()
	if err != nil || len(runs) != 1 || !runs[0].CatchUp || runs[0].Status != "failed" {
		t.Fatalf("catchup %+v %v", runs, err)
	}
}
func TestSnapshotPackageRoundTripAndTamperRejection(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &SnapshotService{App: &AppServices{Store: st, Engine: engine.New()}}
	sch := schema.New("demo").AddTables(schema.NewTable("t").AddColumns(schema.NewIntColumn("id", "int")))
	raw, err := mysql.MarshalHCL(sch)
	if err != nil {
		t.Fatal(err)
	}
	a := dto.SchemaArchive{Format: 1, Database: "demo", Model: string(raw), Tables: []dto.TableSnapshot{}, Warnings: []string{}}
	// Empty table list with nonempty model is deliberately invalid and must fail atomically.
	rec, err := st.PutSnapshot(dto.SnapshotRecord{Database: "demo", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}, a)
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Export([]string{rec.ID}, "json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Import(f.Content); err == nil {
		t.Fatal("inconsistent model was accepted")
	}
	sch = schema.New("empty")
	raw, err = mysql.MarshalHCL(sch)
	if err != nil {
		t.Fatal(err)
	}
	a.Database = "empty"
	a.Model = string(raw)
	rec, err = st.PutSnapshot(dto.SnapshotRecord{Database: "empty", CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}, a)
	if err != nil {
		t.Fatal(err)
	}
	f, err = svc.Export([]string{rec.ID}, "json")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Import(f.Content); err != nil || n != 1 {
		t.Fatalf("roundtrip %d %v", n, err)
	}
	d, err := svc.Compare(dto.SnapshotDiffRequest{BeforeID: rec.ID, AfterID: rec.ID, Normalize: dto.DefaultNormalizeOptions()})
	if err != nil || len(d.Differences) != 0 {
		t.Fatal(err)
	}
	_, err = engine.DiffArchives(context.Background(), a, a, dto.DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetryRecordsMissingConnectionsAndSkipsSuccessfulTargets(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &SnapshotService{App: &AppServices{Store: st, Engine: engine.New()}}
	old := dto.SnapshotRun{ID: "retry-source", TaskName: "原任务", Status: "partial", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Results: []dto.SnapshotOutcome{
		{Target: dto.SnapshotTarget{ConnectionID: "deleted-1", Database: "a"}, Error: "connection gone"},
		{Target: dto.SnapshotTarget{ConnectionID: "deleted-2", Database: "b"}, Error: "connection gone"},
		{Target: dto.SnapshotTarget{ConnectionID: "never-replay-success", Database: "c"}, SnapshotID: "old-success"},
	}}
	if err = st.PutSnapshotRun(old); err != nil {
		t.Fatal(err)
	}
	retry, err := svc.RetryRun(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.Results) != 2 || retry.Status != "failed" || retry.Results[0].Error == "" || retry.Results[1].Error == "" {
		t.Fatalf("incomplete retry results: %+v", retry)
	}
	runs, err := st.SnapshotRuns()
	if err != nil || len(runs) != 2 {
		t.Fatal("retry result was not saved", err)
	}
}
