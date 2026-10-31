//go:build sqld

package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/sqld"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// realIngestStore builds the client of the real sqld: it drops the
// tables of a previous run and applies the migrations from zero. It
// skips when the URL is not set (the test must not run in CI).
func realIngestStore(t *testing.T) store.Store {
	t.Helper()
	u := os.Getenv("NAU_TEST_SQLD_URL")
	if u == "" {
		t.Skip("NAU_TEST_SQLD_URL is not set")
	}
	c, err := sqld.New(u, os.Getenv("NAU_TEST_SQLD_TOKEN"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The tables of every migration, the children of the foreign keys
	// first (the sessions and the machines reference the users), so
	// the reset is re-runnable on a shared database no matter what
	// the previous packages left behind.
	for _, name := range []string{
		"sessions", "machines",
		"pairings", "pairing_attempts", "ingest_deliveries", "shares",
		"agent_history", "agents_current", "hidden_counts", "working_hourly",
		"inventory_snapshots", "login_failures", "invites",
		"users",
		"pairing_guard", "ingest_guard", "accounts_guard",
		"schema_migrations", "hub_meta",
	} {
		if _, err := c.Execute(ctx, store.Stmt{SQL: `DROP TABLE IF EXISTS ` + name}); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	r := &migrate.Runner{Store: c, Now: time.Now}
	if err := r.Apply(ctx); err != nil {
		t.Fatalf("Apply the migrations: %v", err)
	}
	return c
}

// TestSQLDApplyAndQueries: the Apply rules and the queries against the
// real sqld, the inventories coming from the strict reader.
func TestSQLDApplyAndQueries(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	doc := `{
	  "schema": 1,
	  "soho_version": "v0.3.1",
	  "generated_ms": 1793376000000,
	  "projects": [
	    {
	      "name": "acme/web",
	      "sessions": [
	        {
	          "id": "w1",
	          "agents": [
	            {"terminal": "w1:p1", "name": "orchestrator", "role": "orchestrator", "kind": "cli-a", "state": "working", "report_ready": false, "started_ms": 1793372400000},
	            {"terminal": "w1:p2", "name": "build", "role": "implementer", "kind": "cli-b", "model": "example/model-1", "effort": "high", "state": "needs_you", "reason": "approval", "task": "Retry declined cards", "report_ready": false, "started_ms": 1793373000000, "since_ms": 1793375990000}
	          ]
	        }
	      ]
	    }
	  ],
	  "hidden": {"working": 1, "needs_you": 0, "done": 1, "idle": 0, "unknown": 0}
	}`
	inv, err := inventory.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("inventory.Parse: %v", err)
	}
	const gen int64 = 1793376000000
	const recv int64 = 1793376001000
	r, err := Apply(ctx, st, "m1", inv, recv)
	if err != nil || !r.Applied {
		t.Fatalf("Apply: applied=%v err=%v", r.Applied, err)
	}
	// The repeat and the out-of-order inventory change nothing.
	if r, err := Apply(ctx, st, "m1", inv, recv+1000); err != nil || r.Applied {
		t.Fatalf("Apply the repeat: applied=%v err=%v", r.Applied, err)
	}
	stale := inv
	stale.GeneratedMS = gen - 1
	if r, err := Apply(ctx, st, "m1", stale, recv+2000); err != nil || r.Applied {
		t.Fatalf("Apply the stale: applied=%v err=%v", r.Applied, err)
	}
	// The transition: build goes to working, with a new since_ms.
	inv2 := inv
	build := &inv2.Projects[0].Sessions[0].Agents[1]
	since := int64(1793376050000)
	build.State = inventory.StateWorking
	build.Reason = nil
	build.SinceMS = &since
	inv2.GeneratedMS = gen + 50000
	if r, err := Apply(ctx, st, "m1", inv2, recv+51000); err != nil || !r.Applied {
		t.Fatalf("Apply the transition: applied=%v err=%v", r.Applied, err)
	}

	row := func(sql string, args ...store.Value) store.Row {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: sql, Args: args})
		if err != nil {
			t.Fatalf("Execute %s: %v", sql, err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("%s: rows = %v, want 1", sql, res.Rows)
		}
		return res.Rows[0]
	}
	// The snapshot took the transition, not the stale inventories.
	if s := row("SELECT generated_ms, received_ms, soho_version FROM inventory_snapshots WHERE machine_id = ?", "m1"); s[0] != inv2.GeneratedMS || s[1] != recv+51000 || s[2] != "v0.3.1" {
		t.Fatalf("snapshot = %v", s)
	}
	// The hidden counts are the five keys.
	if h := row("SELECT working, needs_you, done, idle, unknown FROM hidden_counts WHERE machine_id = ?", "m1"); h[0] != int64(1) || h[1] != int64(0) || h[2] != int64(1) || h[3] != int64(0) || h[4] != int64(0) {
		t.Fatalf("hidden = %v", h)
	}
	// The hour keeps the maximum: 1 shown + 1 hidden working at first,
	// then 2 shown + 1 hidden in the same hour.
	if h := row("SELECT hour_ms, max_working FROM working_hourly WHERE machine_id = ?", "m1"); h[0] != gen || h[1] != int64(3) {
		t.Fatalf("hour = %v, want the hour of generated with 3", h)
	}
	// The history: the new agent and the transition, with the since_ms
	// as at_ms.
	res, err := st.Execute(ctx, store.Stmt{
		SQL:  "SELECT state, at_ms FROM agent_history WHERE machine_id = ? AND terminal = ? ORDER BY at_ms",
		Args: []store.Value{"m1", "w1:p2"},
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "needs_you" || res.Rows[0][1] != int64(1793375990000) ||
		res.Rows[1][0] != "working" || res.Rows[1][1] != int64(1793376050000) {
		t.Fatalf("history = %v", res.Rows)
	}

	// The queries: the machine list filters and the online flag comes
	// from the snapshot.
	now := recv + 1000
	agents, err := Agents(ctx, st, []string{"m1", "m2"}, now)
	if err != nil {
		t.Fatalf("Agents: %v", err)
	}
	if len(agents.Agents) != 2 {
		t.Fatalf("agents = %d, want 2 (m2 has none)", len(agents.Agents))
	}
	for _, a := range agents.Agents {
		if a.MachineID != "m1" || !a.Online {
			t.Fatalf("agent = %+v, want m1 online", a)
		}
	}
	if len(agents.Hidden) != 1 || agents.Hidden[0].MachineID != "m1" {
		t.Fatalf("hidden = %+v", agents.Hidden)
	}
	if empty, err := Agents(ctx, st, nil, now); err != nil || len(empty.Agents) != 0 {
		t.Fatalf("Agents empty: agents=%d err=%v", len(empty.Agents), err)
	}

	// The agent with the history, newest first.
	detail, err := Agent(ctx, st, "m1", "w1:p2", now)
	if err != nil {
		t.Fatalf("Agent: %v", err)
	}
	if detail.Agent.State != inventory.StateWorking || detail.Agent.Reason != nil ||
		detail.Agent.Model == nil || *detail.Agent.Model != "example/model-1" ||
		detail.Agent.ObservedMS != recv+51000 {
		t.Fatalf("agent = %+v, want the transition applied", detail.Agent)
	}
	if len(detail.History) != 2 || detail.History[0].AtMS != 1793376050000 || detail.History[1].AtMS != 1793375990000 {
		t.Fatalf("history = %+v, want newest first", detail.History)
	}
	if _, err := Agent(ctx, st, "m1", "w1:p9", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Agent absent: %v, want ErrNotFound", err)
	}

	// The overview, two hours later: the machine is offline, the hour
	// of the inventories is the last entry of the 24 h series.
	now2 := gen + 2*hourMS + 1000
	ov, err := Overview(ctx, st, []string{"m1"}, now2)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if ov.Counts.Working != 3 || ov.Counts.Done != 1 {
		t.Fatalf("counts = %+v, want the shown plus the hidden", ov.Counts)
	}
	if ov.Machines != (Machines{Offline: 1}) {
		t.Fatalf("machines = %+v, want offline at now2", ov.Machines)
	}
	if len(ov.TopProjects) != 1 || ov.TopProjects[0].Project != "acme/web" ||
		ov.TopProjects[0].Agents != 2 || ov.TopProjects[0].Working != 2 {
		t.Fatalf("top_projects = %+v", ov.TopProjects)
	}
	if len(ov.Working24h) != hourlyBuckets {
		t.Fatalf("working_24h = %d, want %d", len(ov.Working24h), hourlyBuckets)
	}
	if last := ov.Working24h[hourlyBuckets-2]; last.HourMS != gen || last.Working != 3 {
		t.Fatalf("working_24h = %+v, want the hour %d with 3 (one hour before the end)", last, gen)
	}
}

// TestSQLDRetainBoundaries: the row at the exact boundary stays, the
// one older than it goes.
func TestSQLDRetainBoundaries(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const now int64 = 1794067200000 // on the hour
	insert := func(sql string, args ...store.Value) {
		t.Helper()
		if _, err := st.Execute(ctx, store.Stmt{SQL: sql, Args: args}); err != nil {
			t.Fatalf("Execute %s: %v", sql, err)
		}
	}
	count := func(sql string, args ...store.Value) int64 {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: sql, Args: args})
		if err != nil {
			t.Fatalf("Execute %s: %v", sql, err)
		}
		n, _ := res.Rows[0][0].(int64)
		return n
	}
	const hist = "INSERT INTO agent_history (machine_id, terminal, project, name, state, reason, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)"
	cutoff := now - 7*24*3600*1000
	insert(hist, "m1", "w1:p1", "acme/web", "build", "working", nil, int64(cutoff-1))
	insert(hist, "m1", "w1:p1", "acme/web", "build", "idle", nil, int64(cutoff))
	const hour = "INSERT INTO working_hourly (machine_id, hour_ms, max_working) VALUES (?, ?, ?)"
	hcut := now - 48*3600*1000
	insert(hour, "m1", int64(hcut-3600000), int64(1))
	insert(hour, "m1", int64(hcut), int64(2))
	if err := Retain(ctx, st, now); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if n := count("SELECT COUNT(*) FROM agent_history WHERE at_ms = ?", cutoff); n != 1 {
		t.Fatalf("history at the boundary = %d, want 1 (kept)", n)
	}
	if n := count("SELECT COUNT(*) FROM agent_history WHERE at_ms = ?", cutoff-1); n != 0 {
		t.Fatalf("history past the boundary = %d, want 0 (deleted)", n)
	}
	if n := count("SELECT COUNT(*) FROM working_hourly WHERE hour_ms = ?", hcut); n != 1 {
		t.Fatalf("hour at the boundary = %d, want 1 (kept)", n)
	}
	if n := count("SELECT COUNT(*) FROM working_hourly WHERE hour_ms = ?", hcut-3600000); n != 0 {
		t.Fatalf("hour past the boundary = %d, want 0 (deleted)", n)
	}
}

// pauseRealBatch is the store wrapper that pauses before its batch:
// the test interleaves two applies of the same machine inside the
// window between the read and the write of the first one.
type pauseRealBatch struct {
	store.Store
	ready, release chan struct{}
}

func (p *pauseRealBatch) Batch(ctx context.Context, v []store.Stmt) ([]store.Result, error) {
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
	return p.Store.Batch(ctx, v)
}

// TestSQLDConcurrentApplyKeepsTheNewer: against the real sqld, the
// older apply pauses before its batch, the newer one applies fully,
// and the released older one must not land on top of it: the final
// state is the newer inventory and its history has no row of the
// older one.
func TestSQLDConcurrentApplyKeepsTheNewer(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const g int64 = 1793376000000
	if r, err := Apply(ctx, st, "m3", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10); err != nil || !r.Applied {
		t.Fatalf("seed: applied=%v err=%v", r.Applied, err)
	}
	hold := &pauseRealBatch{Store: st, ready: make(chan struct{}), release: make(chan struct{})}
	type outcome struct {
		applied bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := Apply(ctx, hold, "m3", mkInv(g+1000, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil))), g+1010)
		done <- outcome{r.Applied, err}
	}()
	select {
	case <-hold.ready:
	case <-ctx.Done():
		t.Fatal("the stale apply did not reach its batch")
	}
	if r, err := Apply(ctx, st, "m3", mkInv(g+2000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil))), g+2010); err != nil || !r.Applied {
		t.Fatalf("the newer apply: applied=%v err=%v", r.Applied, err)
	}
	close(hold.release)
	out := <-done
	if out.err != nil || out.applied {
		t.Fatalf("the stale apply after the conflict: applied=%v err=%v, want false and no error", out.applied, out.err)
	}
	res, err := st.Execute(ctx, store.Stmt{SQL: "SELECT generated_ms FROM inventory_snapshots WHERE machine_id = ?", Args: []store.Value{"m3"}})
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != g+2000 {
		t.Fatalf("snapshot = %v, want %d (the newer one wins); err=%v", res.Rows, g+2000, err)
	}
	res, err = st.Execute(ctx, store.Stmt{SQL: "SELECT state FROM agents_current WHERE machine_id = ? AND terminal = ?", Args: []store.Value{"m3", "w1:p1"}})
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != "done" {
		t.Fatalf("agent state = %v, want done (the stale one must not overwrite); err=%v", res.Rows, err)
	}
	res, err = st.Execute(ctx, store.Stmt{SQL: "SELECT COUNT(*) FROM agent_history WHERE machine_id = ? AND at_ms = ?", Args: []store.Value{"m3", g + 1000}})
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != int64(0) {
		t.Fatalf("stale history rows = %v, want 0 (the stale apply must not leave a transition); err=%v", res.Rows, err)
	}
}

