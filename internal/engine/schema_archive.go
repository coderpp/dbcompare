package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
)

func ReadArchiveModel(model string) (*schema.Schema, error) {
	var r schema.Realm
	if err := mysql.EvalHCLBytes([]byte(model), &r, nil); err != nil {
		return nil, fmt.Errorf("读取快照结构模型: %w", err)
	}
	if len(r.Schemas) != 1 {
		return nil, fmt.Errorf("快照必须包含一个数据库")
	}
	// HCL decoding leaves Raw empty. Visibility filtering uses it to compare
	// columns, so restore it or real type/length changes can be pruned as noise.
	for _, table := range r.Schemas[0].Tables {
		for _, column := range table.Columns {
			raw, err := mysql.FormatType(column.Type.Type)
			if err != nil {
				return nil, fmt.Errorf("读取快照字段类型 %s.%s: %w", table.Name, column.Name, err)
			}
			if typ, ok := column.Type.Type.(*schema.StringType); ok && typ.T == mysql.TypeChar && typ.Size == 0 {
				raw = "char(0)"
			}
			column.Type.Raw = raw
		}
	}
	return r.Schemas[0], nil
}
func CaptureArchive(ctx context.Context, cfg dto.ConnConfig) (dto.SchemaArchive, error) {
	a := dto.SchemaArchive{Format: 1, Database: cfg.Database, Warnings: []string{"仅采集当前账户可见的基础表结构；不含业务数据、视图、触发器、存储过程和事件。"}}
	c, err := Open(ctx, cfg)
	if err != nil {
		return a, err
	}
	defer c.Close()
	a.Server = c.Info
	names, views, err := archiveTableNames(ctx, c)
	if err != nil {
		return a, err
	}
	before, err := snapshotsFor(ctx, c, names)
	if err != nil {
		return a, err
	}
	model, err := c.Inspect(ctx, nil)
	if err != nil {
		return a, err
	}
	// Marshal before normalizing: capture options never discard structure.
	hcl, err := marshalArchiveModel(model)
	if err != nil {
		return a, err
	}
	a.Model = string(hcl)
	after, err := snapshotsFor(ctx, c, names)
	if err != nil {
		return a, err
	}
	names2, _, err := archiveTableNames(ctx, c)
	if err != nil {
		return a, err
	}
	if strings.Join(names, "\x00") != strings.Join(names2, "\x00") || !sameSnapshots(before, after) {
		return a, fmt.Errorf("采集期间结构发生变化，请重新采集")
	}
	if len(model.Tables) != len(names) {
		return a, fmt.Errorf("读取的结构模型与基础表数量不一致，未保存快照")
	}
	for _, t := range after {
		if !t.Exists {
			return a, fmt.Errorf("采集期间表 %s 已消失", t.Table)
		}
	}
	secondModel, err := c.Inspect(ctx, nil)
	if err != nil {
		return a, err
	}
	secondHCL, err := marshalArchiveModel(secondModel)
	if err != nil {
		return a, err
	}
	firstCheck, err := archiveModelCheck(a.Model)
	if err != nil {
		return a, err
	}
	secondCheck, err := archiveModelCheck(string(secondHCL))
	if err != nil {
		return a, err
	}
	if firstCheck != secondCheck {
		return a, fmt.Errorf("采集期间结构模型发生变化，请重新采集")
	}

	a.Tables = after
	if views > 0 {
		a.Warnings = append(a.Warnings, fmt.Sprintf("检测到 %d 个视图，未纳入快照。", views))
	}
	for _, t := range after {
		if strings.Contains(t.DDL, "PARTITION BY") {
			a.Warnings = append(a.Warnings, "表 "+t.Table+" 的分区仅保留原始 DDL，结构模型对比不覆盖分区。")
		}
	}
	return a, nil
}

