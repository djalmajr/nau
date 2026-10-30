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

// The account tables of migration 0002 and the guard.
const accountTablesSQL = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'sessions', 'invites', 'login_failures', 'accounts_guard')`

// establishStablePolls is how many consecutive polls must see the
// account tables before the scenarios start: the tagged tests of the
// hub packages share the instance's default database, and another
// package drops and re-creates the whole schema while the suites
// start. The reinitialization is one burst at that package's start, so
// the schema must be stable for this window (about two seconds) before
// the scenarios run; a deterministic migration problem fails on the
// bounded retry before the wait.
const establishStablePolls = 10

// establish applies the migrations with a bounded retry (another
// package may apply or drop them in between the statements), checks
// that migration 0002 created the account tables and the guard, and
// then waits for the schema to be stable before the scenarios start.
func establish(t *testing.T, client *sqld.Client, ctx context.Context) {
	t.Helper()
	var err error
	for attempt := 0; ; attempt++ {
		err = (&migrate.Runner{Store: client, Now: time.Now}).Apply(ctx)
		if err == nil || attempt == 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !accountTablesPresent(t, client, ctx) {
		t.Fatalf("the account tables are not in the schema")
	}
	for stable := 0; stable < establishStablePolls; {
		if accountTablesPresent(t, client, ctx) {
			stable++
		} else {
			stable = 0
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// accountTablesPresent reports whether the five account tables are in
// the schema; a failed read (for example, a schema being dropped in
// between) is one that is not present.
func accountTablesPresent(t *testing.T, client *sqld.Client, ctx context.Context) bool {
	t.Helper()
	res, err := client.Execute(ctx, store.Stmt{SQL: accountTablesSQL})
	if err != nil {
		return false
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return false
	}
	n, ok := res.Rows[0][0].(int64)
	return ok && n == 5
}

// TestSQLDAccountsScenarios runs the whole scenario suite over the SQL
// implementation against a real sqld, named by NAU_TEST_SQLD_URL. Each
// scenario empties the account tables so it starts from the empty
// state. The test is re-runnable on the same database. The tagged
// tests of the hub packages share the instance's default database and
// one of them reinitializes the schema while the suites start, so the
// scenarios start only after the schema is stable.
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
	establish(t, client, ctx)
	runAccountsScenarios(t, func() AccountStore { return freshSQLStore(t, client) })
}

// TestSQLDConcurrencyScenarios runs the concurrency scenarios over the
// SQL implementation against the same real sqld: the interleavings of
// the security review (the stale login, the stale password change, the
// concurrent reservations, the revoked invite and the atomic accept)
// over the actual batches, plus the auth version check of a session.
// It uses the same stable establishment as the scenarios.
func TestSQLDConcurrencyScenarios(t *testing.T) {
	url := os.Getenv("NAU_TEST_SQLD_URL")
	if url == "" {
		t.Skip("set NAU_TEST_SQLD_URL to run the accounts concurrency scenarios against a real sqld")
	}
	client, err := sqld.New(url, "")
	if err != nil {
		t.Fatalf("sqld.New: %v", err)
	}
	ctx := context.Background()
	establish(t, client, ctx)
	runConcurrencyScenarios(t, func() AccountStore { return freshSQLStore(t, client) })
}

// freshSQLStore empties the account tables and the bootstrap mark, so
// every scenario starts from the empty state the migrations create.
// It re-applies the migrations with a bounded retry when another
// package dropped the schema in between.
func freshSQLStore(t *testing.T, client *sqld.Client) AccountStore {
	t.Helper()
	ctx := context.Background()
	var err error
	for attempt := 0; ; attempt++ {
		err = (&migrate.Runner{Store: client, Now: time.Now}).Apply(ctx)
		if err == nil || attempt == 4 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	_, err = client.Batch(ctx, []store.Stmt{
		{SQL: "DELETE FROM accounts_guard"},
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
