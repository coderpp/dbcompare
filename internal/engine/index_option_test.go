package engine

import (
	"context"
	"encoding/json"
	"testing"

	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

func TestIndexComparisonIsEnabledForOldProfiles(t *testing.T) {
	var opts dto.NormalizeOptions
	if err := json.Unmarshal([]byte(`{"ignoreAutoIncrement":true}`), &opts); err != nil {
		t.Fatal(err)
	}
	if opts.IgnoreIndexes || dto.DefaultNormalizeOptions().IgnoreIndexes {
		t.Fatal("indexes must be compared by default, including old profiles")
	}
}
func TestIgnoreIndexesPreservesColumnsAndWholeTables(t *testing.T) {
	id := schema.NewIntColumn("id", "int")
	extra := schema.NewIntColumn("extra", "int")
	idx := schema.NewIndex("idx_id").AddColumns(id)
	mt := &schema.ModifyTable{T: schema.NewTable("existing"), Changes: []schema.Change{
		&schema.AddColumn{C: extra}, &schema.AddIndex{I: idx}, &schema.DropIndex{I: idx}, &schema.ModifyIndex{From: idx, To: idx}, &schema.RenameIndex{From: idx, To: idx}, &schema.AddPrimaryKey{P: idx}, &schema.DropPrimaryKey{P: idx}, &schema.ModifyPrimaryKey{From: idx, To: idx},
	}}
	added := &schema.AddTable{T: schema.NewTable("new_table").AddColumns(id).AddIndexes(idx).SetPrimaryKey(schema.NewPrimaryKey(id))}
	dropped := &schema.DropTable{T: schema.NewTable("extra_table")}
	changes := []schema.Change{mt, added, dropped}
	opts := dto.DefaultNormalizeOptions()
	opts.IgnoreIndexes = true
	got := filterIndexChanges(changes, opts)
	if len(got) != 3 || len(got[0].(*schema.ModifyTable).Changes) != 1 {
		t.Fatalf("unexpected filtered changes: %+v", got)
	}
	if _, ok := got[0].(*schema.ModifyTable).Changes[0].(*schema.AddColumn); !ok {
		t.Fatal("column changes lost")
	}
	if len(mt.Changes) != 8 {
		t.Fatal("stored session changes mutated")
	}
	if got[1] != added || len(added.T.Indexes) != 1 || added.T.PrimaryKey == nil {
		t.Fatal("new tables must retain full definitions")
	}
	only := &schema.ModifyTable{T: mt.T, Changes: mt.Changes[1:]}
	if len(filterIndexChanges([]schema.Change{only}, opts)) != 0 {
		t.Fatal("index-only tables must disappear when disabled")
	}
	opts.IgnoreIndexes = false
	if len(filterIndexChanges([]schema.Change{only}, opts)) != 1 {
		t.Fatal("index changes should remain enabled")
	}
}
func TestIgnoreIndexesAppliesToDetailsAndVerification(t *testing.T) {
	a := schema.NewIntColumn("id", "int")
	b := schema.NewIntColumn("id", "int")
	src := schema.NewTable("t").AddColumns(a).AddIndexes(schema.NewIndex("idx_id").AddColumns(a))
	dst := schema.NewTable("t").AddColumns(b)
	source, target := schema.New("source").AddTables(src), schema.New("target").AddTables(dst)
	opts := dto.DefaultNormalizeOptions()
	opts.IgnoreIndexes = true
	raw := []schema.Change{&schema.ModifyTable{T: dst, Changes: []schema.Change{&schema.AddIndex{I: src.Indexes[0]}}}}
	e := New()
	sess := &Session{ID: "test", Source: &Conn{Driver: verifyDriver{inspected: source}}, Target: &Conn{Driver: verifyDriver{inspected: target, changes: raw}}, SourceSch: source, TargetSch: target, Normalize: opts}
	e.Sessions.Put(sess)
	details, err := e.TableDiff(sess.ID, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(details.Indexes) != 0 {
		t.Fatal("ignored indexes still shown in existing-table details")
	}
	run := &dto.SyncRun{Selected: []dto.ChangeItem{{Table: "t", ObjectType: "table"}}}
	if err := e.verifyRun(context.Background(), sess, run); err != nil {
		t.Fatal(err)
	}
	if !run.SelectionComplete || len(run.Remaining) != 0 {
		t.Fatalf("ignored index reported as remaining: %+v", run)
	}
	sess.Normalize.IgnoreIndexes = false
	details, err = e.TableDiff(sess.ID, "t")
	if err != nil || len(details.Indexes) != 1 {
		t.Fatal("enabled index diff missing")
	}
}
