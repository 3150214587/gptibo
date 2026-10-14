package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gptibo/platform/internal/security"
	"gptibo/platform/migrations"
	"sort"
	"time"
)

type Store struct {
	DB    *pgxpool.Pool
	Vault *security.Vault
}

func Open(ctx context.Context, dsn, key string) (*Store, error) {
	v, e := security.NewVault(key)
	if e != nil {
		return nil, e
	}
	config, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	db, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		return nil, e
	}
	if e = db.Ping(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db, v}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	c, e := s.DB.Acquire(ctx)
	if e != nil {
		return e
	}
	defer c.Release()
	if _, e = c.Exec(ctx, "SELECT pg_advisory_lock(903420)"); e != nil {
		return e
	}
	defer c.Exec(context.Background(), "SELECT pg_advisory_unlock(903420)")
	if _, e = c.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY,checksum text NOT NULL,created_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	entries, e := migrations.Files.ReadDir(".")
	if e != nil {
		return e
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, f := range entries {
		b, e := migrations.Files.ReadFile(f.Name())
		if e != nil {
			return e
		}
		var hash string
		e = c.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE name=$1", f.Name()).Scan(&hash)
		if e == nil {
			if hash != security.Hash(string(b)) {
				return fmt.Errorf("migration changed: %s", f.Name())
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		tx, e := c.Begin(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, string(b)); e == nil {
			_, e = tx.Exec(ctx, "INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)", f.Name(), security.Hash(string(b)))
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}

// Verify every compiled migration before serving traffic; an old schema is not ready.
func (s *Store) VerifyMigrations(ctx context.Context) error {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		var checksum string
		if err = s.DB.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE name=$1", entry.Name()).Scan(&checksum); err != nil || checksum != security.Hash(string(data)) {
			return fmt.Errorf("migration missing or changed: %s; run migrate", entry.Name())
		}
	}
	return nil
}
func (s *Store) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func Audit(ctx context.Context, tx pgx.Tx, actor, action, target, note string) error {
	_, e := tx.Exec(ctx, "INSERT INTO audit(id,actor_id,action,target,note) VALUES($1,$2,$3,$4,$5)", security.ID("aud"), actor, action, target, note)
	return e
}
func (s *Store) Rows(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	rows, e := s.DB.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	fields := rows.FieldDescriptions()
	for rows.Next() {
		values, e := rows.Values()
		if e != nil {
			return nil, e
		}
		row := map[string]any{}
		for i, v := range values {
			row[fields[i].Name] = v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
