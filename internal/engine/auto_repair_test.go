package engine

import (
	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
	"testing"
)

func TestAutomaticRepairOptInRetainsOriginalCandidates(t *testing.T) {
	broken := "åˆ›å»ºæ—¶é—´"
	s := schema.New("test").AddTables(schema.NewTable("t").SetComment(broken).AddColumns(schema.NewStringColumn("c", "varchar").SetComment(broken)))
	if len(detectedCommentRepairs(s, false)) != 0 {
		t.Fatal("disabled mode must not detect/mutate")
	}
	Normalize(s, dto.DefaultNormalizeOptions())
	if s.Tables[0].Attrs[0].(*schema.Comment).Text != broken {
		t.Fatal("default changed a comment")
	}
	candidates := detectedCommentRepairs(s, true)
	opts := dto.DefaultNormalizeOptions()
	opts.AutoRepairComments = true
	Normalize(s, opts)
	if len(candidates) != 2 || candidates[0].Original != broken || candidates[0].ID != repairID("t", "", broken) {
		t.Fatalf("lost raw candidates: %+v", candidates)
	}
	if s.Tables[0].Attrs[0].(*schema.Comment).Text != "创建时间" || s.Tables[0].Columns[0].Attrs[0].(*schema.Comment).Text != "创建时间" {
		t.Fatal("table and column comments must recover")
	}
	if len(detectedCommentRepairs(s, true)) != 0 {
		t.Fatal("recovery must be idempotent")
	}
}
func TestAutomaticTargetRepairPrecedesSchemaAndRetainsSnapshots(t *testing.T) {
	plan := &dto.PlanResult{Statements: []dto.PlannedStatement{{ID: "s0", SQL: "ALTER new"}}, Snapshots: []dto.TableSnapshot{{Table: "a", DDL: "before a"}}}
	repair := &dto.PlanResult{Statements: []dto.PlannedStatement{{ID: "s0", SQL: "ALTER comment"}}, Snapshots: []dto.TableSnapshot{{Table: "a", DDL: "repair a"}, {Table: "b", DDL: "before b"}}, Repairs: []dto.CommentRepair{{ID: "r1"}}}
	mergeTargetRepair(plan, repair)
	if plan.Statements[0].SQL != "ALTER comment" || plan.Statements[0].ID == plan.Statements[1].ID {
		t.Fatal("repair must be first with unique statement ID")
	}
	if len(plan.Snapshots) != 2 || plan.Snapshots[0].DDL != "before a" || len(plan.Repairs) != 1 {
		t.Fatalf("snapshot union invalid: %+v", plan)
	}
}
func TestAutomaticRepairVerificationDoesNotHideFailedRepairs(t *testing.T) {
	repairs := []dto.CommentRepair{{ID: "r", Table: "t", Column: "c", Recovered: "创建时间"}}
	run := &dto.SyncRun{Repairs: repairs, SelectionComplete: true, Remaining: []dto.RemainingDifference{{Category: "unselected"}}}
	appendUnrepairedComments(run, []dto.CommentRepair{{Table: "t", Column: "c", Original: "åˆ›å»ºæ—¶é—´"}})
	if run.SelectionComplete || len(run.Remaining) != 2 {
		t.Fatal("normalization hid an unapplied repair")
	}
	for _, current := range [][]dto.CommentRepair{nil, {{Table: "t", Column: "c", Original: "合法的新注释"}}} {
		run = &dto.SyncRun{Repairs: repairs, SelectionComplete: true}
		appendUnrepairedComments(run, current)
		if !run.SelectionComplete {
			t.Fatal("selected schema replacement/drop is valid")
		}
	}
}
func TestSameDatabaseRepairDeduplication(t *testing.T) {
	a := dto.ConnConfig{ID: "a", Host: "localhost", Port: 3306, Database: "db"}
	b := a
	b.ID = "b"
	if !sameDatabase(a, b) {
		t.Fatal("same endpoint must not be repaired twice")
	}
	b.Database = "other"
	if sameDatabase(a, b) {
		t.Fatal("different databases must stay separate")
	}
	b = a
	b.SnapshotID = "snapshot"
	if sameDatabase(a, b) {
		t.Fatal("archive must never be treated as live source")
	}
}
