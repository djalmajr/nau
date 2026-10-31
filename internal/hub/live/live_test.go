package live

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/hub/visibility"
	"github.com/djalmajr/nau/internal/inventory"
)

// The online window of the fake visibility: the same minute of the
// real one.
const fakeOnlineWindowMS = 60 * 1000

// fakeMachine is one machine of the fake visibility.
type fakeMachine struct {
	name     string
	dev      string
	revoked  bool
	lastSeen *int64
	shares   []string
}

// fakeSharing is the visibility of the live tests: the machines and
// the grants in memory, with the same visible rule of the decision.
type fakeSharing struct {
	mu   sync.Mutex
	byID map[string]fakeMachine
}

func newFakeSharing() *fakeSharing {
	return &fakeSharing{byID: map[string]fakeMachine{}}
}

func (f *fakeSharing) add(id, name, dev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id] = fakeMachine{name: name, dev: dev}
}

func (f *fakeSharing) setLastSeen(id string, ms int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mc := f.byID[id]
	v := ms
	mc.lastSeen = &v
	f.byID[id] = mc
}

func (f *fakeSharing) revoke(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mc := f.byID[id]
	mc.revoked = true
	f.byID[id] = mc
}

func (f *fakeSharing) share(machineID, grantee string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mc := f.byID[machineID]
	mc.shares = append(mc.shares, grantee)
	f.byID[machineID] = mc
}

func (f *fakeSharing) unshare(machineID, grantee string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mc := f.byID[machineID]
	out := make([]string, 0, len(mc.shares))
	for _, g := range mc.shares {
		if g != grantee {
			out = append(out, g)
		}
	}
	mc.shares = out
	f.byID[machineID] = mc
}

func (f *fakeSharing) VisibleMachines(ctx context.Context, developerID string, nowMS int64) ([]visibility.Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.byID))
	for id, mc := range f.byID {
		if mc.revoked {
			continue
		}
		own := mc.dev == developerID
		shared := false
		for _, g := range mc.shares {
			if g == developerID {
				shared = true
				break
			}
		}
		if own || shared {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]visibility.Machine, 0, len(ids))
	for _, id := range ids {
		mc := f.byID[id]
		m := visibility.Machine{
			ID:        id,
			Name:      mc.name,
			Developer: visibility.Developer{ID: mc.dev},
		}
		if mc.dev == developerID {
			m.Own = true
			grants := append([]string(nil), mc.shares...)
			sort.Strings(grants)
			m.SharedWith = make([]visibility.Developer, 0, len(grants))
			for _, g := range grants {
				m.SharedWith = append(m.SharedWith, visibility.Developer{ID: g})
			}
		}
		if ms := mc.lastSeen; ms != nil {
			m.LastSeenMS = ms
			m.Online = nowMS-*ms <= fakeOnlineWindowMS
		}
		out = append(out, m)
	}
	return out, nil
}

// The unused answers of the interface, fixed for the live tests.
func (f *fakeSharing) UserByEmail(ctx context.Context, email string) (visibility.Account, bool, error) {
	return visibility.Account{}, false, nil
}

func (f *fakeSharing) Share(ctx context.Context, machineID, granteeID string, nowMS int64) error {
	f.share(machineID, granteeID)
	return nil
}

func (f *fakeSharing) Unshare(ctx context.Context, machineID, granteeID string) (bool, error) {
	return false, nil
}

func (f *fakeSharing) Accounts(ctx context.Context, viewerID string, owner bool) ([]visibility.Account, error) {
	return []visibility.Account{}, nil
}

// agentBoard is the agents read of the live tests: the stored answer
// plus a failure to force.
type agentBoard struct {
	mu     sync.Mutex
	result ingest.AgentsResult
	err    error
	calls  int
}

func newAgentBoard() *agentBoard {
	return &agentBoard{result: ingest.AgentsResult{Agents: []ingest.AgentRow{}, Hidden: []ingest.Hidden{}}}
}

func (a *agentBoard) set(result ingest.AgentsResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.result = result
}

