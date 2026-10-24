package engine

import (
	"fmt"
	"strings"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// marshalArchiveModel preserves explicit zero lengths. Atlas v1.3.0 omits
// zero-valued type arguments, producing invalid varchar()/varbinary() and
// losing the length of char(0)/binary(0). Patch only column type expressions,
// using the inspected model as the source of truth; never modify the model.
func marshalArchiveModel(s *schema.Schema) ([]byte, error) {
	raw, err := mysql.MarshalHCL(s)
	if err != nil {
		return nil, err
	}
	zeroLengths := make(map[string]map[string]string)
	for _, table := range s.Tables {
		for _, column := range table.Columns {
			var name string
			switch typ := column.Type.Type.(type) {
			case *schema.StringType:
				if typ.Size == 0 && (strings.EqualFold(typ.T, mysql.TypeVarchar) || strings.EqualFold(typ.T, mysql.TypeChar)) {
					name = strings.ToLower(typ.T)
				}
			case *schema.BinaryType:
				if typ.Size != nil && *typ.Size == 0 && (strings.EqualFold(typ.T, mysql.TypeVarBinary) || strings.EqualFold(typ.T, mysql.TypeBinary)) {
					name = strings.ToLower(typ.T)
				}
			}
			if name != "" {
				if zeroLengths[table.Name] == nil {
					zeroLengths[table.Name] = make(map[string]string)
				}
				zeroLengths[table.Name][column.Name] = name
			}
		}
	}
	if len(zeroLengths) == 0 {
		return raw, nil
	}
	f, diags := hclwrite.ParseConfig(raw, "snapshot.hcl", hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("解析快照结构模型: %s", diags.Error())
	}
	for _, table := range f.Body().Blocks() {
		if table.Type() != "table" || len(table.Labels()) != 1 {
			continue
		}
		columns := zeroLengths[table.Labels()[0]]
		for _, column := range table.Body().Blocks() {
			if column.Type() != "column" || len(column.Labels()) != 1 {
				continue
			}
			if name := columns[column.Labels()[0]]; name != "" {
				column.Body().SetAttributeRaw("type", hclwrite.TokensForFunctionCall(name, hclwrite.TokensForValue(cty.NumberIntVal(0))))
			}
		}
	}
	return f.Bytes(), nil
}
