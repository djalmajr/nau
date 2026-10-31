//go:build sqld

package accounts

import (
	"context"
	"testing"

	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/store"
)

// TestSQLDAccountsScenarios runs the whole scenario suite over the SQL
// implementation against a real sqld, named by NAU_TEST_SQLD_URL. The
// helper takes the lock of the shared database, reinitializes the
// schema and holds the lock for the whole test, so the scenarios run
// on a fresh schema and no other package touches it in the middle.
// Each scenario empties the account tables so it starts from the
// empty state. The test is re-runnable on the same database.
func TestSQLDAccountsScenarios(t *testing.T) {
	st := hubtest.SQLD(t)
	runAccountsScenarios(t, func() AccountStore { return freshSQLStore(t, st) })
}

// TestSQLDConcurrencyScenarios runs the concurrency scenarios over the
// SQL implementation against the same real sqld: the interleavings of
// the security review (the stale login, the stale password change, the
// concurrent reservations, the revoked invite and the atomic accept)
// over the actual batches, plus the auth version check of a session.
// It uses the same lock and reset as the scenarios.
func TestSQLDConcurrencyScenarios(t *testing.T) {
	st := hubtest.SQLD(t)
	runConcurrencyScenarios(t, func() AccountStore { return freshSQLStore(t, st) })
}

// freshSQLStore empties the account tables and the bootstrap mark, so
// every scenario starts from the empty state the migrations create.
// The rows of the other layers go away too, in the order that keeps
// the foreign keys free.
func freshSQLStore(t *testing.T, st store.Store) AccountStore {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Batch(ctx, []store.Stmt{
		{SQL: "DELETE FROM accounts_guard"},
		{SQL: "DELETE FROM login_failures"},
		{SQL: "DELETE FROM sessions"},
		{SQL: "DELETE FROM machines"},
		{SQL: "DELETE FROM invites"},
		{SQL: "DELETE FROM users"},
		{SQL: "DELETE FROM hub_meta"},
	}); err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	return NewSQL(st)
}
