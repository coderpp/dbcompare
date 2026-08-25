package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

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
		changeID := ""
		if c.Source != nil {
			// Best-effort map by index.
			changeID = fmt.Sprintf("c%d", i)
		}
		stmts = append(stmts, dto.PlannedStatement{
			ID:       fmt.Sprintf("s%d", i),
			ChangeID: changeID,
			SQL:      sql,
			Reverse:  rev,
			Risk:     risk,
			Comment:  c.Comment,
		})
	}

	result := &dto.PlanResult{Statements: stmts, Prechecks: pre, Blocked: blocked}
	sess.mu.Lock()
	sess.Plan = result
	sess.mu.Unlock()
	return result, nil
}

func isDropChange(ch schema.Change) bool {
	switch ch.(type) {
	case *schema.DropTable, *schema.DropColumn, *schema.DropIndex, *schema.DropForeignKey, *schema.DropCheck:
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

// Apply executes planned statements on the target, then post-verifies.
func (e *Engine) Apply(ctx context.Context, req dto.ApplyRequest, onProgress ProgressFunc, audit func(dto.AuditEntry)) (*dto.ApplyResult, error) {
	sess, ok := e.Sessions.Get(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("session %s not found", req.SessionID)
	}
	sess.mu.Lock()
	plan := sess.Plan
	sess.mu.Unlock()
	if plan == nil || len(plan.Statements) == 0 {
		return nil, fmt.Errorf("no plan; call Plan first")
	}
	if plan.Blocked && !req.DryRun {
		return nil, fmt.Errorf("plan is blocked by precheck errors; set allowUnsafe or fix selection")
	}

	stmts := plan.Statements
	if len(req.StatementIDs) > 0 {
		set := map[string]bool{}
		for _, id := range req.StatementIDs {
			set[id] = true
		}
		filtered := stmts[:0]
		for _, s := range stmts {
			if set[s.ID] {
				filtered = append(filtered, s)
			}
		}
		stmts = filtered
	}

	result := &dto.ApplyResult{}
	if req.DryRun {
		result.Applied = len(stmts)
		result.VerifyClean = true
		result.VerifySummary = "dry-run: no statements executed"
		return result, nil
	}

	// Session safety settings.
	if _, err := sess.Target.DB.ExecContext(ctx, "SET SESSION foreign_key_checks=0"); err != nil {
		return nil, err
	}
	defer func() { _, _ = sess.Target.DB.ExecContext(context.Background(), "SET SESSION foreign_key_checks=1") }()
	_, _ = sess.Target.DB.ExecContext(ctx, "SET SESSION sql_mode='STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION'")

	total := len(stmts)
	for i, st := range stmts {
		if onProgress != nil {
			onProgress(dto.ApplyProgress{Total: total, Done: i, CurrentSQL: st.SQL, Message: fmt.Sprintf("executing %d/%d", i+1, total)})
		}
		start := time.Now()
		_, err := sess.Target.DB.ExecContext(ctx, st.SQL)
		entry := dto.AuditEntry{
			ID:         fmt.Sprintf("%s-%d", sess.ID, i),
			At:         time.Now().Format(time.RFC3339),
			Action:     "apply_ddl",
			SourceDSN:  MaskDSN(sess.Source.Config),
			TargetDSN:  MaskDSN(sess.Target.Config),
			SQL:        st.SQL,
			OK:         err == nil,
			DurationMs: time.Since(start).Milliseconds(),
		}
		if err != nil {
			entry.Error = err.Error()
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", st.SQL, err))
			if audit != nil {
				audit(entry)
			}
			if onProgress != nil {
				onProgress(dto.ApplyProgress{Total: total, Done: i, CurrentSQL: st.SQL, Message: err.Error(), Failed: true})
			}
			break
		}
		result.Applied++
		if audit != nil {
			audit(entry)
		}
	}

	// Post-sync verification.
	verify, err := e.verifySession(ctx, sess)
	if err != nil {
		result.VerifySummary = "verify failed: " + err.Error()
		result.VerifyClean = false
	} else {
		result.VerifyClean = verify.Remaining == 0
		result.Remaining = verify.Remaining
		result.VerifySummary = verify.Summary
	}
	if onProgress != nil {
		onProgress(dto.ApplyProgress{
			Total: total, Done: result.Applied, Message: result.VerifySummary, Failed: result.Failed > 0,
		})
	}
	return result, nil
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
