package store_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/testutil"
	"testing"
)

func TestMigrationReadinessRejectsMissingAndChangedSchema(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	if err := s.VerifyMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := s.DB.Exec(ctx, "UPDATE schema_migrations SET checksum='changed' WHERE name='003_execution_review.sql'")
	if err != nil {
		t.Fatal(err)
	}
	if s.VerifyMigrations(ctx) == nil {
		t.Fatal("changed migration served traffic")
	}
	_, err = s.DB.Exec(ctx, "DELETE FROM schema_migrations WHERE name='003_execution_review.sql'")
	if err != nil {
		t.Fatal(err)
	}
	if s.VerifyMigrations(ctx) == nil {
		t.Fatal("old schema served traffic")
	}
}
func TestRuntimeRoleCanAccountButCannotRewriteMoneyOrDDL(t *testing.T) {
	s := testutil.Database(t)
	ctx := context.Background()
	name := "runtime_" + security.Token()[:12]
	role := pgx.Identifier{name}.Sanitize()
	if err := s.ProvisionRuntime(ctx, name, security.Token()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := s.DB.Exec(ctx, "DROP OWNED BY "+role+"; DROP ROLE "+role)
		if err != nil {
			t.Error(err)
		}
	})
	conn, err := s.DB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	defer conn.Exec(ctx, "RESET ROLE")
	if _, err = conn.Exec(ctx, "SET ROLE "+role); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"INSERT INTO users(id,email,name,role,status) VALUES('runtime-user','runtime@test.invalid','test','channel','active')",
		"INSERT INTO wallets(user_id) VALUES('runtime-user')",
		"SELECT * FROM wallets WHERE user_id='runtime-user' FOR UPDATE",
		"INSERT INTO ledger(id,user_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES('credit','runtime-user','credit',1000,0,'receipt','test','admin')",
	} {
		if _, err = conn.Exec(ctx, sql); err != nil {
			t.Fatalf("required runtime operation failed: %v", err)
		}
	}
	var balance int64
	if err = conn.QueryRow(ctx, "SELECT available_minor FROM wallets WHERE user_id='runtime-user'").Scan(&balance); err != nil || balance != 1000 {
		t.Fatal("trigger failed", err, balance)
	}
	for _, sql := range []string{
		"UPDATE wallets SET available_minor=2000 WHERE user_id='runtime-user'",
		"INSERT INTO wallets(user_id,available_minor) VALUES('admin',1000)",
		"UPDATE ledger SET note='rewritten'", "DELETE FROM ledger", "TRUNCATE wallets CASCADE", "CREATE TABLE forbidden(id int)",
		"DELETE FROM schema_migrations", "UPDATE wallets SET user_id='changed' WHERE user_id='runtime-user'",
	} {
		if _, err = conn.Exec(ctx, sql); err == nil {
			t.Fatalf("runtime could mutate protected state: %s", sql)
		}
	}
}
