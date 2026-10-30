//go:build sqld

package ingest

import (
	"context"
	"errors"
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
	for _, name := range []string{
		"schema_migrations", "hub_meta", "inventory_snapshots", "agents_current",
		"agent_history", "hidden_counts", "working_hourly",
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
