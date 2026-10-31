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

Every command prints exactly one JSON line on stdout and its diagnostics on stderr; `help` and `nau` run with no arguments print the plain-text usage on stdout and exit 0. On bad usage the usage text goes to stderr and stdout still carries one JSON error line. The exit codes are `0`, `2`, `4`.

- `config get|set|list` — manage the machine configuration. `config set [--if-automatic] <key> <value>`.
- `doctor` — print the status line: the version, the config directory, the machine label and the soho backend block (read-only).
- `hub serve [--listen <host:port>] [--web <dir>]` — the central hub server: the health and version API and the static files, with the store in the remote database named by `NAU_HUB_DB_URL` (the optional token in `NAU_HUB_DB_TOKEN`).
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

## Status

The hub is under construction; the soho inventory is the only source.
