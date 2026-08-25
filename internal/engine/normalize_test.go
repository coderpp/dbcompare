package engine

import (
	"testing"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

func TestParseVersion(t *testing.T) {
	v := ParseVersion("8.0.36-0ubuntu0.22.04.1")
	if !v.AtLeast(8, 0, 19) || v.Major != 8 || v.Patch != 36 {
		t.Fatalf("unexpected %#v", v)
	}
	v57 := ParseVersion("5.7.44")
	if !v57.Is57() || v57.AtLeast(8, 0, 0) {
		t.Fatalf("unexpected 5.7 parse %#v", v57)
	}
}

func TestNormalizeStripIntWidth(t *testing.T) {
	s := &schema.Schema{Name: "db", Tables: []*schema.Table{
		{
			Name: "t",
			Columns: []*schema.Column{
				{Name: "id", Type: &schema.ColumnType{Type: &schema.IntegerType{T: "int"}, Raw: "int(11)"}},
				{Name: "zf", Type: &schema.ColumnType{Type: &schema.IntegerType{T: "int"}, Raw: "int(6)"}, Attrs: []schema.Attr{&mysql.ZeroFill{A: "ZEROFILL"}}},
			},
		},
	}}
	Normalize(s, dto.NormalizeOptions{StripIntDisplayWidth: true})
	if s.Tables[0].Columns[0].Type.Raw != "int" {
		t.Fatalf("expected stripped width, got %q", s.Tables[0].Columns[0].Type.Raw)
	}
	if s.Tables[0].Columns[1].Type.Raw != "int(6)" {
		t.Fatalf("zerofill width should remain, got %q", s.Tables[0].Columns[1].Type.Raw)
	}
}

func TestNormalizeUTF8Alias(t *testing.T) {
	col := &schema.Column{
		Name:  "c",
		Type:  &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 10}, Raw: "varchar(10)"},
		Attrs: []schema.Attr{&schema.Charset{V: "utf8"}, &schema.Collation{V: "utf8_general_ci"}},
	}
	s := &schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{col}}}}
	Normalize(s, dto.NormalizeOptions{UnifyUTF8Aliases: true})
	cs := col.Attrs[0].(*schema.Charset)
	cl := col.Attrs[1].(*schema.Collation)
	if cs.V != "utf8mb3" || cl.V != "utf8mb3_general_ci" {
		t.Fatalf("alias failed: %#v %#v", cs, cl)
	}
}

func TestNormalizeUTF8MB4CICollations(t *testing.T) {
	src := &schema.Column{
		Name:  "c",
		Type:  &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 10}, Raw: "varchar(10)"},
		Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}, &schema.Collation{V: "utf8mb4_0900_ai_ci"}},
	}
	dst := &schema.Column{
		Name:  "c",
		Type:  &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 10}, Raw: "varchar(10)"},
		Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}, &schema.Collation{V: "utf8mb4_general_ci"}},
	}
	opts := dto.NormalizeOptions{UnifyUTF8MB4CICollations: true}
	Normalize(&schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{src}}}}, opts)
	Normalize(&schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{dst}}}}, opts)
	if src.Attrs[1].(*schema.Collation).V != "utf8mb4_general_ci" {
		t.Fatalf("0900 should map to general_ci, got %q", src.Attrs[1].(*schema.Collation).V)
	}
	if src.Attrs[1].(*schema.Collation).V != dst.Attrs[1].(*schema.Collation).V {
		t.Fatalf("collations should match after unify: %q vs %q", src.Attrs[1].(*schema.Collation).V, dst.Attrs[1].(*schema.Collation).V)
	}
	if tableHasVisibleDiff(
		&schema.Table{Name: "t", Columns: []*schema.Column{src}},
		&schema.Table{Name: "t", Columns: []*schema.Column{dst}},
		opts,
	) {
		t.Fatal("unified CI collations should not be a visible diff")
	}
}

func TestNormalizeStripAutoInc(t *testing.T) {
	tbl := &schema.Table{
		Name:  "t",
		Attrs: []schema.Attr{&mysql.AutoIncrement{V: 99}, &schema.Comment{Text: "hi"}},
	}
	Normalize(&schema.Schema{Tables: []*schema.Table{tbl}}, dto.NormalizeOptions{IgnoreAutoIncrement: true})
	for _, a := range tbl.Attrs {
		if _, ok := a.(*mysql.AutoIncrement); ok {
			t.Fatal("auto_increment attr should be stripped")
		}
	}
}

