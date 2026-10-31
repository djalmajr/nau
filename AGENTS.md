# Nau

Nau is a read-only hub of the coding agents at work: which agents are working, on which machines, for which developers. The repository holds the Go CLI in `cmd/nau`. The hub is under construction; the soho inventory is the only source.

- Go is the runtime of the CLI and the hub and their test implementation, with no Node.js, Bun, Bash or interpreter fallback. The one exception is the web UI in `web/`: a Vite and React app that Bun builds into static files the hub serves. Bun runs only to install, test and build it, never at runtime. Well-established, maintained community libraries are appropriate when a dependency is justified; follow normal engineering and security best practices when adding or updating one.
- `soho` (the backend) is probed only through its public CLI, executed as an argv subprocess with a context deadline and `WaitDelay` — never a shell, and never by reading or writing backend state files.
- Every command prints exactly one JSON line on stdout; diagnostics go to stderr; errors are `{"status":"<code>","motivo":"<text>"}` on stdout with the exit codes `0`, `2`, `4`.
- The config file is written atomically (temp file + rename, mode 0600) under the `nau` directory of the user configuration directory.
- Keep the code and the documentation portable: no machine paths, hostnames, IPs, private service names, provider, model or tool names, tracker URLs, credentials or person names in code, tests, docs or commit messages; no AI attribution.
- Write all documentation in English. Keep each prose paragraph and list item on one physical line.
- Failing test before implementation; tests are hermetic (no network beyond loopback, injected clocks, no real `soho`), finish in seconds and never hang.
- Run the checks before shipping: `gofmt -l .`, `go vet ./...`, `go test ./...` and `go test -race -timeout=30m ./...`; cross-compile for linux, macos and windows — cross-compilation is not execution proof.
