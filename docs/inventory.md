# Inventory (proposal)

**Status: proposal, schema 1.** soho owns the command and the format; Nau proposes it. This page becomes the vendored contract once soho ships `soho inventory --json` and the owner agrees.

The hub shows which agents are working, on which machines and for which developers. Its only source is `soho inventory --json`, run on each machine by the Nau agent of that machine. soho is the only place that applies the privacy of a project: Nau carries what the inventory prints and never adds to it from another source. Events from the terminal server (`hc events`) only wake the Nau agent, which then runs the inventory again. Nau never shows or forwards what it read in those events.

## Command

```
soho inventory --json
```

- **Read-only and local.** It reads the soho state of the projects that opted in and the terminal server of this machine. It writes nothing, makes no network call and works under `SOHO_NOWRITE=1`.
- **Local terminals only.** Terminals mirrored from another machine (`hc machine add`, `herdr --machine`) are never listed. Each machine reports itself, so no terminal is counted twice.
- **Exit codes:**
  - 0: the inventory was printed, even when no project opted in;
  - 2: bad usage;
  - 4: the terminal server cannot be read. Nothing is printed on stdout, and the Nau agent reports the machine as unknown.
- **Discovery:** `soho capabilities --json` gains `"inventory": 1`. The Nau agent runs the inventory only when it is present.

## Privacy

A project opts in through its own `.agents/soho.conf`. soho chooses the names of the key and its values when it implements this. They are written `inventory=show|hidden|off` here.

| Mode | What leaves the machine |
|---|---|
| `show` | Every field of the project and its agents below. |
| `hidden` | Only counts per state, merged with the other hidden projects of the machine. No project name, session, terminal, role, assistant, model or title. |
| `off`, or no key | Nothing. The project does not appear, and its agents are not counted. |

- **Fail closed.** A project with no key, an unknown value, or a configuration file that cannot be read delivers nothing.
- **Only the project file opens.** Only the project's own `.agents/soho.conf` can set `show` or `hidden`. A user, session or environment layer may only lower the mode (`show` to `hidden`, or either to `off`), never raise it: a project without the key delivers nothing, whatever the other layers say.
- **Never in the output:**
  - in any mode: brief or report content, screen content (dialog and question text included), paths (working directory, state directory, report path), environment, command output and agent arguments;
  - in addition, in `hidden` mode: workspace and tab labels. In `show` mode the project's name is the only label that leaves the machine.

## Format

One JSON object on stdout.

```json
{
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
            {
              "terminal": "w1:p1",
              "name": "orchestrator",
              "role": "orchestrator",
              "kind": "cli-a",
              "model": null,
              "effort": null,
              "state": "working",
              "reason": null,
              "task": "Retry declined cards at checkout",
              "report_ready": false,
              "started_ms": 1793372400000,
              "since_ms": 1793375940000
            },
            {
              "terminal": "w1:p2",
              "name": "build",
              "role": "implementer",
              "kind": "cli-b",
              "model": "example/model-1",
              "effort": "high",
              "state": "needs_you",
              "reason": "approval",
              "task": "Retry declined cards in the checkout worker",
              "report_ready": false,
              "started_ms": 1793373000000,
              "since_ms": 1793375990000
            }
          ]
        }
      ]
    }
  ],
  "hidden": { "working": 1, "needs_you": 0, "done": 0, "idle": 2, "unknown": 0 }
}
```

### Top level

- `schema`: the integer 1. A field may be added without a new schema; removing or changing the meaning of a field takes a new one. Readers ignore unknown fields.
- `soho_version`: the version of the soho that printed the inventory. Never empty.
- `generated_ms`: when soho read the state, in milliseconds since the epoch (UTC). The hub computes "observed N ago" from it.
- `projects`: the projects in `show` mode that have at least one live agent. A project with no live agent is left out.
- `hidden`: the counts of live agents per `state` over every project in `hidden` mode. All five keys (`working`, `needs_you`, `done`, `idle`, `unknown`) are always present, at zero when there is none.

### Project

- `name`: the display name. It comes from an optional name key in the project's configuration, and defaults to the base name of the project root. It is never a path or a remote URL.
- `sessions`: one entry per soho session of the project (the state directory `.soho/<id>`) that has a live agent.

### Session

