// Package inventory holds the schema 1 types of `soho inventory --json`
// and the strict reader for them. The contract lives in docs/inventory.md;
// the JSON tags below are that contract's field names, and Parse refuses
// anything the contract does not allow.
package inventory

// Schema is the only schema version this package knows. A field may be
// added without a new schema; removing or changing a field's meaning
// takes a new one.
const Schema = 1

// Inventory is one `soho inventory --json` document: the projects in
// show mode and the merged per-state counts of the hidden projects of
// one machine.
type Inventory struct {
	// Schema is the document's schema version; Parse accepts only 1.
	Schema int `json:"schema"`
	// SohoVersion is the version of the soho that printed the document.
	// It is required and never empty.
	SohoVersion string `json:"soho_version"`
	// GeneratedMS is when soho read the state, in UTC milliseconds.
	GeneratedMS int64 `json:"generated_ms"`
	// Projects lists the show-mode projects with at least one live agent.
	Projects []Project `json:"projects"`
	// Hidden counts the live agents per state over every hidden-mode
	// project of the machine; all five keys are always present.
	Hidden Hidden `json:"hidden"`
	// Truncated is true when soho kept only the first 500 agents.
	Truncated bool `json:"truncated,omitempty"`
}

// Project is one show-mode project: its display name and the sessions
// (state directories) of the project that have a live agent.
type Project struct {
	// Name is the display name, never a path or a remote URL.
	Name string `json:"name"`
	// Sessions groups the orchestrator with its workers per session.
	Sessions []Session `json:"sessions"`
}

// Session is one soho session of a project (the state directory
// .soho/<id>): an opaque id and the agents of the session.
type Session struct {
	// ID is the session id (today the terminal server's workspace id).
	ID string `json:"id"`
	// Agents holds the orchestrator of the session, then its workers
	// in roster order.
	Agents []Agent `json:"agents"`
}

// Agent is one live agent of a show-mode session. The fields the
// contract marks "or null" are pointers: nil is the JSON null.
type Agent struct {
	// Terminal is the local terminal id, never prefixed with a machine.
	Terminal string `json:"terminal"`
	// Name is the agent name (orchestrator, build, build-2, review).
	Name string `json:"name"`
	// Role is orchestrator, sub-orchestrator, or the worker's role.
	Role string `json:"role"`
	// Kind is one of soho's known kinds, shown as given.
	Kind string `json:"kind"`
	// Model is the model resolved at spawn, or null when the CLI's own
	// default applies.
	Model *string `json:"model"`
	// Effort is low, medium, high, xhigh or max after clamping, or null
	// when none was passed.
	Effort *string `json:"effort"`
	// State is the folded state of the agent.
	State State `json:"state"`
	// Reason is why the agent needs you; set only when State is
	// StateNeedsYou.
	Reason *Reason `json:"reason"`
	// Task is the task title, at most 120 code points, or null when
	// there is none.
	Task *string `json:"task"`
	// ReportReady is true when the worker's report for the current task
	// exists; it is required and always false for an orchestrator.
	ReportReady bool `json:"report_ready"`
	// StartedMS is when the agent was spawned (or, for an orchestrator,
	// when the session started), in UTC milliseconds.
	StartedMS int64 `json:"started_ms"`
	// SinceMS is when soho first saw the agent in its current state, or
	// null until soho tracks it.
	SinceMS *int64 `json:"since_ms"`
}

// Hidden counts the live agents per state over every hidden-mode project
// of the machine. All five keys are always present, at zero when there is
// none.
type Hidden struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needs_you"`
	Done     int `json:"done"`
	Idle     int `json:"idle"`
	Unknown  int `json:"unknown"`
}

// State is the folded state of one agent.
type State string

// The states of the contract's "States" table.
const (
	StateWorking  State = "working"
	StateNeedsYou State = "needs_you"
	StateDone     State = "done"
	StateIdle     State = "idle"
	StateUnknown  State = "unknown"
)

// Reason is why an agent is in StateNeedsYou.
type Reason string

// The reasons of the contract's "States" table.
const (
	ReasonQuestion    Reason = "question"
	ReasonApproval    Reason = "approval"
	ReasonQuota       Reason = "quota"
	ReasonProvider    Reason = "provider"
	ReasonNotReceived Reason = "not_received"
	ReasonNoReport    Reason = "no_report"
	ReasonCompact     Reason = "compact"
)

// Effort is the effort level clamped by soho at spawn.
type Effort string

// The effort levels of the contract's Agent table.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)
