package engine

import (
	"testing"

	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

func TestPruneUtf8CharsetModifyTable(t *testing.T) {
	changes := []schema.Change{
		&schema.ModifyTable{
			T: &schema.Table{Name: "t"},
			Changes: []schema.Change{
				&schema.ModifyAttr{
					From: &schema.Charset{V: "utf8"},
					To:   &schema.Charset{V: "utf8mb3"},
				},
				&schema.ModifyAttr{
					From: &schema.Collation{V: "utf8_general_ci"},
					To:   &schema.Collation{V: "utf8mb3_general_ci"},
				},
			},
		},
	}
	got := pruneNoopChanges(changes, dto.DefaultNormalizeOptions())
	if len(got) != 0 {
		t.Fatalf("expected identical utf8 aliases to be pruned, got %#v", got)
	}
}

func TestPruneKeepsRealColumnChange(t *testing.T) {
	changes := []schema.Change{
		&schema.ModifyTable{
			T: &schema.Table{Name: "t"},
			Changes: []schema.Change{
				&schema.ModifyColumn{
					From:   &schema.Column{Name: "c", Type: &schema.ColumnType{Raw: "int", Type: &schema.IntegerType{T: "int"}}},
					To:     &schema.Column{Name: "c", Type: &schema.ColumnType{Raw: "bigint", Type: &schema.IntegerType{T: "bigint"}}},
					Change: schema.ChangeType,
				},
			},
		},
	}
	got := pruneNoopChanges(changes, dto.DefaultNormalizeOptions())
	if len(got) != 1 {
		t.Fatalf("expected real type change kept, got %#v", got)
	}
}

func TestPruneCharsetOnlyColumnWhenDescEqual(t *testing.T) {
	col := &schema.Column{
		Name: "c",
		Type: &schema.ColumnType{Raw: "varchar(10)", Type: &schema.StringType{T: "varchar", Size: 10}, Null: true},
	}
	changes := []schema.Change{
		&schema.ModifyTable{
			T: &schema.Table{Name: "t"},
			Changes: []schema.Change{
				&schema.ModifyColumn{
					From:   col,
					To:     col,
					Change: schema.ChangeCharset | schema.ChangeCollate,
				},
			},
		},
	}
	got := pruneNoopChanges(changes, dto.DefaultNormalizeOptions())
	if len(got) != 0 {
		t.Fatalf("expected charset-only identical column pruned, got %#v", got)
	}
}
