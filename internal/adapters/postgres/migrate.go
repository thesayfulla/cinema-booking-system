package postgres

import (
	"context"
	"embed"
	_ "embed"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

//go:embed seed.sql
var seedSQL string

// advisoryLockID namespaces the migration lock. Any constant works as long as
// every instance of this service uses the same one.
const advisoryLockID int64 = 8_240_119_001

type migration struct {
	version int
	name    string
	body    string
}

// Migrate applies every pending migration inside a single advisory lock, so
// that starting several replicas at once cannot race. Each migration runs in
// its own transaction: a failure leaves the database at the last good version.
func (d *DB) Migrate(ctx context.Context) (applied []string, err error) {
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// Session-level lock; every replica but one blocks here until the winner
	// finishes migrating.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockID); unlockErr != nil && err == nil {
			err = fmt.Errorf("release advisory lock: %w", unlockErr)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    int PRIMARY KEY,
			name       text        NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	done := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		done[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}

	for _, m := range migrations {
		if done[m.version] {
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.body); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return applied, fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.version, m.name,
		); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return applied, fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("commit migration %d: %w", m.version, err)
		}

		applied = append(applied, fmt.Sprintf("%04d_%s", m.version, m.name))
	}

	return applied, nil
}

// Seed loads demo catalog data. It is idempotent and is only called when
// demo seeding is enabled in configuration.
func (d *DB) Seed(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, seedSQL); err != nil {
		return fmt.Errorf("seed demo data: %w", err)
	}
	return nil
}

// loadMigrations reads embedded migrations, which must be named
// "<version>_<name>.sql", and returns them in version order.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	seen := map[int]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}

		base := strings.TrimSuffix(e.Name(), ".sql")
		numPart, namePart, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: expected <version>_<name>.sql", e.Name())
		}
		version, err := strconv.Atoi(numPart)
		if err != nil {
			return nil, fmt.Errorf("migration %q: invalid version: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("duplicate migration version %d: %s and %s", version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}

		migrations = append(migrations, migration{version: version, name: namePart, body: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}
