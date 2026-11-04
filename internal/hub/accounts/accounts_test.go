package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
)

// mutableNow is the scenario clock: the scenarios advance it to cross
// the 7-day expirations without waiting.
type mutableNow struct {
	t time.Time
}

func (m *mutableNow) Now() time.Time          { return m.t }
func (m *mutableNow) advance(d time.Duration) { m.t = m.t.Add(d) }

// scenario is one scenario run: the Auth and the hub server over one
// fresh store, with the shared scenario clock and the log buffer of
// the recovery lines.
type scenario struct {
	t           *testing.T
	store       AccountStore
	a           *Auth
	srv         *hub.Server
	recSrv      *hub.Server
	key         []byte
	clock       *mutableNow
	log         *bytes.Buffer
	owner       User
	ownerPass   string
	ownerCookie string
}

// The scenario credentials are fixed test values, never secrets. The
// root token is at least 32 bytes, the shape the serve requires.
const (
	testRootTokenValue = "test-root-token-0000000000000000000000"
	testOwnerEmail     = "owner@example.test"
	testOwnerName      = "Ana Dono"
	testOwnerPass      = "owner-owner-pass-1"
	testMemberEmail    = "dev@example.test"
	testMemberName     = "Beto Dev"
	testMemberPass     = "member-member-pass-2"
)

// countingReader is the deterministic randomness of the scenarios: it
// never fails and its values are ordered, so ids and session ids are
// reproducible and distinct.
type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.n)
		r.n = (r.n + 1) % 256
	}
	return len(p), nil
}

// newScenario builds one scenario over the given store with the
// deterministic clock and the root token of the recovery route. The
// public server carries the account routes; the recovery server
// carries the recovery route alone, the same split the hub serve makes
// between the public listener and the recovery listener.
func newScenario(t *testing.T, store AccountStore, clock *mutableNow, rootToken string) *scenario {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	log := &bytes.Buffer{}
	a, err := New(Config{
		Store:      store,
		SessionKey: key,
		RootToken:  rootToken,
		Log:        log,
		Now:        clock.Now,
		Rand:       &countingReader{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := hub.New(hub.Deps{Now: clock.Now, Version: "test"}, nil)
	a.Register(srv)
	recSrv := hub.New(hub.Deps{Now: clock.Now, Version: "test"}, nil)
	a.RegisterRecovery(recSrv)
	return &scenario{t: t, store: store, a: a, srv: srv, recSrv: recSrv, key: key, clock: clock, log: log,
		ownerPass: testOwnerPass}
}

// apiReq builds one hub request with the content type and the cookie.
func apiReq(method, path string, body []byte, ct, cookie string) hub.Request {
	h := make(hub.Header)
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	if cookie != "" {
		h.Set("Cookie", cookie)
	}
	return hub.Request{Method: method, Path: path, Header: h, Body: body}
}

const jsonCT = "application/json"

func (s *scenario) do(method, path string, body []byte, ct, cookie string) hub.Response {
	return s.srv.Serve(context.Background(), apiReq(method, path, body, ct, cookie))
}

// doRec runs one request against the recovery server, the one the hub
// serve binds to the loopback recovery listener: the recovery route
// exists only there.
func (s *scenario) doRec(method, path string, body []byte, ct, cookie string) hub.Response {
	return s.recSrv.Serve(context.Background(), apiReq(method, path, body, ct, cookie))
}

// rootBody is the body of the root-token route.
func rootBody(token, newPass string) []byte {
	b, _ := json.Marshal(map[string]string{"token": token, "new": newPass})
	return b
}

// setCookieOf extracts the signed cookie value of one response.
func setCookieOf(t *testing.T, resp hub.Response) string {
	t.Helper()
	v := resp.Header.Get("Set-Cookie")
	if v == "" {
		t.Fatal("the response has no Set-Cookie header")
	}
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '='); i >= 0 {
		return v[i+1:]
	}
	t.Fatalf("the Set-Cookie %q has no value", resp.Header.Get("Set-Cookie"))
	return ""
}

type sessionJSON struct {
	User struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Role  string `json:"role"`
	} `json:"user"`
}

type inviteJSON struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	ExpiresMS int64  `json:"expires_ms"`
	Token     string `json:"token"`
}

type inviteListJSON struct {
	Invites []struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Role      string `json:"role"`
		CreatedMS int64  `json:"created_ms"`
		ExpiresMS int64  `json:"expires_ms"`
	} `json:"invites"`
}

// seedOwner seeds the owner of the scenario into the empty store and
// logs in: the session of the scenario comes from the login, the same
// as the other accounts.
func (s *scenario) seedOwner() {
	t := s.t
	if err := s.a.SeedOwner(context.Background(), testOwnerName, testOwnerEmail, testOwnerPass); err != nil {
		t.Fatalf("seed the owner: %v", err)
	}
	u, found, err := s.store.FindOwner(context.Background())
	if err != nil || !found {
		t.Fatalf("find the owner: found=%v err=%v", found, err)
	}
	s.owner = u
	s.ownerCookie = s.loginOwner()
}

// loginOwner logs the owner in and returns the new cookie.
func (s *scenario) loginOwner() string {
	body, _ := json.Marshal(map[string]string{"email": testOwnerEmail, "password": testOwnerPass})
	resp := s.do("POST", "/api/session", body, jsonCT, "")
	if resp.Status != 200 {
		s.t.Fatalf("owner login = %d %s, want 200", resp.Status, resp.Body)
	}
	return setCookieOf(s.t, resp)
}

// login is one login attempt, returning the status and the body.
func login(t *testing.T, s *scenario, email, password string) (int, []byte, hub.Response) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp := s.do("POST", "/api/session", body, jsonCT, "")
	return resp.Status, resp.Body, resp
}

