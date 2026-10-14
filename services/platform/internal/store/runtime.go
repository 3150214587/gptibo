package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"regexp"
)

// Only the migration owner invokes this command. Passwords are bind parameters, never logs.
func (s *Store) ProvisionRuntime(ctx context.Context, name, password string) error {
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{2,48}$`).MatchString(name) || len(password) < 32 || len(password) > 128 {
		return errors.New("runtime role and 32–128 byte password required")
	}
	if err := s.VerifyMigrations(ctx); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx pgx.Tx) error {
		var owner, schema string
		if err := tx.QueryRow(ctx, "SELECT current_user,current_schema()").Scan(&owner, &schema); err != nil {
			return err
		}
		if name == owner {
			return errors.New("runtime role must differ from migration owner")
		}
		var inherited bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname=$1)", name).Scan(&inherited); err != nil {
			return err
		}
		if inherited {
			return errors.New("runtime role must have no role memberships")
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('platform.runtime_role',$1,true),set_config('platform.runtime_password',$2,true)", name, password); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DO $$ DECLARE r text:=current_setting('platform.runtime_role'); BEGIN
   IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=r) THEN EXECUTE format('CREATE ROLE %I',r); END IF;
   EXECUTE format('ALTER ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT PASSWORD %L',r,current_setting('platform.runtime_password'));
  END $$`); err != nil {
			return err
		}
		role := pgx.Identifier{name}.Sanitize()
		sc := pgx.Identifier{schema}.Sanitize()
		for _, sql := range []string{
			"REVOKE CREATE ON SCHEMA " + sc + " FROM PUBLIC",
			"GRANT USAGE ON SCHEMA " + sc + " TO " + role,
			"GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA " + sc + " TO " + role,
			"REVOKE INSERT,UPDATE,DELETE ON " + sc + ".schema_migrations FROM " + role,
			"REVOKE UPDATE,DELETE ON " + sc + ".ledger," + sc + ".audit," + sc + ".card_retirements," + sc + ".card_declines FROM " + role,
			"REVOKE INSERT,UPDATE,DELETE ON " + sc + ".wallets FROM " + role,
			"GRANT INSERT(user_id),UPDATE(user_id) ON " + sc + ".wallets TO " + role,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
}
