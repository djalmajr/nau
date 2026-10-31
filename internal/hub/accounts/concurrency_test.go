package accounts

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
)

// raceStore wraps an AccountStore with the interleaving points of the
// concurrency scenarios: each non-nil override is the call the
// scenario freezes, so the race the scenario names happens inside the
// window while the rest of the operations run unmodified. The same
// scenarios run over the memory store, where the mutex of the guarded
// operations models the single-statement guarantee, and over the real
// sqld, where the database serializes them.
type raceStore struct {
	AccountStore
	reserve     func(context.Context, string, int64) (bool, error)
	createLogin func(context.Context, SessionRow, string, int64, string) (bool, error)
	password    func(context.Context, string, int64, string, SessionRow) error
	accept      func(context.Context, string, int64, User, SessionRow) error
	userByID    func(context.Context, string) (User, bool, error)
}

func (r *raceStore) ReserveAttempt(c context.Context, n string, at int64) (bool, error) {
	if r.reserve != nil {
		return r.reserve(c, n, at)
	}
	return r.AccountStore.ReserveAttempt(c, n, at)
}

func (r *raceStore) CreateSessionForUser(c context.Context, s SessionRow, userID string, av int64, ph string) (bool, error) {
	if r.createLogin != nil {
		return r.createLogin(c, s, userID, av, ph)
	}
	return r.AccountStore.CreateSessionForUser(c, s, userID, av, ph)
}

func (r *raceStore) ChangePasswordWithNewSession(c context.Context, id string, av int64, hash string, s SessionRow) error {
	if r.password != nil {
		return r.password(c, id, av, hash, s)
	}
	return r.AccountStore.ChangePasswordWithNewSession(c, id, av, hash, s)
}

func (r *raceStore) AcceptInviteWithSession(c context.Context, id string, nowMS int64, u User, s SessionRow) error {
	if r.accept != nil {
		return r.accept(c, id, nowMS, u, s)
	}
	return r.AccountStore.AcceptInviteWithSession(c, id, nowMS, u, s)
}

func (r *raceStore) FindUserByID(c context.Context, id string) (User, bool, error) {
	if r.userByID != nil {
		return r.userByID(c, id)
	}
	return r.AccountStore.FindUserByID(c, id)
}

// concurrentScenario builds one scenario over the given store with the
// real randomness, so the concurrent session ids never collide on the
// deterministic reader.
func concurrentScenario(t *testing.T, st AccountStore) *scenario {
	t.Helper()
	s := newScenario(t, st, newClock(), testBootstrapToken)
	s.a.rand = rand.Reader
	s.bootstrapOwner()
	return s
}

// barrierWait waits for the barrier or fails the test at the deadline.
func barrierWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier timed out")
	}
}

// respOf reads the response of a request that runs in another
// goroutine, or fails the test at the deadline.
func respOf(t *testing.T, ch <-chan hub.Response) hub.Response {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("request timed out")
		return hub.Response{}
	}
}

func bodyMap(v map[string]string) []byte {
	b, _ := json.Marshal(v)
	return b
}

