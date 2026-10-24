package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pp/db-compare/internal/dto"
	"golang.org/x/text/encoding/charmap"
)

func latinByte(r rune) (byte, bool) {
	if r <= 255 {
		return byte(r), true
	}
	b, err := charmap.Windows1252.NewEncoder().String(string(r))
	if err != nil || len(b) != 1 {
		return 0, false
	}
	return b[0], true
}

// RecoverComment reverses only strictly decodable UTF-8 byte sequences. It
// requires an increase in Chinese characters, avoiding ordinary Latin text.
func RecoverComment(s string) (string, bool) {
	chinese := func(s string) int {
		n := 0
		for _, r := range s {
			if unicode.Is(unicode.Han, r) {
				n++
			}
		}
		return n
	}
	original := s
	for pass := 0; pass < 3; pass++ {
		var out strings.Builder
		buf := []byte{}
		var segment strings.Builder
		flush := func() {
			text := segment.String()
			if utf8.Valid(buf) && chinese(string(buf)) > chinese(text) {
				out.Write(buf)
			} else {
				out.WriteString(text)
			}
			buf = nil
			segment.Reset()
		}
		for _, r := range s {
			if b, ok := latinByte(r); ok {
				buf = append(buf, b)
				segment.WriteRune(r)
			} else {
				flush()
				out.WriteRune(r)
			}
		}
		flush()
		next := out.String()
		if next == s {
			break
		}
		s = next
	}
	return s, s != original
}
func repairID(table, column, text string) string {
	return fmt.Sprintf("r%x", sha256.Sum256([]byte(table+"\x00"+column+"\x00"+text)))
}
func readComments(ctx context.Context, c *Conn) ([]dto.CommentRepair, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT TABLE_NAME,COLUMN_NAME,COLUMN_COMMENT FROM information_schema.columns WHERE table_schema=? AND column_comment<>'' UNION ALL SELECT TABLE_NAME,'',TABLE_COMMENT FROM information_schema.tables WHERE table_schema=? AND TABLE_TYPE='BASE TABLE' AND table_comment<>'' ORDER BY 1,2`, c.Config.Database, c.Config.Database)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dto.CommentRepair{}
	for rows.Next() {
		var x dto.CommentRepair
		if err := rows.Scan(&x.Table, &x.Column, &x.Original); err != nil {
			return nil, err
		}
		x.ID = repairID(x.Table, x.Column, x.Original)
		x.Recovered, _ = RecoverComment(x.Original)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (e *Engine) DiagnoseComments(ctx context.Context, sessionID, side string) ([]dto.CommentRepair, error) {
	sess, ok := e.Sessions.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("请重新对比")
	}
	c := sess.Target
	if side == "source" {
		if sess.Source.Config.SnapshotID != "" {
			return nil, fmt.Errorf("快照为只读来源，不能修复快照；请使用实时源库")
		}
		c = sess.Source
	} else if side != "target" {
		return nil, fmt.Errorf("请选择源库或目标库")
	}
	comments, err := readComments(ctx, c)
	if err != nil {
		return nil, err
	}
	out := []dto.CommentRepair{}
	for _, x := range comments {
		if x.Original != x.Recovered {
			out = append(out, x)
		}
	}
	return out, nil
}
func sqlText(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "'", "''", "\x00", "\\0", "\n", "\\n", "\r", "\\r", "\x1a", "\\Z")
	return "'" + r.Replace(s) + "'"
}

var commentToken = regexp.MustCompile(`\bCOMMENT\s+'(?:[^'\\]|\\.|'')*'`)

