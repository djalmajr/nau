// Package ingest writes the inventories of the machines into the hub:
// the current state of the agents, the state transitions as history
// (never the raw inventory), the hidden-project counts and the
// hourly maximum of working agents. It talks to the store contract
// only: no http, no os; it compiles for the wasip1 hub core.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// The millisecond constants of the ingest.
const (
	// hourMS is one hour.
	hourMS = 3600 * 1000
	// retentionHistory is the retention of the agent history: 7 days.
	retentionHistory = 7 * 24 * hourMS
	// retentionHourly is the retention of working_hourly: 48 hours.
	retentionHourly = 48 * hourMS
	// maxHistoryRows bounds the history of one agent.
	maxHistoryRows = 200
	// maxNeedsYou bounds the needs_you list of the overview.
	maxNeedsYou = 10
	// maxTopProjects bounds the projects of the overview.
	maxTopProjects = 5
	// hourlyBuckets is the number of entries of the 24 h working
	// series.
	hourlyBuckets = 24
	// maxApplyAttempts is the number of attempts of the guarded write
	// of the Apply: the first attempt plus the retries after a
	// conflict.
	maxApplyAttempts = 3
	// maxStatementArgs is the argument budget of one statement of the
	// queries: the machine ids of a chunk plus the other arguments of
	// the statement stay at most this number, far below the variable
	// limit of the database.
	maxStatementArgs = 500
	// guardCheckIndex is the statement number, one-based in the apply
	// batch, of the guard statement: its failure marks the conflict
	// window (a newer or equal inventory committed while the batch
	// ran). The guarded snapshot write fails no statement on a
	// collision: the UPDATE changes no row on a moved version and the
	// INSERT changes no row on a collided machine; only the guard, which
	// sees the changed rows, fails.
	guardCheckIndex = 2
)

// onlineWindowMS is the online window of the queries: a machine is
// online when the received_ms of its last inventory is at most this far
// behind nowMS. It is the default of the functions of the package.
var onlineWindowMS int64 = 60 * 1000

// ErrNotFound is the error of the Agent query when the hub holds no
// such agent.
var ErrNotFound = errors.New("ingest: agent not found")

// ErrConflict is the error of the Apply when its guard fails on the
// last attempt: a newer inventory of the machine committed inside the
// window of the write and the retries are spent.
var ErrConflict = errors.New("ingest: the inventory of the machine was superseded while applying")

// Result is the outcome of one Apply.
type Result struct {
	// Applied is true when the inventory changed the state of the
	// machine.
	Applied bool
}

// Agent is one current agent of the hub state, as the last inventory
// of its machine reported it, with the online flag of the machine.
type AgentRow struct {
	MachineID   string
	Terminal    string
	Project     string
	Session     string
	Name        string
	Role        string
	Kind        string
	Model       *string
	Effort      *string
	State       inventory.State
	Reason      *inventory.Reason
	Task        *string
	ReportReady bool
	StartedMS   int64
	SinceMS     *int64
	ObservedMS  int64
	// Online is true when the machine reported within the online
	// window.
	Online bool
}

// Hidden is the hidden counts of one machine.
type Hidden struct {
	MachineID string
	Counts    inventory.Hidden
}

// AgentsResult is the answer of Agents.
type AgentsResult struct {
	// Agents holds the current agents of the visible machines, with
	// the online flag.
	Agents []AgentRow
	// Hidden holds the hidden counts of the machines that have at
	// least one hidden agent.
	Hidden []Hidden
}

// HistoryRow is one transition of an agent.
type HistoryRow struct {
	State  inventory.State
	Reason *inventory.Reason
	AtMS   int64
}

// AgentDetail is the answer of Agent.
type AgentDetail struct {
	Agent   AgentRow
	History []HistoryRow
}

// Counts is the per-state count of the shown agents plus the hidden
// ones.
type Counts struct {
	Working  int64
	NeedsYou int64
	Done     int64
	Idle     int64
	Unknown  int64
}

