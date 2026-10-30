package ingest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// t0 is the time base of the tests, on the hour.
const t0 int64 = 1793376000000

// fakeSnapshot is the row of inventory_snapshots.
type fakeSnapshot struct {
	generatedMS int64
	receivedMS  int64
	sohoVersion string
}

// fakeAgent is the row of agents_current.
type fakeAgent struct {
	terminal, project, session, name, role, kind string
	model, effort                                *string
	state                                        string
	reason, task                                 *string
	reportReady                                  bool
	startedMS                                    int64
	sinceMS                                      *int64
	observedMS                                   int64
}

// fakeHistory is the row of agent_history.
type fakeHistory struct {
	machineID, terminal, project, name, state string
	reason                                    *string
	atMS                                      int64
}

// fakeStore is an in-memory model of the 0004 schema. It matches the
// statements by their SQL text and interprets the Args: the machine
// filter of the queries is applied only when the statement carries the
// WHERE clause, so a query that drops the clause sees every machine.
type fakeStore struct {
	mu           sync.Mutex
	snapshots    map[string]fakeSnapshot
	agents       map[string]map[string]fakeAgent
	history      []fakeHistory
	hidden       map[string]inventory.Hidden
	hourly       map[string]map[int64]int64
	executeErr   error
	batchErr     error
	executeCalls int
	batchCalls   int
	// retainCutoffs records the cutoffs of the retain deletes, in
	// the statement order, so the tests fix the boundaries without
	// the constants of the package.
	retainCutoffs []int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		snapshots: make(map[string]fakeSnapshot),
		agents:    make(map[string]map[string]fakeAgent),
		hidden:    make(map[string]inventory.Hidden),
		hourly:    make(map[string]map[int64]int64),
	}
}

func (f *fakeStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executeCalls++
	if f.executeErr != nil {
		return store.Result{}, f.executeErr
	}
	switch {
	case stmt.SQL == sqlLastGenerated:
		mid, _ := stmt.Args[0].(string)
		s, ok := f.snapshots[mid]
		if !ok {
			return store.Result{}, nil
		}
		return store.Result{Cols: []string{"generated_ms"}, Rows: []store.Row{{s.generatedMS}}}, nil
	case stmt.SQL == sqlExistingAgents:
		mid, _ := stmt.Args[0].(string)
		var rows []store.Row
		for _, a := range f.agents[mid] {
			rows = append(rows, store.Row{a.terminal, a.state, a.reasonValue()})
		}
		return store.Result{Cols: []string{"terminal", "state", "reason"}, Rows: rows}, nil
	case strings.HasPrefix(stmt.SQL, sqlAgentOnePrefix()):
		mid, _ := stmt.Args[0].(string)
		terminal, _ := stmt.Args[1].(string)
		a, ok := f.agents[mid][terminal]
		if !ok {
			return store.Result{}, nil
		}
		return store.Result{Rows: []store.Row{a.row(mid)}}, nil
	case strings.HasPrefix(stmt.SQL, sqlHistoryPrefix()):
		return f.historyRows(stmt)
	case strings.HasPrefix(stmt.SQL, snapshotInPrefix):
		return f.snapshotRows(stmt)
	case strings.HasPrefix(stmt.SQL, agentsInPrefix):
		return f.agentRows(stmt)
	case strings.HasPrefix(stmt.SQL, hiddenInPrefix):
		return f.hiddenRows(stmt)
	case strings.HasPrefix(stmt.SQL, hourlyInPrefix):
		return f.hourlyRows(stmt)
	case f.applyDML(stmt):
		// The per-statement execution: the statement applies without
		// the all-or-nothing of the batch.
		return store.Result{Affected: 1}, nil
	default:
		return store.Result{}, errors.New("fakeStore: unexpected statement " + stmt.SQL)
	}
}

// sqlAgentOnePrefix is the stable prefix of the one-agent read.
func sqlAgentOnePrefix() string {
	return "SELECT " + agentColumns + " FROM agents_current WHERE machine_id = ? AND terminal = ?"
}

// The stable prefixes of the IN reads, without the machine filter: the
// fake answers the filtered statements and the ones that ignore the
// machine list, applying the filter in Go from the arguments.
const (
	snapshotInPrefix = "SELECT machine_id, received_ms FROM inventory_snapshots"
	agentsInPrefix   = "SELECT " + agentColumns + " FROM agents_current"
	hiddenInPrefix   = "SELECT machine_id, working, needs_you, done, idle, unknown FROM hidden_counts"
	hourlyInPrefix   = "SELECT hour_ms, max_working FROM working_hourly"
)

// sqlHistoryPrefix is the stable prefix of the history read.
func sqlHistoryPrefix() string {
	return "SELECT state, reason, at_ms FROM agent_history WHERE machine_id = ? AND terminal = ? AND at_ms >= ? ORDER BY at_ms DESC LIMIT ?"
}

func (f *fakeStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	var results []store.Result
	for _, s := range stmts {
		switch {
		case s.SQL == sqlRetainHistory:
			cutoff := s.Args[0].(int64)
			f.retainCutoffs = append(f.retainCutoffs, cutoff)
			kept := f.history[:0]
			for _, h := range f.history {
				if h.atMS >= cutoff {
					kept = append(kept, h)
				}
			}
			f.history = kept
		case s.SQL == sqlRetainHourly:
			cutoff := s.Args[0].(int64)
			f.retainCutoffs = append(f.retainCutoffs, cutoff)
			for mid, m := range f.hourly {
				for hour := range m {
					if hour < cutoff {
						delete(m, hour)
					}
				}
				if len(m) == 0 {
					delete(f.hourly, mid)
				}
			}
		case s.SQL == sqlUpdateSnapshot:
			// The guarded write: the row exists and the version is
			// still the one the call read; the guard fails otherwise
			// and the batch aborts with nothing applied.
			mid, _ := s.Args[3].(string)
			last, _ := s.Args[4].(int64)
			cur, ok := f.snapshots[mid]
			if !ok || cur.generatedMS != last {
				return nil, errors.New("fakeStore: the guard failed: the version moved")
			}
			f.snapshots[mid] = fakeSnapshot{s.Args[0].(int64), s.Args[1].(int64), s.Args[2].(string)}
		case s.SQL == sqlInsertSnapshot:
			mid, _ := s.Args[0].(string)
			if _, ok := f.snapshots[mid]; ok {
				return nil, errors.New("fakeStore: the guard failed: a snapshot appeared")
			}
			f.snapshots[mid] = fakeSnapshot{s.Args[1].(int64), s.Args[2].(int64), s.Args[3].(string)}
		case s.SQL == sqlGuardCheck || s.SQL == sqlGuardClear:
			// The guard check and clear are modeled by the guarded
			// writes above: reaching them means the check passed.
		case f.applyDML(s):
			// the batch statement applied per the store semantics
		default:
			return nil, errors.New("fakeStore: unexpected batch statement " + s.SQL)
		}
		results = append(results, store.Result{Affected: 1})
	}
	return results, nil
}

