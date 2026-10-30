//go:build sqld

package accounts

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/sqld"
	"github.com/djalmajr/nau/internal/hub/store"
)

// TestSQLDAccountsScenarios runs the whole scenario suite over the SQL
// implementation against a real sqld, named by NAU_TEST_SQLD_URL. The
// migrations are applied first; each scenario empties the account
// tables so it starts from the empty state. The test is re-runnable on
// the same database. The tagged tests of the hub packages share the
// instance's default database and one of them reinitializes the
// schema, so run the tagged suite with -p 1 (the packages sequential).
func TestSQLDAccountsScenarios(t *testing.T) {
	url := os.Getenv("NAU_TEST_SQLD_URL")
	if url == "" {
		t.Skip("set NAU_TEST_SQLD_URL to run the accounts scenarios against a real sqld")
	}
	client, err := sqld.New(url, "")
	if err != nil {
		t.Fatalf("sqld.New: %v", err)
	}
	ctx := context.Background()
	if err := (&migrate.Runner{Store: client, Now: time.Now}).Apply(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Migration 0002 created the four account tables.
	res, err := client.Execute(ctx, store.Stmt{SQL: `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'sessions', 'invites', 'login_failures') ORDER BY name`})
	if err != nil {
		t.Fatalf("list the account tables: %v", err)
	}
	if len(res.Rows) != 4 {
		t.Fatalf("the account tables = %d, want 4", len(res.Rows))
	}
	runAccountsScenarios(t, func() AccountStore { return freshSQLStore(t, client) })
}

// freshSQLStore empties the account tables and the bootstrap mark, so
// every scenario starts from the empty state the migrations create.
// The tables stay: the migrations are already applied and are
// one-way, so the store must clean the rows, not the schema.
func freshSQLStore(t *testing.T, client *sqld.Client) AccountStore {
	t.Helper()
	ctx := context.Background()
	_, err := client.Batch(ctx, []store.Stmt{
		{SQL: "DELETE FROM login_failures"},
		{SQL: "DELETE FROM sessions"},
		{SQL: "DELETE FROM invites"},
		{SQL: "DELETE FROM users"},
		{SQL: "DELETE FROM hub_meta"},
	})
	if err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	return NewSQL(client)
}
