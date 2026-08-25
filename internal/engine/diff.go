package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	"github.com/google/uuid"

	"github.com/pp/db-compare/internal/dto"
)

// Session holds a compare result and planning context in memory.
type Session struct {
	ID         string
	Source     *Conn
	Target     *Conn
	SourceSch  *schema.Schema
	TargetSch  *schema.Schema
	Changes    []schema.Change
	Items      []dto.ChangeItem
	Normalize  dto.NormalizeOptions
	Plan       *dto.PlanResult
	mu         sync.Mutex
}

// SessionStore is an in-memory session registry.
type SessionStore struct {
	mu   sync.RWMutex
	data map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{data: map[string]*Session{}}
}

func (s *SessionStore) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[sess.ID] = sess
}

func (s *SessionStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[id]
	return v, ok
}

func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.data[id]; ok {
		_ = sess.Source.Close()
		_ = sess.Target.Close()
		delete(s.data, id)
	}
}

// Engine orchestrates inspect → normalize → diff → plan → apply.
type Engine struct {
	Sessions *SessionStore
}

func New() *Engine {
	return &Engine{Sessions: NewSessionStore()}
}

// Compare opens both sides, inspects, normalizes, diffs with target driver, classifies.
func (e *Engine) Compare(ctx context.Context, req dto.CompareRequest) (*dto.CompareResult, error) {
	if req.Normalize == (dto.NormalizeOptions{}) {
		req.Normalize = dto.DefaultNormalizeOptions()
	}
	src, err := Open(ctx, req.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	dst, err := Open(ctx, req.Target)
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("target: %w", err)
	}

	srcSch, err := src.Inspect(ctx, req.Tables)
	if err != nil {
		_ = src.Close()
		_ = dst.Close()
		return nil, err
	}
	dstSch, err := dst.Inspect(ctx, req.Tables)
	if err != nil {
		_ = src.Close()
		_ = dst.Close()
		return nil, err
	}

	Normalize(srcSch, req.Normalize)
	Normalize(dstSch, req.Normalize)

	// Align schema names so Atlas doesn't emit spurious rename/move noise.
	srcSch.Name = dstSch.Name

	// Diff desired(source) vs current(target) using TARGET driver → DDL matches target dialect.
	// SchemaDiff(from, to) means changes to transform `from` into `to`.
	// We want target to become like source, so from=target, to=source.
	changes, err := dst.Driver.SchemaDiff(dstSch, srcSch)
	if err != nil {
		_ = src.Close()
		_ = dst.Close()
		return nil, fmt.Errorf("schema diff: %w", err)
	}
	changes = pruneNoopChanges(changes, req.Normalize)
	changes = pruneInvisibleModifies(changes, dstSch, srcSch, req.Normalize)

	items, warnings := classify(ctx, dst.Driver, changes, src.SemVer, dst.SemVer, srcSch)
	if w := CheckLowerCaseConsistency(src.Info, dst.Info); w != "" {
		warnings = append(warnings, w)
	}
	for _, fi := range AnalyzeDowngradeOn(srcSch, dst.SemVer) {
		if fi.Level == DowngradeBlock {
			warnings = append(warnings, "[BLOCK] "+fi.Message)
		} else {
			warnings = append(warnings, fi.Message)
		}
	}
	for _, t := range srcSch.Tables {
		if IsPartitioned(t) {
			warnings = append(warnings, fmt.Sprintf(
				"[UNSUPPORTED] table `%s` is partitioned — PARTITION BY is outside Atlas OSS support; structure without partitions may still sync",
				t.Name,
			))
		}
	}

	sess := &Session{
		ID:        uuid.NewString(),
		Source:    src,
		Target:    dst,
		SourceSch: srcSch,
		TargetSch: dstSch,
		Changes:   changes,
		Items:     items,
		Normalize: req.Normalize,
	}
	e.Sessions.Put(sess)

	return &dto.CompareResult{
		SourceInfo: src.Info,
		TargetInfo: dst.Info,
		Added:      filterKind(items, dto.KindAdd),
		Modified:   filterKind(items, dto.KindModify),
		Dropped:    filterKind(items, dto.KindDrop),
		Warnings:   warnings,
		SessionID:  sess.ID,
	}, nil
}

