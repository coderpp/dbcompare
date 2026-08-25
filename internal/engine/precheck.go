package engine

import (
	"fmt"
	"strings"
	"unicode"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

// Reserved words added/promoted in MySQL 8.0 (subset of common pitfalls).
var mysql8Reserved = map[string]bool{
	"CUBE": true, "FUNCTION": true, "GROUPING": true, "OF": true, "RECURSIVE": true,
	"CUME_DIST": true, "DENSE_RANK": true, "FIRST_VALUE": true, "GROUPS": true,
	"LAG": true, "LAST_VALUE": true, "LEAD": true, "NTH_VALUE": true, "NTILE": true,
	"OVER": true, "PERCENT_RANK": true, "RANK": true, "ROW": true, "ROWS": true,
	"ROW_NUMBER": true, "WINDOW": true, "SYSTEM": true, "EMPTY": true,
	"JSON_TABLE": true, "LATERAL": true, "INTERSECT": true,
}

// Removed spatial function aliases (5.7 → 8.0).
var removedFuncs = map[string]string{
	"AREA": "ST_Area", "ASTEXT": "ST_AsText", "ASBINARY": "ST_AsBinary",
	"ASWKB": "ST_AsBinary", "ASWKT": "ST_AsText", "GEOMFROMTEXT": "ST_GeomFromText",
	"GEOMFROMWKB": "ST_GeomFromWKB", "GEOMETRYTYPE": "ST_GeometryType",
	"SRID": "ST_SRID", "X": "ST_X", "Y": "ST_Y", "STARTPOINT": "ST_StartPoint",
	"ENDPOINT": "ST_EndPoint", "NUMPOINTS": "ST_NumPoints", "CONTAINS": "ST_Contains",
	"WITHIN": "ST_Within", "EQUALS": "ST_Equals", "INTERSECTS": "ST_Intersects",
	"OVERLAPS": "ST_Overlaps", "DISJOINT": "ST_Disjoint", "GLENGTH": "ST_Length",
	"ENCODE": "", "DECODE": "", "ENCRYPT": "", "PASSWORD": "",
}

// Precheck runs feasibility checks before apply.
func Precheck(desired, current *schema.Schema, target SemVer, changes []schema.Change) []dto.PrecheckIssue {
	var out []dto.PrecheckIssue
	if desired == nil {
		return out
	}

	for _, fi := range AnalyzeDowngradeOn(desired, target) {
		out = append(out, dto.PrecheckIssue{
			Level:   fi.Level.ToDTORisk(),
			Code:    fi.Code,
			Message: fi.Message,
			Table:   fi.Table,
		})
	}

	out = append(out, checkFKNames(desired)...)
	out = append(out, checkReservedNames(desired, target)...)
	out = append(out, checkRemovedFunctions(desired, target)...)
	out = append(out, checkIndexPrefixLimits(desired, current, changes)...)
	out = append(out, checkDropDeps(changes)...)

	return out
}

func checkFKNames(s *schema.Schema) []dto.PrecheckIssue {
	var out []dto.PrecheckIssue
	seen := map[string]string{}
	for _, t := range s.Tables {
		for _, fk := range t.ForeignKeys {
			name := fk.Symbol
			if len(name) >= 64 {
				out = append(out, dto.PrecheckIssue{
					Level: dto.RiskError, Code: "fk_name_too_long", Table: t.Name,
					Message: fmt.Sprintf("foreign key name `%s` length %d >= 64 (MySQL 8 rejects)", name, len(name)),
				})
			}
			if prev, ok := seen[name]; ok {
				out = append(out, dto.PrecheckIssue{
					Level: dto.RiskError, Code: "fk_name_dup", Table: t.Name,
					Message: fmt.Sprintf("foreign key name `%s` duplicated (also on `%s`)", name, prev),
				})
			}
			seen[name] = t.Name
		}
	}
	return out
}

func checkReservedNames(s *schema.Schema, target SemVer) []dto.PrecheckIssue {
	if !target.Is80() {
		return nil
	}
	var out []dto.PrecheckIssue
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			up := strings.ToUpper(c.Name)
			if mysql8Reserved[up] {
				out = append(out, dto.PrecheckIssue{
					Level: dto.RiskWarn, Code: "reserved_word", Table: t.Name,
					Message: fmt.Sprintf("column `%s`.`%s` is a MySQL 8 reserved word; DDL will quote it, but application SQL may break", t.Name, c.Name),
				})
			}
		}
	}
	return out
}

