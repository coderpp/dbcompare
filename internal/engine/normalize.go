package engine

import (
	"fmt"
	"strings"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

// Normalize applies cross-version noise suppression on an Atlas schema in place.
func Normalize(s *schema.Schema, opts dto.NormalizeOptions) {
	if s == nil {
		return
	}
	unifyAttrs(s.Attrs, opts)
	for _, t := range s.Tables {
		normalizeTable(t, opts)
	}
}

func normalizeTable(t *schema.Table, opts dto.NormalizeOptions) {
	unifyAttrs(t.Attrs, opts)
	if opts.HideGIPK {
		stripGIPK(t)
	}
	if opts.IgnoreAutoIncrement {
		stripAutoIncAttr(t)
	}
	if !opts.CompareRowFormat {
		stripCreateOptions(t)
	}
	// Atlas does not plan PARTITION BY; leftover CreateOptions like "partitioned"
	// produce invalid DDL. Always strip partition markers from attrs.
	stripPartitionMarkers(t)

	for _, c := range t.Columns {
		normalizeColumn(c, opts)
	}
	for _, idx := range t.Indexes {
		normalizeIndex(idx, opts)
	}
}

func stripPartitionMarkers(t *schema.Table) {
	attrs := t.Attrs[:0]
	for _, a := range t.Attrs {
		if co, ok := a.(*mysql.CreateOptions); ok {
			up := strings.ToUpper(co.V)
			if strings.Contains(up, "PARTITION") {
				continue
			}
			// Keep non-partition create options (ROW_FORMAT etc.) when comparing row format.
			attrs = append(attrs, a)
			continue
		}
		attrs = append(attrs, a)
	}
	t.Attrs = attrs
}

// IsPartitioned reports whether a table looks partitioned from attrs / create options.
func IsPartitioned(t *schema.Table) bool {
	for _, a := range t.Attrs {
		if co, ok := a.(*mysql.CreateOptions); ok {
			if strings.Contains(strings.ToUpper(co.V), "PARTITION") {
				return true
			}
		}
		if strings.Contains(strings.ToUpper(fmt.Sprintf("%T %v", a, a)), "PARTITION") {
			return true
		}
	}
	return false
}

func normalizeColumn(c *schema.Column, opts dto.NormalizeOptions) {
	if opts.StripIntDisplayWidth {
		stripIntDisplayWidth(c)
	}
	unifyUTF8(c, opts)
	normalizeDefault(c)
}

func stripIntDisplayWidth(c *schema.Column) {
	if c == nil || c.Type == nil {
		return
	}
	zerofill := false
	attrs := c.Attrs[:0]
	for _, a := range c.Attrs {
		if _, ok := a.(*mysql.ZeroFill); ok {
			zerofill = true
			attrs = append(attrs, a)
			continue
		}
		if _, ok := a.(*mysql.DisplayWidth); ok {
			continue // drop display width attr
		}
		attrs = append(attrs, a)
	}
	c.Attrs = attrs
	if zerofill {
		return
	}
	base := ""
	switch t := c.Type.Type.(type) {
	case *schema.IntegerType:
		base = t.T
	case *schema.TimeType:
		if strings.EqualFold(t.T, "year") {
			c.Type.Raw = "year"
			return
		}
	}
	if c.Type.Raw != "" || base != "" {
		c.Type.Raw = stripWidthFromRaw(c.Type.Raw, base)
	}
}

func stripWidthFromRaw(raw, base string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	base = strings.ToLower(strings.TrimSpace(base))
	if raw == "" {
		return base
	}
	// Longer names first so "integer" / "tinyint" are not partially matched as "int".
	prefixes := []string{"tinyint", "smallint", "mediumint", "bigint", "integer", "int", "year"}
	for _, p := range prefixes {
		if strings.HasPrefix(raw, p+"(") {
			rest := raw[len(p):]
			if i := strings.Index(rest, ")"); i >= 0 {
				return strings.TrimSpace(p + rest[i+1:])
			}
		}
	}
	return raw
}

func unifyAttrs(attrs []schema.Attr, opts dto.NormalizeOptions) {
	for _, a := range attrs {
		switch v := a.(type) {
		case *schema.Charset:
			if opts.UnifyUTF8Aliases {
				v.V = aliasCharset(v.V)
			}
		case *schema.Collation:
			v.V = normalizeCollation(v.V, opts)
		}
	}
}

func unifyUTF8(c *schema.Column, opts dto.NormalizeOptions) {
	unifyAttrs(c.Attrs, opts)
	// Also rewrite charset/collation tokens embedded in type Raw.
	if c.Type != nil && c.Type.Raw != "" {
		c.Type.Raw = rewriteCharsetInRaw(c.Type.Raw)
		c.Type.Raw = rewriteCollationInRaw(c.Type.Raw, opts)
	}
}

func aliasCharset(cs string) string {
	cs = strings.ToLower(cs)
	if cs == "utf8" {
		return "utf8mb3"
	}
	return cs
}

func aliasCollation(col string) string {
	col = strings.ToLower(col)
	if strings.HasPrefix(col, "utf8_") {
		return "utf8mb3_" + strings.TrimPrefix(col, "utf8_")
	}
	return col
}

// normalizeCollation applies utf8→utf8mb3 renaming and optional 5.7↔8.0 CI collation unification.
func normalizeCollation(col string, opts dto.NormalizeOptions) string {
	col = strings.ToLower(strings.TrimSpace(col))
	if opts.UnifyUTF8Aliases {
		col = aliasCollation(col)
	}
	if opts.UnifyUTF8MB4CICollations {
		col = unifyUTF8MB4CICollation(col)
	}
	return col
}

// unifyUTF8MB4CICollation maps MySQL 8 default CI collation to the 5.7-era CI collation
// for comparison only (both sides rewritten the same way → no false diff).
func unifyUTF8MB4CICollation(col string) string {
	switch col {
	case "utf8mb4_0900_ai_ci", "utf8mb4_general_ci":
		return "utf8mb4_general_ci"
	}
	return col
}

func rewriteCharsetInRaw(raw string) string {
	r := strings.ReplaceAll(raw, "CHARACTER SET utf8 ", "CHARACTER SET utf8mb3 ")
	r = strings.ReplaceAll(r, "character set utf8 ", "character set utf8mb3 ")
	r = strings.ReplaceAll(r, "COLLATE utf8_", "COLLATE utf8mb3_")
	r = strings.ReplaceAll(r, "collate utf8_", "collate utf8mb3_")
	return r
}

func rewriteCollationInRaw(raw string, opts dto.NormalizeOptions) string {
	if !opts.UnifyUTF8MB4CICollations {
		return raw
	}
	r := raw
	for _, from := range []string{"utf8mb4_0900_ai_ci", "UTF8MB4_0900_AI_CI"} {
		r = strings.ReplaceAll(r, from, "utf8mb4_general_ci")
	}
	return r
}

func normalizeDefault(c *schema.Column) {
	if c.Default == nil {
		return
	}
	switch d := c.Default.(type) {
	case *schema.Literal:
		d.V = normalizeLiteral(d.V, c)
	case *schema.RawExpr:
		d.X = normalizeExpr(d.X)
	}
}

func normalizeLiteral(v string, c *schema.Column) string {
	v = strings.TrimSpace(v)
	// Unwrap quotes for comparison stability.
	if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
		inner := v[1 : len(v)-1]
		if isNumericColumn(c) {
			return strings.TrimLeft(inner, "+")
		}
		return inner
	}
	if strings.HasPrefix(strings.ToLower(v), "b'") || strings.HasPrefix(strings.ToLower(v), "0b") {
		return strings.ToLower(v)
	}
	return v
}

