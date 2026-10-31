//go:build sqld

package machines

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/store"
)

// testSessionKey is the session key of the suite: the base64 of 34
// fixed bytes, inlined so the test has no import cycle risk. It is a
// test value, never a secret.
const testSessionKey = "dGVzdC1zZXNzaW9uLWtleS02MjItMDAwMDAwMDAwMDAwMDAwMA=="

// testBootstrapToken is the one-time bootstrap token of the suite.
const testBootstrapToken = "test-bootstrap-622"

// sqldMachineTables are the tables the suite empties between the
// scenarios: the machine tables and the tables of the layers the real
// flow writes (the accounts and the ingest).
var sqldMachineTables = []string{
	"ingest_deliveries", "pairing_attempts", "pairings", "machines", "shares",
	"agent_history", "agents_current", "hidden_counts", "working_hourly",
	"inventory_snapshots", "login_failures", "sessions", "invites",
	"users", "accounts_guard", "hub_meta",
}

// TestSQLDMachinesScenarios runs the whole scenario suite over the SQL
// implementation against a real sqld, named by NAU_TEST_SQLD_URL, with
// the real accounts (the sessions are the signed cookies) and the real
// ingest.Apply. The helper takes the lock of the shared database,
// reinitializes the schema and holds the lock for the whole test, so
// the scenarios run on a fresh schema and the tagged suites run
// without -p 1. Each scenario empties the tables so it starts from
// the empty state. The test is re-runnable on the same database.
func TestSQLDMachinesScenarios(t *testing.T) {
	st := hubtest.SQLD(t)
	ctx := context.Background()
	// The machine tables of the migration 0003.
	res, err := st.Execute(ctx, store.Stmt{SQL: `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('machines', 'pairings', 'pairing_attempts', 'ingest_deliveries', 'pairing_guard') ORDER BY name`})
	if err != nil {
		t.Fatalf("list the machine tables: %v", err)
	}
	if len(res.Rows) != 5 {
		t.Fatalf("the machine tables = %d, want 5", len(res.Rows))
	}
	runMachinesScenarios(t, func(t *testing.T) *scenario { return freshSQLScenario(t, st) })
}

// freshSQLScenario empties the tables, builds the real layers over the
// fake clock and creates the users (the bootstrap owner and the two
// members, through the real invites).
func freshSQLScenario(t *testing.T, st store.Store) *scenario {
	t.Helper()
	ctx := context.Background()
	stmts := make([]store.Stmt, 0, len(sqldMachineTables))
	for _, table := range sqldMachineTables {
		stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
	}
	if _, err := st.Batch(ctx, stmts); err != nil {
		t.Fatalf("fresh scenario: %v", err)
	}
	clock := newFakeClock()
	sessionKey, err := base64.StdEncoding.DecodeString(testSessionKey)
	if err != nil {
		t.Fatalf("the session key of the suite: %v", err)
	}
	auth, err := accounts.New(accounts.Config{
		Store:          accounts.NewSQL(st),
		SessionKey:     sessionKey,
		BootstrapToken: testBootstrapToken,
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	svc, err := New(Config{
		Store:          st,
		Machines:       NewSQL(st),
		ClientIPHeader: "X-Test-Client-IP",
		SessionUser:    auth.SessionUser,
		Now:            clock.Now,
		Rand:           &counterReader{},
	})
	if err != nil {
		t.Fatalf("machines.New: %v", err)
	}
	srv := hub.New(hub.Deps{Store: st, Now: clock.Now, Version: "dev"}, nil)
	auth.Register(srv)
	svc.Register(srv)

	// The bootstrap owner.
	ownerID, ownerCookie := bootstrapSQLUser(t, srv, "owner", "owner@example.test")
	users := map[string]accounts.User{"owner": {ID: ownerID, Name: "owner", Email: "owner@example.test", Role: accounts.RoleOwner}}
	cookies := map[string]string{"owner": ownerCookie}
	for _, who := range []struct {
		name  string
		email string
	}{
		{"alice", "alice@example.test"},
		{"bob", "bob@example.test"},
	} {
		id, cookie := memberSQLUser(t, srv, ownerCookie, who.name, who.email)
		users[who.name] = accounts.User{ID: id, Name: who.name, Email: who.email, Role: accounts.RoleMember}
		cookies[who.name] = cookie
	}
	return &scenario{
		t:       t,
		ctx:     ctx,
		srv:     srv,
		store:   NewSQL(st),
		clock:   clock,
		users:   users,
		cookies: cookies,
	}
}

// bootstrapSQLUser runs the bootstrap and returns the user id and the
// session cookie.
func bootstrapSQLUser(t *testing.T, srv *hub.Server, name, email string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"token":    testBootstrapToken,
		"name":     name,
		"email":    email,
		"password": "bootstrap-password-1",
	})
	resp := srv.Serve(context.Background(), hub.Request{
		Method: http.MethodPost,
		Path:   "/api/bootstrap",
		Header: jsonHeader(),
		Body:   body,
	})
	if resp.Status != 201 {
		t.Fatalf("bootstrap: %d %s, want 201", resp.Status, resp.Body)
	}
	return userOf(t, resp.Body), cookieOf(t, resp)
}

