// Package migrate applies the embedded, versioned SQL migrations at boot.
//
// Files are named NNNN_name.up.sql; each is applied in its own transaction and
// recorded in schema_migrations. A Postgres advisory lock serialises
// concurrent boots.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lockKey is an arbitrary constant identifying the migration advisory lock.
const lockKey = 7_046_871_993

var upFile = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.up\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// Up applies every migration in fsys that is not yet recorded. It returns the
// versions it applied.
func Up(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) ([]int, error) {
	migs, err := load(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(lockKey)); err != nil {
		return nil, fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(lockKey)) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("migrate: create schema_migrations: %w", err)
	}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	done, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	applied := make(map[int]bool, len(done))
	for _, v := range done {
		applied[int(v)] = true
	}

	var ran []int
	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version)
			return err
		})
		if err != nil {
			return ran, fmt.Errorf("migrate: apply %s: %w", m.name, err)
		}
		if log != nil {
			log.Info("migration applied", "version", m.version, "name", m.name)
		}
		ran = append(ran, m.version)
	}
	return ran, nil
}

func load(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: list migrations: %w", err)
	}
	var migs []migration
	seen := map[int]string{}
	for _, e := range entries {
		m := upFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: duplicate version %d (%s, %s)", v, prev, e.Name())
		}
		seen[v] = e.Name()
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		migs = append(migs, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}
