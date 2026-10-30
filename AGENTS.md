# Nau

Nau is a universal messaging and registry core: a neutral core (durable outbox, routing, job bookkeeping) with four boundary adapters, described in `README.md` and `docs/adapters.md`. The repository holds the Go CLI in `cmd/nau`, the vendored job contract in `docs/upstream/` and the protocol in `docs/protocol.md`.

- Go is the runtime of the CLI and the hub and their test implementation, with no Node.js, Bun, Bash or interpreter fallback. The one exception is the web UI in `web/`: a Vite and React app that Bun builds into static files the hub serves. Bun runs only to install, test and build it, never at runtime. Well-established, maintained community libraries are appropriate when a dependency is justified; follow normal engineering and security best practices when adding or updating one.
- `soho` (the backend) is used only through its public CLI (`soho job …`, `soho send …`, `soho capabilities --json`, `soho config`), executed as an argv subprocess with a context deadline and `WaitDelay` — never a shell, and never by reading or writing backend state files.
- The per-user API key never leaves the credential store and the `Authorization` header: it is never printed, logged, written to the outbox, passed to a subprocess environment, included in an error message, or accepted as a flag value or environment variable. `auth login --key -` is the only way to set it.
- Every command prints exactly one JSON line on stdout (`outbox` prints JSON lines plus a trailer); diagnostics go to stderr; errors are `{"status":"<code>","motivo":"<text>"}` on stdout with the exit codes `0`, `2`, `3`, `4`, `40`, `41`, `42`, `43`.
- Timestamps are local time with an explicit offset, never `Z`; state files are written atomically (temp file + rename, mode 0600) and the state directory is mode 0700.
- `docs/upstream/job-contract.md` is vendored verbatim and never edited; `internal/jobapi/contract_test.go` fails when the vendored constants drift from the document, and the vendored types are the only dependency on the contract.
- Keep the code and the documentation portable: no machine paths, hostnames, IPs, private service names, provider, model or tool names, tracker URLs, credentials or person names in code, tests, docs or commit messages; no AI attribution.
- Portability exception: the four boundary adapter names may appear only in the adapter description in `README.md` and `docs/adapters.md` — nowhere else, and never with model names, accounts, endpoints or credentials.
- Write all documentation in English. Keep each prose paragraph and list item on one physical line.
- Failing test before implementation; tests are hermetic (no network beyond loopback, injected clocks, no real `soho` or dispatcher), finish in seconds and never hang.
- Run the checks before shipping: `gofmt -l .`, `go vet ./...`, `go test ./...` and `go test -race -timeout=30m ./...`; cross-compile for linux, macos and windows — cross-compilation is not execution proof.
