//go:build sqld

package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/machines"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// The session key and the bootstrap token of the suite: fixed test
// values, never secrets.
const sqldSessionKey = "dGVzdC1zZXNzaW9uLWtleS02MjItMDAwMDAwMDAwMDAwMDAwMA=="
const sqldBootstrapToken = "test-bootstrap-625"

// sqldLiveTables are the tables the suite empties: the machine
// tables and the tables of the layers the real flow writes.
var sqldLiveTables = []string{
	"ingest_deliveries", "pairing_attempts", "pairings", "machines", "shares",
	"agent_history", "agents_current", "hidden_counts", "working_hourly",
	"inventory_snapshots", "login_failures", "sessions", "invites",
	"users", "accounts_guard", "hub_meta",
}

// sqldClock is the fake clock of the suite.
type sqldClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *sqldClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *sqldClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var sqldBaseTime = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// sqldScenario is the real layers over one real sqld.
type sqldScenario struct {
	t       *testing.T
	ctx     context.Context
	srv     *hub.Server
	svc     *Service
	clock   *sqldClock
	genMS   int64
	ownerID string
	userIDs map[string]string
	cookies map[string]string
}

// freshSQldScenario empties the tables and builds the real accounts,
// the real machine connect and the real live over the store.
func freshSQldScenario(t *testing.T, st store.Store) *sqldScenario {
	t.Helper()
	ctx := context.Background()
	stmts := make([]store.Stmt, 0, len(sqldLiveTables))
	for _, table := range sqldLiveTables {
		stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
	}
	if _, err := st.Batch(ctx, stmts); err != nil {
		t.Fatalf("fresh scenario: %v", err)
	}
	clock := &sqldClock{t: sqldBaseTime}
	sessionKey, err := base64.StdEncoding.DecodeString(sqldSessionKey)
	if err != nil {
		t.Fatalf("the session key of the suite: %v", err)
	}
	auth, err := accounts.New(accounts.Config{
		Store:          accounts.NewSQL(st),
		SessionKey:     sessionKey,
		BootstrapToken: sqldBootstrapToken,
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatalf("accounts.New: %v", err)
	}
	mach, err := machines.New(machines.Config{
		Store:       st,
		Machines:    machines.NewSQL(st),
		SessionUser: auth.SessionUser,
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("machines.New: %v", err)
	}
	svc, err := New(Config{
		Store:       st,
		SessionUser: auth.SessionUser,
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("live.New: %v", err)
	}
	srv := hub.New(hub.Deps{Store: st, Now: clock.Now, Version: "dev"}, nil)
	auth.Register(srv)
	mach.Register(srv)
	s := &sqldScenario{
		t:       t,
		ctx:     ctx,
		srv:     srv,
		svc:     svc,
		clock:   clock,
		genMS:   sqldBaseTime.UnixMilli() + 1,
		userIDs: map[string]string{},
		cookies: map[string]string{},
	}
	ownerID, ownerCookie := s.bootstrap("owner", "owner@example.test")
	s.ownerID = ownerID
	s.userIDs["owner"] = ownerID
	s.cookies["owner"] = ownerCookie
	aliceID, aliceCookie := s.member("alice", "alice@example.test")
	s.userIDs["alice"] = aliceID
	s.cookies["alice"] = aliceCookie
	return s
}

// sqldHeader is the JSON header, with the session cookie when set.
func sqldHeader(cookie string) hub.Header {
	h := hub.Header{}
	h.Set("Content-Type", "application/json")
	if cookie != "" {
		h.Set("Cookie", "nau_session="+cookie)
	}
	return h
}

func (s *sqldScenario) do(method, path, cookie string, body any, authHeader string) hub.Response {
	s.t.Helper()
	data, _ := json.Marshal(body)
	req := hub.Request{
		Method: method,
		Path:   path,
		Header: sqldHeader(cookie),
		Body:   data,
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp := s.srv.Serve(s.ctx, req)
	if resp.Status >= 500 {
		s.t.Fatalf("%s %s: %d %s", method, path, resp.Status, resp.Body)
	}
	return resp
}

// bootstrap creates the owner through the real bootstrap.
func (s *sqldScenario) bootstrap(name, email string) (string, string) {
	s.t.Helper()
	resp := s.do("POST", "/api/bootstrap", "", map[string]string{
		"token":    sqldBootstrapToken,
		"name":     name,
		"email":    email,
		"password": "bootstrap-password-1",
	}, "")
	if resp.Status != http.StatusCreated {
		s.t.Fatalf("bootstrap: %d %s, want 201", resp.Status, resp.Body)
	}
	return s.userIDOf(resp), s.cookieOf(resp)
}

// member issues the owner's invite and accepts it.
func (s *sqldScenario) member(name, email string) (string, string) {
	s.t.Helper()
	resp := s.do("POST", "/api/invites", s.cookies["owner"], map[string]string{"email": email, "role": "member"}, "")
	if resp.Status != http.StatusCreated {
		s.t.Fatalf("invite: %d %s, want 201", resp.Status, resp.Body)
	}
	var inv struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(resp.Body, &inv); err != nil {
		s.t.Fatalf("the invite body %s: %v", resp.Body, err)
	}
	resp = s.do("POST", "/api/invites/accept", "", map[string]string{
		"token":    inv.Token,
		"name":     name,
		"password": "member-password-12",
	}, "")
	if resp.Status != http.StatusCreated {
		s.t.Fatalf("accept: %d %s, want 201", resp.Status, resp.Body)
	}
	return s.userIDOf(resp), s.cookieOf(resp)
}

func (s *sqldScenario) userIDOf(resp hub.Response) string {
	s.t.Helper()
	var body struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.User.ID == "" {
		s.t.Fatalf("the user id of %s: %v", resp.Body, err)
	}
	return body.User.ID
}

func (s *sqldScenario) cookieOf(resp hub.Response) string {
	s.t.Helper()
	v := resp.Header.Get("Set-Cookie")
	k, _, ok := strings.Cut(v, ";")
	if !ok || !strings.HasPrefix(k, "nau_session=") {
		s.t.Fatal("the response has no session cookie")
	}
	return strings.TrimPrefix(k, "nau_session=")
}

// pairMachine runs the pairing lifecycle and returns the machine id
// and the key.
func (s *sqldScenario) pairMachine(name string) (string, string) {
	s.t.Helper()
	resp := s.do("POST", "/api/pairings", s.cookies["owner"], map[string]string{"name": name}, "")
	if resp.Status != http.StatusCreated {
		s.t.Fatalf("pairing: %d %s, want 201", resp.Status, resp.Body)
	}
	var c struct {
		Code      string `json:"code"`
		PollToken string `json:"poll_token"`
	}
	if err := json.Unmarshal(resp.Body, &c); err != nil {
		s.t.Fatalf("the pairing body %s: %v", resp.Body, err)
	}
	code := c.Code
	for i := 0; i < len(code); i++ {
		if code[i] >= 'A' && code[i] <= 'Z' {
			code = code[:i] + string(code[i]+('a'-'A')) + code[i+1:]
		}
	}
	resp = s.do("POST", "/api/pairings/confirm", s.cookies["owner"], map[string]string{"code": code}, "")
	if resp.Status != http.StatusNoContent {
		s.t.Fatalf("confirm: %d %s, want 204", resp.Status, resp.Body)
	}
	resp = s.do("POST", "/api/pairings/poll", "", map[string]string{"poll_token": c.PollToken}, "")
	if resp.Status != http.StatusOK {
		s.t.Fatalf("poll: %d %s, want 200", resp.Status, resp.Body)
	}
	var d struct {
		MachineID string `json:"machine_id"`
		Key       string `json:"key"`
	}
	if err := json.Unmarshal(resp.Body, &d); err != nil {
		s.t.Fatalf("the poll body %s: %v", resp.Body, err)
	}
	return d.MachineID, d.Key
}

// deliverInventories posts one inventory with the machine key.
func (s *sqldScenario) deliverInventories(key string, inv inventory.Inventory) {
	s.t.Helper()
	data, err := json.Marshal(inv)
	if err != nil {
		s.t.Fatalf("the inventory %v", err)
	}
	resp := s.srv.Serve(s.ctx, hub.Request{
		Method: "POST",
		Path:   "/api/ingest",
		Header: func() hub.Header {
			h := hub.Header{}
			h.Set("Content-Type", "application/json")
			h.Set("Authorization", "Bearer "+key)
			return h
		}(),
		Body: data,
	})
	if resp.Status != http.StatusNoContent {
		s.t.Fatalf("ingest: %d %s, want 204", resp.Status, resp.Body)
	}
}

// inventoryFixture is one inventory of the suite: the given agents and
// hidden counts, with a strictly newer generated_ms on every call
// (the Apply guard refuses the non-newer inventories).
func (s *sqldScenario) inventoryFixture(terminals map[string]inventory.State, hidden inventory.Hidden) inventory.Inventory {
	s.genMS++
	projects := make(map[string][]inventory.Agent)
	for terminal, state := range terminals {
		agent := inventory.Agent{
			Terminal:    terminal,
			Name:        terminal,
			Role:        "orchestrator",
			Kind:        "orchestrator",
			State:       state,
			StartedMS:   s.clock.Now().Add(-5 * time.Minute).UnixMilli(),
			ReportReady: false,
		}
		if task := "the task of " + terminal; state != inventory.StateNeedsYou {
			agent.Task = &task
		}
		if state == inventory.StateNeedsYou {
			reason := inventory.ReasonQuestion
			agent.Reason = &reason
		}
		projects["proj-"+terminal] = append(projects["proj-"+terminal], agent)
	}
	ps := make([]inventory.Project, 0, len(projects))
	for name, agents := range projects {
		ps = append(ps, inventory.Project{
			Name:     name,
			Sessions: []inventory.Session{{ID: "sess-" + name, Agents: agents}},
		})
	}
	return inventory.Inventory{
		Schema:      inventory.Schema,
		SohoVersion: "soho v0.0.0-suite",
		GeneratedMS: s.genMS,
		Projects:    ps,
		Hidden:      hidden,
	}
}

// version is the live version of one user, with the failure check.
func (s *sqldScenario) version(user string) string {
	s.t.Helper()
	v, err := s.svc.Version(s.ctx, s.userIDs[user])
	if err != nil {
		s.t.Fatalf("the version of %s: %v", user, err)
	}
	return v
}

// TestSQLDVersion runs the live board version over the real sqld:
// two accounts, two machines of the owner (one shared, one private),
// real pairings, real deliveries and real revocation. The helper
// takes the lock of the shared database and reinitializes the schema;
// the test is re-runnable on the same database.
func TestSQLDVersion(t *testing.T) {
	st := hubtest.SQLD(t)
	s := freshSQldScenario(t, st)

	m1, key1 := s.pairMachine("shared-machine")
	m2, key2 := s.pairMachine("private-machine")
	_ = m2
	// The shared machine goes to alice.
	resp := s.do("POST", "/api/machines/"+m1+"/shares", s.cookies["owner"], map[string]string{"email": "alice@example.test"}, "")
	if resp.Status != http.StatusCreated {
		t.Fatalf("share: %d %s, want 201", resp.Status, resp.Body)
	}

	// The first delivery: the owner's board holds the two machines,
	// alice's holds the shared one.
	s.deliverInventories(key1, s.inventoryFixture(map[string]inventory.State{"t1": inventory.StateWorking}, inventory.Hidden{Working: 1}))
	s.deliverInventories(key2, s.inventoryFixture(map[string]inventory.State{"t2": inventory.StateIdle}, inventory.Hidden{}))
	own1, alice1 := s.version("owner"), s.version("alice")
	if own1 == "" || alice1 == "" {
		t.Fatalf("the versions are empty: %q %q", own1, alice1)
	}
	if own1 == alice1 {
		t.Fatal("the owner and the guest share a version before any delivery of the private machine")
	}

	// A delivery-only change: the generated_ms moves, the content
	// holds. The versions of both stay.
	s.deliverInventories(key1, s.inventoryFixture(map[string]inventory.State{"t1": inventory.StateWorking}, inventory.Hidden{Working: 1}))
	if got, want := s.version("owner"), own1; got != want {
		t.Fatalf("the owner's version moved on a delivery-only change: %s -> %s", want, got)
	}
	if got, want := s.version("alice"), alice1; got != want {
		t.Fatalf("the guest's version moved on a delivery-only change: %s -> %s", want, got)
	}

	// The state of the shared machine moves: both versions change.
	s.deliverInventories(key1, s.inventoryFixture(map[string]inventory.State{"t1": inventory.StateNeedsYou}, inventory.Hidden{Working: 1}))
	own2, alice2 := s.version("owner"), s.version("alice")
	if own1 == own2 || alice1 == alice2 {
		t.Fatalf("the versions did not move on the state change: %s/%s -> %s/%s", own1, alice1, own2, alice2)
	}

	// Only the private machine moves: the owner's version changes,
	// the guest's stays.
	s.deliverInventories(key2, s.inventoryFixture(map[string]inventory.State{"t2": inventory.StateDone}, inventory.Hidden{}))
	own3, alice3 := s.version("owner"), s.version("alice")
	if own2 == own3 {
		t.Fatalf("the owner's version did not move on the private machine: %s", own3)
	}
	if alice2 != alice3 {
		t.Fatalf("the guest's version moved on a machine that is not shared: %s -> %s", alice2, alice3)
	}

	// The revocation of the shared machine: both versions move, and
	// the guest's board loses the machine.
	resp = s.do("DELETE", "/api/machines/"+m1, s.cookies["owner"], nil, "")
	if resp.Status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s, want 204", resp.Status, resp.Body)
	}
	own4, alice4 := s.version("owner"), s.version("alice")
	if own3 == own4 {
		t.Fatal("the owner's version did not move on the revocation")
	}
	if alice3 == alice4 {
		t.Fatal("the guest's version did not move when the shared machine was revoked")
	}
}
