package machines

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/hub/visibility"
	"github.com/djalmajr/nau/internal/inventory"
)

// fakeShareKey is the key of a grant of the memory suite.
type fakeShareKey struct {
	machineID string
	granteeID string
}

// fakeSharing is the sharing state of the memory suite, mirroring the
// rule of the visibility: the visible machines come from the machine
// store and the grants, the accounts come from the users of the
// suite, and the last-seen comes from the calls of the fake apply.
type fakeSharing struct {
	mu     sync.Mutex
	mem    *Memory
	users  map[string]accounts.User
	byID   map[string]accounts.User
	apply  *fakeApply
	shares map[fakeShareKey]struct{}
	// err is the store-failure knob of the memory suite.
	err error
}

func newFakeSharing(mem *Memory, users map[string]accounts.User, apply *fakeApply) *fakeSharing {
	byID := make(map[string]accounts.User, len(users))
	for _, u := range users {
		byID[u.ID] = u
	}
	return &fakeSharing{mem: mem, users: users, byID: byID, apply: apply, shares: map[fakeShareKey]struct{}{}}
}

// callsLocked copies the calls of the fake apply under its lock.
func (f *fakeApply) callsLocked() []applyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]applyCall(nil), f.calls...)
}

// lastSeen is the received_ms of the last delivery of the machine, or
// zero when the machine never delivered.
func (f *fakeSharing) lastSeen(machineID string) int64 {
	last := int64(0)
	for _, c := range f.apply.callsLocked() {
		if c.machineID == machineID && c.receivedMS > last {
			last = c.receivedMS
		}
	}
	return last
}

// VisibleMachines is the in-memory rule: the non-revoked machines of
// the developer and the non-revoked ones shared with them, ordered by
// id.
func (f *fakeSharing) VisibleMachines(ctx context.Context, developerID string, nowMS int64) ([]visibility.Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	all, err := f.mem.ListAllMachines(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]visibility.Machine, len(all))
	for _, mc := range all {
		if mc.RevokedMS != nil {
			continue
		}
		_, shared := f.shares[fakeShareKey{mc.ID, developerID}]
		if mc.DeveloperID != developerID && !shared {
			continue
		}
		u := f.byID[mc.DeveloperID]
		m := visibility.Machine{
			ID:         mc.ID,
			Name:       mc.Name,
			Developer:  visibility.Developer{ID: mc.DeveloperID, Name: u.Name},
			CreatedMS:  mc.CreatedMS,
			Own:        mc.DeveloperID == developerID,
			SharedWith: nil,
			LastSeenMS: nil,
		}
		if last := f.lastSeen(mc.ID); last > 0 {
			seen := last
			m.LastSeenMS = &seen
			m.Online = nowMS-last <= 60000
		}
		byID[mc.ID] = m
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]visibility.Machine, 0, len(ids))
	for _, id := range ids {
		m := byID[id]
		if m.Own {
			m.SharedWith = []visibility.Developer{}
			for k := range f.shares {
				if k.machineID != id {
					continue
				}
				g := f.byID[k.granteeID]
				m.SharedWith = append(m.SharedWith, visibility.Developer{ID: k.granteeID, Name: g.Name})
			}
			sort.Slice(m.SharedWith, func(i, j int) bool {
				return m.SharedWith[i].ID < m.SharedWith[j].ID
			})
		}
		out = append(out, m)
	}
	return out, nil
}

