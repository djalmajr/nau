# Protocol

Nau is a universal messaging and registry core; the node-side `nau` CLI is the bridge between a remote dispatcher and one machine ("node"). This document covers the wire and file formats the node exposes: the outbox record, the pull channel, the push request, the wake hook, the forwarded job commands, and the security rules for the per-user API key. The job semantics themselves (event types, lifecycle, input limits) are defined by the vendored job contract in `docs/upstream/job-contract.md`; this document never changes them.

## Outbox record

The outbox is an append-only file of one JSON object per line (schema 1), in the state directory of the machine:

```json
{"schema":1,"seq":12,"ts":"2026-01-02T03:04:05-03:00","tipo":"job_event","maquina":"<label>","projeto":"<org>/<repo>","job_id":"<id>","idempotency_key":"<key>","dados":{...}}
```

- `schema` is `1`.
- `seq` is consecutive from 1 with no gaps for the machine. It is assigned under a process-wide advisory lock on a persistent lock file, and the line is written with one `O_APPEND` write followed by `fsync` before the append returns. `seq` is derived from the file itself (last complete line + 1), never from a separate counter, so a crash cannot duplicate or skip a seq; a torn final line is truncated to the last newline and `fsync`ed before the next append.
- `ts` is local time with an explicit offset (for example `2026-01-02T03:04:05-03:00`), never `Z`.
- `tipo` is one of `job_event`, `dispatch`, `amend`, `session`, `decision`.
- `maquina` is the configured machine label; `projeto` is `<org>/<repo>`, or an empty string (the key is always present) when the project is unknown, for example a `job_event` from a job that was not started through `nau`.
- `job_id` is the job id or `null`.
- `idempotency_key` is `<job_id>:<event seq>` for `job_event` (the same key the job contract uses) and `<maquina>:<outbox seq>` for every other record.
- `dados` carries the original job event unchanged for `job_event` (unknown fields included, so fields added later by `soho` are never dropped), or the command payload for the other types.

A `job_event` whose `(job_id, event seq)` is already in the outbox is not appended again: the wake hook, `sync` and push retries converge on the same content. Job events are deduplicated by their idempotency key, and `last_event_seq` is the highest event seq up to which every event of the job is stored, so `sync` refetches any gap.

## Pull channel

`nau outbox [--since <seq>] [--wait <ms>]` is read-only and needs no key and no network endpoint. It prints the records with `seq > since` as JSON lines and always ends with the trailer:

```json
{"outbox":"fim","ultimo_seq":N,"entregue_seq":M}
```

When nothing is new, it long-polls until a record with `seq > since` appears or `--wait` elapses (at most 600000 ms). A `--since` beyond the last seq prints only the trailer. It works under `HERDR_HERMES_NOWRITE=1` and writes nothing.

## Push

Push happens only when `dispatcher_url` is configured; otherwise it is disabled (`push_disabled`) and the records stay in the outbox for a pull. Each pending record (those with `seq` greater than the delivery cursor) is sent as one request, in `seq` order, one at a time:

- Method and body: `POST <dispatcher_url>`, the outbox record line as the JSON body.
- Headers, exactly: `Authorization: Bearer <key>`, `Idempotency-Key: <idempotency_key>`, `Content-Type: application/json`, `User-Agent: herdr-hermes/<version>`.
- The key appears only in the `Authorization` header; errors and friction lines never contain it or the request body.

Response handling:

- 2xx — the record is delivered: the delivery cursor (`delivered_seq` in `cursor.json`) advances to its `seq`, and the next record is sent.
- 401/403 — the batch stops, the outcome `auth_rejected` is recorded in friction, and `sync`/`push` exit 41.
- 408, 429, 5xx, network errors and timeouts — the same record is retried at most 3 times (after 10 s, 30 s and 90 s, on the injected clock) for the retrying commands `push` and `sync`; if it still fails the batch stops with the outcome `unreachable`, the record stays pending and the command exits 42. `wake` makes a single attempt with no retries and exits 0 either way.
- 3xx (redirects are never followed) and any other 4xx — the batch stops without retry, the outcome `rejected` is recorded and the command exits 42.

The URL must be HTTPS unless the host is a loopback address (tests); redirects are not followed. Sending is best effort and never affects a job: a failed push leaves the records in the outbox for a later push or pull, and a crash between a successful POST and the cursor update re-sends the same record with the same `Idempotency-Key`, which the receiver deduplicates.

## last_push

`cursor.json` holds the delivery cursor and the last push result:

```json
{"delivered_seq":N,"last_push":{"ts":"2026-01-02T03:04:05-03:00","status":"ok","code":200}}
```

