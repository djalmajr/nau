package machines

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/visibility"
	"github.com/djalmajr/nau/internal/inventory"
)

// The read answers of the interface. The shapes follow the contract
// of docs/hub-api.md: the agent of the last inventory of its machine,
// with who and where; the counts always with the five keys.

// machineRef is the machine of the read answers.
type machineRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// countsRef is the per-state count of the read answers.
type countsRef struct {
	Working  int64 `json:"working"`
	NeedsYou int64 `json:"needs_you"`
	Done     int64 `json:"done"`
	Idle     int64 `json:"idle"`
	Unknown  int64 `json:"unknown"`
}

func countsOf(c ingest.Counts) countsRef {
	return countsRef{Working: c.Working, NeedsYou: c.NeedsYou, Done: c.Done, Idle: c.Idle, Unknown: c.Unknown}
}

func hiddenCountsOf(h inventory.Hidden) countsRef {
	return countsRef{Working: int64(h.Working), NeedsYou: int64(h.NeedsYou), Done: int64(h.Done), Idle: int64(h.Idle), Unknown: int64(h.Unknown)}
}

// agentItem is the agent of the read answers.
type agentItem struct {
	ID          string            `json:"id"`
	Machine     machineRef        `json:"machine"`
	Developer   developerRef      `json:"developer"`
	Project     string            `json:"project"`
	Session     string            `json:"session"`
	Terminal    string            `json:"terminal"`
	Name        string            `json:"name"`
	Role        string            `json:"role"`
	Kind        string            `json:"kind"`
	Model       *string           `json:"model"`
	Effort      *string           `json:"effort"`
	State       inventory.State   `json:"state"`
	Reason      *inventory.Reason `json:"reason"`
	Task        *string           `json:"task"`
	ReportReady bool              `json:"report_ready"`
	StartedMS   int64             `json:"started_ms"`
	SinceMS     *int64            `json:"since_ms"`
	ObservedMS  int64             `json:"observed_ms"`
}

// historyItem is one transition of the agent detail.
type historyItem struct {
	State  inventory.State   `json:"state"`
	Reason *inventory.Reason `json:"reason"`
	AtMS   int64             `json:"at_ms"`
}

// visibleMachines resolves the visible machines of the request and
// maps them by id, or the 503 when the store fails.
func (m *Service) visibleMachines(ctx context.Context, developerID string, nowMS int64) (map[string]visibility.Machine, []string, hub.Response) {
	list, err := m.sharing.VisibleMachines(ctx, developerID, nowMS)
	if err != nil {
		return nil, nil, unavailable()
	}
	byID := make(map[string]visibility.Machine, len(list))
	ids := make([]string, 0, len(list))
	for _, mc := range list {
		byID[mc.ID] = mc
		ids = append(ids, mc.ID)
	}
	return byID, ids, hub.Response{}
}

// agentOf builds the read agent from the ingest row: the id is the
// machine and the terminal, and the machine and the developer come
// from the visibility of the request.
func agentOf(byID map[string]visibility.Machine, a ingest.AgentRow) agentItem {
	mc := byID[a.MachineID]
	return agentItem{
		ID:          a.MachineID + "." + a.Terminal,
		Machine:     machineRef{ID: a.MachineID, Name: mc.Name, Online: a.Online},
		Developer:   developerRef{ID: mc.Developer.ID, Name: mc.Developer.Name},
		Project:     a.Project,
		Session:     a.Session,
		Terminal:    a.Terminal,
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
		ObservedMS:  a.ObservedMS,
	}
}

// listAgents is the GET /api/agents route (session required): the
// current agents of the visible machines, with the hidden counts of
// the machines that have them, the optional filters applied after the
// visibility.
func (m *Service) listAgents(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	nowMS := m.nowMS()
	byID, ids, resp := m.visibleMachines(ctx, u.ID, nowMS)
	if resp.Status != 0 {
		return resp
	}
	res := ingest.AgentsResult{Agents: []ingest.AgentRow{}, Hidden: []ingest.Hidden{}}
	if len(ids) > 0 {
		var err error
		res, err = m.agents(ctx, m.store, ids, nowMS)
		if err != nil {
			return unavailable()
		}
	}
	developer := req.Query.Get("developer")
	machine := req.Query.Get("machine")
	project := req.Query.Get("project")
	agents := make([]agentItem, 0, len(res.Agents))
	for _, a := range res.Agents {
		if machine != "" && a.MachineID != machine {
			continue
		}
		if developer != "" && byID[a.MachineID].Developer.ID != developer {
			continue
		}
		if project != "" && a.Project != project {
			continue
		}
		agents = append(agents, agentOf(byID, a))
	}
	hidden := make([]struct {
		Machine machineRef `json:"machine"`
		Counts  countsRef  `json:"counts"`
	}, 0, len(res.Hidden))
	for _, h := range res.Hidden {
		if machine != "" && h.MachineID != machine {
			continue
		}
		if developer != "" && byID[h.MachineID].Developer.ID != developer {
			continue
		}
		mc := byID[h.MachineID]
		hidden = append(hidden, struct {
			Machine machineRef `json:"machine"`
			Counts  countsRef  `json:"counts"`
		}{
			Machine: machineRef{ID: h.MachineID, Name: mc.Name, Online: mc.Online},
			Counts:  hiddenCountsOf(h.Counts),
		})
	}
	out, _ := json.Marshal(struct {
		GeneratedMS int64       `json:"generated_ms"`
		Agents      []agentItem `json:"agents"`
		Hidden      []any       `json:"hidden"`
	}{
		GeneratedMS: nowMS,
		Agents:      agents,
		Hidden:      anySliceOf(hidden),
	})
	return hub.Response{Status: 200, Body: out}
}