// archiveModelCheck uses the same HCL round-trip on both inspections. Comparing
// a decoded model to a live one produces false changes for inherited charset
// attributes (notably MySQL 5.7 utf8 aliases). Keep the original archive intact.
func archiveModelCheck(model string) (string, error) {
	s, err := ReadArchiveModel(model)
	if err != nil {
		return "", err
	}
	Normalize(s, dto.NormalizeOptions{IgnoreAutoIncrement: true, CompareRowFormat: true})
	// Object enumeration order is not structure. Column and index-part order is.
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].Name < s.Tables[j].Name })
	for _, t := range s.Tables {
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
		sort.Slice(t.ForeignKeys, func(i, j int) bool { return t.ForeignKeys[i].Symbol < t.ForeignKeys[j].Symbol })
	}
	raw, err := marshalArchiveModel(s)
	return string(raw), err
}

// SameArchiveStructure ignores server metadata and advancing row counters, but
// checks both the model and raw DDL so partitions and other unmodeled options
// still produce a new version.
func SameArchiveStructure(a, b dto.SchemaArchive) (bool, error) {
	if a.Format != b.Format || a.Database != b.Database || !sameSnapshots(a.Tables, b.Tables) {
		return false, nil
	}
	first, err := archiveModelCheck(a.Model)
	if err != nil {
		return false, err
	}
	second, err := archiveModelCheck(b.Model)
	return first == second, err
}

