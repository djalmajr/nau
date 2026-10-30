package inventory_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/nau/internal/inventory"
)

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

var validFixtures = []string{
	"show-every-state.json",
	"hidden-only.json",
	"none.json",
	"mixed.json",
}

// TestParseAcceptsValidFixtures: every valid fixture parses, and the
// main one matches the contract's example (every state, every reason,
// every null field, the hidden counts).
func TestParseAcceptsValidFixtures(t *testing.T) {
	for _, name := range validFixtures {
		data := readFixture(t, "testdata", name)
		if _, err := inventory.Parse(data); err != nil {
			t.Errorf("%s: Parse: %v", name, err)
		}
	}

	data := readFixture(t, "testdata", "show-every-state.json")
	inv, err := inventory.Parse(data)
	if err != nil {
		t.Fatalf("show-every-state.json: Parse: %v", err)
	}
	if inv.Schema != inventory.Schema {
		t.Errorf("schema = %d, want %d", inv.Schema, inventory.Schema)
	}
	if inv.GeneratedMS != 1793376000000 {
		t.Errorf("generated_ms = %d, want 1793376000000", inv.GeneratedMS)
	}
	if len(inv.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(inv.Projects))
	}
	project := inv.Projects[0]
	if project.Name != "acme/web" {
		t.Errorf("projects[0].name = %q, want acme/web", project.Name)
	}
	if len(project.Sessions) != 1 || project.Sessions[0].ID != "w1" {
		t.Fatalf("sessions = %+v, want one session w1", project.Sessions)
	}
	agents := project.Sessions[0].Agents
	if len(agents) != 12 {
		t.Fatalf("agents = %d, want 12", len(agents))
	}
	var (
		seenStates  = map[inventory.State]int{}
		seenReasons = map[inventory.Reason]int{}
	)
	for i, agent := range agents {
		seenStates[agent.State]++
		if agent.Reason != nil {
			seenReasons[*agent.Reason]++
		}
		if agent.Terminal == "" || agent.Name == "" || agent.Role == "" || agent.Kind == "" {
			t.Errorf("agent %d: terminal/name/role/kind must be non-empty, got %+v", i, agent)
		}
		switch agent.State {
		case inventory.StateNeedsYou:
			if agent.Reason == nil {
				t.Errorf("agent %d: state needs_you needs a reason", i)
			}
		default:
			if agent.Reason != nil {
				t.Errorf("agent %d: reason %q on state %s", i, *agent.Reason, agent.State)
			}
		}
	}
	for _, s := range []inventory.State{
		inventory.StateWorking,
		inventory.StateNeedsYou,
		inventory.StateDone,
		inventory.StateIdle,
		inventory.StateUnknown,
	} {
		if seenStates[s] == 0 {
			t.Errorf("state %s appears in no agent of the fixture", s)
		}
	}
	for _, r := range []inventory.Reason{
		inventory.ReasonQuestion,
		inventory.ReasonApproval,
		inventory.ReasonQuota,
		inventory.ReasonProvider,
		inventory.ReasonNotReceived,
		inventory.ReasonNoReport,
		inventory.ReasonCompact,
	} {
		if seenReasons[r] != 1 {
			t.Errorf("reason %s appears %d times, want 1", r, seenReasons[r])
		}
	}
	last := agents[11]
	if last.Model != nil || last.Effort != nil || last.Task != nil || last.SinceMS != nil {
		t.Errorf("agent 11: model/effort/task/since_ms = %v/%v/%v/%v, want all nil", last.Model, last.Effort, last.Task, last.SinceMS)
	}
	done := agents[9]
	if !done.ReportReady {
		t.Errorf("agent 9: report_ready = false, want true")
	}
	want := inventory.Hidden{Working: 1, NeedsYou: 0, Done: 0, Idle: 2}
	if inv.Hidden != want {
		t.Errorf("hidden = %+v, want %+v", inv.Hidden, want)
	}
}

