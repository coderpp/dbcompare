//go:build integration

package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
	"github.com/pp/db-compare/internal/service"
	"github.com/pp/db-compare/internal/store"
)

// Only this test's UUID-named databases are changed. No business schema writes.
func TestAutomaticCommentRepairIsolatedMySQL(t *testing.T) {
	if os.Getenv("DBCOMPARE_TEST_LOCAL") != "1" {
		t.Skip("set DBCOMPARE_TEST_LOCAL=1 for isolated local MySQL verification")
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
	for _, c := range connections {
		if c.Name == "本地" {
			cfg, err = credentials.GetConnection(c.ID, true)
			if err != nil {
				t.Fatal("saved local credential unavailable")
			}
			break
		}
	}
	if cfg.Host == "" {
		t.Fatal("saved local connection not found")
	}
	cfg.ID = ""
	cfg.Database = ""
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := engine.OpenServer(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	srcDB, dstDB := "dbcompare_auto_src_"+suffix, "dbcompare_auto_dst_"+suffix
	for _, db := range []string{srcDB, dstDB} {
		if _, err := admin.DB.ExecContext(ctx, "CREATE DATABASE `"+db+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci"); err != nil {
			t.Fatal(err)
		}
		defer admin.DB.Exec("DROP DATABASE `" + db + "`")
	}
	broken := "åˆ›å»ºæ—¶é—´"
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.DB.ExecContext(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE `" + srcDB + "`.demo (id int PRIMARY KEY, name varchar(80) NULL COMMENT '" + broken + "') COMMENT='" + broken + "'")
	exec("CREATE TABLE `" + dstDB + "`.demo (id int PRIMARY KEY, name varchar(20) NULL COMMENT '" + broken + "', extra int NULL COMMENT '" + broken + "') COMMENT='" + broken + "'")
	src, dst := cfg, cfg
	src.Database = srcDB
	dst.Database = dstDB
	eng := engine.New()
	syncService := &service.SyncService{App: &service.AppServices{Engine: eng}}
	compare := func(opts dto.NormalizeOptions) *dto.CompareResult {
		t.Helper()
		r, e := eng.Compare(ctx, dto.CompareRequest{Source: src, Target: dst, Normalize: opts})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { eng.Sessions.Delete(r.SessionID) })
		return r
	}
	comment := func(db, column string) string {
		t.Helper()
		var text string
		var e error
		if column == "" {
			e = admin.DB.QueryRowContext(ctx, "SELECT TABLE_COMMENT FROM information_schema.tables WHERE table_schema=? AND table_name='demo'", db).Scan(&text)
		} else {
			e = admin.DB.QueryRowContext(ctx, "SELECT COLUMN_COMMENT FROM information_schema.columns WHERE table_schema=? AND table_name='demo' AND column_name=?", db, column).Scan(&text)
		}
		if e != nil {
			t.Fatal(e)
		}
		return text
	}
	apply := func(p *dto.PlanResult) {
		t.Helper()
		r, e := eng.Apply(ctx, dto.ApplyRequest{SessionID: p.SessionID, PlanID: p.ID}, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		if !r.VerifyClean {
			t.Fatalf("apply not clean: %+v", r)
		}
	}
	opts := dto.DefaultNormalizeOptions()
	off := compare(opts)
	if len(off.SourceRepairs)+len(off.TargetRepairs) != 0 {
		t.Fatal("default must stay disabled")
	}
	opts.AutoRepairComments = true
	res := compare(opts)
	if len(res.SourceRepairs) != 2 || len(res.TargetRepairs) != 3 {
		t.Fatalf("candidate counts source=%d target=%d", len(res.SourceRepairs), len(res.TargetRepairs))
	}
	ids := []string{}
	for _, it := range res.Modified {
		if it.ObjectType == "table" {
			ids = append(ids, it.ID)
		}
	}
	plan, err := eng.Plan(ctx, dto.PlanRequest{SessionID: res.SessionID, ChangeIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRepairPlan == nil || len(plan.Repairs) != 3 || len(plan.Statements) < 2 {
		t.Fatalf("missing combined plans: %+v", plan)
	}
	if comment(srcDB, "name") != broken || comment(dstDB, "name") != broken {
		t.Fatal("compare/planning wrote comments before confirmation")
	}
	file, err := syncService.Export(dto.ExportRequest{SessionID: plan.SessionID, Kind: "sql"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(file.Content, srcDB) || !strings.Contains(file.Content, dstDB) || strings.Contains(file.Content, broken) {
		t.Fatal("SQL export omits source or propagates mojibake")
	}
	apply(plan.SourceRepairPlan)
	eng.Sessions.Delete(plan.SourceRepairPlan.SessionID)
	apply(plan)
	if comment(srcDB, "name") != "创建时间" || comment(dstDB, "name") != "创建时间" || comment(dstDB, "extra") != "创建时间" {
		t.Fatal("both sides and retained extra column must be repaired")
	}
	var width int
	if err := admin.DB.QueryRowContext(ctx, "SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=? AND table_name='demo' AND column_name='name'", dstDB).Scan(&width); err != nil || width != 80 {
		t.Fatalf("repair undid selected type change: %d, %v", width, err)
	}
	t.Log("combined repair/schema SQL preserves field definitions, does not propagate mojibake, and verifies both sides")

	exec("ALTER TABLE `" + srcDB + "`.demo COMMENT='" + broken + "'")
	res = compare(opts)
	plan, err = eng.Plan(ctx, dto.PlanRequest{SessionID: res.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRepairPlan == nil || len(plan.Statements) != 0 {
		t.Fatal("source-only repair must not require selected schema changes")
	}
	apply(plan.SourceRepairPlan)
	eng.Sessions.Delete(plan.SourceRepairPlan.SessionID)
	t.Log("source-only plan works with no selected schema changes")

	exec("ALTER TABLE `" + srcDB + "`.demo COMMENT='" + broken + "'")
	same, err := eng.Compare(ctx, dto.CompareRequest{Source: src, Target: src, Normalize: opts})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Sessions.Delete(same.SessionID)
	plan, err = eng.Plan(ctx, dto.PlanRequest{SessionID: same.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRepairPlan != nil || len(plan.Statements) != 1 {
		t.Fatal("same database must be repaired only once")
	}
	apply(plan)
	t.Log("same-database comparison deduplicates source and target repairs")

	exec("ALTER TABLE `" + srcDB + "`.demo COMMENT='" + broken + "'")
	archive, err := engine.CaptureArchive(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	originalModel := archive.Model
	exec("DROP TABLE `" + dstDB + "`.demo")
	snapshot, err := eng.CompareArchive(ctx, archive, "test-snapshot", dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Sessions.Delete(snapshot.SessionID)
	ids = []string{}
	for _, it := range snapshot.Added {
		if it.ObjectType == "table" {
			ids = append(ids, it.ID)
		}
	}
	plan, err = eng.Plan(ctx, dto.PlanRequest{SessionID: snapshot.SessionID, ChangeIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRepairPlan != nil || archive.Model != originalModel {
		t.Fatal("snapshot source must stay immutable")
	}
	for _, s := range plan.Statements {
		if strings.Contains(s.SQL, broken) {
			t.Fatal("snapshot restoration propagated mojibake")
		}
	}
	apply(plan)
	if comment(srcDB, "") != broken || comment(dstDB, "") != "创建时间" {
		t.Fatal("snapshot restore changed live source or did not recover target")
	}
	t.Log("snapshot source stays immutable and restored target uses recovered comments")
}