func filterKind(items []dto.ChangeItem, k dto.ChangeKind) []dto.ChangeItem {
	out := make([]dto.ChangeItem, 0)
	for _, it := range items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

func classify(ctx context.Context, drv migrate.Driver, changes []schema.Change, srcVer, dstVer SemVer, desired *schema.Schema) ([]dto.ChangeItem, []string) {
	_ = srcVer
	_ = desired
	var warnings []string
	items := make([]dto.ChangeItem, 0, len(changes))

	// Plan all changes once to attach SQL previews.
	plan, planErr := drv.PlanChanges(ctx, "preview", changes, func(o *migrate.PlanOptions) {
		o.Indent = "  "
	})
	sqlByIdx := map[int]string{}
	if planErr == nil && plan != nil {
		for i, c := range plan.Changes {
			sqlByIdx[i] = c.Cmd
		}
	} else if planErr != nil {
		warnings = append(warnings, "SQL preview planning failed: "+planErr.Error())
	}

	for i, ch := range changes {
		item := changeToItem(ch, i, sqlByIdx[i], dstVer)
		items = append(items, item)
		// Expand ModifyTable children for finer selection.
		if mt, ok := ch.(*schema.ModifyTable); ok {
			for j, sub := range mt.Changes {
				subItem := changeToItem(sub, i*1000+j+1, "", dstVer)
				subItem.Table = mt.T.Name
				if subItem.Kind == "" {
					subItem.Kind = dto.KindModify
				}
				items = append(items, subItem)
			}
		}
	}
	return items, warnings
}

func changeToItem(ch schema.Change, idx int, sql string, dstVer SemVer) dto.ChangeItem {
	_ = dstVer
	id := fmt.Sprintf("c%d", idx)
	item := dto.ChangeItem{ID: id, Selected: true, SQLPreview: sql, Risk: dto.RiskInfo}

	switch c := ch.(type) {
	case *schema.AddTable:
		item.Kind = dto.KindAdd
		item.Table = c.T.Name
		item.ObjectType = "table"
		item.ObjectName = c.T.Name
		item.Summary = fmt.Sprintf("CREATE TABLE `%s`", c.T.Name)
		item.Risk = dto.RiskInfo
	case *schema.DropTable:
		item.Kind = dto.KindDrop
		item.Table = c.T.Name
		item.ObjectType = "table"
		item.ObjectName = c.T.Name
		item.Summary = fmt.Sprintf("DROP TABLE `%s`", c.T.Name)
		item.Risk = dto.RiskCritical
	case *schema.ModifyTable:
		item.Kind = dto.KindModify
		item.Table = c.T.Name
		item.ObjectType = "table"
		item.ObjectName = c.T.Name
		item.Summary = fmt.Sprintf("ALTER TABLE `%s` (%d changes)", c.T.Name, len(c.Changes))
		item.Risk = riskOfModify(c)
	case *schema.AddColumn:
		item.Kind = dto.KindAdd
		item.ObjectType = "column"
		item.ObjectName = c.C.Name
		item.Summary = fmt.Sprintf("ADD COLUMN `%s`", c.C.Name)
	case *schema.DropColumn:
		item.Kind = dto.KindDrop
		item.ObjectType = "column"
		item.ObjectName = c.C.Name
		item.Summary = fmt.Sprintf("DROP COLUMN `%s`", c.C.Name)
		item.Risk = dto.RiskCritical
	case *schema.ModifyColumn:
		item.Kind = dto.KindModify
		item.ObjectType = "column"
		item.ObjectName = c.To.Name
		item.Summary = fmt.Sprintf("MODIFY COLUMN `%s`", c.To.Name)
		item.Risk = dto.RiskWarn
	case *schema.AddIndex:
		item.Kind = dto.KindAdd
		item.ObjectType = "index"
		item.ObjectName = c.I.Name
		item.Summary = fmt.Sprintf("ADD INDEX `%s`", c.I.Name)
	case *schema.DropIndex:
		item.Kind = dto.KindDrop
		item.ObjectType = "index"
		item.ObjectName = c.I.Name
		item.Summary = fmt.Sprintf("DROP INDEX `%s`", c.I.Name)
		item.Risk = dto.RiskWarn
	case *schema.ModifyIndex:
		item.Kind = dto.KindModify
		item.ObjectType = "index"
		item.ObjectName = c.To.Name
		item.Summary = fmt.Sprintf("MODIFY INDEX `%s`", c.To.Name)
	case *schema.AddForeignKey:
		item.Kind = dto.KindAdd
		item.ObjectType = "foreign_key"
		item.ObjectName = c.F.Symbol
		item.Summary = fmt.Sprintf("ADD FOREIGN KEY `%s`", c.F.Symbol)
	case *schema.DropForeignKey:
		item.Kind = dto.KindDrop
		item.ObjectType = "foreign_key"
		item.ObjectName = c.F.Symbol
		item.Summary = fmt.Sprintf("DROP FOREIGN KEY `%s`", c.F.Symbol)
		item.Risk = dto.RiskWarn
	case *schema.AddCheck:
		item.Kind = dto.KindAdd
		item.ObjectType = "check"
		item.ObjectName = c.C.Name
		item.Summary = fmt.Sprintf("ADD CHECK `%s`", c.C.Name)
		item.Risk = dto.RiskError
	case *schema.DropCheck:
		item.Kind = dto.KindDrop
		item.ObjectType = "check"
		item.ObjectName = c.C.Name
		item.Summary = fmt.Sprintf("DROP CHECK `%s`", c.C.Name)
	case *schema.ModifyCheck:
		item.Kind = dto.KindModify
		item.ObjectType = "check"
		item.ObjectName = c.To.Name
		item.Summary = fmt.Sprintf("MODIFY CHECK `%s`", c.To.Name)
	default:
		item.Kind = dto.KindModify
		item.ObjectType = "attr"
		item.ObjectName = fmt.Sprintf("%T", ch)
		item.Summary = fmt.Sprintf("%T", ch)
	}
	return item
}

func riskOfModify(mt *schema.ModifyTable) dto.RiskLevel {
	risk := dto.RiskInfo
	for _, ch := range mt.Changes {
		switch ch.(type) {
		case *schema.DropColumn, *schema.DropTable:
			return dto.RiskCritical
		case *schema.DropIndex, *schema.DropForeignKey, *schema.ModifyColumn:
			risk = dto.RiskWarn
		case *schema.AddCheck:
			risk = dto.RiskError
		}
	}
	return risk
}

// SelectChanges filters schema.Change by selected item IDs (top-level only).
func (s *Session) SelectChanges(ids []string) []schema.Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	// If child modify items selected, include parent ModifyTable.
	parentNeed := map[int]bool{}
	for _, id := range ids {
		var n int
		if _, err := fmt.Sscanf(id, "c%d", &n); err == nil && n >= 1000 {
			parentNeed[n/1000] = true
		}
	}
	out := make([]schema.Change, 0)
	for i, ch := range s.Changes {
		id := fmt.Sprintf("c%d", i)
		if set[id] || parentNeed[i] {
			out = append(out, ch)
			continue
		}
		// Filter ModifyTable sub-changes if only some children selected.
		if mt, ok := ch.(*schema.ModifyTable); ok {
			var keep []schema.Change
			for j, sub := range mt.Changes {
				sid := fmt.Sprintf("c%d", i*1000+j+1)
				if set[sid] {
					keep = append(keep, sub)
				}
			}
			if len(keep) > 0 {
				cp := *mt
				cp.Changes = keep
				out = append(out, &cp)
			}
		}
	}
	return out
}