// applyDML applies one DML statement with the store semantics. The
// batch uses it inside the atomic batch; the per-statement execution
// (Execute) uses it without the all-or-nothing.
func (f *fakeStore) applyDML(s store.Stmt) bool {
	switch {
	case s.SQL == sqlDeleteAgents:
		mid, _ := s.Args[0].(string)
		delete(f.agents, mid)
		return true
	case s.SQL == sqlInsertAgent:
		f.applyInsertAgent(s)
		return true
	case s.SQL == sqlInsertHistory:
		reason, _ := s.Args[5].(string)
		var rp *string
		if reason != "" {
			rp = &reason
		}
		f.history = append(f.history, fakeHistory{
			machineID: s.Args[0].(string), terminal: s.Args[1].(string),
			project: s.Args[2].(string), name: s.Args[3].(string),
			state: s.Args[4].(string), reason: rp, atMS: s.Args[6].(int64),
		})
		return true
	case s.SQL == sqlUpsertHidden:
		f.hidden[s.Args[0].(string)] = inventory.Hidden{
			Working: int(s.Args[1].(int64)), NeedsYou: int(s.Args[2].(int64)),
			Done: int(s.Args[3].(int64)), Idle: int(s.Args[4].(int64)),
			Unknown: int(s.Args[5].(int64)),
		}
		return true
	case s.SQL == sqlUpsertHourly:
		mid, _ := s.Args[0].(string)
		hour, _ := s.Args[1].(int64)
		working, _ := s.Args[2].(int64)
		m := f.hourly[mid]
		if m == nil {
			m = make(map[int64]int64)
			f.hourly[mid] = m
		}
		if working > m[hour] {
			m[hour] = working
		}
		return true
	}
	return false
}

func (f *fakeStore) Ping(ctx context.Context) error { return nil }

// applyInsertAgent applies one INSERT INTO agents_current.
func (f *fakeStore) applyInsertAgent(s store.Stmt) {
	mid, _ := s.Args[0].(string)
	terminal, _ := s.Args[1].(string)
	model, _ := s.Args[7].(string)
	effort, _ := s.Args[8].(string)
	reason, _ := s.Args[10].(string)
	task, _ := s.Args[11].(string)
	var since int64
	hasSince := false
	if v, ok := s.Args[14].(int64); ok {
		since, hasSince = v, true
	}
	a := fakeAgent{
		terminal:    terminal,
		project:     s.Args[2].(string),
		session:     s.Args[3].(string),
		name:        s.Args[4].(string),
		role:        s.Args[5].(string),
		kind:        s.Args[6].(string),
		state:       s.Args[9].(string),
		reportReady: s.Args[12].(int64) == 1,
		startedMS:   s.Args[13].(int64),
		observedMS:  s.Args[15].(int64),
	}
	if model != "" {
		a.model = &model
	}
	if effort != "" {
		a.effort = &effort
	}
	if reason != "" {
		a.reason = &reason
	}
	if task != "" {
		a.task = &task
	}
	if hasSince {
		a.sinceMS = &since
	}
	m := f.agents[mid]
	if m == nil {
		m = make(map[string]fakeAgent)
		f.agents[mid] = m
	}
	m[terminal] = a
}

// terminal is the key of the agent.
func (a fakeAgent) reasonValue() store.Value {
	if a.reason == nil {
		return nil
	}
	return *a.reason
}

// row is the full row of the agent columns.
func (a fakeAgent) row(mid string) store.Row {
	var model, effort, reason, task store.Value
	if a.model != nil {
		model = *a.model
	}
	if a.effort != nil {
		effort = *a.effort
	}
	if a.reason != nil {
		reason = *a.reason
	}
	if a.task != nil {
		task = *a.task
	}
	var since store.Value
	if a.sinceMS != nil {
		since = *a.sinceMS
	}
	var ready int64
	if a.reportReady {
		ready = 1
	}
	return store.Row{mid, a.terminal, a.project, a.session, a.name, a.role, a.kind,
		model, effort, a.state, reason, task, ready, a.startedMS, since, a.observedMS}
}

// machineArgs is the machine id list of an IN statement.
func machineArgs(stmt store.Stmt) []string {
	n := strings.Count(stmt.SQL, "?")
	ids := make([]string, 0, n)
	for _, v := range stmt.Args[:n] {
		id, _ := v.(string)
		ids = append(ids, id)
	}
	return ids
}

// hasMachineFilter reports whether the statement filters by machine.
func hasMachineFilter(stmt store.Stmt) bool {
	return strings.Contains(stmt.SQL, "WHERE machine_id IN (") ||
		strings.Contains(stmt.SQL, "WHERE machine_id = ?")
}

// snapshotRows answers the snapshot IN read.
func (f *fakeStore) snapshotRows(stmt store.Stmt) (store.Result, error) {
	ids := map[string]bool{}
	if hasMachineFilter(stmt) {
		for _, id := range machineArgs(stmt) {
			ids[id] = true
		}
	}
	var rows []store.Row
	for mid, s := range f.snapshots {
		if len(ids) > 0 && !ids[mid] {
			continue
		}
		rows = append(rows, store.Row{mid, s.receivedMS})
	}
	sortRows(rows, 0)
	return store.Result{Cols: []string{"machine_id", "received_ms"}, Rows: rows}, nil
}

// agentRows answers the agents IN read.
func (f *fakeStore) agentRows(stmt store.Stmt) (store.Result, error) {
	ids := map[string]bool{}
	if hasMachineFilter(stmt) {
		for _, id := range machineArgs(stmt) {
			ids[id] = true
		}
	}
	var rows []store.Row
	for mid, m := range f.agents {
		if len(ids) > 0 && !ids[mid] {
			continue
		}
		for _, a := range m {
			rows = append(rows, a.row(mid))
		}
	}
	sortRows(rows, 0)
	return store.Result{Rows: rows}, nil
}

// hiddenRows answers the hidden IN read.
func (f *fakeStore) hiddenRows(stmt store.Stmt) (store.Result, error) {
	ids := map[string]bool{}
	if hasMachineFilter(stmt) {
		for _, id := range machineArgs(stmt) {
			ids[id] = true
		}
	}
	var rows []store.Row
	for mid, h := range f.hidden {
		if len(ids) > 0 && !ids[mid] {
			continue
		}
		rows = append(rows, store.Row{mid, int64(h.Working), int64(h.NeedsYou), int64(h.Done), int64(h.Idle), int64(h.Unknown)})
	}
	sortRows(rows, 0)
	return store.Result{Rows: rows}, nil
}

// hourlyRows answers the working hours IN read.
func (f *fakeStore) hourlyRows(stmt store.Stmt) (store.Result, error) {
	ids := map[string]bool{}
	if hasMachineFilter(stmt) {
		n := strings.Count(stmt.SQL, "?") - 1 // the last argument is the cutoff
		for _, id := range machineArgs(stmt)[:n] {
			ids[id] = true
		}
		cutoff := stmt.Args[n].(int64)
		var rows []store.Row
		for mid, m := range f.hourly {
			if len(ids) > 0 && !ids[mid] {
				continue
			}
			for hour, w := range m {
				if hour >= cutoff {
					rows = append(rows, store.Row{hour, w})
				}
			}
		}
		sortRows(rows, 0)
		return store.Result{Rows: rows}, nil
	}
	return store.Result{}, nil
}

