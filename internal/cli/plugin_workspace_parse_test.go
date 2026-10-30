package cli

import (
	"strings"
	"testing"
)

// TestParseWorkspaceEventPayloadShapes: the workspace event shapes the
// parser accepts (moved here with the parser from the notify package).
func TestParseWorkspaceEventPayloadShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		hook string
		want parsedWorkspaceEvent
	}{
		{
			name: "socket subscription created",
			raw:  `{"event":"workspace.created","data":{"type":"workspace_created","workspace":{"workspace_id":"w2","label":"job-1","title":"a title","agent_kind":"agent-a","tokens":7,"worktree":"wt","counts":{"open":1}}}}`,
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "socket subscription created envelope name only",
			raw:  `{"event":"workspace.created","data":{"workspace":{"workspace_id":"w2","label":"job-1"}}}`,
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "socket subscription closed envelope wins over data type",
			raw:  `{"event":"workspace.created","data":{"type":"workspace_closed","workspace_id":"w2","label":"job-1"}}`,
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "socket subscription closed workspace null",
			raw:  `{"event":"workspace.closed","data":{"type":"workspace_closed","workspace_id":"w2","workspace":null}}`,
			want: parsedWorkspaceEvent{created: false, id: "w2", label: ""},
		},
		{
			name: "socket subscription closed with workspace object",
			raw:  `{"event":"workspace.closed","data":{"type":"workspace_closed","workspace":{"workspace_id":"w2","label":"job-1"}}}`,
			want: parsedWorkspaceEvent{created: false, id: "w2", label: "job-1"},
		},
		{
			name: "event stream created",
			raw:  `{"type":"workspace_created","workspace":{"workspace_id":"w2","label":"job-1","extra":{"a":1}}}`,
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "event stream closed workspace null",
			raw:  `{"type":"workspace_closed","workspace_id":"w3","workspace":null}`,
			want: parsedWorkspaceEvent{created: false, id: "w3", label: ""},
		},
		{
			name: "event stream closed with workspace",
			raw:  `{"type":"workspace_closed","workspace":{"workspace_id":"w3","label":"job-1"}}`,
			want: parsedWorkspaceEvent{created: false, id: "w3", label: "job-1"},
		},
		{
			name: "plugin nested with hook created",
			raw:  `{"workspace":{"label":"job-1","workspace_id":"w2"}}`,
			hook: "workspace.created",
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "plugin nested with hook closed",
			raw:  `{"workspace":{"label":"job-1","workspace_id":"w2"}}`,
			hook: "workspace.closed",
			want: parsedWorkspaceEvent{created: false, id: "w2", label: "job-1"},
		},
		{
			name: "plugin flat with hook created",
			raw:  `{"label":"job-1","workspace_id":"w2"}`,
			hook: "workspace.created",
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
		{
			name: "plugin flat closed without label",
			raw:  `{"workspace_id":"w2"}`,
			hook: "workspace.closed",
			want: parsedWorkspaceEvent{created: false, id: "w2", label: ""},
		},
		{
			name: "plugin flat closed with empty label",
			raw:  `{"label":"","workspace_id":"w2"}`,
			hook: "workspace.closed",
			want: parsedWorkspaceEvent{created: false, id: "w2", label: ""},
		},
		{
			name: "data level workspace id and label",
			raw:  `{"event":"workspace.closed","data":{"workspace_id":"w4","label":"job-1","workspace":null}}`,
			want: parsedWorkspaceEvent{created: false, id: "w4", label: "job-1"},
		},
		{
			name: "workspace id wins over data level",
			raw:  `{"type":"workspace_created","workspace_id":"w-other","workspace":{"workspace_id":"w2","label":"job-1"}}`,
			want: parsedWorkspaceEvent{created: true, id: "w2", label: "job-1"},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got, err := parseWorkspaceEventPayload([]byte(c.raw), c.hook)
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if got != c.want {
				t.Fatalf("got = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestParseWorkspaceEventPayloadRejected: another event, malformed JSON,
// missing names and invalid fields are refused.
func TestParseWorkspaceEventPayloadRejected(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		hook string
	}{
		{"envelope other event", `{"event":"pane.exited","data":{"type":"workspace_created","workspace":{"workspace_id":"w2","label":"job-1"}}}`, ""},
		{"envelope other underscored event", `{"event":"pane_agent_status_changed","data":{"workspace":{"workspace_id":"w2","label":"job-1"}}}`, ""},
		{"data type other event", `{"type":"pane_agent_status_changed","workspace":{"workspace_id":"w2","label":"job-1"}}`, ""},
		{"hook other event", `{"label":"job-1","workspace_id":"w2"}`, "pane.exited"},
		{"not json", `not json at all`, ""},
		{"json array", `[1,2]`, ""},
		{"json string", `"w2"`, ""},
		{"json number", `7`, ""},
		{"json null", `null`, ""},
		{"empty", ``, ""},
		{"no name at all", `{"workspace":{"workspace_id":"w2","label":"job-1"}}`, ""},
		{"empty object no name", `{}`, ""},
		{"event name not a string", `{"event":42,"data":{"workspace":{"workspace_id":"w2","label":"job-1"}}}`, ""},
		{"type not a string", `{"type":42,"workspace":{"workspace_id":"w2","label":"job-1"}}`, ""},
		{"envelope with no data", `{"event":"workspace.created"}`, ""},
		{"envelope with null data", `{"event":"workspace.created","data":null}`, ""},
		{"envelope with string data", `{"event":"workspace.created","data":"w2"}`, ""},
		{"invalid workspace id", `{"type":"workspace_created","workspace":{"workspace_id":"bad ws","label":"job-1"}}`, ""},
		{"workspace id too long", `{"type":"workspace_created","workspace":{"workspace_id":"` + strings.Repeat("w", 65) + `","label":"job-1"}}`, ""},
		{"missing workspace id", `{"type":"workspace_created","workspace":{"label":"job-1"}}`, ""},
		{"workspace id not a string", `{"type":"workspace_created","workspace":{"workspace_id":7,"label":"job-1"}}`, ""},
		{"created without label", `{"type":"workspace_created","workspace":{"workspace_id":"w2"}}`, ""},
		{"created with empty label", `{"type":"workspace_created","workspace":{"workspace_id":"w2","label":""}}`, ""},
		{"created without label via hook", `{"workspace_id":"w2"}`, "workspace.created"},
		{"label not a string", `{"type":"workspace_created","workspace":{"workspace_id":"w2","label":7}}`, ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseWorkspaceEventPayload([]byte(c.raw), c.hook); err == nil {
				t.Fatal("err = nil, want an error")
			}
		})
	}
}