// testStaleLoginAfterPasswordChange freezes a login between the
// credential read and the session write, lets a password change
// commit, and releases the login: the session must not enter, because
// the verified version and hash no longer hold.
func testStaleLoginAfterPasswordChange(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	arrived, release := make(chan struct{}), make(chan struct{})
	w.createLogin = func(ctx context.Context, r SessionRow, userID string, av int64, ph string) (bool, error) {
		close(arrived)
		select {
		case <-release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		return w.AccountStore.CreateSessionForUser(ctx, r, userID, av, ph)
	}
	result := make(chan hub.Response, 1)
	go func() {
		result <- s.do("POST", "/api/session", bodyMap(map[string]string{"email": testOwnerEmail, "password": testOwnerPass}), jsonCT, "")
	}()
	barrierWait(t, arrived)
	changed := s.do("POST", "/api/session/password", bodyMap(map[string]string{"current": testOwnerPass, "new": "replacement-password-test"}), jsonCT, "nau_session="+s.ownerCookie)
	if changed.Status != 204 {
		t.Fatalf("setup password change=%d, want 204", changed.Status)
	}
	close(release)
	r := respOf(t, result)
	t.Logf("password_change=%d stale_login=%d", changed.Status, r.Status)
	if r.Status != 401 {
		t.Fatalf("in-flight login=%d, want 401: the credential rotated while it was in flight", r.Status)
	}
}

// testStalePasswordChange freezes one change before its write, lets a
// second change commit, and releases the first: the stale request must
// not replace the committed password, and its answer is the 401 of a
// lost authority.
func testStalePasswordChange(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	arrived, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.password = func(c context.Context, id string, av int64, hash string, r SessionRow) error {
		blocked := false
		once.Do(func() { blocked = true; close(arrived) })
		if blocked {
			select {
			case <-release:
			case <-c.Done():
				return c.Err()
			}
		}
		return w.AccountStore.ChangePasswordWithNewSession(c, id, av, hash, r)
	}
	result := make(chan hub.Response, 1)
	go func() {
		result <- s.do("POST", "/api/session/password", bodyMap(map[string]string{"current": testOwnerPass, "new": "stale-request-password"}), jsonCT, "nau_session="+s.ownerCookie)
	}()
	barrierWait(t, arrived)
	legitimate := s.do("POST", "/api/session/password", bodyMap(map[string]string{"current": testOwnerPass, "new": "legitimate-new-password"}), jsonCT, "nau_session="+s.ownerCookie)
	close(release)
	stale := respOf(t, result)
	legitLogin, _, _ := login(t, s, testOwnerEmail, "legitimate-new-password")
	staleLogin, _, _ := login(t, s, testOwnerEmail, "stale-request-password")
	t.Logf("legitimate_change=%d stale_change=%d legitimate_new_password_login=%d stale_new_password_login=%d", legitimate.Status, stale.Status, legitLogin, staleLogin)
	if legitLogin != 200 {
		t.Error("the legitimate password does not log in")
	}
	if stale.Status == 204 || staleLogin == 200 {
		t.Error("a request authenticated before the revocation replaced the committed password")
	}
}

// testConcurrentRateLimit seeds four failures of the owner e-mail and
// fires six concurrent logins with the wrong password, synchronized at
// the reservation: only the remaining attempt of the budget may pass
// the reservation and reach the verification, the rest is 429 before
// any check, and the final count is the seeded four plus one.
func testConcurrentRateLimit(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	ctx := context.Background()
	now := s.clock.Now().UnixMilli()
	for i := 0; i < 4; i++ {
		if ok, err := w.ReserveAttempt(ctx, normalizeEmail(testOwnerEmail), now); err != nil || !ok {
			t.Fatalf("seed failure %d: ok=%v err=%v", i, ok, err)
		}
	}
	const n = 6
	var arrived sync.WaitGroup
	arrived.Add(n)
	release := make(chan struct{})
	w.reserve = func(c context.Context, nrm string, at int64) (bool, error) {
		arrived.Done()
		select {
		case <-release:
		case <-c.Done():
			return false, c.Err()
		}
		return w.AccountStore.ReserveAttempt(c, nrm, at)
	}
	statuses := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() {
			r := s.do("POST", "/api/session", bodyMap(map[string]string{"email": "  OWNER@example.test  ", "password": "invalid-password-test"}), jsonCT, "")
			statuses <- r.Status
		}()
	}
	ready := make(chan struct{})
	go func() { arrived.Wait(); close(ready) }()
	barrierWait(t, ready)
	close(release)
	checked, limited := 0, 0
	for i := 0; i < n; i++ {
		select {
		case v := <-statuses:
			switch v {
			case 401:
				checked++
			case 429:
				limited++
			default:
				t.Errorf("unexpected status=%d", v)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("request timed out")
		}
	}
	count, err := w.CountEmailFailures(ctx, normalizeEmail(testOwnerEmail), now-failureWindowMS)
	if err != nil {
		t.Fatalf("count the failures: %v", err)
	}
	t.Logf("initial_failures=4 concurrent_requests=%d checked=%d limited=%d final_failures=%d", n, checked, limited, count)
	if checked != 1 {
		t.Errorf("the remaining budget admitted %d verifications, want exactly 1", checked)
	}
	if limited != n-1 {
		t.Errorf("limited=%d, want %d (every attempt beyond the budget)", limited, n-1)
	}
	if count != 5 {
		t.Errorf("final_failures=%d, want 5 (the four seeded plus the one reserved attempt)", count)
	}
}

// testRevokedInviteRace freezes the accept between the invite read and
// the atomic batch, lets the owner revoke, and releases the accept:
// the revocation must prevent the account creation and the answer is
// the same 400 of an invalid invite.
func testRevokedInviteRace(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	inv := s.createInvite(t, s.ownerCookie, testMemberEmail)
	arrived, release := make(chan struct{}), make(chan struct{})
	w.accept = func(ctx context.Context, id string, nowMS int64, u User, r SessionRow) error {
		close(arrived)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return w.AccountStore.AcceptInviteWithSession(ctx, id, nowMS, u, r)
	}
	result := make(chan hub.Response, 1)
	go func() {
		result <- s.acceptRaw(t, inv.Token, "Test member", testMemberPass)
	}()
	barrierWait(t, arrived)
	revoked := s.do("DELETE", "/api/invites/"+inv.ID, nil, jsonCT, "nau_session="+s.ownerCookie)
	if revoked.Status != 204 {
		t.Fatalf("revoke setup=%d, want 204", revoked.Status)
	}
	close(release)
	r := respOf(t, result)
	_, created, err := w.FindUserByEmailNorm(context.Background(), normalizeEmail(testMemberEmail))
	if err != nil {
		t.Fatalf("look up the member: %v", err)
	}
	t.Logf("revoke=%d accept_after_revoke=%d account_created=%t", revoked.Status, r.Status, created)
	if r.Status != 400 {
		t.Errorf("accept after the revoke=%d, want 400", r.Status)
	}
	if created {
		t.Error("the revocation completed before the consumption but the account was created")
	}
}

// testFailedAcceptIsAtomic injects a local write failure in the
// atomic accept after the invite was read: the invite must survive the
// failure, unused, and the retry must create the account.
func testFailedAcceptIsAtomic(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	inv := s.createInvite(t, s.ownerCookie, testMemberEmail)
	w.accept = func(context.Context, string, int64, User, SessionRow) error {
		return errors.New("injected local write failure")
	}
	first := s.acceptRaw(t, inv.Token, "Test member", testMemberPass)
	w.accept = nil
	retry := s.acceptRaw(t, inv.Token, "Test member", testMemberPass)
	row, found, err := w.FindInviteByID(context.Background(), inv.ID)
	if err != nil {
		t.Fatalf("read the invite: %v", err)
	}
	_, created, err := w.FindUserByEmailNorm(context.Background(), normalizeEmail(testMemberEmail))
	if err != nil {
		t.Fatalf("look up the member: %v", err)
	}
	t.Logf("first_accept=%d retry=%d invite_found=%t used=%t account_created=%t", first.Status, retry.Status, found, row.Used, created)
	if first.Status != 503 {
		t.Errorf("first accept=%d, want 503 (the injected write failure)", first.Status)
	}
	if retry.Status != 201 || !created {
		t.Error("the retry after a failed accept must create the account")
	}
	if row.Used && !created {
		t.Error("a failed user creation permanently consumed the invitation outside its transaction")
	}
}

// testUnknownEmailDummyPassword answers the public dummy password: the
// known e-mail with the wrong password and the unknown e-mail must be
// the same 401, so the response does not reveal which e-mails exist.
func testUnknownEmailDummyPassword(t *testing.T, st AccountStore) {
	t.Helper()
	s := concurrentScenario(t, st)
	known, _, _ := login(t, s, testOwnerEmail, "nau-dummy-verification-password")
	unknown, _, _ := login(t, s, "absent@example.test", "nau-dummy-verification-password")
	t.Logf("known_email=%d unknown_email=%d", known, unknown)
	if known != 401 || unknown != 401 {
		t.Error("the dummy password distinguishes the unknown e-mail from an incorrect password")
	}
}

// testSessionAuthVersionCheck keeps a live session row while the
// credential version moves past it, without a revocation of that row:
// the only mechanism that rejects the session is the auth version
// check, so the answer is 401. The control proves the same session is
// live while the version holds.
func testSessionAuthVersionCheck(t *testing.T, st AccountStore) {
	t.Helper()
	w := &raceStore{AccountStore: st}
	s := concurrentScenario(t, w)
	cookie := s.loginOwner()
	resp := s.do("GET", "/api/session", nil, "", "nau_session="+cookie)
	if resp.Status != 200 {
		t.Fatalf("control GET=%d, want 200 (the session is live while the version holds)", resp.Status)
	}
	w.userByID = func(c context.Context, id string) (User, bool, error) {
		u, found, err := w.AccountStore.FindUserByID(c, id)
		if found {
			u.AuthVersion++
		}
		return u, found, err
	}
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+cookie)
	w.userByID = nil
	t.Logf("version_mismatch_GET=%d", resp.Status)
	if resp.Status != 401 {
		t.Error("a session with a stale auth version stayed live")
	}
}