// TestParseIgnoresUnknownFields: the mixed fixture carries unknown
// fields at the top level and on one agent; Parse keeps what the
// contract names and drops the rest.
func TestParseIgnoresUnknownFields(t *testing.T) {
	inv, err := inventory.Parse(readFixture(t, "testdata", "mixed.json"))
	if err != nil {
		t.Fatalf("mixed.json: Parse: %v", err)
	}
	if len(inv.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(inv.Projects))
	}
	if len(inv.Projects[0].Sessions) != 2 {
		t.Fatalf("project 0 sessions = %d, want 2", len(inv.Projects[0].Sessions))
	}
	if got := inv.Projects[0].Sessions[0].Agents[1]; got.Reason == nil || *got.Reason != inventory.ReasonApproval {
		t.Errorf("agent build: reason = %v, want approval", got.Reason)
	}
}

var invalidFixtures = []struct {
	file string
	want string
}{
	{"invalid/invalid-json.json", "invalid json"},
	{"invalid/invalid-utf8.json", "invalid utf-8"},
	{"invalid/schema-missing.json", "schema"},
	{"invalid/schema-not-one.json", "schema"},
	{"invalid/soho-version-missing.json", "soho_version"},
	{"invalid/generated-ms-missing.json", "generated_ms"},
	{"invalid/projects-missing.json", "projects"},
	{"invalid/project-name-missing.json", "projects[0].name"},
	{"invalid/project-name-empty.json", "projects[0].name"},
	{"invalid/project-sessions-missing.json", "projects[0].sessions"},
	{"invalid/project-sessions-null.json", "projects[0].sessions"},
	{"invalid/project-sessions-empty.json", "projects[0].sessions"},
	{"invalid/session-id-missing.json", "sessions[0].id"},
	{"invalid/session-id-empty.json", "sessions[0].id"},
	{"invalid/session-agents-missing.json", "sessions[0].agents"},
	{"invalid/session-agents-null.json", "sessions[0].agents"},
	{"invalid/session-agents-empty.json", "sessions[0].agents"},
	{"invalid/hidden-missing.json", "hidden"},
	{"invalid/hidden-key-missing.json", "hidden.idle"},
	{"invalid/hidden-count-negative.json", "hidden.idle"},
	{"invalid/agent-terminal-missing.json", "terminal"},
	{"invalid/agent-name-missing.json", "name"},
	{"invalid/agent-role-missing.json", "role"},
	{"invalid/agent-kind-missing.json", "kind"},
	{"invalid/agent-state-missing.json", "state"},
	{"invalid/agent-started-ms-missing.json", "started_ms"},
	{"invalid/state-out-of-list.json", "state"},
	{"invalid/reason-without-needs-you.json", "reason"},
	{"invalid/reason-missing-for-needs-you.json", "reason"},
	{"invalid/reason-out-of-list.json", "reason"},
	{"invalid/report-ready-missing.json", "report_ready"},
	{"invalid/report-ready-null.json", "report_ready"},
	{"invalid/orchestrator-report-ready-true.json", "report_ready"},
	{"invalid/sub-orchestrator-report-ready-true.json", "report_ready"},
	{"invalid/effort-out-of-list.json", "effort"},
	{"invalid/task-too-long.json", "task"},
	{"invalid/control-character.json", "name"},
	{"invalid/generated-ms-negative.json", "generated_ms"},
	{"invalid/started-ms-negative.json", "started_ms"},
	{"invalid/since-ms-negative.json", "since_ms"},
}

// TestParseRejectsInvalidFixtures: every invalid fixture is refused,
// with a *inventory.Error whose message names the offending rule.
func TestParseRejectsInvalidFixtures(t *testing.T) {
	for _, tc := range invalidFixtures {
		t.Run(tc.file, func(t *testing.T) {
			data := readFixture(t, "testdata", tc.file)
			_, err := inventory.Parse(data)
			if err == nil {
				t.Fatalf("%s: Parse accepted the document", tc.file)
			}
			var perr *inventory.Error
			if !errors.As(err, &perr) {
				t.Fatalf("%s: error = %v, want *inventory.Error", tc.file, err)
			}
			if !strings.Contains(perr.Msg, tc.want) {
				t.Errorf("%s: message %q does not name %q", tc.file, perr.Msg, tc.want)
			}
		})
	}
}

