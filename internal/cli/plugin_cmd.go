package cli

// The Herdr plugin entry points (slice 4b): the one-shot startup and event
// hooks plus the two bridge popup actions. The one-shot hooks always exit 0
// (failures go to friction), so the plugin can never fail the Herdr server;
// the bridge actions behave like the regular commands. No second copy of
// the outbox or push logic lives here — startup and bridge sync reuse the
// sync run, and bridge status reads the state read-only.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/djalmajr/nau/internal/outbox"
	"github.com/djalmajr/nau/internal/plugin"
)

// The Herdr event names of the workspace hooks.
const (
	pluginEventWorkspaceCreated = "workspace.created"
	pluginEventWorkspaceClosed  = "workspace.closed"
)

// The underscored event-stream names of the Herdr workspace events (the
// dotted names are the pluginEventWorkspace* constants above).
const (
	workspaceCreatedUnderscored = "workspace_created"
	workspaceClosedUnderscored  = "workspace_closed"
)

// pluginWorkspaceIDPattern is the id of a Herdr workspace.
var pluginWorkspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// cmdPlugin dispatches the plugin subcommands.
func cmdPlugin(args []string, env Env) int {
	if len(args) == 0 {
		badUsage(env, "usage: plugin startup|event|bridge status|bridge sync")
		return 2
	}
	switch args[0] {
	case "startup":
		if len(args) != 1 {
			badUsage(env, "usage: plugin startup")
			return 2
		}
		return pluginStartup(env)
	case "event":
		if len(args) != 1 {
			badUsage(env, "usage: plugin event")
			return 2
		}
		return pluginEvent(env)
	case "bridge":
		return cmdPluginBridge(args[1:], env)
	default:
		badUsage(env, "unknown plugin subcommand "+quote(args[0]))
		return 2
	}
}

func cmdPluginBridge(args []string, env Env) int {
	if len(args) != 1 {
		badUsage(env, "usage: plugin bridge status|sync")
		return 2
	}
	switch args[0] {
	case "status":
		return pluginBridgeStatus(env)
	case "sync":
		// The bridge sync runs the full sync (writes plus push), so it is
		// refused under NOWRITE like every writing command.
		if env.Getenv(nowriteVar) == "1" {
			_, _ = fmt.Fprintf(env.Stdout, "%s\n", nowriteErrorJSON)
			return 2
		}
		return pluginBridgeSync(env)
	default:
		badUsage(env, "unknown plugin bridge subcommand "+quote(args[0]))
		return 2
	}
}

// pluginStartup runs the sync logic for every open tracked job plus the
// push step and always exits 0: a config/capability/machine-label failure
// writes one friction line and prints the skipped line; a push failure is a
// normal sync outcome (the counts line carries its status).
func pluginStartup(env Env) int {
	if env.Getenv(nowriteVar) == "1" {
		printPluginSkipped(env, nowriteMotivo)
		return 0
	}
	res, code := runSync(context.Background(), env, "", false)
	if code == 2 || code == 43 {
		pluginFriction(env, "startup: "+res.ErrMotivo)
		printPluginSkipped(env, res.ErrMotivo)
		return 0
	}
	printSyncLine(env, res)
	return 0
}

// pluginFriction records one friction line in the plugin's own write area,
// creating the state dir when needed; best effort (the state dir may be
// unwritable, in which case the line is lost, as with every friction line).
func pluginFriction(env Env, msg string) {
	dir := outbox.StateDir(env.ConfigDir)
	if _, err := outbox.Open(dir, outbox.Options{Now: env.Now}); err == nil {
		outbox.Friction(dir, "plugin", msg)
	}
}

// pluginEvent handles the workspace.created and workspace.closed Herdr
// events for workspaces labeled job-<id> and always exits 0: created
// tracks the job (empty projeto) when unknown and runs the sync --job
// logic; closed runs a final sync for a tracked job. Other labels, other
// event names and unknown or empty JSON shapes are ignored (an unknown or
// empty shape writes one friction line).
func pluginEvent(env Env) int {
	if env.Getenv(nowriteVar) == "1" {
		printPluginSkipped(env, nowriteMotivo)
		return 0
	}
	eventName := env.Getenv(plugin.EnvEvent)
	switch eventName {
	case pluginEventWorkspaceCreated, pluginEventWorkspaceClosed:
		pluginWorkspaceEvent(env, eventName)
	default:
		// Other event names: nothing written, but an unreadable payload
		// still leaves one friction line, as before.
		if _, _, ok := plugin.ParseEvent(env.Getenv(plugin.EnvEventJSON)); !ok {
			pluginFriction(env, "event: unknown event JSON shape")
		}
	}
	return 0
}