// ownRevoked is the in-memory query of the machine listing route: the
// revoked machines of the developer, in the id order, with the
// last-seen of the deliveries. They hold no read: the shared_with
// stays empty, as the revocation ends the grants.
func (f *fakeSharing) ownRevoked(ctx context.Context, _ store.Store, developerID string, nowMS int64) ([]visibility.Machine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	all, err := f.mem.ListAllMachines(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]visibility.Machine, 0)
	for _, mc := range all {
		if mc.DeveloperID != developerID || mc.RevokedMS == nil {
			continue
		}
		u := f.byID[mc.DeveloperID]
		m := visibility.Machine{
			ID:         mc.ID,
			Name:       mc.Name,
			Developer:  visibility.Developer{ID: mc.DeveloperID, Name: u.Name},
			CreatedMS:  mc.CreatedMS,
			Own:        true,
			Revoked:    true,
			SharedWith: []visibility.Developer{},
		}
		if last := f.lastSeen(mc.ID); last > 0 {
			seen := last
			m.LastSeenMS = &seen
			m.Online = nowMS-last <= 60000
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// UserByEmail is the in-memory grantee lookup.
func (f *fakeSharing) UserByEmail(_ context.Context, email string) (visibility.Account, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return visibility.Account{}, false, f.err
	}
	norm := visibility.EmailNorm(email)
	for _, u := range f.users {
		if u.EmailNorm == norm {
			return visibility.Account{ID: u.ID, Name: u.Name, Email: u.Email}, true, nil
		}
	}
	return visibility.Account{}, false, nil
}

// Share is the in-memory grant, idempotent.
func (f *fakeSharing) Share(_ context.Context, machineID, granteeID string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.shares[fakeShareKey{machineID, granteeID}] = struct{}{}
	return nil
}

// Unshare is the in-memory removal of the grant.
func (f *fakeSharing) Unshare(_ context.Context, machineID, granteeID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	k := fakeShareKey{machineID, granteeID}
	_, existed := f.shares[k]
	delete(f.shares, k)
	return existed, nil
}

// Accounts is the in-memory developers route: for the member, the
// viewer, the developers that share with the viewer and the developers
// the viewer shares with; for the owner, every user. The shares of the
// revoked machines create no relationship and hold no count.
func (f *fakeSharing) Accounts(_ context.Context, viewerID string, owner bool) ([]visibility.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	all, err := f.mem.ListAllMachines(context.Background())
	if err != nil {
		return nil, err
	}
	// The non-revoked machine of each id, the developer of the id:
	// the shares of a revoked machine create no relationship.
	devOf := map[string]string{}
	machines := map[string]int{}
	for _, mc := range all {
		if mc.RevokedMS != nil {
			continue
		}
		devOf[mc.ID] = mc.DeveloperID
		machines[mc.DeveloperID]++
	}
	included := map[string]bool{viewerID: true}
	for k := range f.shares {
		dev, ok := devOf[k.machineID]
		if !ok {
			continue
		}
		if k.granteeID == viewerID {
			// The developers that share a machine with the viewer.
			included[dev] = true
		}
		if dev == viewerID {
			// The developers the viewer shares with.
			included[k.granteeID] = true
		}
	}
	ids := make([]string, 0, len(f.users))
	for name, u := range f.users {
		if owner || included[u.ID] {
			ids = append(ids, name)
		}
	}
	sort.Strings(ids)
	out := make([]visibility.Account, 0, len(ids))
	for _, name := range ids {
		u := f.users[name]
		id := u.ID
		a := visibility.Account{ID: u.ID, Name: u.Name, Email: u.Email, Machines: machines[id]}
		for k := range f.shares {
			dev, ok := devOf[k.machineID]
			if !ok {
				continue
			}
			if k.granteeID == id {
				a.SharesWithMe++
			}
			if dev == id {
				a.SharedByMe++
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// fakeReads is the scripted read of the memory suite, built from the
// inventories the fake apply captured: the agents and the hidden
// counts of the last inventory of the machine, the transitions of the
// agent and the aggregates of the overview.
type fakeReads struct {
	apply *fakeApply
}

func newFakeReads(apply *fakeApply) *fakeReads {
	return &fakeReads{apply: apply}
}

// lastCalls returns the last call of each of the machines.
func (f *fakeReads) lastCalls(machineIDs []string) map[string]applyCall {
	last := map[string]applyCall{}
	for _, c := range f.apply.callsLocked() {
		for _, id := range machineIDs {
			if c.machineID == id {
				last[id] = c
			}
		}
	}
	return last
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func agentRowOf(mid string, a inventory.Agent, online bool, observedMS int64) ingest.AgentRow {
	return ingest.AgentRow{
		MachineID:   mid,
		Terminal:    a.Terminal,
		Project:     "",
		Session:     "",
		Name:        a.Name,
		Role:        a.Role,
		Kind:        a.Kind,
		Model:       a.Model,
		Effort:      a.Effort,
		State:       a.State,
		Reason:      a.Reason,
		Task:        a.Task,
		ReportReady: a.ReportReady,
		StartedMS:   a.StartedMS,
		SinceMS:     a.SinceMS,
		ObservedMS:  observedMS,
		Online:      online,
	}
}

// agents is the in-memory GET /api/agents read: the agents of the last
// inventory of the machines and the hidden counts of the machines that
// have them.
func (f *fakeReads) agents(_ context.Context, _ store.Store, machineIDs []string, nowMS int64) (ingest.AgentsResult, error) {
	out := ingest.AgentsResult{Agents: []ingest.AgentRow{}, Hidden: []ingest.Hidden{}}
	if len(machineIDs) == 0 {
		return out, nil
	}
	last := f.lastCalls(machineIDs)
	for _, mid := range sortedStrings(machineIDs) {
		c, ok := last[mid]
		if !ok {
			continue
		}
		online := nowMS-c.receivedMS <= 60000
		for _, p := range c.inv.Projects {
			for _, sess := range p.Sessions {
				for _, a := range sess.Agents {
					row := agentRowOf(mid, a, online, c.receivedMS)
					row.Project = p.Name
					row.Session = sess.ID
					out.Agents = append(out.Agents, row)
				}
			}
		}
		h := c.inv.Hidden
		if h.Working+h.NeedsYou+h.Done+h.Idle+h.Unknown > 0 {
			out.Hidden = append(out.Hidden, ingest.Hidden{MachineID: mid, Counts: h})
		}
	}
	return out, nil
}

// agent is the in-memory GET /api/agents/{id} read: the current agent
// of the machine and terminal, with the transitions of the state and
// the reason seen by the deliveries, newest first.
func (f *fakeReads) agent(_ context.Context, _ store.Store, machineID, terminal string, nowMS int64) (ingest.AgentDetail, error) {
	var (
		current *inventory.Agent
		lastMS  int64
		history []ingest.HistoryRow
		seen    bool
		prev    inventory.Agent
	)
	for _, c := range f.apply.callsLocked() {
		if c.machineID != machineID {
			continue
		}
		for _, p := range c.inv.Projects {
			for _, sess := range p.Sessions {
				for j := range sess.Agents {
					a := sess.Agents[j]
					if a.Terminal != terminal {
						continue
					}
					cp := a
					current = &cp
					lastMS = c.receivedMS
					if !seen || a.State != prev.State || reasonDiffers(prev.Reason, a.Reason) {
						history = append(history, ingest.HistoryRow{State: a.State, Reason: a.Reason, AtMS: c.receivedMS})
						seen = true
						prev = a
					}
				}
			}
		}
	}
	if current == nil {
		return ingest.AgentDetail{}, ingest.ErrNotFound
	}
	if len(history) > 200 {
		history = history[len(history)-200:]
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	row := agentRowOf(machineID, *current, nowMS-lastMS <= 60000, lastMS)
	return ingest.AgentDetail{Agent: row, History: history}, nil
}

func reasonDiffers(a, b *inventory.Reason) bool {
	if a == nil || b == nil {
		return a != b
	}
	return *a != *b
}

func fakeHourOf(ms int64) int64 { return ms - ms%3600000 }

// overview is the in-memory GET /api/overview read, mirroring the
// aggregates of the ingest over the last inventories of the machines:
// the counts with the hidden ones, the online split, the needs_you,
// the per-machine, the 24 hours (the most agents working seen per
// machine and hour, summed over the machines) and the top projects.
func (f *fakeReads) overview(_ context.Context, _ store.Store, machineIDs []string, nowMS int64) (ingest.OverviewResult, error) {
	out := ingest.OverviewResult{
		NeedsYou:    []ingest.AgentRow{},
		PerMachine:  []ingest.MachineCounts{},
		Working24h:  []ingest.HourPoint{},
		TopProjects: []ingest.TopProject{},
	}
	if len(machineIDs) == 0 {
		return out, nil
	}
	last := f.lastCalls(machineIDs)
	perMachine := map[string]*ingest.Counts{}
	projects := map[string]*ingest.TopProject{}
	byMachineHour := map[string]int{}
	needs := []ingest.AgentRow{}
	for _, mid := range sortedStrings(machineIDs) {
		c, ok := last[mid]
		if !ok {
			continue
		}
		online := nowMS-c.receivedMS <= 60000
		if online {
			out.Machines.Online++
		} else {
			out.Machines.Offline++
		}
		working := 0
		cnt := perMachine[mid]
		if cnt == nil {
			cnt = &ingest.Counts{}
			perMachine[mid] = cnt
		}
		for _, p := range c.inv.Projects {
			for _, sess := range p.Sessions {
				for _, a := range sess.Agents {
					addStateTo(cnt, a.State)
					pr := projects[p.Name]
					if pr == nil {
						pr = &ingest.TopProject{Project: p.Name}
						projects[p.Name] = pr
					}
					pr.Agents++
					if a.State == inventory.StateWorking {
						pr.Working++
						working++
					}
					if a.State == inventory.StateNeedsYou {
						row := agentRowOf(mid, a, online, c.receivedMS)
						row.Project = p.Name
						row.Session = sess.ID
						needs = append(needs, row)
					}
				}
			}
		}
		key := mid + "|" + strconv.FormatInt(fakeHourOf(c.receivedMS), 10)
		if working > byMachineHour[key] {
			byMachineHour[key] = working
		}
		h := c.inv.Hidden
		if h.Working+h.NeedsYou+h.Done+h.Idle+h.Unknown > 0 {
			addHiddenTo(perMachine[mid], h)
		}
	}
	// The needs_you, the oldest since_ms first, at most ten.
	sort.Slice(needs, func(i, j int) bool {
		return needsBeforeRows(needs[i], needs[j])
	})
	if len(needs) > 10 {
		needs = needs[:10]
	}
	out.NeedsYou = needs
	for _, p := range projects {
		out.TopProjects = append(out.TopProjects, *p)
	}
	sort.Slice(out.TopProjects, func(i, j int) bool {
		a, b := out.TopProjects[i], out.TopProjects[j]
		if a.Working != b.Working {
			return a.Working > b.Working
		}
		if a.Agents != b.Agents {
			return a.Agents > b.Agents
		}
		return a.Project < b.Project
	})
	if len(out.TopProjects) > 5 {
		out.TopProjects = out.TopProjects[:5]
	}
	// The per-machine, every machine included, and the totals with
	// the hidden ones.
	for _, mid := range sortedStrings(machineIDs) {
		c := perMachine[mid]
		if c == nil {
			c = &ingest.Counts{}
		}
		out.PerMachine = append(out.PerMachine, ingest.MachineCounts{MachineID: mid, Counts: *c})
		out.Counts.Working += c.Working
		out.Counts.NeedsYou += c.NeedsYou
		out.Counts.Done += c.Done
		out.Counts.Idle += c.Idle
		out.Counts.Unknown += c.Unknown
	}
	// The 24 hours, oldest first, ending at the last complete hour,
	// the most agents working seen per machine and hour summed over
	// the machines.
	curHour := fakeHourOf(nowMS)
	sumByHour := map[int64]int{}
	for key, working := range byMachineHour {
		parts := strings.SplitN(key, "|", 2)
		n, _ := strconv.ParseInt(parts[1], 10, 64)
		sumByHour[n] += working
	}
	for i := 23; i >= 0; i-- {
		h := curHour - int64(i)*3600000
		out.Working24h = append(out.Working24h, ingest.HourPoint{HourMS: h, Working: int64(sumByHour[h])})
	}
	return out, nil
}

func addStateTo(c *ingest.Counts, s inventory.State) {
	switch s {
	case inventory.StateWorking:
		c.Working++
	case inventory.StateNeedsYou:
		c.NeedsYou++
	case inventory.StateDone:
		c.Done++
	case inventory.StateIdle:
		c.Idle++
	default:
		c.Unknown++
	}
}

func addHiddenTo(c *ingest.Counts, h inventory.Hidden) {
	c.Working += int64(h.Working)
	c.NeedsYou += int64(h.NeedsYou)
	c.Done += int64(h.Done)
	c.Idle += int64(h.Idle)
	c.Unknown += int64(h.Unknown)
}

func needsBeforeRows(a, b ingest.AgentRow) bool {
	as, bs := a.SinceMS, b.SinceMS
	if (as == nil) != (bs == nil) {
		return bs == nil // the nil since is the longest waiting
	}
	if as != nil && *as != *bs {
		return *as < *bs
	}
	if a.MachineID != b.MachineID {
		return a.MachineID < b.MachineID
	}
	return a.Terminal < b.Terminal
}

// sharingInventoryBody is the inventory of the sharing scenarios: one
// show project with two agents (one working, one needs_you) and the
// counts of the hidden projects.
func sharingInventoryBody(generatedMS int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"schema":       1,
		"soho_version": "v0.3.1",
		"generated_ms": generatedMS,
		"projects": []any{
			map[string]any{
				"name": "acme/web",
				"sessions": []any{
					map[string]any{
						"id": "w1",
						"agents": []any{
							map[string]any{
								"terminal":     "w1:p1",
								"name":         "orchestrator",
								"role":         "orchestrator",
								"kind":         "cli-a",
								"model":        nil,
								"effort":       nil,
								"state":        "working",
								"reason":       nil,
								"task":         nil,
								"report_ready": false,
								"started_ms":   generatedMS - 3600000,
								"since_ms":     generatedMS - 120000,
							},
							map[string]any{
								"terminal":     "w1:p2",
								"name":         "build",
								"role":         "implementer",
								"kind":         "cli-b",
								"model":        "example/model-1",
								"effort":       "high",
								"state":        "needs_you",
								"reason":       "question",
								"task":         "Review the change",
								"report_ready": false,
								"started_ms":   generatedMS - 3000000,
								"since_ms":     generatedMS - 90000,
							},
						},
					},
				},
			},
		},
		"hidden": map[string]int{"working": 1, "needs_you": 0, "done": 1, "idle": 0, "unknown": 0},
	})
	return body
}

// ---- the shared scenario surface of the sharing ----------------------

type agentsBody struct {
	GeneratedMS int64 `json:"generated_ms"`
	Agents      []struct {
		ID       string `json:"id"`
		Terminal string `json:"terminal"`
		State    string `json:"state"`
		Machine  struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Online bool   `json:"online"`
		} `json:"machine"`
		Developer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"developer"`
	} `json:"agents"`
	Hidden []struct {
		Machine struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Online bool   `json:"online"`
		} `json:"machine"`
		Counts struct {
			Working  int64 `json:"working"`
			NeedsYou int64 `json:"needs_you"`
			Done     int64 `json:"done"`
			Idle     int64 `json:"idle"`
			Unknown  int64 `json:"unknown"`
		} `json:"counts"`
	} `json:"hidden"`
}

func parseAgents(t *testing.T, body []byte) agentsBody {
	t.Helper()
	var b agentsBody
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("agents body %s: %v", body, err)
	}
	return b
}

// machineListBody is the answer of the machine listing of the
// scenarios.
type machineListBody struct {
	Machines []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Developer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"developer"`
		CreatedMS  int64  `json:"created_ms"`
		Revoked    bool   `json:"revoked"`
		LastSeenMS *int64 `json:"last_seen_ms"`
		Online     bool   `json:"online"`
		SharedWith []struct {
			ID string `json:"id"`
		} `json:"shared_with"`
	} `json:"machines"`
}

func parseMachineList(t *testing.T, body []byte) machineListBody {
	t.Helper()
	var b machineListBody
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("machines body %s: %v", body, err)
	}
	return b
}

// testSharingReads is the sharing and the read routes, per the
// contract: without a share, the other developer and the owner of the
// hub see nothing of the machine; with the share, the recipient sees
// the show agents and the hidden counts only; the revocation of the
// share or of the machine ends the read.
func testSharingReads(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	agentID := d.MachineID + ".w1:p1"
	aliceID, bobID := s.users["alice"].ID, s.users["bob"].ID

	// The delivery of the inventory goes through.
	if r := s.ingestWith(d.Key, sharingInventoryBody(s.nowMS())); r.Status != 204 {
		t.Fatalf("ingest: %d %s, want 204", r.Status, r.Body)
	}

	// Without a share, the other developer sees nothing of the
	// machine, in every read route.
	r := s.do("GET", "/api/agents", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("agents of bob: %d %s, want 200", r.Status, r.Body)
	}
	b := parseAgents(t, r.Body)
	if len(b.Agents) != 0 || len(b.Hidden) != 0 {
		t.Fatalf("agents of bob = %+v, want none before the share", b)
	}
	r = s.do("GET", "/api/overview", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("overview of bob: %d %s, want 200", r.Status, r.Body)
	}
	var ov overviewBody
	if err := json.Unmarshal(r.Body, &ov); err != nil {
		t.Fatalf("overview body %s: %v", r.Body, err)
	}
	if ov.Machines.Online != 0 || ov.Machines.Offline != 0 || ov.Developers != 0 ||
		len(ov.NeedsYou) != 0 || len(ov.PerMachine) != 0 || len(ov.Working24h) != 24 ||
		ov.Counts.Working+ov.Counts.NeedsYou+ov.Counts.Done+ov.Counts.Idle+ov.Counts.Unknown != 0 {
		t.Fatalf("overview of bob = %+v, want the empty answer with 24 hours", ov)
	}
	r = s.do("GET", "/api/machines", nil, s.asUser("bob"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"machines":[]`) {
		t.Fatalf("machines of bob: %d %s, want the empty list", r.Status, r.Body)
	}
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob: %d %s, want 200", r.Status, r.Body)
	}
	devs := parseDevelopers(t, r.Body)
	if len(devs) != 1 || devs[0].ID != bobID {
		t.Fatalf("developers of bob = %+v, want only bob before the share", devs)
	}

	// The 404 of the detail is the same for an agent that does not
	// exist and an agent of an invisible machine.
	r = s.do("GET", "/api/agents/"+agentID, nil, s.asUser("bob"), false)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("detail of the invisible machine: %d %s, want 404 not_found", r.Status, r.Body)
	}
	r = s.do("GET", "/api/agents/unknown-machine.unknown", nil, s.asUser("bob"), false)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("detail of the unknown agent: %d %s, want the same 404 not_found", r.Status, r.Body)
	}

	// The owner of the hub, without a share, sees no agent of the
	// machine and no machine either.
	r = s.do("GET", "/api/agents", nil, s.asUser("owner"), false)
	if r.Status != 200 {
		t.Fatalf("agents of the owner: %d %s, want 200", r.Status, r.Body)
	}
	if b := parseAgents(t, r.Body); len(b.Agents) != 0 || len(b.Hidden) != 0 {
		t.Fatalf("agents of the owner = %+v, want none without a share", b)
	}
	r = s.do("GET", "/api/machines", nil, s.asUser("owner"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"machines":[]`) {
		t.Fatalf("machines of the owner: %d %s, want the empty list", r.Status, r.Body)
	}

	// The sharing, by the developer of the machine.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("share: %d %s, want 201", r.Status, r.Body)
	}
	var shareResp struct {
		Developer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"developer"`
	}
	if err := json.Unmarshal(r.Body, &shareResp); err != nil {
		t.Fatalf("share body %s: %v", r.Body, err)
	}
	if shareResp.Developer.ID != bobID || shareResp.Developer.Name != s.users["bob"].Name {
		t.Fatalf("share body %s, want the developer of bob", r.Body)
	}

	// With the share, bob sees the show agents and the hidden counts,
	// and nothing else.
	r = s.do("GET", "/api/agents", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("agents of bob with the share: %d %s, want 200", r.Status, r.Body)
	}
	b = parseAgents(t, r.Body)
	if len(b.Agents) != 2 || len(b.Hidden) != 1 {
		t.Fatalf("agents of bob with the share = %+v, want the two show agents and the hidden counts", b)
	}
	if b.Agents[0].ID != d.MachineID+".w1:p1" || b.Agents[0].State != "working" ||
		b.Agents[0].Machine.ID != d.MachineID || !b.Agents[0].Machine.Online ||
		b.Agents[0].Developer.ID != aliceID || b.Agents[0].Developer.Name != s.users["alice"].Name {
		t.Fatalf("agent of bob = %+v, want the show agent with the machine and the developer", b.Agents[0])
	}
	if b.Hidden[0].Machine.ID != d.MachineID ||
		b.Hidden[0].Counts.Working != 1 || b.Hidden[0].Counts.Done != 1 || b.Hidden[0].Counts.NeedsYou != 0 {
		t.Fatalf("hidden of bob = %+v, want the counts of the hidden projects", b.Hidden[0])
	}

	// The machine of bob: without the shared_with, the developer is
	// alice.
	r = s.do("GET", "/api/machines", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("machines of bob with the share: %d %s, want 200", r.Status, r.Body)
	}
	if !containsBody(t, r.Body, d.MachineID) || containsBody(t, r.Body, `"shared_with"`) {
		t.Fatalf("machines of bob %s, want the machine without shared_with", r.Body)
	}
	if !containsBody(t, r.Body, `"online":true`) || !containsBody(t, r.Body, `"last_seen_ms":`) {
		t.Fatalf("machines of bob %s, want the machine online with the last-seen", r.Body)
	}
	// The machine of alice: with the shared_with filled with bob.
	r = s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"shared_with":[{"id":"`+bobID) {
		t.Fatalf("machines of alice %s, want the shared_with with bob", r.Body)
	}

	// The developers of bob hold alice, the developer that shares with
	// him.
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob with the share: %d %s, want 200", r.Status, r.Body)
	}
	devs = parseDevelopers(t, r.Body)
	if len(devs) != 2 {
		t.Fatalf("developers of bob = %+v, want alice and bob", devs)
	}
	var bobDev, aliceDev *developerRow
	for i := range devs {
		switch devs[i].ID {
		case bobID:
			bobDev = &devs[i]
		case aliceID:
			aliceDev = &devs[i]
		}
	}
	if bobDev == nil || aliceDev == nil || bobDev.SharesWithMe != 1 || aliceDev.SharesWithMe != 0 ||
		aliceDev.SharedByMe != 1 || aliceDev.Machines != 1 {
		t.Fatalf("developers of bob = %+v, want the counts of the share", devs)
	}

	// The detail of bob: the agent with the history.
	r = s.do("GET", "/api/agents/"+agentID, nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("detail of bob: %d %s, want 200", r.Status, r.Body)
	}
	var detail detailBody
	if err := json.Unmarshal(r.Body, &detail); err != nil {
		t.Fatalf("detail body %s: %v", r.Body, err)
	}
	if detail.Agent.ID != agentID || detail.Agent.State != "working" || len(detail.History) != 1 ||
		detail.History[0].State != "working" {
		t.Fatalf("detail of bob = %+v, want the agent with the history", detail)
	}

	// The overview of bob: the counts with the hidden ones, the
	// per-machine with the developer, the machine online.
	r = s.do("GET", "/api/overview", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("overview of bob with the share: %d %s, want 200", r.Status, r.Body)
	}
	ov = overviewBody{}
	if err := json.Unmarshal(r.Body, &ov); err != nil {
		t.Fatalf("overview body %s: %v", r.Body, err)
	}
	if ov.Machines.Online != 1 || ov.Machines.Offline != 0 || ov.Developers != 1 ||
		ov.Counts.Working != 2 || ov.Counts.NeedsYou != 1 || ov.Counts.Done != 1 ||
		len(ov.NeedsYou) != 1 || len(ov.PerMachine) != 1 ||
		ov.PerMachine[0].Developer.ID != aliceID || ov.PerMachine[0].Counts.Done != 1 {
		t.Fatalf("overview of bob = %+v, want the aggregates with the hidden counts", ov)
	}
	if ov.NeedsYou[0].ID != d.MachineID+".w1:p2" {
		t.Fatalf("needs_you of bob = %+v, want the agent that needs you", ov.NeedsYou)
	}

	// The filters of the agents route, applied after the visibility.
	for _, c := range []struct {
		query string
		want  int
	}{
		{"?project=acme/web", 2},
		{"?project=other", 0},
		{"?machine=" + d.MachineID, 2},
		{"?machine=other", 0},
		{"?developer=" + aliceID, 2},
		{"?developer=other", 0},
	} {
		r = s.do("GET", "/api/agents"+c.query, nil, s.asUser("bob"), false)
		if r.Status != 200 {
			t.Fatalf("agents with %s: %d %s, want 200", c.query, r.Status, r.Body)
		}
		if b := parseAgents(t, r.Body); len(b.Agents) != c.want {
			t.Fatalf("agents with %s = %d, want %d", c.query, len(b.Agents), c.want)
		}
	}

	// The one that is not the developer of the machine does not share
	// it; the 404 is the same for an e-mail with no account.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "owner@example.test"}, s.asUser("bob"))
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("share of the foreign machine: %d %s, want 404 not_found", r.Status, r.Body)
	}
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "ghost@example.test"}, s.asUser("alice"))
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("share of the unknown e-mail: %d %s, want the same 404 not_found", r.Status, r.Body)
	}
	// Sharing with oneself is refused.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "alice@example.test"}, s.asUser("alice"))
	if r.Status != 400 || string(r.Body) != `{"error":"bad_request"}` {
		t.Fatalf("share with oneself: %d %s, want 400 bad_request", r.Status, r.Body)
	}
	// The sharing is idempotent: the same 201 with the same body.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("second share: %d %s, want 201", r.Status, r.Body)
	}
	var again struct {
		Developer struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"developer"`
	}
	if err := json.Unmarshal(r.Body, &again); err != nil {
		t.Fatalf("share body %s: %v", r.Body, err)
	}
	if again.Developer.ID != bobID || again.Developer.Name != s.users["bob"].Name {
		t.Fatalf("second share body %s, want the same developer", r.Body)
	}

	// The machine key reads none of the new routes.
	for _, c := range []struct {
		method, path string
	}{
		{"GET", "/api/agents"},
		{"GET", "/api/agents/" + agentID},
		{"GET", "/api/overview"},
		{"GET", "/api/developers"},
		{"GET", "/api/machines"},
	} {
		r = s.do(c.method, c.path, nil, bearer(d.Key), false)
		if r.Status != 401 {
			t.Fatalf("%s %s with the key: %d %s, want 401", c.method, c.path, r.Status, r.Body)
		}
	}
	r = s.do("POST", "/api/machines/"+d.MachineID+"/shares", []byte(`{"email":"bob@example.test"}`), bearer(d.Key), true)
	if r.Status != 401 {
		t.Fatalf("share with the key: %d %s, want 401", r.Status, r.Body)
	}

	// bob does not end the share (the machine is not his).
	r = s.do("DELETE", "/api/machines/"+d.MachineID+"/shares/"+bobID, nil, s.asUser("bob"), true)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("unshare by bob: %d %s, want 404 not_found", r.Status, r.Body)
	}
	// The unshare of a grant that does not exist is 404.
	r = s.do("DELETE", "/api/machines/"+d.MachineID+"/shares/"+s.users["owner"].ID, nil, s.asUser("alice"), true)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("unshare of the absent grant: %d %s, want 404 not_found", r.Status, r.Body)
	}

	// The unshare, by the developer of the machine: 204, and bob sees
	// nothing again.
	r = s.do("DELETE", "/api/machines/"+d.MachineID+"/shares/"+bobID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("unshare: %d %s, want 204", r.Status, r.Body)
	}
	r = s.do("GET", "/api/agents", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("agents of bob after the unshare: %d %s, want 200", r.Status, r.Body)
	}
	if b := parseAgents(t, r.Body); len(b.Agents) != 0 || len(b.Hidden) != 0 {
		t.Fatalf("agents of bob after the unshare = %+v, want none", b)
	}

	// The revocation of the machine ends the share: the revoked
	// machine is not visible, so the read does not come back.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("share before the revocation: %d %s, want 201", r.Status, r.Body)
	}
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("revoke: %d %s, want 204", r.Status, r.Body)
	}
	r = s.do("GET", "/api/agents", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("agents of bob after the revocation: %d %s, want 200", r.Status, r.Body)
	}
	if b := parseAgents(t, r.Body); len(b.Agents) != 0 || len(b.Hidden) != 0 {
		t.Fatalf("agents of bob after the revocation = %+v, want none (the share is inert)", b)
	}
	r = s.do("GET", "/api/machines", nil, s.asUser("bob"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"machines":[]`) {
		t.Fatalf("machines of bob after the revocation: %d %s, want the empty list", r.Status, r.Body)
	}
}