// TestParseRoundTrip: Parse then Marshal then Parse gives back the
// same value, so the struct is the contract's JSON shape.
func TestParseRoundTrip(t *testing.T) {
	data := readFixture(t, "testdata", "show-every-state.json")
	inv, err := inventory.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	marshaled, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	again, err := inventory.Parse(marshaled)
	if err != nil {
		t.Fatalf("Parse of the marshaled document: %v", err)
	}
	if !reflect.DeepEqual(inv, again) {
		t.Errorf("round trip differs:\nfirst  %+v\nsecond %+v", inv, again)
	}
}

// controlDoc is a valid document whose string fields
// TestParseRejectsControlCharacters poisons one at a time.
type controlDoc struct {
	Schema      int              `json:"schema"`
	SohoVersion string           `json:"soho_version"`
	GeneratedMS int64            `json:"generated_ms"`
	Projects    []controlProject `json:"projects"`
	Hidden      controlHidden    `json:"hidden"`
}

type controlProject struct {
	Name     string       `json:"name"`
	Sessions []controlSes `json:"sessions"`
}

type controlSes struct {
	ID     string       `json:"id"`
	Agents []controlAge `json:"agents"`
}

type controlAge struct {
	Terminal    string  `json:"terminal"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	Kind        string  `json:"kind"`
	Model       *string `json:"model"`
	Effort      *string `json:"effort"`
	State       string  `json:"state"`
	Reason      *string `json:"reason"`
	Task        *string `json:"task"`
	ReportReady bool    `json:"report_ready"`
	StartedMS   int64   `json:"started_ms"`
}

type controlHidden struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needs_you"`
	Done     int `json:"done"`
	Idle     int `json:"idle"`
	Unknown  int `json:"unknown"`
}

func baseControlDoc() *controlDoc {
	model := "example/model-1"
	task := "Retry declined cards"
	return &controlDoc{
		Schema:      inventory.Schema,
		SohoVersion: "v0.3.1",
		GeneratedMS: 1793376000000,
		Projects: []controlProject{{
			Name: "acme/web",
			Sessions: []controlSes{{
				ID: "w1",
				Agents: []controlAge{{
					Terminal:    "w1:p1",
					Name:        "build",
					Role:        "implementer",
					Kind:        "cli-b",
					Model:       &model,
					Effort:      nil,
					State:       "working",
					Reason:      nil,
					Task:        &task,
					ReportReady: false,
					StartedMS:   1793372400000,
				}},
			}},
		}},
		Hidden: controlHidden{Working: 1},
	}
}

// TestParseRejectsControlCharacters: every string field of the
// document refuses the C0 control U+0007 and the C1 control U+0085,
// and the message names the field.
func TestParseRejectsControlCharacters(t *testing.T) {
	controls := []struct {
		name string
		ch   string
	}{
		{"c0-u0007", "\u0007"},
		{"c1-u0085", "\u0085"},
	}
	fields := []struct {
		field string
		want  string
		apply func(doc *controlDoc, value string)
	}{
		{"soho_version", "soho_version", func(d *controlDoc, v string) { d.SohoVersion = v }},
		{"project.name", "projects[0].name", func(d *controlDoc, v string) { d.Projects[0].Name = v }},
		{"session.id", "projects[0].sessions[0].id", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].ID = v }},
		{"terminal", "agents[0].terminal", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Terminal = v }},
		{"name", "agents[0].name", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Name = v }},
		{"role", "agents[0].role", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Role = v }},
		{"kind", "agents[0].kind", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Kind = v }},
		{"model", "agents[0].model", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Model = &v }},
		{"effort", "agents[0].effort", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Effort = &v }},
		{"task", "agents[0].task", func(d *controlDoc, v string) { d.Projects[0].Sessions[0].Agents[0].Task = &v }},
	}
	for _, c := range controls {
		for _, f := range fields {
			t.Run(f.field+"/"+c.name, func(t *testing.T) {
				doc := baseControlDoc()
				f.apply(doc, "x"+c.ch+"x")
				data, err := json.Marshal(doc)
				if err != nil {
					t.Fatalf("marshal the poisoned document: %v", err)
				}
				_, err = inventory.Parse(data)
				var perr *inventory.Error
				if !errors.As(err, &perr) {
					t.Fatalf("%s %s: error = %v, want *inventory.Error", f.field, c.name, err)
				}
				if !strings.Contains(perr.Msg, f.want) {
					t.Errorf("%s %s: message %q does not name %q", f.field, c.name, perr.Msg, f.want)
				}
			})
		}
	}
}