func TestCapabilityMatrix(t *testing.T) {
	c57 := CapabilityFor(SemVer{Major: 5, Minor: 7, Patch: 44, Raw: "5.7.44"})
	if c57.CheckConstraint || c57.ExprDefault || c57.InvisibleIndex {
		t.Fatalf("5.7 should not support 8.0 features: %#v", c57)
	}
	c80 := CapabilityFor(SemVer{Major: 8, Minor: 0, Patch: 36, Raw: "8.0.36"})
	if !c80.CheckConstraint || !c80.FunctionalIndex || c80.DisplayWidthOutput {
		t.Fatalf("8.0.36 capability unexpected: %#v", c80)
	}
}

func TestAnalyzeDowngradeAllowsCurrentTimestamp(t *testing.T) {
	tbl := &schema.Table{
		Name: "flyway_schema_history",
		Columns: []*schema.Column{
			{Name: "installed_on", Default: &schema.RawExpr{X: "CURRENT_TIMESTAMP"}},
			{Name: "F_CreatorTime", Default: &schema.RawExpr{X: "(CURRENT_TIMESTAMP)"}},
			{Name: "F_LastModifyTime", Default: &schema.RawExpr{X: "CURRENT_TIMESTAMP(6)"}},
			{Name: "F_ExecutedTime", Default: &schema.RawExpr{X: "now()"}},
		},
	}
	issues := AnalyzeDowngradeOn(&schema.Schema{Tables: []*schema.Table{tbl}}, SemVer{Major: 5, Minor: 7, Patch: 44, Raw: "5.7.44"})
	for _, i := range issues {
		if i.Code == "expr_default" {
			t.Fatalf("native timestamp default should not block 5.7: %#v", issues)
		}
	}
}

func TestAnalyzeDowngradeBlocksJSONDefault(t *testing.T) {
	tbl := &schema.Table{
		Name: "expr_default_demo",
		Columns: []*schema.Column{
			{Name: "meta", Default: &schema.RawExpr{X: "(JSON_OBJECT())"}},
		},
	}
	issues := AnalyzeDowngradeOn(&schema.Schema{Tables: []*schema.Table{tbl}}, SemVer{Major: 5, Minor: 7, Patch: 44, Raw: "5.7.44"})
	found := false
	for _, i := range issues {
		if i.Code == "expr_default" && i.Level == DowngradeBlock {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected blocked expression default, got %#v", issues)
	}
}

func TestNormalizeUnwrapsTimestampDefault(t *testing.T) {
	col := &schema.Column{Name: "ts", Default: &schema.RawExpr{X: "(CURRENT_TIMESTAMP)"}}
	Normalize(&schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{col}}}}, dto.NormalizeOptions{})
	got, _ := col.Default.(*schema.RawExpr)
	if got == nil || got.X != "CURRENT_TIMESTAMP" {
		t.Fatalf("expected CURRENT_TIMESTAMP, got %#v", col.Default)
	}
}

