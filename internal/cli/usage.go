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
  serve [--listen <host:port>] [--db <path>] [--machines <label,...>]
                        [--poll-interval <seconds>] [--max-jobs <n>]
                        [--max-sessions <n>] [--ended-retention-days <n>]
                                    run the registry service on a loopback address:
                                    inventory push, poll and read/write HTTP API
  jobs [--maquina <label>] [--agente <a>] [--server <url>]
                                    print the registry inventory of every node (read-only)
  jobs check --agente <a> [--card <c>] [--projeto <org/repo>] [--branch <b>] [--pr <n>]
                        [--workspace <name>] [--maquina <label>] [--ttl <s>]
                                    mandatory pre-dispatch check: refuse duplicate work or
                                    a hold, else record a claim
  jobs release --claim <id>         release a claim
  jobs policy set|clear, jobs folder add|clear, jobs node set
                                    holds and priorities, retained folders (a record, never
                                    a permission to delete) and node facts
                                    (every jobs form takes [--server <url>])
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
  plugin startup|event|bridge       Herdr plugin entry points

exit codes:
  0   success (forwarded commands: whatever soho returned)
  2   bad usage, input limit exceeded, unknown config key, internal job
      subcommand refused, registry usage or field rejected (serve, jobs)
  3   unknown job id in %s bookkeeping (sync --job) and unknown
      claim, policy or folder (jobs)
  4   soho not found or not runnable; no eligible machine available
      (route); registry unreachable or unavailable (serve, jobs)
  40  no API key configured (push required)
  41  API key rejected by the dispatcher (401/403)
  42  dispatcher unreachable or retries exhausted; records stay pending
  43  soho lacks the ephemeral_job or job_events capability
      (doctor, sync)
  44  the pre-dispatch check refused: duplicate work or a hold (jobs check)
`

// usageText renders the usage for the program name of env.
func usageText(env Env) string {
	prog := env.program()
	return fmt.Sprintf(usageTemplate, prog, prog, prog)
}