func (a *agentBoard) fail(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func (a *agentBoard) read(ctx context.Context, st store.Store, machineIDs []string, nowMS int64) (ingest.AgentsResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return ingest.AgentsResult{}, a.err
	}
	// The real read scopes the rows to the requested machines: the
	// fake keeps the same contract.
	allowed := make(map[string]bool, len(machineIDs))
	for _, id := range machineIDs {
		allowed[id] = true
	}
	res := ingest.AgentsResult{Agents: []ingest.AgentRow{}, Hidden: []ingest.Hidden{}}
	for _, r := range a.result.Agents {
		if allowed[r.MachineID] {
			res.Agents = append(res.Agents, r)
		}
	}
	for _, h := range a.result.Hidden {
		if allowed[h.MachineID] {
			res.Hidden = append(res.Hidden, h)
		}
	}
	return res, nil
}

// nopStore is the store of the live tests: the fakes never read it.
type nopStore struct{}

func (nopStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	return store.Result{}, nil
}

func (nopStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	return nil, nil
}

func (nopStore) Ping(ctx context.Context) error { return nil }

// fakeClock is the clock of the live tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// baseInventoryTime is the start of the live tests.
var baseInventoryTime = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// newLiveTest builds the service and the fakes of the live tests.
func newLiveTest() (*Service, *fakeSharing, *agentBoard, *fakeClock) {
	clk := &fakeClock{t: baseInventoryTime}
	sh := newFakeSharing()
	ab := newAgentBoard()
	svc, err := New(Config{
		Store:   nopStore{},
		Sharing: sh,
		Agents:  ab.read,
		SessionUser: func(ctx context.Context, req hub.Request) (accounts.User, bool) {
			return accounts.User{ID: "dev-a"}, true
		},
		Now: clk.Now,
	})
	if err != nil {
		panic(err)
	}
	return svc, sh, ab, clk
}

// agentRow is one agent of the test fixtures.
func agentRow(machine, terminal, project, task string, state inventory.State) ingest.AgentRow {
	t := task
	return ingest.AgentRow{
		MachineID:   machine,
		Terminal:    terminal,
		Project:     project,
		Session:     "sess-1",
		Name:        terminal,
		Role:        "orchestrator",
		Kind:        "orchestrator",
		State:       state,
		Task:        &t,
		ReportReady: false,
		StartedMS:   baseInventoryTime.UnixMilli() - 10*60*1000,
	}
}

func TestVersionShape(t *testing.T) {
	svc, sh, _, _ := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	v, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !re.MatchString(v) {
		t.Fatalf("version %q is not the 32 lowercase hex characters", v)
	}
}

func TestVersionStableAcrossDeliveryOnlyTimes(t *testing.T) {
	svc, sh, ab, clk := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	sh.setLastSeen("m1", baseInventoryTime.Add(-10*time.Second).UnixMilli())
	ab.set(ingest.AgentsResult{
		Agents: []ingest.AgentRow{{
			MachineID:  "m1",
			Terminal:   "t1",
			Project:    "p1",
			Session:    "s1",
			Name:       "orchestrator",
			Role:       "orchestrator",
			Kind:       "orchestrator",
			State:      inventory.StateWorking,
			StartedMS:  baseInventoryTime.Add(-5 * time.Minute).UnixMilli(),
			ObservedMS: baseInventoryTime.UnixMilli(),
			Online:     true,
		}},
		Hidden: []ingest.Hidden{{MachineID: "m1", Counts: inventory.Hidden{Working: 1}}},
	})
	v1, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// A new delivery: the observed_ms of the agent moves and the
	// last_seen of the machine moves, inside the online window.
	clk.Add(7 * time.Second)
	sh.setLastSeen("m1", clk.Now().Add(-5*time.Second).UnixMilli())
	ab.set(ingest.AgentsResult{
		Agents: []ingest.AgentRow{{
			MachineID:  "m1",
			Terminal:   "t1",
			Project:    "p1",
			Session:    "s1",
			Name:       "orchestrator",
			Role:       "orchestrator",
			Kind:       "orchestrator",
			State:      inventory.StateWorking,
			StartedMS:  baseInventoryTime.Add(-5 * time.Minute).UnixMilli(),
			ObservedMS: clk.Now().UnixMilli(),
			Online:     true,
		}},
		Hidden: []ingest.Hidden{{MachineID: "m1", Counts: inventory.Hidden{Working: 1}}},
	})
	v2, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v1 != v2 {
		t.Fatalf("the version moved on a delivery-only change: %s -> %s", v1, v2)
	}
}