// MachineCounts is the counts of one machine.
type MachineCounts struct {
	MachineID string
	Counts    Counts
}

// HourPoint is one hour of the working series.
type HourPoint struct {
	HourMS  int64
	Working int64
}

// TopProject is one project of the shown projects.
type TopProject struct {
	Project string
	Agents  int64
	Working int64
}

// Machines is the online/offline split of the visible machines.
type Machines struct {
	Online  int
	Offline int
}

// OverviewResult is the answer of Overview.
type OverviewResult struct {
	// Counts is the per-state count of the shown agents plus the
	// hidden ones, over the visible machines.
	Counts Counts
	// Machines splits the visible machines by online state.
	Machines Machines
	// NeedsYou holds at most 10 needs_you agents, the oldest
	// since_ms first.
	NeedsYou []AgentRow
	// PerMachine holds the counts of every visible machine, the
	// offline ones included.
	PerMachine []MachineCounts
	// Working24h holds exactly 24 hours, oldest first, the maximum
	// working summed over the visible machines per hour.
	Working24h []HourPoint
	// TopProjects holds at most 5 of the shown projects, by working
	// and then by total.
	TopProjects []TopProject
}

// The statements of the Apply. They are controlled text: the tests
// match them exactly.
const (
	// sqlLastGenerated reads the generated_ms of the last applied
	// inventory of the machine.
	sqlLastGenerated = "SELECT generated_ms FROM inventory_snapshots WHERE machine_id = ?"
	// sqlExistingAgents reads the current agents of the machine, for
	// the history diff.
	sqlExistingAgents = "SELECT terminal, state, reason FROM agents_current WHERE machine_id = ?"
	// sqlDeleteAgents drops the current agents of the machine.
	sqlDeleteAgents = "DELETE FROM agents_current WHERE machine_id = ?"
	// sqlInsertAgent writes one current agent.
	sqlInsertAgent = "INSERT INTO agents_current (machine_id, terminal, project, session, name, role, kind, model, effort, state, reason, task, report_ready, started_ms, since_ms, observed_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	// sqlInsertHistory writes one transition.
	sqlInsertHistory = "INSERT INTO agent_history (machine_id, terminal, project, name, state, reason, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?)"
	// sqlUpdateSnapshot is the guarded write of the snapshot of a
	// machine with a row: it changes exactly one row when the stored
	// version is still the one the call read.
	sqlUpdateSnapshot = "UPDATE inventory_snapshots SET generated_ms = ?, received_ms = ?, soho_version = ? WHERE machine_id = ? AND generated_ms = ?"
	// sqlInsertSnapshot is the guarded write of the snapshot of a
	// machine without a row: on a collision with another first ingest
	// it changes no row, and the guard fails on the changed rows.
	sqlInsertSnapshot = "INSERT INTO inventory_snapshots (machine_id, generated_ms, received_ms, soho_version) VALUES (?, ?, ?, ?) ON CONFLICT(machine_id) DO NOTHING"
	// sqlGuardCheck fails the batch when the guarded step changed no
	// row: the CHECK of the table rejects anything but 1.
	sqlGuardCheck = "INSERT INTO ingest_guard VALUES (changes())"
	// sqlGuardClear empties the guard table after the check.
	sqlGuardClear = "DELETE FROM ingest_guard"
	// sqlUpsertHidden writes or replaces the hidden counts.
	sqlUpsertHidden = "INSERT INTO hidden_counts (machine_id, working, needs_you, done, idle, unknown, observed_ms) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(machine_id) DO UPDATE SET working = excluded.working, needs_you = excluded.needs_you, done = excluded.done, idle = excluded.idle, unknown = excluded.unknown, observed_ms = excluded.observed_ms"
	// sqlUpsertHourly keeps, per hour, the maximum of working agents.
	sqlUpsertHourly = "INSERT INTO working_hourly (machine_id, hour_ms, max_working) VALUES (?, ?, ?) ON CONFLICT(machine_id, hour_ms) DO UPDATE SET max_working = MAX(excluded.max_working, working_hourly.max_working)"
	// sqlRetainHistory deletes the history older than the cutoff.
	sqlRetainHistory = "DELETE FROM agent_history WHERE at_ms < ?"
	// sqlRetainHourly deletes the hours older than the cutoff.
	sqlRetainHourly = "DELETE FROM working_hourly WHERE hour_ms < ?"
)