- `id`: the session id (today the Herdr workspace id). It is opaque and only groups an orchestrator with its workers.
- `agents`: the orchestrator of the session, then its workers in roster order.

### Agent

| Field | Type | Meaning |
|---|---|---|
| `terminal` | string | The local terminal id, opaque, stable while the terminal lives (a Herdr pane id today, an `hc` terminal id later). Never prefixed with a machine. |
| `name` | string | The agent name (`orchestrator`, `build`, `build-2`, `review`). |
| `role` | string | `orchestrator`, `sub-orchestrator`, or the worker's role (`implementer`, `reviewer`, …). |
| `kind` | string | The assistant CLI, one of soho's known kinds (`soho kinds`). Nau shows it as given. |
| `model` | string or null | The model resolved at spawn. `null` when the CLI's own default applies. |
| `effort` | string or null | `low`, `medium`, `high`, `xhigh` or `max` after clamping; `null` when none was passed. |
| `state` | string | `working`, `needs_you`, `done`, `idle` or `unknown` (see "States"). |
| `reason` | string or null | Why the agent needs you; set only when `state` is `needs_you`. |
| `task` | string or null | The task title: the H1 of the current brief without the role prefix and the done mark, or the orchestrator's `soho title` objective. At most 120 code points, with control characters removed. `null` when there is none. |
| `report_ready` | boolean | The worker's report for the current task exists. Always `false` for an orchestrator. |
| `started_ms` | integer | When the agent was spawned (or, for an orchestrator, when the session started), in UTC milliseconds. |
| `since_ms` | integer or null | When soho first saw the agent in its current `state`. `null` until soho tracks it. |

Strings are valid UTF-8 and carry no control characters. An inventory lists at most 500 agents. Past that, soho keeps the first 500 in project, session and roster order and adds `"truncated": true` at the top level.

## States

soho already distinguishes these cases. The inventory folds them into five values, plus a reason for `needs_you`:

| soho today | `state` | `reason` |
|---|---|---|
| Working with a screen that moves, compacting, an active collaboration | `working` | — |
| Blocked on a question dialog | `needs_you` | `question` |
| Blocked on a permission or trust dialog | `needs_you` | `approval` |
| Out of quota (exit 11) | `needs_you` | `quota` |
| Provider error, refusal or no capacity | `needs_you` | `provider` |
| The brief was not received | `needs_you` | `not_received` |
| Settled with no report | `needs_you` | `no_report` |
| A compaction was interrupted | `needs_you` | `compact` |
| A worker whose report is ready; an orchestrator whose turn ended unseen (Herdr `done`) | `done` | — |
| Idle, or idle with no report yet | `idle` | — |
| The state cannot be read | `unknown` | — |
| Gone (closed terminal) | not listed | — |

The hub's board columns follow `state`: Needs you, Working, Done and Idle. `unknown` agents are shown apart.

## Nau side

- **When it runs:** the Nau agent runs the inventory when a terminal-server event wakes it, at most once every 2 seconds, and at least once a minute without events.
- **What it sends:** the hub receives the inventory unchanged, plus the machine identity that Nau holds. The inventory carries no machine or developer field, because Nau owns both.
- **Retention:** the hub keeps the last inventory per machine and the state changes it derives. It never keeps more than the inventory gave it.
- **Tests:** they use a fake `soho inventory --json` that covers every mode and every state above, so the agent and the hub can be built before soho ships the command.

## Open questions for soho

1. **Discovery of projects.** soho has no index of the projects on a machine. One option: `soho init` and `soho config set` record the root of each project whose mode is not `off` in a local index under the user state directory. The index never leaves the machine, and the inventory prunes the roots that no longer exist. Another option: derive the projects from the live terminals' working directories.
2. **The orchestrator's terminal.** The roster does not list the orchestrator. Does `soho init` record its terminal in the session state, so the inventory can list it with its title?
3. **`since_ms`.** soho has screen-activity markers but no state-transition time. Can it record the first time it saw each state, or does the terminal server expose one?
4. **Waiting for the owner.** soho has no such state for workers (`blocked` and `question` come closest). Does a job waiting for human input (`job_human_input_minutes`) map to `needs_you` with its own reason?
5. **Key names.** The names of the mode key, the name key and their values. The proposal above says that only the project file opens a project; the other layers may only close it.
