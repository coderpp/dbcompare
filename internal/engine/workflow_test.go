package engine

import (
	"ariga.io/atlas/sql/schema"
	"strings"
	"testing"

	"github.com/pp/db-compare/internal/dto"
)

func TestRecoverComment(t *testing.T) {
	cases := []struct{ input, want string }{
		{"åˆ›å»ºæ—¶é—´", "创建时间"},
		{"æ\u008f\u008fè¿°", "描述"},
		{"业务所属合同；无合同为空", "业务所属合同；无合同为空"},
		{"中文 / åˆ›å»ºæ—¶é—´", "中文 / 创建时间"},
		{"Café, 50%", "Café, 50%"},
		{"损坏�无法猜测", "损坏�无法猜测"},
	}
	for _, tc := range cases {
		got, changed := RecoverComment(tc.input)
		if got != tc.want || changed != (tc.input != tc.want) {
			t.Errorf("%q => %q (%v), want %q", tc.input, got, changed, tc.want)
		}
	}
}
func TestRepairClausePreservesDefinition(t *testing.T) {
	sn := dto.TableSnapshot{Table: "t", DDL: "CREATE TABLE `t` (\n  `c` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL DEFAULT 'a,b' COMMENT 'åˆ›å»ºæ—¶é—´',\n  PRIMARY KEY (`c`)\n) ENGINE=InnoDB"}
	c := dto.CommentRepair{Table: "t", Column: "c"}
	got, err := repairClause(sn, c, "创建时间 '测试'\\路径")
	if err != nil {
		t.Fatal(err)
	}
	want := "MODIFY COLUMN `c` varchar(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci NOT NULL DEFAULT 'a,b' COMMENT '创建时间 ''测试''\\\\路径'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if _, err := repairClause(sn, dto.CommentRepair{Table: "t", Column: "missing"}, "中文"); err == nil {
		t.Fatal("missing column should fail")
	}
}
func TestSnapshotsIgnoreOnlyCounterChanges(t *testing.T) {
	a := []dto.TableSnapshot{{Table: "t", Exists: true, DDL: "CREATE TABLE `t` (`id` int AUTO_INCREMENT) AUTO_INCREMENT=2"}}
	b := []dto.TableSnapshot{{Table: "t", Exists: true, DDL: "CREATE TABLE `t` (`id` int AUTO_INCREMENT) AUTO_INCREMENT=200"}}
	if !sameSnapshots(a, b) {
		t.Fatal("ordinary auto-increment progress should not invalidate a plan")
	}
	b[0].DDL = strings.ReplaceAll(b[0].DDL, "`id` int", "`id` bigint")
	if sameSnapshots(a, b) {
		t.Fatal("type drift must invalidate plan")
	}
	b[0].Exists = false
	if sameSnapshots(a, b) {
		t.Fatal("removed table must invalidate plan")
	}
}

func TestIndexSelectionRequiresItsColumn(t *testing.T) {
	current := schema.New("test").AddTables(schema.NewTable("t").AddColumns(schema.NewIntColumn("id", "int")))
	col := schema.NewIntColumn("added", "int")
	idx := schema.NewIndex("idx_added").AddColumns(col)
	table := schema.NewTable("t")
	mt := &schema.ModifyTable{T: table, Changes: []schema.Change{&schema.AddIndex{I: idx}}}
	if got := checkSelectionDependencies(current, []schema.Change{mt}); len(got) != 1 {
		t.Fatalf("expected missing column dependency, got %+v", got)
	}
	mt.Changes = append(mt.Changes, &schema.AddColumn{C: col})
	if got := checkSelectionDependencies(current, []schema.Change{mt}); len(got) != 0 {
		t.Fatalf("selected dependency should pass: %+v", got)
	}
}