func checkRemovedFunctions(s *schema.Schema, target SemVer) []dto.PrecheckIssue {
	if !target.Is80() {
		return nil
	}
	var out []dto.PrecheckIssue
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			exprs := []string{}
			if c.Default != nil {
				if r, ok := c.Default.(*schema.RawExpr); ok {
					exprs = append(exprs, r.X)
				}
			}
			for _, a := range c.Attrs {
				if g, ok := a.(*schema.GeneratedExpr); ok {
					exprs = append(exprs, g.Expr)
				}
			}
			for _, ex := range exprs {
				for fn, repl := range removedFuncs {
					if containsFunc(ex, fn) {
						msg := fmt.Sprintf("expression on `%s`.`%s` uses removed function %s()", t.Name, c.Name, fn)
						if repl != "" {
							msg += "; map to " + repl + "()"
						}
						out = append(out, dto.PrecheckIssue{
							Level: dto.RiskError, Code: "removed_func", Table: t.Name, Message: msg,
						})
					}
				}
			}
		}
	}
	return out
}

func containsFunc(expr, fn string) bool {
	up := strings.ToUpper(expr)
	fn = strings.ToUpper(fn)
	idx := strings.Index(up, fn+"(")
	if idx < 0 {
		return false
	}
	if idx > 0 {
		r := rune(up[idx-1])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return false
		}
	}
	return true
}

func checkIndexPrefixLimits(desired, current *schema.Schema, changes []schema.Change) []dto.PrecheckIssue {
	var out []dto.PrecheckIssue
	// Only validate indexes this sync would create/alter — not every index on the source schema.
	for _, item := range indexesTouchedByChanges(desired, changes) {
		rowFormat := tableRowFormat(item.table)
		limit := 3072
		if rowFormat == "COMPACT" || rowFormat == "REDUNDANT" {
			limit = 767
		}
		bytes := estimateIndexBytes(item.table, item.idx)
		if bytes <= limit {
			continue
		}
		msg := fmt.Sprintf("index `%s` estimated %d bytes exceeds %d-byte limit for ROW_FORMAT=%s",
			item.idx.Name, bytes, limit, rowFormat)
		level := dto.RiskError
		// If target already has this oversize index, recreating may fail but it is not a new risk
		// introduced solely by comparing schemas — downgrade to warning.
		if currentHasIndex(current, item.table.Name, item.idx.Name) {
			level = dto.RiskWarn
			msg += " (already exists on target)"
		}
		out = append(out, dto.PrecheckIssue{
			Level: level, Code: "index_prefix_limit", Table: item.table.Name,
			Message: msg,
		})
	}
	return out
}

type touchedIndex struct {
	table *schema.Table
	idx   *schema.Index
}

