package engine

import (
	"context"
	"fmt"
	"time"

	"ariga.io/atlas/sql/schema"
	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
)

type RunPersister func(*dto.SyncRun) error

func (e *Engine) ApplyTracked(ctx context.Context, req dto.ApplyRequest, onProgress ProgressFunc, audit func(dto.AuditEntry), persist RunPersister) (*dto.ApplyResult, error) {
	sess, ok := e.Sessions.Get(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("对比会话已失效，请重新对比")
	}
	sess.mu.Lock()
	plan := sess.Plan
	if sess.applying {
		sess.mu.Unlock()
		return nil, fmt.Errorf("该会话正在同步")
	}
	if plan == nil || len(plan.Statements) == 0 {
		sess.mu.Unlock()
		return nil, fmt.Errorf("请先生成 SQL")
	}
	if req.PlanID != "" && req.PlanID != plan.ID {
		sess.mu.Unlock()
		return nil, fmt.Errorf("SQL 计划已变化，请重新生成")
	}
	if plan.Blocked && !req.DryRun {
		sess.mu.Unlock()
		return nil, fmt.Errorf("计划被预检阻止")
	}
	sess.applying = true
	sess.mu.Unlock()
	defer func() { sess.mu.Lock(); sess.applying = false; sess.mu.Unlock() }()
	// Partial execution is expressed by selecting differences and replanning, not
	// by dropping dependency statements from the already ordered SQL plan.
	if len(req.StatementIDs) > 0 {
		all := map[string]bool{}
		for _, id := range req.StatementIDs {
			all[id] = true
		}
		if len(all) != len(plan.Statements) {
			return nil, fmt.Errorf("请按差异选择并重新生成完整计划，不支持截取依赖 SQL")
		}
		for _, st := range plan.Statements {
			if !all[st.ID] {
				return nil, fmt.Errorf("SQL 选择已失效，请重新生成计划")
			}
		}
	}
	if req.DryRun {
		return &dto.ApplyResult{VerifySummary: "仅预览，未执行 SQL"}, nil
	}
	current, err := snapshotsFor(ctx, sess.Target, snapshotTables(plan.Snapshots))
	if err != nil {
		return nil, err
	}
	if !sameSnapshots(current, plan.Snapshots) {
		return nil, fmt.Errorf("目标结构自生成 SQL 后已变化，请重新对比生成，未执行任何语句")
	}
	run := &dto.SyncRun{Tables: append([]string(nil), sess.Tables...), ID: uuid.NewString(), SessionID: sess.ID, PlanID: plan.ID, StartedAt: time.Now().Format(time.RFC3339Nano), Status: "running", Source: sess.Source.Config, Target: sess.Target.Config, Normalize: sess.Normalize, AllowDrop: plan.AllowDrop, AllowUnsafe: plan.AllowUnsafe, Mode: plan.Mode, Selected: plan.Selected, Snapshots: current, Repairs: plan.Repairs, Outcomes: []dto.StatementOutcome{}, Remaining: []dto.RemainingDifference{}}
	run.Source.Password = ""
	run.Source.Params = ""
	run.Target.Password = ""
	run.Target.Params = ""
	for _, st := range plan.Statements {
		run.Outcomes = append(run.Outcomes, dto.StatementOutcome{Statement: st, Status: "pending"})
	}
	save := func() error {
		if persist != nil {
			return persist(run)
		}
		return nil
	}
	if err := save(); err != nil {
		return nil, fmt.Errorf("无法保存同步前快照，未执行 SQL: %w", err)
	}
	finish := func(err error) {
		if err != nil {
			run.Error = err.Error()
		}
		for i := range run.Outcomes {
			if run.Outcomes[i].Status == "pending" {
				run.Outcomes[i].Status = "not_executed"
			}
		}
		run.FinishedAt = time.Now().Format(time.RFC3339Nano)
		if run.Error != "" || run.Failed > 0 {
			run.Status = "failed"
		} else if run.VerificationError != "" {
			run.Status = "verification_failed"
		} else if !run.SelectionComplete {
			run.Status = "incomplete"
		} else {
			run.Status = "success"
		}
		if err := save(); err != nil {
			run.Error += "；保存最终报告失败: " + err.Error()
			run.Status = "report_error"
		}
	}
	conn, err := sess.Target.DB.Conn(ctx)
	if err != nil {
		finish(err)
		return resultForRun(run), nil
	}
	defer conn.Close()
	var oldMode string
	if err = conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&oldMode); err != nil {
		finish(err)
		return resultForRun(run), nil
	}
	if _, err = conn.ExecContext(ctx, "SET SESSION sql_mode='STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION'"); err != nil {
		finish(err)
		return resultForRun(run), nil
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(restoreCtx, "SET SESSION sql_mode=?", oldMode)
	}()
	var executionErr error
	for i := range run.Outcomes {
		outcome := &run.Outcomes[i]
		outcome.Status = "running"
		if err := save(); err != nil {
			outcome.Status = "not_executed"
			executionErr = fmt.Errorf("无法保存执行记录，已停止后续 SQL: %w", err)
			break
		}
		if onProgress != nil {
			onProgress(dto.ApplyProgress{Total: len(run.Outcomes), Done: run.Applied, CurrentSQL: outcome.Statement.SQL, Message: fmt.Sprintf("执行 %d/%d", i+1, len(run.Outcomes))})
		}
		start := time.Now()
		_, err := conn.ExecContext(ctx, outcome.Statement.SQL)
		outcome.DurationMs = time.Since(start).Milliseconds()
		entry := dto.AuditEntry{ID: uuid.NewString(), At: time.Now().Format(time.RFC3339Nano), Action: "apply_ddl", SourceDSN: MaskDSN(sess.Source.Config), TargetDSN: MaskDSN(sess.Target.Config), SQL: outcome.Statement.SQL, OK: err == nil, DurationMs: outcome.DurationMs}
		if err != nil {
			outcome.Status = "failed"
			outcome.Error = err.Error()
			entry.Error = err.Error()
			run.Failed++
			executionErr = err
		} else {
			outcome.Status = "success"
			run.Applied++
		}
		if audit != nil {
			audit(entry)
		}
		if err := save(); err != nil {
			executionErr = fmt.Errorf("保存执行结果失败，已停止后续 SQL: %w", err)
		}
		if executionErr != nil {
			break
		}
	}
	// Always re-read both sides after partial failure. Never infer rollback or
	// success merely from the number of successfully returned Exec calls.
	verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if plan.Mode == "repair" {
		err = e.verifyRepairs(verifyCtx, sess, run)
	} else {
		err = e.verifyRun(verifyCtx, sess, run)
		if err == nil && len(plan.Repairs) > 0 {
			err = verifyAutomaticRepairs(verifyCtx, sess, run)
		}
	}
	if err != nil {
		run.VerificationError = err.Error()
	}
	finish(executionErr)
	result := resultForRun(run)
	if onProgress != nil {
		onProgress(dto.ApplyProgress{Total: len(run.Outcomes), Done: run.Applied, Message: result.VerifySummary, Failed: run.Status != "success"})
	}
	return result, nil
}
func resultForRun(run *dto.SyncRun) *dto.ApplyResult {
	errors := []string{}
	if run.Error != "" {
		errors = append(errors, run.Error)
	}
	remaining := 0
	for _, d := range run.Remaining {
		if d.Category == "selected" {
			remaining++
		}
	}
	summary := fmt.Sprintf("成功 %d，失败 %d；所选变更剩余 %d 项，全库剩余 %d 项", run.Applied, run.Failed, remaining, len(run.Remaining))
	if run.VerificationError != "" {
		summary += "；复查失败: " + run.VerificationError
	}
	return &dto.ApplyResult{Run: run, Applied: run.Applied, Failed: run.Failed, Errors: errors, VerifyClean: run.SelectionComplete && run.VerificationError == "" && run.Status == "success", VerifySummary: summary, Remaining: remaining}
}
func (e *Engine) verifyRun(ctx context.Context, sess *Session, run *dto.SyncRun) error {
	a, err := sess.Source.Inspect(ctx, sess.Tables)
	if err != nil {
		return err
	}
	b, err := sess.Target.Inspect(ctx, sess.Tables)
	if err != nil {
		return err
	}
	Normalize(a, sess.Normalize)
	Normalize(b, sess.Normalize)
	a.Name = b.Name
	changes, err := sess.Target.Driver.SchemaDiff(b, a)
	if err != nil {
		return err
	}
	changes = visibleSchemaChanges(changes, b, a, sess.Normalize)
	wanted := map[string]bool{}
	whole := map[string]bool{}
	for _, it := range run.Selected {
		wanted[objectKey(it)] = true
		if it.ObjectType == "table" {
			whole[it.Table] = true
		}
	}
	run.Remaining = []dto.RemainingDifference{}
	run.SelectionComplete = true
	add := func(ch schema.Change, table string) {
		it := changeToItem(ch, len(run.Remaining), "", sess.Target.SemVer)
		it.Table = table
		category := "unselected"
		if wanted[objectKey(it)] || whole[table] {
			category = "selected"
			run.SelectionComplete = false
		} else if isDropChange(ch) && !run.AllowDrop {
			category = "skipped_drop"
		}
		run.Remaining = append(run.Remaining, dto.RemainingDifference{Item: it, Category: category})
	}
	for _, ch := range changes {
		table := changeTable(ch)
		if mt, ok := ch.(*schema.ModifyTable); ok {
			for _, sub := range mt.Changes {
				add(sub, table)
			}
		} else {
			add(ch, table)
		}
	}
	return nil
}