// testRevokedShareEndsTheRelationship is the end of the relation:
// the sharing of a revoked machine creates no relationship — the
// grantee sees no account and no count of the former sharer, not even
// when the sharer connects another machine without sharing it. The
// owner of the hub keeps seeing every account, per the contract.
func testRevokedShareEndsTheRelationship(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	bobID := s.users["bob"].ID

	// The share of the machine with bob, and the relation before the
	// revocation: bob sees alice.
	r := s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("share: %d %s, want 201", r.Status, r.Body)
	}
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob before the revocation: %d %s, want 200", r.Status, r.Body)
	}
	if devs := parseDevelopers(t, r.Body); len(devs) != 2 {
		t.Fatalf("developers of bob before the revocation = %+v, want alice and bob", devs)
	}

	// The revocation of the machine ends the relation: the grantee
	// sees no account and no count of the sharer.
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("revoke: %d %s, want 204", r.Status, r.Body)
	}
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob after the revocation: %d %s, want 200", r.Status, r.Body)
	}
	devs := parseDevelopers(t, r.Body)
	if len(devs) != 1 || devs[0].ID != bobID {
		t.Fatalf("developers of bob after the revocation = %+v, want only bob", devs)
	}
	if devs[0].Machines != 0 || devs[0].SharesWithMe != 0 || devs[0].SharedByMe != 0 {
		t.Fatalf("counts of bob after the revocation = %+v, want none", devs[0])
	}

	// The sharer connects another machine without sharing it: the
	// grantee still sees no account and no count of the sharer.
	s.deliver(t, "alice", "laptop-2")
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob after the new machine: %d %s, want 200", r.Status, r.Body)
	}
	devs = parseDevelopers(t, r.Body)
	if len(devs) != 1 || devs[0].ID != bobID {
		t.Fatalf("developers of bob after the new machine = %+v, want only bob", devs)
	}
	if devs[0].Machines != 0 || devs[0].SharesWithMe != 0 || devs[0].SharedByMe != 0 {
		t.Fatalf("counts of bob after the new machine = %+v, want none (the private machine does not count)", devs[0])
	}

	// The owner of the hub keeps seeing every account, per the
	// contract, with the revoked machine not counted.
	r = s.do("GET", "/api/developers", nil, s.asUser("owner"), false)
	if r.Status != 200 {
		t.Fatalf("developers of the owner: %d %s, want 200", r.Status, r.Body)
	}
	devs = parseDevelopers(t, r.Body)
	if len(devs) != 3 {
		t.Fatalf("developers of the owner = %+v, want every account", devs)
	}
	wantMachines := map[string]int{s.users["alice"].ID: 1, s.users["bob"].ID: 0, s.users["owner"].ID: 0}
	for _, d := range devs {
		w, ok := wantMachines[d.ID]
		if !ok {
			t.Fatalf("the account %s of the owner listing, want alice, bob and the owner", d.ID)
		}
		if d.Machines != w {
			t.Fatalf("machines of %s in the owner listing = %d, want %d (the revoked machine does not count)", d.ID, d.Machines, w)
		}
	}
}

