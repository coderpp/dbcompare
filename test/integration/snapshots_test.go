//go:build integration

package integration_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/service"
	"github.com/pp/db-compare/internal/store"
)

// This test only targets an explicitly provided disposable, loopback MySQL container.
func TestSnapshotDisposableMySQL(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("DBCOMPARE_SNAPSHOT_TEST_PORT"))
	if err != nil || port == 0 {
		t.Skip("set DBCOMPARE_SNAPSHOT_TEST_PORT for a disposable MySQL container")
	}
	cfg := dto.ConnConfig{Host: "127.0.0.1", Port: port, User: "root"}
	ctx := context.Background()
	admin, err := engine.OpenServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	database := "dbcompare_snapshot_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.DB.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE DATABASE `" + database + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci")
	defer admin.DB.Exec("DROP DATABASE `" + database + "`")
	exec("CREATE TABLE `" + database + "`.account (id int NOT NULL PRIMARY KEY, name varchar(50) COMMENT '姓名') COMMENT='账户'")
	exec("CREATE VIEW `" + database + "`.account_view AS SELECT id FROM `" + database + "`.account")
	cfg.Database = database
	dir := os.Getenv("DBCOMPARE_SNAPSHOT_QA_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	st, err := store.Open(dir, store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg.Name = "隔离容器验收"
	saved, err := st.SaveConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &service.AppServices{Store: st, Engine: engine.New()}
	svc := &service.SnapshotService{App: app}
	target := dto.SnapshotTarget{ConnectionID: saved.ID, Database: database}
	retryDatabase := database + "_retry"
	r, err := svc.Capture([]dto.SnapshotTarget{target, {ConnectionID: saved.ID, Database: retryDatabase}})
	if err != nil || r.Status != "partial" || r.Results[0].SnapshotID == "" || r.Results[1].Error == "" {
		t.Fatalf("partial run: %+v %v", r, err)
	}
	partialRunID := r.ID
	exec("CREATE DATABASE `" + retryDatabase + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci")
	defer admin.DB.Exec("DROP DATABASE `" + retryDatabase + "`")
	retried, retryErr := svc.RetryRun(partialRunID)
	if retryErr != nil || retried.Status != "success" || len(retried.Results) != 1 || retried.Results[0].Target.Database != retryDatabase {
		t.Fatalf("retry did not recover only failed database: %+v %v", retried, retryErr)
	}
	first := r.Results[0].SnapshotID
	doc, err := svc.Get(first)
	if err != nil || len(doc.Archive.Tables) != 1 || !strings.Contains(doc.Archive.Tables[0].DDL, "姓名") || len(doc.Archive.Warnings) < 2 {
		t.Fatalf("capture: %+v %v", doc, err)
	}
	f, err := svc.Export([]string{first}, "json")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := svc.Import(f.Content); err != nil || n != 1 {
		t.Fatalf("actual MySQL roundtrip: %d %v", n, err)
	}
	r, err = svc.Capture([]dto.SnapshotTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	doc, err = svc.Get(r.Results[0].SnapshotID)
	if err != nil || !r.Results[0].Unchanged || r.Results[0].SnapshotID != first {
		t.Fatal("unchanged capture", err)
	}
	exec("ALTER TABLE `" + database + "`.account ADD COLUMN amount decimal(18,2) NULL COMMENT '金额', ADD INDEX idx_name(name)")
	r, err = svc.Capture([]dto.SnapshotTarget{target})
	if err != nil || r.Results[0].SnapshotID == "" {
		t.Fatal(err)
	}
	second := r.Results[0].SnapshotID
	d, err := svc.Compare(dto.SnapshotDiffRequest{BeforeID: first, AfterID: second, Normalize: dto.DefaultNormalizeOptions()})
	if err != nil || len(d.Differences) != 1 || !strings.Contains(d.SQL, "amount") {
		t.Fatalf("offline diff %+v %v", d, err)
	}
	d, err = svc.Compare(dto.SnapshotDiffRequest{BeforeID: first, Live: &target, Normalize: dto.DefaultNormalizeOptions()})
	if err != nil || len(d.Differences) != 1 {
		t.Fatalf("live diff %+v %v", d, err)
	}
	res, err := svc.RestoreCompare(first, target, dto.DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, v := range append(append(res.Added, res.Modified...), res.Dropped...) {
		ids = append(ids, v.ID)
	}
	plan, err := app.Engine.Plan(ctx, dto.PlanRequest{SessionID: res.SessionID, ChangeIDs: ids, AllowDrop: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Statements) == 0 {
		t.Fatal("empty restore plan")
	}
	result, err := app.Engine.ApplyTracked(ctx, dto.ApplyRequest{SessionID: res.SessionID, PlanID: plan.ID}, nil, nil, st.SaveRun)
	if err != nil || !result.VerifyClean {
		t.Fatalf("restore result %+v %v", result, err)
	}
	resumed, err := (&service.SyncService{App: app}).Resume(result.Run.ID)
	if err != nil || len(resumed.Compare.Modified) > 0 {
		t.Fatalf("snapshot-source resume %+v %v", resumed, err)
	}
	cfg.Name = "第二个隔离连接"
	secondConnection, err := st.SaveConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	secondTarget := dto.SnapshotTarget{ConnectionID: secondConnection.ID, Database: retryDatabase}
	task, err := svc.SaveTask(dto.SnapshotTask{Name: "每天结构归档", Targets: []dto.SnapshotTarget{target, secondTarget}, Schedule: "daily", Time: "02:00", RetentionDays: 90, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	beforeNext := task.NextRun
	r, err = svc.RunTask(task.ID)
	if err != nil || r.Status != "success" || len(r.Results) != 2 {
		t.Fatal(err)
	}
	ov, err := svc.Overview()
	if err != nil || ov.Tasks[0].NextRun != beforeNext {
		t.Fatal("manual execution changed schedule", err)
	}
	versionCount := len(ov.Snapshots)
	r, err = svc.RunTask(task.ID)
	if err != nil || r.Status != "success" || len(r.Results) != 2 || !r.Results[0].Unchanged || !r.Results[1].Unchanged {
		t.Fatalf("unchanged task results: %+v %v", r, err)
	}
	ov, err = svc.Overview()
	if err != nil || len(ov.Snapshots) != versionCount || ov.Tasks[0].NextRun != beforeNext {
		t.Fatalf("unchanged task created versions or changed schedule: %+v %v", ov, err)
	}
}
