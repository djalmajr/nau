# Pre-merge validation

This document is the pre-merge validation checklist and evidence contract for Nau. Before a change is merged, someone must run the real `nau` CLI end to end on a real development node, and the real hub when the change touches it, for the exact reviewed commit, and paste the actual evidence on the linked tracking card. This checklist holds no results: it only says what to run and what to record.

## 1. Purpose and scope

- This is an actual end-to-end CLI validation on a real development node, for the exact reviewed commit SHA. It is in addition to code review and CI, never a replacement for either: green unit tests, a green CI run or a clean review do not satisfy it, and it does not satisfy them.
- In scope: the candidate `nau` binary and its three read surfaces — `nau version`, `nau config` and `nau doctor` — and, when the change touches the hub, `nau hub serve` and `nau hub recover` against a disposable database.
- Out of scope: production deployment or install, creating, rotating, reading or logging secrets, and any production service. The hub item uses only synthetic values generated in the run, and never prints them.
- The public repository holds only this checklist. The real evidence (command outputs and versions) is posted on the linked tracking card before merge. A path, a link, a count or a report assertion alone is not evidence.
- The validation fails closed: anything that is missing, unreadable or not proved is `FAIL`, `NOT RUN` or `BLOCKED` with its reason, never `PASS`.
- Every item has one of the results `PASS`, `FAIL`, `NOT RUN` or `BLOCKED`, with the reason. Required items, always: 1 build and version, 2 isolated state, 3 config and 4 doctor. Required when the change touches `internal/hub/`, `internal/cli/hub_*`, `web/` or the image: 5 hub.

## 2. Preconditions

Check every precondition first and record the exact command, version and output of each. If any prerequisite is missing the validation is `FAIL` or `BLOCKED` and the evidence records why. Make no global search path change.

- Native build of the candidate at the reviewed SHA, built on the development node itself with `go build -o <dir>/nau ./cmd/nau` into a scratch directory. Record `git rev-parse HEAD` and the tree hash, then run `<dir>/nau version` and `<dir>/nau help`. The version output must carry the reviewed SHA. Do not install the binary globally and do not change `PATH` for the whole machine.
- An isolated test state that does not hide the backend or Git authentication. `nau` keeps its configuration in the `nau` directory under the user configuration directory: on macOS and Linux `$XDG_CONFIG_HOME/nau` or else `$HOME/.config/nau`, on Windows `%AppData%\nau`. Before the first write (the first `nau config set`), record the resolved `nau` path and the non-secret selector values (the operating system, and which of `HOME`, `XDG_CONFIG_HOME` or `AppData` decides the path), and take a sorted listing of the names in the whole config root, hidden ones included, into the scratch directory. Do not post the complete listing, since its names show what is installed on the machine and the proof does not need them: post its entry count and SHA-256 hash, and the diff of item 2. `nau` reads only the `nau` directory and never touches any other directory under it, so the listing must be unchanged afterwards, apart from a `nau` entry that did not exist before. Never relocate `HOME` or the configuration roots, copy credentials or configuration, or silently restore real data afterwards. `herdr_soho_bin` only selects the backend executable. Only after that check, set the machine label with `<dir>/nau config set machine_label <label>`, and read back `<dir>/nau config list`.

## 3. Items

