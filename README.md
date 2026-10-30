# Nau

Nau is a read-only hub of the coding agents at work: which agents are working, on which machines, for which developers.

## Install

The recommended path is the install script: it puts the `nau` binary in the bin directory from the ref and records `machine_label` while it is still empty. Prerequisites: Go 1.26 or newer on the `PATH`.

On macOS and Linux:

```text
sh scripts/install.sh [--ref main] [--bin-dir $HOME/.local/bin] [--machine-label <label>]
```

On Windows (PowerShell):

```text
powershell -File scripts\install.ps1 [-Ref main] [-BinDir $env:USERPROFILE\.local\bin] [-MachineLabel <label>]
```

On every install, the script requests `nau config set --if-automatic herdr_soho_bin <absolute path>` for the backend found on the `PATH`. An explicit selection or an unsafe candidate leaves the configuration bytes unchanged and emits a stderr warning or explanation with exit 0, so installation continues.

For development, from a checkout of the module `github.com/djalmajr/nau`:

```text
go build -o <dir>/nau ./cmd/nau
```

The binary must be on your `PATH`.

## Configuration

`nau` keeps its configuration in the `nau` directory under the user's configuration directory. Two keys exist:

- `machine_label` — the label the machine reports. Set it with `nau config set machine_label <label>`.
- `herdr_soho_bin` — the backend selection. Only the empty value is automatic and resolves to `soho` on the `PATH`; any stored non-empty value is explicit and used as is.

## Commands

Every command prints exactly one JSON line on stdout and its diagnostics on stderr; `help` and `nau` run with no arguments print the plain-text usage on stdout and exit 0. On bad usage the usage text goes to stderr and stdout still carries one JSON error line. The exit codes are `0`, `2`, `3`, `4`.

- `config get|set|list` — manage the machine configuration. `config set [--if-automatic] <key> <value>`.
- `doctor` — print the status line: the version, the config directory, the machine label and the soho backend block (read-only).
- `hub serve [--listen <host:port>] [--web <dir>]` — the central hub server: the health and version API and the static files, with the store in the remote database named by `NAU_HUB_DB_URL` (the optional token in `NAU_HUB_DB_TOKEN`).
- `connect <hub-url> [--name <nome>]` — connect this machine to the hub: it requests a pairing, shows the short code on stderr, polls the confirmation and stores the machine key in `hub.json` of the config directory (the name defaults to `machine_label`); the key never reaches the output or a log.
- `version` — print the version.
- `help` — print the usage as plain text (not JSON); running `nau` with no arguments does the same.

## Development

Run the checks before shipping:

```text
gofmt -l .
go vet ./...
go test ./...
go test -race -timeout=30m ./...
go build ./cmd/nau
```

The project is Go, with the web UI in `web/` built by Bun (never at runtime); well-established, maintained community libraries are used when a dependency is justified, following normal engineering and security best practices. Tests are hermetic (no network beyond loopback, injected clocks) and must not hang.

## Container image

The hub ships as a container image. Building it from the repository root bundles the `nau` binary and the web interface, which the hub itself serves at `/`:

```text
docker build -t nau-hub:<tag> .
```

The image runs as a non-root user on `gcr.io/distroless/static-debian12:nonroot`, listens on port `7440`, and starts `nau hub serve --listen 0.0.0.0:7440 --web /srv/web`; the cluster runs the health probes against `/api/health`, so the image carries no `HEALTHCHECK`.

The secrets always come from the environment, never from the image. The environment surface of the hub is:

- `NAU_HUB_DB_URL` — the URL of the sqld database the hub reads and writes.
- `NAU_HUB_DB_TOKEN` — the token for that database.
- `NAU_HUB_SESSION_KEY` — the key that signs the session cookies.
- `NAU_HUB_BOOTSTRAP_TOKEN` — the token that bootstraps the first account.
- `NAU_HUB_CRON_TOKEN` — the token that authorizes the cron routes.

Today `hub serve` reads `NAU_HUB_DB_URL` and `NAU_HUB_DB_TOKEN` from that list; the remaining variables are read by the hub's account and cron surfaces as they land.

## Status

The hub is under construction; the soho inventory is the only source.
