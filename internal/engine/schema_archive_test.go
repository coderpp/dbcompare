package engine

import (
	"context"
	"strings"
	"testing"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

func archiveFixture(t *testing.T, extra bool) dto.SchemaArchive {
	t.Helper()
	s := schema.New("demo").SetCharset("utf8mb4").SetCollation("utf8mb4_general_ci")
	table := schema.NewTable("account").AddColumns(schema.NewIntColumn("id", "int"))
	if extra {
		table.AddColumns(schema.NewStringColumn("name", "varchar", schema.StringSize(50)))
		table.AddIndexes(schema.NewIndex("idx_name").AddColumns(table.Columns[1]))
	}
	s.AddTables(table)
	raw, err := mysql.MarshalHCL(s)
	if err != nil {
		t.Fatal(err)
	}
	return dto.SchemaArchive{Format: 1, Database: "demo", Model: string(raw), Tables: []dto.TableSnapshot{{Table: "account", Exists: true, DDL: "CREATE TABLE account"}}, Warnings: []string{}}
}

func TestSameArchiveStructurePreservesRealChanges(t *testing.T) {
	a := archiveFixture(t, true)
	a.Tables[0].DDL = "CREATE TABLE `account` (`id` int, `name` varchar(50) COMMENT 'AUTO_INCREMENT=10') AUTO_INCREMENT=10 PARTITION BY HASH(id) PARTITIONS 2"
	b := a
	b.Tables = append([]dto.TableSnapshot(nil), a.Tables...)
	b.Tables[0].DDL = strings.Replace(a.Tables[0].DDL, ") AUTO_INCREMENT=10", ") AUTO_INCREMENT=20", 1)
	b.Tables[0].SHA256 = "new raw hash"
	b.Server.Version = "8.4.5"
	b.Warnings = []string{"different capture metadata"}
	if same, err := SameArchiveStructure(a, b); err != nil || !same {
		t.Fatalf("counter/metadata created a version: %v %v", same, err)
	}
	withoutCounter := a
	withoutCounter.Tables = append([]dto.TableSnapshot(nil), a.Tables...)
	withoutCounter.Tables[0].DDL = strings.Replace(a.Tables[0].DDL, ") AUTO_INCREMENT=10", ")", 1)
	if same, err := SameArchiveStructure(withoutCounter, b); err != nil || !same {
		t.Fatalf("first insert created a version: %v %v", same, err)
	}
	for _, kind := range []string{"comment", "partition", "column", "index", "column-order", "schema-charset"} {
		t.Run(kind, func(t *testing.T) {
			c := b
			c.Tables = append([]dto.TableSnapshot(nil), b.Tables...)
			switch kind {
			case "comment":
				c.Tables[0].DDL = strings.Replace(c.Tables[0].DDL, "'AUTO_INCREMENT=10'", "'AUTO_INCREMENT=99'", 1)
			case "partition":
				c.Tables[0].DDL = strings.Replace(c.Tables[0].DDL, "PARTITIONS 2", "PARTITIONS 3", 1)
			default:
				m, err := ReadArchiveModel(c.Model)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "column":
					m.Tables[0].AddColumns(schema.NewIntColumn("revision", "int"))
				case "index":
					m.Tables[0].Indexes = nil
				case "column-order":
					m.Tables[0].Columns[0], m.Tables[0].Columns[1] = m.Tables[0].Columns[1], m.Tables[0].Columns[0]
				case "schema-charset":
					m.SetCharset("latin1").SetCollation("latin1_swedish_ci")
				}
				raw, err := marshalArchiveModel(m)
				if err != nil {
					t.Fatal(err)
				}
				c.Model = string(raw)
			}
			if same, err := SameArchiveStructure(a, c); err != nil || same {
				t.Fatalf("real %s change skipped: %v %v", kind, same, err)
			}
		})
	}
}

