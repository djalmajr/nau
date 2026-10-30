package cli

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/djalmajr/nau/internal/jobapi"
	"github.com/djalmajr/nau/internal/outbox"
)

// gateCommandVar is the variable the ssh server sets to the text the client
// asked to run when the key carries a forced command.
const gateCommandVar = "SSH_ORIGINAL_COMMAND"

const (
	// gateMaxCommand is the longest accepted SSH_ORIGINAL_COMMAND, in bytes.
	gateMaxCommand = 4096
	// gateLogCommand is the longest client command written to the friction
	// log, in bytes.
	gateLogCommand = 120
)

// The refusal classes of the gate: a fixed set, so the refusal line never
// repeats what the client sent.
const (
	// gateEmpty: the variable is absent, empty or only spaces.
	gateEmpty = "empty"
	// gateTooLong: more than gateMaxCommand bytes.
	gateTooLong = "too-long"
	// gateBadChar: a byte outside [A-Za-z0-9._/-] and the space.
	gateBadChar = "bad-char"
	// gateProgram: the first token is not the program name.
	gateProgram = "program"
	// gateCommand: the command is neither job nor outbox.
	gateCommand = "command"
	// gateSubcommand: the job subcommand is missing, internal or unknown.
	gateSubcommand = "subcommand"
	// gateFlag: a flag or positional that the subcommand does not accept,
	// or one given twice.
	gateFlag = "flag"
	// gateValue: a flag value or positional outside what the flag accepts.
	gateValue = "value"
)

// gateValueKind says what a valued flag accepts.
type gateValueKind int

const (
	gateID    gateValueKind = iota // jobapi.ValidID
	gateRepo                       // jobapi.ValidRepo
	gateBase                       // jobapi.ValidBase
	gateMode                       // worktree or workspace
	gateStdin                      // only "-": the body comes on stdin
	gateNum                        // an unsigned decimal that fits an int64
	gateState                      // one of jobapi.States
)

// gateFlagSpec is what one subcommand accepts: its valued flags with their
// kind, its bare flags and whether it takes the optional "-" positional.
type gateFlagSpec struct {
	valued     map[string]gateValueKind
	bare       []string
	positional bool
}

// gateJobFlags is the allow-list of `job` subcommands with their flags,
// from the contract's command table (docs/upstream/job-contract.md). It is
// the gate's own list, not a view of jobPublicSubs: the internal
// subcommands (supervise, checkpoint, note) are absent from it.
var gateJobFlags = map[string]gateFlagSpec{
	"start": {
		valued: map[string]gateValueKind{
			"--id": gateID, "--repo": gateRepo, "--base": gateBase,
			"--mode": gateMode, "--brief": gateStdin, "--timeout": gateNum,
		},
		bare: []string{"--dry-run"},
	},
	"status":  {valued: map[string]gateValueKind{"--id": gateID}},
	"wait":    {valued: map[string]gateValueKind{"--id": gateID, "--timeout": gateNum}},
	"events":  {valued: map[string]gateValueKind{"--id": gateID, "--since": gateNum, "--wait": gateNum}},
	"collect": {valued: map[string]gateValueKind{"--id": gateID}, bare: []string{"--md", "--verify"}},
	"amend":   {valued: map[string]gateValueKind{"--id": gateID}, positional: true},
	"send":    {valued: map[string]gateValueKind{"--id": gateID}, positional: true},
	"ack":     {valued: map[string]gateValueKind{"--id": gateID, "--upto": gateNum}},
	"cancel":  {valued: map[string]gateValueKind{"--id": gateID, "--grace": gateNum}},
	"close":   {valued: map[string]gateValueKind{"--id": gateID}, bare: []string{"--force"}},
	"list":    {valued: map[string]gateValueKind{"--state": gateState}},
}

// gateOutboxFlags is what `outbox` accepts.
var gateOutboxFlags = gateFlagSpec{
	valued: map[string]gateValueKind{"--since": gateNum, "--wait": gateNum},
}

