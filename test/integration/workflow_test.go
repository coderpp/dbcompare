//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/service"
	"github.com/pp/db-compare/internal/store"
)

// Explicit opt-in. Only UUID-named databases created here are modified/deleted.
func TestWorkflowIsolatedMySQL(t *testing.T) {
	if os.Getenv("DBCOMPARE_TEST_LOCAL") != "1" {
		t.Skip("set DBCOMPARE_TEST_LOCAL=1 to use saved local connection with isolated test schemas")
	}
	credentials, err := store.Open("", store.NewSecretStore())
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	connections, err := credentials.ListConnections()
	if err != nil {
		t.Fatal(err)
	}
	var cfg dto.ConnConfig
	found := false
	for _, c := range connections {
		if c.Name == "本地" {
			cfg, err = credentials.GetConnection(c.ID, true)
			if err != nil {
				t.Fatal("saved credential unavailable")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("local saved connection not found")
	}
	cfg.Database = ""
	ctx := context.Background()
	admin, err := engine.OpenServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	sourceDB, targetDB := "dbcompare_test_src_"+suffix, "dbcompare_test_dst_"+suffix
	for _, db := range []string{sourceDB, targetDB} {
		if _, err := admin.DB.Exec("CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"); err != nil {
			t.Fatal(err)
		}
		defer admin.DB.Exec("DROP DATABASE `" + db + "`")
	}
	exec := func(db, sql string) {
		t.Helper()
		conn, err := admin.DB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err = conn.ExecContext(ctx, "USE `"+db+"`"); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.ExecContext(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(sourceDB, "CREATE TABLE a (id int NOT NULL PRIMARY KEY, added varchar(100) NULL COMMENT '新增字段', INDEX idx_added(added))")
	exec(targetDB, "CREATE TABLE a (id int NOT NULL PRIMARY KEY)")
	// Separate test-only store: credentials and execution reports never touch the user's history.
	dir := filepath.Join(t.TempDir(), "store")
	st, err := store.Open(dir, store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID = ""
	cfg.Name = "isolated-test"
	saved, err := st.SaveConnection(cfg)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := saved, saved
	src.Database = sourceDB
	dst.Database = targetDB
	app := &service.AppServices{Engine: engine.New(), Store: st}
	cmp := &service.CompareService{App: app}
	sync := &service.SyncService{App: app}
	compare := func() *dto.CompareResult {
		t.Helper()
		r, err := cmp.Compare(dto.CompareRequest{Source: src, Target: dst, Normalize: dto.DefaultNormalizeOptions()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { app.Engine.Sessions.Delete(r.SessionID) })
		return r
	}
	items := func(r *dto.CompareResult) []dto.ChangeItem {
		return append(append(append([]dto.ChangeItem{}, r.Added...), r.Modified...), r.Dropped...)
	}
	plan := func(r *dto.CompareResult, ids []string) *dto.PlanResult {
		t.Helper()
		p, err := sync.Plan(dto.PlanRequest{SessionID: r.SessionID, ChangeIDs: ids, AllowDrop: true})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	apply := func(p *dto.PlanResult) *dto.ApplyResult {
		t.Helper()
		r, err := sync.Apply(dto.ApplyRequest{SessionID: p.SessionID, PlanID: p.ID})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := compare()
	var columnID string
	for _, it := range items(r) {
		if it.Table == "a" && it.ObjectType == "column" && it.ObjectName == "added" {
			columnID = it.ID
		}
	}
	if columnID == "" {
		t.Fatal("column difference missing")
	}
	p := plan(r, []string{columnID})
	if len(p.Statements) != 1 || strings.Contains(p.Statements[0].SQL, "ADD INDEX") {
		t.Fatal("column-only selection generated unrelated SQL")
	}
	if len(p.Statements[0].ChangeIDs) != 1 || p.Statements[0].ChangeIDs[0] != columnID {
		t.Fatal("SQL links incorrect")
	}
	if _, err := app.Engine.ApplyTracked(ctx, dto.ApplyRequest{SessionID: p.SessionID, PlanID: p.ID}, nil, nil, func(*dto.SyncRun) error { return errors.New("test persistence unavailable") }); err == nil {
		t.Fatal("execution must stop if snapshot cannot be persisted")
	}
	var columnCount int
	if err := admin.DB.QueryRow("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=? AND table_name='a' AND column_name='added'", targetDB).Scan(&columnCount); err != nil || columnCount != 0 {
		t.Fatal("DDL ran despite snapshot persistence failure")
	}
	result := apply(p)
	if !result.VerifyClean || result.Applied != 1 || result.Run == nil || !result.Run.SelectionComplete {
		t.Fatalf("partial selection report: %+v", result)
	}
	if len(result.Run.Remaining) != 1 || result.Run.Remaining[0].Category != "unselected" {
		t.Fatalf("unselected index misclassified: %+v", result.Run.Remaining)
	}
	if len(result.Run.Snapshots) != 1 || strings.Contains(result.Run.Snapshots[0].DDL, "`added`") {
		t.Fatal("pre-sync snapshot was not captured before DDL")
	}
	resumed, err := sync.Resume(result.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.ChangeIDs) != 0 {
		t.Fatal("completed column was selected again on resume")
	}
	app.Engine.Sessions.Delete(resumed.Compare.SessionID)
	for _, kind := range []string{"sql", "snapshot", "report", "reverse"} {
		file, err := sync.Export(dto.ExportRequest{RunID: result.Run.ID, Kind: kind})
		if err != nil || file == nil || file.Content == "" {
			t.Fatalf("export %s: %v", kind, err)
		}
		if strings.Contains(file.Content, cfg.Password) && cfg.Password != "" {
			t.Fatal("export contains saved password")
		}
	}
	// Reopen persistent store; histories and snapshots survive process lifetime.
	st.Close()
	st, err = store.Open(dir, store.NewMemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	app.Store = st
	if got, err := sync.GetRun(result.Run.ID); err != nil || got.Applied != 1 || len(got.Snapshots) != 1 {
		t.Fatal("history reopen failed")
	}
	// Restore test-only credential to the new in-memory keyring.
	cfg.ID = saved.ID
	if _, err := st.SaveConnection(cfg); err != nil {
		t.Fatal(err)
	}
	// Real multi-statement failure: successful table modification then a duplicate unique index.
	exec(sourceDB, "CREATE TABLE z_fail (id int NOT NULL PRIMARY KEY, value int, UNIQUE INDEX unique_value(value))")
	exec(targetDB, "CREATE TABLE z_fail (id int NOT NULL PRIMARY KEY, value int)")
	exec(targetDB, "INSERT INTO z_fail VALUES (1,5),(2,5)")
	exec(sourceDB, "CREATE TABLE zz_after (id int NOT NULL PRIMARY KEY, added int)")
	exec(targetDB, "CREATE TABLE zz_after (id int NOT NULL PRIMARY KEY)")
	r = compare()
	ids := []string{}
	for _, it := range items(r) {
		if it.ObjectType != "table" {
			ids = append(ids, it.ID)
		}
	}
	p = plan(r, ids)
	if len(p.Statements) < 2 {
		t.Fatalf("expected multiple statements, got %d", len(p.Statements))
	}
	result = apply(p)
	if result.Failed != 1 || result.Run.Status != "failed" || result.Run.SelectionComplete {
		t.Fatalf("failure report: %+v", result.Run)
	}
	if result.Applied < 1 {
		t.Fatal("test did not exercise partial success")
	}
	resumed, err = sync.Resume(result.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items(resumed.Compare) {
		for _, id := range resumed.ChangeIDs {
			if it.ID == id && it.Table == "a" {
				t.Fatal("successful index was selected for replay")
			}
		}
	}
	notExecuted := 0
	for _, o := range result.Run.Outcomes {
		if o.Status == "not_executed" {
			notExecuted++
		}
	}
	if notExecuted != 1 {
		t.Fatalf("expected a remaining statement not executed after failure, got %d", notExecuted)
	}
	if len(resumed.ChangeIDs) != 2 {
		t.Fatalf("want failed index and unexecuted column; IDs=%v", resumed.ChangeIDs)
	}
	app.Engine.Sessions.Delete(resumed.Compare.SessionID)
	// Drift is rejected before executing any old SQL.
	r = compare()
	ids = nil
	for _, it := range items(r) {
		if it.Table == "z_fail" && it.ObjectType == "index" {
			ids = append(ids, it.ID)
		}
	}
	p = plan(r, ids)
	exec(targetDB, "ALTER TABLE z_fail ADD COLUMN external_change int")
	if _, err := sync.Apply(dto.ApplyRequest{SessionID: p.SessionID, PlanID: p.ID}); err == nil || !strings.Contains(err.Error(), "结构") {
		t.Fatalf("expected drift rejection: %v", err)
	}
	// Inject known reversible mojibake into a disposable test table only.
	exec(targetDB, "CREATE TABLE comments_test (id int COMMENT 'åˆ›å»ºæ—¶é—´', normal int COMMENT '正常中文') COMMENT='æ\u008f\u008fè¿°'")
	r = compare()
	candidates, err := sync.DiagnoseComments(r.SessionID, "target")
	if err != nil {
		t.Fatal(err)
	}
	repairIDs := []string{}
	for _, c := range candidates {
		if c.Table == "comments_test" {
			repairIDs = append(repairIDs, c.ID)
		}
	}
	if len(repairIDs) != 2 {
		t.Fatalf("expected field/table candidates, got %+v", candidates)
	}
	repairPlan, err := sync.PlanCommentRepair(dto.RepairRequest{SessionID: r.SessionID, Side: "target", IDs: repairIDs})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Engine.Sessions.Delete(repairPlan.SessionID)
	if repairPlan.TargetDatabase != targetDB || len(repairPlan.Statements) != 1 {
		t.Fatal("repair target/scope incorrect")
	}
	result = apply(repairPlan)
	if !result.VerifyClean || result.Applied != 1 {
		t.Fatalf("repair failed: %+v", result)
	}
	after, err := sync.DiagnoseComments(r.SessionID, "target")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range after {
		if c.Table == "comments_test" {
			t.Fatal("repair left mojibake")
		}
	}
	// Index switch applies to comparison, SQL generation and actual post-verification.
	exec(sourceDB, "CREATE TABLE option_test (id int NOT NULL PRIMARY KEY, added varchar(100), INDEX idx_added(added))")
	exec(targetDB, "CREATE TABLE option_test (id int NOT NULL PRIMARY KEY)")
	ignoreOptions := dto.DefaultNormalizeOptions()
	ignoreOptions.IgnoreIndexes = true
	ignored, err := cmp.Compare(dto.CompareRequest{Source: src, Target: dst, Tables: []string{"option_test"}, Normalize: ignoreOptions})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Engine.Sessions.Delete(ignored.SessionID)
	ids = nil
	for _, it := range items(ignored) {
		if it.ObjectType == "index" {
			t.Fatal("disabled index still in comparison")
		}
		if it.ObjectType == "table" {
			ids = append(ids, it.ID)
		}
	}
	ignoredPlan := plan(ignored, ids)
	if len(ignoredPlan.Statements) != 1 || strings.Contains(ignoredPlan.Statements[0].SQL, "INDEX") {
		t.Fatal("index-disabled plan generated index DDL")
	}
	ignoredResult := apply(ignoredPlan)
	if !ignoredResult.VerifyClean || len(ignoredResult.Run.Remaining) != 0 {
		t.Fatalf("ignored index affected post-verification: %+v", ignoredResult.Run)
	}
	enabled, err := cmp.Compare(dto.CompareRequest{Source: src, Target: dst, Tables: []string{"option_test"}, Normalize: dto.DefaultNormalizeOptions()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Engine.Sessions.Delete(enabled.SessionID)
	indexDifferences := 0
	for _, it := range items(enabled) {
		if it.ObjectType == "index" {
			indexDifferences++
		}
	}
	if indexDifferences != 1 {
		t.Fatalf("re-enabling index comparison lost index: %d", indexDifferences)
	}
	exec(sourceDB, "CREATE TABLE new_indexed_table (id int NOT NULL PRIMARY KEY, value int, INDEX idx_value(value))")
	addedWithIndexes, err := cmp.Compare(dto.CompareRequest{Source: src, Target: dst, Tables: []string{"new_indexed_table"}, Normalize: ignoreOptions})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Engine.Sessions.Delete(addedWithIndexes.SessionID)
	ids = nil
	for _, it := range items(addedWithIndexes) {
		if it.ObjectType == "table" {
			ids = append(ids, it.ID)
		}
	}
	newPlan := plan(addedWithIndexes, ids)
	if len(newPlan.Statements) != 1 || !strings.Contains(newPlan.Statements[0].SQL, "PRIMARY KEY") || !strings.Contains(newPlan.Statements[0].SQL, "idx_value") {
		t.Fatal("disabling index diff stripped indexes from new table")
	}
	t.Log("Index switch: disabled existing-table index DDL and verification; re-enable restores diff; CREATE TABLE retains full keys")
	t.Logf("Validated selection, SQL links, partial success/failure, durable history, fresh resume, drift blocking, 4 exports, and comment repair on disposable databases (%s). No business database DDL executed.", time.Now().Format(time.RFC3339))
}
