package engine

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"strings"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

// Plan builds ordered DDL for selected changes.
func (e *Engine) Plan(ctx context.Context, req dto.PlanRequest) (*dto.PlanResult, error) {
	sess, ok := e.Sessions.Get(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("session %s not found; run compare again", req.SessionID)
	}
	sess.mu.Lock()
	if sess.applying {
		sess.mu.Unlock()
		return nil, fmt.Errorf("同步正在执行，请等待完成")
	}
	sess.Plan = nil
	sess.mu.Unlock()
	known := map[string]bool{}
	for _, it := range sess.Items {
		known[it.ID] = true
	}
	for _, id := range req.ChangeIDs {
		if !known[id] {
			return nil, fmt.Errorf("未知差异 %s，请重新对比", id)
		}
	}
	selected := sess.SelectChanges(req.ChangeIDs)
	skippedDrops := 0
	if !req.AllowDrop {
		filtered := make([]schema.Change, 0, len(selected))
		for _, ch := range selected {
			if isDropChange(ch) {
				skippedDrops++
				continue
			}
			if mt, ok := ch.(*schema.ModifyTable); ok {
				keep := make([]schema.Change, 0, len(mt.Changes))
				for _, sub := range mt.Changes {
					if isDropChange(sub) {
						skippedDrops++
						continue
					}
					keep = append(keep, sub)
				}
				if len(keep) == 0 {
					continue
				}
				// Copy so we never mutate the session's stored ModifyTable.
				cp := *mt
				cp.Changes = keep
				filtered = append(filtered, &cp)
				continue
			}
			filtered = append(filtered, ch)
		}
		selected = filtered
	}

	pre := Precheck(sess.SourceSch, sess.TargetSch, sess.Target.SemVer, selected)
	pre = append(pre, checkSelectionDependencies(sess.TargetSch, selected)...)
	if skippedDrops > 0 {
		pre = append(pre, dto.PrecheckIssue{
			Level: dto.RiskWarn, Code: "drops_skipped",
			Message: fmt.Sprintf("已跳过 %d 项删除（DROP 列/索引/表等）；勾选「允许删除多余对象」后重新生成才会包含", skippedDrops),
		})
	}
	blocked := false
	for _, p := range pre {
		if p.Level == dto.RiskError || p.Level == dto.RiskCritical {
			if !req.AllowUnsafe {
				blocked = true
			}
		}
	}

	plan, err := sess.Target.Driver.PlanChanges(ctx, "sync", selected, func(o *migrate.PlanOptions) {
		o.Indent = "  "
	})
	if err != nil {
		return nil, fmt.Errorf("plan changes: %w", err)
	}

	selectedItems := itemsForChanges(sess, selected)
	stmts := make([]dto.PlannedStatement, 0, len(plan.Changes))
	for i, c := range plan.Changes {
		sql := ensureQuotedSQL(c.Cmd)
		if strings.Contains(strings.ToLower(sql), " partitioned") || strings.Contains(strings.ToUpper(sql), "PARTITION BY") {
			pre = append(pre, dto.PrecheckIssue{
				Level: dto.RiskWarn, Code: "partition_unsupported",
				Message: "skipped statement involving partitions (not supported by OSS Atlas planner): " + truncate(sql, 120),
			})
			continue
		}
		rev := ""
		switch r := c.Reverse.(type) {
		case string:
			rev = r
		case []string:
			rev = strings.Join(r, ";\n")
		}
		risk := dto.RiskInfo
		up := strings.ToUpper(sql)
		if strings.Contains(up, "DROP TABLE") || strings.Contains(up, "DROP COLUMN") {
			risk = dto.RiskCritical
		} else if strings.Contains(up, "DROP ") || strings.Contains(up, "MODIFY") || strings.Contains(up, "CHANGE") {
			risk = dto.RiskWarn
		}
		table, links := statementLinks(sess, c.Source, selectedItems)
		if table == "" || len(links) == 0 {
			return nil, fmt.Errorf("SQL 缺少明确的差异关联，已停止生成：%s", c.Comment)
		}
		changeID := ""
		if len(links) > 0 {
			changeID = links[0]
		}
		stmts = append(stmts, dto.PlannedStatement{
			ID:        fmt.Sprintf("s%d", i),
			ChangeID:  changeID,
			ChangeIDs: links,
			Table:     table,
			SQL:       sql,
			Reverse:   rev,
			Risk:      risk,
			Comment:   c.Comment,
		})
	}

	tables := []string{}
	for _, st := range stmts {
		tables = append(tables, st.Table)
	}
	snapshots, err := snapshotsFor(ctx, sess.Target, tables)
	if err != nil {
		return nil, fmt.Errorf("读取同步前结构: %w", err)
	}
	result := &dto.PlanResult{TargetDatabase: sess.Target.Config.Database, ID: uuid.NewString(), SessionID: sess.ID, Selected: selectedItems, Snapshots: snapshots, AllowDrop: req.AllowDrop, AllowUnsafe: req.AllowUnsafe, Mode: "schema", Statements: stmts, Prechecks: pre, Blocked: blocked}
	if sess.Normalize.AutoRepairComments {
		if err := e.includeAutomaticRepairs(ctx, sess, result); err != nil {
			return nil, err
		}
	}
	sess.mu.Lock()
	sess.Plan = result
	sess.mu.Unlock()
	return result, nil
}