func TestAnalyzeDowngradeBlocksCheck(t *testing.T) {
	desired := &schema.Schema{Tables: []*schema.Table{{
		Name:  "t",
		Attrs: []schema.Attr{&schema.Check{Name: "chk", Expr: "age >= 0"}},
	}}}
	issues := AnalyzeDowngradeOn(desired, SemVer{Major: 5, Minor: 7, Patch: 44, Raw: "5.7.44"})
	found := false
	for _, i := range issues {
		if i.Code == "check_constraint" && i.Level == DowngradeBlock {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected blocked check constraint, got %#v", issues)
	}
}

func TestIndexPrefixLimitOnlyTouchesChangedIndexes(t *testing.T) {
	tBig := &schema.Table{
		Name: "t",
		Columns: []*schema.Column{
			{Name: "c", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 1200}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
		},
		Indexes: []*schema.Index{
			{Name: "idx_big", Parts: []*schema.IndexPart{{C: &schema.Column{Name: "c"}}}},
		},
	}
	tBig.Indexes[0].Parts[0].C = tBig.Columns[0]
	desired := &schema.Schema{Tables: []*schema.Table{tBig}}
	current := &schema.Schema{Tables: []*schema.Table{tBig}} // same oversized index already on target

	// No index changes selected → should not emit index_prefix_limit.
	issues := checkIndexPrefixLimits(desired, current, []schema.Change{
		&schema.ModifyTable{T: tBig, Changes: []schema.Change{
			&schema.ModifyColumn{From: tBig.Columns[0], To: tBig.Columns[0]},
		}},
	})
	for _, i := range issues {
		if i.Code == "index_prefix_limit" {
			t.Fatalf("untouched oversized index should not precheck: %#v", issues)
		}
	}

	// Adding the oversized index when target lacks it → error.
	issues = checkIndexPrefixLimits(desired, &schema.Schema{}, []schema.Change{
		&schema.ModifyTable{T: tBig, Changes: []schema.Change{&schema.AddIndex{I: tBig.Indexes[0]}}},
	})
	found := false
	for _, i := range issues {
		if i.Code == "index_prefix_limit" && i.Level == dto.RiskError {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected blocking index_prefix_limit for new oversized index, got %#v", issues)
	}

	// Adding when target already has it → warn only.
	issues = checkIndexPrefixLimits(desired, current, []schema.Change{
		&schema.ModifyTable{T: tBig, Changes: []schema.Change{&schema.AddIndex{I: tBig.Indexes[0]}}},
	})
	for _, i := range issues {
		if i.Code == "index_prefix_limit" && i.Level == dto.RiskError {
			t.Fatalf("existing oversized index should warn, not block: %#v", issues)
		}
	}
}

func TestEstimateIndexBytesIgnoresNonStringAsVarchar(t *testing.T) {
	// Real-world shape that previously false-fired at 4596 bytes:
	// 3×varchar(128) utf8mb4 + 3×datetime — should be ~1536+15, not 4596.
	tbl := &schema.Table{
		Name: "syjc_ai_conversation",
		Columns: []*schema.Column{
			{Name: "tenant_id", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 128}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
			{Name: "user_id", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 128}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
			{Name: "agent_id", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 128}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
			{Name: "deleted_at", Type: &schema.ColumnType{Type: &schema.TimeType{T: "datetime"}}},
			{Name: "archived_at", Type: &schema.ColumnType{Type: &schema.TimeType{T: "datetime"}}},
			{Name: "updated_at", Type: &schema.ColumnType{Type: &schema.TimeType{T: "datetime"}}},
		},
	}
	idx := &schema.Index{Name: "idx_syjc_ai_conversation_list", Parts: []*schema.IndexPart{
		{C: tbl.Columns[0]}, {C: tbl.Columns[1]}, {C: tbl.Columns[2]},
		{C: tbl.Columns[3]}, {C: tbl.Columns[4]}, {C: tbl.Columns[5]},
	}}
	got := estimateIndexBytes(tbl, idx)
	if got > 3072 {
		t.Fatalf("composite varchar+datetime index estimated %d bytes; should be under 3072", got)
	}
	// varchar-only oversize still flags.
	wide := &schema.Table{
		Name: "t",
		Columns: []*schema.Column{
			{Name: "a", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 500}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
			{Name: "b", Type: &schema.ColumnType{Type: &schema.StringType{T: "varchar", Size: 500}}, Attrs: []schema.Attr{&schema.Charset{V: "utf8mb4"}}},
		},
	}
	wideIdx := &schema.Index{Name: "idx_wide", Parts: []*schema.IndexPart{{C: wide.Columns[0]}, {C: wide.Columns[1]}}}
	if estimateIndexBytes(wide, wideIdx) <= 3072 {
		t.Fatal("expected wide varchar index to exceed 3072")
	}
}

func TestReservedWordPrecheck(t *testing.T) {
	s := &schema.Schema{Tables: []*schema.Table{{
		Name: "orders",
		Columns: []*schema.Column{
			{Name: "rank", Type: &schema.ColumnType{Type: &schema.IntegerType{T: "int"}}},
		},
	}}}
	issues := checkReservedNames(s, SemVer{Major: 8, Minor: 0, Patch: 36})
	if len(issues) == 0 {
		t.Fatal("expected reserved word warning")
	}
}

func TestMaskDSN(t *testing.T) {
	d := MaskDSN(dto.ConnConfig{User: "u", Password: "secret", Host: "h", Port: 3306, Database: "db"})
	if d == "" || contains(d, "secret") {
		t.Fatalf("password leaked in %q", d)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()))
}
