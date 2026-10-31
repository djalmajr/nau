//go:build sqld

package machines

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/sqld"
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

// hubZeroTables is the order of every table the hub migrations create:
// the children first, so the foreign keys never block the delete or
// the drop of a parent.
var hubZeroTables = []string{
	"ingest_deliveries", "pairing_attempts", "pairings", "machines", "shares",
	"agent_history", "agents_current", "hidden_counts", "working_hourly",
	"inventory_snapshots", "login_failures", "sessions", "invites",
	"users", "pairing_guard", "ingest_guard", "accounts_guard",
	"schema_migrations",
	"hub_meta",
}

// resetToZero brings the shared database to the pristine state: it
// deletes the rows and drops the tables of every migration (the
// children first, so the foreign keys never block) and leaves no
// schema. The suite re-applies the migrations from zero, so the test
// is re-runnable no matter what the previous packages left behind.
func resetToZero(t *testing.T, client *sqld.Client, ctx context.Context) {
	t.Helper()
	res, err := client.Execute(ctx, store.Stmt{SQL: `SELECT name FROM sqlite_master WHERE type = 'table'`})
	if err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	exists := make(map[string]bool, len(res.Rows))
	for _, row := range res.Rows {
		if name, ok := row[0].(string); ok {
			exists[name] = true
		}
	}
	var stmts []store.Stmt
	for _, table := range hubZeroTables {
		if exists[table] {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
	}
	for _, table := range hubZeroTables {
		if exists[table] {
			stmts = append(stmts, store.Stmt{SQL: "DROP TABLE " + table})
		}
	}
	if len(stmts) == 0 {
		return
	}
	if _, err := client.Batch(ctx, stmts); err != nil {
		t.Fatalf("zero reset: %v", err)
	}
}

// TestSQLDMachinesScenarios runs the whole scenario suite over the SQL
// implementation against a real sqld, named by NAU_TEST_SQLD_URL, with
// the real accounts (the sessions are the signed cookies) and the real
// ingest.Apply. The migrations are applied first; each scenario
// empties the tables so it starts from the empty state. The test is
// re-runnable on the same database. The tagged tests of the hub
// packages share the instance's default database and one of them
// reinitializes the schema, so run the tagged suite with -p 1 (the
// packages sequential).
func TestSQLDMachinesScenarios(t *testing.T) {
	url := os.Getenv("NAU_TEST_SQLD_URL")
	if url == "" {
		t.Skip("set NAU_TEST_SQLD_URL to run the machines scenarios against a real sqld")
	}
	client, err := sqld.New(url, "")
	if err != nil {
		t.Fatalf("sqld.New: %v", err)
	}
	ctx := context.Background()
	resetToZero(t, client, ctx)
	if err := (&migrate.Runner{Store: client, Now: time.Now}).Apply(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The rows of the account and machine tables go away at the end
	// (the tables stay: the migrations are one-way), so the dropAll
	// of the sqld integration test sees the empty state.
	t.Cleanup(func() {
		stmts := make([]store.Stmt, 0, 8)
		for _, table := range []string{
			"ingest_deliveries", "pairing_attempts", "pairings", "machines", "shares",
			"login_failures", "sessions", "invites", "users",
		} {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
		if _, err := client.Batch(ctx, stmts); err != nil {
			t.Logf("the cleanup of the rows: %v", err)
		}
	})
	// The machine tables of the migration 0003.
	res, err := client.Execute(ctx, store.Stmt{SQL: `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('machines', 'pairings', 'pairing_attempts', 'ingest_deliveries', 'pairing_guard') ORDER BY name`})
	if err != nil {
		t.Fatalf("list the machine tables: %v", err)
	}
	if len(res.Rows) != 5 {
		t.Fatalf("the machine tables = %d, want 5", len(res.Rows))
	}
	runMachinesScenarios(t, func(t *testing.T) *scenario { return freshSQLScenario(t, client) })
}

// freshSQLScenario empties the tables, builds the real layers over the
// fake clock and creates the users (the bootstrap owner and the two
// members, through the real invites).
func freshSQLScenario(t *testing.T, client *sqld.Client) *scenario {
	t.Helper()
	ctx := context.Background()
	stmts := make([]store.Stmt, 0, len(sqldMachineTables))
	for _, table := range sqldMachineTables {
		stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
	}
	if _, err := client.Batch(ctx, stmts); err != nil {
		t.Fatalf("fresh scenario: %v", err)
	}
	clock := newFakeClock()
	sessionKey, err := base64.StdEncoding.DecodeString(testSessionKey)
	if err != nil {
		t.Fatalf("the session key of the suite: %v", err)
	}
	auth, err := accounts.New(accounts.Config{
		Store:          accounts.NewSQL(client),
		SessionKey:     sessionKey,
		BootstrapToken: testBootstrapToken,
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	svc, err := New(Config{
		Store:          client,
		Machines:       NewSQL(client),
		ClientIPHeader: "X-Test-Client-IP",
		SessionUser:    auth.SessionUser,
		Now:            clock.Now,
		Rand:           &counterReader{},
	})
	if err != nil {
		t.Fatalf("machines.New: %v", err)
	}
	srv := hub.New(hub.Deps{Store: client, Now: clock.Now, Version: "dev"}, nil)
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
		store:   NewSQL(client),
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
	url := os.Getenv("NAU_TEST_SQLD_URL")
	if url == "" {
		t.Skip("set NAU_TEST_SQLD_URL to run the developers counters against a real sqld")
	}
	client, err := sqld.New(url, "")
	if err != nil {
		t.Fatalf("sqld.New: %v", err)
	}
	ctx := context.Background()
	resetToZero(t, client, ctx)
	if err := (&migrate.Runner{Store: client, Now: time.Now}).Apply(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		stmts := make([]store.Stmt, 0, 9)
		for _, table := range []string{
			"ingest_deliveries", "pairing_attempts", "pairings", "machines", "shares",
			"login_failures", "sessions", "invites", "users",
		} {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
		if _, err := client.Batch(ctx, stmts); err != nil {
			t.Logf("the cleanup of the rows: %v", err)
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