func indexesTouchedByChanges(desired *schema.Schema, changes []schema.Change) []touchedIndex {
	desiredBy := tableByName(desired)
	var out []touchedIndex
	seen := map[string]bool{}
	add := func(t *schema.Table, idx *schema.Index) {
		if t == nil || idx == nil {
			return
		}
		key := t.Name + "\x00" + idx.Name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, touchedIndex{table: t, idx: idx})
	}
	for _, ch := range changes {
		switch c := ch.(type) {
		case *schema.AddTable:
			for _, idx := range c.T.Indexes {
				add(c.T, idx)
			}
			if c.T.PrimaryKey != nil {
				add(c.T, c.T.PrimaryKey)
			}
		case *schema.AddIndex:
			t := c.I.Table
			if t == nil {
				t = desiredBy[indexTableName(c.I)]
			}
			add(t, c.I)
		case *schema.ModifyIndex:
			t := c.To.Table
			if t == nil {
				t = desiredBy[indexTableName(c.To)]
			}
			add(t, c.To)
		case *schema.AddPrimaryKey:
			t := c.P.Table
			if t == nil {
				t = desiredBy[indexTableName(c.P)]
			}
			add(t, c.P)
		case *schema.ModifyPrimaryKey:
			t := c.To.Table
			if t == nil {
				t = desiredBy[indexTableName(c.To)]
			}
			add(t, c.To)
		case *schema.ModifyTable:
			tbl := c.T
			if tbl == nil {
				continue
			}
			// Prefer the desired-schema table for column charset/size lookups.
			if d := desiredBy[tbl.Name]; d != nil {
				tbl = d
			}
			for _, sub := range c.Changes {
				switch s := sub.(type) {
				case *schema.AddIndex:
					add(tbl, s.I)
				case *schema.ModifyIndex:
					add(tbl, s.To)
				case *schema.AddPrimaryKey:
					add(tbl, s.P)
				case *schema.ModifyPrimaryKey:
					add(tbl, s.To)
				}
			}
		}
	}
	return out
}

func indexTableName(idx *schema.Index) string {
	if idx == nil || idx.Table == nil {
		return ""
	}
	return idx.Table.Name
}

func currentHasIndex(current *schema.Schema, table, index string) bool {
	if current == nil {
		return false
	}
	for _, t := range current.Tables {
		if t.Name != table {
			continue
		}
		for _, idx := range t.Indexes {
			if idx.Name == index {
				return true
			}
		}
		if t.PrimaryKey != nil && (t.PrimaryKey.Name == index || index == "PRIMARY") {
			return true
		}
		return false
	}
	return false
}

func tableRowFormat(t *schema.Table) string {
	for _, a := range t.Attrs {
		if s := fmt.Sprintf("%v", a); strings.Contains(strings.ToUpper(s), "ROW_FORMAT=COMPACT") {
			return "COMPACT"
		}
		if strings.Contains(strings.ToUpper(fmt.Sprintf("%v", a)), "ROW_FORMAT=REDUNDANT") {
			return "REDUNDANT"
		}
	}
	return "DYNAMIC"
}

func estimateIndexBytes(t *schema.Table, idx *schema.Index) int {
	total := 0
	colMap := map[string]*schema.Column{}
	for _, c := range t.Columns {
		colMap[c.Name] = c
	}
	for _, p := range idx.Parts {
		if p.C == nil {
			continue
		}
		c := colMap[p.C.Name]
		if c == nil {
			c = p.C
		}
		size := columnIndexKeyBytes(c, t)
		if sub := indexPartLen(p); sub > 0 {
			// Prefix length is in characters for CHAR/VARCHAR; convert with charset width.
			mb := charsetMaxBytes(columnCharset(c, t))
			size = sub * mb
		}
		total += size
	}
	return total
}

// columnIndexKeyBytes estimates InnoDB index key storage for one column (no prefix).
func columnIndexKeyBytes(c *schema.Column, t *schema.Table) int {
	if c == nil || c.Type == nil || c.Type.Type == nil {
		return 0
	}
	switch typ := c.Type.Type.(type) {
	case *schema.StringType:
		size := typ.Size
		if size <= 0 {
			size = 255
		}
		return size * charsetMaxBytes(columnCharset(c, t))
	case *schema.BinaryType:
		if typ.Size != nil && *typ.Size > 0 {
			return *typ.Size
		}
		return 255
	case *schema.IntegerType:
		switch strings.ToLower(typ.T) {
		case "tinyint":
			return 1
		case "smallint":
			return 2
		case "mediumint":
			return 3
		case "bigint":
			return 8
		default:
			return 4 // int
		}
	case *schema.FloatType:
		if strings.Contains(strings.ToLower(typ.T), "double") {
			return 8
		}
		return 4
	case *schema.DecimalType:
		// Rough upper bound; enough to avoid false string-width inflation.
		p := typ.Precision
		if p <= 0 {
			p = 10
		}
		return p/2 + 2
	case *schema.BoolType:
		return 1
	case *schema.EnumType:
		return 2
	case *schema.TimeType:
		switch strings.ToLower(typ.T) {
		case "date":
			return 3
		case "time":
			return 3
		case "year":
			return 1
		case "timestamp":
			return 4
		default: // datetime
			return 5
		}
	case *schema.JSONType:
		// JSON cannot be fully indexed without prefix/expression; avoid utf8mb4*255 blow-up.
		return 0
	case *schema.SpatialType, *schema.UUIDType:
		return 16
	default:
		raw := strings.ToLower(c.Type.Raw)
		switch {
		case strings.Contains(raw, "bigint"):
			return 8
		case strings.Contains(raw, "int"):
			return 4
		case strings.HasPrefix(raw, "datetime"), strings.HasPrefix(raw, "timestamp"):
			return 5
		case strings.HasPrefix(raw, "date"):
			return 3
		case strings.Contains(raw, "double"), strings.Contains(raw, "bigint"):
			return 8
		case strings.Contains(raw, "float"):
			return 4
		default:
			// Unknown typed column: do not assume varchar(255) utf8mb4.
			return 0
		}
	}
}

