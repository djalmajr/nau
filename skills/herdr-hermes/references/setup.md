# Setup

One-time setup on a machine that will act as a `nau` node. Every step is local; nothing is sent until a push or a pull happens.

Names: the command is `nau`; the old `herdr-hermes` name is retired. The state directory and the skill `name:` keep the `herdr-hermes` name.

## Install

With Go 1.25 or newer, from a checkout of the module `github.com/djalmajr/nau`:

```text
go build -o <dir>/nau ./cmd/nau
```

Make sure the installed binary is on your `PATH` (and, for the optional plugin, on the Herdr server's `PATH`).

## Configure the machine

Set the machine label the outbox records are stamped with:

```text
nau config set machine_label <label>
```

Store the per-user API key the dispatcher operator issued you. `auth login` reads the key from standard input (or from an interactive prompt with echo off) and never accepts it as a flag value or an environment variable:

```text
nau auth login --key -
```

Point the node at the dispatcher. The URL must be HTTPS:

```text
nau config set dispatcher_url <https-url>
```

Until the URL is set, push is disabled and the outbox still works for a dispatcher that pulls it with `nau outbox`.

## Register the wake hook

In the `soho` machine configuration, set the key:

```text
job_wake_cmd=nau wake
```

With this set, every job event that wakes the dispatcher is also recorded in the outbox as soon as it happens. The backend passes `SOHO_JOB_ID`, `SOHO_JOB_SEQ`, `SOHO_JOB_EVENT` and `SOHO_JOB_IDEMPOTENCY_KEY`; `nau wake` reads only these variables.

## Register notification recipients (optional)

Native notifications reach only explicitly registered recipients; until the first `notify register`, no notification file is written. Register the project owner, the job orchestrator, the machine coordinator and the Herdr panes to watch:

```text
nau notify register owner --projeto <org>/<repo> --to <ref>
nau notify register orchestrator --job <id> --to <ref>
nau notify register coordinator --to <ref>
nau notify register watch --pane <ws:pane> --job <id>
nau notify register workspace --workspace <ws> --job <id>
```

`<ref>` is a `soho send` reference `<machine>/<ws>:<pane>` or a local agent name; it is validated, never guessed. `notify list` and `notify status` are read-only; `notify deliver`, `notify ingest agent-status|workspace`, `notify raise`, `notify ack` and `notify retry` write the ledger and are refused under `HERDR_HERMES_NOWRITE=1` with exit 2.

## Dispatcher host (optional)

The host the dispatcher uses to reach the fleet chooses machines with `nau route`. List the fleet in priority order with saved Herdr machine labels (`local` is the reserved label for the Herdr server of the host that runs `route`); a dispatch-only host that must never run jobs is simply not listed in the fleet:

```text
nau config set route_machines <label>,<label>
nau route
```

The optional keys are `route_disabled` (labels to exclude without editing the Herdr machine profiles), `route_orchestrator_name` (the orchestrator agent base name to count, default `orchestrator`), `route_probe_timeout_s` (the bound of each probe, 1..120 seconds, default 20) and `herdr_bin` (the `herdr` executable, default `herdr`). `route` is read-only and advisory: it prints the chosen machine as one JSON line and never starts, moves or replays a job; the dispatcher then runs `nau job start` on the chosen machine through its own channel and never re-sends the dispatch to another machine after a transport loss or timeout.

Run the dispatcher as the operating-system user that saved the Herdr machines, with that user's environment (the same `HOME` on macOS and Linux, the same user profile on Windows): `route` reads its configuration from that user's configuration directory and the saved machines only through `herdr machine list --json` in its own environment. Another environment usually sees an empty configuration, which makes `route` exit 2 before any probe, or an empty catalog, which turns every saved label of a configured fleet that is not excluded by `route_disabled` into `unsupported` (`unknown_machine`) while `local`, probed on its own, may still be `available`. Check it read-only first; `config get route_machines` must print the intended fleet:

```text
herdr machine list --json
nau config get route_machines
nau route
```

An orchestrator counts toward its machine's load only when its Herdr agent is named after the orchestrator base — `orchestrator` or `orchestrator-<n>` by default, `<base>` or `<base>-<n>` when `route_orchestrator_name` sets another base — as `soho init` names the calling agent; agents whose name does not match the base (with the default base, the workers `soho` spawns and unnamed agents) do not count, and a finished orchestrator counts until its pane or workspace closes.

## Dispatch key (optional)

A dispatcher that reaches this node over ssh can bind its key to the forced command `nau gate`, in the node's `authorized_keys`: `command="<absolute path to nau> gate",restrict ssh-ed25519 <public key> <comment>`, with an optional `from="<allowed source addresses>"` in front (on Windows, the entry goes in the administrators keys file with the absolute path of `nau.exe`). The gate reads `SSH_ORIGINAL_COMMAND` and lets through only `nau job <sub> ...` (the public subcommands) and `nau outbox ...`, with their own flags and no quoting; anything else exits 2 before it runs or reads stdin. Nothing writes the key line for you, and how each sshd handles the forced command is confirmed by real use on each node; the contract and the refusal classes are in `docs/gate.md` of the repository.

## Verify

```text
nau doctor
```

It reports, in one JSON line: whether the backend is found and has the `ephemeral_job` and `job_events` capabilities (an empty `herdr_soho_bin` resolves to `soho` on the `PATH`; non-empty values are explicit), whether the machine label and the dispatcher URL are set, whether the key is configured (boolean only), whether the wake hook is configured (`true`, `false` or `unknown`) and the outbox counts. It exits 43 when a capability is missing and 0 otherwise. The backend configuration probes run with `SOHO_NOWRITE=1`.
