package engine

import (
	"context"
	"testing"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
)

type selectionPlanner struct{ migrate.Driver }

func (selectionPlanner) PlanChanges(context.Context, string, []schema.Change, ...migrate.PlanOption) (*migrate.Plan, error) {
	return &migrate.Plan{}, nil
}

func TestSelectionDoesNotIncludeUnselectedTables(t *testing.T) {
	changes := []schema.Change{&schema.ModifyTable{T: schema.NewTable("syjc_contract"), Changes: []schema.Change{
		&schema.AddColumn{C: schema.NewIntColumn("charge_mode", "int")},
		&schema.AddColumn{C: schema.NewIntColumn("pricing_plan", "int")},
	}}}
	for _, name := range []string{"syjc_finance_account", "syjc_finance_refund"} {
		changes = append(changes, &schema.AddTable{T: schema.NewTable(name)})
	}
	items, _ := classify(context.Background(), selectionPlanner{}, changes, SemVer{}, SemVer{}, nil)
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ID] {
			t.Errorf("duplicate change ID %q for table %s", item.ID, item.Table)
		}
		seen[item.ID] = true
	}
	sess := &Session{Changes: changes}
	for _, table := range []string{"syjc_contract", "syjc_finance_account", "syjc_finance_refund"} {
		var ids []string
		for _, item := range items {
			if item.Table == table {
				ids = append(ids, item.ID)
			}
		}
		selected := sess.SelectChanges(ids)
		if len(selected) != 1 {
			t.Errorf("selecting %s selected %d tables, want 1", table, len(selected))
			continue
		}
		switch c := selected[0].(type) {
		case *schema.ModifyTable:
			if c.T.Name != table {
				t.Errorf("selected %s instead of %s", c.T.Name, table)
			}
		case *schema.AddTable:
			if c.T.Name != table {
				t.Errorf("selected %s instead of %s", c.T.Name, table)
			}
		}
	}
	if got := sess.SelectChanges(nil); len(got) != 0 {
		t.Fatalf("empty selection returned %d changes", len(got))
	}
}

func TestSelectionOfChildKeepsOnlyThatChild(t *testing.T) {
	changes := []schema.Change{&schema.AddTable{T: schema.NewTable("unselected")}, &schema.ModifyTable{T: schema.NewTable("chosen"), Changes: []schema.Change{
		&schema.AddColumn{C: schema.NewIntColumn("first", "int")},
		&schema.AddColumn{C: schema.NewIntColumn("second", "int")},
	}}}
	items, _ := classify(context.Background(), selectionPlanner{}, changes, SemVer{}, SemVer{}, nil)
	var childID string
	for _, item := range items {
		if item.Table == "chosen" && item.ObjectName == "second" {
			childID = item.ID
		}
	}
	sess := &Session{Changes: changes}
	for i := 0; i < 2; i++ {
		got := sess.SelectChanges([]string{childID})
		if len(got) != 1 {
			t.Fatalf("got %d tables", len(got))
		}
		mt := got[0].(*schema.ModifyTable)
		if len(mt.Changes) != 1 {
			t.Fatalf("got %d children, want 1", len(mt.Changes))
		}
		if mt.Changes[0].(*schema.AddColumn).C.Name != "second" {
			t.Fatal("wrong child selected")
		}
	}
	if len(changes[1].(*schema.ModifyTable).Changes) != 2 {
		t.Fatal("session changes mutated")
	}
}