// TestSQLDLargeMachineList: a list of 1200 machines is answered by
// the chunked queries: no statement exceeds the argument budget, the
// join counts each machine once, and the aggregates sum over the
// chunks.
func TestSQLDLargeMachineList(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const n = 1200
	const now int64 = 1793462400000 // on the hour
	lastHour := now - 3600000       // the last complete hour
	snap := "INSERT INTO inventory_snapshots (machine_id, generated_ms, received_ms, soho_version) VALUES (?, ?, ?, ?)"
	agent := "INSERT INTO agents_current (machine_id, terminal, project, session, name, role, kind, model, effort, state, reason, task, report_ready, started_ms, since_ms, observed_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	hidden := "INSERT INTO hidden_counts (machine_id, working, needs_you, done, idle, unknown, observed_ms) VALUES (?, ?, ?, ?, ?, ?, ?)"
	hourly := "INSERT INTO working_hourly (machine_id, hour_ms, max_working) VALUES (?, ?, ?)"
	stmts := make([]store.Stmt, 0, 4*n)
	for i := 0; i < n; i++ {
		id := "m" + itoa(i)
		stmts = append(stmts,
			store.Stmt{SQL: snap, Args: []store.Value{id, now - 3600000, now - int64(i), "v0.3.1"}},
			store.Stmt{SQL: agent, Args: []store.Value{id, "w1:p1", "acme/web", "w1", "build", "implementer", "cli-b", nil, nil, "working", nil, nil, int64(0), now - 7200000, nil, now - int64(i)}},
			store.Stmt{SQL: hidden, Args: []store.Value{id, int64(1), int64(0), int64(0), int64(0), int64(0), now - int64(i)}},
			store.Stmt{SQL: hourly, Args: []store.Value{id, lastHour, int64(1)}},
		)
	}
	if _, err := st.Batch(ctx, stmts); err != nil {
		t.Fatalf("seed the machines: %v", err)
	}
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		ids[i] = "m" + itoa(i)
	}
	agents, err := Agents(ctx, st, ids, now)
	if err != nil {
		t.Fatalf("Agents with %d machines: %v", n, err)
	}
	if len(agents.Agents) != n {
		t.Fatalf("agents = %d, want %d (each machine counted once)", len(agents.Agents), n)
	}
	for _, a := range agents.Agents {
		if !a.Online || a.State != inventory.StateWorking {
			t.Fatalf("agent = %+v, want online working", a)
		}
	}
	ov, err := Overview(ctx, st, ids, now)
	if err != nil {
		t.Fatalf("Overview with %d machines: %v", n, err)
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
	if len(ov.TopProjects) != 1 || ov.TopProjects[0].Project != "acme/web" || ov.TopProjects[0].Agents != n || ov.TopProjects[0].Working != n {
		t.Fatalf("top_projects = %+v, want the single project with %d agents", ov.TopProjects, n)
	}
}