// TableDiff returns a structured column/index comparison for one table in a session.
func (e *Engine) TableDiff(sessionID, tableName string) (*dto.TableDiffResult, error) {
	sess, ok := e.Sessions.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("session not found")
	}

	var srcTable, dstTable *schema.Table
	if sess.SourceSch != nil {
		for _, t := range sess.SourceSch.Tables {
			if t.Name == tableName {
				srcTable = t
				break
			}
		}
	}
	if sess.TargetSch != nil {
		for _, t := range sess.TargetSch.Tables {
			if t.Name == tableName {
				dstTable = t
				break
			}
		}
	}

	result := &dto.TableDiffResult{Table: tableName}

	if srcTable != nil || dstTable != nil {
		var srcAttrs, dstAttrs []schema.Attr
		if srcTable != nil {
			srcAttrs = srcTable.Attrs
		}
		if dstTable != nil {
			dstAttrs = dstTable.Attrs
		}
		result.Attrs = appendTableAttrDiffs(result.Attrs, "CHARSET", attrCharset(srcAttrs), attrCharset(dstAttrs))
		result.Attrs = appendTableAttrDiffs(result.Attrs, "COLLATE", attrCollation(srcAttrs), attrCollation(dstAttrs))
		result.Attrs = appendTableAttrDiffs(result.Attrs, "ENGINE", attrEngine(srcAttrs), attrEngine(dstAttrs))
		result.Attrs = appendTableAttrDiffs(result.Attrs, "COMMENT", attrComment(srcAttrs), attrComment(dstAttrs))
	}

	// Column diff
	srcCols := map[string]*schema.Column{}
	dstCols := map[string]*schema.Column{}
	var srcColOrder []string
	if srcTable != nil {
		for _, c := range srcTable.Columns {
			srcCols[c.Name] = c
			srcColOrder = append(srcColOrder, c.Name)
		}
	}
	if dstTable != nil {
		for _, c := range dstTable.Columns {
			dstCols[c.Name] = c
		}
	}
	// Columns present in source
	seen := map[string]bool{}
	for _, name := range srcColOrder {
		seen[name] = true
		sc := srcCols[name]
		dc, inDst := dstCols[name]
		if !inDst {
			result.Columns = append(result.Columns, dto.ColDiff{
				Name: name, Kind: "add",
				Source: colDesc(sc, sess.Normalize), Target: "",
			})
		} else {
			srcDesc := colDesc(sc, sess.Normalize)
			dstDesc := colDesc(dc, sess.Normalize)
			kind := "same"
			if srcDesc != dstDesc {
				kind = "modify"
			}
			result.Columns = append(result.Columns, dto.ColDiff{
				Name: name, Kind: kind,
				Source: srcDesc, Target: dstDesc,
			})
		}
	}
	// Columns only in target (to be dropped)
	if dstTable != nil {
		for _, c := range dstTable.Columns {
			if !seen[c.Name] {
				result.Columns = append(result.Columns, dto.ColDiff{
					Name: c.Name, Kind: "drop",
					Source: "", Target: colDesc(c, sess.Normalize),
				})
			}
		}
	}

	// Index diff
	srcIdxs := map[string]*schema.Index{}
	dstIdxs := map[string]*schema.Index{}
	var srcIdxOrder []string
	if srcTable != nil {
		for _, idx := range srcTable.Indexes {
			srcIdxs[idx.Name] = idx
			srcIdxOrder = append(srcIdxOrder, idx.Name)
		}
	}
	if dstTable != nil {
		for _, idx := range dstTable.Indexes {
			dstIdxs[idx.Name] = idx
		}
	}
	seenIdx := map[string]bool{}
	for _, name := range srcIdxOrder {
		seenIdx[name] = true
		si := srcIdxs[name]
		di, inDst := dstIdxs[name]
		if !inDst {
			result.Indexes = append(result.Indexes, dto.ColDiff{
				Name: name, Kind: "add",
				Source: idxDesc(si), Target: "",
			})
		} else {
			srcDesc := idxDesc(si)
			dstDesc := idxDesc(di)
			kind := "same"
			if srcDesc != dstDesc {
				kind = "modify"
			}
			result.Indexes = append(result.Indexes, dto.ColDiff{
				Name: name, Kind: kind,
				Source: srcDesc, Target: dstDesc,
			})
		}
	}
	if dstTable != nil {
		for _, idx := range dstTable.Indexes {
			if !seenIdx[idx.Name] {
				result.Indexes = append(result.Indexes, dto.ColDiff{
					Name: idx.Name, Kind: "drop",
					Source: "", Target: idxDesc(idx),
				})
			}
		}
	}

	srcFKs := map[string]*schema.ForeignKey{}
	dstFKs := map[string]*schema.ForeignKey{}
	var srcFKOrder []string
	if srcTable != nil {
		for _, fk := range srcTable.ForeignKeys {
			srcFKs[fk.Symbol] = fk
			srcFKOrder = append(srcFKOrder, fk.Symbol)
		}
	}
	if dstTable != nil {
		for _, fk := range dstTable.ForeignKeys {
			dstFKs[fk.Symbol] = fk
		}
	}
	seenFK := map[string]bool{}
	for _, name := range srcFKOrder {
		seenFK[name] = true
		sf := srcFKs[name]
		df, inDst := dstFKs[name]
		if !inDst {
			result.ForeignKeys = append(result.ForeignKeys, dto.ColDiff{
				Name: name, Kind: "add",
				Source: fkDesc(sf), Target: "",
			})
		} else {
			srcDesc := fkDesc(sf)
			dstDesc := fkDesc(df)
			kind := "same"
			if srcDesc != dstDesc {
				kind = "modify"
			}
			result.ForeignKeys = append(result.ForeignKeys, dto.ColDiff{
				Name: name, Kind: kind,
				Source: srcDesc, Target: dstDesc,
			})
		}
	}
	if dstTable != nil {
		for _, fk := range dstTable.ForeignKeys {
			if !seenFK[fk.Symbol] {
				result.ForeignKeys = append(result.ForeignKeys, dto.ColDiff{
					Name: fk.Symbol, Kind: "drop",
					Source: "", Target: fkDesc(fk),
				})
			}
		}
	}
	return result, nil
}

