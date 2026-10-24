package engine

import (
	"context"
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

// Inspect only table/column comments. Database contents and unrelated attributes
// are never guessed or transcoded. Call before Normalize to retain original IDs.
func detectedCommentRepairs(s *schema.Schema, enabled bool) []dto.CommentRepair {
	if !enabled || s == nil {
		return nil
	}
	out := []dto.CommentRepair{}
	add := func(table, column string, attrs []schema.Attr) {
		for _, a := range attrs {
			if comment, ok := a.(*schema.Comment); ok {
				if recovered, changed := RecoverComment(comment.Text); changed {
					out = append(out, dto.CommentRepair{ID: repairID(table, column, comment.Text), Table: table, Column: column, Original: comment.Text, Recovered: recovered})
				}
			}
		}
	}
	for _, t := range s.Tables {
		add(t.Name, "", t.Attrs)
		for _, c := range t.Columns {
			add(t.Name, c.Name, c.Attrs)
		}
	}
	return out
}

func recoverSchemaComments(s *schema.Schema) {
	fix := func(attrs []schema.Attr) {
		for i, a := range attrs {
			if c, ok := a.(*schema.Comment); ok {
				if text, changed := RecoverComment(c.Text); changed {
					attrs[i] = &schema.Comment{Text: text}
				}
			}
		}
	}
	for _, t := range s.Tables {
		fix(t.Attrs)
		for _, c := range t.Columns {
			fix(c.Attrs)
		}
	}
}

func repairIDs(repairs []dto.CommentRepair) []string {
	ids := make([]string, 0, len(repairs))
	for _, r := range repairs {
		ids = append(ids, r.ID)
	}
	return ids
}
func sameDatabase(a, b dto.ConnConfig) bool {
	return a.SnapshotID == "" && b.SnapshotID == "" && a.Database == b.Database &&
		((a.ID != "" && a.ID == b.ID) || (a.Host == b.Host && a.Port == b.Port))
}

// Target repair clauses run before schema changes planned from the recovered
// target model. This preserves column definitions and prevents later ALTERs
// from restoring mojibake. Both use a single raw, pre-execution snapshot check.
func (e *Engine) includeAutomaticRepairs(ctx context.Context, sess *Session, plan *dto.PlanResult) error {
	if len(sess.TargetRepairs) > 0 {
		repair, err := e.PlanCommentRepair(ctx, dto.RepairRequest{SessionID: sess.ID, Side: "target", IDs: repairIDs(sess.TargetRepairs)})
		if err != nil {
			return fmt.Errorf("目标库注释修复计划: %w", err)
		}
		e.Sessions.Delete(repair.SessionID)
		mergeTargetRepair(plan, repair)
	}
	if len(sess.SourceRepairs) > 0 && sess.Source.Config.SnapshotID == "" && !sameDatabase(sess.Source.Config, sess.Target.Config) {
		repair, err := e.PlanCommentRepair(ctx, dto.RepairRequest{SessionID: sess.ID, Side: "source", IDs: repairIDs(sess.SourceRepairs)})
		if err != nil {
			return fmt.Errorf("源库注释修复计划: %w", err)
		}
		plan.SourceRepairPlan = repair
	}
	return nil
}

func mergeTargetRepair(plan, repair *dto.PlanResult) {
	for i := range repair.Statements {
		repair.Statements[i].ID = "repair-" + repair.Statements[i].ID
	}
	plan.Statements = append(repair.Statements, plan.Statements...)
	plan.Repairs = repair.Repairs
	plan.Selected = append(plan.Selected, repair.Selected...)
	seen := map[string]bool{}
	for _, s := range plan.Snapshots {
		seen[s.Table] = true
	}
	for _, s := range repair.Snapshots {
		if !seen[s.Table] {
			plan.Snapshots = append(plan.Snapshots, s)
			seen[s.Table] = true
		}
	}
}

// Schema verification uses recovered models, so independently check that a
// failed prefix did not leave hidden mojibake. A selected schema change may
// legitimately replace the comment or remove its column/table altogether.
func verifyAutomaticRepairs(ctx context.Context, sess *Session, run *dto.SyncRun) error {
	comments, err := readComments(ctx, sess.Target)
	if err != nil {
		return err
	}
	appendUnrepairedComments(run, comments)
	return nil
}
func appendUnrepairedComments(run *dto.SyncRun, comments []dto.CommentRepair) {
	wanted := map[string]dto.CommentRepair{}
	for _, r := range run.Repairs {
		wanted[r.Table+"\x00"+r.Column] = r
	}
	for _, c := range comments {
		if old, ok := wanted[c.Table+"\x00"+c.Column]; ok {
			if _, broken := RecoverComment(c.Original); broken {
				run.SelectionComplete = false
				name := c.Column
				if name == "" {
					name = "表注释"
				}
				run.Remaining = append(run.Remaining, dto.RemainingDifference{Category: "selected", Item: dto.ChangeItem{ID: old.ID, Table: c.Table, ObjectType: "comment", ObjectName: name, Summary: "注释修复未完成", Kind: dto.KindModify}})
			}
		}
	}
}
