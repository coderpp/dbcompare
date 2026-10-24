package engine

import (
	"testing"

	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

func TestTableHasVisibleDiffFalseWhenEqual(t *testing.T) {
	col := &schema.Column{
		Name: "id",
		Type: &schema.ColumnType{Raw: "int", Type: &schema.IntegerType{T: "int"}, Null: false},
	}
	src := &schema.Table{
		Name:    "t",
		Columns: []*schema.Column{col},
		Attrs:   []schema.Attr{&schema.Charset{V: "utf8"}, &schema.Collation{V: "utf8_general_ci"}},
	}
	dst := &schema.Table{
		Name:    "t",
		Columns: []*schema.Column{col},
		Attrs:   []schema.Attr{&schema.Charset{V: "utf8mb3"}, &schema.Collation{V: "utf8mb3_general_ci"}},
	}
	if tableHasVisibleDiff(src, dst, dto.DefaultNormalizeOptions()) {
		t.Fatal("utf8 aliases should not count as visible table diff")
	}
}

func TestPruneInvisibleModifies(t *testing.T) {
	col := &schema.Column{
		Name: "id",
		Type: &schema.ColumnType{Raw: "int", Type: &schema.IntegerType{T: "int"}},
	}
	srcSch := &schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{col}}}}
	dstSch := &schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{col}}}}
	changes := []schema.Change{
		&schema.ModifyTable{
			T:       &schema.Table{Name: "t"},
			Changes: []schema.Change{&schema.ModifyAttr{From: &schema.Charset{V: "utf8"}, To: &schema.Charset{V: "utf8mb3"}}},
		},
	}
	got := pruneInvisibleModifies(changes, dstSch, srcSch, dto.DefaultNormalizeOptions())
	if len(got) != 0 {
		t.Fatalf("expected invisible modify pruned, got %#v", got)
	}
}

func TestIntDisplayWidthNotVisibleDiff(t *testing.T) {
	opts := dto.DefaultNormalizeOptions()
	src := &schema.Column{
		Name:  "F_DeleteMark",
		Type:  &schema.ColumnType{Raw: "int", Type: &schema.IntegerType{T: "int"}, Null: true},
		Attrs: []schema.Attr{&schema.Comment{Text: "删除标志"}},
	}
	dst := &schema.Column{
		Name:  "F_DeleteMark",
		Type:  &schema.ColumnType{Raw: "int(1)", Type: &schema.IntegerType{T: "int"}, Null: true},
		Attrs: []schema.Attr{&schema.Comment{Text: "删除标志"}},
	}
	Normalize(&schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{src}}}}, opts)
	Normalize(&schema.Schema{Tables: []*schema.Table{{Name: "t", Columns: []*schema.Column{dst}}}}, opts)
	if colDesc(src, opts) != colDesc(dst, opts) {
		t.Fatalf("int vs int(1) should match after strip: %q vs %q", colDesc(src, opts), colDesc(dst, opts))
	}
	if tableHasVisibleDiff(
		&schema.Table{Name: "t", Columns: []*schema.Column{src}},
		&schema.Table{Name: "t", Columns: []*schema.Column{dst}},
		opts,
	) {
		t.Fatal("int display width should not be a visible diff")
	}
}

func TestTableAttributeDirectionMatchesSyncDirection(t *testing.T) {
	for _, tc := range []struct{ source, target, kind string }{{"utf8mb4", "", "add"}, {"", "utf8mb4", "drop"}, {"a", "b", "modify"}, {"a", "a", "same"}} {
		got := appendTableAttrDiffs(nil, "COMMENT", tc.source, tc.target)
		if len(got) != 1 || got[0].Kind != tc.kind {
			t.Fatalf("source=%q target=%q: got %+v, want %s", tc.source, tc.target, got, tc.kind)
		}
	}
}
