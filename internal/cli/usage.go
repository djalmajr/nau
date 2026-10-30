package cli

import (
	"fmt"
)

// usageTemplate is the usage text printed by `help`, with no arguments and
// on bad usage (to stderr). It lists every command and every exit code of
// the bridge, including the commands the later slices register. The %s
// slots are the program name (ProgramNau).
const usageTemplate = `%s - universal messaging and registry core; this build bridges a remote job dispatcher and a Herdr node

usage: %s <command> [args]

commands:
  job <sub> [args]                  forward a dispatcher-facing job subcommand to soho
                                    (start, status, wait, events, collect, amend, send, ack,
                                    cancel, close, list)
  gate                              forced command of the dispatch ssh key: run the text of
                                    SSH_ORIGINAL_COMMAND only when it is "nau job <sub> ..."
                                    (the public subcommands) or "nau outbox ...", else refuse
                                    with exit 2 (takes no arguments; see docs/gate.md)
  route [--machine <label>]         choose the machine for a new job (dispatcher side, read-only)
  wake                              read one job event on stdin, record it in the outbox and
                                    attempt one push
  sync [--job <id>] [--push-only]   sync tracked job events and push pending records
  push                              push pending outbox records to the dispatcher
  outbox [--since <seq>] [--wait <ms>]
                                    print outbox records with seq > since as JSON lines, then
                                    the trailer (the dispatcher pull channel; read-only)
  session start|update|end          manage open session records for a project
  decision --projeto <org/repo> --escopo global|projeto --motivo <text> [--job <id>]
                                    [<resumo>|-]
  auth login|status|logout          manage the per-user dispatcher API key
  config get|set|list               manage the machine configuration
                                    set [--if-automatic] <key> <value>
  doctor                            check the bridge prerequisites
  capabilities --json               print the bridge capability line
  version                           print the version
  help                              print this usage
  hub serve [--listen <host:port>] [--web <dir>]
                                    run the hub server on the given address: the health and
                                    version API and the static files, with the store in the
                                    remote database named by NAU_HUB_DB_URL (token in
                                    NAU_HUB_DB_TOKEN)

exit codes:
  0   success (forwarded commands: whatever soho returned)
  2   bad usage, input limit exceeded, unknown config key, internal job
      subcommand refused
  3   unknown job id in %s bookkeeping (sync --job)
  4   soho not found or not runnable; no eligible machine available
      (route)
  40  no API key configured (push required)
  41  API key rejected by the dispatcher (401/403)
  42  dispatcher unreachable or retries exhausted; records stay pending
  43  soho lacks the ephemeral_job or job_events capability
      (doctor, sync)
`

// usageText renders the usage for the program name of env.
func usageText(env Env) string {
	prog := env.program()
	return fmt.Sprintf(usageTemplate, prog, prog, prog)
}
