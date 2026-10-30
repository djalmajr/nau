# Pre-merge validation

This document is the pre-merge validation checklist and evidence contract for Nau. Before a change is merged, someone must run the real `nau` CLI end to end on a real development node, for the exact reviewed commit, and paste the actual evidence on the linked tracking card. This checklist holds no results: it only says what to run and what to record.

## 1. Purpose and scope

- This is an actual end-to-end CLI validation on a real development node, for the exact reviewed commit SHA. It is in addition to code review and CI, never a replacement for either: green unit tests, a green CI run or a clean review do not satisfy it, and it does not satisfy them.
- In scope: the candidate `nau` binary and its three read surfaces — `nau version`, `nau config` and `nau doctor`.
- Out of scope: production deployment or install, creating, rotating, reading or logging secrets, and any production service.
- The public repository holds only this checklist. The real evidence (command outputs and versions) is posted on the linked tracking card before merge. A path, a link, a count or a report assertion alone is not evidence.
- The validation fails closed: anything that is missing, unreadable or not proved is `FAIL`, `NOT RUN` or `BLOCKED` with its reason, never `PASS`.
- Every item has one of the results `PASS`, `FAIL`, `NOT RUN` or `BLOCKED`, with the reason. Required items, always: 1 build and version, 2 isolated state, 3 config and 4 doctor.

## 2. Preconditions

Check every precondition first and record the exact command, version and output of each. If any prerequisite is missing the validation is `FAIL` or `BLOCKED` and the evidence records why. Make no global search path change.

- Native build of the candidate at the reviewed SHA, built on the development node itself with `go build -o <dir>/nau ./cmd/nau` into a scratch directory. Record `git rev-parse HEAD` and the tree hash, then run `<dir>/nau version` and `<dir>/nau help`. The version output must carry the reviewed SHA. Do not install the binary globally and do not change `PATH` for the whole machine.
- An isolated test state that does not hide the backend or Git authentication. `nau` keeps its configuration in the `nau` directory under the Go user configuration directory: on macOS `$HOME/Library/Application Support/nau` (`XDG_CONFIG_HOME` is ignored on macOS), on Linux `$XDG_CONFIG_HOME/nau` or else `$HOME/.config/nau`, on Windows `%AppData%\nau`. Before the first write (the first `nau config set`), record the resolved `nau` path and the non-secret selector values (the operating system, and which of `HOME`, `XDG_CONFIG_HOME` or `AppData` decides the path), and record the listing of the whole config root: `nau` reads only the `nau` directory and never touches any other directory under it, so the listing must be unchanged afterwards. Never relocate `HOME` or the configuration roots, copy credentials or configuration, or silently restore real data afterwards. `herdr_soho_bin` only selects the backend executable. Only after that check, set the machine label with `<dir>/nau config set machine_label <label>`, and read back `<dir>/nau config list`.

## 3. Items

1. Build and version: the `nau version` and `nau help` outputs from the precondition, with their exit codes.
2. Isolated state: the resolved `nau` path recorded before the first write, and the before and after listing of the config root proving that no directory other than `nau` was touched.
3. Config: `nau config set machine_label <label>`, `nau config get machine_label` and `nau config list`, with their outputs and exit codes; the read-back equals the set value, and an old `config` file that carries a key removed from the configuration still reads (a removed key is ignored on read and refused on set).
4. Doctor: `nau doctor` (read-only, exits 0), with its output and exit code: one JSON line carrying the version, the config directory, the machine label and the soho backend block (the resolved binary and the version the backend reports).

## 4. Evidence template

Post the filled-in template, with the real pasted outputs, on the linked tracking card before merge. Leave a slot empty or write `NOT RUN` with the reason; never fill a slot with an expectation.

```text
Reviewed commit SHA:
Tree hash:
Native OS and architecture:
State isolation (resolved nau path, OS, which of HOME / XDG_CONFIG_HOME / AppData decides it, before and after listing of the config root):

nau version (command, stdout, exit):
nau help (first lines and the full output attached):
nau config set / get / list (commands, stdout, exit):
nau doctor (stdout, exit):

Result per item (PASS | FAIL | NOT RUN | BLOCKED, with the reason):
  1 build and version (required):
  2 isolated state (required):
  3 config (required):
  4 doctor (required):
Overall result:
```

- The overall result is `PASS` only when every required item is `PASS`. Any required item that is `FAIL`, `NOT RUN` or `BLOCKED` forbids an overall `PASS`, whatever reason is recorded, and the change is not validated.
- CLI output pasted as text is enough.

## 5. Cleanup

- Stop only the processes the validation started itself, identified by the process ids it recorded when it started them. Never stop, signal or kill anything else.
- Keep the test `nau` directory and every record in it; removing it is a decision of the owner, taken later by hand. No directory other than the test `nau` directory is touched by this validation.