// movingErrorWriter fails the wrapped batch on a statement after the
// guard and commits a newer version of the machine in the window
// surrounding the failed batch, against the real sqld.
type movingErrorWriter struct {
	store.Store
	machine string
	base    int64
	calls   int
}

func (m *movingErrorWriter) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	m.calls++
	// The unrelated writer commits in the window surrounding the
	// failed batch.
	bad := append(append([]store.Stmt{}, stmts...), store.Stmt{SQL: "INSERT INTO a_missing_table VALUES (1)"})
	res, err := m.Store.Batch(ctx, bad)
	if err != nil {
		other := mkInv(m.base+int64(m.calls)*1000, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil)))
		if r, aerr := Apply(ctx, m.Store, m.machine, other, other.GeneratedMS+10); aerr != nil || !r.Applied {
			return nil, fmt.Errorf("the other writer must commit in the window: applied=%v err=%v", r.Applied, aerr)
		}
	}
	return res, err
}

// alwaysMovingSQL commits a newer version of the machine before
// every batch the wrapped apply runs, so the guard fails on every
// attempt, against the real sqld.
type alwaysMovingSQL struct {
	store.Store
	machine string
	next    int64
	calls   int
}

func (a *alwaysMovingSQL) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	a.calls++
	other := mkInv(a.next, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil)))
	if r, err := Apply(ctx, a.Store, a.machine, other, other.GeneratedMS+10); err != nil || !r.Applied {
		return nil, fmt.Errorf("the other writer must commit in the window: applied=%v err=%v", r.Applied, err)
	}
	a.next += 1000
	return a.Store.Batch(ctx, stmts)
}