func TestVersionChangesOnAgentStateAndTask(t *testing.T) {
	svc, sh, ab, _ := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	ab.set(ingest.AgentsResult{Agents: []ingest.AgentRow{agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking)}, Hidden: []ingest.Hidden{}})
	v1, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	ab.set(ingest.AgentsResult{Agents: []ingest.AgentRow{agentRow("m1", "t1", "p1", "task-2", inventory.StateNeedsYou)}, Hidden: []ingest.Hidden{}})
	v2, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v1 == v2 {
		t.Fatal("the version did not move when the agent changed of state and task")
	}
}

func TestVersionChangesOnOnlineFlip(t *testing.T) {
	svc, sh, _, clk := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	sh.setLastSeen("m1", baseInventoryTime.Add(-10*time.Second).UnixMilli())
	on, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// Past the online window: the machine goes offline.
	clk.Add(61 * time.Second)
	off, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if on == off {
		t.Fatal("the version did not move when the machine went offline")
	}
}

func TestVersionChangesOnShareAndRevocation(t *testing.T) {
	svc, sh, ab, _ := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	ab.set(ingest.AgentsResult{Agents: []ingest.AgentRow{agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking)}, Hidden: []ingest.Hidden{}})
	own1, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest1, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// The share: the owner's shared_with gains the grantee and the
	// machine enters the guest's board.
	sh.share("m1", "dev-b")
	own2, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest2, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if own1 == own2 {
		t.Fatal("the owner's version did not move when the share was given")
	}
	if guest1 == guest2 {
		t.Fatal("the guest's version did not move when the share was given")
	}
	// The unshare: both move back.
	sh.unshare("m1", "dev-b")
	own3, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest3, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if own2 == own3 || guest2 == guest3 {
		t.Fatal("the versions did not move when the share was taken")
	}
	// A third user, with nothing visible, is not affected by any of
	// the changes above or by the revocation below.
	stranger1, err := svc.Version(context.Background(), "dev-c")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	sh.revoke("m1")
	own4, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	stranger2, err := svc.Version(context.Background(), "dev-c")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if own3 == own4 {
		t.Fatal("the owner's version did not move when the machine was revoked")
	}
	if stranger1 != stranger2 {
		t.Fatal("the stranger's version moved on a revocation that does not touch it")
	}
}

func TestVersionIsolationByVisibility(t *testing.T) {
	svc, sh, ab, _ := newLiveTest()
	// The owner has a shared machine and a private one.
	sh.add("m1", "shared", "dev-a")
	sh.add("m2", "private", "dev-a")
	sh.share("m1", "dev-b")
	ab.set(ingest.AgentsResult{
		Agents: []ingest.AgentRow{
			agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking),
			agentRow("m2", "t2", "p2", "task-2", inventory.StateIdle),
		},
		Hidden: []ingest.Hidden{},
	})
	own1, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest1, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// Only the private machine changes.
	ab.set(ingest.AgentsResult{
		Agents: []ingest.AgentRow{
			agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking),
			agentRow("m2", "t2", "p2", "task-3", inventory.StateNeedsYou),
		},
		Hidden: []ingest.Hidden{},
	})
	own2, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest2, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if own1 == own2 {
		t.Fatal("the owner's version did not move when the private machine changed")
	}
	if guest1 != guest2 {
		t.Fatal("the guest's version moved on a machine that is not shared")
	}
	// The shared machine changes: both move.
	ab.set(ingest.AgentsResult{
		Agents: []ingest.AgentRow{
			agentRow("m1", "t1", "p1", "task-4", inventory.StateDone),
			agentRow("m2", "t2", "p2", "task-3", inventory.StateNeedsYou),
		},
		Hidden: []ingest.Hidden{},
	})
	own3, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	guest3, err := svc.Version(context.Background(), "dev-b")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if own2 == own3 || guest2 == guest3 {
		t.Fatal("the versions did not move when the shared machine changed")
	}
}

func TestVersionStableWithNoVisibleMachines(t *testing.T) {
	svc, _, ab, _ := newLiveTest()
	ab.set(ingest.AgentsResult{Agents: []ingest.AgentRow{agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking)}, Hidden: []ingest.Hidden{}})
	v1, err := svc.Version(context.Background(), "dev-none")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	v2, err := svc.Version(context.Background(), "dev-none")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v1 != v2 {
		t.Fatalf("the empty board version moved: %s -> %s", v1, v2)
	}
}