func archiveTableNames(ctx context.Context, c *Conn) ([]string, int, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT TABLE_NAME,TABLE_TYPE FROM information_schema.tables WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME`, c.Config.Database)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	names := []string{}
	views := 0
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			return nil, 0, err
		}
		if kind == "BASE TABLE" {
			names = append(names, name)
		} else {
			views++
		}
	}
	sort.Strings(names)
	return names, views, rows.Err()
}

// DiffArchives compares semantic models offline. The SQL is a review artifact, never executed here.
func DiffArchives(ctx context.Context, before, after dto.SchemaArchive, opts dto.NormalizeOptions) (dto.SnapshotDiff, error) {
	out := dto.SnapshotDiff{Differences: []dto.SnapshotDifference{}, Warnings: append(append([]string{}, before.Warnings...), after.Warnings...)}
	a, err := ReadArchiveModel(before.Model)
	if err != nil {
		return out, err
	}
	b, err := ReadArchiveModel(after.Model)
	if err != nil {
		return out, err
	}
	Normalize(a, opts)
	Normalize(b, opts)
	a.Name = b.Name
	changes, err := mysql.DefaultDiff.SchemaDiff(a, b)
	if err != nil {
		return out, err
	}
	changes = visibleSchemaChanges(changes, a, b, opts)
	byTable := map[string][]schema.Change{}
	for _, ch := range changes {
		byTable[changeTable(ch)] = append(byTable[changeTable(ch)], ch)
	}
	keys := []string{}
	for k := range byTable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ddl := func(a dto.SchemaArchive, name string) string {
		for _, t := range a.Tables {
			if t.Table == name {
				return t.DDL
			}
		}
		return ""
	}
	for _, name := range keys {
		d := dto.SnapshotDifference{Table: name, Kind: "modify", Before: ddl(before, name), After: ddl(after, name), Items: []dto.ChangeItem{}}
		for _, ch := range byTable[name] {
			it := changeToItem(ch, len(d.Items), "", ParseVersion(after.Server.Version))
			if _, ok := ch.(*schema.AddTable); ok {
				d.Kind = "add"
			}
			if _, ok := ch.(*schema.DropTable); ok {
				d.Kind = "drop"
			}
			d.Items = append(d.Items, it)
			if mt, ok := ch.(*schema.ModifyTable); ok {
				for _, sub := range mt.Changes {
					it = changeToItem(sub, len(d.Items), "", ParseVersion(after.Server.Version))
					it.Table = name
					d.Items = append(d.Items, it)
				}
			}
		}
		out.Differences = append(out.Differences, d)
	}
	plan, err := mysql.DefaultPlan.PlanChanges(ctx, "snapshot-review", changes)
	if err != nil {
		out.Warnings = append(out.Warnings, "离线 SQL 预览不可用: "+err.Error())
	} else {
		out.SQL = "-- 历史结构差异，仅供审阅；离线 SQL 使用 MySQL 8.0 方言。实际同步需重新读取目标库并生成计划。\n"
		for _, c := range plan.Changes {
			out.SQL += c.Cmd + ";\n\n"
		}
	}
	if w := CheckLowerCaseConsistency(before.Server, after.Server); w != "" {
		out.Warnings = append(out.Warnings, w)
	}
	seenWarnings := map[string]bool{}
	warnings := []string{}
	for _, w := range out.Warnings {
		if !seenWarnings[w] {
			warnings = append(warnings, w)
			seenWarnings[w] = true
		}
	}
	out.Warnings = warnings
	return out, nil
}
func (e *Engine) CompareArchive(ctx context.Context, archive dto.SchemaArchive, snapshotID string, target dto.ConnConfig, opts dto.NormalizeOptions) (*dto.CompareResult, error) {
	dst, err := Open(ctx, target)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = dst.Close()
		}
	}()
	a, err := ReadArchiveModel(archive.Model)
	if err != nil {
		return nil, err
	}
	b, err := dst.Inspect(ctx, nil)
	if err != nil {
		return nil, err
	}
	sourceRepairs := detectedCommentRepairs(a, opts.AutoRepairComments)
	targetRepairs := detectedCommentRepairs(b, opts.AutoRepairComments)
	Normalize(a, opts)
	Normalize(b, opts)
	a.Name = b.Name
	changes, err := dst.Driver.SchemaDiff(b, a)
	if err != nil {
		return nil, err
	}
	changes = visibleSchemaChanges(changes, b, a, opts)
	items, warnings := classify(ctx, dst.Driver, changes, ParseVersion(archive.Server.Version), dst.SemVer, a)
	warnings = append(warnings, archive.Warnings...)
	src := &Conn{Config: dto.ConnConfig{Name: "快照 " + snapshotID, Database: archive.Database, SnapshotID: snapshotID}, Info: archive.Server, SemVer: ParseVersion(archive.Server.Version), ArchiveModel: archive.Model}
	sess := &Session{SourceRepairs: sourceRepairs, TargetRepairs: targetRepairs, ID: uuid.NewString(), Source: src, Target: dst, SourceSch: a, TargetSch: b, Changes: changes, Items: items, Normalize: opts}
	e.Sessions.Put(sess)
	ok = true
	return &dto.CompareResult{SourceRepairs: sourceRepairs, TargetRepairs: targetRepairs, SourceInfo: src.Info, TargetInfo: dst.Info, Added: filterKind(items, dto.KindAdd), Modified: filterKind(items, dto.KindModify), Dropped: filterKind(items, dto.KindDrop), Warnings: warnings, SessionID: sess.ID}, nil
}

func ValidateArchive(a dto.SchemaArchive, hash string) error {
	if a.Format != 1 || a.Database == "" || len(a.Tables) > 10000 {
		return fmt.Errorf("不支持的快照版本或数据库结构无效")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("快照校验值不匹配")
	}
	s, err := ReadArchiveModel(a.Model)
	if err != nil {
		return err
	}
	if s.Name != a.Database || len(s.Tables) != len(a.Tables) {
		return fmt.Errorf("结构模型与快照数据库不一致")
	}
	names := map[string]bool{}
	for _, t := range s.Tables {
		names[t.Name] = true
	}
	for _, t := range a.Tables {
		sum = sha256.Sum256([]byte(t.DDL))
		if !names[t.Table] || !t.Exists || t.DDL == "" || hex.EncodeToString(sum[:]) != t.SHA256 {
			return fmt.Errorf("表 %s 的快照校验失败", t.Table)
		}
		delete(names, t.Table)
	}
	return nil
}
