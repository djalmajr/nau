# Hub API

The hub serves a JSON API under `/api/` and the web UI everywhere else. This page is the contract between the UI and the server. The ingest from the machines, the accounts and the sharing rules have their own pages when they land.

Every endpoint below needs a session (`GET /api/session`), and it answers 401 `{"error":"unauthenticated"}` without one. Times are milliseconds since the epoch, UTC. An answer only holds what the signed-in user may see: their own machines and the machines shared with them. The server applies that rule. The UI never filters for permission.

## Session

- **`GET /api/session`:** 200 `{"user":{"id":"...","name":"...","email":"...","role":"owner"|"member"}}`, or 401.
- **`POST /api/session`:** body `{"email":"...","password":"..."}`. Answers:
  - 200 with the same body as `GET`, and the server sets an `HttpOnly` session cookie;
  - 401 `{"error":"invalid_credentials"}`;
  - 429 `{"error":"rate_limited"}`.
- **`DELETE /api/session`:** 204.

## Shared shapes

```json
{
  "machine":   { "id": "m1", "name": "laptop-b", "online": true },
  "developer": { "id": "u1", "name": "Ana Exemplo" }
}
```

**Agent:** one live agent, as the last inventory of its machine reported it, plus who and where.

```json
{
  "id": "m1.w1:p2",
  "machine": { "id": "m1", "name": "laptop-b", "online": true },
  "developer": { "id": "u1", "name": "Ana Exemplo" },
  "project": "acme/web",
  "session": "w1",
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
  "since_ms": 1793375990000,
  "observed_ms": 1793376000000
}
```

- **From the inventory** ([`inventory.md`](inventory.md)), with the same meaning: `project`, `session`, `terminal`, `name`, `role`, `kind`, `model`, `effort`, `state`, `reason`, `task`, `report_ready`, `started_ms` and `since_ms`.
- **`id`:** opaque and stable while the terminal lives.
- **`observed_ms`:** when the hub received the inventory that last listed the agent.
- **An offline machine:** its agents keep their last state. `machine.online` is `false`.

**Counts:** used for hidden projects and for totals. Always the five keys:

```json
{ "working": 0, "needs_you": 0, "done": 0, "idle": 0, "unknown": 0 }
```

## Agents

- **`GET /api/agents`:** 200 `{"generated_ms": n, "agents": [Agent], "hidden": [{"machine": Machine, "counts": Counts}]}`.
  - `agents` comes in no particular order; the UI sorts.
  - `hidden` holds one entry per machine that reported hidden projects with at least one agent.
  - The optional query parameters `developer`, `machine` (ids) and `project` filter on the server. The UI may also filter the full list itself.
- **`GET /api/agents/{id}`:** 200 `{"agent": Agent, "history": [{"state": "...", "reason": null, "at_ms": n}]}`, or 404 `{"error":"not_found"}`.
  - `history` is newest first, at most 200 entries, from the last 7 days.
  - Each entry is a change of `state` or `reason` that the hub saw.

## Overview

- **`GET /api/overview`:** 200 with:

```json
{
  "generated_ms": 1793376000000,
  "counts": { "working": 3, "needs_you": 1, "done": 2, "idle": 4, "unknown": 0 },
  "machines": { "online": 2, "offline": 1 },
  "developers": 2,
  "needs_you": [Agent],
  "per_machine": [{ "machine": Machine, "developer": Developer, "counts": Counts }],
  "working_24h": [{ "hour_ms": 1793289600000, "working": 2 }],
  "top_projects": [{ "project": "acme/web", "agents": 4, "working": 2 }]
}
```

- **`counts`:** includes the hidden projects.
- **`needs_you`:** at most 10, the longest waiting first (oldest `since_ms`).
- **`per_machine`:** every visible machine, offline ones included.
- **`working_24h`:** exactly 24 entries, oldest first. Each is the start of an hour and the most agents seen working in it.
- **`top_projects`:** at most 5, from the shown projects only, ordered by `working` and then `agents`.