// agentColumns is the column list of the agents_current reads.
const agentColumns = "machine_id, terminal, project, session, name, role, kind, model, effort, state, reason, task, report_ready, started_ms, since_ms, observed_ms"

// The statements of the queries. The IN clause is always built from
// placeholders; the values go through Args, never through the text.
const (
	// sqlSnapshotIN reads the last received_ms of the machines.
	sqlSnapshotIN = "SELECT machine_id, received_ms FROM inventory_snapshots WHERE machine_id IN ("
	// sqlAgentsIN reads the current agents of the machines.
	sqlAgentsIN = "SELECT " + agentColumns + " FROM agents_current WHERE machine_id IN ("
	// sqlHiddenIN reads the hidden counts of the machines.
	sqlHiddenIN = "SELECT machine_id, working, needs_you, done, idle, unknown FROM hidden_counts WHERE machine_id IN ("
	// sqlAgentOne reads one current agent.
	sqlAgentOne = "SELECT " + agentColumns + " FROM agents_current WHERE machine_id = ? AND terminal = ?"
	// sqlHistory reads the history of an agent, newest first, the
	// last seven days only.
	sqlHistory = "SELECT state, reason, at_ms FROM agent_history WHERE machine_id = ? AND terminal = ? AND at_ms >= ? ORDER BY at_ms DESC LIMIT ?"
	// sqlHourlyIN reads the working hours of the machines since the
	// cutoff.
	sqlHourlyIN = "SELECT hour_ms, max_working FROM working_hourly WHERE machine_id IN ("
)