// pluginWorkspaceEvent handles workspace.created and workspace.closed. A
// job workspace (label job-<id>, the job contract's label) keeps today's
// bookkeeping: created tracks the job (empty projeto) when unknown, and a
// tracked job is synced (on close, a final sync). Any other workspace
// writes nothing; an unreadable payload writes one friction line.
func pluginWorkspaceEvent(env Env, eventName string) {
	ev, err := parseWorkspaceEventPayload([]byte(env.Getenv(plugin.EnvEventJSON)), eventName)
	if err != nil {
		pluginFriction(env, "event: unknown event JSON shape")
		return
	}
	labelJob := workspaceLabelJob(ev)
	syncJob := labelJob
	tracked := syncJob != "" && isJobTracked(env, syncJob)
	if ev.created && labelJob != "" && !tracked {
		if err := trackPluginJob(env, labelJob); err != nil {
			pluginFriction(env, "event: track "+labelJob+": "+err.Error())
			return
		}
		tracked = true
	}
	if tracked {
		res, code := runSync(context.Background(), env, syncJob, false)
		if code != 0 {
			pluginFriction(env, "event: sync "+syncJob+": "+res.ErrMotivo)
		}
	}
}

// parsedWorkspaceEvent is the parsed Herdr workspace event of the plugin
// event hook: the kind, the workspace id and the label.
type parsedWorkspaceEvent struct {
	created bool
	id      string
	label   string
}

// parseWorkspaceEventPayload parses a Herdr workspace event. It accepts
// the socket subscription line {"event":"workspace.created"|"workspace_created"
// |"workspace.closed"|"workspace_closed","data":{…}} where data is the
// event-stream form, the event-stream form
// {"type":"workspace_created","workspace":{…}} and
// {"type":"workspace_closed","workspace_id":…,"workspace":null|{…}} (the
// bundled API schema), and the existing plugin payload shapes — nested
// {"workspace":{"label":…,"workspace_id":…}} or flat
// {"label":…,"workspace_id":…} — whose event name comes from the hook
// ("workspace.created" or "workspace.closed") when the JSON carries no
// name of its own. The event name is the envelope event, else the data
// type, else the hook; any other name is refused and a missing name is
// refused. The workspace id is workspace.workspace_id, else the
// top-level (or data-level) workspace_id, and must match the workspace
// id rule. The label is workspace.label, else the top-level label; it may
// be empty only for a closed event (a created event without a label is
// refused). Unknown fields (agent kinds, titles, tokens, worktree,
// counts) are discarded at parse time.
func parseWorkspaceEventPayload(raw []byte, hook string) (parsedWorkspaceEvent, error) {
	if !isWorkspaceJSONObject(raw) {
		return parsedWorkspaceEvent{}, errors.New("workspace event is not a JSON object")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return parsedWorkspaceEvent{}, fmt.Errorf("decode workspace event: %w", err)
	}
	var data map[string]any
	var envelope string
	if ev, present := m["event"]; present {
		name, isStr := ev.(string)
		if !isStr {
			return parsedWorkspaceEvent{}, errors.New("workspace event name is not a string")
		}
		envelope = name
		d, ok := m["data"].(map[string]any)
		if !ok {
			return parsedWorkspaceEvent{}, errors.New("workspace event has no data object")
		}
		data = d
	} else {
		// No envelope: the event-stream form or a plugin payload; the
		// data fields sit at the top level.
		data = m
	}
	name := envelope
	if name == "" {
		switch t, present := data["type"]; {
		case present:
			ts, isStr := t.(string)
			if !isStr {
				return parsedWorkspaceEvent{}, errors.New("workspace event type is not a string")
			}
			name = ts
		case hook != "":
			name = hook
		default:
			return parsedWorkspaceEvent{}, errors.New("workspace event name is missing")
		}
	}
	var created bool
	switch name {
	case pluginEventWorkspaceCreated, workspaceCreatedUnderscored:
		created = true
	case pluginEventWorkspaceClosed, workspaceClosedUnderscored:
	default:
		return parsedWorkspaceEvent{}, errors.New("workspace event is not a workspace created or closed event")
	}
	var ws map[string]any
	if w, present := data["workspace"]; present {
		ws, _ = w.(map[string]any) // null or non-object: no workspace object
	}
	wsID, _, err := workspaceStringField([]map[string]any{ws, data, m}, "workspace_id")
	if err != nil {
		return parsedWorkspaceEvent{}, err
	}
	if !pluginWorkspaceIDPattern.MatchString(wsID) {
		return parsedWorkspaceEvent{}, errors.New("workspace id is missing or invalid")
	}
	label, _, err := workspaceStringField([]map[string]any{ws, data, m}, "label")
	if err != nil {
		return parsedWorkspaceEvent{}, err
	}
	if created && label == "" {
		return parsedWorkspaceEvent{}, errors.New("workspace created event has no label")
	}
	return parsedWorkspaceEvent{created: created, id: wsID, label: label}, nil
}

