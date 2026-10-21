# Gate

`nau gate` is the forced command of the ssh key a dispatcher uses to start jobs on a node and to pull its outbox. The key is bound to this command in `authorized_keys`, so whatever the client asks to run reaches `nau gate` as text in `SSH_ORIGINAL_COMMAND`; the gate lets through only `nau job <sub> ...` (the eleven public job subcommands) and `nau outbox ...`, and refuses everything else with exit 2 before anything is read from stdin or run. The text comes from another machine, so it is treated as hostile input.

The gate is an allow-list, not a shell: it has no quoting, no escaping and no expansion. It does not write `authorized_keys`, generate keys, authenticate anyone or audit tamper-proof; the `friction.log` it appends to is a diary the node's user can edit.

## Contract

- Input: the text of `SSH_ORIGINAL_COMMAND`, ASCII, at most 4096 bytes. `nau gate` takes no arguments of its own (it is a usage error, exit 2, with the usage text on stderr and `{"status":"2","motivo":"usage: gate ..."}` on stdout). A missing or empty variable, or one with only spaces, is refused with class `empty`: an interactive session with this key gets a refusal, not a shell.
- Split: tokens are separated by ASCII spaces (0x20); a run of spaces, and spaces at the start and at the end, count as one separator. Any other white space (tab, CR, LF, vertical tab, form feed) is refused. A token has only `[A-Za-z0-9._/-]`: there is no quote and no escape in the grammar, so `;`, `|`, `&`, `$`, `` ` ``, `$(...)`, `%VAR%`, `^`, quotes, `\`, `=`, NUL and every byte of 0x80 and above are refused as a character outside the set.
- Program name: the first token is `nau` (`nau.exe` also on Windows). It is never executed and never looked up in `PATH`: the program that runs is the one the forced command names. A first token with a path, another case or another extension is refused.
- Accepted: `job` with one of `start`, `status`, `wait`, `events`, `collect`, `amend`, `send`, `ack`, `cancel`, `close`, `list`, each with only its own flags (table below), and `outbox` with `--since <n>` and `--wait <ms>`, both optional. The internal subcommands `supervise`, `checkpoint` and `note`, every other `job` word and every other `nau` command (`route`, `doctor`, `a2a`, `config`, `gate` itself, and so on) are refused.
- Flags: each flag appears at most once, as `--flag value`; `--flag=value` is refused. A value that starts with `-` is refused, so it cannot be read as the next flag, except the `-` that names stdin. `--id`, `--repo` and `--base` pass the same checks as `nau job` (`internal/jobapi`); `--mode` is `worktree` or `workspace`; `--brief` and the positional of `amend` and `send` accept only `-` (the body comes on stdin, never a file of the node); `--state` of `list` is exactly one of the contract's lifecycle states (`accepted`, `preparing`, `running`, `blocked`, `finishing`, `done`, `failed`, `timeout`, `canceled`, `collected`, `closed`, as vendored in `internal/jobapi`), in lower case; numbers are unsigned decimal integers that fit a signed 64-bit integer. Ranges and required flags are left to `nau job`, `nau outbox` and the backend, as when they are run directly.
- Pass-through: an accepted command runs in the same process through the CLI dispatch, with the process's own stdin, stdout, stderr and exit code. There is no `exec`, no second process of the gate and no translation of bytes. The input limits (256 KiB for `start`, 64 KiB for `amend`, 16 KiB for `send`), the deadlines and the bookkeeping are those of `nau job` and `nau outbox`, not repeated in the gate.
- Refusal: exit 2, one line on stderr, `nau: gate: <class>`, and `{"status":"2","motivo":"gate: <class>"}` on stdout, the same error shape the other commands use. Nothing is read from stdin and nothing is executed. The checks and the refusal need no configuration directory: without one the gate still refuses with these lines and only skips the log line, and an accepted `job` or `outbox` answers exactly as when run directly (`{"status":"no_config_dir",...}`, exit 2). The message is one of the fixed classes below and never repeats the client's command.
- Log: the gate appends to the `friction.log` of the state directory with the command name `gate`, and creates no other log. A refusal writes one line, `refused <class> len=<bytes> cmd="<shape>"`: the class, the length of the client's command and its shape, never its values. The shape splits the command on spaces and keeps a token only when it is a word of the gate's fixed vocabulary (`nau`, `nau.exe`, the `nau` top-level commands, the public and internal `job` subcommands and the flag names of the tables below); every other token, a flag value, a brief, a key, a token with a forbidden character or free text alike, is written as `?`, and the shape is cut at 120 bytes on a token boundary. For example `nau job start --id J1 --repo o/r --brief notes.md` is logged as `refused value len=49 cmd="nau job start --id ? --repo ? --brief ?"`. An accepted write (`start`, `amend`, `send`, `ack`, `cancel`, `close`) writes one line after it ends, `job <sub> id=<id> exit=<n>`. Reads (`status`, `wait`, `events`, `collect`, `list`, `outbox`) write nothing: the outbox is pulled every minute or so, and a line each time would drown the log. The stdin, the brief and any secret are never written. Under `HERDR_HERMES_NOWRITE=1` the gate writes nothing, and a writing subcommand is refused by `nau job` with the NOWRITE line.

## Refusal classes

The set is fixed. The first check that fails names the class, in this order.

| Class | Meaning |
|---|---|
| `empty` | the variable is absent, empty or only spaces |
| `too-long` | more than 4096 bytes |
| `bad-char` | a byte outside `[A-Za-z0-9._/-]` and the space, including every other white space, `=` and bytes of 0x80 and above |
| `program` | the first token is not `nau` (or `nau.exe` on Windows) |
| `command` | missing, or neither `job` nor `outbox` |
| `subcommand` | `job` with no subcommand, an internal one, or an unknown one |
| `flag` | a flag the subcommand does not have, a flag given twice, or a positional the subcommand does not take |
| `value` | a flag with no value or a value the flag does not accept, or a positional of `amend` or `send` that is not `-` or is given twice |

## Flags by subcommand

The table is the contract's command list (`docs/upstream/job-contract.md`), as the backend parses it.

| Subcommand | Flags with a value | Flags without a value | Positional |
|---|---|---|---|
| `start` | `--id`, `--repo`, `--base`, `--mode`, `--brief` (only `-`), `--timeout` | `--dry-run` | none |
| `status` | `--id` | none | none |
| `wait` | `--id`, `--timeout` | none | none |
| `events` | `--id`, `--since`, `--wait` | none | none |
| `collect` | `--id` | `--md`, `--verify` | none |
| `amend`, `send` | `--id` | none | `-` (optional) |
| `ack` | `--id`, `--upto` | none | none |
| `cancel` | `--id`, `--grace` | none | none |
| `close` | `--id` | `--force` | none |
| `list` | `--state` | none | none |
| `outbox` | `--since`, `--wait` | none | none |

## What the dispatcher sends

The dispatcher sends the plain command, the same text on every system: `nau job status --id <id>`, `nau job amend --id <id> -` with the body on stdin, `nau outbox --since <n> --wait <ms>`. It does not send the path of the program: with a forced command the string is not run, it only arrives in `SSH_ORIGINAL_COMMAND`, so a path with `$HOME` or `%USERPROFILE%` would arrive unexpanded and the gate refuses it.

## authorized_keys on Unix

Add one line to the `authorized_keys` of the user that owns the node's state, with the absolute path of the installed `nau`:

```text
from="<allowed source addresses>",command="<absolute path to nau> gate",restrict ssh-ed25519 <public key> <comment>
```

`command=` forces the gate whatever the client asks. `restrict` turns off the terminal, the forwardings and the other optional features of the key. The `from=` option is optional and recommended: it limits the key to the addresses the dispatcher connects from, which the owner fills in; leave it out where the addresses are not stable. The path must be the absolute path of the `nau` that was installed on the node.

## administrators_authorized_keys on Windows

For an administrator account the sshd reads the keys from the shared administrators file instead of the user's own file, with the access rights that file needs. The entry has the same shape, with the absolute path of `nau.exe`:

```text
command="<absolute path to nau.exe> gate",restrict ssh-ed25519 <public key> <comment>
```

Keep the path free of `%`, a double quote and non-ASCII characters, as the installer already requires for the service it creates; the sshd may pass the line through a command interpreter. The `from=` option goes in front exactly as on Unix.

## Confirm on the real node

How each sshd behaves cannot be proved here: the tests simulate `SSH_ORIGINAL_COMMAND` and run the real binary against a fake backend, but they never run ssh. Treat the following as open until it has been checked on each node, with the new key installed:

- `SSH_ORIGINAL_COMMAND` arrives with exactly the text sent: no expansion of `$` or `%`, no removed quotes, no trailing CR or LF. On Windows, confirm that the variable is set at all and how the string is quoted.
- `ssh -n <node> 'nau job status --id <unknown id>'` returns the same exit code and the same stdout as the command run locally on the node.
- `nau job amend --id <unknown id> -` with a body of exactly 64 KiB reaches the backend, and with one byte more is refused with exit 2 by `nau`; the stdin crosses the channel whole, with no end-of-line translation and no truncation.
- A session with no command (what `restrict` leaves an interactive login as) is refused with class `empty` and does not wait for stdin.
- A refused command (`nau config list`, `nau job note ...`, a `;` or a `$(...)`) returns exit 2 and the gate's line, and nothing runs.
- The state directory the gate sees is the one the node's user sees: the `ultimo_seq` of `nau outbox` through ssh equals the local one. Which `HOME`, `PATH` and, on Windows, `APPDATA` the forced command runs with, and whether the backend and the Herdr server are found from it.
- sshd runs the forced command with a minimal `PATH` that may omit the backend; `herdr_soho_bin` must then be an absolute path. The installers pin it when it is automatic, and `nau doctor` warns while it is automatic.
- A dropped connection does not end a pending `nau outbox --wait`: the gate process stays alive until the wait expires, so dispatchers should keep `--wait` short.
- Whether `command=` with the absolute path, and on Windows the shell the sshd uses and the rights on `administrators_authorized_keys`, behave as described here, and whether this version honors `restrict`.

Until then the previous access of the dispatcher stays in place: `nau route` probes the nodes through the Herdr machine transport, not through this gate, and `nau a2a` is not accepted by it either.

## Not covered

Writing `authorized_keys` or generating a key, `nau route`, `nau doctor`, `nau a2a` and `herdr` through the gate, quoting and expansion, the helper that sends the commands, and the remote channel check recorded as a TODO in the job forwarder, which changes only after the real use above.
