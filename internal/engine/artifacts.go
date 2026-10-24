package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func changeTable(ch schema.Change) string {
	switch c := ch.(type) {
	case *schema.AddTable:
		return c.T.Name
	case *schema.DropTable:
		return c.T.Name
	case *schema.ModifyTable:
		return c.T.Name
	}
	return ""
}
func objectKey(i dto.ChangeItem) string {
	return i.Table + "\x00" + i.ObjectType + "\x00" + i.ObjectName
}
func itemsForChanges(sess *Session, changes []schema.Change) []dto.ChangeItem {
	out := []dto.ChangeItem{}
	seen := map[string]bool{}
	add := func(item dto.ChangeItem) {
		if !seen[item.ID] {
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	for _, ch := range changes {
		table := changeTable(ch)
		if mt, ok := ch.(*schema.ModifyTable); ok {
			for _, sub := range mt.Changes {
				item := changeToItem(sub, 0, "", sess.Target.SemVer)
				item.Table = table
				for _, orig := range sess.Items {
					if orig.ObjectType != "table" && objectKey(orig) == objectKey(item) && orig.Kind == item.Kind {
						add(orig)
					}
				}
			}
		} else {
			for _, orig := range sess.Items {
				if orig.Table == table && orig.ObjectType == "table" {
					add(orig)
				}
			}
		}
	}
	return out
}
func statementLinks(sess *Session, source schema.Change, selected []dto.ChangeItem) (string, []string) {
	table := changeTable(source)
	if table == "" {
		return "", nil
	}
	keys := map[string]bool{}
	if mt, ok := source.(*schema.ModifyTable); ok {
		for _, sub := range mt.Changes {
			it := changeToItem(sub, 0, "", sess.Target.SemVer)
			it.Table = table
			keys[objectKey(it)] = true
		}
	}
	ids := []string{}
	for _, it := range selected {
		if it.Table == table && (len(keys) == 0 || keys[objectKey(it)]) {
			ids = append(ids, it.ID)
		}
	}
	// Planner may split a modification (e.g. index recreation) into new types.
	// Only an explicitly selected whole-table change may cover such a dependency.
	if len(ids) == 0 {
		for _, it := range selected {
			if it.Table == table && it.ObjectType == "table" {
				ids = append(ids, it.ID)
			}
		}
	}
	return table, ids
}
func snapshotsFor(ctx context.Context, c *Conn, tables []string) ([]dto.TableSnapshot, error) {
	names := map[string]bool{}
	for _, t := range tables {
		if t != "" {
			names[t] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for t := range names {
		ordered = append(ordered, t)
	}
	sort.Strings(ordered)
	out := make([]dto.TableSnapshot, 0, len(ordered))
	for _, t := range ordered {
		var n int
		if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=? AND table_name=? AND TABLE_TYPE='BASE TABLE'`, c.Config.Database, t).Scan(&n); err != nil {
			return nil, err
		}
		sn := dto.TableSnapshot{Table: t, Exists: n > 0}
		if sn.Exists {
			var name string
			if err := c.DB.QueryRowContext(ctx, "SHOW CREATE TABLE "+quoteIdent(c.Config.Database)+"."+quoteIdent(t)).Scan(&name, &sn.DDL); err != nil {
				return nil, err
			}
		}
		sum := sha256.Sum256([]byte(sn.DDL))
		sn.SHA256 = hex.EncodeToString(sum[:])
		out = append(out, sn)
	}
	return out, nil
}
func snapshotTables(snaps []dto.TableSnapshot) []string {
	out := []string{}
	for _, s := range snaps {
		out = append(out, s.Table)
	}
	return out
}

var tableAutoIncrement = regexp.MustCompile(`\bAUTO_INCREMENT=\d+\b`)

// Ignore only the table's advancing counter, never text inside a comment,
// identifier or default value that happens to contain AUTO_INCREMENT=123.
func normalizeSnapshotDDL(ddl string) string {
	masked := []byte(ddl)
	for i := 0; i < len(ddl); {
		start := i
		switch {
		case ddl[i] == '\'' || ddl[i] == '"' || ddl[i] == '`':
			quote := ddl[i]
			i++
			for i < len(ddl) {
				if ddl[i] == '\\' {
					i += 2
				} else if ddl[i] == quote {
					i++
					if i < len(ddl) && ddl[i] == quote {
						i++
						continue
					}
					break
				} else {
					i++
				}
			}
		case strings.HasPrefix(ddl[i:], "/*"):
			if end := strings.Index(ddl[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(ddl)
			}
		case ddl[i] == '#' || strings.HasPrefix(ddl[i:], "-- "):
			if end := strings.IndexByte(ddl[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(ddl)
			}
		default:
			i++
			continue
		}
		for j := start; j < i && j < len(masked); j++ {
			masked[j] = ' '
		}
	}
	var out strings.Builder
	start := 0
	for _, match := range tableAutoIncrement.FindAllIndex(masked, -1) {
		begin := match[0]
		if begin > start && (ddl[begin-1] == ' ' || ddl[begin-1] == '\t') {
			begin--
		}
		out.WriteString(ddl[start:begin])
		start = match[1]
	}
	out.WriteString(ddl[start:])
	return out.String()
}

func sameSnapshots(a, b []dto.TableSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]dto.TableSnapshot{}
	for _, x := range a {
		m[x.Table] = x
	}
	for _, x := range b {
		v, ok := m[x.Table]
		if !ok || v.Exists != x.Exists || normalizeSnapshotDDL(v.DDL) != normalizeSnapshotDDL(x.DDL) {
			return false
		}
	}
	return true
}
func SQLFile(statements []dto.PlannedStatement) string {
	s := "-- DB Compare: 请核对目标库后执行。DDL 自动提交。\nSET NAMES utf8mb4;\nSET @dbcompare_previous_sql_mode = @@SESSION.sql_mode;\nSET SESSION sql_mode = 'STRICT_TRANS_TABLES,NO_ENGINE_SUBSTITUTION';\n\n"
	for _, st := range statements {
		s += "-- " + strings.ReplaceAll(st.Comment, "\n", " ") + "\n" + strings.TrimSuffix(strings.TrimSpace(st.SQL), ";") + ";\n\n"
	}
	return s + "SET SESSION sql_mode = @dbcompare_previous_sql_mode;\n"
}
func SnapshotSQL(snaps []dto.TableSnapshot, database string) string {
	s := "-- 同步前结构快照，仅含结构，不含业务数据。\n-- 用于审阅和恢复参考，不是可直接覆盖现有表的回滚脚本。\nSET NAMES utf8mb4;\nUSE " + quoteIdent(database) + ";\n\n"
	for _, sn := range snaps {
		if !sn.Exists {
			s += "-- " + quoteIdent(sn.Table) + "：同步前不存在\n\n"
			continue
		}
		s += fmt.Sprintf("-- %s SHA256 %s\n%s;\n\n", quoteIdent(sn.Table), sn.SHA256, strings.TrimSuffix(sn.DDL, ";"))
	}
	return s
}

func (e *Engine) SessionPlan(id string) *dto.PlanResult {
	sess, ok := e.Sessions.Get(id)
	if !ok {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.Plan
}