func charsetMaxBytes(cs string) int {
	switch strings.ToLower(cs) {
	case "utf8mb4":
		return 4
	case "utf8", "utf8mb3":
		return 3
	case "ucs2", "utf16":
		return 2
	default:
		return 1
	}
}

func indexPartLen(p *schema.IndexPart) int {
	for _, a := range p.Attrs {
		if s, ok := a.(*mysql.SubPart); ok {
			return s.Len
		}
	}
	return 0
}

func columnCharset(c *schema.Column, t *schema.Table) string {
	for _, a := range c.Attrs {
		if cs, ok := a.(*schema.Charset); ok {
			return strings.ToLower(cs.V)
		}
	}
	if t != nil {
		if cs := attrCharset(t.Attrs); cs != "" {
			return strings.ToLower(cs)
		}
	}
	return "utf8mb4"
}

func checkDropDeps(changes []schema.Change) []dto.PrecheckIssue {
	var out []dto.PrecheckIssue
	dropped := map[string]bool{}
	needed := map[string]string{}
	for _, ch := range changes {
		switch c := ch.(type) {
		case *schema.DropTable:
			dropped[c.T.Name] = true
		case *schema.AddForeignKey:
			if c.F.RefTable != nil {
				needed[c.F.RefTable.Name] = c.F.Table.Name
			}
		case *schema.ModifyTable:
			for _, sub := range c.Changes {
				if fk, ok := sub.(*schema.AddForeignKey); ok && fk.F.RefTable != nil {
					needed[fk.F.RefTable.Name] = c.T.Name
				}
			}
		}
	}
	for ref, from := range needed {
		if dropped[ref] {
			out = append(out, dto.PrecheckIssue{
				Level: dto.RiskError, Code: "fk_dep_missing", Table: from,
				Message: fmt.Sprintf("table `%s` adds FK to `%s` which is selected for DROP — selection is not dependency-closed", from, ref),
			})
		}
	}
	return out
}

// HasBlockingPrechecks reports whether any ERROR/CRITICAL issues exist.
func HasBlockingPrechecks(issues []dto.PrecheckIssue, allowUnsafe bool) bool {
	for _, i := range issues {
		if i.Level == dto.RiskError || i.Level == dto.RiskCritical {
			if allowUnsafe && i.Code != "check_constraint" && i.Code != "multivalue_index" && i.Code != "expr_default" {
				continue
			}
			// CHECK / multivalue / expr_default always block unless explicitly allowed via allowUnsafe for all.
			if !allowUnsafe {
				return true
			}
			if i.Code == "check_constraint" || i.Code == "multivalue_index" || i.Code == "expr_default" {
				if !allowUnsafe {
					return true
				}
			}
		}
	}
	if allowUnsafe {
		return false
	}
	for _, i := range issues {
		if i.Level == dto.RiskError || i.Level == dto.RiskCritical {
			return true
		}
	}
	return false
}
