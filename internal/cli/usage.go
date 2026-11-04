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
  agent run [--interval <duration>] [--hc-bin <bin>]
                                    run the machine agent: read the soho
                                    inventory on the period (15 s by
                                    default, 5 s to 30 s) and deliver it to
                                    the hub from hub.json; the machine key
                                    stays in the Authorization header
  config get|set|list               manage the machine configuration
                                    set [--if-automatic] <key> <value>
  doctor                            print the status line: the version, the
                                    config directory, the machine label and
                                    the soho backend (read-only)
  version                           print the version
  help                              print this usage
  hub serve [--listen <host:port>] [--web <dir>] [--recovery-listen <host:port>]
                                    run the hub server on the given address: the health and
                                    version API and the static files, with the store in the
                                    remote database named by NAU_HUB_DB_URL (token in
                                    NAU_HUB_DB_TOKEN); on the empty database the initial
                                    owner is seeded from NAU_HUB_OWNER_NAME,
                                    NAU_HUB_OWNER_EMAIL and NAU_HUB_OWNER_PASSWORD (ignored
                                    once a user exists); NAU_HUB_ROOT_TOKEN (at least 32
                                    characters when set) opens the password recovery route,
                                    served only on the recovery listener, which binds to the
                                    loopback by default (127.0.0.1:7441, the host of
                                    --recovery-listen must be a literal loopback ip)
  hub recover --url <url>
                                    recover the account access of a running hub: the url is
                                    the loopback of the recovery port (the port-forward of
                                    the recovery listener), and the root token and the new
                                    password are read on stdin, one per line
  connect <hub-url> [--name <nome>]
                                    connect this machine to the hub: request a pairing, show
                                    the code, poll the confirmation and store the machine key
                                    in hub.json of the config directory (https only, or http
                                    on a loopback host; the name defaults to machine_label)

exit codes:
  0   success
  2   bad usage, unknown config key, hub serve: a missing or invalid owner
      variable, a blank root token or one shorter than 32 bytes, or a recovery
      listener that is not a literal loopback host:port
  3   connect: the pairing expired
  4   hub serve: the database or the listener is unavailable, or the user
      count or the owner seed fails; connect: the hub is unreachable or its
      answer is not valid; hub recover: the hub refused the reset or is
      unreachable; agent run: the hub revoked the machine key (doctor
      reports the soho backend in its JSON and exits 0 without it)
  130 connect: interrupted (Ctrl-C) before hub.json was written
`

// usageText renders the usage for the program name of env.
func usageText(env Env) string {
	prog := env.program()
	return fmt.Sprintf(usageTemplate, prog, prog)
}