func isDropChange(ch schema.Change) bool {
	switch ch.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropIndex, *schema.DropForeignKey, *schema.DropCheck, *schema.DropPrimaryKey:
		return true
	default:
		return false
	}
}

// ensureQuotedSQL is a light pass; Atlas already quotes most identifiers.
func ensureQuotedSQL(sql string) string {
	return strings.TrimSpace(sql)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ProgressFunc receives apply progress updates.
type ProgressFunc func(dto.ApplyProgress)

// Apply executes a plan; services use ApplyTracked to persist each transition.
func (e *Engine) Apply(ctx context.Context, req dto.ApplyRequest, onProgress ProgressFunc, audit func(dto.AuditEntry)) (*dto.ApplyResult, error) {
	return e.ApplyTracked(ctx, req, onProgress, audit, nil)
}

type verifyResult struct {
	Remaining int
	Summary   string
}

func (e *Engine) verifySession(ctx context.Context, sess *Session) (*verifyResult, error) {
	srcSch, err := sess.Source.Inspect(ctx, nil)
	if err != nil {
		return nil, err
	}
	dstSch, err := sess.Target.Inspect(ctx, nil)
	if err != nil {
		return nil, err
	}
	Normalize(srcSch, sess.Normalize)
	Normalize(dstSch, sess.Normalize)
	srcSch.Name = dstSch.Name
	changes, err := sess.Target.Driver.SchemaDiff(dstSch, srcSch)
	if err != nil {
		return nil, err
	}
	changes = visibleSchemaChanges(changes, dstSch, srcSch, sess.Normalize)

	// Ignore remaining drops if user didn't allow drop (target may have extra tables).
	remaining := 0
	for _, ch := range changes {
		if _, ok := ch.(*schema.DropTable); ok {
			continue
		}
		if mt, ok := ch.(*schema.ModifyTable); ok {
			onlyDrops := true
			for _, sub := range mt.Changes {
				if !isDropChange(sub) {
					onlyDrops = false
					break
				}
			}
			if onlyDrops {
				continue
			}
		}
		if isDropChange(ch) {
			continue
		}
		remaining++
	}
	summary := fmt.Sprintf("post-verify: %d remaining non-drop differences", remaining)
	if remaining == 0 {
		summary = "post-verify: schemas match (ignoring extra objects on target)"
	}
	return &verifyResult{Remaining: remaining, Summary: summary}, nil
}
