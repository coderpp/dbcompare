package engine

import (
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/pp/db-compare/internal/dto"
)

// DowngradeLevel classifies how a feature is handled when targeting an older server.
type DowngradeLevel int

const (
	DowngradeNone DowngradeLevel = iota
	DowngradeLossless            // structure kept, semantics change (INFO/WARN)
	DowngradeLossy               // rewrite / strip (WARN)
	DowngradeBlock               // must not silently apply (ERROR)
)

// FeatureIssue describes a capability mismatch.
type FeatureIssue struct {
	Level   DowngradeLevel
	Code    string
	Table   string
	Object  string
	Message string
}

// Capability describes what a target MySQL version supports.
type Capability struct {
	Version            SemVer
	InvisibleIndex     bool
	DescIndex          bool
	FunctionalIndex    bool
	CheckConstraint    bool
	ExprDefault        bool
	InvisibleColumn    bool
	MultiValueIndex    bool
	InstantAddColumn   bool
	DisplayWidthOutput bool
	UTF8MB3Display     bool
}

// CapabilityFor builds the matrix for a server version.
func CapabilityFor(v SemVer) Capability {
	c := Capability{Version: v}
	if v.Is57() || (v.Major == 5 && v.Minor < 7) {
		c.DisplayWidthOutput = true
		return c
	}
	// MySQL 8.0+
	c.InvisibleIndex = v.AtLeast(8, 0, 0)
	c.DescIndex = v.AtLeast(8, 0, 0)
	c.FunctionalIndex = v.AtLeast(8, 0, 13)
	c.ExprDefault = v.AtLeast(8, 0, 13)
	c.CheckConstraint = v.AtLeast(8, 0, 16)
	c.MultiValueIndex = v.AtLeast(8, 0, 17)
	c.DisplayWidthOutput = !v.AtLeast(8, 0, 19)
	c.InvisibleColumn = v.AtLeast(8, 0, 23)
	c.InstantAddColumn = v.AtLeast(8, 0, 12)
	c.UTF8MB3Display = v.AtLeast(8, 0, 30)
	return c
}

// AnalyzeDowngrade scans a desired (source) schema for features unsupported on target.
func AnalyzeDowngrade(desired *schema.Schema, target Cap) []FeatureIssue {
	cap := CapabilityFor(target.semVer())
	var out []FeatureIssue
	if desired == nil {
		return out
	}
	for _, t := range desired.Tables {
		out = append(out, analyzeTableFeatures(t, cap)...)
	}
	return out
}

// Cap is a small interface so callers can pass *Conn or SemVer wrapper.
type Cap interface {
	semVer() SemVer
}

func (c *Conn) semVer() SemVer { return c.SemVer }

type verCap SemVer

func (v verCap) semVer() SemVer { return SemVer(v) }

// CapabilityOf returns Capability for any Cap.
func CapabilityOf(c Cap) Capability { return CapabilityFor(c.semVer()) }

func AnalyzeDowngradeOn(desired *schema.Schema, target SemVer) []FeatureIssue {
	return AnalyzeDowngrade(desired, verCap(target))
}

func analyzeTableFeatures(t *schema.Table, cap Capability) []FeatureIssue {
	var out []FeatureIssue
	for _, c := range t.Columns {
		if hasInvisible(c) && !cap.InvisibleColumn {
			out = append(out, FeatureIssue{
				Level: DowngradeLossless, Code: "invisible_column", Table: t.Name, Object: c.Name,
				Message: fmt.Sprintf("column `%s`.`%s` is INVISIBLE; will become visible on target (SELECT * result set changes)", t.Name, c.Name),
			})
		}
		if isExprDefault(c) && !cap.ExprDefault {
			out = append(out, FeatureIssue{
				Level: DowngradeBlock, Code: "expr_default", Table: t.Name, Object: c.Name,
				Message: fmt.Sprintf("column `%s`.`%s` has expression DEFAULT; unsupported on target %s", t.Name, c.Name, cap.Version.Raw),
			})
		}
		if hasSRID(c) && !cap.Version.AtLeast(8, 0, 3) {
			out = append(out, FeatureIssue{
				Level: DowngradeLossless, Code: "srid", Table: t.Name, Object: c.Name,
				Message: fmt.Sprintf("column `%s`.`%s` has SRID; will be stripped on target", t.Name, c.Name),
			})
		}
	}
	for _, idx := range t.Indexes {
		if isInvisibleIndex(idx) && !cap.InvisibleIndex {
			out = append(out, FeatureIssue{
				Level: DowngradeLossless, Code: "invisible_index", Table: t.Name, Object: idx.Name,
				Message: fmt.Sprintf("index `%s`.`%s` is INVISIBLE; optimizer will use it on target", t.Name, idx.Name),
			})
		}
		if hasDescPart(idx) && !cap.DescIndex {
			out = append(out, FeatureIssue{
				Level: DowngradeLossy, Code: "desc_index", Table: t.Name, Object: idx.Name,
				Message: fmt.Sprintf("index `%s`.`%s` has DESC key parts; DESC will be ignored on target", t.Name, idx.Name),
			})
		}
		if hasFunctionalPart(idx) && !cap.FunctionalIndex {
			out = append(out, FeatureIssue{
				Level: DowngradeLossy, Code: "functional_index", Table: t.Name, Object: idx.Name,
				Message: fmt.Sprintf("index `%s`.`%s` uses expression key parts; requires rewrite or skip on target", t.Name, idx.Name),
			})
		}
		if isMultiValueIndex(idx) && !cap.MultiValueIndex {
			out = append(out, FeatureIssue{
				Level: DowngradeBlock, Code: "multivalue_index", Table: t.Name, Object: idx.Name,
				Message: fmt.Sprintf("index `%s`.`%s` is a multi-valued (JSON ARRAY) index; cannot downgrade", t.Name, idx.Name),
			})
		}
	}
	for _, ck := range checksOf(t) {
		if !cap.CheckConstraint {
			out = append(out, FeatureIssue{
				Level: DowngradeBlock, Code: "check_constraint", Table: t.Name, Object: ck,
				Message: fmt.Sprintf("CHECK constraint `%s` on `%s` would be silently ignored on MySQL < 8.0.16 — blocked", ck, t.Name),
			})
		}
	}
	return out
}