// TestSQLDKeepsTheOriginalErrorWhenTheGuardIsIntact: the
// classification against the real sqld. A common failure of a
// statement after the guard, with another apply moving the version in
// the window, comes back as the original error: no retry, no
// ErrConflict. The guard failing on every attempt still retries and
// conflicts on the third.
func TestSQLDKeepsTheOriginalErrorWhenTheGuardIsIntact(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const g int64 = 1793376000000

	// The moving error: the common failure after the guard, with
	// another apply moving the version in the window.
	if r, err := Apply(ctx, st, "m7", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10); err != nil || !r.Applied {
		t.Fatalf("seed m7: applied=%v err=%v", r.Applied, err)
	}
	w := &movingErrorWriter{Store: st, machine: "m7", base: g}
	bigger := mkInv(g+100000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil)))
	res, err := Apply(ctx, w, "m7", bigger, g+100010)
	if res.Applied {
		t.Fatal("the failed batch applied")
	}
	if err == nil {
		t.Fatal("the store error must come back")
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("a common error after the guard must not become a conflict: %v", err)
	}
	if w.calls != 1 {
		t.Fatalf("batch calls = %d, want 1 (no retry for a common error)", w.calls)
	}
	var se *store.StatementError
	if !errors.As(err, &se) {
		t.Fatalf("the original typed error is not preserved: %v", err)
	}

	// The conflict: the guard failing on every attempt retries and
	// conflicts on the third.
	if r, err := Apply(ctx, st, "m8", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10); err != nil || !r.Applied {
		t.Fatalf("seed m8: applied=%v err=%v", r.Applied, err)
	}
	am := &alwaysMovingSQL{Store: st, machine: "m8", next: g + 1000}
	res2, err2 := Apply(ctx, am, "m8", mkInv(g+200000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil))), g+200010)
	if !errors.Is(err2, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict after the guard fails three times", err2)
	}
	if res2.Applied {
		t.Fatal("the conflicting apply applied")
	}
	if am.calls != 3 {
		t.Fatalf("batch calls = %d, want 3 (one per attempt)", am.calls)
	}

	// The guard table is empty at the end.
	res3, err := st.Execute(ctx, store.Stmt{SQL: "SELECT COUNT(*) FROM ingest_guard"})
	if err != nil || len(res3.Rows) != 1 || res3.Rows[0][0] != int64(0) {
		t.Fatalf("ingest_guard rows = %v, want 0; err=%v", res3.Rows, err)
	}
}