func isNumericColumn(c *schema.Column) bool {
	switch c.Type.Type.(type) {
	case *schema.IntegerType, *schema.FloatType, *schema.DecimalType:
		return true
	default:
		return false
	}
}

func normalizeExpr(x string) string {
	x = strings.TrimSpace(x)
	// Strip charset introducers like _utf8mb4' '
	x = strings.ReplaceAll(x, "_utf8mb4", "")
	x = strings.ReplaceAll(x, "_utf8mb3", "")
	x = strings.ReplaceAll(x, "_utf8", "")
	// Undo I_S double-escaping of quotes.
	x = strings.ReplaceAll(x, `\'`, `'`)
	x = strings.ReplaceAll(x, `\\`, `\`)
	// Synonyms. Unwrap DEFAULT (CURRENT_TIMESTAMP) so 5.7 DDL stays valid.
	if canon, ok := canonicalTimestampDefault(x); ok {
		return canon
	}
	return x
}

func canonicalTimestampDefault(x string) (string, bool) {
	if !isNativeTimestampDefault(x) {
		return "", false
	}
	inner := strings.ToUpper(strings.ReplaceAll(unwrapOuterParens(strings.TrimSpace(x)), " ", ""))
	prec := ""
	switch {
	case strings.HasPrefix(inner, "CURRENT_TIMESTAMP("):
		prec = inner[len("CURRENT_TIMESTAMP(") : len(inner)-1]
	case strings.HasPrefix(inner, "NOW("):
		prec = inner[len("NOW(") : len(inner)-1]
	case strings.HasPrefix(inner, "LOCALTIME("):
		prec = inner[len("LOCALTIME(") : len(inner)-1]
	case strings.HasPrefix(inner, "LOCALTIMESTAMP("):
		prec = inner[len("LOCALTIMESTAMP(") : len(inner)-1]
	}
	if prec == "" {
		return "CURRENT_TIMESTAMP", true
	}
	return "CURRENT_TIMESTAMP(" + prec + ")", true
}

func stripAutoIncAttr(t *schema.Table) {
	attrs := t.Attrs[:0]
	for _, a := range t.Attrs {
		if _, ok := a.(*mysql.AutoIncrement); ok {
			continue
		}
		attrs = append(attrs, a)
	}
	t.Attrs = attrs
}

func stripCreateOptions(t *schema.Table) {
	attrs := t.Attrs[:0]
	for _, a := range t.Attrs {
		if _, ok := a.(*mysql.CreateOptions); ok {
			continue
		}
		attrs = append(attrs, a)
	}
	t.Attrs = attrs
}

func stripGIPK(t *schema.Table) {
	// Remove my_row_id invisible auto-inc PK column if present.
	cols := t.Columns[:0]
	for _, c := range t.Columns {
		if c.Name == "my_row_id" && hasInvisible(c) {
			continue
		}
		cols = append(cols, c)
	}
	t.Columns = cols
}

func hasInvisible(c *schema.Column) bool {
	for _, a := range c.Attrs {
		// Atlas may represent invisibility via a dedicated attr type across versions.
		name := fmt.Sprintf("%T", a)
		if strings.Contains(name, "Invisible") {
			return true
		}
	}
	return false
}

func normalizeIndex(idx *schema.Index, opts dto.NormalizeOptions) {
	_ = opts
	// No-op placeholder: index rename detection happens at classify time.
}
