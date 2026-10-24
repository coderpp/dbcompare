package engine

import (
	"context"
	"testing"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
	"github.com/pp/db-compare/internal/dto"
)

type verifyDriver struct {
	migrate.Driver
	inspected *schema.Schema
	changes   []schema.Change
}

func (d verifyDriver) InspectSchema(context.Context, string, *schema.InspectOptions) (*schema.Schema, error) {
	return d.inspected, nil
}
func (d verifyDriver) SchemaDiff(*schema.Schema, *schema.Schema, ...schema.DiffOption) ([]schema.Change, error) {
	return d.changes, nil
}

func TestPostVerifyFiltersSameNoiseAsCompare(t *testing.T) {
	for _, realChange := range []bool{false, true} {
		name := "normalization noise only"
		if realChange {
			name = "real difference remains"
		}
		t.Run(name, func(t *testing.T) {
			src := schema.New("source").AddTables(schema.NewTable("same"))
			dst := schema.New("target").AddTables(schema.NewTable("same"))
			changes := []schema.Change{&schema.ModifyTable{T: dst.Tables[0], Changes: []schema.Change{
				&schema.ModifyAttr{From: &schema.Charset{V: "utf8"}, To: &schema.Charset{V: "utf8mb3"}},
			}}, &schema.DropTable{T: schema.NewTable("extra")}}
			if realChange {
				changes = append(changes, &schema.AddTable{T: schema.NewTable("missing")})
			}
			sess := &Session{Source: &Conn{Driver: verifyDriver{inspected: src}}, Target: &Conn{Driver: verifyDriver{inspected: dst, changes: changes}}, Normalize: dto.DefaultNormalizeOptions()}
			got, err := New().verifySession(context.Background(), sess)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if realChange {
				want = 1
			}
			if got.Remaining != want {
				t.Fatalf("remaining=%d, want %d: %s", got.Remaining, want, got.Summary)
			}
		})
	}
}