// runConcurrencyScenarios runs the concurrency scenarios over one
// store factory: the memory store hermetically and, with the sqld
// tag, the real database.
func runConcurrencyScenarios(t *testing.T, fresh func() AccountStore) {
	t.Run("StaleLoginAfterPasswordChange", func(t *testing.T) { testStaleLoginAfterPasswordChange(t, fresh()) })
	t.Run("StalePasswordChange", func(t *testing.T) { testStalePasswordChange(t, fresh()) })
	t.Run("ConcurrentRateLimit", func(t *testing.T) { testConcurrentRateLimit(t, fresh()) })
	t.Run("RevokedInviteRace", func(t *testing.T) { testRevokedInviteRace(t, fresh()) })
	t.Run("FailedAcceptIsAtomic", func(t *testing.T) { testFailedAcceptIsAtomic(t, fresh()) })
	t.Run("UnknownEmailDummyPassword", func(t *testing.T) { testUnknownEmailDummyPassword(t, fresh()) })
	t.Run("SessionAuthVersionCheck", func(t *testing.T) { testSessionAuthVersionCheck(t, fresh()) })
}

// TestMemoryConcurrencyScenarios runs the concurrency scenarios over
// the memory store, whose mutex models the single-statement
// guarantees.
func TestMemoryConcurrencyScenarios(t *testing.T) {
	runConcurrencyScenarios(t, func() AccountStore { return NewMemory() })
}