// historyRows answers the history read of one agent.
func (f *fakeStore) historyRows(stmt store.Stmt) (store.Result, error) {
	mid, _ := stmt.Args[0].(string)
	terminal, _ := stmt.Args[1].(string)
	cutoff, _ := stmt.Args[2].(int64)
	limit, _ := stmt.Args[3].(int64)
	var rows []store.Row
	for _, h := range f.history {
		if h.machineID != mid || h.terminal != terminal || h.atMS < cutoff {
			continue
		}
		var reason store.Value
		if h.reason != nil {
			reason = *h.reason
		}
		rows = append(rows, store.Row{h.state, reason, h.atMS})
	}
	// Newest first, the ties in the insertion order (stable sort).
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j][2].(int64) > rows[j-1][2].(int64); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	if int64(len(rows)) > limit {
		rows = rows[:limit]
	}
	return store.Result{Rows: rows}, nil
}

// sortRows sorts rows by their first column (stable enough: the keys
// are unique in the tables the fake models).
func sortRows(rows []store.Row, col int) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rowLess(rows[j], rows[j-1], col); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// rowLess compares two rows by their column col.
func rowLess(a, b store.Row, col int) bool {
	av, bv := a[col], b[col]
	switch x := av.(type) {
	case string:
		return x < bv.(string)
	case int64:
		return x < bv.(int64)
	}
	return false
}

// mkAgent builds one inventory agent of the given state.
func mkAgent(terminal, name, state string, since *int64, reason *inventory.Reason) inventory.Agent {
	return inventory.Agent{
		Terminal:    terminal,
		Name:        name,
		Role:        "implementer",
		Kind:        "cli-b",
		Model:       nil,
		Effort:      nil,
		State:       inventory.State(state),
		Reason:      reason,
		Task:        nil,
		ReportReady: false,
		StartedMS:   t0 - 3600000,
		SinceMS:     since,
	}
}

// mkProject builds one inventory project of one session.
func mkProject(name, session string, agents ...inventory.Agent) inventory.Project {
	return inventory.Project{Name: name, Sessions: []inventory.Session{{ID: session, Agents: agents}}}
}

// mkInv builds one inventory of schema 1.
func mkInv(generatedMS int64, hidden inventory.Hidden, projects ...inventory.Project) inventory.Inventory {
	return inventory.Inventory{
		Schema:      inventory.Schema,
		SohoVersion: "v0.3.1",
		GeneratedMS: generatedMS,
		Projects:    projects,
		Hidden:      hidden,
	}
}

// TestApplyTwiceSameInputIsIdempotent: the same input applied twice
// changes the state once.
func TestApplyTwiceSameInputIsIdempotent(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	inv := mkInv(t0, inventory.Hidden{Working: 1},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil)))
	r1, err := Apply(ctx, db, "m1", inv, t0+1000)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !r1.Applied {
		t.Fatal("the first apply did not apply")
	}
	r2, err := Apply(ctx, db, "m1", inv, t0+2000)
	if err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	if r2.Applied {
		t.Fatal("the second apply of the same input applied")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.agents["m1"]) != 1 {
		t.Fatalf("agents = %d, want 1", len(db.agents["m1"]))
	}
	if len(db.history) != 1 {
		t.Fatalf("history rows = %d, want 1", len(db.history))
	}
	if s := db.snapshots["m1"]; s.receivedMS != t0+1000 || s.generatedMS != t0 {
		t.Fatalf("snapshot = %+v, the repeat must not touch it", s)
	}
}

// TestApplyOutOfOrderAndEqual: a generated_ms not greater than the last
// applied one changes nothing.
func TestApplyOutOfOrderAndEqual(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	newer := mkInv(t0+5*hourMS, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil)))
	older := mkInv(t0, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil)))
	if r, err := Apply(ctx, db, "m1", newer, t0+5*hourMS+1000); err != nil || !r.Applied {
		t.Fatalf("Apply the newer: applied=%v err=%v", r.Applied, err)
	}
	r, err := Apply(ctx, db, "m1", older, t0+6*hourMS)
	if err != nil {
		t.Fatalf("Apply the older: %v", err)
	}
	if r.Applied {
		t.Fatal("the out-of-order inventory applied")
	}
	r, err = Apply(ctx, db, "m1", newer, t0+7*hourMS)
	if err != nil {
		t.Fatalf("Apply the equal: %v", err)
	}
	if r.Applied {
		t.Fatal("the equal generated_ms applied")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if a := db.agents["m1"]["w1:p1"]; a.state != "working" {
		t.Fatalf("agent state = %q, the out-of-order inventory must not change it", a.state)
	}
	if len(db.history) != 1 {
		t.Fatalf("history rows = %d, want 1", len(db.history))
	}
	if s := db.snapshots["m1"]; s.generatedMS != t0+5*hourMS || s.receivedMS != t0+5*hourMS+1000 {
		t.Fatalf("snapshot = %+v, the stale inventories must not touch it", s)
	}
}

// TestApplyTransitionsAndRepeatedState: a history row on the new agent
// and on the state or reason change; none on the repeated state. The
// at_ms is the since_ms when present, the generated_ms otherwise.
func TestApplyTransitionsAndRepeatedState(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	s1, s3, s4 := int64(t0+60000), int64(t0+2*3600000), int64(t0+3*3600000)
	ap := inventory.Reason("approval")
	quota := inventory.Reason("quota")
	mustApply := func(g int64, state string, reason *inventory.Reason, since *int64) {
		t.Helper()
		inv := mkInv(g, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", state, since, reason)))
		r, err := Apply(ctx, db, "m1", inv, g+500)
		if err != nil || !r.Applied {
			t.Fatalf("Apply %d: applied=%v err=%v", g, r.Applied, err)
		}
	}
	mustApply(t0+3600000, "working", nil, nil)           // new agent: at_ms = generated
	mustApply(t0+3600000+1000, "needs_you", &ap, &s1)    // state change: at_ms = since
	mustApply(t0+3600000+2000, "needs_you", &ap, &s3)    // repeated state: no row
	mustApply(t0+3600000+3000, "needs_you", &quota, &s3) // reason change: at_ms = since
	mustApply(t0+3600000+4000, "done", nil, &s4)         // state change: at_ms = since
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.history) != 4 {
		t.Fatalf("history rows = %d, want 4", len(db.history))
	}
	want := []struct {
		state  string
		reason string
		atMS   int64
	}{
		{"working", "", t0 + 3600000},
		{"needs_you", "approval", s1},
		{"needs_you", "quota", s3},
		{"done", "", s4},
	}
	for i, w := range want {
		h := db.history[i]
		if h.state != w.state || (h.reason == nil) != (w.reason == "") ||
			(w.reason != "" && *h.reason != w.reason) || h.atMS != w.atMS {
			t.Fatalf("history[%d] = %+v, want %+v", i, h, w)
		}
	}
}

