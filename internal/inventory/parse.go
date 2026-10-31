package inventory

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const (
	// maxInputBytes is the largest document Parse accepts: 4 MiB.
	maxInputBytes = 4 << 20
	// maxTaskCodePoints is the contract's limit for a task title.
	maxTaskCodePoints = 120
	// maxAgents is the contract's limit for the whole document.
	maxAgents = 500
)

// Error is a refusal to parse an inventory document. Msg is a short
// English message that names the offending field and never repeats the
// value Parse found.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return e.Msg }

func refusal(msg string) *Error { return &Error{Msg: msg} }

// rawInventory is the wire shape of the document with pointers, so a
// missing key is distinguishable from a zero value.
type rawInventory struct {
	Schema      *int          `json:"schema"`
	SohoVersion *string       `json:"soho_version"`
	GeneratedMS *int64        `json:"generated_ms"`
	Projects    *[]rawProject `json:"projects"`
	Hidden      *rawHidden    `json:"hidden"`
	Truncated   *bool         `json:"truncated"`
}

type rawProject struct {
	Name     string        `json:"name"`
	Sessions *[]rawSession `json:"sessions"`
}

type rawSession struct {
	ID     string      `json:"id"`
	Agents *[]rawAgent `json:"agents"`
}

type rawAgent struct {
	Terminal    *string `json:"terminal"`
	Name        *string `json:"name"`
	Role        *string `json:"role"`
	Kind        *string `json:"kind"`
	Model       *string `json:"model"`
	Effort      *string `json:"effort"`
	State       *string `json:"state"`
	Reason      *Reason `json:"reason"`
	Task        *string `json:"task"`
	ReportReady *bool   `json:"report_ready"`
	StartedMS   *int64  `json:"started_ms"`
	SinceMS     *int64  `json:"since_ms"`
}

type rawHidden struct {
	Working  *int `json:"working"`
	NeedsYou *int `json:"needs_you"`
	Done     *int `json:"done"`
	Idle     *int `json:"idle"`
	Unknown  *int `json:"unknown"`
}

// Parse reads one inventory document and refuses anything the contract
// does not allow. Unknown fields are accepted and ignored at every
// level, because adding a field does not take a new schema.
func Parse(data []byte) (Inventory, error) {
	var inv Inventory
	if len(data) > maxInputBytes {
		return inv, refusal("input exceeds 4 MiB")
	}
	if !utf8.Valid(data) {
		return inv, refusal("invalid utf-8")
	}
	var raw rawInventory
	if err := json.Unmarshal(data, &raw); err != nil {
		return inv, refusal("invalid json")
	}
	if raw.Schema == nil {
		return inv, refusal("schema is missing")
	}
	if *raw.Schema != Schema {
		return inv, refusal("schema must be 1")
	}
	if raw.GeneratedMS == nil {
		return inv, refusal("generated_ms is missing")
	}
	if *raw.GeneratedMS < 0 {
		return inv, refusal("generated_ms is negative")
	}
	if raw.Projects == nil {
		return inv, refusal("projects is missing")
	}
	if raw.Hidden == nil {
		return inv, refusal("hidden is missing")
	}
	hidden, err := parseHidden(raw.Hidden)
	if err != nil {
		return inv, err
	}
	inv.Schema = Schema
	inv.GeneratedMS = *raw.GeneratedMS
	inv.Hidden = hidden
	inv.Truncated = raw.Truncated != nil && *raw.Truncated
	if raw.SohoVersion == nil || *raw.SohoVersion == "" {
		return inv, refusal("soho_version is missing")
	}
	if err := noControl("soho_version", *raw.SohoVersion); err != nil {
		return inv, err
	}
	inv.SohoVersion = *raw.SohoVersion
	// The agent limit applies to the whole document, so count before
	// validating the agents.
	count := 0
	for i := range *raw.Projects {
		sessions := (*raw.Projects)[i].Sessions
		if sessions == nil {
			continue
		}
		for j := range *sessions {
			if agents := (*sessions)[j].Agents; agents != nil {
				count += len(*agents)
			}
		}
	}
	if count > maxAgents {
		return inv, refusal("more than 500 agents")
	}
	inv.Projects = make([]Project, 0, len(*raw.Projects))
	agentIndex := 0
	for i := range *raw.Projects {
		p := &(*raw.Projects)[i]
		if p.Name == "" {
			return inv, refusal(fmt.Sprintf("projects[%d].name is missing or empty", i))
		}
		if err := noControl(fmt.Sprintf("projects[%d].name", i), p.Name); err != nil {
			return inv, err
		}
		if p.Sessions == nil || len(*p.Sessions) == 0 {
			return inv, refusal(fmt.Sprintf("projects[%d].sessions is missing or empty", i))
		}
		project := Project{Name: p.Name, Sessions: make([]Session, 0, len(*p.Sessions))}
		for j := range *p.Sessions {
			s := &(*p.Sessions)[j]
			if s.ID == "" {
				return inv, refusal(fmt.Sprintf("projects[%d].sessions[%d].id is missing or empty", i, j))
			}
			if err := noControl(fmt.Sprintf("projects[%d].sessions[%d].id", i, j), s.ID); err != nil {
				return inv, err
			}
			if s.Agents == nil || len(*s.Agents) == 0 {
				return inv, refusal(fmt.Sprintf("projects[%d].sessions[%d].agents is missing or empty", i, j))
			}
			session := Session{ID: s.ID, Agents: make([]Agent, 0, len(*s.Agents))}
			for k := range *s.Agents {
				agent, err := parseAgent(&(*s.Agents)[k], agentIndex)
				if err != nil {
					return inv, err
				}
				session.Agents = append(session.Agents, agent)
				agentIndex++
			}
			project.Sessions = append(project.Sessions, session)
		}
		inv.Projects = append(inv.Projects, project)
	}
	return inv, nil
}