func colDesc(c *schema.Column, opts dto.NormalizeOptions) string {
	if c == nil || c.Type == nil {
		return ""
	}
	raw := c.Type.Raw
	if opts.StripIntDisplayWidth && !hasZeroFill(c) {
		base := ""
		if it, ok := c.Type.Type.(*schema.IntegerType); ok {
			base = it.T
		}
		raw = stripWidthFromRaw(raw, base)
	}
	s := raw
	if c.Type.Null {
		s += " NULL"
	} else {
		s += " NOT NULL"
	}
	if c.Default != nil {
		switch d := c.Default.(type) {
		case *schema.Literal:
			s += " DEFAULT " + normalizeExpr(d.V)
		case *schema.RawExpr:
			s += " DEFAULT " + normalizeExpr(d.X)
		}
	}
	if cs := attrCharset(c.Attrs); cs != "" {
		s += " CHARACTER SET " + aliasCharset(cs)
	}
	if cl := attrCollation(c.Attrs); cl != "" {
		s += " COLLATE " + normalizeCollation(cl, opts)
	}
	if ou := columnOnUpdate(c); ou != "" {
		s += " ON UPDATE " + normalizeExpr(ou)
	}
	for _, a := range c.Attrs {
		switch v := a.(type) {
		case *schema.Comment:
			if v.Text != "" {
				s += " COMMENT '" + v.Text + "'"
			}
		}
	}
	return s
}