// testRevokedOwnMachineStaysInTheListing is the revocation in the
// machine listing, per the decision: the owner of the machine sees it
// in the listing, with the seal and the last-seen he has, beyond the
// visible ones; the grantee and the owner of the hub do not see it;
// and the agents of the machine are outside every read route.
func testRevokedOwnMachineStaysInTheListing(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	aliceID, bobID := s.users["alice"].ID, s.users["bob"].ID
	agentID := d.MachineID + ".w1:p1"

	// The share, so the machine grants the read to the grantee.
	r := s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("share: %d %s, want 201", r.Status, r.Body)
	}

	// The delivery with the agents, so the last-seen and the agents
	// exist before the revocation.
	if r := s.ingestWith(d.Key, sharingInventoryBody(s.nowMS())); r.Status != 204 {
		t.Fatalf("ingest: %d %s, want 204", r.Status, r.Body)
	}

	// The revocation.
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("revoke: %d %s, want 204", r.Status, r.Body)
	}

	// The owner of the machine sees it in the listing, with the seal
	// and the last-seen of the delivery: the list holds his own
	// revoked machines beyond the visible ones.
	r = s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 {
		t.Fatalf("machines of alice: %d %s, want 200", r.Status, r.Body)
	}
	list := parseMachineList(t, r.Body)
	if len(list.Machines) != 1 {
		t.Fatalf("machines of alice = %s, want only the revoked machine", r.Body)
	}
	mc := list.Machines[0]
	if mc.ID != d.MachineID || mc.Name != "laptop" || mc.Developer.ID != aliceID || mc.Developer.Name != s.users["alice"].Name {
		t.Fatalf("machine of alice = %s, want the revoked laptop of alice", r.Body)
	}
	if !mc.Revoked {
		t.Fatalf("machine of alice = %s, want the revoked seal", r.Body)
	}
	if mc.LastSeenMS == nil || *mc.LastSeenMS != s.nowMS() || !mc.Online {
		t.Fatalf("machine of alice = %s, want the last-seen of the delivery and the online state", r.Body)
	}
	if len(mc.SharedWith) != 0 {
		t.Fatalf("machine of alice = %s, want no shared_with (the revocation ends the grants)", r.Body)
	}

	// The grantee and the owner of the hub do not see the machine:
	// the revocation ends the grants and the machine grants nothing.
	r = s.do("GET", "/api/machines", nil, s.asUser("bob"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"machines":[]`) {
		t.Fatalf("machines of bob: %d %s, want the empty list", r.Status, r.Body)
	}
	r = s.do("GET", "/api/machines", nil, s.asUser("owner"), false)
	if r.Status != 200 || !containsBody(t, r.Body, `"machines":[]`) {
		t.Fatalf("machines of the owner: %d %s, want the empty list", r.Status, r.Body)
	}

	// The agents of the revoked machine are outside every read
	// route, for the grantee and for the owner of the machine.
	for _, who := range []string{"bob", "alice"} {
		r := s.do("GET", "/api/agents", nil, s.asUser(who), false)
		if r.Status != 200 {
			t.Fatalf("agents of %s: %d %s, want 200", who, r.Status, r.Body)
		}
		if b := parseAgents(t, r.Body); len(b.Agents) != 0 || len(b.Hidden) != 0 {
			t.Fatalf("agents of %s = %+v, want none of the revoked machine", who, b)
		}
		r = s.do("GET", "/api/agents/"+agentID, nil, s.asUser(who), false)
		if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
			t.Fatalf("detail of %s: %d %s, want the 404 not_found of the invisible machine", who, r.Status, r.Body)
		}
		r = s.do("GET", "/api/overview", nil, s.asUser(who), false)
		if r.Status != 200 {
			t.Fatalf("overview of %s: %d %s, want 200", who, r.Status, r.Body)
		}
		var ov overviewBody
		if err := json.Unmarshal(r.Body, &ov); err != nil {
			t.Fatalf("overview body %s: %v", r.Body, err)
		}
		if ov.Machines.Online != 0 || ov.Machines.Offline != 0 || len(ov.Working24h) != 24 ||
			ov.Counts.Working+ov.Counts.NeedsYou+ov.Counts.Done+ov.Counts.Idle+ov.Counts.Unknown != 0 {
			t.Fatalf("overview of %s = %+v, want the empty answer with 24 hours", who, ov)
		}
	}

	// The grantee sees no relation of the revoked machine.
	r = s.do("GET", "/api/developers", nil, s.asUser("bob"), false)
	if r.Status != 200 {
		t.Fatalf("developers of bob: %d %s, want 200", r.Status, r.Body)
	}
	if devs := parseDevelopers(t, r.Body); len(devs) != 1 || devs[0].ID != bobID {
		t.Fatalf("developers of bob = %+v, want only bob", devs)
	}

	// Sharing the revoked machine keeps refusing with the same 404.
	r = s.doJSON("POST", "/api/machines/"+d.MachineID+"/shares", map[string]string{"email": "bob@example.test"}, s.asUser("alice"))
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("share after the revocation: %d %s, want the 404 of the today's refusal", r.Status, r.Body)
	}
}

// overviewBody is the answer of the overview route of the scenarios.
type overviewBody struct {
	GeneratedMS int64 `json:"generated_ms"`
	Counts      struct {
		Working  int64 `json:"working"`
		NeedsYou int64 `json:"needs_you"`
		Done     int64 `json:"done"`
		Idle     int64 `json:"idle"`
		Unknown  int64 `json:"unknown"`
	} `json:"counts"`
	Machines struct {
		Online  int `json:"online"`
		Offline int `json:"offline"`
	} `json:"machines"`
	Developers int `json:"developers"`
	NeedsYou   []struct {
		ID string `json:"id"`
	} `json:"needs_you"`
	PerMachine []struct {
		Machine struct {
			ID string `json:"id"`
		} `json:"machine"`
		Developer struct {
			ID string `json:"id"`
		} `json:"developer"`
		Counts struct {
			Working  int64 `json:"working"`
			NeedsYou int64 `json:"needs_you"`
			Done     int64 `json:"done"`
			Idle     int64 `json:"idle"`
			Unknown  int64 `json:"unknown"`
		} `json:"counts"`
	} `json:"per_machine"`
	Working24h []struct {
		HourMS int64 `json:"hour_ms"`
	} `json:"working_24h"`
	TopProjects []struct {
		Project string `json:"project"`
		Agents  int64  `json:"agents"`
		Working int64  `json:"working"`
	} `json:"top_projects"`
}

type developerRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Machines     int    `json:"machines"`
	SharesWithMe int    `json:"shares_with_me"`
	SharedByMe   int    `json:"shared_by_me"`
}

func parseDevelopers(t *testing.T, body []byte) []developerRow {
	t.Helper()
	var b struct {
		Developers []developerRow `json:"developers"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("developers body %s: %v", body, err)
	}
	return b.Developers
}

type detailBody struct {
	Agent struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Terminal string `json:"terminal"`
	} `json:"agent"`
	History []struct {
		State  string  `json:"state"`
		Reason *string `json:"reason"`
		AtMS   int64   `json:"at_ms"`
	} `json:"history"`
}

// containsBody reports whether the JSON body contains the fragment.
func containsBody(t *testing.T, body []byte, want string) bool {
	t.Helper()
	return strings.Contains(string(body), want)
}