`status` is one of `ok`, `auth_rejected`, `unreachable`, `rejected`. Recording a new push result preserves `delivered_seq`; advancing the cursor preserves the last result. The `plugin bridge status` action and `doctor` read it.

## Wake hook

`nau wake` is meant to be configured as the `soho` machine key `job_wake_cmd=nau wake`. When the backend wakes the dispatcher for a job event, it runs `wake` with the event JSON on stdin (cap 64 KiB) and the four environment variables `SOHO_JOB_ID`, `SOHO_JOB_SEQ`, `SOHO_JOB_EVENT` and `SOHO_JOB_IDEMPOTENCY_KEY`. Only these variables are read, and error messages name the corresponding variable. `wake` appends one `job_event` record (deduplicated by `(job_id, event seq)`), then attempts exactly one push when `dispatcher_url` is set. It exits 0 as soon as the record is durable, whatever the push result, and prints `{"seq":N,"duplicado":bool,"enviados":N,"pendentes":N}` (`seq` is the outbox seq, 0 when the event was already recorded). It exits 2 for malformed input (missing or invalid job id, stdin over the cap, invalid JSON, a missing or mismatched event seq, a mismatched idempotency key, or an empty machine label), and also exits 2 when the record could not be made durable (the state directory or the outbox cannot be opened or written). A job unknown to the local bookkeeping is tracked on the fly, so interactive jobs not started through `nau` are covered too. Job events are deduplicated by their idempotency key, and `last_event_seq` is the highest event seq up to which every event of the job is stored, so `sync` refetches any gap.

## Job forwarding

`nau job <sub> [args]` forwards the dispatcher-facing job subcommands to `soho job <sub> [args]` as an argv subprocess (never a shell): `start`, `status`, `wait`, `events`, `collect`, `amend`, `send`, `ack`, `cancel`, `close`, `list`. The internal subcommands `supervise`, `checkpoint` and `note` are refused with exit 2 without running anything.