// Apply writes the inventory of one machine into the hub: the current
// state of the agents, the transitions, the hidden counts and the
// working hour. The inventory is the one already validated by
// inventory.Parse; receivedMS is when the hub received it.
//
// The order is guarded inside the batch: the snapshot write changes
// exactly one row only when the generated_ms the call read is still
// the current one and no other first ingest landed in the window, and
// the guard statement fails the batch when the guarded step changed no
// row. The check and the writes therefore count together, and an
// older inventory never lands on top of a newer one even when two
// applies race between the read and the write. When the guard fails
// because a newer or equal inventory committed in that window, Apply
// re-reads and retries, up to maxApplyAttempts attempts; after the
// last one it returns ErrConflict. A failure of any other statement,
// the failure of the guarded write itself included, is a store error
// and comes back as is on the first attempt, with no retry and no
// ErrConflict. An inventory not greater than the last applied one of
// the machine changes nothing and returns Applied: false, which makes
// the repeat idempotent and ignores the out-of-order ones.
func Apply(ctx context.Context, st store.Store, machineID string, inv inventory.Inventory, receivedMS int64) (Result, error) {
	for attempt := 0; attempt < maxApplyAttempts; attempt++ {
		// The order: read the last applied generated_ms of the
		// machine and refuse the non-newer inventories.
		res, err := st.Execute(ctx, store.Stmt{SQL: sqlLastGenerated, Args: []store.Value{machineID}})
		if err != nil {
			return Result{}, fmt.Errorf("ingest: read the snapshot: %w", err)
		}
		last, hasLast := int64(0), false
		if len(res.Rows) > 0 {
			g, ok := res.Rows[0][0].(int64)
			if !ok {
				return Result{}, errors.New("ingest: the snapshot generated_ms is not an integer")
			}
			if inv.GeneratedMS <= g {
				return Result{Applied: false}, nil
			}
			last, hasLast = g, true
		}
		// The history diff needs the current agents of the machine.
		res, err = st.Execute(ctx, store.Stmt{SQL: sqlExistingAgents, Args: []store.Value{machineID}})
		if err != nil {
			return Result{}, fmt.Errorf("ingest: read the current agents: %w", err)
		}
		existing := make(map[string]existingAgent, len(res.Rows))
		for _, row := range res.Rows {
			terminal, _ := row[0].(string)
			state, _ := row[1].(string)
			var reason *inventory.Reason
			if rs, ok := row[2].(string); ok {
				r := inventory.Reason(rs)
				reason = &r
			}
			existing[terminal] = existingAgent{state: state, reason: reason}
		}
		// One atomic batch: the guarded snapshot write and its guard
		// first, then the current state, the history, the hidden
		// counts and the working hour.
		stmts := make([]store.Stmt, 0, 10)
		if hasLast {
			stmts = append(stmts, store.Stmt{SQL: sqlUpdateSnapshot, Args: []store.Value{inv.GeneratedMS, receivedMS, inv.SohoVersion, machineID, last}})
		} else {
			stmts = append(stmts, store.Stmt{SQL: sqlInsertSnapshot, Args: []store.Value{machineID, inv.GeneratedMS, receivedMS, inv.SohoVersion}})
		}
		stmts = append(stmts,
			store.Stmt{SQL: sqlGuardCheck},
			store.Stmt{SQL: sqlGuardClear},
			store.Stmt{SQL: sqlDeleteAgents, Args: []store.Value{machineID}},
		)
		var working int64
		for _, p := range inv.Projects {
			for _, s := range p.Sessions {
				for _, a := range s.Agents {
					if a.State == inventory.StateWorking {
						working++
					}
					stmts = append(stmts, insertAgent(machineID, p.Name, s.ID, a, receivedMS))
					old, had := existing[a.Terminal]
					if !had || old.state != string(a.State) || !sameReason(old.reason, a.Reason) {
						atMS := inv.GeneratedMS
						if a.SinceMS != nil {
							atMS = *a.SinceMS
						}
						stmts = append(stmts, store.Stmt{
							SQL:  sqlInsertHistory,
							Args: []store.Value{machineID, a.Terminal, p.Name, a.Name, string(a.State), valueReason(a.Reason), atMS},
						})
					}
				}
			}
		}
		working += int64(inv.Hidden.Working)
		stmts = append(stmts,
			store.Stmt{SQL: sqlUpsertHidden, Args: []store.Value{
				machineID, int64(inv.Hidden.Working), int64(inv.Hidden.NeedsYou), int64(inv.Hidden.Done),
				int64(inv.Hidden.Idle), int64(inv.Hidden.Unknown), receivedMS,
			}},
			store.Stmt{SQL: sqlUpsertHourly, Args: []store.Value{machineID, hourOf(inv.GeneratedMS), working}},
		)
		if _, err := st.Batch(ctx, stmts); err != nil {
			// The batch failed. Only the guard failing is the conflict
			// signature: a newer or equal inventory committed in the
			// window, and the conflict re-reads and retries. Any other
			// failure, the failure of the guarded write itself included,
			// is a store error, returned as is on the first attempt, with
			// no retry and no ErrConflict.
			if !isGuardFailure(err) {
				return Result{}, fmt.Errorf("ingest: apply the inventory: %w", err)
			}
			cur, cerr := st.Execute(ctx, store.Stmt{SQL: sqlLastGenerated, Args: []store.Value{machineID}})
			if cerr != nil {
				return Result{}, fmt.Errorf("ingest: apply the inventory: %w", err)
			}
			moved := false
			if hasLast {
				if len(cur.Rows) == 0 {
					moved = true
				} else if v, ok := cur.Rows[0][0].(int64); ok {
					moved = v != last
				}
			} else if len(cur.Rows) > 0 {
				moved = true
			}
			if !moved {
				return Result{}, fmt.Errorf("ingest: apply the inventory: %w", err)
			}
			if attempt == maxApplyAttempts-1 {
				return Result{}, ErrConflict
			}
			continue
		}
		return Result{Applied: true}, nil
	}
	return Result{}, ErrConflict
}

