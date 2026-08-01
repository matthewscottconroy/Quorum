//go:build integration

// Migration reversibility: every .up.sql must have a .down.sql that actually
// undoes it. This runs the full ladder up, all the way back down to zero, and up
// again — proving both directions on a real PostgreSQL instance, which is what
// makes "roll back a bad migration" a supported operation rather than a hope.
//
// It uses its OWN throwaway database (created from QUORUM_TEST_DATABASE_URL's
// server) because it drops the entire schema; it must never run against the
// database the other integration tests share.
package repo_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"quorum/internal/db"
)

// scratchDB creates a uniquely-named database on the same server and returns a
// pool for it plus a cleanup that drops it.
func scratchDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("QUORUM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("QUORUM_TEST_DATABASE_URL not set; skipping integration tests")
	}
	ctx := context.Background()

	admin, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()

	// A per-run name so parallel CI jobs don't collide.
	name := fmt.Sprintf("quorum_migtest_%d", os.Getpid())
	// CREATE DATABASE cannot run inside a transaction or take parameters.
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, err := db.Connect(ctx, dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(ctx, "DROP DATABASE IF EXISTS "+name)
	})
	return pool
}

func appliedVersions(t *testing.T, pool *pgxpool.Pool) []int {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func publicTableCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name <> 'schema_migrations'`).Scan(&n); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	return n
}

// TestIntegration_MigrationsAreReversible runs up → down to 0 → up again.
func TestIntegration_MigrationsAreReversible(t *testing.T) {
	pool := scratchDB(t)
	ctx := context.Background()

	// --- up ---
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("initial migrate up: %v", err)
	}
	upVersions := appliedVersions(t, pool)
	if len(upVersions) == 0 {
		t.Fatal("no migrations applied")
	}
	tablesAfterUp := publicTableCount(t, pool)
	if tablesAfterUp == 0 {
		t.Fatal("schema is empty after migrating up")
	}
	t.Logf("up: %d migrations, %d tables", len(upVersions), tablesAfterUp)

	// --- down to zero ---
	if err := db.MigrateDown(ctx, pool, 0); err != nil {
		t.Fatalf("migrate down to 0: %v", err)
	}
	if v := appliedVersions(t, pool); len(v) != 0 {
		t.Errorf("schema_migrations should be empty after full rollback, got %v", v)
	}
	// Every table the migrations created must be gone. A leftover table means
	// some .down.sql does not fully undo its .up.sql.
	if n := publicTableCount(t, pool); n != 0 {
		var names []string
		rows, _ := pool.Query(ctx, `SELECT table_name FROM information_schema.tables
			WHERE table_schema='public' AND table_name <> 'schema_migrations' ORDER BY table_name`)
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			names = append(names, s)
		}
		rows.Close()
		t.Errorf("%d table(s) survived full rollback (incomplete .down.sql): %s", n, strings.Join(names, ", "))
	}

	// --- up again ---
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("re-migrate up after rollback: %v", err)
	}
	if got := appliedVersions(t, pool); len(got) != len(upVersions) {
		t.Errorf("re-apply produced %d migrations, want %d", len(got), len(upVersions))
	}
	if n := publicTableCount(t, pool); n != tablesAfterUp {
		t.Errorf("re-apply produced %d tables, want %d", n, tablesAfterUp)
	}
}

// TestIntegration_MigrateDownOneStep proves a single-version rollback works —
// the realistic "back out the migration we just deployed" case.
func TestIntegration_MigrateDownOneStep(t *testing.T) {
	pool := scratchDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	versions := appliedVersions(t, pool)
	latest := versions[len(versions)-1]
	prev := versions[len(versions)-2]

	if err := db.MigrateDown(ctx, pool, prev); err != nil {
		t.Fatalf("roll back one: %v", err)
	}
	after := appliedVersions(t, pool)
	if len(after) != len(versions)-1 || after[len(after)-1] != prev {
		t.Fatalf("after rolling back %d, want top version %d, got %v", latest, prev, after)
	}
	// And forward again, so the deploy can be retried.
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("re-apply latest: %v", err)
	}
	if got := appliedVersions(t, pool); got[len(got)-1] != latest {
		t.Errorf("latest migration did not re-apply: %v", got)
	}
}
