// Package cli implements the nau command line: the command table
// (config, doctor, hub, agent, version, help) with the NAU_NOWRITE
// read-only gating and the shared output conventions (one JSON line on
// stdout, diagnostics on stderr, errors as
// {"status":"<code>","motivo":"<text>"}).
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"time"
)

// version is set for release builds with -ldflags. Dev builds keep "dev"
// and get the VCS revision recorded by the Go toolchain appended, or the
// module version of a "go install" build (which records no VCS info).
var version = "dev"

// nowriteVar is the read-only gate.
const nowriteVar = "NAU_NOWRITE"

// command is one entry of the command table.
type command struct {
	name           string
	writes         bool
	needsConfigDir bool
	run            func(args []string, env Env) int
}

// Command table.
var commands = []command{
	{"config", false, true, cmdConfig},
	{"doctor", false, true, cmdDoctor},
	{"version", false, false, cmdVersion},
	{"help", false, false, cmdHelp},
	// DJA-620: the central hub server (no local state: the store is the
	// remote database named by the environment)
	{"hub", false, false, cmdHub},
	// DJA-622: the machine connect to the hub (writes the local
	// hub.json credential once the pairing is confirmed)
	{"connect", true, true, cmdConnect},
	// DJA-626: the per-machine agent (reads hub.json, writes the health
	// file in the config dir)
	{"agent", true, true, cmdAgent},
}

// Run executes args[0] as a command and returns its exit code. An unknown
// command exits 2. Under NAU_NOWRITE=1 a command with writes=true
// exits 2 with the nowrite error before any side effect.
func Run(args []string, env Env) int {
	if env.Stdout == nil || env.Stderr == nil {
		env.Stdout = io.Discard
		env.Stderr = io.Discard
	}
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}
	if env.Now == nil {
		env.Now = time.Now
	}
	if env.Sleep == nil {
		env.Sleep = sleepCtx
	}
	if len(args) == 0 {
		_, _ = io.WriteString(env.Stdout, usageText(env))
		return 0
	}
	var cmd *command
	for i := range commands {
		if commands[i].name == args[0] {
			cmd = &commands[i]
			break
		}
	}
	if cmd == nil {
		badUsage(env, "unknown command "+quote(args[0]))
		return 2
	}
	// A command that reads or writes the config dir cannot run when the user
	// config directory is unavailable; help and version still work. There is
	// no silent temp-dir fallback.
	if cmd.needsConfigDir && env.ConfigDir == "" {
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", noConfigDirErrorJSON)
		return 2
	}
	if env.Getenv(nowriteVar) == "1" && cmd.writes {
		_, _ = fmt.Fprintf(env.Stdout, "%s\n", nowriteErrorJSON)
		return 2
	}
	return cmd.run(args[1:], env)
}

// errorLine is the error JSON on stdout: {"status":"<code>","motivo":"…"}.
type errorLine struct {
	Status string `json:"status"`
	Motivo string `json:"motivo"`
}

const nowriteErrorJSON = `{"status":"nowrite","motivo":"NAU_NOWRITE=1"}`

const noConfigDirErrorJSON = `{"status":"no_config_dir","motivo":"user config directory unavailable"}`

// badUsage prints the usage text to stderr and the exit-2 error JSON to
// stdout.
func badUsage(env Env, motivo string) {
	_, _ = io.WriteString(env.Stderr, usageText(env))
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(errorLine{Status: "2", Motivo: motivo}))
}

// fail prints an error JSON with the given numeric code to stdout and a
// diagnostic to stderr.
func fail(env Env, code int, motivo string) {
	_, _ = fmt.Fprintf(env.Stderr, "%s: %s\n", env.program(), motivo)
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(errorLine{Status: itoa(code), Motivo: motivo}))
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"status":"2","motivo":"internal error"}`
	}
	return string(b)
}

func quote(s string) string { return `"` + s + `"` }

func cmdVersion(args []string, env Env) int {
	if len(args) != 0 {
		badUsage(env, "usage: version")
		return 2
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(struct {
		Version string `json:"version"`
	}{Version: currentVersion()}))
	return 0
}

// devVersion derives the dev version from the build info: the VCS
// revision recorded by the Go toolchain wins when present (nothing
// changes for builds that carry it); without it, a "go install" build
// records the module version instead, used when it is a real version
// and not (devel); otherwise the version stays plain "dev".
func devVersion(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "dev"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return "dev+" + setting.Value
		}
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return "dev+" + v
	}
	return "dev"
}

func cmdHelp(args []string, env Env) int {
	if len(args) != 0 {
		badUsage(env, "usage: help")
		return 2
	}
	_, _ = io.WriteString(env.Stdout, usageText(env))
	return 0
}
