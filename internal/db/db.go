// Package db manages the PostgreSQL connection pool and the embedded,
// advisory-locked migration runner.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations
var migrationsFS embed.FS

// Connect opens a bounded pgx connection pool and verifies connectivity with a ping.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Bound the pool so a traffic spike or connection leak degrades gracefully
	// instead of exhausting PostgreSQL's max_connections.
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return pool, nil
}

// Migrate applies embedded up-migrations in version order under a Postgres advisory
// lock, recording each in schema_migrations within its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	// Advisory lock to prevent concurrent migration runs.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(8675309)"); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock(8675309)") //nolint:errcheck

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	// A mid-iteration error would leave `applied` incomplete and re-run
	// already-applied migrations, so it must be fatal.
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	files, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	type migration struct {
		version int
		name    string
	}
	var ups []migration
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".up.sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(f.Name(), "%04d", &v); err != nil || v <= 0 {
			return fmt.Errorf("migration %s: filename must start with a positive 4-digit version", f.Name())
		}
		ups = append(ups, migration{version: v, name: f.Name()})
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].version < ups[j].version })

	for _, m := range ups {
		if applied[m.version] {
			continue
		}
		sql, err := fs.ReadFile(migrationsFS, "migrations/"+m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}
		// Apply the migration and record its version atomically so a crash
		// between the two can never re-run non-idempotent DDL on next boot.
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
		fmt.Printf("applied migration %s\n", m.name)
	}
	return nil
}

// MigrateDown rolls the schema back to targetVersion by applying each
// .down.sql from the current version downward, newest first, stopping once
// schema_migrations holds only versions <= targetVersion. Pass 0 to unwind
// everything. Each step runs in its own transaction with the migration row
// deleted atomically, under the same advisory lock as Migrate.
//
// This is an operator tool (see `quorum -migrate-down`) and the basis of the
// CI check that every migration is reversible — it is never called at startup.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool, targetVersion int) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(8675309)"); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock(8675309)") //nolint:errcheck

	// Applied versions above the target, newest first — the order they unwind in.
	rows, err := conn.Query(ctx,
		"SELECT version FROM schema_migrations WHERE version > $1 ORDER BY version DESC", targetVersion)
	if err != nil {
		return err
	}
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}

	// Index the embedded .down.sql files by version.
	files, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	downs := map[int]string{}
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".down.sql") {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(f.Name(), "%04d", &v); err != nil || v <= 0 {
			return fmt.Errorf("migration %s: filename must start with a positive 4-digit version", f.Name())
		}
		downs[v] = f.Name()
	}

	for _, v := range versions {
		name, ok := downs[v]
		if !ok {
			return fmt.Errorf("no down-migration for version %04d: cannot roll back past it", v)
		}
		sql, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin rollback %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("apply rollback %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", v); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("unrecord migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit rollback %s: %w", name, err)
		}
		fmt.Printf("rolled back migration %s\n", name)
	}
	return nil
}