// isGuardFailure reports whether the batch failure came from the
// guard statement: the only failure that means a newer or equal
// inventory committed while the batch ran. A failure of the guarded
// write itself is a common store error, never a conflict.
func isGuardFailure(err error) bool {
	var se *store.StatementError
	if !errors.As(err, &se) {
		return false
	}
	return se.Index == guardCheckIndex
}

// existingAgent is the diff entry of one current agent.
type existingAgent struct {
	state  string
	reason *inventory.Reason
}

// insertAgent builds the statement of one current agent.
func insertAgent(machineID, project, session string, a inventory.Agent, observedMS int64) store.Stmt {
	var ready int64
	if a.ReportReady {
		ready = 1
	}
	return store.Stmt{
		SQL: sqlInsertAgent,
		Args: []store.Value{
			machineID, a.Terminal, project, session, a.Name, a.Role, a.Kind,
			value(a.Model), value(a.Effort), string(a.State), valueReason(a.Reason), value(a.Task),
			ready, a.StartedMS, value64(a.SinceMS), observedMS,
		},
	}
}

// hourOf takes the start, in UTC, of the hour of the milliseconds.
func hourOf(ms int64) int64 { return ms - ms%hourMS }

// sameReason reports whether the stored reason and the reason of the
// agent agree, the nils included.
func sameReason(stored *inventory.Reason, a *inventory.Reason) bool {
	if stored == nil || a == nil {
		return stored == nil && a == nil
	}
	return *stored == *a
}

// Retain deletes the history older than 7 days and the working hours
// older than 48 h; nowMS is the reference.
func Retain(ctx context.Context, st store.Store, nowMS int64) error {
	_, err := st.Batch(ctx, []store.Stmt{
		{SQL: sqlRetainHistory, Args: []store.Value{nowMS - retentionHistory}},
		{SQL: sqlRetainHourly, Args: []store.Value{nowMS - retentionHourly}},
	})
	if err != nil {
		return fmt.Errorf("ingest: retain: %v", err)
	}
	return nil
}

// Agents returns the current agents of the visible machines, with the
// online flag, and the hidden counts of the machines that have at
// least one hidden agent. An empty machine list returns the empty
// answer without consulting the store.
func Agents(ctx context.Context, st store.Store, machineIDs []string, nowMS int64) (AgentsResult, error) {
	out := AgentsResult{Agents: []AgentRow{}, Hidden: []Hidden{}}
	if len(machineIDs) == 0 {
		return out, nil
	}
	online, err := onlineOf(ctx, st, machineIDs, nowMS)
	if err != nil {
		return AgentsResult{}, err
	}
	res, err := runIn(ctx, st, sqlAgentsIN, " ORDER BY machine_id, terminal", machineIDs, maxStatementArgs)
	if err != nil {
		return AgentsResult{}, fmt.Errorf("ingest: read the agents: %v", err)
	}
	for _, row := range res.Rows {
		out.Agents = append(out.Agents, agentFromRow(row, online))
	}
	res, err = runIn(ctx, st, sqlHiddenIN, " ORDER BY machine_id", machineIDs, maxStatementArgs)
	if err != nil {
		return AgentsResult{}, fmt.Errorf("ingest: read the hidden counts: %v", err)
	}
	for _, row := range res.Rows {
		mid, _ := row[0].(string)
		h := Hidden{MachineID: mid, Counts: hiddenFromRow(row)}
		if h.Counts.Working+h.Counts.NeedsYou+h.Counts.Done+h.Counts.Idle+h.Counts.Unknown > 0 {
			out.Hidden = append(out.Hidden, h)
		}
	}
	return out, nil
}

