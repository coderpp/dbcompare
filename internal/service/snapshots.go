package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
)

type SnapshotService struct{ App *AppServices }

func NextSnapshotTime(t dto.SnapshotTask, now time.Time) (time.Time, error) {
	switch t.Schedule {
	case "interval":
		if t.IntervalHours < 1 || t.IntervalHours > 8760 {
			return time.Time{}, fmt.Errorf("间隔需为 1–8760 小时")
		}
		return now.Add(time.Duration(t.IntervalHours) * time.Hour), nil
	case "daily", "weekly":
		clock, err := time.Parse("15:04", t.Time)
		if err != nil {
			return time.Time{}, fmt.Errorf("时间格式必须为 HH:mm")
		}
		if t.Schedule == "weekly" && len(t.Weekdays) == 0 {
			return time.Time{}, fmt.Errorf("请选择星期")
		}
		for _, d := range t.Weekdays {
			if d < 0 || d > 6 {
				return time.Time{}, fmt.Errorf("星期无效")
			}
		}
		for i := 0; i <= 8; i++ {
			day := now.AddDate(0, 0, i)
			at := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
			if !at.After(now) {
				continue
			}
			if t.Schedule == "daily" {
				return at, nil
			}
			for _, d := range t.Weekdays {
				if int(at.Weekday()) == d {
					return at, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("无效的执行周期")
}
func (s *SnapshotService) Overview() (dto.SnapshotOverview, error) {
	out := dto.SnapshotOverview{Background: SnapshotBackgroundEnabled(s.App.Store.ConfigDir())}
	var err error
	if out.Tasks, err = s.App.Store.SnapshotTasks(); err != nil {
		return out, err
	}
	if out.Snapshots, err = s.App.Store.SnapshotRecords(); err != nil {
		return out, err
	}
	out.Runs, err = s.App.Store.SnapshotRuns()
	return out, err
}
func (s *SnapshotService) validateTargets(targets []dto.SnapshotTarget) error {
	if len(targets) == 0 || len(targets) > 100 {
		return fmt.Errorf("请选择 1–100 个数据库")
	}
	seen := map[string]bool{}
	for _, t := range targets {
		if strings.TrimSpace(t.Database) == "" || strings.ContainsRune(t.Database, 0) {
			return fmt.Errorf("数据库名无效")
		}
		key := t.ConnectionID + "\x00" + t.Database
		if seen[key] {
			return fmt.Errorf("同一数据库不可重复选择")
		}
		seen[key] = true
		if _, err := s.App.Store.GetConnection(t.ConnectionID, false); err != nil {
			return fmt.Errorf("连接不存在，请重新选择")
		}
	}
	return nil
}
func (s *SnapshotService) SaveTask(t dto.SnapshotTask) (dto.SnapshotTask, error) {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 300 {
		return t, fmt.Errorf("请输入任务名称（最多 300 字节）")
	}
	if err := s.validateTargets(t.Targets); err != nil {
		return t, err
	}
	if t.RetentionDays < 1 || t.RetentionDays > 36500 {
		return t, fmt.Errorf("保留天数需为 1–36500")
	}
	next, err := NextSnapshotTime(t, time.Now())
	if err != nil {
		return t, err
	}
	t.NextRun = next.UTC().Format(time.RFC3339Nano)
	if t.Background && t.Enabled && !SnapshotBackgroundEnabled(s.App.Store.ConfigDir()) {
		return t, fmt.Errorf("请先启用 macOS 后台调度，或选择仅应用运行时执行")
	}
	return s.App.Store.PutSnapshotTask(t)
}
func (s *SnapshotService) DeleteTask(id string) error { return s.App.Store.DeleteSnapshotTask(id) }
func (s *SnapshotService) Get(id string) (dto.SnapshotDocument, error) {
	return s.App.Store.Snapshot(id)
}
func (s *SnapshotService) Annotate(id, note string, pinned bool) error {
	if len(note) > 4000 {
		return fmt.Errorf("备注过长")
	}
	return s.App.Store.AnnotateSnapshot(id, note, pinned)
}
func (s *SnapshotService) Delete(id string) error { return s.App.Store.DeleteSnapshot(id) }
func (s *SnapshotService) hydrated(t dto.SnapshotTarget) (dto.ConnConfig, error) {
	c, err := s.App.Store.GetConnection(t.ConnectionID, false)
	if err != nil {
		return c, fmt.Errorf("连接已删除，请修改任务")
	}
	c.Password, err = s.App.Store.PasswordForExport(c.ID)
	if err != nil {
		return c, fmt.Errorf("无法读取连接密码，请解锁钥匙串或重新保存连接")
	}
	c.Database = t.Database
	return c, nil
}
func (s *SnapshotService) Capture(targets []dto.SnapshotTarget) (dto.SnapshotRun, error) {
	if err := s.validateTargets(targets); err != nil {
		return dto.SnapshotRun{}, err
	}
	return s.captureTask(dto.SnapshotTask{Name: "手动采集", Targets: targets}, false)
}
func (s *SnapshotService) captureTask(t dto.SnapshotTask, catchUp bool) (dto.SnapshotRun, error) {
	r := dto.SnapshotRun{ID: uuid.NewString(), TaskID: t.ID, TaskName: t.Name, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), CatchUp: catchUp, Status: "running", Results: []dto.SnapshotOutcome{}}
	if err := s.App.Store.PutSnapshotRun(r); err != nil {
		return r, err
	}
	for _, target := range t.Targets {
		if t.ID != "" {
			if err := s.App.Store.RenewSnapshotTask(t.ID); err != nil {
				r.Status = "interrupted"
				r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
				_ = s.App.Store.PutSnapshotRun(r)
				return r, err
			}
		}
		start := time.Now()
		result := dto.SnapshotOutcome{Target: target}
		cfg, err := s.hydrated(target)
		result.ConnectionName = cfg.Name
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			var a dto.SchemaArchive
			a, err = engine.CaptureArchive(ctx, cfg)
			cancel()
			if err == nil {
				var rec dto.SnapshotRecord
				rec, err = s.App.Store.PutSnapshotIfChanged(dto.SnapshotRecord{TaskID: t.ID, ConnectionID: target.ConnectionID, ConnectionName: cfg.Name, Database: target.Database, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}, a, engine.SameArchiveStructure)
				if err == nil {
					result.SnapshotID = rec.ID
					result.Unchanged = rec.Unchanged
				}
			}
		}
		if err != nil {
			result.Error = redactSnapshotError(err, cfg)
		}
		result.DurationMS = time.Since(start).Milliseconds()
		r.Results = append(r.Results, result)
		if err := s.App.Store.PutSnapshotRun(r); err != nil {
			return r, err
		}
	}
	r.Status = "success"
	failed := 0
	for _, x := range r.Results {
		if x.Error != "" {
			failed++
		}
	}
	if failed > 0 {
		r.Status = "partial"
	}
	if failed == len(r.Results) {
		r.Status = "failed"
	}
	r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.App.Store.PutSnapshotRun(r); err != nil {
		return r, err
	}
	if t.ID != "" {
		if err := s.App.Store.PruneSnapshots(t.ID, t.RetentionDays); err != nil {
			return r, fmt.Errorf("采集已完成，清理过期快照失败: %w", err)
		}
	}
	s.App.emitter().Emit("snapshot:changed", r.ID)
	return r, nil
}
func redactSnapshotError(err error, c dto.ConnConfig) string {
	v := err.Error()
	if c.Password != "" {
		v = strings.ReplaceAll(v, c.Password, "***")
	}
	if c.Params != "" {
		v = strings.ReplaceAll(v, c.Params, "***")
	}
	return v
}
func (s *SnapshotService) RunTask(id string) (dto.SnapshotRun, error) {
	tasks, err := s.App.Store.SnapshotTasks()
	if err != nil {
		return dto.SnapshotRun{}, err
	}
	for _, t := range tasks {
		if t.ID == id {
			claimed, err := s.App.Store.ClaimSnapshotTask(id, "", false)
			if err != nil {
				return dto.SnapshotRun{}, err
			}
			if !claimed {
				return dto.SnapshotRun{}, fmt.Errorf("任务正在执行，请稍后查看结果")
			}
			defer s.App.Store.ReleaseSnapshotTask(id)
			return s.captureTask(t, false)
		}
	}
	return dto.SnapshotRun{}, fmt.Errorf("任务不存在")
}
func (s *SnapshotService) RetryRun(id string) (dto.SnapshotRun, error) {
	runs, err := s.App.Store.SnapshotRuns()
	if err != nil {
		return dto.SnapshotRun{}, err
	}
	for _, r := range runs {
		if r.ID == id {
			targets := []dto.SnapshotTarget{}
			for _, x := range r.Results {
				if x.Error != "" {
					targets = append(targets, x.Target)
				}
			}
			if len(targets) == 0 {
				return dto.SnapshotRun{}, fmt.Errorf("没有已记录的失败项；中断任务请使用立即执行")
			}
			return s.captureTask(dto.SnapshotTask{Name: r.TaskName + "（重试失败项）", Targets: targets}, false)
		}
	}
	return dto.SnapshotRun{}, fmt.Errorf("执行记录不存在")
}

// Tick is used by both the UI timer and the short-lived launchd worker.
func (s *SnapshotService) tick(backgroundOnly bool) error {
	tasks, err := s.App.Store.SnapshotTasks()
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if !t.Enabled || (backgroundOnly && !t.Background) {
			continue
		}
		due, err := time.Parse(time.RFC3339Nano, t.NextRun)
		if err != nil {
			continue
		}
		now := time.Now()
		if due.After(now) {
			continue
		}
		next, err := NextSnapshotTime(t, now)
		if err != nil {
			continue
		}
		claimed, err := s.App.Store.ClaimSnapshotTask(t.ID, next.UTC().Format(time.RFC3339Nano), true)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		_, runErr := s.captureTask(t, now.Sub(due) > 2*time.Minute)
		s.App.Store.ReleaseSnapshotTask(t.ID)
		if runErr != nil {
			return runErr
		}
	}
	return nil
}
func StartSnapshotScheduler(ctx context.Context, s *SnapshotService) {
	go func() {
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			_ = s.tick(false)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}
func (s *SnapshotService) Compare(req dto.SnapshotDiffRequest) (dto.SnapshotDiff, error) {
	before, err := s.Get(req.BeforeID)
	if err != nil {
		return dto.SnapshotDiff{}, err
	}
	var after dto.SnapshotDocument
	if req.Live != nil {
		cfg, err := s.hydrated(*req.Live)
		if err != nil {
			return dto.SnapshotDiff{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		after.Archive, err = engine.CaptureArchive(ctx, cfg)
		if err != nil {
			return dto.SnapshotDiff{}, fmt.Errorf("读取当前库: %s", redactSnapshotError(err, cfg))
		}
		after.Record = dto.SnapshotRecord{Database: cfg.Database, ConnectionName: cfg.Name, CapturedAt: "当前"}
	} else {
		after, err = s.Get(req.AfterID)
		if err != nil {
			return dto.SnapshotDiff{}, err
		}
	}
	out, err := engine.DiffArchives(context.Background(), before.Archive, after.Archive, req.Normalize)
	out.Before = before.Record.ConnectionName + " / " + before.Record.Database + " · " + before.Record.CapturedAt
	out.After = after.Record.ConnectionName + " / " + after.Record.Database + " · " + after.Record.CapturedAt
	return out, err
}
func (s *SnapshotService) RestoreCompare(id string, target dto.SnapshotTarget, opts dto.NormalizeOptions) (*dto.CompareResult, error) {
	doc, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	cfg, err := s.hydrated(target)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.App.Engine.CompareArchive(ctx, doc.Archive, id, cfg, opts)
}
func (s *SnapshotService) Export(ids []string, kind string) (dto.SnapshotExport, error) {
	out := dto.SnapshotExport{Filename: "dbcompare-snapshots-" + time.Now().Format("20060102-150405") + ".json"}
	if len(ids) == 0 || len(ids) > 100 {
		return out, fmt.Errorf("请选择 1–100 份快照")
	}
	docs := []dto.SnapshotDocument{}
	for _, id := range ids {
		doc, err := s.Get(id)
		if err != nil {
			return out, err
		}
		docs = append(docs, doc)
	}
	if kind == "sql" {
		out.Filename = strings.TrimSuffix(out.Filename, ".json") + ".sql"
		for _, d := range docs {
			out.Content += "-- 结构快照 " + d.Record.CapturedAt + "\n" + engine.SnapshotSQL(d.Archive.Tables, d.Archive.Database) + "\n"
		}
		return out, nil
	}
	if kind != "json" {
		return out, fmt.Errorf("不支持的导出格式")
	}
	raw, err := json.MarshalIndent(struct {
		Format    string                 `json:"format"`
		Version   int                    `json:"version"`
		Snapshots []dto.SnapshotDocument `json:"snapshots"`
	}{"dbcompare-schema-archives", 1, docs}, "", "  ")
	out.Content = string(raw)
	if len(raw) > 64<<20 {
		return out, fmt.Errorf("快照包超过 64 MB，请减少选择数量")
	}
	return out, err
}
func (s *SnapshotService) SaveExport(ids []string, kind string) (string, error) {
	f, err := s.Export(ids, kind)
	if err != nil {
		return "", err
	}
	if s.App.SaveFile == nil {
		return "", fmt.Errorf("不支持保存对话框")
	}
	return s.App.SaveFile(f.Filename, f.Content)
}
func (s *SnapshotService) ReadImportFile() (string, error) {
	if s.App.OpenArchive == nil {
		return "", fmt.Errorf("不支持打开对话框")
	}
	return s.App.OpenArchive()
}
func (s *SnapshotService) Import(content string) (int, error) {
	if len(content) > 64<<20 {
		return 0, fmt.Errorf("快照包不能超过 64 MB")
	}
	var pack struct {
		Format    string                 `json:"format"`
		Version   int                    `json:"version"`
		Snapshots []dto.SnapshotDocument `json:"snapshots"`
	}
	if err := json.Unmarshal([]byte(content), &pack); err != nil {
		return 0, fmt.Errorf("快照包格式无效")
	}
	if pack.Format != "dbcompare-schema-archives" || pack.Version != 1 || len(pack.Snapshots) == 0 || len(pack.Snapshots) > 100 {
		return 0, fmt.Errorf("不支持的快照包或数量超限")
	}
	for _, d := range pack.Snapshots {
		if d.Record.Database != d.Archive.Database {
			return 0, fmt.Errorf("快照记录与结构模型的数据库名不一致")
		}
		if _, err := time.Parse(time.RFC3339Nano, d.Record.CapturedAt); err != nil {
			return 0, fmt.Errorf("快照时间无效")
		}
		if len(d.Record.Note) > 4000 {
			return 0, fmt.Errorf("快照备注过长")
		}
		if err := engine.ValidateArchive(d.Archive, d.Record.Hash); err != nil {
			return 0, err
		}
	}
	return s.App.Store.ImportSnapshots(pack.Snapshots)
}
func (s *SnapshotService) SetBackground(enabled bool) error {
	return configureSnapshotBackground(enabled, s.App.Store.ConfigDir())
}

// RunSnapshotWorker handles due background jobs and exits without starting Wails.
func RunSnapshotWorker(s *SnapshotService) error { return s.tick(true) }

func (s *SnapshotService) SaveDiffReport(report dto.SnapshotDiff) (string, error) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	if len(raw) > 64<<20 {
		return "", fmt.Errorf("差异报告超过 64 MB")
	}
	if s.App.SaveFile == nil {
		return "", fmt.Errorf("不支持保存对话框")
	}
	return s.App.SaveFile("结构差异报告-"+time.Now().Format("20060102-150405")+".json", string(raw))
}