func TestVersionReportsTheReadFailures(t *testing.T) {
	clk := &fakeClock{t: baseInventoryTime}
	var visErr error
	sharing := visibilityFuncSharing(func(ctx context.Context, developerID string, nowMS int64) ([]visibility.Machine, error) {
		if visErr != nil {
			return nil, visErr
		}
		// One visible machine so the version reaches the agents read.
		return []visibility.Machine{{ID: "m1", Name: "one", Developer: visibility.Developer{ID: "dev-a"}}}, nil
	})
	sharingErr := errors.New("live test: the visibility failed")
	visErr = sharingErr
	ab := newAgentBoard()
	svc, err := New(Config{
		Store:   nopStore{},
		Sharing: sharing,
		Agents:  ab.read,
		SessionUser: func(ctx context.Context, req hub.Request) (accounts.User, bool) {
			return accounts.User{ID: "dev-a"}, true
		},
		Now: clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := svc.Version(context.Background(), "dev-a"); !errors.Is(err, sharingErr) {
		t.Fatalf("the visibility failure did not reach the caller: %v", err)
	}
	visErr = nil
	ab.fail(sharingErr)
	if _, err := svc.Version(context.Background(), "dev-a"); !errors.Is(err, sharingErr) {
		t.Fatalf("the agents failure did not reach the caller: %v", err)
	}
	ab.fail(nil)
	v, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version after the failures: %v", err)
	}
	if v == "" {
		t.Fatal("the version is empty")
	}
}

// visibilityFuncSharing is the Sharing driven by one function.
type visibilityFuncSharing func(ctx context.Context, developerID string, nowMS int64) ([]visibility.Machine, error)

func (f visibilityFuncSharing) VisibleMachines(ctx context.Context, developerID string, nowMS int64) ([]visibility.Machine, error) {
	return f(ctx, developerID, nowMS)
}

func (f visibilityFuncSharing) UserByEmail(ctx context.Context, email string) (visibility.Account, bool, error) {
	return visibility.Account{}, false, nil
}

func (f visibilityFuncSharing) Share(ctx context.Context, machineID, granteeID string, nowMS int64) error {
	return nil
}

func (f visibilityFuncSharing) Unshare(ctx context.Context, machineID, granteeID string) (bool, error) {
	return false, nil
}

func (f visibilityFuncSharing) Accounts(ctx context.Context, viewerID string, owner bool) ([]visibility.Account, error) {
	return []visibility.Account{}, nil
}

func TestLiveRoute(t *testing.T) {
	svc, sh, ab, _ := newLiveTest()
	sh.add("m1", "laptop", "dev-a")
	ab.set(ingest.AgentsResult{Agents: []ingest.AgentRow{agentRow("m1", "t1", "p1", "task-1", inventory.StateWorking)}, Hidden: []ingest.Hidden{}})
	v, err := svc.Version(context.Background(), "dev-a")
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	// No session: 401.
	s401, _ := New(Config{
		Store:       nopStore{},
		Sharing:     sh,
		Agents:      ab.read,
		SessionUser: func(ctx context.Context, req hub.Request) (accounts.User, bool) { return accounts.User{}, false },
		Now:         func() time.Time { return baseInventoryTime },
	})
	hs := hub.New(hub.Deps{}, nil)
	s401.Register(hs)
	resp := hs.Serve(context.Background(), hub.Request{Method: "GET", Path: "/api/live"})
	if resp.Status != 401 || string(resp.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("no session: %d %s, want 401 unauthenticated", resp.Status, resp.Body)
	}
	// Session: the version, 200.
	hs2 := hub.New(hub.Deps{}, nil)
	svc.Register(hs2)
	resp = hs2.Serve(context.Background(), hub.Request{Method: "GET", Path: "/api/live"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("the body %s is not JSON: %v", resp.Body, err)
	}
	if body.Version != v {
		t.Fatalf("the route version %s is not the computed one %s", body.Version, v)
	}
	// Store failure: 503 without the error.
	ab.fail(errors.New("live test: the agents read failed"))
	resp = hs2.Serve(context.Background(), hub.Request{Method: "GET", Path: "/api/live"})
	if resp.Status != 503 || string(resp.Body) != `{"error":"unavailable"}` {
		t.Fatalf("store failure: %d %s, want 503 unavailable", resp.Status, resp.Body)
	}
}

// TestLiveRouteRefusesMisconfiguration keeps the required fields of
// the construction.
func TestLiveRouteRefusesMisconfiguration(t *testing.T) {
	if _, err := New(Config{}); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("a missing store must fail: %v", err)
	}
	if _, err := New(Config{Store: nopStore{}}); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("a missing session resolver must fail: %v", err)
	}
}