// Agent returns the current agent with its history, newest first, at
// most 200 rows of the last 7 days. It is ErrNotFound when the hub
// holds no such agent; a machine without a snapshot is offline.
func Agent(ctx context.Context, st store.Store, machineID, terminal string, nowMS int64) (AgentDetail, error) {
	res, err := st.Execute(ctx, store.Stmt{SQL: sqlAgentOne, Args: []store.Value{machineID, terminal}})
	if err != nil {
		return AgentDetail{}, fmt.Errorf("ingest: read the agent: %v", err)
	}
	if len(res.Rows) == 0 {
		return AgentDetail{}, ErrNotFound
	}
	agentRow := res.Rows[0]
	online, err := onlineOf(ctx, st, []string{machineID}, nowMS)
	if err != nil {
		return AgentDetail{}, err
	}
	res, err = st.Execute(ctx, store.Stmt{
		SQL:  sqlHistory,
		Args: []store.Value{machineID, terminal, nowMS - retentionHistory, int64(maxHistoryRows)},
	})
	if err != nil {
		return AgentDetail{}, fmt.Errorf("ingest: read the history: %v", err)
	}
	detail := AgentDetail{Agent: agentFromRow(agentRow, online), History: []HistoryRow{}}
	for _, row := range res.Rows {
		state, _ := row[0].(string)
		detail.History = append(detail.History, HistoryRow{
			State:  inventory.State(state),
			Reason: reasonPtr(row[1]),
			AtMS:   int64Value(row[2]),
		})
	}
	return detail, nil
}

// Overview returns the aggregates of the visible machines: the counts
// per state with the hidden ones, the online/offline split, the
// needs_you agents, the counts per machine, the 24 hours of working
// and the top projects. An empty machine list returns the empty
// answer without consulting the store.
func Overview(ctx context.Context, st store.Store, machineIDs []string, nowMS int64) (OverviewResult, error) {
	out := emptyOverview()
	if len(machineIDs) == 0 {
		return out, nil
	}
	online, err := onlineOf(ctx, st, machineIDs, nowMS)
	if err != nil {
		return OverviewResult{}, err
	}
	res, err := runIn(ctx, st, sqlAgentsIN, " ORDER BY machine_id, terminal", machineIDs, maxStatementArgs)
	if err != nil {
		return OverviewResult{}, fmt.Errorf("ingest: read the agents: %v", err)
	}
	agents := make([]AgentRow, 0, len(res.Rows))
	for _, row := range res.Rows {
		agents = append(agents, agentFromRow(row, online))
	}
	hiddenBy := make(map[string]inventory.Hidden)
	res, err = runIn(ctx, st, sqlHiddenIN, " ORDER BY machine_id", machineIDs, maxStatementArgs)
	if err != nil {
		return OverviewResult{}, fmt.Errorf("ingest: read the hidden counts: %v", err)
	}
	for _, row := range res.Rows {
		mid, _ := row[0].(string)
		hiddenBy[mid] = hiddenFromRow(row)
	}
	// The per-state counts of the shown agents, the needs_you list,
	// the per-machine and the per-project figures, all in one pass.
	needs := []AgentRow{}
	projects := make(map[string]*TopProject)
	perMachine := make(map[string]*Counts)
	for _, a := range agents {
		c := perMachine[a.MachineID]
		if c == nil {
			c = &Counts{}
			perMachine[a.MachineID] = c
		}
		addState(c, a.State)
		p := projects[a.Project]
		if p == nil {
			p = &TopProject{Project: a.Project}
			projects[a.Project] = p
		}
		p.Agents++
		if a.State == inventory.StateWorking {
			p.Working++
		}
		if a.State == inventory.StateNeedsYou {
			needs = append(needs, a)
		}
	}
	// The hidden counts join the totals, the machines and the
	// unknown state.
	for mid, h := range hiddenBy {
		c := perMachine[mid]
		if c == nil {
			c = &Counts{}
			perMachine[mid] = c
		}
		c.Working += int64(h.Working)
		c.NeedsYou += int64(h.NeedsYou)
		c.Done += int64(h.Done)
		c.Idle += int64(h.Idle)
		c.Unknown += int64(h.Unknown)
	}
	out.NeedsYou = needs
	sort.Slice(out.NeedsYou, func(i, j int) bool {
		return needsBefore(out.NeedsYou[i], out.NeedsYou[j])
	})
	if len(out.NeedsYou) > maxNeedsYou {
		out.NeedsYou = out.NeedsYou[:maxNeedsYou]
	}
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
	if len(out.TopProjects) > maxTopProjects {
		out.TopProjects = out.TopProjects[:maxTopProjects]
	}
	// The online/offline split and the per-machine list, every
	// visible machine included.
	visible := make(map[string]bool, len(machineIDs))
	for _, id := range machineIDs {
		visible[id] = true
	}
	for mid := range visible {
		if online[mid] {
			out.Machines.Online++
		} else {
			out.Machines.Offline++
		}
		c := perMachine[mid]
		if c == nil {
			c = &Counts{}
		}
		out.PerMachine = append(out.PerMachine, MachineCounts{MachineID: mid, Counts: *c})
	}
	sort.Slice(out.PerMachine, func(i, j int) bool {
		return out.PerMachine[i].MachineID < out.PerMachine[j].MachineID
	})
	// The totals join the shown and the hidden counts.
	for _, mc := range out.PerMachine {
		out.Counts.Working += mc.Counts.Working
		out.Counts.NeedsYou += mc.Counts.NeedsYou
		out.Counts.Done += mc.Counts.Done
		out.Counts.Idle += mc.Counts.Idle
		out.Counts.Unknown += mc.Counts.Unknown
	}
	// The 24 hours: exactly 24 entries, oldest first, the maximum
	// working summed over the visible machines per hour, zero where
	// there is no data. The entries end at the last complete hour. The
	// budget leaves one argument for the cutoff.
	curHour := hourOf(nowMS)
	res, err = runIn(ctx, st, sqlHourlyIN, " AND hour_ms >= ? ORDER BY hour_ms", machineIDs, maxStatementArgs-1, curHour-int64(hourlyBuckets)*hourMS)
	if err != nil {
		return OverviewResult{}, fmt.Errorf("ingest: read the working hours: %v", err)
	}
	sumBy := make(map[int64]int64, len(res.Rows))
	for _, row := range res.Rows {
		sumBy[int64Value(row[0])] += int64Value(row[1])
	}
	out.Working24h = make([]HourPoint, 0, hourlyBuckets)
	for i := 0; i < hourlyBuckets; i++ {
		h := curHour - int64(hourlyBuckets-i)*hourMS
		out.Working24h = append(out.Working24h, HourPoint{HourMS: h, Working: sumBy[h]})
	}
	return out, nil
}