// isWorkspaceJSONObject reports whether b is a JSON object: non-empty
// after trimming, starting with '{' and valid JSON.
func isWorkspaceJSONObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

// workspaceStringField returns the value of key from the first map (in
// order) that carries the key. It errors when the first present value is
// not a string; "" with no error when no map carries the key.
func workspaceStringField(maps []map[string]any, key string) (string, bool, error) {
	for _, m := range maps {
		if m == nil {
			continue
		}
		v, present := m[key]
		if !present {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			return "", false, fmt.Errorf("workspace field %q is not a string", key)
		}
		return s, true, nil
	}
	return "", false, nil
}

// workspaceLabelJob returns the job of a job-<id> workspace label, or "".
func workspaceLabelJob(ev parsedWorkspaceEvent) string {
	if job, ok := plugin.JobIDFromLabel(ev.label); ok {
		return job
	}
	return ""
}

// pluginBridgeStatus is the read-only popup action: exactly one JSON line
// (and nothing else), no writes — it works under HERDR_HERMES_NOWRITE=1
// and creates nothing.
func pluginBridgeStatus(env Env) int {
	jobsAbertos, pendentes := pluginBridgeCounts(env)
	var lastPush *outbox.PushResult
	store, err := outbox.Open(outbox.StateDir(env.ConfigDir), outbox.Options{Now: env.Now, ReadOnly: true})
	if err == nil {
		if lp, lerr := store.LastPush(); lerr == nil {
			lastPush = lp
		}
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(struct {
		JobsAbertos   int                `json:"jobs_abertos"`
		Pendentes     int                `json:"pendentes"`
		LastPush      *outbox.PushResult `json:"ultimo_push"`
		KeyConfigured bool               `json:"key_configured"`
	}{jobsAbertos, pendentes, lastPush, keyConfigured(env)}))
	return 0
}

// pluginBridgeSync says what it will do (open tracked jobs and pending
// records) on stderr, then runs the sync and returns the sync exit code.
func pluginBridgeSync(env Env) int {
	jobs, pending := pluginBridgeCounts(env)
	_, _ = fmt.Fprintf(env.Stderr, "%s: plugin bridge sync: %d open job(s), %d pending record(s)\n", env.program(), jobs, pending)
	res, code := runSync(context.Background(), env, "", false)
	printSyncOutcome(env, res, code)
	return code
}

// pluginBridgeCounts reads the open-job and pending-record counts
// read-only (zero on unreadable state), for the bridge lines.
func pluginBridgeCounts(env Env) (jobs, pending int) {
	store, err := outbox.Open(outbox.StateDir(env.ConfigDir), outbox.Options{Now: env.Now, ReadOnly: true})
	if err != nil {
		return 0, 0
	}
	if js, err := store.LoadJobs(); err == nil {
		for _, j := range js.Jobs {
			if !j.Closed && !isSyncTerminal(j.Estado) {
				jobs++
			}
		}
	}
	if _, last, err := store.Read(0); err == nil {
		delivered, derr := store.DeliveredSeq()
		if derr != nil {
			delivered = 0
		}
		pending = int(last - delivered)
		if pending < 0 {
			pending = 0
		}
	}
	return jobs, pending
}

// trackPluginJob adds the job to the local bookkeeping when it is not
// there yet (empty projeto) and is a no-op when it is.
func trackPluginJob(env Env, jobID string) error {
	store, err := outbox.Open(outbox.StateDir(env.ConfigDir), outbox.Options{Now: env.Now})
	if err != nil {
		return err
	}
	return store.UpdateJobs(func(js *outbox.Jobs) error {
		if _, ok := js.Jobs[jobID]; !ok {
			ts := env.Now().Format(outbox.TSLayout)
			js.Jobs[jobID] = &outbox.Job{ID: jobID, CreatedAt: ts, UpdatedAt: ts}
		}
		return nil
	})
}

// isJobTracked reports whether the job is in the local bookkeeping
// (read-only).
func isJobTracked(env Env, jobID string) bool {
	store, err := outbox.Open(outbox.StateDir(env.ConfigDir), outbox.Options{Now: env.Now, ReadOnly: true})
	if err != nil {
		return false
	}
	jobs, err := store.LoadJobs()
	if err != nil {
		return false
	}
	_, ok := jobs.Jobs[jobID]
	return ok
}

// printPluginSkipped prints the plugin skipped line.
func printPluginSkipped(env Env, motivo string) {
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(struct {
		Status string `json:"status"`
		Motivo string `json:"motivo"`
	}{"skipped", motivo}))
}