// TestSQLDConcurrentFirstIngestOfANewMachine: against the real sqld,
// two applies race on the first ingest of a new machine: the insert
// of the loser changes no row on the collision and the guard fails;
// the loser retries, applies when it carries the newer version and
// returns Applied: false when it carries the equal one.
func TestSQLDConcurrentFirstIngestOfANewMachine(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const g int64 = 1793376000000
	type outcome struct {
		applied bool
		err     error
	}
	row := func(sql string, args ...store.Value) store.Row {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: sql, Args: args})
		if err != nil {
			t.Fatalf("Execute %s: %v", sql, err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("%s: rows = %v, want 1", sql, res.Rows)
		}
		return res.Rows[0]
	}
	// The equal inventory: the loser retries and returns Applied:
	// false, with no error.
	holdEq := &pauseRealBatch{Store: st, ready: make(chan struct{}), release: make(chan struct{})}
	doneEq := make(chan outcome, 1)
	go func() {
		r, err := Apply(ctx, holdEq, "m9", mkInv(g, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10)
		doneEq <- outcome{r.Applied, err}
	}()
	select {
	case <-holdEq.ready:
	case <-ctx.Done():
		t.Fatal("the held apply did not reach its batch")
	}
	if r, err := Apply(ctx, st, "m9", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+20); err != nil || !r.Applied {
		t.Fatalf("the first apply: applied=%v err=%v", r.Applied, err)
	}
	close(holdEq.release)
	out := <-doneEq
	if out.err != nil || out.applied {
		t.Fatalf("the equal apply after the collision: applied=%v err=%v, want false and no error", out.applied, out.err)
	}
	// The newer inventory: the loser retries and applies.
	holdNew := &pauseRealBatch{Store: st, ready: make(chan struct{}), release: make(chan struct{})}
	doneNew := make(chan outcome, 1)
	go func() {
		r, err := Apply(ctx, holdNew, "m10", mkInv(g+1000, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil))), g+1010)
		doneNew <- outcome{r.Applied, err}
	}()
	select {
	case <-holdNew.ready:
	case <-ctx.Done():
		t.Fatal("the held apply did not reach its batch")
	}
	if r, err := Apply(ctx, st, "m10", mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+20); err != nil || !r.Applied {
		t.Fatalf("the older apply: applied=%v err=%v", r.Applied, err)
	}
	close(holdNew.release)
	out = <-doneNew
	if out.err != nil || !out.applied {
		t.Fatalf("the newer apply after the collision: applied=%v err=%v, want applied", out.applied, out.err)
	}
	// The final states: the applied inventory of each machine.
	if s := row("SELECT generated_ms FROM inventory_snapshots WHERE machine_id = ?", "m9"); s[0] != g {
		t.Fatalf("snapshot of m9 = %v, want %d", s[0], g)
	}
	if s := row("SELECT generated_ms FROM inventory_snapshots WHERE machine_id = ?", "m10"); s[0] != g+1000 {
		t.Fatalf("snapshot of m10 = %v, want %d (the newer one wins)", s[0], g+1000)
	}
	if a := row("SELECT state FROM agents_current WHERE machine_id = ? AND terminal = ?", "m10", "w1:p1"); a[0] != "idle" {
		t.Fatalf("agent state = %v, want idle (the newer one wins)", a[0])
	}
	// The guard table is empty.
	if n := row("SELECT COUNT(*) FROM ingest_guard"); n[0] != int64(0) {
		t.Fatalf("ingest_guard rows = %v, want 0", n[0])
	}
}