// emptyOverview builds the OverviewResult of the empty machine list.
func emptyOverview() OverviewResult {
	return OverviewResult{
		NeedsYou:    []AgentRow{},
		PerMachine:  []MachineCounts{},
		Working24h:  make([]HourPoint, 0, hourlyBuckets),
		TopProjects: []TopProject{},
	}
}

// onlineOf marks the machines whose last inventory was received within
// the online window; a machine without a snapshot is offline.
func onlineOf(ctx context.Context, st store.Store, machineIDs []string, nowMS int64) (map[string]bool, error) {
	res, err := runIn(ctx, st, sqlSnapshotIN, " ORDER BY machine_id", machineIDs, maxStatementArgs)
	if err != nil {
		return nil, fmt.Errorf("ingest: read the snapshots: %v", err)
	}
	online := make(map[string]bool, len(res.Rows))
	for _, row := range res.Rows {
		mid, _ := row[0].(string)
		received, _ := row[1].(int64)
		online[mid] = nowMS-received <= onlineWindowMS
	}
	return online, nil
}

// agentFromRow converts a row of the agent columns to the Agent.
func agentFromRow(row store.Row, online map[string]bool) AgentRow {
	machineID, _ := row[0].(string)
	return AgentRow{
		MachineID:   machineID,
		Terminal:    strValue(row[1]),
		Project:     strValue(row[2]),
		Session:     strValue(row[3]),
		Name:        strValue(row[4]),
		Role:        strValue(row[5]),
		Kind:        strValue(row[6]),
		Model:       strPtrValue(row[7]),
		Effort:      strPtrValue(row[8]),
		State:       inventory.State(strValue(row[9])),
		Reason:      reasonPtr(row[10]),
		Task:        strPtrValue(row[11]),
		ReportReady: int64Value(row[12]) == 1,
		StartedMS:   int64Value(row[13]),
		SinceMS:     int64PtrValue(row[14]),
		ObservedMS:  int64Value(row[15]),
		Online:      online[machineID],
	}
}