func hasZeroFill(c *schema.Column) bool {
	for _, a := range c.Attrs {
		if _, ok := a.(*mysql.ZeroFill); ok {
			return true
		}
	}
	return false
}

func columnOnUpdate(c *schema.Column) string {
	for _, a := range c.Attrs {
		if o, ok := a.(*mysql.OnUpdate); ok {
			return o.A
		}
	}
	return ""
}

func idxDesc(idx *schema.Index) string {
	if idx == nil {
		return ""
	}
	var parts []string
	for _, p := range idx.Parts {
		col := ""
		if p.C != nil {
			col = p.C.Name
		} else if p.X != nil {
			col = "(" + fmt.Sprint(p.X) + ")"
		}
		if p.Desc {
			col += " DESC"
		}
		parts = append(parts, col)
	}
	s := "(" + strings.Join(parts, ", ") + ")"
	if idx.Unique {
		s = "UNIQUE " + s
	}
	return s
}

func fkDesc(fk *schema.ForeignKey) string {
	if fk == nil {
		return ""
	}
	var cols, refs []string
	for _, c := range fk.Columns {
		cols = append(cols, c.Name)
	}
	for _, c := range fk.RefColumns {
		refs = append(refs, c.Name)
	}
	refTable := ""
	if fk.RefTable != nil {
		refTable = fk.RefTable.Name
	}
	return fmt.Sprintf("(%s) REFERENCES %s(%s) ON UPDATE %s ON DELETE %s",
		strings.Join(cols, ","), refTable, strings.Join(refs, ","), fk.OnUpdate, fk.OnDelete)
}

// QuoteIdent backticks a MySQL identifier.
func QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
