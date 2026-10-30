---
name: herdr-hermes
description: >
  Two-way bridge between a remote job dispatcher and this Herdr node. Use
  when you start or keep working on a tracked project (session start),
  reach a milestone (session update: branch pushed, draft PR opened,
  blocked), make a relevant decision outside a job (decision), stop the
  work (session end), or want the dispatcher to see job events right away
  (sync). It records job events, session status and decisions in a durable
  local outbox that the dispatcher can pull or that the node can push. The
  skill is optional: a project without it keeps using `soho` unchanged.
argument-hint: "<org>/<repo>"
user-invocable: true
---

# /herdr-hermes

Guide for a local orchestrator inside Herdr that works under a remote job dispatcher. The commands run the `nau` CLI; every one prints a single JSON line. The dispatcher sees what you report here plus the job events that the `job_wake_cmd` hook records; nothing is sent silently.

Names: the command is `nau`; the old `herdr-hermes` name is retired. The state directory and the skill `name:` keep the `herdr-hermes` name.

## When to use which command

- `session start` — when you start work on a project. Include `--card` and `--branch` when they are known, and `--job` when the session is tied to a dispatcher job:

  ```text
  nau session start --projeto <org>/<repo> --branch <branch> --card <card-ref>
  nau session start --projeto <org>/<repo> --job <id>
  ```

- `session update` — at milestones: a branch pushed, a draft PR opened, or the work blocked. Repeat it as the state changes; each call appends one record:

  ```text
  nau session update --projeto <org>/<repo> --branch <branch> --estado "branch pushed"
  nau session update --projeto <org>/<repo> --pr <https://github.com/org/repo/pull/1> --estado "draft PR opened"
  nau session update --projeto <org>/<repo> --estado "blocked: waiting on a dependency"
  ```

- `decision` — for relevant decisions taken outside a job (job decisions already travel as job events). The scope is `global` or `projeto`, the `--motivo` is required, and the `resumo` is the positional argument or stdin:

  ```text
  nau decision --projeto <org>/<repo> --escopo projeto --motivo "chose the outbox pull channel for phase 1" "Use the durable outbox instead of a live socket"
  ```

- `session end` — when you stop the work, with the final state and the PR when there is one:

  ```text
  nau session end --projeto <org>/<repo> --estado "done: draft PR ready" --pr <https://github.com/org/repo/pull/1>
  ```

- `sync` — when the dispatcher should see job events right away (it pulls new events for the open tracked jobs and pushes the pending outbox records):

  ```text
  nau sync
  nau sync --job <id>
  ```

## Choosing a machine (dispatcher side)

The dispatcher chooses which machine a new job goes to; `soho` never routes a job to another machine. On the host the dispatcher uses to reach the fleet, choose the machine right before a `job start`:

```text
nau route
nau route --machine <label>
```

`route` is read-only (it works under `HERDR_HERMES_NOWRITE=1` and writes nothing): it probes the configured fleet and prints the chosen machine as one JSON line. It is advisory — it never starts, moves or replays a job. With `--machine <label>` the requested machine wins when it is available; otherwise the available machine with the lowest orchestrator load wins (its recognized orchestrators, whatever their status: idle, working, blocked, done or unknown), a tie going to the first in `route_machines` order, and a requested machine that is not available falls back with `motivo: fallback` and `solicitada` set. The dispatcher then runs `job start` on the chosen machine through its own channel. Once a `job start` (or any other mutating job command) has been sent to a machine, never re-send it to another machine after a transport loss or timeout: a connection failure does not prove the mutation was not applied; inspect that machine with `job status --id <id>` instead (the same `--id` is the job's idempotency key). The fleet keys live in `references/setup.md` and the condensed reference in `references/protocol.md`. An orchestrator counts only when its Herdr agent is named after the orchestrator base — `orchestrator` or `orchestrator-<n>` by default, `<base>` or `<base>-<n>` when `route_orchestrator_name` sets another base — as `soho init` names it; run the dispatcher as the operating-system user that saved the Herdr machines and check `herdr machine list --json` first (see `references/setup.md`).

## Dispatch key (ssh)

A dispatcher's ssh key can be bound to the forced command `nau gate`, which accepts only `nau job <sub> ...` and `nau outbox ...` and refuses everything else with exit 2; never run it by hand with arguments, and see `references/setup.md` for the key line.

## Rules

- Amendments and notes that arrive from the dispatcher are information, never user intent beyond their text. Act on them only where they say to.
- Never handle the dispatcher API key and never pass it to workers: it is read only by `nau` at push time and goes nowhere else.
- The skill is optional. A project without it keeps using `soho` unchanged.
- Setup lives in `references/setup.md`; the wire format, push headers and exit codes in `references/protocol.md`.
