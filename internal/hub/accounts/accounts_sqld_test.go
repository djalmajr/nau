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

// freshSQLStore empties the account tables, so every scenario starts
// from the empty state the migrations create. The rows of the other
// layers go away too, in the order that keeps the foreign keys free.
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

// Mutation captured: a separate cleanup cannot roll back the committed credential.
func TestSQLDRootTokenResetDeleteFailureIsAtomic(t *testing.T) {
	st := hubtest.SQLD(t)
	s := newScenario(t, freshSQLStore(t, st), newClock(), testRootTokenValue)
	s.seedOwner()
	if status, _, _ := login(t, s, testOwnerEmail, "wrong-password"); status != 401 {
		t.Fatal(status)
	}
	ctx := context.Background()
	const drop = "DROP TRIGGER IF EXISTS test_reset_delete"
	t.Cleanup(func() {
		if _, err := st.Execute(ctx, store.Stmt{SQL: drop}); err != nil {
			t.Errorf("drop trigger: %v", err)
		}
	})
	if _, err := st.Execute(ctx, store.Stmt{SQL: "CREATE TRIGGER test_reset_delete BEFORE DELETE ON login_failures BEGIN SELECT RAISE(ABORT, 'test'); END"}); err != nil {
		t.Fatal(err)
	}
	resp := s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, "recovered-owner-pass-2"), jsonCT, "")
	// The helper checks the retained budget before successful login clears it.
	if resp.Status != 503 || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("reset with aborting delete=%d", resp.Status)
	}
	owner, found, err := s.store.FindOwner(ctx)
	if err != nil || !found || owner.PasswordHash != s.owner.PasswordHash || owner.AuthVersion != s.owner.AuthVersion {
		t.Fatal("delete failure changed credential")
	}
	count, err := s.store.CountEmailFailures(ctx, s.owner.EmailNorm, s.clock.Now().UnixMilli()-failureWindowMS)
	if err != nil || count != 1 {
		t.Fatalf("retained failures=%d err=%v", count, err)
	}
	if got := s.do("GET", "/api/session", nil, "", "nau_session="+s.ownerCookie); got.Status != 200 {
		t.Fatalf("old session=%d", got.Status)
	}
	if !VerifyPassword(testOwnerPass, owner.PasswordHash) || VerifyPassword("recovered-owner-pass-2", owner.PasswordHash) {
		t.Fatal("password changed after abort")
	}
	if _, err := st.Execute(ctx, store.Stmt{SQL: drop}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := login(t, s, testOwnerEmail, testOwnerPass); status != 200 {
		t.Fatalf("old password login=%d", status)
	}
	if resp = s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, "recovered-owner-pass-2"), jsonCT, ""); resp.Status != 200 {
		t.Fatalf("retry=%d", resp.Status)
	}
	setCookieOf(t, resp)
	if status, _, _ := login(t, s, testOwnerEmail, "recovered-owner-pass-2"); status != 200 {
		t.Fatalf("new password login=%d", status)
	}
}