// parseHidden reads the five always-present hidden counts.
func parseHidden(h *rawHidden) (Hidden, error) {
	var out Hidden
	fields := []struct {
		name string
		in   *int
		out  *int
	}{
		{"working", h.Working, &out.Working},
		{"needs_you", h.NeedsYou, &out.NeedsYou},
		{"done", h.Done, &out.Done},
		{"idle", h.Idle, &out.Idle},
		{"unknown", h.Unknown, &out.Unknown},
	}
	for _, f := range fields {
		if f.in == nil {
			return out, refusal("hidden." + f.name + " is missing")
		}
		if *f.in < 0 {
			return out, refusal("hidden." + f.name + " is negative")
		}
		*f.out = *f.in
	}
	return out, nil
}

// parseAgent validates one agent; index is its position in project,
// session and roster order, for the error message only. The presence
// checks run first, then the control-character check on every string,
// then the semantic checks, so a control character is named before a
// value that also breaks a semantic rule.
func parseAgent(a *rawAgent, index int) (Agent, error) {
	prefix := fmt.Sprintf("agents[%d].", index)
	var out Agent
	if a.Terminal == nil {
		return out, refusal(prefix + "terminal is missing")
	}
	out.Terminal = *a.Terminal
	if a.Name == nil {
		return out, refusal(prefix + "name is missing")
	}
	out.Name = *a.Name
	if a.Role == nil {
		return out, refusal(prefix + "role is missing")
	}
	out.Role = *a.Role
	if a.Kind == nil {
		return out, refusal(prefix + "kind is missing")
	}
	out.Kind = *a.Kind
	if a.State == nil {
		return out, refusal(prefix + "state is missing")
	}
	if a.StartedMS == nil {
		return out, refusal(prefix + "started_ms is missing")
	}
	if a.ReportReady == nil {
		return out, refusal(prefix + "report_ready is missing or null")
	}
	out.ReportReady = *a.ReportReady
	for _, f := range []struct {
		name  string
		value *string
	}{
		{"terminal", a.Terminal},
		{"name", a.Name},
		{"role", a.Role},
		{"kind", a.Kind},
		{"model", a.Model},
		{"effort", a.Effort},
		{"task", a.Task},
	} {
		if f.value != nil && hasControlChars(*f.value) {
			return out, refusal(prefix + f.name + " contains a control character")
		}
	}
	if !knownState(State(*a.State)) {
		return out, refusal(prefix + "state is not a known state")
	}
	out.State = State(*a.State)
	if out.State == StateNeedsYou {
		if a.Reason == nil {
			return out, refusal(prefix + "reason is required for state needs_you")
		}
		if !knownReason(*a.Reason) {
			return out, refusal(prefix + "reason is not a known reason")
		}
	} else if a.Reason != nil {
		return out, refusal(prefix + "reason is only allowed for state needs_you")
	}
	out.Reason = a.Reason
	if *a.StartedMS < 0 {
		return out, refusal(prefix + "started_ms is negative")
	}
	out.StartedMS = *a.StartedMS
	if out.ReportReady && (out.Role == "orchestrator" || out.Role == "sub-orchestrator") {
		return out, refusal(prefix + "report_ready must be false for an orchestrator")
	}
	if a.SinceMS != nil && *a.SinceMS < 0 {
		return out, refusal(prefix + "since_ms is negative")
	}
	out.SinceMS = a.SinceMS
	if a.Task != nil {
		if utf8.RuneCountInString(*a.Task) > maxTaskCodePoints {
			return out, refusal(prefix + "task exceeds 120 code points")
		}
		out.Task = a.Task
	}
	if a.Effort != nil && !knownEffort(Effort(*a.Effort)) {
		return out, refusal(prefix + "effort is not a known effort")
	}
	out.Model = a.Model
	out.Effort = a.Effort
	return out, nil
}

func knownState(s State) bool {
	switch s {
	case StateWorking, StateNeedsYou, StateDone, StateIdle, StateUnknown:
		return true
	}
	return false
}

func knownReason(r Reason) bool {
	switch r {
	case ReasonQuestion, ReasonApproval, ReasonQuota, ReasonProvider, ReasonNotReceived, ReasonNoReport, ReasonCompact, ReasonOwner:
		return true
	}
	return false
}

func knownEffort(e Effort) bool {
	switch e {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

func noControl(field, value string) error {
	if hasControlChars(value) {
		return refusal(field + " contains a control character")
	}
	return nil
}

// hasControlChars reports whether s holds a control character:
// U+0000-U+001F or U+007F-U+009F.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}
