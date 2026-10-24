package engine

import (
	"context"
	"strings"
	"testing"

	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

func TestArchiveZeroLengthTypes(t *testing.T) {
	for _, name := range []string{"varchar", "char", "varbinary", "binary"} {
		t.Run(name, func(t *testing.T) {
			zero := 0
			var typ schema.Type = &schema.StringType{T: name, Size: 0}
			if strings.Contains(name, "binary") {
				typ = &schema.BinaryType{T: name, Size: &zero}
			}
			column := schema.NewColumn("zero").SetType(typ).SetComment("literal varchar() and varbinary() must survive")
			regular := schema.NewStringColumn("regular", "varchar", schema.StringSize(50))
			s := schema.New("demo").AddTables(schema.NewTable("example").AddColumns(column, regular))
			raw, err := marshalArchiveModel(s)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := ReadArchiveModel(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			var length int
			switch got := decoded.Tables[0].Columns[0].Type.Type.(type) {
			case *schema.StringType:
				length = got.Size
			case *schema.BinaryType:
				if got.Size == nil {
					t.Fatal("explicit zero length lost")
				}
				length = *got.Size
			default:
				t.Fatal("unexpected decoded type")
			}
			if length != 0 || column.Type.Type != typ || !strings.Contains(string(raw), "literal varchar() and varbinary() must survive") {
				t.Fatalf("length, original model or comment changed: %s", raw)
			}
			check, err := archiveModelCheck(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			roundtrip, err := marshalArchiveModel(decoded)
			if err != nil {
				t.Fatal(err)
			}
			again, err := archiveModelCheck(string(roundtrip))
			if err != nil || check != again {
				t.Fatalf("zero-length model is unstable: %v", err)
			}
			// A real length change must remain visible in offline diff/SQL.
			var changedType schema.Type = &schema.StringType{T: name, Size: 1}
			if strings.Contains(name, "binary") {
				one := 1
				changedType = &schema.BinaryType{T: name, Size: &one}
			}
			decoded.Tables[0].Columns[0].SetType(changedType)
			changed, err := marshalArchiveModel(decoded)
			if err != nil {
				t.Fatal(err)
			}
			diff, err := DiffArchives(context.Background(), dto.SchemaArchive{Model: string(raw)}, dto.SchemaArchive{Model: string(changed)}, dto.DefaultNormalizeOptions())
			expectedSQL := name + "(1)"
			if name == "binary" {
				expectedSQL = "binary NOT NULL" // MySQL defaults binary to length 1.
			}
			if err != nil || len(diff.Differences) != 1 || !strings.Contains(diff.SQL, expectedSQL) {
				t.Fatalf("length change missed: %+v %v", diff, err)
			}
		})
	}
}
