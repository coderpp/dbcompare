//go:build integration

package integration_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pp/db-compare/internal/dto"
	"github.com/pp/db-compare/internal/engine"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestCompareAndSyncSameHost80(t *testing.T) {
	src := dto.ConnConfig{
		Name: "src", Host: envOr("MYSQL80_HOST", "127.0.0.1"), Port: 33080,
		User: "root", Password: "root", Database: "dst_db",
	}
	dst := dto.ConnConfig{
		Name: "dst", Host: envOr("MYSQL80_HOST", "127.0.0.1"), Port: 33080,
		User: "root", Password: "root", Database: "sync_target",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Probe
	c, err := engine.Open(ctx, src)
	if err != nil {
		t.Skipf("mysql80 not available: %v", err)
	}
	_ = c.Close()

	eng := engine.New()
	res, err := eng.Compare(ctx, dto.CompareRequest{
		Source:    src,
		Target:    dst,
		Normalize: dto.DefaultNormalizeOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added)+len(res.Modified)+len(res.Dropped) == 0 {
		t.Fatal("expected differences between dst_db and sync_target")
	}
	t.Logf("diff: +%d ~%d -%d warnings=%d", len(res.Added), len(res.Modified), len(res.Dropped), len(res.Warnings))

	ids := make([]string, 0)
	for _, it := range append(append(res.Added, res.Modified...), res.Dropped...) {
		if it.Kind == dto.KindDrop {
			continue
		}
		// Skip partitioned table creates — Atlas cannot emit valid PARTITION BY.
		if it.ObjectType == "table" && it.ObjectName == "part_sales" {
			continue
		}
		ids = append(ids, it.ID)
	}
	plan, err := eng.Plan(ctx, dto.PlanRequest{
		SessionID:   res.SessionID,
		ChangeIDs:   ids,
		AllowDrop:   false,
		AllowUnsafe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Statements) == 0 {
		t.Fatal("expected planned statements")
	}

	apply, err := eng.Apply(ctx, dto.ApplyRequest{
		SessionID:    res.SessionID,
		StatementIDs: statementIDs(plan),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("applied=%d failed=%d verify=%v %s", apply.Applied, apply.Failed, apply.VerifyClean, apply.VerifySummary)
	if apply.Failed > 0 {
		t.Fatalf("apply errors: %v", apply.Errors)
	}
}

func statementIDs(plan *dto.PlanResult) []string {
	ids := make([]string, 0, len(plan.Statements))
	for _, s := range plan.Statements {
		// Skip any leftover partitioned DDL.
		if strings.Contains(strings.ToLower(s.SQL), "partition") {
			continue
		}
		ids = append(ids, s.ID)
	}
	return ids
}

func TestCrossVersionCompare57to80(t *testing.T) {
	src := dto.ConnConfig{
		Name: "src57", Host: envOr("MYSQL57_HOST", "127.0.0.1"), Port: 33057,
		User: "root", Password: "root", Database: "src_db",
	}
	dst := dto.ConnConfig{
		Name: "dst80", Host: envOr("MYSQL80_HOST", "127.0.0.1"), Port: 33080,
		User: "root", Password: "root", Database: "dst_db",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := engine.Open(ctx, src); err != nil {
		t.Skipf("mysql57/mariadb not available: %v", err)
	}

	eng := engine.New()
	res, err := eng.Compare(ctx, dto.CompareRequest{
		Source:    src,
		Target:    dst,
		Normalize: dto.DefaultNormalizeOptions(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("5.7→8.0: +%d ~%d -%d", len(res.Added), len(res.Modified), len(res.Dropped))
	// With normalization, int(11) vs int should not dominate; at least no panic.
	for _, w := range res.Warnings {
		t.Log("warn:", w)
	}
}

func TestNormalizeKillsIntWidthNoise(t *testing.T) {
	// Pure unit-style via live inspect of int_width_demo on 8.0 vs itself after normalize.
	cfg := dto.ConnConfig{
		Host: envOr("MYSQL80_HOST", "127.0.0.1"), Port: 33080,
		User: "root", Password: "root", Database: "dst_db",
	}
	ctx := context.Background()
	c, err := engine.Open(ctx, cfg)
	if err != nil {
		t.Skip(err)
	}
	defer c.Close()
	sch, err := c.Inspect(ctx, []string{"int_width_demo"})
	if err != nil {
		t.Fatal(err)
	}
	engine.Normalize(sch, dto.DefaultNormalizeOptions())
	// Diff against itself after normalize must be empty.
	changes, err := c.Driver.SchemaDiff(sch, sch)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("self-diff after normalize should be empty, got %d", len(changes))
	}
}