func isExprDefault(c *schema.Column) bool {
	r, ok := c.Default.(*schema.RawExpr)
	if !ok {
		return false
	}
	// Atlas inspects DEFAULT CURRENT_TIMESTAMP as RawExpr. That form (and
	// NOW()/LOCALTIME synonyms, with optional fractional precision) is valid
	// on MySQL 5.6.5+ and must not be treated as an 8.0.13 expression default.
	return !isNativeTimestampDefault(r.X)
}

// isNativeTimestampDefault reports timestamp defaults that 5.7 can emit
// without the 8.0.13 DEFAULT (expr) syntax.
func isNativeTimestampDefault(expr string) bool {
	x := unwrapOuterParens(strings.TrimSpace(expr))
	x = strings.ToUpper(strings.TrimSpace(x))
	x = strings.ReplaceAll(x, " ", "")
	switch x {
	case "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP()",
		"NOW()", "LOCALTIME", "LOCALTIME()",
		"LOCALTIMESTAMP", "LOCALTIMESTAMP()":
		return true
	}
	for _, prefix := range []string{"CURRENT_TIMESTAMP(", "NOW(", "LOCALTIME(", "LOCALTIMESTAMP("} {
		if strings.HasPrefix(x, prefix) && strings.HasSuffix(x, ")") {
			inner := x[len(prefix) : len(x)-1]
			if inner == "" {
				return true
			}
			for _, r := range inner {
				if r < '0' || r > '9' {
					return false
				}
			}
			return true
		}
	}
	return false
}

func unwrapOuterParens(s string) string {
	for {
		s = strings.TrimSpace(s)
		if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
			return s
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if strings.Count(s, "(") != strings.Count(s, ")") {
			return s
		}
		// Only unwrap a single wrapping pair, not function-call parens like NOW().
		if inner == "" {
			return s
		}
		s = inner
	}
}

func hasSRID(c *schema.Column) bool {
	for _, a := range c.Attrs {
		if strings.Contains(fmt.Sprintf("%T", a), "SRID") || strings.Contains(fmt.Sprintf("%T", a), "Srid") {
			return true
		}
	}
	return false
}

func isInvisibleIndex(idx *schema.Index) bool {
	for _, a := range idx.Attrs {
		if strings.Contains(fmt.Sprintf("%T", a), "Invisible") {
			return true
		}
	}
	return false
}

func hasDescPart(idx *schema.Index) bool {
	for _, p := range idx.Parts {
		if p.Desc {
			return true
		}
	}
	return false
}

func hasFunctionalPart(idx *schema.Index) bool {
	for _, p := range idx.Parts {
		if p.X != nil {
			return true
		}
	}
	return false
}

func isMultiValueIndex(idx *schema.Index) bool {
	for _, p := range idx.Parts {
		if p.X == nil {
			continue
		}
		ex := strings.ToUpper(exprString(p.X))
		if strings.Contains(ex, "ARRAY") {
			return true
		}
	}
	return false
}

func exprString(x schema.Expr) string {
	switch e := x.(type) {
	case *schema.Literal:
		return e.V
	case *schema.RawExpr:
		return e.X
	default:
		return fmt.Sprint(x)
	}
}

func checksOf(t *schema.Table) []string {
	var names []string
	for _, a := range t.Attrs {
		if ck, ok := a.(*schema.Check); ok {
			names = append(names, ck.Name)
		}
	}
	return names
}

// ToDTORisk maps downgrade level to UI risk.
func (l DowngradeLevel) ToDTORisk() dto.RiskLevel {
	switch l {
	case DowngradeLossless:
		return dto.RiskWarn
	case DowngradeLossy:
		return dto.RiskWarn
	case DowngradeBlock:
		return dto.RiskError
	default:
		return dto.RiskInfo
	}
}