func TestSnapshotCounterNormalizationKeepsQuotedAndCommentText(t *testing.T) {
	for _, text := range []string{"'AUTO_INCREMENT=10'", "'escaped\\' AUTO_INCREMENT=10'", "'doubled'' AUTO_INCREMENT=10'", "`AUTO_INCREMENT=10`", "\"AUTO_INCREMENT=10\"", "/* AUTO_INCREMENT=10 */", "-- AUTO_INCREMENT=10\n", "# AUTO_INCREMENT=10\n"} {
		ddl := "CREATE TABLE t (id int) " + text + " AUTO_INCREMENT=42"
		got := normalizeSnapshotDDL(ddl)
		if got != "CREATE TABLE t (id int) "+text {
			t.Fatalf("literal altered: %q -> %q", ddl, got)
		}
	}
}
func TestOfflineSnapshotDiffDirectionAndIndexOption(t *testing.T) {
	a, b := archiveFixture(t, false), archiveFixture(t, true)
	d, err := DiffArchives(context.Background(), a, b, dto.DefaultNormalizeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Differences) != 1 || !strings.Contains(d.SQL, "ADD COLUMN") || !strings.Contains(d.SQL, "INDEX") {
		t.Fatalf("bad forward diff: %+v", d)
	}
	d, err = DiffArchives(context.Background(), b, a, dto.DefaultNormalizeOptions())
	if err != nil || !strings.Contains(d.SQL, "DROP COLUMN") {
		t.Fatalf("bad reverse diff: %+v %v", d, err)
	}
	// Isolate an index-only change: ignoreIndexes must eliminate it without losing the stored index.
	m, err := ReadArchiveModel(b.Model)
	if err != nil {
		t.Fatal(err)
	}
	m.Tables[0].Indexes = nil
	raw, err := mysql.MarshalHCL(m)
	if err != nil {
		t.Fatal(err)
	}
	a = b
	a.Model = string(raw)
	opts := dto.DefaultNormalizeOptions()
	opts.IgnoreIndexes = true
	d, err = DiffArchives(context.Background(), a, b, opts)
	if err != nil || len(d.Differences) != 0 {
		t.Fatalf("indexes were not ignored: %+v %v", d, err)
	}
	opts.IgnoreIndexes = false
	d, err = DiffArchives(context.Background(), a, b, opts)
	if err != nil || len(d.Differences) != 1 {
		t.Fatalf("stored index was lost: %+v %v", d, err)
	}
}
func TestArchiveSourceInspectDoesNotNeedLiveDatabase(t *testing.T) {
	a := archiveFixture(t, true)
	c := &Conn{ArchiveModel: a.Model}
	s, err := c.Inspect(context.Background(), nil)
	if err != nil || len(s.Tables) != 1 {
		t.Fatal(err)
	}
	s.Tables = nil
	s, err = c.Inspect(context.Background(), nil)
	if err != nil || len(s.Tables) != 1 {
		t.Fatal("inspect must return a fresh immutable model")
	}
}

func TestArchiveModelCheckRoundTripAndRealChanges(t *testing.T) {
	// MySQL 5.7 inspection may retain an explicit utf8 alias on a text column
	// even though decoding HCL inherits it from the table instead.
	s := schema.New("demo").SetCharset("utf8").SetCollation("utf8_general_ci")
	c := schema.NewStringColumn("config", "text").SetCharset("utf8").SetCollation("utf8_general_ci")
	table := schema.NewTable("settings").SetCharset("utf8").SetCollation("utf8_general_ci").AddColumns(c)
	s.AddTables(table)
	raw, err := mysql.MarshalHCL(s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadArchiveModel(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := mysql.MarshalHCL(decoded)
	if err != nil {
		t.Fatal(err)
	}
	a, err := archiveModelCheck(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := archiveModelCheck(string(roundtrip))
	if err != nil || a != b {
		t.Fatalf("unchanged round-trip falsely changed: %v", err)
	}
	for _, kind := range []string{"column", "index", "comment", "charset"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := ReadArchiveModel(string(raw))
			switch kind {
			case "column":
				m.Tables[0].AddColumns(schema.NewIntColumn("revision", "int"))
			case "index":
				m.Tables[0].AddIndexes(schema.NewIndex("idx_config").AddColumns(m.Tables[0].Columns[0]))
			case "comment":
				m.Tables[0].SetComment("changed")
			case "charset":
				m.SetCharset("utf8mb4").SetCollation("utf8mb4_general_ci")
			}
			changed, err := mysql.MarshalHCL(m)
			if err != nil {
				t.Fatal(err)
			}
			check, err := archiveModelCheck(string(changed))
			if err != nil || check == a {
				t.Fatalf("real %s change missed: %v", kind, err)
			}
		})
	}
}

func TestArchiveModelCheckIgnoresObjectOrderAndAutoIncrement(t *testing.T) {
	s := schema.New("demo")
	a := schema.NewTable("a").AddColumns(schema.NewIntColumn("id", "int"), schema.NewIntColumn("other", "int"))
	a.AddIndexes(schema.NewIndex("idx_id").AddColumns(a.Columns[0]), schema.NewIndex("idx_other").AddColumns(a.Columns[1]))
	a.AddAttrs(&mysql.AutoIncrement{V: 10})
	b := schema.NewTable("b").AddColumns(schema.NewIntColumn("id", "int"))
	s.AddTables(a, b)
	raw, _ := mysql.MarshalHCL(s)
	before, err := archiveModelCheck(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	s.Tables[0], s.Tables[1] = s.Tables[1], s.Tables[0]
	a.Indexes[0], a.Indexes[1] = a.Indexes[1], a.Indexes[0]
	a.Attrs = []schema.Attr{&mysql.AutoIncrement{V: 11}}
	raw, _ = mysql.MarshalHCL(s)
	after, err := archiveModelCheck(string(raw))
	if err != nil || before != after {
		t.Fatalf("object order/counter falsely changed: %v", err)
	}
	// Do not sort columns: their order is meaningful structure.
	a.Columns[0], a.Columns[1] = a.Columns[1], a.Columns[0]
	raw, _ = mysql.MarshalHCL(s)
	after, err = archiveModelCheck(string(raw))
	if err != nil || before == after {
		t.Fatalf("column order change missed: %v", err)
	}
}