// TestParseSizeLimit: a valid document padded with whitespace to
// exactly 4 MiB is accepted; one byte more is refused.
func TestParseSizeLimit(t *testing.T) {
	const limit = 4 * 1024 * 1024
	doc := string(docWithAgents(1))
	if len(doc) >= limit {
		t.Fatalf("document too large to pad: %d bytes", len(doc))
	}
	exact := doc + strings.Repeat(" ", limit-len(doc))
	if _, err := inventory.Parse([]byte(exact)); err != nil {
		t.Errorf("%d bytes: Parse: %v, want accepted", len(exact), err)
	}
	over := exact + " "
	_, err := inventory.Parse([]byte(over))
	var perr *inventory.Error
	if !errors.As(err, &perr) || !strings.Contains(perr.Msg, "input exceeds 4 MiB") {
		t.Errorf("%d bytes: error = %v, want the size refusal", len(over), err)
	}
}

// TestParseAgentLimit: 500 agents is the last allowed document; 501 is
// refused.
func TestParseAgentLimit(t *testing.T) {
	if _, err := inventory.Parse(docWithAgents(500)); err != nil {
		t.Errorf("500 agents: Parse: %v", err)
	}
	_, err := inventory.Parse(docWithAgents(501))
	var perr *inventory.Error
	if !errors.As(err, &perr) || !strings.Contains(perr.Msg, "more than 500 agents") {
		t.Errorf("501 agents: error = %v, want the agent limit refusal", err)
	}
}

// TestParseTruncatedAccepted: truncated true is a field soho writes
// when it kept only the first 500 agents, so it must parse (small and
// with a 500-agent document).
func TestParseTruncatedAccepted(t *testing.T) {
	inv, err := inventory.Parse(docWithAgents(1))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if inv.Truncated {
		t.Errorf("truncated = true, want false when the key is absent")
	}
	for _, doc := range [][]byte{docWithAgents(1), docWithAgents(500)} {
		withTruncated := withTruncatedTrue(t, doc)
		inv, err := inventory.Parse(withTruncated)
		if err != nil {
			t.Fatalf("Parse with truncated true: %v", err)
		}
		if !inv.Truncated {
			t.Errorf("truncated = false, want true")
		}
	}
}

// docWithAgents builds a document with n working agents in one
// session of one project.
func docWithAgents(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"schema":1,"soho_version":"v0.3.1","generated_ms":1793376000000,`)
	b.WriteString(`"projects":[{"name":"acme/web","sessions":[{"id":"w1","agents":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"terminal":"w1:p%d","name":"agent-%d","role":"implementer","kind":"cli-b","state":"working","report_ready":false,"started_ms":1793372400000}`, i+1, i+1)
	}
	b.WriteString(`]}]}],"hidden":{"working":0,"needs_you":0,"done":0,"idle":0,"unknown":0}}`)
	return []byte(b.String())
}

// withTruncatedTrue adds "truncated": true to a document's top-level
// fields.
func withTruncatedTrue(t *testing.T, doc []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("unmarshal the generated document: %v", err)
	}
	m["truncated"] = true
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal the generated document: %v", err)
	}
	return out
}