// runAccountsScenarios runs the whole scenario suite over one store
// factory: the same scenarios over the in-memory store and, with the
// sqld tag, over the real database.
func runAccountsScenarios(t *testing.T, fresh func() AccountStore) {
	t.Run("SeedOwner", func(t *testing.T) { testSeedOwner(t, fresh) })
	t.Run("RootToken", func(t *testing.T) { testRootToken(t, fresh) })
	t.Run("RootTokenClearsOwnerFailures", func(t *testing.T) { testRootTokenClearsOwnerFailures(t, fresh) })
	t.Run("RootTokenClearFailureError", func(t *testing.T) { testRootTokenClearFailureError(t, fresh) })
	t.Run("SessionContract", func(t *testing.T) { testSessionContract(t, fresh) })
	t.Run("LoginErrors", func(t *testing.T) { testLoginErrors(t, fresh) })
	t.Run("RateLimitPerEmail", func(t *testing.T) { testRateLimitPerEmail(t, fresh) })
	t.Run("RateLimitGlobal", func(t *testing.T) { testRateLimitGlobal(t, fresh) })
	t.Run("RateLimitResetOnSuccess", func(t *testing.T) { testRateLimitResetOnSuccess(t, fresh) })
	t.Run("CookieSecurity", func(t *testing.T) { testCookieSecurity(t, fresh) })
	t.Run("PasswordChange", func(t *testing.T) { testPasswordChange(t, fresh) })
	t.Run("Invites", func(t *testing.T) { testInvites(t, fresh) })
	t.Run("ContentType", func(t *testing.T) { testContentType(t, fresh) })
	t.Run("StrictBody", func(t *testing.T) { testStrictBody(t, fresh) })
}

// newClock is the fresh scenario clock of one scenario: each scenario
// starts at the fixed time, so the advances of one scenario never leak
// into the next.
func newClock() *mutableNow {
	return &mutableNow{t: fixedNow()}
}

// TestMemoryAccountsScenarios runs the whole scenario suite over the
// in-memory store, with the deterministic clock.
func TestMemoryAccountsScenarios(t *testing.T) {
	runAccountsScenarios(t, func() AccountStore { return NewMemory() })
}

// testSeedOwner covers the store contract of the owner seed: the empty
// store has no owner, the seed creates it (the login works, the
// restart with another password does not change the account) and
// the second seed is ErrOwnerExists; with a member in the store
// the owner is still the only owner; and a legacy database that
// holds a user of another e-mail and no owner refuses the seed
// without writing.
func testSeedOwner(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	ctx := context.Background()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	// The empty store has no owner.
	if _, found, err := s.store.FindOwner(ctx); err != nil || found {
		t.Fatalf("FindOwner on the empty store: found=%v err=%v, want none", found, err)
	}
	// The seed creates the owner, without a session.
	if err := s.a.SeedOwner(ctx, testOwnerName, testOwnerEmail, testOwnerPass); err != nil {
		t.Fatalf("SeedOwner: %v", err)
	}
	// A second seed, with another password, is ErrOwnerExists: the
	// restart does not change the account.
	err := s.a.SeedOwner(ctx, testOwnerName, testOwnerEmail, "another-pass-pass-1")
	if !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("the second SeedOwner = %v, want ErrOwnerExists", err)
	}
	u, found, err := s.store.FindOwner(ctx)
	if err != nil || !found {
		t.Fatalf("FindOwner after the seed: found=%v err=%v", found, err)
	}
	if u.Email != testOwnerEmail || u.Name != testOwnerName || u.Role != RoleOwner || u.AuthVersion != 1 || u.ID == "" || u.PasswordHash == "" {
		t.Fatalf("the owner = %+v", u)
	}
	// The login with the seeded owner works: the seed wrote a live
	// credential, and the account carries the seeded password, not the
	// one of the refused restart.
	status, body, _ := login(t, s, testOwnerEmail, testOwnerPass)
	if status != 200 {
		t.Fatalf("login with the seeded password = %d %s, want 200", status, body)
	}
	status, body, _ = login(t, s, testOwnerEmail, "another-pass-pass-1")
	if status != 401 {
		t.Fatalf("login with the password of the refused restart = %d %s, want 401", status, body)
	}
	// With a member in the store the owner is still the only owner: the
	// invites create the member only.
	s.owner = u
	s.ownerCookie = s.loginOwner()
	invite := s.createInvite(t, s.ownerCookie, testMemberEmail)
	s.acceptInvite(t, invite.Token, testMemberName, testMemberPass)
	u2, found, err := s.store.FindOwner(ctx)
	if err != nil || !found || u2.ID != u.ID {
		t.Fatalf("FindOwner with a member: found=%v id=%q err=%v, want the same owner", found, u2.ID, err)
	}
	// A legacy database with a user of another e-mail and no owner
	// (a user created outside the seed flow) refuses the seed, with
	// no write: the e-mail does not need to match.
	legacy := newScenario(t, fresh(), newClock(), "")
	memberHash, err := HashPassword(&countingReader{}, testMemberPass)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	tok := make([]byte, sessionIDSize)
	copy(tok, []byte("legacy-invite-token"))
	inv := Invite{
		ID:        "legacy-invite",
		Email:     testMemberEmail,
		EmailNorm: normalizeEmail(testMemberEmail),
		Role:      RoleMember,
		TokenHash: sha256Hex(tok),
		CreatedMS: fixedNow().UnixMilli(),
		ExpiresMS: fixedNow().Add(time.Hour).UnixMilli(),
	}
	if err := legacy.store.CreateInvite(ctx, inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	member := User{
		ID:           "legacy-member",
		Email:        testMemberEmail,
		EmailNorm:    inv.EmailNorm,
		Name:         testMemberName,
		PasswordHash: memberHash,
		Role:         RoleMember,
		AuthVersion:  1,
		CreatedMS:    fixedNow().UnixMilli(),
	}
	sess, _, err := NewSession(&countingReader{}, legacy.key, fixedNow())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.UserID = member.ID
	sess.AuthVersion = 1
	if err := legacy.store.AcceptInviteWithSession(ctx, inv.ID, fixedNow().UnixMilli(), member, sess); err != nil {
		t.Fatalf("AcceptInviteWithSession: %v", err)
	}
	// The seed is refused and nothing is written: the user is still
	// the only user, and there is no owner.
	if err := legacy.a.SeedOwner(ctx, testOwnerName, testOwnerEmail, testOwnerPass); !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("SeedOwner on the legacy database = %v, want ErrOwnerExists", err)
	}
	count, err := legacy.store.CountUsers(ctx)
	if err != nil || count != 1 {
		t.Fatalf("CountUsers after the refused seed = %d err=%v, want 1", count, err)
	}
	if _, found, err := legacy.store.FindOwner(ctx); err != nil || found {
		t.Fatalf("FindOwner after the refused seed: found=%v err=%v, want none", found, err)
	}
	if u, found, err := legacy.store.FindUserByEmailNorm(ctx, inv.EmailNorm); err != nil || !found || u.Role != RoleMember {
		t.Fatalf("the legacy member moved: found=%v err=%v", found, err)
	}
}

