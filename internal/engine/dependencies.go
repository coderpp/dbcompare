package engine

import (
	"ariga.io/atlas/sql/schema"
	"fmt"
	"github.com/pp/db-compare/internal/dto"
)

// Validate explicit partial selections without silently expanding their scope.
func checkSelectionDependencies(current *schema.Schema, changes []schema.Change) []dto.PrecheckIssue {
	available := map[string]map[string]bool{}
	if current != nil {
		for _, t := range current.Tables {
			available[t.Name] = map[string]bool{}
			for _, c := range t.Columns {
				available[t.Name][c.Name] = true
			}
		}
	}
	for _, ch := range changes {
		switch c := ch.(type) {
		case *schema.AddTable:
			cols := map[string]bool{}
			for _, col := range c.T.Columns {
				cols[col.Name] = true
			}
			available[c.T.Name] = cols
		case *schema.DropTable:
			delete(available, c.T.Name)
		}
	}
	for _, ch := range changes {
		if mt, ok := ch.(*schema.ModifyTable); ok {
			if available[mt.T.Name] == nil {
				available[mt.T.Name] = map[string]bool{}
			}
			for _, sub := range mt.Changes {
				switch c := sub.(type) {
				case *schema.AddColumn:
					available[mt.T.Name][c.C.Name] = true
				case *schema.DropColumn:
					delete(available[mt.T.Name], c.C.Name)
				}
			}
		}
	}
	out := []dto.PrecheckIssue{}
	seen := map[string]bool{}
	missing := func(table, column, reason string) {
		if !available[table][column] {
			key := table + "." + column + "/" + reason
			if !seen[key] {
				seen[key] = true
				out = append(out, dto.PrecheckIssue{Level: dto.RiskError, Code: "selection_dependency", Table: table, Message: fmt.Sprintf("%s 依赖 %s.%s，但该字段在目标库及本次选择中不存在；请同时勾选所需表/字段", reason, table, column)})
			}
		}
	}
	index := func(table string, idx *schema.Index) {
		for _, part := range idx.Parts {
			if part.C != nil {
				missing(table, part.C.Name, "索引 "+idx.Name)
			}
		}
	}
	fk := func(table string, f *schema.ForeignKey) {
		for _, c := range f.Columns {
			missing(table, c.Name, "外键 "+f.Symbol)
		}
		if f.RefTable != nil {
			for _, c := range f.RefColumns {
				missing(f.RefTable.Name, c.Name, "外键 "+f.Symbol)
			}
		}
	}
	for _, ch := range changes {
		switch c := ch.(type) {
		case *schema.AddTable:
			for _, i := range c.T.Indexes {
				index(c.T.Name, i)
			}
			for _, f := range c.T.ForeignKeys {
				fk(c.T.Name, f)
			}
		case *schema.ModifyTable:
			for _, sub := range c.Changes {
				switch v := sub.(type) {
				case *schema.AddIndex:
					index(c.T.Name, v.I)
				case *schema.ModifyIndex:
					index(c.T.Name, v.To)
				case *schema.AddPrimaryKey:
					index(c.T.Name, v.P)
				case *schema.ModifyPrimaryKey:
					index(c.T.Name, v.To)
				case *schema.AddForeignKey:
					fk(c.T.Name, v.F)
				case *schema.ModifyForeignKey:
					fk(c.T.Name, v.To)
				}
			}
		}
	}
	return out
}
