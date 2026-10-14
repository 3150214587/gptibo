package testutil

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
	"net/url"
	"os"
	"strings"
	"testing"
)

func Database(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("PLATFORM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PLATFORM_TEST_DATABASE_URL required for isolated PostgreSQL integration tests")
	}
	check, err := url.Parse(dsn)
	if err != nil || (check.Hostname() != "localhost" && check.Hostname() != "127.0.0.1" && check.Hostname() != "::1") || !strings.HasSuffix(check.Path, "_test") {
		t.Fatal("PLATFORM_TEST_DATABASE_URL must use an isolated local *_test database")
	}
	ctx := context.Background()
	root, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + security.Token()[:16]
	if _, e = root.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, e := store.Open(ctx, u.String(), "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close(); _, _ = root.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); root.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, `UPDATE products SET config=jsonb_build_object('months',CASE code WHEN 'x-3m' THEN 3 ELSE 6 END,'amount_minor',CASE code WHEN 'x-3m' THEN 30000 ELSE 60000 END,'currency','bdt','stripe_product',CASE code WHEN 'x-3m' THEN 'prod_fixture3' ELSE 'prod_fixture6' END,'stripe_merchant','acct_fixture') WHERE business='x'`); e != nil {
		t.Fatal(e)
	}
	return s
}