// memberSQLUser issues the invite of the owner and accepts it: the
// member with its session cookie.
func memberSQLUser(t *testing.T, srv *hub.Server, ownerCookie, name, email string) (string, string) {
	t.Helper()
	invBody, _ := json.Marshal(map[string]string{"email": email, "role": "member"})
	resp := srv.Serve(context.Background(), hub.Request{
		Method: http.MethodPost,
		Path:   "/api/invites",
		Header: headerWithCookie(jsonHeader(), ownerCookie),
		Body:   invBody,
	})
	if resp.Status != 201 {
		t.Fatalf("create invite: %d %s, want 201", resp.Status, resp.Body)
	}
	var inv struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Body, &inv); err != nil {
		t.Fatalf("invite body %s: %v", resp.Body, err)
	}
	accBody, _ := json.Marshal(map[string]string{
		"token":    inv.Token,
		"name":     name,
		"password": "member-password-12",
	})
	resp = srv.Serve(context.Background(), hub.Request{
		Method: http.MethodPost,
		Path:   "/api/invites/accept",
		Header: jsonHeader(),
		Body:   accBody,
	})
	if resp.Status != 201 {
		t.Fatalf("accept invite: %d %s, want 201", resp.Status, resp.Body)
	}
	return userOf(t, resp.Body), cookieOf(t, resp)
}

// jsonHeader is the header of the JSON bodies.
func jsonHeader() hub.Header {
	h := hub.Header{}
	h.Set("Content-Type", "application/json")
	return h
}

// headerWithCookie is the header with the session cookie.
func headerWithCookie(h hub.Header, cookie string) hub.Header {
	h.Set("Cookie", "nau_session="+cookie)
	return h
}

// userOf reads the user id of the session body.
func userOf(t *testing.T, body []byte) string {
	t.Helper()
	var u struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.User.ID == "" {
		t.Fatalf("the user id of %s: %v", body, err)
	}
	return u.User.ID
}

// cookieOf reads the session cookie of the Set-Cookie header.
func cookieOf(t *testing.T, resp hub.Response) string {
	t.Helper()
	v := resp.Header.Get("Set-Cookie")
	if k, _, ok := strings.Cut(v, ";"); ok && strings.HasPrefix(k, "nau_session=") {
		return strings.TrimPrefix(k, "nau_session=")
	}
	t.Fatal("the response has no session cookie")
	return ""
}

// TestSQLDDeveloperCounters is the counters of the developers route
// on the real sqld: several accounts with different counts, grouped
// by the right key of each account.
func TestSQLDDeveloperCounters(t *testing.T) {
	client := hubtest.SQLD(t)
	// The rows of the fixture go away at the end of the test, before
	// the helper releases the lock (the cleanups run last in, first
	// out), so the shared database never keeps them. The test context
	// is already done when the cleanups run: the delete has its own.
	t.Cleanup(func() {
		stmts := make([]store.Stmt, 0, len(sqldMachineTables))
		for _, table := range sqldMachineTables {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
		if _, err := client.Batch(context.Background(), stmts); err != nil {
			t.Errorf("the cleanup of the rows: %v", err)
		}
	})
	s := freshSQLScenario(t, client)
	// alice has two machines, one of them shared with bob; bob has
	// one machine, shared with alice and with the owner of the hub.
	da1, _ := s.deliver(t, "alice", "m1")
	s.deliver(t, "alice", "m2")
	db1, _ := s.deliver(t, "bob", "m3")
	for _, sh := range []struct{ machine, by, email string }{
		{da1.MachineID, "alice", "bob@example.test"},
		{db1.MachineID, "bob", "alice@example.test"},
		{db1.MachineID, "bob", "owner@example.test"},
	} {
		r := s.doJSON("POST", "/api/machines/"+sh.machine+"/shares", map[string]string{"email": sh.email}, s.asUser(sh.by))
		if r.Status != 201 {
			t.Fatalf("share %s with %s: %d %s, want 201", sh.machine, sh.email, r.Status, r.Body)
		}
	}
	// The owner of the hub sees every account; the counters of each
	// account hold the counts of that account: the machines, the
	// shares that reach the account and the shares the account gives.
	r := s.do("GET", "/api/developers", nil, s.asUser("owner"), false)
	if r.Status != 200 {
		t.Fatalf("developers of the owner: %d %s, want 200", r.Status, r.Body)
	}
	devs := parseDevelopers(t, r.Body)
	want := map[string][3]int{
		s.users["alice"].ID: {2, 1, 1}, // machines, incoming, outgoing
		s.users["bob"].ID:   {1, 1, 2},
		s.users["owner"].ID: {0, 1, 0},
	}
	if len(devs) != len(want) {
		t.Fatalf("developers of the owner = %+v, want the three accounts", devs)
	}
	for _, d := range devs {
		w, ok := want[d.ID]
		if !ok {
			t.Fatalf("the account %s of the owner listing, want alice, bob and the owner", d.ID)
		}
		if d.Machines != w[0] || d.SharesWithMe != w[1] || d.SharedByMe != w[2] {
			t.Fatalf("counts of %s = [%d %d %d], want [%d %d %d]", d.ID, d.Machines, d.SharesWithMe, d.SharedByMe, w[0], w[1], w[2])
		}
	}
}