func repairClause(sn dto.TableSnapshot, x dto.CommentRepair, text string) (string, error) {
	if x.Column == "" {
		return "COMMENT = " + sqlText(text), nil
	}
	prefix := quoteIdent(x.Column) + " "
	for _, raw := range strings.Split(sn.DDL, "\n") {
		line := strings.TrimSuffix(strings.TrimSpace(raw), ",")
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		matches := commentToken.FindAllStringIndex(line, -1)
		if len(matches) == 0 {
			return "", fmt.Errorf("%s.%s 无法安全定位注释", x.Table, x.Column)
		}
		m := matches[len(matches)-1]
		return "MODIFY COLUMN " + line[:m[0]] + "COMMENT " + sqlText(text) + line[m[1]:], nil
	}
	return "", fmt.Errorf("%s.%s 字段不存在", x.Table, x.Column)
}
func (e *Engine) PlanCommentRepair(ctx context.Context, req dto.RepairRequest) (*dto.PlanResult, error) {
	original, ok := e.Sessions.Get(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("请先对比")
	}
	c := original.Target
	if req.Side == "source" {
		c = original.Source
	} else if req.Side != "target" {
		return nil, fmt.Errorf("请选择源库或目标库")
	}
	candidates, err := e.DiagnoseComments(ctx, req.SessionID, req.Side)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range req.IDs {
		want[id] = true
	}
	if len(want) == 0 {
		return nil, fmt.Errorf("请先勾选待修复注释")
	}
	repairs := []dto.CommentRepair{}
	tables := []string{}
	for _, x := range candidates {
		if want[x.ID] {
			repairs = append(repairs, x)
			tables = append(tables, x.Table)
			delete(want, x.ID)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("注释已变化，请重新扫描乱码")
	}
	target, err := Open(ctx, c.Config)
	if err != nil {
		return nil, err
	}
	source, err := Open(ctx, c.Config)
	if err != nil {
		target.Close()
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			source.Close()
			target.Close()
		}
	}()
	snapshots, err := snapshotsFor(ctx, target, tables)
	if err != nil {
		return nil, err
	}
	byTable := map[string]dto.TableSnapshot{}
	for _, sn := range snapshots {
		byTable[sn.Table] = sn
	}
	sess := &Session{ID: uuid.NewString(), Source: source, Target: target, Normalize: original.Normalize}
	plan := &dto.PlanResult{TargetDatabase: c.Config.Database, ID: uuid.NewString(), SessionID: sess.ID, Mode: "repair", Repairs: repairs, Snapshots: snapshots, Selected: []dto.ChangeItem{}, Statements: []dto.PlannedStatement{}, Prechecks: []dto.PrecheckIssue{}}
	for _, sn := range snapshots {
		clauses, reverse, ids := []string{}, []string{}, []string{}
		for _, x := range repairs {
			if x.Table != sn.Table {
				continue
			}
			cl, err := repairClause(sn, x, x.Recovered)
			if err != nil {
				return nil, err
			}
			rev, err := repairClause(sn, x, x.Original)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, cl)
			reverse = append(reverse, rev)
			ids = append(ids, x.ID)
			name := x.Column
			if name == "" {
				name = "表注释"
			}
			plan.Selected = append(plan.Selected, dto.ChangeItem{ID: x.ID, Table: x.Table, ObjectType: "comment", ObjectName: name, Summary: "修复注释 " + name, Kind: dto.KindModify, Risk: dto.RiskWarn})
		}
		prefix := "ALTER TABLE " + quoteIdent(c.Config.Database) + "." + quoteIdent(sn.Table) + "\n  "
		plan.Statements = append(plan.Statements, dto.PlannedStatement{ID: fmt.Sprintf("s%d", len(plan.Statements)), Table: sn.Table, ChangeID: ids[0], ChangeIDs: ids, SQL: prefix + strings.Join(clauses, ",\n  "), Reverse: prefix + strings.Join(reverse, ",\n  "), Risk: dto.RiskWarn, Comment: "修复 " + sn.Table + " 的注释"})
	}
	sess.Plan = plan
	sess.Items = plan.Selected
	e.Sessions.Put(sess)
	success = true
	return plan, nil
}
func (e *Engine) verifyRepairs(ctx context.Context, sess *Session, run *dto.SyncRun) error {
	comments, err := readComments(ctx, sess.Target)
	if err != nil {
		return err
	}
	current := map[string]string{}
	for _, x := range comments {
		current[x.Table+"\x00"+x.Column] = x.Original
	}
	run.Remaining = []dto.RemainingDifference{}
	run.SelectionComplete = true
	for _, x := range run.Repairs {
		if current[x.Table+"\x00"+x.Column] != x.Recovered {
			run.SelectionComplete = false
			name := x.Column
			if name == "" {
				name = "表注释"
			}
			run.Remaining = append(run.Remaining, dto.RemainingDifference{Category: "selected", Item: dto.ChangeItem{ID: x.ID, Table: x.Table, ObjectType: "comment", ObjectName: name, Summary: "注释修复未完成", Kind: dto.KindModify}})
		}
	}
	return nil
}
