package engine

import (
	"strings"

	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

// pruneNoopChanges drops Atlas diffs that are equivalent after normalization
// (utf8/utf8mb3 aliases, inherited charset, comment-only when ignored, empty ModifyTable).
func pruneNoopChanges(changes []schema.Change, opts dto.NormalizeOptions) []schema.Change {
	out := make([]schema.Change, 0, len(changes))
	for _, ch := range changes {
		switch c := ch.(type) {
		case *schema.ModifyTable:
			c.Changes = pruneNested(c.Changes, opts)
			if len(c.Changes) == 0 {
				continue
			}
			out = append(out, c)
		case *schema.ModifySchema:
			c.Changes = pruneNested(c.Changes, opts)
			if len(c.Changes) == 0 {
				continue
			}
			out = append(out, c)
		default:
			if isNoopChange(ch, opts) {
				continue
			}
			out = append(out, ch)
		}
	}
	return out
}

func pruneNested(changes []schema.Change, opts dto.NormalizeOptions) []schema.Change {
	out := make([]schema.Change, 0, len(changes))
	for _, ch := range changes {
		if isNoopChange(ch, opts) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func isNoopChange(ch schema.Change, opts dto.NormalizeOptions) bool {
	switch c := ch.(type) {
	case *schema.ModifyAttr:
		if opts.IgnoreCommentsOnly {
			if _, ok := c.From.(*schema.Comment); ok {
				return true
			}
			if _, ok := c.To.(*schema.Comment); ok {
				return true
			}
		}
		return attrsEquivalent(c.From, c.To)
	case *schema.AddAttr:
		if opts.IgnoreCommentsOnly {
			if _, ok := c.A.(*schema.Comment); ok {
				return true
			}
		}
		return false
	case *schema.DropAttr:
		if opts.IgnoreCommentsOnly {
			if _, ok := c.A.(*schema.Comment); ok {
				return true
			}
		}
		return false
	case *schema.ModifyColumn:
		return isNoopModifyColumn(c, opts)
	case *schema.ModifyIndex:
		return isNoopModifyIndex(c, opts)
	default:
		return false
	}
}

func isNoopModifyColumn(c *schema.ModifyColumn, opts dto.NormalizeOptions) bool {
	if c == nil || c.From == nil || c.To == nil {
		return false
	}
	noise := schema.ChangeCharset | schema.ChangeCollate | schema.ChangeAttr
	if opts.IgnoreCommentsOnly {
		noise |= schema.ChangeComment
	}
	if c.Change&^noise != 0 {
		// Still treat display-width-only type noise as noop when stripping int width.
		if opts.StripIntDisplayWidth && colDesc(c.From, opts) == colDesc(c.To, opts) {
			return true
		}
		return false
	}
	if c.Change == 0 {
		return true
	}
	if c.Change&schema.ChangeComment != 0 && !opts.IgnoreCommentsOnly {
		return colDesc(c.From, opts) == colDesc(c.To, opts)
	}
	return colDesc(c.From, opts) == colDesc(c.To, opts) && charsetCollationEquiv(c.From.Attrs, c.To.Attrs)
}

func isNoopModifyIndex(c *schema.ModifyIndex, opts dto.NormalizeOptions) bool {
	if c == nil || c.From == nil || c.To == nil {
		return false
	}
	if idxDesc(c.From) != idxDesc(c.To) {
		return false
	}
	noise := schema.ChangeAttr | schema.ChangeComment
	if opts.IgnoreCommentsOnly {
		return c.Change&^noise == 0
	}
	// Same key parts/uniqueness; only comment/attr noise.
	return c.Change&^(schema.ChangeAttr) == 0
}

func attrsEquivalent(a, b schema.Attr) bool {
	if a == nil || b == nil {
		return a == b
	}
	switch x := a.(type) {
	case *schema.Charset:
		y, ok := b.(*schema.Charset)
		return ok && aliasCharset(x.V) == aliasCharset(y.V)
	case *schema.Collation:
		y, ok := b.(*schema.Collation)
		return ok && aliasCollation(x.V) == aliasCollation(y.V)
	case *mysql.Engine:
		y, ok := b.(*mysql.Engine)
		return ok && strings.EqualFold(x.V, y.V)
	case *schema.Comment:
		y, ok := b.(*schema.Comment)
		return ok && x.Text == y.Text
	default:
		return false
	}
}

func charsetCollationEquiv(a, b []schema.Attr) bool {
	return aliasCharset(attrCharset(a)) == aliasCharset(attrCharset(b)) &&
		aliasCollation(attrCollation(a)) == aliasCollation(attrCollation(b))
}

func attrCharset(attrs []schema.Attr) string {
	for _, a := range attrs {
		if c, ok := a.(*schema.Charset); ok {
			return c.V
		}
	}
	return ""
}

func attrCollation(attrs []schema.Attr) string {
	for _, a := range attrs {
		if c, ok := a.(*schema.Collation); ok {
			return c.V
		}
	}
	return ""
}

func attrComment(attrs []schema.Attr) string {
	for _, a := range attrs {
		if c, ok := a.(*schema.Comment); ok {
			return c.Text
		}
	}
	return ""
}

func attrEngine(attrs []schema.Attr) string {
	for _, a := range attrs {
		if e, ok := a.(*mysql.Engine); ok {
			return e.V
		}
	}
	return ""
}

func normalizeEngine(e string) string {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "innodb"
	}
	return e
}

func appendTableAttrDiffs(out []dto.ColDiff, name, src, dst string) []dto.ColDiff {
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if src == "" && dst == "" {
		return out
	}
	// Compare charset/collation after alias normalization.
	cmpSrc, cmpDst := src, dst
	switch name {
	case "CHARSET":
		cmpSrc, cmpDst = aliasCharset(src), aliasCharset(dst)
	case "COLLATE":
		cmpSrc, cmpDst = aliasCollation(src), aliasCollation(dst)
	case "ENGINE":
		cmpSrc, cmpDst = normalizeEngine(src), normalizeEngine(dst)
	}
	kind := "same"
	if cmpSrc != cmpDst {
		kind = "modify"
		if src == "" {
			kind = "add"
		} else if dst == "" {
			kind = "drop"
		}
	}
	return append(out, dto.ColDiff{Name: name, Kind: kind, Source: src, Target: dst})
}

// pruneInvisibleModifies drops ModifyTable changes whose source/target tables
// have no differences visible in the UI structural comparison.
func pruneInvisibleModifies(changes []schema.Change, dstSch, srcSch *schema.Schema, opts dto.NormalizeOptions) []schema.Change {
	srcBy := tableByName(srcSch)
	dstBy := tableByName(dstSch)
	out := make([]schema.Change, 0, len(changes))
	for _, ch := range changes {
		mt, ok := ch.(*schema.ModifyTable)
		if !ok {
			out = append(out, ch)
			continue
		}
		name := mt.T.Name
		if !tableHasVisibleDiff(srcBy[name], dstBy[name], opts) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func tableByName(s *schema.Schema) map[string]*schema.Table {
	m := map[string]*schema.Table{}
	if s == nil {
		return m
	}
	for _, t := range s.Tables {
		m[t.Name] = t
	}
	return m
}

func tableHasVisibleDiff(src, dst *schema.Table, opts dto.NormalizeOptions) bool {
	if src == nil || dst == nil {
		return src != dst
	}
	if aliasCharset(attrCharset(src.Attrs)) != aliasCharset(attrCharset(dst.Attrs)) {
		return true
	}
	if normalizeCollation(attrCollation(src.Attrs), opts) != normalizeCollation(attrCollation(dst.Attrs), opts) {
		return true
	}
	if normalizeEngine(attrEngine(src.Attrs)) != normalizeEngine(attrEngine(dst.Attrs)) {
		return true
	}
	if attrComment(src.Attrs) != attrComment(dst.Attrs) {
		return true
	}

	srcCols := map[string]*schema.Column{}
	for _, c := range src.Columns {
		srcCols[c.Name] = c
	}
	dstCols := map[string]*schema.Column{}
	for _, c := range dst.Columns {
		dstCols[c.Name] = c
	}
	if len(srcCols) != len(dstCols) {
		return true
	}
	for name, sc := range srcCols {
		dc, ok := dstCols[name]
		if !ok || colDesc(sc, opts) != colDesc(dc, opts) {
			return true
		}
	}

	srcIdx := map[string]string{}
	for _, idx := range src.Indexes {
		srcIdx[idx.Name] = idxDesc(idx)
	}
	dstIdx := map[string]string{}
	for _, idx := range dst.Indexes {
		dstIdx[idx.Name] = idxDesc(idx)
	}
	if len(srcIdx) != len(dstIdx) {
		return true
	}
	for name, desc := range srcIdx {
		if dstIdx[name] != desc {
			return true
		}
	}

	srcFK := map[string]string{}
	for _, fk := range src.ForeignKeys {
		srcFK[fk.Symbol] = fkDesc(fk)
	}
	dstFK := map[string]string{}
	for _, fk := range dst.ForeignKeys {
		dstFK[fk.Symbol] = fkDesc(fk)
	}
	if len(srcFK) != len(dstFK) {
		return true
	}
	for name, desc := range srcFK {
		if dstFK[name] != desc {
			return true
		}
	}
	return false
}