// assertRoot404 requires that the response is the 404 of the unknown
// route: the same status and the same body, byte for byte.
func assertRoot404(t *testing.T, resp, unknown hub.Response) {
	t.Helper()
	if resp.Status != unknown.Status || string(resp.Body) != string(unknown.Body) {
		t.Fatalf("= %d %q, want the 404 of the unknown route %d %q", resp.Status, resp.Body, unknown.Status, unknown.Body)
	}
}

// testRootToken covers the root-token route on the recovery server:
// the public server has no such route at all, the 404 of the unknown
// route; with the root token absent, the route answers the 404 of the
// unknown route on the recovery server too; with it set, the 415 and
// the 400 of the invalid body; the 401 of the wrong token, with or
// without the right length, before the password policy, and without
// any write; the 400 of the weak password, without any write; the 409
// of the absent owner; and the success that redefines the password,
// revokes the old session and answers with the cookie.
func testRootToken(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	ctx := context.Background()
	// The reference 404 of an unknown /api/ route, and the public
	// server: the recovery route is not registered there at all, so
	// even the right token and body get the same 404, in the wrong
	// method as in the right one.
	sref := newScenario(t, fresh(), clock, testRootTokenValue)
	unknown := sref.do("POST", "/api/no-such-route", []byte(`{"x":1}`), jsonCT, "")
	if unknown.Status != 404 || string(unknown.Body) != `{"error":"not_found"}` {
		t.Fatalf("the reference 404 = %d %s, want 404 not_found", unknown.Status, unknown.Body)
	}
	assertRoot404(t, sref.do("POST", "/api/session/root-token", rootBody(testRootTokenValue, testOwnerPass), jsonCT, ""), unknown)
	assertRoot404(t, sref.do("GET", "/api/session/root-token", nil, "", ""), unknown)
	// Without the variable the route does not exist on the recovery
	// server either: the same 404, even with the right body.
	s := newScenario(t, fresh(), clock, "")
	assertRoot404(t, s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, testOwnerPass), jsonCT, ""), unknown)
	// With the variable: the 415 without the content type, and the 400
	// of the invalid body.
	s = newScenario(t, fresh(), clock, testRootTokenValue)
	resp := s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, testOwnerPass), "", "")
	if resp.Status != 415 || string(resp.Body) != `{"error":"unsupported_media_type"}` {
		t.Fatalf("no content type: %d %s, want 415", resp.Status, resp.Body)
	}
	resp = s.doRec("POST", "/api/session/root-token",
		[]byte(`{"token":"`+testRootTokenValue+`","new":"`+testOwnerPass+`","extra":1}`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("unknown field: %d %s, want 400 bad_request", resp.Status, resp.Body)
	}
	resp = s.doRec("POST", "/api/session/root-token", []byte(`not json`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("invalid json: %d %s, want 400 bad_request", resp.Status, resp.Body)
	}
	// The absent owner: on the empty store the right token and the
	// right password are the 409 no_owner, before any write.
	sEmpty := newScenario(t, fresh(), clock, testRootTokenValue)
	resp = sEmpty.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, testOwnerPass), jsonCT, "")
	if resp.Status != 409 || string(resp.Body) != `{"error":"no_owner"}` {
		t.Fatalf("without an owner: %d %s, want 409 no_owner", resp.Status, resp.Body)
	}
	if n, err := sEmpty.store.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("the 409 wrote a user: n=%d err=%v, want 0", n, err)
	}
	// With the owner seeded: the wrong token (any length) is 401,
	// before the password policy, and writes nothing.
	s.seedOwner()
	for name, token := range map[string]string{
		"wrong":      "wrong-token-0000000000000000000000",
		"short":      "x",
		"wrong+weak": "wrong-token-0000000000000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			newPass := testOwnerPass
			if name == "wrong+weak" {
				newPass = "short"
			}
			resp := s.doRec("POST", "/api/session/root-token", rootBody(token, newPass), jsonCT, "")
			if resp.Status != 401 || string(resp.Body) != `{"error":"invalid_token"}` {
				t.Fatalf("%s: %d %s, want 401 invalid_token", name, resp.Status, resp.Body)
			}
			status, body, _ := login(t, s, testOwnerEmail, testOwnerPass)
			if status != 200 {
				t.Fatalf("the 401 wrote something: login = %d %s, want 200", status, body)
			}
			u, found, err := s.store.FindOwner(ctx)
			if err != nil || !found || u.PasswordHash != s.owner.PasswordHash {
				t.Fatalf("the 401 changed the credential: found=%v err=%v", found, err)
			}
		})
	}
	// The right token with a weak password is 400 and writes nothing.
	resp = s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, "short"), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"weak_password"}` {
		t.Fatalf("weak password: %d %s, want 400 weak_password", resp.Status, resp.Body)
	}
	status, body, _ := login(t, s, testOwnerEmail, testOwnerPass)
	if status != 200 {
		t.Fatalf("the 400 wrote something: login = %d %s, want 200", status, body)
	}
	// The success: the 200 with the cookie, the old session revoked in
	// the store, the old password out and the new one in.
	const newPass = "new-new-pass-pass-1"
	resp = s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, newPass), jsonCT, "")
	if resp.Status != 200 {
		t.Fatalf("the reset = %d %s, want 200", resp.Status, resp.Body)
	}
	var got sessionJSON
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("the reset body: %v", err)
	}
	if got.User.Email != testOwnerEmail || got.User.Role != RoleOwner {
		t.Fatalf("the reset user = %+v", got.User)
	}
	freshCookie := setCookieOf(t, resp)
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+s.ownerCookie)
	if resp.Status != 401 {
		t.Fatalf("the old session is still live: %d", resp.Status)
	}
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+freshCookie)
	if resp.Status != 200 {
		t.Fatalf("the new session = %d %s, want 200", resp.Status, resp.Body)
	}
	// The revocation is in the store: the row of the old session
	// carries revoked, and the row of the new one does not.
	oldSHA, ok := ParseSession(s.ownerCookie, s.key)
	if !ok {
		t.Fatal("the old cookie does not parse")
	}
	oldRow, found, err := s.store.FindSession(ctx, oldSHA)
	if err != nil || !found || !oldRow.Revoked {
		t.Fatalf("the old session row: found=%v revoked=%v err=%v, want revoked", found, oldRow.Revoked, err)
	}
	newSHA, ok := ParseSession(freshCookie, s.key)
	if !ok {
		t.Fatal("the new cookie does not parse")
	}
	newRow, found, err := s.store.FindSession(ctx, newSHA)
	if err != nil || !found || newRow.Revoked {
		t.Fatalf("the new session row: found=%v revoked=%v err=%v, want live", found, newRow.Revoked, err)
	}
	status, body, _ = login(t, s, testOwnerEmail, testOwnerPass)
	if status != 401 {
		t.Fatalf("the old password still logs in: %d %s", status, body)
	}
	status, body, _ = login(t, s, testOwnerEmail, newPass)
	if status != 200 {
		t.Fatalf("the new password does not log in: %d %s", status, body)
	}
	// The credential rotated: the auth version of the owner moved.
	u, found, err := s.store.FindOwner(ctx)
	if err != nil || !found || u.AuthVersion != s.owner.AuthVersion+1 {
		t.Fatalf("the auth version = %d found=%v err=%v, want the bump", u.AuthVersion, found, err)
	}
	// The log lines: one per wrong token and one per reset, with no
	// token, no password and no e-mail of any value.
	log := s.log.String()
	if n := strings.Count(log, rootTokenLogWrong); n != 3 {
		t.Fatalf("the log has %d wrong-token lines, want 3: %q", n, log)
	}
	if n := strings.Count(log, rootTokenLogReset); n != 1 {
		t.Fatalf("the log has %d reset lines, want 1: %q", n, log)
	}
	for _, secret := range []string{testRootTokenValue, testOwnerPass, newPass, testOwnerEmail} {
		if strings.Contains(log, secret) {
			t.Fatalf("the log carries the value %q: %q", secret, log)
		}
	}
}

func testSessionContract(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	// GET without a session is 401 unauthenticated.
	resp := s.do("GET", "/api/session", nil, "", "")
	if resp.Status != 401 || string(resp.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("GET without session: %d %s", resp.Status, resp.Body)
	}
	// GET with the seeded owner's cookie is 200 with the owner.
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+s.ownerCookie)
	if resp.Status != 200 {
		t.Fatalf("GET with session: %d %s", resp.Status, resp.Body)
	}
	var got sessionJSON
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("session body: %v", err)
	}
	if got.User.Email != testOwnerEmail || got.User.Role != "owner" || got.User.Name != testOwnerName {
		t.Fatalf("session user = %+v", got.User)
	}
	// The logout is 204 with the cleared cookie.
	resp = s.do("DELETE", "/api/session", nil, jsonCT, "nau_session="+s.ownerCookie)
	if resp.Status != 204 {
		t.Fatalf("logout = %d %s, want 204", resp.Status, resp.Body)
	}
	cleared := resp.Header.Get("Set-Cookie")
	if !strings.Contains(cleared, "Max-Age=0") || strings.Contains(cleared, s.ownerCookie) {
		t.Fatalf("the cleared cookie = %q", cleared)
	}
	// The repeated cookie after the logout is refused.
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+s.ownerCookie)
	if resp.Status != 401 || string(resp.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("repeated cookie: %d %s, want 401", resp.Status, resp.Body)
	}
	// The logout is still 204 with a cookie that no longer exists.
	resp = s.do("DELETE", "/api/session", nil, jsonCT, "nau_session="+s.ownerCookie)
	if resp.Status != 204 {
		t.Fatalf("logout again = %d, want 204", resp.Status)
	}
}

func testLoginErrors(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	// A wrong password and an unknown e-mail answer the exact same
	// 401 invalid_credentials.
	wrong, bodyWrong, _ := login(t, s, testOwnerEmail, "wrong-wrong-pass-1")
	if wrong != 401 || string(bodyWrong) != `{"error":"invalid_credentials"}` {
		t.Fatalf("wrong password: %d %s", wrong, bodyWrong)
	}
	unknown, bodyUnknown, _ := login(t, s, "ghost@example.test", testOwnerPass)
	if unknown != 401 || string(bodyUnknown) != `{"error":"invalid_credentials"}` {
		t.Fatalf("unknown e-mail: %d %s", unknown, bodyUnknown)
	}
	if string(bodyWrong) != string(bodyUnknown) {
		t.Fatalf("the two 401 bodies differ: %q vs %q", bodyWrong, bodyUnknown)
	}
	// The e-mail is matched normalized: upper case and edge spaces log
	// the same owner in.
	status, body, resp := login(t, s, "  OWNER@example.test  ", testOwnerPass)
	if status != 200 {
		t.Fatalf("normalized login = %d %s, want 200", status, body)
	}
	if setCookieOf(t, resp) == "" {
		t.Fatal("the normalized login has no cookie")
	}
}

func testRateLimitPerEmail(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	now := s.clock.Now().UnixMilli()
	// Five failures of the e-mail inside the window: the sixth attempt
	// is 429, even with the right password.
	for i := 0; i < perEmailMaxFailures; i++ {
		if err := s.store.RecordFailure(context.Background(), normalizeEmail(testOwnerEmail), now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ := login(t, s, testOwnerEmail, testOwnerPass)
	if status != 429 || string(body) != `{"error":"rate_limited"}` {
		t.Fatalf("per-e-mail limit: %d %s, want 429 rate_limited", status, body)
	}
	// The 429 does not depend on the e-mail existing: an unknown
	// e-mail with the same five failures is 429 too.
	for i := 0; i < perEmailMaxFailures; i++ {
		if err := s.store.RecordFailure(context.Background(), "limited-unknown@example.test", now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ = login(t, s, "limited-unknown@example.test", testOwnerPass)
	if status != 429 || string(body) != `{"error":"rate_limited"}` {
		t.Fatalf("per-e-mail limit of an unknown e-mail: %d %s", status, body)
	}
}

func testRateLimitGlobal(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	now := s.clock.Now().UnixMilli()
	// One hundred failures summed over many e-mails: every e-mail is
	// 429, even one with zero failures of its own.
	for i := 0; i < globalMaxFailures; i++ {
		email := "global-" + itoa(i) + "@example.test"
		if err := s.store.RecordFailure(context.Background(), email, now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ := login(t, s, "fresh@example.test", testOwnerPass)
	if status != 429 || string(body) != `{"error":"rate_limited"}` {
		t.Fatalf("global limit: %d %s, want 429 rate_limited", status, body)
	}
	// The limit is inside the window: on a clean store, failures older
	// than the window do not count and the login passes.
	sclean := newScenario(t, fresh(), clock, testRootTokenValue)
	sclean.seedOwner()
	old := sclean.clock.Now().UnixMilli() - failureWindowMS - 1
	for i := 0; i < globalMaxFailures; i++ {
		email := "old-" + itoa(i) + "@example.test"
		if err := sclean.store.RecordFailure(context.Background(), email, old); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ = login(t, sclean, testOwnerEmail, testOwnerPass)
	if status != 200 {
		t.Fatalf("old failures do not count: %d %s, want 200", status, body)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func testRateLimitResetOnSuccess(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	now := s.clock.Now().UnixMilli()
	norm := normalizeEmail(testOwnerEmail)
	for i := 0; i < perEmailMaxFailures-1; i++ {
		if err := s.store.RecordFailure(context.Background(), norm, now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	// A success clears the counter of the e-mail.
	status, body, _ := login(t, s, testOwnerEmail, testOwnerPass)
	if status != 200 {
		t.Fatalf("login after the clear: %d %s, want 200", status, body)
	}
	// Four more failures: without the reset the total would be eight
	// and the login below would be 429; with the reset it is four and
	// the login passes.
	for i := 0; i < perEmailMaxFailures-1; i++ {
		if err := s.store.RecordFailure(context.Background(), norm, now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ = login(t, s, testOwnerEmail, testOwnerPass)
	if status != 200 {
		t.Fatalf("login after the reset: %d %s, want 200", status, body)
	}
	// One more failure and the login is 429 again: the counter moved.
	// (Each success above cleared it, so the full five is seeded here.)
	for i := 0; i < perEmailMaxFailures; i++ {
		if err := s.store.RecordFailure(context.Background(), norm, now); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	status, body, _ = login(t, s, testOwnerEmail, testOwnerPass)
	if status != 429 {
		t.Fatalf("login after the fifth failure: %d %s, want 429", status, body)
	}
}

func testCookieSecurity(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	cookie := s.loginOwner()
	// A cookie signed with the wrong key is refused: the row exists,
	// but the signature does not verify with the server key.
	wrongKey := make([]byte, 32)
	for i := range wrongKey {
		wrongKey[i] = byte(i + 1)
	}
	row, forged, err := NewSession(&countingReader{}, wrongKey, s.clock.Now())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// The row points at the real owner, so the foreign key holds over
	// the real store: the point of the forgery is the signature, not
	// the user.
	row.UserID = s.owner.ID
	if err := s.store.CreateSession(context.Background(), row); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	resp := s.do("GET", "/api/session", nil, "", "nau_session="+forged)
	if resp.Status != 401 || string(resp.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("forged cookie: %d %s, want 401", resp.Status, resp.Body)
	}
	// A tampered id part of a valid cookie is refused.
	tampered := mutateID(cookie, 0)
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+tampered)
	if resp.Status != 401 {
		t.Fatalf("tampered cookie: %d, want 401", resp.Status)
	}
	// The session expires absolutely: seven days later the same cookie
	// is refused.
	cookie = s.loginOwner()
	s.clock.advance(7*24*time.Hour + time.Second)
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+cookie)
	if resp.Status != 401 || string(resp.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("expired cookie: %d %s, want 401", resp.Status, resp.Body)
	}
}

func testPasswordChange(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	a := s.loginOwner()
	b := s.loginOwner()
	// A body without the current password is refused like a wrong one,
	// and without the new one the policy refuses: no session skips the
	// current password.
	body, _ := json.Marshal(map[string]string{"new": "new-new-pass-pass-1"})
	resp := s.do("POST", "/api/session/password", body, jsonCT, "nau_session="+b)
	if resp.Status != 401 || string(resp.Body) != `{"error":"invalid_credentials"}` {
		t.Fatalf("without current: %d %s, want 401", resp.Status, resp.Body)
	}
	body, _ = json.Marshal(map[string]string{"current": testOwnerPass})
	resp = s.do("POST", "/api/session/password", body, jsonCT, "nau_session="+b)
	if resp.Status != 400 || string(resp.Body) != `{"error":"weak_password"}` {
		t.Fatalf("without new: %d %s, want 400 weak_password", resp.Status, resp.Body)
	}
	// The wrong current password is 401.
	body, _ = json.Marshal(map[string]string{"current": "wrong-current-pass-1", "new": "new-new-pass-pass-1"})
	resp = s.do("POST", "/api/session/password", body, jsonCT, "nau_session="+b)
	if resp.Status != 401 || string(resp.Body) != `{"error":"invalid_credentials"}` {
		t.Fatalf("wrong current: %d %s", resp.Status, resp.Body)
	}
	// A weak new password is 400 weak_password.
	body, _ = json.Marshal(map[string]string{"current": testOwnerPass, "new": "short"})
	resp = s.do("POST", "/api/session/password", body, jsonCT, "nau_session="+b)
	if resp.Status != 400 || string(resp.Body) != `{"error":"weak_password"}` {
		t.Fatalf("weak new: %d %s", resp.Status, resp.Body)
	}
	// The change is 204 with the new cookie.
	const newPass = "new-new-pass-pass-1"
	body, _ = json.Marshal(map[string]string{"current": testOwnerPass, "new": newPass})
	resp = s.do("POST", "/api/session/password", body, jsonCT, "nau_session="+b)
	if resp.Status != 204 {
		t.Fatalf("change = %d %s, want 204", resp.Status, resp.Body)
	}
	c := setCookieOf(t, resp)
	// Every previous session is revoked, the new one is live.
	for name, old := range map[string]string{"first": a, "changing": b} {
		resp := s.do("GET", "/api/session", nil, "", "nau_session="+old)
		if resp.Status != 401 {
			t.Fatalf("the %s cookie is still valid: %d", name, resp.Status)
		}
	}
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+c)
	if resp.Status != 200 {
		t.Fatalf("the new cookie = %d, want 200", resp.Status)
	}
	// The old password does not log in, the new one does.
	status, bodyLogin, _ := login(t, s, testOwnerEmail, testOwnerPass)
	if status != 401 {
		t.Fatalf("the old password still logs in: %d %s", status, bodyLogin)
	}
	status, bodyLogin, _ = login(t, s, testOwnerEmail, newPass)
	if status != 200 {
		t.Fatalf("the new password does not log in: %d %s", status, bodyLogin)
	}
}

func testInvites(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	cookie := s.loginOwner()
	// The member is created through an invite of the owner; the accept
	// consumes that invite.
	memberInvite := s.createInvite(t, cookie, testMemberEmail)
	memberCookie := s.acceptInvite(t, memberInvite.Token, testMemberName, testMemberPass)
	// The member login works with the new account.
	status, body, _ := login(t, s, testMemberEmail, testMemberPass)
	if status != 200 {
		t.Fatalf("the member login = %d %s, want 200", status, body)
	}
	// The list shows the pending invites: the consumed one is out.
	pending := s.createInvite(t, cookie, "pending@example.test")
	resp := s.do("GET", "/api/invites", nil, "", "nau_session="+cookie)
	if resp.Status != 200 {
		t.Fatalf("list = %d %s", resp.Status, resp.Body)
	}
	var list inviteListJSON
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		t.Fatalf("list body: %v", err)
	}
	if len(list.Invites) != 1 || list.Invites[0].ID != pending.ID || list.Invites[0].Email != "pending@example.test" || list.Invites[0].Role != RoleMember {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(string(resp.Body), pending.Token) {
		t.Fatal("the list leaks the invite token")
	}
	if memberInvite.ExpiresMS != s.clock.Now().Add(InviteTTL).UnixMilli() {
		t.Fatalf("the invite expires at %d", memberInvite.ExpiresMS)
	}
	if len(memberInvite.Token) != 43 {
		t.Fatalf("the invite token has %d chars, want 43", len(memberInvite.Token))
	}
	// A member cannot create, list or revoke: 403 forbidden.
	bodyJSON, _ := json.Marshal(map[string]string{"email": "x@example.test", "role": RoleMember})
	resp = s.do("POST", "/api/invites", bodyJSON, jsonCT, "nau_session="+memberCookie)
	if resp.Status != 403 || string(resp.Body) != `{"error":"forbidden"}` {
		t.Fatalf("member creates an invite: %d %s, want 403", resp.Status, resp.Body)
	}
	resp = s.do("GET", "/api/invites", nil, "", "nau_session="+memberCookie)
	if resp.Status != 403 {
		t.Fatalf("member lists the invites: %d %s, want 403", resp.Status, resp.Body)
	}
	// The owner revokes the pending invite: 204, and the accept of it
	// is 400 invalid_invite.
	resp = s.do("DELETE", "/api/invites/"+pending.ID, nil, jsonCT, "nau_session="+cookie)
	if resp.Status != 204 {
		t.Fatalf("revoke = %d %s, want 204", resp.Status, resp.Body)
	}
	resp = s.acceptRaw(t, pending.Token, "Pending Dev", testMemberPass)
	if resp.Status != 400 || string(resp.Body) != `{"error":"invalid_invite"}` {
		t.Fatalf("accept of a revoked invite: %d %s", resp.Status, resp.Body)
	}
	// An unknown id is 404, for the owner.
	resp = s.do("DELETE", "/api/invites/does-not-exist", nil, jsonCT, "nau_session="+cookie)
	if resp.Status != 404 || string(resp.Body) != `{"error":"not_found"}` {
		t.Fatalf("revoke of an unknown id: %d %s", resp.Status, resp.Body)
	}
	// The member cannot revoke the new invite either.
	expiring := s.createInvite(t, cookie, "expiring@example.test")
	resp = s.do("DELETE", "/api/invites/"+expiring.ID, nil, jsonCT, "nau_session="+memberCookie)
	if resp.Status != 403 {
		t.Fatalf("member revokes: %d %s, want 403", resp.Status, resp.Body)
	}
	// An expired invite is 400 invalid_invite: the clock crosses the
	// seven days of the invite.
	s.clock.advance(7*24*time.Hour + time.Second)
	resp = s.acceptRaw(t, expiring.Token, "Expiring Dev", testMemberPass)
	if resp.Status != 400 || string(resp.Body) != `{"error":"invalid_invite"}` {
		t.Fatalf("accept of an expired invite: %d %s", resp.Status, resp.Body)
	}
	// The owner session expired with the advance: a fresh login for
	// the rest of the scenario.
	cookie = s.loginOwner()
	// An unknown token and a malformed token are the same 400.
	resp = s.acceptRaw(t, "AAAA"+strings.Repeat("B", 41), "Unknown", testMemberPass)
	if resp.Status != 400 || string(resp.Body) != `{"error":"invalid_invite"}` {
		t.Fatalf("accept of an unknown token: %d %s", resp.Status, resp.Body)
	}
	resp = s.acceptRaw(t, "not-a-base64url!!", "Unknown", testMemberPass)
	if resp.Status != 400 || string(resp.Body) != `{"error":"invalid_invite"}` {
		t.Fatalf("accept of a malformed token: %d %s", resp.Status, resp.Body)
	}
	// A weak password is 400 weak_password, before the invite is read.
	resp = s.acceptRawWeak(t, expiring.Token, "Weak Dev", "short")
	if resp.Status != 400 || string(resp.Body) != `{"error":"weak_password"}` {
		t.Fatalf("accept with a weak password: %d %s", resp.Status, resp.Body)
	}
	// An e-mail that already has an account is 409 email_taken: the
	// owner's e-mail already does, and the invite is not consumed.
	taken := s.createInvite(t, cookie, testOwnerEmail)
	resp = s.acceptRaw(t, taken.Token, "Owner Clone", testMemberPass)
	if resp.Status != 409 || string(resp.Body) != `{"error":"email_taken"}` {
		t.Fatalf("accept with a taken e-mail: %d %s", resp.Status, resp.Body)
	}
	// The invite survives the 409: the owner's account already exists,
	// but the invite is still usable for the right owner flow is not
	// re-run here; instead, the second use of a valid invite is 400
	// invalid_invite: a fresh invite is accepted once and refused on
	// the second use.
	twice := s.createInvite(t, cookie, "twice@example.test")
	first := s.acceptRaw(t, twice.Token, "Twice Dev", testMemberPass)
	if first.Status != 201 {
		t.Fatalf("the first accept = %d %s, want 201", first.Status, first.Body)
	}
	resp = s.acceptRaw(t, twice.Token, "Twice Dev", testMemberPass)
	if resp.Status != 400 || string(resp.Body) != `{"error":"invalid_invite"}` {
		t.Fatalf("the second accept: %d %s, want 400 invalid_invite", resp.Status, resp.Body)
	}
	// The final list: the consumed and the revoked invites are out,
	// the pending ones (expired or not) stay.
	resp = s.do("GET", "/api/invites", nil, "", "nau_session="+cookie)
	if resp.Status != 200 {
		t.Fatalf("final list = %d %s", resp.Status, resp.Body)
	}
	var final inviteListJSON
	if err := json.Unmarshal(resp.Body, &final); err != nil {
		t.Fatalf("final list: %v", err)
	}
	seen := make(map[string]bool, len(final.Invites))
	for _, inv := range final.Invites {
		seen[inv.ID] = true
	}
	if seen[memberInvite.ID] {
		t.Fatal("the consumed member invite is still listed")
	}
	if seen[pending.ID] {
		t.Fatal("the revoked invite is still listed")
	}
	if seen[twice.ID] {
		t.Fatal("the used invite is still listed")
	}
	if !seen[expiring.ID] || !seen[taken.ID] {
		t.Fatalf("the pending invites are not listed: %+v", final.Invites)
	}
}

// createInvite issues one invite as the owner and returns it.
func (s *scenario) createInvite(t *testing.T, cookie, email string) inviteJSON {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "role": RoleMember})
	resp := s.do("POST", "/api/invites", body, jsonCT, "nau_session="+cookie)
	if resp.Status != 201 {
		t.Fatalf("create invite = %d %s, want 201", resp.Status, resp.Body)
	}
	var got inviteJSON
	if err := json.Unmarshal(resp.Body, &got); err != nil {
		t.Fatalf("invite body: %v", err)
	}
	if got.ID == "" || got.Email != email || got.Token == "" || got.ExpiresMS == 0 {
		t.Fatalf("invite = %+v", got)
	}
	return got
}

// acceptInvite accepts one invite and returns the cookie of the new
// session.
func (s *scenario) acceptInvite(t *testing.T, token, name, password string) string {
	t.Helper()
	resp := s.acceptRaw(t, token, name, password)
	if resp.Status != 201 {
		t.Fatalf("accept = %d %s, want 201", resp.Status, resp.Body)
	}
	return setCookieOf(t, resp)
}

// acceptRaw accepts one invite and returns the full response.
func (s *scenario) acceptRaw(t *testing.T, token, name, password string) hub.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "password": password})
	return s.do("POST", "/api/invites/accept", body, jsonCT, "")
}

// acceptRawWeak accepts with a weak password.
func (s *scenario) acceptRawWeak(t *testing.T, token, name, password string) hub.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "password": password})
	return s.do("POST", "/api/invites/accept", body, jsonCT, "")
}

func testContentType(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	body, _ := json.Marshal(map[string]string{"email": testOwnerEmail, "password": testOwnerPass})
	// A POST without the content type is 415 unsupported_media_type.
	resp := s.do("POST", "/api/session", body, "", "")
	if resp.Status != 415 || string(resp.Body) != `{"error":"unsupported_media_type"}` {
		t.Fatalf("no content type: %d %s, want 415", resp.Status, resp.Body)
	}
	// A different media type is 415.
	resp = s.do("POST", "/api/session", body, "text/json", "")
	if resp.Status != 415 {
		t.Fatalf("text/json: %d %s, want 415", resp.Status, resp.Body)
	}
	// The parameters of the media type are accepted.
	resp = s.do("POST", "/api/session", body, "application/json; charset=utf-8", "")
	if resp.Status != 200 {
		t.Fatalf("charset parameter: %d %s, want 200", resp.Status, resp.Body)
	}
	// The media type is case-insensitive.
	resp = s.do("POST", "/api/session", body, "APPLICATION/JSON", "")
	if resp.Status != 200 {
		t.Fatalf("uppercase media type: %d %s, want 200", resp.Status, resp.Body)
	}
	// A DELETE under /api/ is gated by the content type too: without
	// it, the logout is 415 (the CSRF defense of the decision).
	resp = s.do("DELETE", "/api/session", nil, "", "nau_session="+s.ownerCookie)
	if resp.Status != 415 {
		t.Fatalf("DELETE without content type: %d %s, want 415", resp.Status, resp.Body)
	}
	// With the content type the logout revokes the session.
	resp = s.do("DELETE", "/api/session", nil, jsonCT, "nau_session="+s.ownerCookie)
	if resp.Status != 204 {
		t.Fatalf("logout with content type: %d %s, want 204", resp.Status, resp.Body)
	}
	// The cookie is revoked: the next GET with it is 401.
	resp = s.do("GET", "/api/session", nil, "", "nau_session="+s.ownerCookie)
	if resp.Status != 401 {
		t.Fatalf("GET after the logout: %d %s, want 401", resp.Status, resp.Body)
	}
	// A GET is not gated by the content type.
	resp = s.do("GET", "/api/session", nil, "", "")
	if resp.Status != 401 {
		t.Fatalf("GET without a session: %d %s, want 401", resp.Status, resp.Body)
	}
}

func testStrictBody(t *testing.T, fresh func() AccountStore) {
	clock := newClock()
	s := newScenario(t, fresh(), clock, testRootTokenValue)
	s.seedOwner()
	// An unknown field is 400 bad_request.
	resp := s.do("POST", "/api/session",
		[]byte(`{"email":"`+testOwnerEmail+`","password":"`+testOwnerPass+`","extra":1}`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("unknown field: %d %s, want 400 bad_request", resp.Status, resp.Body)
	}
	// Invalid JSON is 400.
	resp = s.do("POST", "/api/session", []byte(`not json`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("invalid json: %d %s", resp.Status, resp.Body)
	}
	// Data after the JSON object is 400.
	resp = s.do("POST", "/api/session",
		[]byte(`{"email":"`+testOwnerEmail+`","password":"`+testOwnerPass+`"}{"x":1}`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("trailing data: %d %s", resp.Status, resp.Body)
	}
	// An empty body is 400.
	resp = s.do("POST", "/api/session", nil, jsonCT, "")
	if resp.Status != 400 {
		t.Fatalf("empty body: %d %s, want 400", resp.Status, resp.Body)
	}
	// The unknown field is refused on the root-token route too, on the
	// recovery server of the test.
	resp = s.doRec("POST", "/api/session/root-token",
		[]byte(`{"token":"`+testRootTokenValue+`","new":"`+testOwnerPass+`","extra":1}`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("root-token unknown field: %d %s, want 400", resp.Status, resp.Body)
	}
	// And on the accept, where the body must stay 400 bad_request, not
	// 400 invalid_invite: the parse fails before the token check.
	resp = s.do("POST", "/api/invites/accept",
		[]byte(`{"token":"`+strings.Repeat("A", 43)+`","name":"x","password":"`+testMemberPass+`","admin":true}`), jsonCT, "")
	if resp.Status != 400 || string(resp.Body) != `{"error":"bad_request"}` {
		t.Fatalf("accept unknown field: %d %s", resp.Status, resp.Body)
	}
}

// Mutation captured: omitting the recovery clear leaves the owner locked out.
func testRootTokenClearsOwnerFailures(t *testing.T, fresh func() AccountStore) {
	s := newScenario(t, fresh(), newClock(), testRootTokenValue)
	s.seedOwner()
	for _, email := range []string{testMemberEmail, testOwnerEmail} {
		for i := 0; i < perEmailMaxFailures; i++ {
			if status, _, _ := login(t, s, email, "wrong-password"); status != 401 {
				t.Fatalf("failed attempt %d = %d, want 401", i, status)
			}
		}
	}
	if status, _, _ := login(t, s, testOwnerEmail, testOwnerPass); status != 429 {
		t.Fatalf("blocked owner = %d, want 429", status)
	}
	const newPass = "recovered-owner-pass-2"
	resp := s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, newPass), jsonCT, "")
	if resp.Status != 200 {
		t.Fatalf("recovery = %d, want 200", resp.Status)
	}
	setCookieOf(t, resp)
	if status, _, _ := login(t, s, testOwnerEmail, newPass); status != 200 {
		t.Fatalf("recovered owner login = %d, want 200", status)
	}
	if status, _, _ := login(t, s, testOwnerEmail, testOwnerPass); status != 401 {
		t.Fatalf("old password = %d, want 401", status)
	}
	if status, _, _ := login(t, s, testMemberEmail, testMemberPass); status != 429 {
		t.Fatalf("other email budget = %d, want 429", status)
	}
}

type clearFailureStore struct {
	AccountStore
	fail bool
}

func (s *clearFailureStore) ClearFailures(ctx context.Context, email string) error {
	if s.fail {
		return errors.New("synthetic clear failure")
	}
	return s.AccountStore.ClearFailures(ctx, email)
}

// Mutation captured: returning 503 after a committed reset loses the usable cookie.
func testRootTokenClearFailureError(t *testing.T, fresh func() AccountStore) {
	st := &clearFailureStore{AccountStore: fresh()}
	s := newScenario(t, st, newClock(), testRootTokenValue)
	s.seedOwner()
	st.fail = true
	const newPass = "recovered-owner-pass-2"
	resp := s.doRec("POST", "/api/session/root-token", rootBody(testRootTokenValue, newPass), jsonCT, "")
	if resp.Status != 200 {
		t.Fatalf("recovery after clear failure = %d, want 200", resp.Status)
	}
	cookie := setCookieOf(t, resp)
	if got := s.do("GET", "/api/session", nil, "", "nau_session="+cookie); got.Status != 200 {
		t.Fatalf("recovery cookie = %d, want 200", got.Status)
	}
	const clearLog = "hub accounts: the login failures of the owner were not cleared"
	if s.log.String() != clearLog+"\n"+rootTokenLogReset+"\n" {
		t.Fatalf("unexpected recovery log: %q", s.log.String())
	}
	for _, secret := range []string{testRootTokenValue, testOwnerPass, newPass, testOwnerEmail} {
		if strings.Contains(s.log.String(), secret) {
			t.Fatal("recovery log contains a credential")
		}
	}
	st.fail = false
	if status, _, _ := login(t, s, testOwnerEmail, newPass); status != 200 {
		t.Fatalf("new password = %d, want 200", status)
	}
	if status, _, _ := login(t, s, testOwnerEmail, testOwnerPass); status != 401 {
		t.Fatalf("old password = %d, want 401", status)
	}
}
