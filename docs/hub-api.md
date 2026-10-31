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
- **`POST /api/session/password`:** body `{"current":"...","new":"..."}`. Answers:
  - 204 with a new session cookie; every other session of the user ends;
  - 401 when `current` is wrong;
  - 400 `{"error":"weak_password"}` when `new` is not 12 to 128 bytes.

Every `POST`, `PUT` and `DELETE` under `/api/` needs `Content-Type: application/json`, and answers 415 `{"error":"unsupported_media_type"}` without it.

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

## Machines and pairing

- **`GET /api/machines`:** 200 `{"machines":[{"id","name","developer":Developer,"created_ms","revoked":false,"last_seen_ms":n|null,"online":true,"shared_with":[Developer]}]}`.
  - `machines`: the user's own machines, plus the ones shared with them.
  - `shared_with`: filled in only for the user's own machines.
- **`DELETE /api/machines/{id}`:** 204 revokes the key, and the machine stops delivering at once. Only the machine's developer may do it; anyone else gets 404.
- **`POST /api/pairings/confirm`:** body `{"code":"XXXX-XXXX"}`, with or without the hyphen and in any case. Answers:
  - 204 binds the pending pairing to the signed-in user;
  - 400 `{"error":"invalid_code"}` for an unknown, expired or already used code;
  - 429 after 10 wrong codes in 15 minutes.

  The machine side (`POST /api/pairings`, `POST /api/pairings/poll`, `POST /api/ingest`) is for `nau connect` and the Nau agent, not for the UI.

## Sharing

- **`POST /api/machines/{id}/shares`:** body `{"email":"..."}`. Answers:
  - 201 `{"developer":Developer}` shares the machine, read-only, with that developer;
  - 404 when the machine is not the user's own, or when no account has that e-mail. The two cases look the same.
- **`DELETE /api/machines/{id}/shares/{developerId}`:** 204 ends the share.
- **What a share shows:** the machine's agents in projects in `show` mode, plus the counts of its hidden projects. A share never grants a write.

## Developers

- **`GET /api/developers`:** 200 `{"developers":[{"id","name","email","machines":n,"shares_with_me":n,"shared_by_me":n}]}`.
  - The list holds the user, the developers who share a machine with the user, and the developers the user shares with.
  - The hub owner (`role: owner`) sees every account, but no machine data beyond what is shared with them.

## Invites (hub owner only)

- **`POST /api/invites`:** body `{"email":"...","role":"member"}`. Answers 201 `{"id","email","expires_ms","token"}`. The token appears only here; the invite link is `<hub>/invite#<token>`.
- **`GET /api/invites`:** 200 `{"invites":[{"id","email","expires_ms"}]}`, the pending ones only.
- **`DELETE /api/invites/{id}`:** 204.
- **`POST /api/invites/accept`:** body `{"token","name","password"}`, without a session. Answers:
  - 201 with the `GET /api/session` body and the cookie;
  - 400 `{"error":"invalid_invite"}`;
  - 400 `{"error":"weak_password"}` when `password` is not 12 to 128 bytes;
  - 409 `{"error":"email_taken"}`.
- **A member** calling the first three gets 403 `{"error":"forbidden"}`.
