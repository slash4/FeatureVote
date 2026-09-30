// Package testdb gives tests a migrated, empty real Postgres database.
//
// The URL comes from FV_TEST_DATABASE_URL; tests are skipped when it is unset.
// Packages using it must run sequentially (go test -p 1) because they share
// one database.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slash4/featurevote/internal/migrate"
	"github.com/slash4/featurevote/migrations"
)

// Open connects, applies migrations and truncates all data. The pool is
// closed when the test ends.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("FV_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FV_TEST_DATABASE_URL not set; skipping test that needs a real Postgres (see scripts/test.sh)")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("testdb: connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := migrate.Up(ctx, pool, migrations.FS, nil); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE ideas, votes RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("testdb: truncate: %v", err)
	}
	return pool
}
