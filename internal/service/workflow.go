package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
)

func (s *SyncService) ListRuns(limit int) ([]dto.SyncRun, error) { return s.App.Store.ListRuns(limit) }
func (s *SyncService) GetRun(id string) (*dto.SyncRun, error)    { return s.App.Store.GetRun(id) }
func (s *SyncService) DiagnoseComments(sessionID, side string) ([]dto.CommentRepair, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.App.Engine.DiagnoseComments(ctx, sessionID, side)
}
func (s *SyncService) PlanCommentRepair(req dto.RepairRequest) (*dto.PlanResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.App.Engine.PlanCommentRepair(ctx, req)
}

// Resume always inspects live structure; previously successful SQL is never replayed.
func (s *SyncService) Resume(runID string) (*dto.ResumeResult, error) {
	run, err := s.App.Store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	var res *dto.CompareResult
	if run.Source.SnapshotID != "" {
		res, err = (&SnapshotService{App: s.App}).RestoreCompare(run.Source.SnapshotID, dto.SnapshotTarget{ConnectionID: run.Target.ID, Database: run.Target.Database}, run.Normalize)
	} else {
		if run.Source.ID == "" || run.Target.ID == "" {
			return nil, fmt.Errorf("该历史没有已保存连接，请手动选择连接重新对比")
		}
		src, e := s.App.Store.GetConnection(run.Source.ID, true)
		if e != nil {
			return nil, e
		}
		src.Database = run.Source.Database
		dst, e := s.App.Store.GetConnection(run.Target.ID, true)
		if e != nil {
			return nil, e
		}
		dst.Database = run.Target.Database
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		res, err = s.App.Engine.Compare(ctx, dto.CompareRequest{Source: src, Target: dst, Tables: run.Tables, Normalize: run.Normalize})
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out := &dto.ResumeResult{Compare: res, Mode: run.Mode, ChangeIDs: []string{}, RepairIDs: []string{}}
	if run.Mode == "repair" {
		candidates, err := s.App.Engine.DiagnoseComments(ctx, res.SessionID, "target")
		if err != nil {
			return nil, err
		}
		for _, c := range candidates {
			for _, old := range run.Repairs {
				if c.Table == old.Table && c.Column == old.Column {
					out.RepairIDs = append(out.RepairIDs, c.ID)
					break
				}
			}
		}
		return out, nil
	}
	wanted, whole := map[string]bool{}, map[string]bool{}
	for _, it := range run.Selected {
		wanted[it.Table+"\x00"+it.ObjectType+"\x00"+it.ObjectName] = true
		if it.ObjectType == "table" {
			whole[it.Table] = true
		}
	}
	items := append(append(append([]dto.ChangeItem{}, res.Added...), res.Modified...), res.Dropped...)
	for _, it := range items {
		if whole[it.Table] || wanted[it.Table+"\x00"+it.ObjectType+"\x00"+it.ObjectName] {
			out.ChangeIDs = append(out.ChangeIDs, it.ID)
		}
	}
	return out, nil
}
func (s *SyncService) Export(req dto.ExportRequest) (*dto.ExportFile, error) {
	var plan *dto.PlanResult
	var run *dto.SyncRun
	var database string
	var sourceConnection, targetConnection dto.ConnConfig
	if req.RunID != "" {
		var err error
		run, err = s.App.Store.GetRun(req.RunID)
		if err != nil {
			return nil, err
		}
		database = run.Target.Database
		sourceConnection, targetConnection = run.Source, run.Target
		plan = &dto.PlanResult{Snapshots: run.Snapshots, Mode: run.Mode}
		for _, o := range run.Outcomes {
			plan.Statements = append(plan.Statements, o.Statement)
		}
	} else {
		sess, ok := s.App.Engine.Sessions.Get(req.SessionID)
		if !ok {
			return nil, fmt.Errorf("请先生成 SQL")
		}
		plan = s.App.Engine.SessionPlan(req.SessionID)
		if plan == nil {
			return nil, fmt.Errorf("请先生成 SQL")
		}
		database = sess.Target.Config.Database
		sourceConnection, targetConnection = sess.Source.Config, sess.Target.Config
	}
	label := func(c dto.ConnConfig) string {
		return strings.NewReplacer("\n", " ", "\r", " ").Replace(fmt.Sprintf("%s (%s:%d) / %s", c.Name, c.Host, c.Port, c.Database))
	}
	out := &dto.ExportFile{}
	switch req.Kind {
	case "sql":
		out.Filename = "dbcompare-sync-" + database + ".sql"
		out.Content = "-- 实际执行库：" + label(targetConnection) + "\n" + engine.SQLFile(plan.Statements)
		if source := plan.SourceRepairPlan; source != nil {
			targetSQL := out.Content
			out.Content = "-- 源库注释修复（先执行）：" + label(sourceConnection) + "\n" + engine.SQLFile(source.Statements)
			if len(plan.Statements) > 0 {
				out.Content += "\n-- 目标库同步及注释修复\n" + targetSQL
			}
		}
	case "snapshot":
		out.Filename = "dbcompare-before-" + database + ".sql"
		out.Content = engine.SnapshotSQL(plan.Snapshots, database)
		if source := plan.SourceRepairPlan; source != nil {
			out.Content = "-- 源库修复前结构：" + label(sourceConnection) + "\n" + engine.SnapshotSQL(source.Snapshots, source.TargetDatabase) + "\n-- 目标库同步前结构：" + label(targetConnection) + "\n" + out.Content
		}
	case "report":
		if run == nil {
			return nil, fmt.Errorf("请选择执行历史")
		}
		b, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return nil, err
		}
		out.Filename = "dbcompare-report-" + run.ID + ".json"
		out.Content = string(b)
	case "reverse":
		stmts := []dto.PlannedStatement{}
		all := []dto.PlannedStatement{}
		if source := plan.SourceRepairPlan; source != nil {
			for _, st := range source.Statements {
				st.Comment = "[源库 " + label(sourceConnection) + "] " + st.Comment
				all = append(all, st)
			}
		}
		for _, st := range plan.Statements {
			st.Comment = "[实际执行库 " + label(targetConnection) + "] " + st.Comment
			all = append(all, st)
		}
		for i := len(all) - 1; i >= 0; i-- {
			st := all[i]
			if st.Reverse != "" {
				st.SQL = st.Reverse
				stmts = append(stmts, st)
			}
		}
		out.Filename = "dbcompare-reverse-reference-" + database + ".sql"
		out.Content = "-- 结构恢复参考：必须人工审阅，不能恢复删除的数据，不能直接作为部分失败的回滚。\n" + engine.SQLFile(stmts)
	default:
		return nil, fmt.Errorf("不支持的导出类型")
	}
	if plan.SourceRepairPlan != nil && len(plan.Statements) > 0 {
		out.Content = "-- 本文件包含不同执行目标，请按源库/目标库分组连接对应服务器执行，勿在单一连接中直接执行整个文件。\n" + out.Content
	}
	return out, nil
}
func (s *SyncService) SaveExport(req dto.ExportRequest) (string, error) {
	file, err := s.Export(req)
	if err != nil {
		return "", err
	}
	if s.App.SaveFile == nil {
		return "", fmt.Errorf("当前运行环境不支持保存对话框，请使用文件下载")
	}
	return s.App.SaveFile(file.Filename, file.Content)
}