// gateWrites are the job subcommands that change state; an accepted one
// leaves a line in the friction log.
var gateWrites = map[string]bool{
	"start": true, "amend": true, "send": true, "ack": true,
	"cancel": true, "close": true,
}

// gateDispatch is the CLI dispatch the gate hands an accepted command to,
// set at init to Run (the command table refers to cmdGate, so cmdGate
// cannot refer to Run in its initialization).
var gateDispatch func(args []string, env Env) int

func init() { gateDispatch = Run }

// cmdGate is the forced command of the dispatch key: `nau gate`. It takes
// the text of SSH_ORIGINAL_COMMAND, splits it without a shell and lets only
// `nau job <sub> ...` (the public subcommands) and `nau outbox ...` through,
// each with its own flags. An accepted command runs in this process through
// Run, with this process's stdin, stdout, stderr and exit code; a refused one
// exits 2 before anything is read from stdin or run. The client's first
// token is only a name, never executed or looked up.
func cmdGate(args []string, env Env) int {
	if len(args) != 0 {
		badUsage(env, "usage: gate (takes no arguments; the command comes from "+gateCommandVar+")")
		return 2
	}
	command := env.Getenv(gateCommandVar)
	argv, class := gateSplit(command, runtime.GOOS)
	if class != "" {
		fail(env, 2, "gate: "+class)
		gateLogRefusal(env, class, command)
		return 2
	}
	exit := gateDispatch(argv, env)
	if argv[0] == "job" && gateWrites[argv[1]] {
		id, ok := flagValue(argv[2:], "--id")
		if !ok {
			id = "-"
		}
		gateLog(env, "job "+argv[1]+" id="+id+" exit="+strconv.Itoa(exit))
	}
	return exit
}

// gateSplit splits the client's command into the arguments after the
// program name, or returns the refusal class. The grammar has no quoting and
// no escape: the text is ASCII of at most gateMaxCommand bytes, the tokens
// are separated by spaces (0x20, runs and the ends are tolerated) and a
// token has only [A-Za-z0-9._/-]. The first token must be "nau" ("nau.exe"
// too on Windows); then `job <sub>` or `outbox`, each with the flags of its
// table, a flag once, as "--flag value".
func gateSplit(command, goos string) (argv []string, class string) {
	if len(command) > gateMaxCommand {
		return nil, gateTooLong
	}
	for i := 0; i < len(command); i++ {
		if !gateTokenByte(command[i]) && command[i] != ' ' {
			return nil, gateBadChar
		}
	}
	var tokens []string
	for start := 0; start < len(command); {
		if command[start] == ' ' {
			start++
			continue
		}
		end := start
		for end < len(command) && command[end] != ' ' {
			end++
		}
		tokens = append(tokens, command[start:end])
		start = end
	}
	if len(tokens) == 0 {
		return nil, gateEmpty
	}
	if tokens[0] != "nau" && !(goos == "windows" && tokens[0] == "nau.exe") {
		return nil, gateProgram
	}
	if len(tokens) < 2 {
		return nil, gateCommand
	}
	switch tokens[1] {
	case "job":
		if len(tokens) < 3 {
			return nil, gateSubcommand
		}
		spec, ok := gateJobFlags[tokens[2]]
		if !ok {
			return nil, gateSubcommand
		}
		if class := gateFlags(tokens[3:], spec); class != "" {
			return nil, class
		}
		return tokens[1:], ""
	case "outbox":
		if class := gateFlags(tokens[2:], gateOutboxFlags); class != "" {
			return nil, class
		}
		return tokens[1:], ""
	}
	return nil, gateCommand
}

// gateTokenByte reports whether c may appear in a token.
func gateTokenByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return c == '.' || c == '_' || c == '/' || c == '-'
}