// hiddenFromRow converts a row of the hidden columns to the counts.
func hiddenFromRow(row store.Row) inventory.Hidden {
	return inventory.Hidden{
		Working:  int(int64Value(row[1])),
		NeedsYou: int(int64Value(row[2])),
		Done:     int(int64Value(row[3])),
		Idle:     int(int64Value(row[4])),
		Unknown:  int(int64Value(row[5])),
	}
}

// addState adds one state to the counts.
func addState(c *Counts, s inventory.State) {
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

// needsBefore orders the needs_you list: the oldest since_ms first,
// the agents without since_ms after the ones with.
func needsBefore(a, b AgentRow) bool {
	as, bs := a.SinceMS != nil, b.SinceMS != nil
	if as != bs {
		return as
	}
	if !as {
		return false
	}
	return *a.SinceMS < *b.SinceMS
}

// inPlaceholders builds the "?" list of one IN clause for n entries,
// closed: the caller appends nothing but the following clause.
func inPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") + ")"
}

// runIn runs the IN query in chunks of the machine list and joins the
// rows: one statement per chunk, the ids of the chunk plus the extra
// arguments of the statement staying at most budget, and the join of
// the rows without counting a machine twice. The ids are deduplicated
// and sorted, so each machine falls in exactly one chunk and the
// joined rows keep the machine order of the statement.
func runIn(ctx context.Context, st store.Store, prefix, suffix string, machineIDs []string, budget int, extra ...store.Value) (store.Result, error) {
	var rows []store.Row
	for _, chunk := range inChunks(machineIDs, budget) {
		args := make([]store.Value, 0, len(chunk)+len(extra))
		args = append(args, valuesOf(chunk)...)
		args = append(args, extra...)
		res, err := st.Execute(ctx, store.Stmt{
			SQL:  prefix + inPlaceholders(len(chunk)) + suffix,
			Args: args,
		})
		if err != nil {
			return store.Result{}, err
		}
		rows = append(rows, res.Rows...)
	}
	return store.Result{Rows: rows}, nil
}

// inChunks deduplicates and sorts the machine ids and splits them
// into chunks of at most size ids.
func inChunks(ids []string, size int) [][]string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	var chunks [][]string
	for len(out) > 0 {
		n := size
		if len(out) < n {
			n = len(out)
		}
		chunks = append(chunks, out[:n])
		out = out[n:]
	}
	return chunks
}

// valuesOf builds the Args list of a machine id list.
func valuesOf(ids []string) []store.Value {
	out := make([]store.Value, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// value converts an optional string field to its store value.
func value(s *string) store.Value {
	if s == nil {
		return nil
	}
	return *s
}

// valueReason converts an optional reason field to its store value.
func valueReason(r *inventory.Reason) store.Value {
	if r == nil {
		return nil
	}
	return string(*r)
}

// value64 converts an optional millisecond field to its store value.
func value64(ms *int64) store.Value {
	if ms == nil {
		return nil
	}
	return *ms
}

// reasonPtr converts a store value to the optional reason.
func reasonPtr(v store.Value) *inventory.Reason {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	r := inventory.Reason(s)
	return &r
}

// strPtrValue converts a store value to the optional string.
func strPtrValue(v store.Value) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

// int64PtrValue converts a store value to the optional milliseconds.
func int64PtrValue(v store.Value) *int64 {
	n, ok := v.(int64)
	if !ok {
		return nil
	}
	return &n
}

// strValue converts a store value to the string.
func strValue(v store.Value) string {
	s, _ := v.(string)
	return s
}

// int64Value converts a store value to the int64.
func int64Value(v store.Value) int64 {
	n, _ := v.(int64)
	return n
}