// TestApplyAgentAndProjectDisappear: the agent and the project leave
// the current state; the history of the vanished agent stays.
func TestApplyAgentAndProjectDisappear(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	v1 := mkInv(t0, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil)),
		mkProject("acme/api", "w2", mkAgent("w2:p1", "review", "working", nil, nil)))
	if r, err := Apply(ctx, db, "m1", v1, t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply v1: applied=%v err=%v", r.Applied, err)
	}
	v2 := mkInv(t0+3600000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil)))
	if r, err := Apply(ctx, db, "m1", v2, t0+3600000+1000); err != nil || !r.Applied {
		t.Fatalf("Apply v2: applied=%v err=%v", r.Applied, err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.agents["m1"]) != 1 {
		t.Fatalf("current agents = %d, want 1 (the vanished one leaves)", len(db.agents["m1"]))
	}
	if _, gone := db.agents["m1"]["w2:p1"]; gone {
		t.Fatal("the vanished agent is still in the current state")
	}
	// The history of the vanished agent stays until the retention.
	var kept int
	for _, h := range db.history {
		if h.terminal == "w2:p1" {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("history rows of the vanished agent = %d, want 1", kept)
	}
	// The repeated agent wrote no new row.
	var buildRows int
	for _, h := range db.history {
		if h.terminal == "w1:p1" {
			buildRows++
		}
	}
	if buildRows != 1 {
		t.Fatalf("history rows of the unchanged agent = %d, want 1", buildRows)
	}
}

// TestApplyHiddenReplaced: the hidden counts are replaced whole.
func TestApplyHiddenReplaced(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	if r, err := Apply(ctx, db, "m1", mkInv(t0, inventory.Hidden{Working: 2},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply v1: applied=%v err=%v", r.Applied, err)
	}
	if r, err := Apply(ctx, db, "m1", mkInv(t0+3600000, inventory.Hidden{NeedsYou: 1, Unknown: 1},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), t0+3600000+1000); err != nil || !r.Applied {
		t.Fatalf("Apply v2: applied=%v err=%v", r.Applied, err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	h := db.hidden["m1"]
	want := inventory.Hidden{NeedsYou: 1, Unknown: 1}
	if h != want {
		t.Fatalf("hidden = %+v, want %+v (replaced whole)", h, want)
	}
}

// TestApplyBatchIsAtomic: a failed batch leaves the store untouched.
func TestApplyBatchIsAtomic(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	db.batchErr = errors.New("the batch failed")
	inv := mkInv(t0, inventory.Hidden{Working: 1},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil)))
	if _, err := Apply(ctx, db, "m1", inv, t0+1000); err == nil {
		t.Fatal("Apply: want the batch error")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.snapshots) != 0 || len(db.agents) != 0 || len(db.history) != 0 ||
		len(db.hidden) != 0 || len(db.hourly) != 0 {
		t.Fatal("the failed batch left state behind")
	}
}

// pauseBatch is the store wrapper that pauses before its batch: the
// test interleaves two applies of the same machine inside the window
// between the read and the write of the first one.
type pauseBatch struct {
	*fakeStore
	ready, release chan struct{}
}

func (p *pauseBatch) Batch(ctx context.Context, v []store.Stmt) ([]store.Result, error) {
	select {
	case <-p.ready:
	default:
		close(p.ready)
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return p.fakeStore.Batch(ctx, v)
}

// TestApplyConcurrentKeepsTheNewer: the older apply pauses before its
// batch, the newer one applies fully, and the released older one must
// not land on top of it: the final state is the newer inventory and
// its history has no row of the older one.
func TestApplyConcurrentKeepsTheNewer(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	const g int64 = t0
	if r, err := Apply(ctx, db, "m3", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10); err != nil || !r.Applied {
		t.Fatalf("seed: applied=%v err=%v", r.Applied, err)
	}
	paused := &pauseBatch{fakeStore: db, ready: make(chan struct{}), release: make(chan struct{})}
	type outcome struct {
		applied bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := Apply(ctx, paused, "m3", mkInv(g+1000, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil))), g+1010)
		done <- outcome{r.Applied, err}
	}()
	<-paused.ready // the stale call is inside the window, before its batch
	if r, err := Apply(ctx, db, "m3", mkInv(g+2000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil))), g+2010); err != nil || !r.Applied {
		t.Fatalf("the newer apply: applied=%v err=%v", r.Applied, err)
	}
	close(paused.release)
	out := <-done
	if out.err != nil || out.applied {
		t.Fatalf("the stale apply after the conflict: applied=%v err=%v, want false and no error", out.applied, out.err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if s := db.snapshots["m3"]; s.generatedMS != g+2000 {
		t.Fatalf("snapshot generated_ms = %d, want %d (the newer one wins)", s.generatedMS, g+2000)
	}
	if a := db.agents["m3"]["w1:p1"]; a.state != "done" {
		t.Fatalf("agent state = %q, want done (the stale one must not overwrite)", a.state)
	}
	// The stale apply left no transition of its own.
	for _, h := range db.history {
		if h.machineID == "m3" && h.atMS == g+1000 {
			t.Fatalf("history row at %d: the stale apply must not leave a transition", g+1000)
		}
	}
	// The seed and the newer transition are the only rows of the
	// agent.
	var rows int
	for _, h := range db.history {
		if h.machineID == "m3" && h.terminal == "w1:p1" {
			rows++
		}
	}
	if rows != 2 {
		t.Fatalf("history rows of the agent = %d, want 2 (the seed and the newer)", rows)
	}
}

// TestApplyObservedMSIsReceivedMS: every agent takes the receivedMS as
// its observed_ms.
func TestApplyObservedMSIsReceivedMS(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	inv := mkInv(t0, inventory.Hidden{},
		mkProject("acme/web", "w1",
			mkAgent("w1:p1", "build", "working", nil, nil),
			mkAgent("w1:p2", "review", "done", nil, nil)))
	if r, err := Apply(ctx, db, "m1", inv, t0+42000); err != nil || !r.Applied {
		t.Fatalf("Apply: applied=%v err=%v", r.Applied, err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for terminal, a := range db.agents["m1"] {
		if a.observedMS != t0+42000 {
			t.Fatalf("observed_ms of %s = %d, want the receivedMS %d", terminal, a.observedMS, t0+42000)
		}
	}
}

// TestMachineOfflineAndBack: the machine is online within the 60 s
// window, offline after, and online again when it reports.
func TestMachineOfflineAndBack(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	r1 := t0 + 1000
	if r, err := Apply(ctx, db, "m1", mkInv(t0, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), r1); err != nil || !r.Applied {
		t.Fatalf("Apply v1: applied=%v err=%v", r.Applied, err)
	}
	// Exactly the window: online.
	res, err := Agents(ctx, db, []string{"m1"}, r1+onlineWindowMS)
	if err != nil {
		t.Fatalf("Agents at the window edge: %v", err)
	}
	if len(res.Agents) != 1 || !res.Agents[0].Online {
		t.Fatalf("at %d ms the agent must be online: %+v", onlineWindowMS, res.Agents)
	}
	// One millisecond past the window: offline.
	res, err = Agents(ctx, db, []string{"m1"}, r1+onlineWindowMS+1)
	if err != nil {
		t.Fatalf("Agents past the window: %v", err)
	}
	if len(res.Agents) != 1 || res.Agents[0].Online {
		t.Fatalf("past the window the agent must be offline: %+v", res.Agents)
	}
	detail, err := Agent(ctx, db, "m1", "w1:p1", r1+onlineWindowMS+1)
	if err != nil {
		t.Fatalf("Agent past the window: %v", err)
	}
	if detail.Agent.Online {
		t.Fatal("the Agent query must mark the offline machine")
	}
	// The machine reports again: online.
	r2 := r1 + onlineWindowMS + 1000
	if r, err := Apply(ctx, db, "m1", mkInv(t0+3600000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), r2); err != nil || !r.Applied {
		t.Fatalf("Apply v2: applied=%v err=%v", r.Applied, err)
	}
	res, err = Agents(ctx, db, []string{"m1"}, r2)
	if err != nil {
		t.Fatalf("Agents after the new report: %v", err)
	}
	if len(res.Agents) != 1 || !res.Agents[0].Online {
		t.Fatalf("after the new report the agent must be online: %+v", res.Agents)
	}
}

// TestRetainSevenDayAnd48hBoundaries: the row at the exact boundary
// stays, the one older than it goes. The boundaries are fixed as
// literals, independent of the constants of the package, so a
// changed retention constant breaks the test without a sqld.
func TestRetainSevenDayAnd48hBoundaries(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	now := t0 + 8*24*hourMS
	// Absolute timestamps, on the hour, eight days after the base.
	const sevenDays = 604800000       // 7 * 24 * 3600 * 1000
	const fortyEightHours = 172800000 // 48 * 3600 * 1000
	db.history = []fakeHistory{
		{machineID: "m1", terminal: "w1:p1", project: "acme/web", name: "build", state: "working", atMS: now - sevenDays - 1},
		{machineID: "m1", terminal: "w1:p1", project: "acme/web", name: "build", state: "needs_you", atMS: now - sevenDays},
	}
	db.hourly = map[string]map[int64]int64{
		"m2": {now - fortyEightHours - 1: 1, now - fortyEightHours: 2},
	}
	if err := Retain(ctx, db, now); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	// The cutoffs of the deletes are the literal boundaries.
	if len(db.retainCutoffs) != 2 || db.retainCutoffs[0] != now-sevenDays || db.retainCutoffs[1] != now-fortyEightHours {
		t.Fatalf("retain cutoffs = %v, want [%d %d] (the 7 day and 48 h boundaries)", db.retainCutoffs, now-sevenDays, now-fortyEightHours)
	}
	// The m1 history: one row, at the boundary.
	if len(db.history) != 1 {
		t.Fatalf("history rows = %d, want 1 (the older one is past 7 days)", len(db.history))
	}
	if db.history[0].atMS != now-sevenDays {
		t.Fatalf("history at_ms = %d, want the boundary %d", db.history[0].atMS, now-sevenDays)
	}
	m := db.hourly["m2"]
	if len(m) != 1 {
		t.Fatalf("hours = %d, want 1 (the older one is past 48 h)", len(m))
	}
	if _, ok := m[now-fortyEightHours]; !ok {
		t.Fatalf("hours = %v, want the boundary hour kept", m)
	}
}

// TestWorkingHourlyMax: the hour keeps the maximum of working agents,
// the shown and the hidden ones summed.
func TestWorkingHourlyMax(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	// Hour t0: 2 shown working + 3 hidden working = 5.
	if r, err := Apply(ctx, db, "m1", mkInv(t0+60000, inventory.Hidden{Working: 3},
		mkProject("acme/web", "w1",
			mkAgent("w1:p1", "build", "working", nil, nil),
			mkAgent("w1:p2", "build-2", "working", nil, nil),
			mkAgent("w1:p3", "review", "done", nil, nil))), t0+61000); err != nil || !r.Applied {
		t.Fatalf("Apply v1: applied=%v err=%v", r.Applied, err)
	}
	// Same hour, less working: the maximum stays.
	if r, err := Apply(ctx, db, "m1", mkInv(t0+1800000, inventory.Hidden{Working: 1},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), t0+1801000); err != nil || !r.Applied {
		t.Fatalf("Apply v2: applied=%v err=%v", r.Applied, err)
	}
	// The next hour: 7 shown working.
	if r, err := Apply(ctx, db, "m1", mkInv(t0+hourMS+60000, inventory.Hidden{},
		mkProject("acme/web", "w1",
			mkAgent("w1:p1", "build", "working", nil, nil),
			mkAgent("w1:p2", "build-2", "working", nil, nil),
			mkAgent("w1:p3", "review", "working", nil, nil),
			mkAgent("w1:p4", "scouter", "working", nil, nil),
			mkAgent("w1:p5", "designer", "working", nil, nil),
			mkAgent("w1:p6", "tasker", "working", nil, nil),
			mkAgent("w1:p7", "inspector", "working", nil, nil))), t0+hourMS+61000); err != nil || !r.Applied {
		t.Fatalf("Apply v3: applied=%v err=%v", r.Applied, err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	m := db.hourly["m1"]
	if len(m) != 2 {
		t.Fatalf("hours = %v, want 2", m)
	}
	if m[t0] != 5 {
		t.Fatalf("hour t0 = %d, want 5 (the maximum of the two inventories)", m[t0])
	}
	if m[t0+hourMS] != 7 {
		t.Fatalf("hour t0+1h = %d, want 7", m[t0+hourMS])
	}
}

// TestAgentsQuery: the fields, the online flag, the hidden filter and
// the machine list filtering.
func TestAgentsQuery(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	model := "example/model-1"
	inv1 := inventory.Inventory{
		Schema: inventory.Schema, SohoVersion: "v0.3.1", GeneratedMS: t0,
		Projects: []inventory.Project{{
			Name: "acme/web",
			Sessions: []inventory.Session{{ID: "w1", Agents: []inventory.Agent{
				mkAgent("w1:p1", "build", "working", nil, nil),
				{Terminal: "w1:p2", Name: "review", Role: "review", Kind: "cli-a",
					Model: &model, State: inventory.StateNeedsYou,
					Reason: ptrReason("approval"), Task: ptrStr("Retry declined cards"),
					ReportReady: true, StartedMS: t0 - 7200000, SinceMS: ptrInt64(t0 + 100000)},
			}}},
		}},
		Hidden: inventory.Hidden{Working: 1},
	}
	inv2 := mkInv(t0, inventory.Hidden{},
		mkProject("acme/api", "w1", mkAgent("w1:p1", "build", "idle", nil, nil)))
	if r, err := Apply(ctx, db, "m1", inv1, t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply m1: applied=%v err=%v", r.Applied, err)
	}
	if r, err := Apply(ctx, db, "m2", inv2, t0+1500); err != nil || !r.Applied {
		t.Fatalf("Apply m2: applied=%v err=%v", r.Applied, err)
	}
	// m1 reports again, ten hours later, with the same agents: the
	// snapshot is fresh at now.
	inv1b := inv1
	inv1b.GeneratedMS = t0 + 10*hourMS
	if r, err := Apply(ctx, db, "m1", inv1b, t0+10*hourMS+1000); err != nil || !r.Applied {
		t.Fatalf("Apply m1 again: applied=%v err=%v", r.Applied, err)
	}
	now := t0 + 10*hourMS + 2000 // m1 online (1000 ms old), m2 offline (10 h old)
	res, err := Agents(ctx, db, []string{"m1", "m2"}, now)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(res.Agents) != 3 {
		t.Fatalf("agents = %d, want 3", len(res.Agents))
	}
	byKey := map[string]AgentRow{}
	for _, a := range res.Agents {
		byKey[a.MachineID+a.Terminal] = a
	}
	a1 := byKey["m1w1:p1"]
	if !a1.Online || a1.State != inventory.StateWorking || a1.Project != "acme/web" ||
		a1.Session != "w1" || a1.ObservedMS != t0+10*hourMS+1000 || a1.ReportReady || a1.SinceMS != nil {
		t.Fatalf("agent m1/w1:p1 = %+v", a1)
	}
	a2 := byKey["m1w1:p2"]
	if a2.State != inventory.StateNeedsYou || a2.Model == nil || *a2.Model != model ||
		a2.Reason == nil || *a2.Reason != "approval" || a2.Task == nil || *a2.Task != "Retry declined cards" ||
		!a2.ReportReady || a2.SinceMS == nil || *a2.SinceMS != t0+100000 || a2.StartedMS != t0-7200000 {
		t.Fatalf("agent m1/w1:p2 = %+v", a2)
	}
	if a3 := byKey["m2w1:p1"]; a3.Online || a3.State != inventory.StateIdle {
		t.Fatalf("agent m2/w1:p1 = %+v, want offline idle", a3)
	}
	// The hidden list holds only the machine with at least one hidden
	// agent: m1, not m2 (all zero).
	if len(res.Hidden) != 1 || res.Hidden[0].MachineID != "m1" || res.Hidden[0].Counts.Working != 1 {
		t.Fatalf("hidden = %+v, want only m1 with working 1", res.Hidden)
	}
	// The machine list filters: m2 alone sees no agents of m1.
	res2, err := Agents(ctx, db, []string{"m2"}, now)
	if err != nil {
		t.Fatalf("Agents m2 only: %v", err)
	}
	if len(res2.Agents) != 1 || res2.Agents[0].MachineID != "m2" {
		t.Fatalf("agents = %+v, the machine list must filter", res2.Agents)
	}
	if len(res2.Hidden) != 0 {
		t.Fatalf("hidden = %+v, m2 has no hidden agent", res2.Hidden)
	}
}

// TestAgentsEmptyListNoQuery: the empty list returns the empty answer
// without consulting the store.
func TestAgentsEmptyListNoQuery(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	res, err := Agents(ctx, db, nil, t0)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(res.Agents) != 0 || len(res.Hidden) != 0 {
		t.Fatalf("answer = %+v, want empty", res)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.executeCalls != 0 || db.batchCalls != 0 {
		t.Fatalf("store calls = %d/%d, want none", db.executeCalls, db.batchCalls)
	}
}

// recordingStore records the statements it runs, for the checks of the
// argument budget of the chunked queries.
type recordingStore struct {
	store.Store
	stmts []store.Stmt
}

func (r *recordingStore) Execute(ctx context.Context, s store.Stmt) (store.Result, error) {
	r.stmts = append(r.stmts, s)
	return r.Store.Execute(ctx, s)
}

// TestLargeMachineListSplitsTheQuery: the machine list is split so
// that no statement exceeds the argument budget, the duplicate id is
// counted once, and the aggregates sum over the chunks.
func TestLargeMachineListSplitsTheQuery(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	const n = 1203
	// t0 is on the hour: the 24 h series ends at the last complete
	// hour, the hour before the base.
	for i := 0; i < n; i++ {
		id := "m" + itoa(i)
		db.snapshots[id] = fakeSnapshot{generatedMS: t0, receivedMS: t0 + int64(i), sohoVersion: "v0.3.1"}
		db.agents[id] = map[string]fakeAgent{"w1:p1": fakeAgent{
			terminal: "w1:p1", project: "acme/web", session: "w1", name: "build",
			role: "implementer", kind: "cli-b", state: "working", observedMS: t0,
		}}
		db.hidden[id] = inventory.Hidden{Working: 1}
		db.hourly[id] = map[int64]int64{t0 - hourMS: 1}
	}
	// The list carries one duplicate: the join must count it once.
	list := make([]string, n+1)
	for i := 0; i < n; i++ {
		list[i] = "m" + itoa(i)
	}
	list[n] = "m0"
	rec := &recordingStore{Store: db}
	res, err := Agents(ctx, rec, list, t0+1000)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(res.Agents) != n {
		t.Fatalf("agents = %d, want %d (the duplicate counted once)", len(res.Agents), n)
	}
	if len(res.Hidden) != n {
		t.Fatalf("hidden = %d, want %d", len(res.Hidden), n)
	}
	for _, s := range rec.stmts {
		if strings.Contains(s.SQL, "machine_id IN (") && len(s.Args) > maxStatementArgs {
			t.Fatalf("a statement of the large list carries %d arguments, want at most %d", len(s.Args), maxStatementArgs)
		}
	}
	// The overview sums over the chunks: the shown plus the hidden
	// working, and the hour summed over the machines.
	rec2 := &recordingStore{Store: db}
	ov, err := Overview(ctx, rec2, list, t0+1000)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.Counts.Working != 2*n {
		t.Fatalf("working = %d, want %d (the shown plus the hidden, over the chunks)", ov.Counts.Working, 2*n)
	}
	if len(ov.PerMachine) != n {
		t.Fatalf("per_machine = %d, want %d", len(ov.PerMachine), n)
	}
	if w := ov.Working24h[hourlyBuckets-1].Working; w != n {
		t.Fatalf("the last complete hour = %d, want %d (the sum over the chunks)", w, n)
	}
	for _, s := range rec2.stmts {
		if strings.Contains(s.SQL, "machine_id IN (") && len(s.Args) > maxStatementArgs {
			t.Fatalf("a statement of the large list carries %d arguments, want at most %d", len(s.Args), maxStatementArgs)
		}
	}
}

// TestAgentQuery: the agent with the history, newest first, the 7 day
// window and the 200 rows cap; ErrNotFound on the absent agent.
func TestAgentQuery(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	now := t0 + 9*24*hourMS
	// One old transition (8 days back), then 205 recent ones, a state
	// flip per inventory.
	oldSince := now - 8*24*hourMS
	if r, err := Apply(ctx, db, "m1", mkInv(oldSince, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", &oldSince, nil))), oldSince+100); err != nil || !r.Applied {
		t.Fatalf("Apply the old inventory: applied=%v err=%v", r.Applied, err)
	}
	state := "working"
	for i := 1; i <= 205; i++ {
		// 15 minutes apart: all 205 transitions fall inside the 7 day
		// window, so the 200 rows cap is the one that trims.
		g := now - int64(205-i)*900000
		if g <= oldSince {
			g = oldSince + int64(i)
		}
		if i%2 == 0 {
			state = "idle"
		} else {
			state = "working"
		}
		if r, err := Apply(ctx, db, "m1", mkInv(g, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", state, nil, nil))), g+100); err != nil || !r.Applied {
			t.Fatalf("Apply %d: applied=%v err=%v", i, r.Applied, err)
		}
	}
	// The machine is offline: the query runs two hours after the last
	// received (the applies stop at now).
	queryNow := now + 2*hourMS
	detail, err := Agent(ctx, db, "m1", "w1:p1", queryNow)
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	if detail.Agent.Online {
		t.Fatal("the stale machine must be offline")
	}
	if len(detail.History) != 200 {
		t.Fatalf("history rows = %d, want 200 (the contractual cap)", len(detail.History))
	}
	// Newest first.
	for i := 1; i < len(detail.History); i++ {
		if detail.History[i-1].AtMS <= detail.History[i].AtMS {
			t.Fatalf("history not newest first at %d: %d then %d", i,
				detail.History[i-1].AtMS, detail.History[i].AtMS)
		}
	}
	// The 7 day window: nothing older than queryNow-7d.
	cutoff := queryNow - retentionHistory
	for _, h := range detail.History {
		if h.AtMS < cutoff {
			t.Fatalf("history at_ms %d is past the 7 day window", h.AtMS)
		}
	}
	// The old (8 days back) transition must be out of the answer.
	for _, h := range detail.History {
		if h.AtMS == oldSince {
			t.Fatalf("the 8 day old transition is in the answer")
		}
	}
	// The five oldest of the 205 recent transitions are the ones the
	// cap leaves out, by the newest-first order of the contract.
	if detail.History[0].AtMS != now {
		t.Fatalf("history[0] at_ms = %d, want %d (the newest)", detail.History[0].AtMS, now)
	}
	if detail.History[199].AtMS != now-199*900000 {
		t.Fatalf("history[199] at_ms = %d, want %d (the 200th newest)", detail.History[199].AtMS, now-199*900000)
	}
	for i := 200; i <= 204; i++ {
		excluded := now - int64(i)*900000
		for _, h := range detail.History {
			if h.AtMS == excluded {
				t.Fatalf("history at_ms %d: past the cap, out of the answer", excluded)
			}
		}
	}
	// The absent agent is ErrNotFound.
	if _, err := Agent(ctx, db, "m1", "w1:p9", queryNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Agent absent: err = %v, want ErrNotFound", err)
	}
	if _, err := Agent(ctx, db, "no-such-machine", "w1:p1", queryNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Agent absent machine: err = %v, want ErrNotFound", err)
	}
}

// TestOverview: the counts with the hidden ones, the machines split,
// the needs_you list, the per-machine, the 24 h series and the top
// projects.
func TestOverview(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	sinceA, sinceB, sinceC := t0+100000, t0+200000, t0+300000
	inv1 := inventory.Inventory{
		Schema: inventory.Schema, SohoVersion: "v0.3.1", GeneratedMS: t0,
		Projects: []inventory.Project{
			{Name: "acme/web", Sessions: []inventory.Session{{ID: "w1", Agents: []inventory.Agent{
				mkAgent("w1:p1", "build", "working", nil, nil),
				mkAgent("w1:p2", "build-2", "working", nil, nil),
				{Terminal: "w1:p3", Name: "review", Role: "review", Kind: "cli-a",
					State: inventory.StateNeedsYou, Reason: ptrReason("approval"),
					StartedMS: t0 - 7200000, SinceMS: &sinceB},
				{Terminal: "w1:p4", Name: "planner", Role: "planner", Kind: "cli-a",
					State: inventory.StateNeedsYou, StartedMS: t0 - 7200000, SinceMS: nil},
			}}}},
			{Name: "acme/api", Sessions: []inventory.Session{{ID: "w2", Agents: []inventory.Agent{
				mkAgent("w2:p1", "tasker", "idle", &sinceC, nil),
			}}}},
		},
		Hidden: inventory.Hidden{Working: 1, NeedsYou: 1, Done: 1, Unknown: 2},
	}
	inv2 := mkInv(t0, inventory.Hidden{},
		mkProject("acme/web", "w1",
			mkAgent("w1:p1", "build", "working", &sinceA, nil),
			mkAgent("w1:p2", "review", "needs_you", &sinceC, nil)))
	if r, err := Apply(ctx, db, "m1", inv1, t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply m1: applied=%v err=%v", r.Applied, err)
	}
	if r, err := Apply(ctx, db, "m2", inv2, t0+1500); err != nil || !r.Applied {
		t.Fatalf("Apply m2: applied=%v err=%v", r.Applied, err)
	}
	// m1 reports again, two hours later, with the same agents: no new
	// history, but the snapshot is fresh (m1 is online at now).
	inv1b := inv1
	inv1b.GeneratedMS = t0 + 2*hourMS + 1000
	if r, err := Apply(ctx, db, "m1", inv1b, t0+2*hourMS+1500); err != nil || !r.Applied {
		t.Fatalf("Apply m1 again: applied=%v err=%v", r.Applied, err)
	}
	now := t0 + 2*hourMS + 2000 // m1 online (500 ms old), m2 offline (10 h old)
	res, err := Overview(ctx, db, []string{"m1", "m2"}, now)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	// Counts: the shown plus the hidden.
	if res.Counts != (Counts{Working: 4, NeedsYou: 4, Done: 1, Idle: 1, Unknown: 2}) {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if res.Machines != (Machines{Online: 1, Offline: 1}) {
		t.Fatalf("machines = %+v", res.Machines)
	}
	// NeedsYou: at most 10, the oldest since_ms first, the one without
	// since_ms after the ones with.
	if len(res.NeedsYou) != 3 {
		t.Fatalf("needs_you = %d, want 3", len(res.NeedsYou))
	}
	wantNeed := []struct {
		mid, terminal string
		online        bool
	}{
		{"m1", "w1:p3", true},  // since t0+200000, oldest of the with-since
		{"m2", "w1:p2", false}, // since t0+300000
		{"m1", "w1:p4", true},  // no since_ms: last
	}
	for i, w := range wantNeed {
		a := res.NeedsYou[i]
		if a.MachineID != w.mid || a.Terminal != w.terminal || a.Online != w.online ||
			a.State != inventory.StateNeedsYou {
			t.Fatalf("needs_you[%d] = %+v, want %s/%s online=%v", i, a, w.mid, w.terminal, w.online)
		}
	}
	// PerMachine: every visible machine, the offline one included.
	if len(res.PerMachine) != 2 {
		t.Fatalf("per_machine = %+v", res.PerMachine)
	}
	if res.PerMachine[0] != (MachineCounts{MachineID: "m1", Counts: Counts{Working: 3, NeedsYou: 3, Done: 1, Idle: 1, Unknown: 2}}) {
		t.Fatalf("per_machine m1 = %+v", res.PerMachine[0])
	}
	if res.PerMachine[1] != (MachineCounts{MachineID: "m2", Counts: Counts{Working: 1, NeedsYou: 1}}) {
		t.Fatalf("per_machine m2 = %+v", res.PerMachine[1])
	}
	// TopProjects: at most 5, by working and then by total.
	if len(res.TopProjects) != 2 {
		t.Fatalf("top_projects = %+v", res.TopProjects)
	}
	if res.TopProjects[0] != (TopProject{Project: "acme/web", Agents: 6, Working: 3}) {
		t.Fatalf("top_projects[0] = %+v", res.TopProjects[0])
	}
	if res.TopProjects[1] != (TopProject{Project: "acme/api", Agents: 1, Working: 0}) {
		t.Fatalf("top_projects[1] = %+v", res.TopProjects[1])
	}
	// Working24h: exactly 24 entries, oldest first, zero where there
	// is no data, the last one at the last complete hour.
	if len(res.Working24h) != hourlyBuckets {
		t.Fatalf("working_24h = %d entries, want %d", len(res.Working24h), hourlyBuckets)
	}
	curHour := hourOf(now)
	if res.Working24h[0].HourMS != curHour-int64(hourlyBuckets)*hourMS {
		t.Fatalf("working_24h[0] = %d, want %d", res.Working24h[0].HourMS, curHour-int64(hourlyBuckets)*hourMS)
	}
	if res.Working24h[hourlyBuckets-1].HourMS != curHour-hourMS {
		t.Fatalf("working_24h[last] = %d, want %d", res.Working24h[hourlyBuckets-1].HourMS, curHour-hourMS)
	}
	// The inventories of the hour t0 left 2 shown + 1 hidden working
	// on m1 and 1 on m2: the hour t0 sums 4. now is in the hour
	// t0+2h, so t0 is the second to last entry (index 22).
	if res.Working24h[hourlyBuckets-2].Working != 4 {
		t.Fatalf("working_24h[%d] = %d, want 4 (the sum over the machines)", hourlyBuckets-2, res.Working24h[hourlyBuckets-2].Working)
	}
	for i := 0; i < hourlyBuckets; i++ {
		if i == hourlyBuckets-2 {
			continue
		}
		if res.Working24h[i].Working != 0 {
			t.Fatalf("working_24h[%d] = %d, want 0 (no data)", i, res.Working24h[i].Working)
		}
	}
}

// TestOverviewNeedsYouCap: more than 10 needs_you agents keep the 10
// oldest.
func TestOverviewNeedsYouCap(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	agents := make([]inventory.Agent, 0, 12)
	for i := 1; i <= 12; i++ {
		s := t0 + int64(i)*1000
		agents = append(agents, inventory.Agent{
			Terminal: "w1:p" + itoa(i), Name: "w" + itoa(i), Role: "implementer",
			Kind: "cli-b", State: inventory.StateNeedsYou,
			Reason: ptrReason("question"), StartedMS: t0 - 7200000, SinceMS: &s,
		})
	}
	inv := inventory.Inventory{
		Schema: inventory.Schema, SohoVersion: "v0.3.1", GeneratedMS: t0,
		Projects: []inventory.Project{{Name: "acme/web", Sessions: []inventory.Session{
			{ID: "w1", Agents: agents}}}},
		Hidden: inventory.Hidden{},
	}
	if r, err := Apply(ctx, db, "m1", inv, t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply: applied=%v err=%v", r.Applied, err)
	}
	res, err := Overview(ctx, db, []string{"m1"}, t0+2000)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(res.NeedsYou) != 10 {
		t.Fatalf("needs_you = %d, want 10 (the contractual cap)", len(res.NeedsYou))
	}
	// The 10 oldest: since t0+1000 .. t0+10000, the two newest out.
	for i, a := range res.NeedsYou {
		if a.SinceMS == nil || *a.SinceMS != t0+int64(i+1)*1000 {
			t.Fatalf("needs_you[%d] since = %v, want %d", i, a.SinceMS, t0+int64(i+1)*1000)
		}
	}
	for _, a := range res.NeedsYou {
		if a.SinceMS != nil && *a.SinceMS > t0+10000 {
			t.Fatalf("needs_you since %d: past the cap, out of the answer", *a.SinceMS)
		}
	}
}

// TestOverviewTopProjectsCap: more than 5 projects keep the 5 by
// working and then by total.
func TestOverviewTopProjectsCap(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	projects := make([]inventory.Project, 0, 7)
	for i := 1; i <= 7; i++ {
		// Project 1: 2 working. Project 2: 2 agents idle. The rest:
		// 1 agent idle.
		state := "idle"
		count := 1
		if i == 1 {
			state, count = "working", 2
		}
		if i == 2 {
			count = 2
		}
		agents := make([]inventory.Agent, 0, count)
		for j := 1; j <= count; j++ {
			// One session and one terminal per project: the terminals
			// never repeat across projects.
			agents = append(agents, mkAgent("w"+itoa(i)+":p"+itoa(j), "w"+itoa(j), state, nil, nil))
		}
		projects = append(projects, inventory.Project{
			Name:     "proj" + itoa(i),
			Sessions: []inventory.Session{{ID: "w" + itoa(i), Agents: agents}},
		})
	}
	inv := inventory.Inventory{
		Schema: inventory.Schema, SohoVersion: "v0.3.1", GeneratedMS: t0,
		Projects: projects, Hidden: inventory.Hidden{},
	}
	if r, err := Apply(ctx, db, "m1", inv, t0+1000); err != nil || !r.Applied {
		t.Fatalf("Apply: applied=%v err=%v", r.Applied, err)
	}
	res, err := Overview(ctx, db, []string{"m1"}, t0+2000)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(res.TopProjects) != 5 {
		t.Fatalf("top_projects = %d, want 5 (the contractual cap)", len(res.TopProjects))
	}
	// proj1 (2 working) first, then proj2 (2 idle) and the rest by
	// the name (the tie-breaker of the test).
	wantOrder := []string{"proj1", "proj2", "proj3", "proj4", "proj5"}
	for i, w := range wantOrder {
		if res.TopProjects[i].Project != w {
			t.Fatalf("top_projects[%d] = %q, want %q", i, res.TopProjects[i].Project, w)
		}
	}
	// proj6 and proj7 are the two left out by the cap.
	for _, p := range res.TopProjects {
		if p.Project == "proj6" || p.Project == "proj7" {
			t.Fatalf("top_projects = %v: %s is past the cap, out of the answer", res.TopProjects, p.Project)
		}
	}
}

// TestOverviewEmptyListNoQuery: the empty list returns the empty
// answer without consulting the store.
func TestOverviewEmptyListNoQuery(t *testing.T) {
	db := newFakeStore()
	ctx := context.Background()
	res, err := Overview(ctx, db, nil, t0)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if res.Counts != (Counts{}) || res.Machines != (Machines{}) ||
		len(res.NeedsYou) != 0 || len(res.PerMachine) != 0 ||
		len(res.Working24h) != 0 || len(res.TopProjects) != 0 {
		t.Fatalf("answer = %+v, want empty", res)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.executeCalls != 0 || db.batchCalls != 0 {
		t.Fatalf("store calls = %d/%d, want none", db.executeCalls, db.batchCalls)
	}
}

// ptrStr, ptrInt64 and ptrReason are the pointer helpers of the tests.
func ptrStr(s string) *string { return &s }
func ptrInt64(n int64) *int64 { return &n }
func ptrReason(r string) *inventory.Reason {
	v := inventory.Reason(r)
	return &v
}

// itoa is the decimal form of the small integers of the tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