// gateFlags checks the tokens after the subcommand against its spec and
// returns the refusal class, or "".
func gateFlags(tokens []string, spec gateFlagSpec) string {
	seen := map[string]bool{}
	positional := false
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if tok == "-" || tok[0] != '-' {
			// The only positional is "-": the body on stdin, once.
			if !spec.positional {
				return gateFlag
			}
			if tok != "-" || positional {
				return gateValue
			}
			positional = true
			continue
		}
		if seen[tok] {
			return gateFlag
		}
		if kind, ok := spec.valued[tok]; ok {
			seen[tok] = true
			if i+1 >= len(tokens) || !gateValid(kind, tokens[i+1]) {
				return gateValue
			}
			i++
			continue
		}
		if !gateIsBare(spec, tok) {
			return gateFlag
		}
		seen[tok] = true
	}
	return ""
}

func gateIsBare(spec gateFlagSpec, tok string) bool {
	for _, b := range spec.bare {
		if b == tok {
			return true
		}
	}
	return false
}

// gateValid reports whether value is acceptable for a flag of the given
// kind. A value that starts with "-" is refused for every kind but the
// stdin marker, so it cannot be read as the next flag.
func gateValid(kind gateValueKind, value string) bool {
	if kind == gateStdin {
		return value == "-"
	}
	if value == "" || value[0] == '-' {
		return false
	}
	switch kind {
	case gateID:
		return jobapi.ValidID(value)
	case gateRepo:
		return jobapi.ValidRepo(value)
	case gateBase:
		return jobapi.ValidBase(value)
	case gateMode:
		return value == "worktree" || value == "workspace"
	case gateNum:
		for i := 0; i < len(value); i++ {
			if value[i] < '0' || value[i] > '9' {
				return false
			}
		}
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case gateState:
		for _, s := range jobapi.States {
			if value == s {
				return true
			}
		}
		return false
	}
	return false
}

// gateTopCommands are the nau top-level commands, kept in the shape of a
// refused command; the command table is not read here because it refers to
// cmdGate.
var gateTopCommands = []string{
	"job", "sync", "doctor", "outbox", "wake", "session", "decision", "push",
	"auth", "config", "capabilities", "version", "help",
	"route", "gate", "hub",
}

// gateLogWord reports whether tok belongs to the gate's fixed vocabulary:
// the program names, the top-level commands, the public and internal job
// subcommands and the flag names the gate knows. Anything else is a value.
func gateLogWord(tok string) bool {
	if tok == "nau" || tok == "nau.exe" || jobInternalSubs[tok] {
		return true
	}
	for _, c := range gateTopCommands {
		if tok == c {
			return true
		}
	}
	specs := []gateFlagSpec{gateOutboxFlags}
	for sub, spec := range gateJobFlags {
		if tok == sub {
			return true
		}
		specs = append(specs, spec)
	}
	for _, spec := range specs {
		if _, ok := spec.valued[tok]; ok || gateIsBare(spec, tok) {
			return true
		}
	}
	return false
}

// gateProjection is the shape of a refused command: its space-separated
// tokens, each kept only when it is a word of the gate's vocabulary and
// written as "?" otherwise, cut at gateLogCommand bytes on a token
// boundary. No value, brief, key or free text of the client survives it.
func gateProjection(command string) string {
	var out string
	for _, tok := range strings.Split(command, " ") {
		if tok == "" {
			continue
		}
		if !gateLogWord(tok) {
			tok = "?"
		}
		next := tok
		if out != "" {
			next = out + " " + tok
		}
		if len(next) > gateLogCommand {
			break
		}
		out = next
	}
	return out
}

// gateLogRefusal records one refusal: its class, the length of the client's
// command in bytes and its projection, never the values it carried.
func gateLogRefusal(env Env, class, command string) {
	gateLog(env, "refused "+class+" len="+strconv.Itoa(len(command))+" cmd="+strconv.Quote(gateProjection(command)))
}

// gateLog appends one line to the friction log under the command name
// "gate". It writes nothing under HERDR_HERMES_NOWRITE=1 or without a
// configuration directory, and never takes stdin, a brief or a secret:
// callers pass only classes, lengths, projections, subcommands, ids and
// exit codes.
func gateLog(env Env, msg string) {
	if env.ConfigDir == "" || env.Getenv(nowriteVar) == "1" {
		return
	}
	outbox.Friction(outbox.StateDir(env.ConfigDir), "gate", msg)
}