// getAgent is the GET /api/agents/{id} route (session required): the
// agent with its history. The 404 is the same for an agent that does
// not exist and an agent of a machine the user may not see, so the
// existence of the agent is not revealed.
func (m *Service) getAgent(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	nowMS := m.nowMS()
	byID, _, resp := m.visibleMachines(ctx, u.ID, nowMS)
	if resp.Status != 0 {
		return resp
	}
	machineID, terminal, ok := strings.Cut(req.PathParam("id"), ".")
	if !ok {
		return errResponse(404, "not_found")
	}
	if _, seen := byID[machineID]; !seen {
		return errResponse(404, "not_found")
	}
	detail, err := m.agent(ctx, m.store, machineID, terminal, nowMS)
	if errors.Is(err, ingest.ErrNotFound) {
		return errResponse(404, "not_found")
	}
	if err != nil {
		return unavailable()
	}
	history := make([]historyItem, 0, len(detail.History))
	for _, h := range detail.History {
		history = append(history, historyItem{State: h.State, Reason: h.Reason, AtMS: h.AtMS})
	}
	item := agentOf(byID, detail.Agent)
	out, _ := json.Marshal(struct {
		Agent   agentItem     `json:"agent"`
		History []historyItem `json:"history"`
	}{Agent: item, History: history})
	return hub.Response{Status: 200, Body: out}
}

// The hour of the working series of the contract, in milliseconds.
const hourMS = 60 * 60 * 1000

// getOverview is the GET /api/overview route (session required): the
// aggregates of the visible machines, the offline ones included. The
// working series keeps the 24 entries of the contract also when the
// user has no visible machine.
func (m *Service) getOverview(ctx context.Context, req hub.Request) hub.Response {
	u, ok := m.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	nowMS := m.nowMS()
	byID, ids, resp := m.visibleMachines(ctx, u.ID, nowMS)
	if resp.Status != 0 {
		return resp
	}
	ov := ingest.OverviewResult{
		NeedsYou:    []ingest.AgentRow{},
		PerMachine:  []ingest.MachineCounts{},
		Working24h:  []ingest.HourPoint{},
		TopProjects: []ingest.TopProject{},
	}
	if len(ids) > 0 {
		var err error
		ov, err = m.overview(ctx, m.store, ids, nowMS)
		if err != nil {
			return unavailable()
		}
	}
	if len(ov.Working24h) == 0 {
		// The contract holds 24 entries, oldest first: fill the
		// empty answer with the zero hours.
		curHour := nowMS - nowMS%hourMS
		for i := 23; i >= 0; i-- {
			ov.Working24h = append(ov.Working24h, ingest.HourPoint{HourMS: curHour - int64(i)*hourMS, Working: 0})
		}
	}
	needs := make([]agentItem, 0, len(ov.NeedsYou))
	for _, a := range ov.NeedsYou {
		needs = append(needs, agentOf(byID, a))
	}
	perMachine := make([]struct {
		Machine   machineRef   `json:"machine"`
		Developer developerRef `json:"developer"`
		Counts    countsRef    `json:"counts"`
	}, 0, len(ov.PerMachine))
	for _, pm := range ov.PerMachine {
		mc := byID[pm.MachineID]
		perMachine = append(perMachine, struct {
			Machine   machineRef   `json:"machine"`
			Developer developerRef `json:"developer"`
			Counts    countsRef    `json:"counts"`
		}{
			Machine:   machineRef{ID: pm.MachineID, Name: mc.Name, Online: mc.Online},
			Developer: developerRef{ID: mc.Developer.ID, Name: mc.Developer.Name},
			Counts:    countsOf(pm.Counts),
		})
	}
	working := make([]struct {
		HourMS  int64 `json:"hour_ms"`
		Working int64 `json:"working"`
	}, 0, len(ov.Working24h))
	for _, h := range ov.Working24h {
		working = append(working, struct {
			HourMS  int64 `json:"hour_ms"`
			Working int64 `json:"working"`
		}{HourMS: h.HourMS, Working: h.Working})
	}
	top := make([]struct {
		Project string `json:"project"`
		Agents  int64  `json:"agents"`
		Working int64  `json:"working"`
	}, 0, len(ov.TopProjects))
	for _, p := range ov.TopProjects {
		top = append(top, struct {
			Project string `json:"project"`
			Agents  int64  `json:"agents"`
			Working int64  `json:"working"`
		}{Project: p.Project, Agents: p.Agents, Working: p.Working})
	}
	developers := make(map[string]bool, len(byID))
	for _, mc := range byID {
		developers[mc.Developer.ID] = true
	}
	out, _ := json.Marshal(struct {
		GeneratedMS int64     `json:"generated_ms"`
		Counts      countsRef `json:"counts"`
		Machines    struct {
			Online  int `json:"online"`
			Offline int `json:"offline"`
		} `json:"machines"`
		Developers  int         `json:"developers"`
		NeedsYou    []agentItem `json:"needs_you"`
		PerMachine  []any       `json:"per_machine"`
		Working24h  []any       `json:"working_24h"`
		TopProjects []any       `json:"top_projects"`
	}{
		GeneratedMS: nowMS,
		Counts:      countsOf(ov.Counts),
		Machines: struct {
			Online  int `json:"online"`
			Offline int `json:"offline"`
		}{Online: ov.Machines.Online, Offline: ov.Machines.Offline},
		Developers:  len(developers),
		NeedsYou:    needs,
		PerMachine:  anySliceOf(perMachine),
		Working24h:  anySliceOf(working),
		TopProjects: anySliceOf(top),
	})
	return hub.Response{Status: 200, Body: out}
}

// anySliceOf converts a slice of anonymous items to the any slice of
// the JSON answers (an empty slice marshals as [], not null).
func anySliceOf[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}
