package cli

import (
	"fmt"
)

// usageTemplate is the usage text printed by `help`, with no arguments and
// on bad usage (to stderr). It lists every command and every exit code.
// The %s slots are the program name (ProgramNau).
const usageTemplate = `%s - read-only hub of the coding agents at work: which agents are working, on which machines, for which developers

usage: %s <command> [args]

commands:
  config get|set|list               manage the machine configuration
                                    set [--if-automatic] <key> <value>
  doctor                            print the status line: the version, the
                                    config directory, the machine label and
                                    the soho backend (read-only)
  version                           print the version
  help                              print this usage
  hub serve [--listen <host:port>] [--web <dir>]
                                    run the hub server on the given address: the health and
                                    version API and the static files, with the store in the
                                    remote database named by NAU_HUB_DB_URL (token in
                                    NAU_HUB_DB_TOKEN)

exit codes:
  0   success
  2   bad usage, unknown config key
  4   hub serve: the database or the listener is unavailable (doctor reports
      the soho backend in its JSON and exits 0 without it)
`

// usageText renders the usage for the program name of env.
func usageText(env Env) string {
	prog := env.program()
	return fmt.Sprintf(usageTemplate, prog, prog)
}