Forwarding is transparent: stdin is streamed through, stdout and stderr are copied unchanged, and the exit code of `soho` is returned unchanged. Because the child's lifetime is bounded by the deadline above, the forwarder waits for the delivery of everything the child wrote to reach the consumer's stdout and stderr before it returns: a consumer that stops reading holds the command until it reads again (exactly as it would hold `soho` run directly), so no byte the child wrote is dropped and no exit code is returned before the delivery ends. Before forwarding, `nau` applies the contract input limits so an oversize or malformed input never reaches the subprocess: `--id` must match `^[A-Za-z0-9._-]{1,64}$`, `--repo` is `<org>/<repo>` with each part `^[A-Za-z0-9._-]{1,100}$`, `--base` is `^[A-Za-z0-9._/-]{1,100}$` with no `..`, `wait --timeout` and `events --wait` accept at most 600000 ms, and the stdin caps are 256 KiB for `start`, 64 KiB for `amend` and 16 KiB for `send`; a violation exits 2 without running the subprocess. Every forwarded subprocess runs with a context deadline (the command's own bound plus a margin: for `wait`, the `--timeout` value plus 30 s, or 600000 ms plus 30 s when `--timeout` is absent; for `events`, the `--wait` value plus 30 s, or 120 s when `--wait` is absent; 120 s for the others); a missing or not runnable `soho` exits 4 with `{"status":"unavailable","motivo":"soho not found"}`. When the deadline kills the subprocess, `job` exits 4 and, if the subprocess wrote nothing to stdout, prints `{"status":"timeout","motivo":"soho job <sub> did not finish within <bound>"}`; no bookkeeping happens on that path.

Bookkeeping happens only after a successful forward, never changes the forwarded exit code or stdout, and its failures go to friction: a `start` that exits 0 (or answers with `duplicate_of`) adds the job to the local bookkeeping and appends one `dispatch` record — but a `duplicate_of` response for an already tracked job adds no second job and no second record; `amend` and `send` that exit 0 append one `amend` record each (`dados.refs.tipo` is `amend` or `nota`); a `close` that exits 0 marks the job closed. When the machine label is empty, bookkeeping writes a friction line instead of a record and the forward still succeeds.

While the job orchestrator is in a dialog or a question, the supervisor holds a pending `amend` or `send`, records one `control_held` event per request, and lists the request in the `status` and `wait` lines' `held_controls`; while a cancel is pending, `amend`, `send` and `checkpoint` are refused with exit 2 (`job: cancel_pending: the job is being canceled`), and `nau job` forwards both the status lines and the refusal without interpreting them. On a clean end, the automatic close removes the job's worktree only when it is clean (no change and no untracked file) and pushed (`HEAD` equals `origin/job/<id>`) and closes the job's Herdr workspace (`workspace_closed`); the job's folder stays on the machine (`folder_retained`); a worktree that is not removed is kept and fires a `needs_attention` event.

A dispatcher that reaches the node over ssh can bind its key to `nau gate`, the forced command that lets through only `nau job <sub> ...` and `nau outbox ...` and refuses the rest with exit 2 before anything runs; the contract, the refusal classes and the `authorized_keys` lines are in [`gate.md`](gate.md).

## Sync

`nau sync [--job <id>] [--push-only]` first checks that the backend has the `ephemeral_job` and `job_events` capabilities (skipped with `--push-only`); a missing capability exits 43 with `{"status":"capabilities_missing",…}`, and a `--job` unknown to the local bookkeeping exits 3 with `{"status":"not_found"}`. For each tracked job (or the given one) it runs `soho job events --id <id> --since <prefix>` without waiting, where `<prefix>` is the contiguous stored prefix recomputed from the outbox (the recorded `last_event_seq` is never the `--since` value), appends the new events in order, updates the job's `estado` from the events trailer and marks the job terminal when the trailer state is a terminal one (`done`, `failed`, `timeout`, `canceled`, `collected`, `closed`); a closed job is skipped only while it has no gap, and a closed or terminal job is still synced while the outbox shows a stored event above the contiguous stored prefix or the recorded `last_event_seq` above the stored records; a terminal job without a gap keeps being synced until a `workspace_closed` event of the job whose event `seq` is greater than the highest event `seq` of the job's stored `terminal` records (with no stored `terminal` record, any `workspace_closed` of the job ends the job) is recorded in the outbox, the job is closed, or the 1-hour post-terminal grace counted from the job's `terminal_seen_at` is over (the sync writes `terminal_seen_at` with its own clock when the job enters a terminal state, rewrites it when the stored value is empty or does not parse, and clears it when the job goes back to a non-terminal state), so that the automatic-close events reach the outbox; with the automatic close switched off the `workspace_closed` never comes and only the 1-hour grace ends the job. A per-job events failure goes to friction and the loop continues. Job events are deduplicated by their idempotency key, and `last_event_seq` is the highest event seq up to which every event of the job is stored, so `sync` refetches any gap. It then pushes the pending records and prints:

```json
{"jobs":N,"novos":N,"enviados":N,"pendentes":N}
```

with exit 0, 40, 41 or 42 according to the push outcome; on a non-zero outcome the line carries the counts plus a `status` field.

## Sessions

`nau session start|update|end` manages the node's open session records. `--projeto <org>/<repo>` is required (each part `^[A-Za-z0-9._-]{1,100}$`) as is the machine label. `start` takes `--card`, `--branch` and `--job`; a repeated `start` for the same project and branch (or job) updates the stored session and still appends a record (the receiver deduplicates by content). `update` takes `--branch`, `--card`, `--estado` and `--pr`; `end` takes `--estado` and `--pr` and marks the session closed. Every call appends one `session` record whose `dados` is `{"acao":"start|update|end","projeto","branch","card","job","estado","pr"}` (empty fields omitted), with the record's `job_id` set to the session's job when there is one and `--estado` cut at 280 code points. The record never carries a person field; the receiver derives the person from the authenticated key.

## Decisions

`nau decision --projeto <org/repo> --escopo global|projeto --motivo <text> [--job <id>] [<resumo>|-]` records a decision taken outside a job (job decisions already travel as `job_event` records). `--escopo` other than `global` or `projeto` exits 2, `--motivo` is required, and the `resumo` comes from the positional argument or from stdin when it is `-` (cap 16 KiB, trailing newline trimmed). `resumo` and `motivo` are cut at 280 code points, by whole code points. It appends one `decision` record whose `dados` is `{"resumo","motivo","escopo"}` and prints `{"seq":N}`.

## Doctor

`nau doctor` is read-only (it works under `HERDR_HERMES_NOWRITE=1`) and prints one JSON line:

```json
{"herdr_soho":{"found":true,"capabilities":"{...}","ephemeral_job":true,"job_events":true},"machine_label":true,"dispatcher_url":true,"key_configured":false,"wake_hook":"true","outbox":{"ultimo_seq":N,"entregue_seq":M,"pendentes":K}}
```

`capabilities` is the raw `soho capabilities --json` line or null. `wake_hook` is `true` when the effective `job_wake_cmd` value has a first field that is `nau` or a path whose last element is `nau` (a first field ending in a separator is never accepted, and on Windows the `.exe` form of the last element also counts in any casing, the backslash separator counts, and a leading letter volume prefix such as `C:` is dropped before the last element is taken), a second field that is exactly `wake` and any later fields; `false` when the value is empty or different; and `unknown` when the configuration cannot be read or parsed. `doctor` first probes `soho config get job_wake_cmd`, whose exit 0 first stdout line is the effective value and an empty line means unset; on any other exit it reads the `soho config` listing, where a listed `job_wake_cmd` row wins, else the path on the `user file:` row is read with the backend's own single-file config rules (the file is decoded as UTF-8 with CRLF line endings as LF, the line is cut at its first `#` wherever it appears, the remainder is trimmed and a blank line or a line without `=` is ignored, the key before the first `=` is trimmed and normalized so that `.` and `-` become `_`, only letters, digits and `_` remain and the case is kept, the value after it is trimmed and a quoted value loses one pair of double quotes, and the last occurrence of the key wins), and a missing file or a file without the key gives `false` while an unreadable file gives `unknown`. `doctor` exits 43 when the backend is missing or lacks either capability, else 0. When the stored `herdr_soho_bin` is empty (automatic), `doctor` warns on stderr that an ssh forced command (`nau gate`) may run with a minimal `PATH` that omits the backend and that setting `herdr_soho_bin` to an absolute path pins it; the JSON line and the exit code are unchanged. The `config` probes of `doctor` (`config get job_wake_cmd` and `config`) run the backend with exactly one `SOHO_NOWRITE=1` in the child environment, removing inherited values of that variable first (case-insensitively on Windows).

`nau config set [--if-automatic] <key> <value>` rejects CR, LF or NUL in ordinary values with exit 2 before accessing the configuration. The flag is valid only for `herdr_soho_bin` (another key exits 2); it rechecks the stored automatic value and validates the candidate under the same exclusive `config.lock` used by every `config set`, before atomic replacement. Explicit selections and unsafe candidates are skipped with exit 0 and a stderr explanation, retaining the configuration bytes. Candidate paths must be absolute regular executable files without CR, LF or NUL and without leading or trailing spaces (`Load` trims them); supplied paths, resolved paths and intermediate symlink destinations are checked. On POSIX their file and ancestors must be owned by the current user or root and not writable by group or others, except a sticky directory whose next entry has a trusted owner; on Windows a write, modify, full-control, WRITE_DAC or WRITE_OWNER allow ACE for Everyone, Users, Authenticated Users or Interactive is refused on the executable and the directory that holds it; on the ancestors above, only the rights that can replace an entry of the chain (full control, WRITE_DAC, WRITE_OWNER, DELETE and delete-child) are refused, so the default volume-root grant to create a folder does not block the pin. A null DACL is refused; ordinary, object and callback allow ACEs are checked, including inherited grants that apply to the object. Checks describe permissions at pin time, not protection from later changes by trusted owners. The directory and lock file may be created before refusal, but the configuration file is untouched. Successful writes retain comments, blank lines, order and each existing LF or CRLF terminator, replacing only the last duplicate record (the record `Load` uses); missing keys append using the first existing line terminator, or LF for an empty file. Replacement is atomic with mode 0600, and new configuration directories use mode 0700. Stdout remains exactly one JSON line.

## Plugin

The optional plugin (`plugin/herdr-plugin.toml`) runs the CLI as one-shot entry points; it keeps no second copy of the outbox or push logic, and the CLI works fully without the plugin. Every `command` in the manifest is an argv array whose first element is `nau`.

- `nau plugin startup` (the `[[startup]]` hook) runs the `sync` logic for every tracked job and then the push step. It always exits 0 and never fails the Herdr server: a config, capability or machine-label failure writes one friction line and prints `{"status":"skipped","motivo":…}`; otherwise it prints the sync counts line (a push failure is a normal sync outcome, with its `status` field).
- `nau plugin event` (the `workspace.created` and `workspace.closed` hooks) reads `HERDR_PLUGIN_EVENT` and `HERDR_PLUGIN_EVENT_JSON` from the environment. A `workspace.created` for a workspace labeled `job-<id>` (id matching the contract id rule) tracks the job in the local bookkeeping when it is unknown (empty `projeto`) and then runs the `sync --job <id>` logic; a `workspace.closed` runs a final `sync --job <id>` for a tracked `job-<id>`, so terminal and cleanup events reach the outbox; a close event whose workspace carries no `job-<id>` label writes nothing for the bookkeeping. The plugin hook payload shape is not documented by Herdr and has not been verified live (no plugin was installed to observe it), so the parsers accept the socket subscription line (`{"event":"workspace_created","data":{…}}`), the event-stream forms of the bundled API schema and the flat or nested payloads; the event name comes from the envelope, else the data type, else the hook name, and any other name is refused. Other event names are ignored with nothing written; an unknown or empty `HERDR_PLUGIN_EVENT_JSON` shape writes one friction line. It always exits 0; failures go to friction.
- `nau plugin bridge status` (read-only action, exit 0) prints exactly one JSON line and nothing else: `{"jobs_abertos":N,"pendentes":N,"ultimo_push":…,"key_configured":bool}`, where `ultimo_push` is the `last_push` entry of `cursor.json` or `null`. It is read-only: it works under `HERDR_HERMES_NOWRITE=1` and creates no files.
- `nau plugin bridge sync` (action) prints to stderr what it will do — the number of open tracked jobs and of pending outbox records — then runs the `sync` logic and prints its JSON line; its exit code is the sync exit code (0, 2, 40, 41, 42, 43). It is a writing command and is refused under `HERDR_HERMES_NOWRITE=1` with exit 2.


## Routing

`nau route [--machine <label>]` is the dispatcher-side machine choice: it runs on the host the dispatcher uses to reach the fleet, probes the configured fleet read-only, picks one machine and prints it as one JSON line. The choice is advisory — `route` never starts, moves or replays a job — and the dispatcher then runs `job start` on the chosen machine through its own remote execution channel, never re-sending the dispatch to another machine after a transport loss or timeout. It is read-only (it works under `HERDR_HERMES_NOWRITE=1` and writes nothing), exits 2 for bad usage, an invalid configuration, `route_machines` not configured, or a requested machine that is not configured, and 4 when no machine is available. The configuration keys, the probe, the selection and tie-break, the outputs and exit codes, and the safety rules are in [`routing.md`](routing.md).

## Exit codes

| code | meaning |
|------|---------|
| 0 | success (forwarded commands: whatever `soho` returned) |
| 2 | bad usage, input limit exceeded, unknown config key, internal job subcommand refused, a writing command under `HERDR_HERMES_NOWRITE=1`, no user config directory (`no_config_dir`), or a `wake` event that could not be made durable |
| 3 | unknown job id in `nau` bookkeeping (`sync --job`) |
| 4 | `soho` not found, not runnable, or killed by the forwarding deadline, or no eligible machine available (`route`) |
| 40 | no API key configured (push required) |
| 41 | API key rejected by the dispatcher (401/403) |
| 42 | dispatcher unreachable or retries exhausted; records stay pending |
| 43 | `soho` lacks the `ephemeral_job` or `job_events` capability (`doctor`, `sync`) |
| 44 | the pre-dispatch check refused the request: duplicate work or a hold (`jobs check`) |

Forwarded `job` subcommands return the `soho` exit code unchanged; codes 40–44 are never produced by a forwarded command.

## NOWRITE

`HERDR_HERMES_NOWRITE=1` makes the CLI read-only. The read-only commands work normally and write nothing: `outbox`, `route`, `doctor`, `auth status`, `config get` and `config list`, `capabilities`, `version`, `help`, `plugin bridge status`, and the read-only `job` subcommands (`status`, `wait`, `events`, `collect`, `list`, which also skip bookkeeping). Every writing command (`job start|amend|send|ack|cancel|close`, `wake`, `sync`, `push`, `session`, `decision`, `auth login`, `auth logout`, `config set` and `plugin bridge sync`) refuses with exit 2 and `{"status":"nowrite","motivo":"HERDR_HERMES_NOWRITE=1"}` before any side effect. Exception: the plugin entrypoints `plugin startup` and `plugin event` exit 0 under NOWRITE, print `{"status":"skipped","motivo":"HERDR_HERMES_NOWRITE=1"}` and write nothing.

## Key security

The per-user API key is issued to the person by the dispatcher operator; phase 1 only stores and sends it.

- Storage: one file in the user configuration directory (`credentials`), written atomically with mode 0600, behind a credential-store interface so an OS keychain backend can replace it later without changing the commands.
- The key is never printed, logged, written to the outbox, passed to any subprocess environment, typed into a pane, or included in an error message. It appears only in the `Authorization` header of push requests.
- `nau auth login --key -` is the only way to set the key: it reads from stdin (or from an interactive prompt with echo off when stdin is a terminal), and refuses an empty key, a key containing whitespace, and a key over 4 KiB. No flag value or environment variable is ever accepted as the key.
- `auth status` prints only `{"configured":true|false,"store":"file"}`; `auth logout` removes the key.
- Commands that need the key with none configured: `wake` records the event and exits 0; `sync`/`push` exit 40 with `{"status":"auth_missing"}`. Pulling through `outbox` never needs a key.