1. Build and version: the `nau version` and `nau help` outputs from the precondition, with their exit codes.
2. Isolated state: the resolved `nau` path recorded before the first write, the entry count and hash of the config root listing before and after, and the output of `diff` between the two listings, which is empty when nothing changed and otherwise shows only the changed names. This proves that no entry of the config root other than `nau` appeared or disappeared, not that the content of the other directories is the same. The only accepted change is a `nau` entry that did not exist before; any other changed name is `FAIL`.
3. Config: `nau config set machine_label <label>`, `nau config get machine_label` and `nau config list`, with their outputs and exit codes; the read-back equals the set value, and an old `config` file that carries a key removed from the configuration still reads (a removed key is ignored on read and refused on set).
4. Doctor: `nau doctor` (read-only, exits 0), with its output and exit code: one JSON line carrying the version, the config directory, the machine label and the soho backend block (the resolved binary and the version the backend reports).
5. Hub: the candidate binary against a disposable `sqld`, in a temporary directory and on a free port, with synthetic values generated in the run. Record each command with its exit code or HTTP status, never a value of a secret.
   1. Real database: `NAU_TEST_SQLD_URL=<url> go test -count=1 -timeout=30m -tags sqld ./internal/hub/...` passes.
   2. Start: `nau hub serve --listen 127.0.0.1:<port> --recovery-listen 127.0.0.1:<port>` with `NAU_HUB_DB_URL`, `NAU_HUB_SESSION_KEY`, `NAU_HUB_OWNER_NAME`, `NAU_HUB_OWNER_EMAIL`, `NAU_HUB_OWNER_PASSWORD` and `NAU_HUB_ROOT_TOKEN`. The serving line names both ports, `GET /api/health` answers ok, and `GET /api/version` carries the reviewed SHA.
   3. The seeded owner: `POST /api/session` answers 200, and `GET /api/session` with that cookie answers the owner.
   4. The public port: `POST /api/session/root-token` with the right token answers 404.
   5. Recovery: `nau hub recover --url http://127.0.0.1:<recovery port>` with a remote url exits 2 with no request; with a wrong token it exits 4 and the password still works; with the right token it exits 0, the old cookie and the old password are refused, and the new password logs in.
   6. The state a user has: SIGTERM closes both ports, and a new start on the same database, with other owner variables, keeps the owner and its password.
   7. The login limit: after the attempts of the owner are spent (429), the right token exits 0 and the login stays at 429, since the recovery does not reset the limit.
   8. When `web/` changed: the changed screens in a real browser, served by the candidate with `--web`, in the light and dark themes. Record what was seen at each screen.

## 4. Evidence template

Post the filled-in template, with the real pasted outputs, on the linked tracking card before merge. Leave a slot empty or write `NOT RUN` with the reason; never fill a slot with an expectation.

```text
Reviewed commit SHA:
Tree hash:
Native OS and architecture:
State isolation (resolved nau path, OS, which of HOME / XDG_CONFIG_HOME / AppData decides it, entry count and hash of the config root listing before and after, diff between the two listings):

nau version (command, stdout, exit):
nau help (first lines and the full output attached):
nau config set / get / list (commands, stdout, exit):
nau doctor (stdout, exit):
nau hub, sub-items 1 to 8 of item 5 (commands, exit codes and HTTP statuses, the SHA from /api/version; no secret values):

Result per item (PASS | FAIL | NOT RUN | BLOCKED, with the reason):
  1 build and version (required):
  2 isolated state (required):
  3 config (required):
  4 doctor (required):
  5 hub (required when the change touches the hub):
Overall result:
```

- The overall result is `PASS` only when every required item is `PASS`. Any required item that is `FAIL`, `NOT RUN` or `BLOCKED` forbids an overall `PASS`, whatever reason is recorded, and the change is not validated.
- CLI output pasted as text is enough.

## 5. Cleanup

- Stop only the processes the validation started itself, identified by the process ids it recorded when it started them. Never stop, signal or kill anything else.
- Keep the test `nau` directory and every record in it; removing it is a decision of the owner, taken later by hand. No directory other than the test `nau` directory and the disposable database directory of item 5 is touched by this validation.
- The disposable database holds only synthetic data: remove its directory after stopping the processes.
- Remove the config root listings from the scratch directory once the evidence is posted.

## 6. Test layers

What proves a change before this validation, each layer with the command that runs it. A layer with no automated run says so, and the gap is tracked on a card.

| Layer | Command | Runs in |
|---|---|---|
| Format, vet and build | `gofmt -l` on the tracked Go files, `go vet ./...`, `go build -o <dir>/nau ./cmd/nau` | CI, three systems |
| Unit and handlers, with the memory store | `go test -timeout=30m ./...` | CI, three systems |
| Race detector | `go test -race -timeout=30m ./...` | CI, three systems |
| Integration with the real database | `NAU_TEST_SQLD_URL=<url> go test -count=1 -timeout=30m -tags sqld ./internal/hub/...` | Item 5 by hand; not in CI yet |
| Web: types, lint, unit tests and build | `bun run typecheck`, `bun run lint`, `bun run test`, `bun run build`, in `web/` | CI |
| Image smoke against the real database | the `Build and smoke` job of `image.yml`: health, index page, owner login, recovery absent from the public port | CI |
| Mutation | one mutation per acceptance criterion, in a throwaway copy, named in the review | By hand, per change; no tool yet |
| Browser end to end | none: item 5 by hand | Not automated yet |
| Fuzz on untrusted input | none | Not automated yet |
| Contract between the hub API and `web/` | none: the web types are written by hand | Not evaluated yet |