// failingSnapshotWrite commits a newer version of the machine after
// every failed batch the wrapped apply runs, so a common failure of
// the guarded write keeps its original shape, against the real sqld.
type failingSnapshotWrite struct {
	store.Store
	machine string
	base    int64
	calls   int
}

func (w *failingSnapshotWrite) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	w.calls++
	res, err := w.Store.Batch(ctx, stmts)
	if err != nil {
		other := mkInv(w.base+int64(w.calls)*1000, inventory.Hidden{},
			mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "idle", nil, nil)))
		if r, aerr := Apply(ctx, w.Store, w.machine, other, other.GeneratedMS+10); aerr != nil || !r.Applied {
			return nil, fmt.Errorf("the other writer must commit in the window: applied=%v err=%v", r.Applied, aerr)
		}
	}
	return res, err
}

// TestSQLDKeepsTheOriginalErrorOnTheSnapshotWrite: the classification
// of a common failure of the guarded write against the real sqld. A
// trigger on the disposable database fails the ordinary write of the
// snapshot, the statement 1 of the apply batch, and after each
// rollback another apply commits a newer version of the machine: the
// error comes back as the original one, one attempt, no ErrConflict,
// and the version the other writer committed is intact. The trigger
// is a failure injection of the fixture, not a claim about production
// triggers.
func TestSQLDKeepsTheOriginalErrorOnTheSnapshotWrite(t *testing.T) {
	st := realIngestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const g int64 = 1793376000000
	const m = "m11"
	const version = "v0.3.9"
	if r, err := Apply(ctx, st, m, mkInv(g, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "working", nil, nil))), g+10); err != nil || !r.Applied {
		t.Fatalf("seed %s: applied=%v err=%v", m, r.Applied, err)
	}
	// The fixture fault trigger: the common failure of the ordinary
	// write, only for the target version.
	triggerSQL := fmt.Sprintf(
		"CREATE TRIGGER ingest_write_failure BEFORE UPDATE ON inventory_snapshots "+
			"WHEN NEW.machine_id = '%s' AND NEW.soho_version = '%s' "+
			"BEGIN SELECT RAISE(ABORT, 'fixture ordinary write failure'); END", m, version)
	if _, err := st.Execute(ctx, store.Stmt{SQL: triggerSQL}); err != nil {
		t.Fatalf("the fixture fault trigger: %v", err)
	}
	w := &failingSnapshotWrite{Store: st, machine: m, base: g}
	bigger := mkInv(g+100000, inventory.Hidden{},
		mkProject("acme/web", "w1", mkAgent("w1:p1", "build", "done", nil, nil)))
	bigger.SohoVersion = version
	res, err := Apply(ctx, w, m, bigger, g+100010)
	if res.Applied {
		t.Fatal("the failed batch applied")
	}
	if err == nil {
		t.Fatal("the store error must come back")
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("a common failure of the snapshot write must not become a conflict: %v", err)
	}
	if w.calls != 1 {
		t.Fatalf("batch calls = %d, want 1 (no retry for a common failure)", w.calls)
	}
	var se *store.StatementError
	if !errors.As(err, &se) {
		t.Fatalf("the original typed error is not preserved: %v", err)
	}
	if se.Index != 1 || se.Code != "SQLITE_CONSTRAINT" {
		t.Fatalf("statement = index %d code %q, want index 1 code SQLITE_CONSTRAINT", se.Index, se.Code)
	}
	// The other writer's version is intact: the failed batch rolled
	// back.
	row := func(sql string, args ...store.Value) store.Row {
		t.Helper()
		r, e := st.Execute(ctx, store.Stmt{SQL: sql, Args: args})
		if e != nil {
			t.Fatalf("Execute %s: %v", sql, e)
		}
		if len(r.Rows) != 1 {
			t.Fatalf("%s: rows = %v, want 1", sql, r.Rows)
		}
		return r.Rows[0]
	}
	if s := row("SELECT generated_ms, soho_version FROM inventory_snapshots WHERE machine_id = ?", m); s[0] != g+1000 || s[1] != "v0.3.1" {
		t.Fatalf("snapshot = %v, want %d v0.3.1 (the other writer)", s, g+1000)
	}
	if a := row("SELECT state FROM agents_current WHERE machine_id = ? AND terminal = ?", m, "w1:p1"); a[0] != "idle" {
		t.Fatalf("agent state = %v, want idle (the other writer)", a[0])
	}
	// The guard table is empty.
	if n := row("SELECT COUNT(*) FROM ingest_guard"); n[0] != int64(0) {
		t.Fatalf("ingest_guard rows = %v, want 0", n[0])
	}
}
