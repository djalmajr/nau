package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/jobapi"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// gateRun is one simulated forced-command run: the text sshd would put in
// SSH_ORIGINAL_COMMAND, the stdin the client would send and the NOWRITE
// switch.
type gateRun struct {
	cmd     string
	unset   bool // SSH_ORIGINAL_COMMAND absent (an interactive session)
	nowrite bool
	stdin   string
	args    []string // the arguments of `nau gate` itself
}

// countingReader fails the test's expectation when read: it counts reads.
type countingReader struct {
	data  string
	reads atomic.Int64
	r     *strings.Reader
}

func newCountingReader(data string) *countingReader {
	return &countingReader{data: data, r: strings.NewReader(data)}
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.r.Read(p)
}

// runGate runs `nau gate` with the simulated SSH_ORIGINAL_COMMAND and
// returns stdout, stderr, the exit code and the stdin reader.
func runGate(t *testing.T, cfgDir string, g gateRun) (string, string, int, *countingReader) {
	t.Helper()
	var out, errb bytes.Buffer
	stdin := newCountingReader(g.stdin)
	env := Env{
		Stdin:  stdin,
		Stdout: &out,
		Stderr: &errb,
		Getenv: func(k string) string {
			switch {
			case k == gateCommandVar && !g.unset:
				return g.cmd
			case k == nowriteVar && g.nowrite:
				return "1"
			}
			return ""
		},
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: cfgDir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run(append([]string{"gate"}, g.args...), env)
	return out.String(), errb.String(), exit, stdin
}

// gateFake installs a fake soho that answers every call with the same
// output and returns the config dir pointing at it and the fake's dir.
func gateFake(t *testing.T, rules ...fakesoho.Rule) (cfgDir, fakeDir string) {
	t.Helper()
	exe, fakeDir := installFakeSoho(t, rules...)
	cfgDir = t.TempDir()
	setSohoConfig(t, cfgDir, exe, "machine-a")
	return cfgDir, fakeDir
}

// gateCalls returns the calls the fake soho received; none is a nil slice.
func gateCalls(t *testing.T, fakeDir string) []fakesoho.Call {
	t.Helper()
	calls, err := fakesoho.ReadCalls(fakeDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadCalls: %v", err)
	}
	return calls
}

// gateGOOSes are the systems the split is checked for: the table below is
// the same on every one, except for the program name `nau.exe`.
var gateGOOSes = []string{"linux", "darwin", "windows"}

// TestGateSplit: the pure split accepts and refuses the same table for the
// three systems; only `nau.exe` depends on the system.
func TestGateSplit(t *testing.T) {
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	type tc struct {
		name  string
		cmd   string
		argv  []string // the arguments after the program name, when accepted
		class string   // the refusal class, when refused
	}
	cases := []tc{
		// Accepted.
		{name: "job status", cmd: "nau job status --id J1", argv: []string{"job", "status", "--id", "J1"}},
		{name: "job list bare", cmd: "nau job list", argv: []string{"job", "list"}},
		{name: "job start full", cmd: "nau job start --id a.b_c-1 --repo org/re.po --base feat/x --mode worktree --brief - --timeout 30 --dry-run",
			argv: []string{"job", "start", "--id", "a.b_c-1", "--repo", "org/re.po", "--base", "feat/x", "--mode", "worktree", "--brief", "-", "--timeout", "30", "--dry-run"}},
		{name: "amend dash", cmd: "nau job amend --id J1 -", argv: []string{"job", "amend", "--id", "J1", "-"}},
		{name: "amend no positional", cmd: "nau job amend --id J1", argv: []string{"job", "amend", "--id", "J1"}},
		{name: "outbox", cmd: "nau outbox", argv: []string{"outbox"}},
		{name: "outbox flags", cmd: "nau outbox --since 3 --wait 0", argv: []string{"outbox", "--since", "3", "--wait", "0"}},
		{name: "double space", cmd: "nau  job   status --id J1", argv: []string{"job", "status", "--id", "J1"}},
		{name: "leading space", cmd: " nau job list", argv: []string{"job", "list"}},
		{name: "trailing space", cmd: "nau job list ", argv: []string{"job", "list"}},
		{name: "4096 bytes", cmd: pad("nau job list", 4096), argv: []string{"job", "list"}},

		// Empty.
		{name: "empty", cmd: "", class: gateEmpty},
		{name: "spaces only", cmd: "   ", class: gateEmpty},

		// Too long.
		{name: "4097 bytes", cmd: pad("nau job list", 4097), class: gateTooLong},
		{name: "4097 bytes of junk", cmd: strings.Repeat(";", 4097), class: gateTooLong},

		// Characters outside the set, one per metacharacter class.
		{name: "semicolon", cmd: "nau job list;id", class: gateBadChar},
		{name: "semicolon spaced", cmd: "nau job list ; rm", class: gateBadChar},
		{name: "pipe", cmd: "nau job list | cat", class: gateBadChar},
		{name: "ampersand", cmd: "nau job list & id", class: gateBadChar},
		{name: "dollar", cmd: "nau job status --id $HOME", class: gateBadChar},
		{name: "backtick", cmd: "nau job status --id `id`", class: gateBadChar},
		{name: "command substitution", cmd: "nau job status --id $(id)", class: gateBadChar},
		{name: "percent var", cmd: "nau job status --id %USERNAME%", class: gateBadChar},
		{name: "caret", cmd: "nau job status --id a^b", class: gateBadChar},
		{name: "double quote", cmd: `nau job status --id "J1"`, class: gateBadChar},
		{name: "single quote", cmd: "nau job status --id 'J1'", class: gateBadChar},
		{name: "backslash", cmd: `nau job status --id a\b`, class: gateBadChar},
		{name: "cr", cmd: "nau job list\r", class: gateBadChar},
		{name: "lf", cmd: "nau job list\n", class: gateBadChar},
		{name: "lf in the middle", cmd: "nau job list\nnau job list", class: gateBadChar},
		{name: "tab", cmd: "nau\tjob list", class: gateBadChar},
		{name: "nul", cmd: "nau job list\x00", class: gateBadChar},
		{name: "nul in the middle", cmd: "nau job\x00list", class: gateBadChar},
		{name: "high byte", cmd: "nau job status --id J\xc3\xa9", class: gateBadChar},
		{name: "invalid utf8", cmd: "nau job status --id J\xff", class: gateBadChar},
		{name: "high byte at 0x80", cmd: "nau job status --id \x80", class: gateBadChar},
		{name: "equals form", cmd: "nau job status --id=J1", class: gateBadChar},
		{name: "redirect", cmd: "nau job list > x", class: gateBadChar},
		{name: "glob", cmd: "nau job status --id J*", class: gateBadChar},
		{name: "tilde", cmd: "nau job status --id ~", class: gateBadChar},
		{name: "vertical tab", cmd: "nau\vjob list", class: gateBadChar},
		{name: "form feed", cmd: "nau\fjob list", class: gateBadChar},
		{name: "del", cmd: "nau job list\x7f", class: gateBadChar},
		{name: "nbsp", cmd: "nau\u00a0job list", class: gateBadChar},

		// The program name.
		{name: "NAU", cmd: "NAU job list", class: gateProgram},
		{name: "dot slash nau", cmd: "./nau job list", class: gateProgram},
		{name: "absolute nau", cmd: "/x/nau job list", class: gateProgram},
		{name: "nau.bat", cmd: "nau.bat job list", class: gateProgram},
		{name: "nau.exe.exe", cmd: "nau.exe.exe job list", class: gateProgram},
		{name: "nau dot", cmd: "nau. job list", class: gateProgram},
		{name: "other program", cmd: "id", class: gateProgram},
		{name: "job first", cmd: "job list", class: gateProgram},

		// The command.
		{name: "no command", cmd: "nau", class: gateCommand},
		{name: "route", cmd: "nau route", class: gateCommand},
		{name: "doctor", cmd: "nau doctor", class: gateCommand},
		{name: "gate", cmd: "nau gate", class: gateCommand},
		{name: "config", cmd: "nau config list", class: gateCommand},
		{name: "herdr", cmd: "nau herdr agent list", class: gateCommand},
		{name: "JOB", cmd: "nau JOB list", class: gateCommand},
		{name: "flag first", cmd: "nau --id J1", class: gateCommand},

		// The job subcommand.
		{name: "job alone", cmd: "nau job", class: gateSubcommand},
		{name: "supervise", cmd: "nau job supervise --id J1", class: gateSubcommand},
		{name: "checkpoint", cmd: "nau job checkpoint --id J1", class: gateSubcommand},
		{name: "note", cmd: "nau job note --id J1", class: gateSubcommand},
		{name: "unknown sub", cmd: "nau job frobnicate", class: gateSubcommand},
		{name: "upper sub", cmd: "nau job START --id J1", class: gateSubcommand},
		{name: "flag as sub", cmd: "nau job --id J1", class: gateSubcommand},

		// Flags.
		{name: "unknown flag", cmd: "nau job status --id J1 --verbose", class: gateFlag},
		{name: "foreign flag", cmd: "nau job status --id J1 --repo org/repo", class: gateFlag},
		{name: "repeated flag", cmd: "nau job status --id J1 --id J2", class: gateFlag},
		{name: "repeated bare flag", cmd: "nau job collect --id J1 --md --md", class: gateFlag},
		{name: "repeated dash", cmd: "nau job amend --id J1 - -", class: gateValue},
		{name: "short flag", cmd: "nau job status -i J1", class: gateFlag},
		{name: "double dash", cmd: "nau job status --id J1 --", class: gateFlag},
		{name: "positional on status", cmd: "nau job status --id J1 J2", class: gateFlag},
		{name: "dash on status", cmd: "nau job status --id J1 -", class: gateFlag},
		{name: "internal flag on start", cmd: "nau job start --id J1 --tipo note", class: gateFlag},
		{name: "outbox foreign flag", cmd: "nau outbox --id J1", class: gateFlag},
		{name: "outbox repeated since", cmd: "nau outbox --since 1 --since 2", class: gateFlag},
		{name: "outbox repeated wait", cmd: "nau outbox --wait 1 --wait 2", class: gateFlag},
		{name: "outbox positional", cmd: "nau outbox 3", class: gateFlag},
		{name: "bare flag with a value", cmd: "nau job collect --id J1 --md yes", class: gateFlag},

		// Values.
		{name: "missing value", cmd: "nau job status --id", class: gateValue},
		{name: "empty value before a flag", cmd: "nau job start --id --repo org/repo", class: gateValue},
		{name: "id starting with a dash", cmd: "nau job status --id -J1", class: gateValue},
		{name: "id too long", cmd: "nau job status --id " + strings.Repeat("a", 65), class: gateValue},
		{name: "id 64", cmd: "nau job status --id " + strings.Repeat("a", 64), argv: []string{"job", "status", "--id", strings.Repeat("a", 64)}},
		{name: "id slash", cmd: "nau job status --id a/b", class: gateValue},
		{name: "repo no org", cmd: "nau job start --id J1 --repo repo", class: gateValue},
		{name: "repo three parts", cmd: "nau job start --id J1 --repo a/b/c", class: gateValue},
		{name: "base dotdot", cmd: "nau job start --id J1 --repo o/r --base a/../b", class: gateValue},
		{name: "base dash flag", cmd: "nau job start --id J1 --repo o/r --base --brief", class: gateValue},
		{name: "mode unknown", cmd: "nau job start --id J1 --repo o/r --mode shell", class: gateValue},
		{name: "mode workspace", cmd: "nau job start --id J1 --repo o/r --mode workspace", argv: []string{"job", "start", "--id", "J1", "--repo", "o/r", "--mode", "workspace"}},
		{name: "brief file", cmd: "nau job start --id J1 --repo o/r --brief brief.md", class: gateValue},
		{name: "brief path", cmd: "nau job start --id J1 --repo o/r --brief /etc/passwd", class: gateValue},
		{name: "amend file", cmd: "nau job amend --id J1 body.md", class: gateValue},
		{name: "send path", cmd: "nau job send --id J1 /etc/passwd", class: gateValue},
		{name: "number letters", cmd: "nau job wait --id J1 --timeout 1x", class: gateValue},
		{name: "number sign", cmd: "nau job events --id J1 --since -1", class: gateValue},
		{name: "number plus", cmd: "nau job events --id J1 --since +1", class: gateBadChar},
		{name: "number decimal", cmd: "nau job events --id J1 --since 1.5", class: gateValue},
		{name: "number overflow", cmd: "nau job ack --id J1 --upto 99999999999999999999", class: gateValue},
		{name: "number max int64", cmd: "nau job ack --id J1 --upto 9223372036854775807", argv: []string{"job", "ack", "--id", "J1", "--upto", "9223372036854775807"}},
		{name: "number above int64", cmd: "nau job ack --id J1 --upto 9223372036854775808", class: gateValue},
		{name: "number leading zero", cmd: "nau job ack --id J1 --upto 007", argv: []string{"job", "ack", "--id", "J1", "--upto", "007"}},
		{name: "grace word", cmd: "nau job cancel --id J1 --grace soon", class: gateValue},
		{name: "state upper", cmd: "nau job list --state Running", class: gateValue},
		{name: "state digit", cmd: "nau job list --state r2", class: gateValue},
		{name: "state outside the contract", cmd: "nau job list --state timed_out", class: gateValue},
		{name: "state unknown", cmd: "nau job list --state unknown_state", class: gateValue},
		{name: "state valid in upper case", cmd: "nau job list --state RUNNING", class: gateValue},
		{name: "state prefix", cmd: "nau job list --state run", class: gateValue},
		{name: "state too long", cmd: "nau job list --state " + strings.Repeat("a", 33), class: gateValue},
		{name: "outbox since word", cmd: "nau outbox --since x", class: gateValue},
		{name: "outbox wait missing", cmd: "nau outbox --wait", class: gateValue},
		{name: "outbox since negative", cmd: "nau outbox --since -1", class: gateValue},
	}
	for _, goos := range gateGOOSes {
		for _, c := range cases {
			t.Run(goos+"/"+c.name, func(t *testing.T) {
				argv, class := gateSplit(c.cmd, goos)
				if c.class != "" {
					if class != c.class || argv != nil {
						t.Fatalf("gateSplit(%q) = %q, %q; want class %q", c.cmd, argv, class, c.class)
					}
					return
				}
				if class != "" || !reflect.DeepEqual(argv, c.argv) {
					t.Fatalf("gateSplit(%q) = %q, %q; want %q", c.cmd, argv, class, c.argv)
				}
			})
		}
	}
	// --state accepts exactly the contract's lifecycle states.
	if len(jobapi.States) != 11 {
		t.Fatalf("jobapi.States has %d states, want 11", len(jobapi.States))
	}
	for _, goos := range gateGOOSes {
		for _, state := range jobapi.States {
			argv, class := gateSplit("nau job list --state "+state, goos)
			if class != "" || !reflect.DeepEqual(argv, []string{"job", "list", "--state", state}) {
				t.Errorf("%s --state %s = %q, %q", goos, state, argv, class)
			}
		}
	}
	// nau.exe is the program name on Windows only.
	for _, goos := range gateGOOSes {
		argv, class := gateSplit("nau.exe job list", goos)
		if goos == "windows" {
			if class != "" || !reflect.DeepEqual(argv, []string{"job", "list"}) {
				t.Errorf("windows nau.exe = %q, %q", argv, class)
			}
			continue
		}
		if class != gateProgram || argv != nil {
			t.Errorf("%s nau.exe = %q, %q; want %q", goos, argv, class, gateProgram)
		}
	}
	// A path to nau.exe is refused on Windows too.
	for _, cmd := range []string{`C:\x\nau.exe job list`, "./nau.exe job list", "/x/nau.exe job list", "NAU.EXE job list"} {
		if _, class := gateSplit(cmd, "windows"); class == "" {
			t.Errorf("windows %q was accepted", cmd)
		}
	}
}

// TestGateSplitTableMatchesJob: the gate's own list of public subcommands
// is the contract's 11, the same keys as the forwarder's, and none is
// internal.
func TestGateSplitTableMatchesJob(t *testing.T) {
	var got, want []string
	for sub := range gateJobFlags {
		got = append(got, sub)
		if jobInternalSubs[sub] {
			t.Errorf("gate accepts the internal subcommand %q", sub)
		}
	}
	for sub := range jobPublicSubs {
		want = append(want, sub)
	}
	if len(got) != 11 || len(want) != 11 {
		t.Fatalf("subcommands: gate %d, job %d; want 11", len(got), len(want))
	}
	for _, sub := range want {
		if _, ok := gateJobFlags[sub]; !ok {
			t.Errorf("job public subcommand %q is missing from the gate", sub)
		}
	}
}

// gateJobCommand renders the SSH_ORIGINAL_COMMAND of a jobTestSubs case.
func gateJobCommand(sub string, args []string) string {
	return strings.Join(append([]string{"nau", "job", sub}, args...), " ")
}

// TestGateJobPassthrough: each of the 11 public subcommands passes through
// the gate with the fake soho receiving the exact argv and the exact stdin,
// and the stdout, stderr and exit of the fake coming back unchanged.
func TestGateJobPassthrough(t *testing.T) {
	var rules []fakesoho.Rule
	for _, tc := range jobTestSubs {
		rules = append(rules, fakesoho.Rule{
			Argv: append([]string{"job", tc.sub}, tc.args...), ArgvPrefix: true,
			Stdout: tc.stdout, Stderr: tc.stderr, Code: tc.code,
		})
	}
	cfgDir, fakeDir := gateFake(t, rules...)
	for _, tc := range jobTestSubs {
		stdout, stderr, exit, _ := runGate(t, cfgDir, gateRun{cmd: gateJobCommand(tc.sub, tc.args), stdin: "body-σ"})
		if exit != tc.code || stdout != tc.stdout || stderr != tc.stderr {
			t.Errorf("%s: exit %d stdout %q stderr %q; want %d %q %q", tc.sub, exit, stdout, stderr, tc.code, tc.stdout, tc.stderr)
		}
	}
	calls := gateCalls(t, fakeDir)
	if len(calls) != len(jobTestSubs) {
		t.Fatalf("fake calls = %d, want %d", len(calls), len(jobTestSubs))
	}
	for i, tc := range jobTestSubs {
		want := append([]string{"job", tc.sub}, tc.args...)
		if !reflect.DeepEqual(calls[i].Argv, want) {
			t.Errorf("%s: child argv = %q, want %q", tc.sub, calls[i].Argv, want)
		}
		if calls[i].Stdin != "body-σ" {
			t.Errorf("%s: child stdin = %q, want %q", tc.sub, calls[i].Stdin, "body-σ")
		}
	}
}

// TestGateEquivalence: for the same arguments the gate and `nau job` run
// directly give the same stdout, stderr, exit, child argv and child stdin.
func TestGateEquivalence(t *testing.T) {
	rules := func() []fakesoho.Rule {
		var rs []fakesoho.Rule
		for _, tc := range jobTestSubs {
			rs = append(rs, fakesoho.Rule{
				Argv: append([]string{"job", tc.sub}, tc.args...), ArgvPrefix: true,
				Stdout: tc.stdout, Stderr: tc.stderr, Code: tc.code,
			})
		}
		return rs
	}
	for _, tc := range jobTestSubs {
		t.Run(tc.sub, func(t *testing.T) {
			directCfg, directFake := gateFake(t, rules()...)
			gateCfg, gateFakeDir := gateFake(t, rules()...)
			dOut, dErr, dExit := runHermes(t, directCfg, false, "body-σ", append([]string{"job", tc.sub}, tc.args...)...)
			gOut, gErr, gExit, _ := runGate(t, gateCfg, gateRun{cmd: gateJobCommand(tc.sub, tc.args), stdin: "body-σ"})
			if gOut != dOut || gErr != dErr || gExit != dExit {
				t.Errorf("gate %q %q %d; direct %q %q %d", gOut, gErr, gExit, dOut, dErr, dExit)
			}
			dCalls, gCalls := gateCalls(t, directFake), gateCalls(t, gateFakeDir)
			if len(dCalls) != 1 || len(gCalls) != 1 {
				t.Fatalf("calls: direct %d, gate %d; want 1 and 1", len(dCalls), len(gCalls))
			}
			if !reflect.DeepEqual(gCalls[0].Argv, dCalls[0].Argv) || gCalls[0].Stdin != dCalls[0].Stdin {
				t.Errorf("gate child %q %q; direct child %q %q", gCalls[0].Argv, gCalls[0].Stdin, dCalls[0].Argv, dCalls[0].Stdin)
			}
		})
	}
}

// TestGateOutboxPassthrough: `outbox` through the gate prints exactly what
// `nau outbox` prints directly, with and without the flags, including the
// refusals `nau outbox` itself makes after the gate let the arguments by.
func TestGateOutboxPassthrough(t *testing.T) {
	cfgDir := t.TempDir()
	seedOutbox(t, cfgDir, 3)
	for _, args := range [][]string{
		nil,
		{"--since", "1"},
		{"--since", "3"},
		{"--wait", "0"},
		{"--since", "1", "--wait", "0"},
		{"--wait", "0", "--since", "2"},
		{"--wait", "600001"}, // refused by the outbox itself, not by the gate
	} {
		dOut, dErr, dExit := runHermes(t, cfgDir, false, "", append([]string{"outbox"}, args...)...)
		gOut, gErr, gExit, stdin := runGate(t, cfgDir, gateRun{cmd: strings.Join(append([]string{"nau", "outbox"}, args...), " "), stdin: "ignored"})
		if gOut != dOut || gErr != dErr || gExit != dExit {
			t.Errorf("outbox %v: gate %q %q %d; direct %q %q %d", args, gOut, gErr, gExit, dOut, dErr, dExit)
		}
		if n := stdin.reads.Load(); n != 0 {
			t.Errorf("outbox %v: the gate read the stdin %d times", args, n)
		}
	}
	// The read really happened: the seeded records came back.
	stdout, _, exit, _ := runGate(t, cfgDir, gateRun{cmd: "nau outbox --since 1"})
	if exit != 0 || !strings.Contains(stdout, `{"outbox":"fim","ultimo_seq":3,`) || strings.Count(stdout, "\n") != 3 {
		t.Errorf("outbox --since 1 = %q (exit %d)", stdout, exit)
	}
}

// refusalLine is the one stdout line of a gate refusal.
func refusalLine(class string) string {
	return `{"status":"2","motivo":"gate: ` + class + `"}` + "\n"
}

// TestGateRefusals: every refusal exits 2 with the fixed stdout line and
// the single stderr line, never runs soho, never reads the stdin and never
// repeats the client's command.
func TestGateRefusals(t *testing.T) {
	long := strings.Repeat("a", 65)
	cases := []struct {
		name  string
		g     gateRun
		class string
	}{
		{"unset", gateRun{unset: true}, gateEmpty},
		{"empty", gateRun{cmd: ""}, gateEmpty},
		{"spaces", gateRun{cmd: "    "}, gateEmpty},
		{"too long", gateRun{cmd: "nau job list" + strings.Repeat(" ", 4085)}, gateTooLong},
		{"newline", gateRun{cmd: "nau job list\nnau job list"}, gateBadChar},
		{"semicolon", gateRun{cmd: "nau job list; cat /etc/passwd"}, gateBadChar},
		{"substitution", gateRun{cmd: "nau job status --id $(id)"}, gateBadChar},
		{"equals", gateRun{cmd: "nau job status --id=J1"}, gateBadChar},
		{"unprintable", gateRun{cmd: "nau job list\x00\x01"}, gateBadChar},
		{"high byte", gateRun{cmd: "nau job list \xc3\xa9"}, gateBadChar},
		{"program path", gateRun{cmd: "/usr/bin/nau job list"}, gateProgram},
		{"program case", gateRun{cmd: "NAU job list"}, gateProgram},
		{"nau.bat", gateRun{cmd: "nau.bat job list"}, gateProgram},
		{"route", gateRun{cmd: "nau route"}, gateCommand},
		{"doctor", gateRun{cmd: "nau doctor"}, gateCommand},
		{"gate again", gateRun{cmd: "nau gate"}, gateCommand},
		{"config", gateRun{cmd: "nau config list"}, gateCommand},
		{"sync", gateRun{cmd: "nau sync"}, gateCommand},
		{"supervise", gateRun{cmd: "nau job supervise --id J1"}, gateSubcommand},
		{"checkpoint", gateRun{cmd: "nau job checkpoint --id J1"}, gateSubcommand},
		{"note", gateRun{cmd: "nau job note --id J1"}, gateSubcommand},
		{"unknown sub", gateRun{cmd: "nau job explode"}, gateSubcommand},
		{"unknown flag", gateRun{cmd: "nau job status --id J1 --x y"}, gateFlag},
		{"repeated flag", gateRun{cmd: "nau job status --id J1 --id J2"}, gateFlag},
		{"outbox flag", gateRun{cmd: "nau outbox --id J1"}, gateFlag},
		{"brief file", gateRun{cmd: "nau job start --id J1 --repo o/r --brief brief.md", stdin: "x"}, gateValue},
		{"amend file", gateRun{cmd: "nau job amend --id J1 body.md", stdin: "x"}, gateValue},
		{"bad id", gateRun{cmd: "nau job status --id " + long}, gateValue},
		{"bad number", gateRun{cmd: "nau job wait --id J1 --timeout 1x"}, gateValue},
		{"nowrite", gateRun{cmd: "nau job list;", nowrite: true}, gateBadChar},
	}
	// One fake for the whole table: a refusal must never call it, so any
	// call in the log fails the case that made it.
	cfgDir, fakeDir := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.g.stdin = "SECRET-STDIN-" + c.g.stdin
			stdout, stderr, exit, stdin := runGate(t, cfgDir, c.g)
			if exit != 2 {
				t.Errorf("exit = %d, want 2", exit)
			}
			if want := refusalLine(c.class); stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if want := "nau: gate: " + c.class + "\n"; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if n := stdin.reads.Load(); n != 0 {
				t.Errorf("the stdin was read %d times", n)
			}
			if calls := gateCalls(t, fakeDir); len(calls) != 0 {
				t.Errorf("soho ran: %q", calls)
			}
			// The client's text is never echoed back.
			if c.g.cmd != "" && (strings.Contains(stdout, c.g.cmd) || strings.Contains(stderr, c.g.cmd)) {
				t.Errorf("the output repeats the client's command %q", c.g.cmd)
			}
		})
	}
	// The program name `nau.exe` is accepted on Windows only; the gate
	// uses the system it runs on.
	exeCfg, exeFake := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "{}\n"})
	stdout, _, exit, _ := runGate(t, exeCfg, gateRun{cmd: "nau.exe job list"})
	ran := len(gateCalls(t, exeFake)) == 1
	if runtime.GOOS == "windows" && (!ran || exit != 0) {
		t.Errorf("windows: nau.exe was refused (exit %d, stdout %q)", exit, stdout)
	}
	if runtime.GOOS != "windows" && (ran || exit != 2 || stdout != refusalLine(gateProgram)) {
		t.Errorf("%s: nau.exe ran=%v exit %d stdout %q", runtime.GOOS, ran, exit, stdout)
	}
}

// TestGateOwnArguments: `nau gate` takes no arguments of its own; it is a
// usage error and nothing is read or run.
func TestGateOwnArguments(t *testing.T) {
	cfgDir, fakeDir := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
	stdout, _, exit, stdin := runGate(t, cfgDir, gateRun{cmd: "nau job list", args: []string{"job", "list"}, stdin: "x"})
	if exit != 2 || !strings.HasPrefix(stdout, `{"status":"2","motivo":"usage: gate`) {
		t.Errorf("exit %d stdout %q", exit, stdout)
	}
	if n := stdin.reads.Load(); n != 0 {
		t.Errorf("the stdin was read %d times", n)
	}
	if calls := gateCalls(t, fakeDir); len(calls) != 0 {
		t.Errorf("soho ran: %q", calls)
	}
}

// TestGateStdinCaps: the stdin caps of start, amend and send hold inside
// the gate, exactly at the cap and one byte above.
func TestGateStdinCaps(t *testing.T) {
	caps := []struct {
		cmd string
		sub string
		cap int
	}{
		{"nau job start --id J1 --repo o/r --brief -", "start", 256 * 1024},
		{"nau job amend --id J1 -", "amend", 64 * 1024},
		{"nau job send --id J1 -", "send", 16 * 1024},
	}
	for _, tc := range caps {
		t.Run(tc.sub, func(t *testing.T) {
			cfgDir, fakeDir := gateFake(t, fakesoho.Rule{
				Argv: []string{"job", tc.sub}, ArgvPrefix: true, Stdout: `{"id":"J1"}` + "\n",
			})
			body := strings.Repeat("σ", tc.cap/2)
			if len(body) != tc.cap {
				t.Fatalf("fixture length %d, want %d", len(body), tc.cap)
			}
			stdout, _, exit, _ := runGate(t, cfgDir, gateRun{cmd: tc.cmd, stdin: body})
			calls := gateCalls(t, fakeDir)
			if exit != 0 || len(calls) != 1 || calls[0].Stdin != body {
				t.Fatalf("at the cap: exit %d stdout %q calls %d", exit, stdout, len(calls))
			}
			cfgDir, fakeDir = gateFake(t, fakesoho.Rule{
				Argv: []string{"job", tc.sub}, ArgvPrefix: true, Stdout: `{"id":"J1"}` + "\n",
			})
			stdout, _, exit, _ = runGate(t, cfgDir, gateRun{cmd: tc.cmd, stdin: body + "x"})
			if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || strings.Contains(stdout, `"id"`) {
				t.Errorf("one byte above: exit %d stdout %q", exit, stdout)
			}
			if _, err := os.Stat(fakesoho.LogPath(fakeDir)); !os.IsNotExist(err) {
				t.Errorf("one byte above: soho ran")
			}
		})
	}
}

// gateLogLines returns the friction lines the gate wrote, as the part after
// the timestamp and the command, and fails when a line is not shaped as
// "<ts>\t<cmd>\t<msg>". Lines of other commands are skipped.
func gateLogLines(t *testing.T, cfgDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfgDir, "state", "friction.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read friction.log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("friction line %q is not <ts>\\t<cmd>\\t<msg>", line)
		}
		if parts[1] == "gate" {
			lines = append(lines, parts[2])
		}
	}
	return lines
}

// TestGateLog: a refusal and an accepted write each leave one line; reads
// leave none; NOWRITE leaves no file at all; the stdin is never logged.
func TestGateLog(t *testing.T) {
	const secret = "SECRET-BODY-σ"
	answers := fakesoho.Rule{AnyArgs: true, Stdout: `{"id":"J1"}` + "\n"}

	t.Run("refusal", func(t *testing.T) {
		cfgDir, _ := gateFake(t, answers)
		runGate(t, cfgDir, gateRun{cmd: "nau route", stdin: secret})
		runGate(t, cfgDir, gateRun{cmd: "nau job list;id\x01\xc3\xa9\ttab", stdin: secret})
		runGate(t, cfgDir, gateRun{unset: true, stdin: secret})
		long := "nau job status --id " + strings.Repeat("a", 200)
		runGate(t, cfgDir, gateRun{cmd: long, stdin: secret})
		runGate(t, cfgDir, gateRun{cmd: "nau job note --id J1 --tipo x", stdin: secret})
		got := gateLogLines(t, cfgDir)
		want := []string{
			`refused command len=9 cmd="nau route"`,
			`refused bad-char len=22 cmd="nau job ?"`,
			`refused empty len=0 cmd=""`,
			`refused value len=220 cmd="nau job status --id ?"`,
			`refused subcommand len=29 cmd="nau job note --id ? ? ?"`,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("log = %q\nwant %q", got, want)
		}
		if b, _ := os.ReadFile(filepath.Join(cfgDir, "state", "friction.log")); strings.Contains(string(b), "SECRET") {
			t.Errorf("the log holds the stdin")
		}
	})

	t.Run("projection is cut on a token boundary", func(t *testing.T) {
		cfgDir, _ := gateFake(t, answers)
		runGate(t, cfgDir, gateRun{cmd: "nau" + strings.Repeat(" --id", 40)})
		got := gateLogLines(t, cfgDir)
		// "nau" and 23 " --id" make 118 bytes; a 24th would pass 120.
		want := []string{`refused command len=203 cmd="nau` + strings.Repeat(" --id", 23) + `"`}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("log = %q\nwant %q", got, want)
		}
	})

	t.Run("accepted writes", func(t *testing.T) {
		cfgDir, _ := gateFake(t, answers)
		for _, c := range []struct{ cmd, line string }{
			{"nau job start --id J1 --repo o/r --brief -", "job start id=J1 exit=0"},
			{"nau job amend --id J2 -", "job amend id=J2 exit=0"},
			{"nau job send --id J3 -", "job send id=J3 exit=0"},
			{"nau job ack --id J4 --upto 2", "job ack id=J4 exit=0"},
			{"nau job cancel --id J5", "job cancel id=J5 exit=0"},
			{"nau job close --id J6 --force", "job close id=J6 exit=0"},
		} {
			runGate(t, cfgDir, gateRun{cmd: c.cmd, stdin: secret})
			got := gateLogLines(t, cfgDir)
			if len(got) == 0 || got[len(got)-1] != c.line {
				t.Errorf("after %q: log = %q, want last %q", c.cmd, got, c.line)
			}
		}
		if got := gateLogLines(t, cfgDir); len(got) != 6 {
			t.Errorf("log has %d gate lines, want 6: %q", len(got), got)
		}
		if b, _ := os.ReadFile(filepath.Join(cfgDir, "state", "friction.log")); strings.Contains(string(b), "SECRET") {
			t.Errorf("the log holds the stdin")
		}
	})

	t.Run("accepted write records the exit", func(t *testing.T) {
		cfgDir, _ := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: `{"status":"not_found"}` + "\n", Code: 3})
		_, _, exit, _ := runGate(t, cfgDir, gateRun{cmd: "nau job cancel --id J1"})
		got := gateLogLines(t, cfgDir)
		if exit != 3 || !reflect.DeepEqual(got, []string{"job cancel id=J1 exit=3"}) {
			t.Errorf("exit %d log %q", exit, got)
		}
	})

	t.Run("reads do not write", func(t *testing.T) {
		cfgDir, _ := gateFake(t, answers)
		seedOutbox(t, cfgDir, 1)
		for _, cmd := range []string{
			"nau job status --id J1", "nau job wait --id J1 --timeout 0", "nau job events --id J1",
			"nau job collect --id J1 --md", "nau job list", "nau outbox", "nau outbox --since 0 --wait 0",
		} {
			runGate(t, cfgDir, gateRun{cmd: cmd, stdin: secret})
		}
		if got := gateLogLines(t, cfgDir); len(got) != 0 {
			t.Errorf("reads wrote %q", got)
		}
	})

	t.Run("nowrite writes nothing", func(t *testing.T) {
		cfgDir, _ := gateFake(t, answers)
		for _, g := range []gateRun{
			{cmd: "nau route", nowrite: true},
			{unset: true, nowrite: true},
			{cmd: "nau job start --id J1 --repo o/r --brief -", nowrite: true, stdin: secret},
			{cmd: "nau job amend --id J1 -", nowrite: true, stdin: secret},
			{cmd: "nau job close --id J1", nowrite: true},
			{cmd: "nau job status --id J1", nowrite: true},
		} {
			runGate(t, cfgDir, g)
		}
		if _, err := os.Stat(filepath.Join(cfgDir, "state", "friction.log")); !os.IsNotExist(err) {
			t.Errorf("friction.log exists under NOWRITE (err %v)", err)
		}
	})
}

// TestGateNowriteWrites: under NOWRITE the gate lets a writing subcommand
// through to the job command, which refuses it with the NOWRITE line.
func TestGateNowriteWrites(t *testing.T) {
	cfgDir, fakeDir := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
	stdout, _, exit, _ := runGate(t, cfgDir, gateRun{cmd: "nau job close --id J1", nowrite: true})
	if exit != 2 || stdout != nowriteErrorJSON+"\n" {
		t.Errorf("exit %d stdout %q", exit, stdout)
	}
	if calls := gateCalls(t, fakeDir); len(calls) != 0 {
		t.Errorf("soho ran: %q", calls)
	}
}

// TestGateLogNeverValues: a refused command never leaves a value in the
// friction log, wherever the value sits: a --brief value, the --key value
// of another command, a token with a forbidden character, a long token.
func TestGateLogNeverValues(t *testing.T) {
	const marker = "SYNTHETICMARKER"
	cases := []struct {
		cmd   string
		class string
		shape string
	}{
		{"nau job start --id J1 --repo o/r --brief " + marker, gateValue, "nau job start --id ? --repo ? --brief ?"},
		{"nau auth login --key " + marker, gateCommand, "nau auth ? ? ?"},
		{"nau job status --id " + marker + "$x", gateBadChar, "nau job status --id ?"},
		{"nau job status --id " + marker + strings.Repeat("Z", 300), gateValue, "nau job status --id ?"},
		{marker + " job list", gateProgram, "? job list"},
		{"nau job " + marker, gateSubcommand, "nau job ?"},
		{"nau job amend --id J1 " + marker, gateValue, "nau job amend --id ? ?"},
		{"nau outbox --since " + marker, gateValue, "nau outbox --since ?"},
	}
	cfgDir, fakeDir := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
	var want []string
	for _, c := range cases {
		stdout, _, exit, stdin := runGate(t, cfgDir, gateRun{cmd: c.cmd, stdin: "unread"})
		if exit != 2 || stdout != refusalLine(c.class) || stdin.reads.Load() != 0 {
			t.Errorf("%q: exit %d stdout %q reads %d", c.cmd, exit, stdout, stdin.reads.Load())
		}
		want = append(want, "refused "+c.class+" len="+strconv.Itoa(len(c.cmd))+` cmd="`+c.shape+`"`)
	}
	if got := gateLogLines(t, cfgDir); !reflect.DeepEqual(got, want) {
		t.Errorf("log = %q\nwant %q", got, want)
	}
	b, err := os.ReadFile(filepath.Join(cfgDir, "state", "friction.log"))
	if err != nil {
		t.Fatalf("read friction.log: %v", err)
	}
	if strings.Contains(string(b), marker) || strings.Contains(string(b), "ZZZ") {
		t.Errorf("the log holds a value of the command: %q", b)
	}
	if calls := gateCalls(t, fakeDir); len(calls) != 0 {
		t.Errorf("soho ran: %q", calls)
	}
}

// TestGateLogVocabulary: the words the projection keeps are the gate's
// fixed vocabulary, and every nau top-level command is one of them.
func TestGateLogVocabulary(t *testing.T) {
	for _, c := range commands {
		if !gateLogWord(c.name) {
			t.Errorf("top-level command %q is not in the log vocabulary", c.name)
		}
	}
	for _, w := range []string{"nau", "nau.exe", "supervise", "checkpoint", "note", "start", "list", "--id", "--force", "--since", "--dry-run"} {
		if !gateLogWord(w) {
			t.Errorf("%q is not in the log vocabulary", w)
		}
	}
	for _, w := range []string{"-", "J1", "o/r", "--key", "--tipo", "NAU", "running", "0", ""} {
		if gateLogWord(w) {
			t.Errorf("%q is in the log vocabulary", w)
		}
	}
}

// TestGateNoConfigDir: without a configuration directory the gate still
// refuses with its own lines (and logs nothing), and an accepted command
// gets exactly the answer `nau job` or `nau outbox` gives directly.
func TestGateNoConfigDir(t *testing.T) {
	for _, c := range []struct{ cmd, class string }{
		{"nau job note --id J1", gateSubcommand},
		{"nau route", gateCommand},
		{"", gateEmpty},
	} {
		stdout, stderr, exit, stdin := runGate(t, "", gateRun{cmd: c.cmd, stdin: "unread"})
		if exit != 2 || stdout != refusalLine(c.class) || stderr != "nau: gate: "+c.class+"\n" || stdin.reads.Load() != 0 {
			t.Errorf("%q: exit %d stdout %q stderr %q reads %d", c.cmd, exit, stdout, stderr, stdin.reads.Load())
		}
	}
	stdout, _, exit, _ := runGate(t, "", gateRun{cmd: "nau job list", args: []string{"x"}})
	if exit != 2 || !strings.HasPrefix(stdout, `{"status":"2","motivo":"usage: gate`) {
		t.Errorf("own arguments without config: exit %d stdout %q", exit, stdout)
	}
	for _, args := range [][]string{{"job", "status", "--id", "J1"}, {"job", "close", "--id", "J1"}, {"outbox", "--since", "1"}} {
		dOut, dErr, dExit := runHermes(t, "", false, "body", args...)
		gOut, gErr, gExit, _ := runGate(t, "", gateRun{cmd: "nau " + strings.Join(args, " "), stdin: "body"})
		if gOut != dOut || gErr != dErr || gExit != dExit {
			t.Errorf("%v: gate %q %q %d; direct %q %q %d", args, gOut, gErr, gExit, dOut, dErr, dExit)
		}
		if gOut != noConfigDirErrorJSON+"\n" || gExit != 2 {
			t.Errorf("%v: gate %q exit %d, want the no_config_dir line", args, gOut, gExit)
		}
	}
}

// contractFlags is the flag list of every subcommand written from the job
// contract's command table (docs/upstream/job-contract.md) and the outbox
// usage, independently of gateJobFlags: true marks a flag that takes a
// value.
var contractFlags = map[string]map[string]bool{
	"start":   {"--id": true, "--repo": true, "--base": true, "--mode": true, "--brief": true, "--timeout": true, "--dry-run": false},
	"status":  {"--id": true},
	"wait":    {"--id": true, "--timeout": true},
	"events":  {"--id": true, "--since": true, "--wait": true},
	"collect": {"--id": true, "--md": false, "--verify": false},
	"amend":   {"--id": true},
	"send":    {"--id": true},
	"ack":     {"--id": true, "--upto": true},
	"cancel":  {"--id": true, "--grace": true},
	"close":   {"--id": true, "--force": false},
	"list":    {"--state": true},
	"outbox":  {"--since": true, "--wait": true},
}

// contractSample is a valid value for every valued flag of contractFlags.
var contractSample = map[string]string{
	"--id": "J1", "--repo": "o/r", "--base": "main", "--mode": "worktree",
	"--brief": "-", "--timeout": "30", "--since": "1", "--wait": "0",
	"--upto": "2", "--grace": "5", "--state": "running",
}

// TestGateForeignFlags: every public flag crossed with every subcommand.
// A pair outside the contract is refused (exit 2, nothing dispatched, the
// stdin unread); a pair inside it is accepted by the production table.
func TestGateForeignFlags(t *testing.T) {
	isValued := map[string]bool{}
	for _, flags := range contractFlags {
		for f, v := range flags {
			isValued[f] = v
		}
	}
	// The production table holds exactly the contract's flags.
	for sub, flags := range contractFlags {
		spec := gateOutboxFlags
		if sub != "outbox" {
			var ok bool
			if spec, ok = gateJobFlags[sub]; !ok {
				t.Errorf("subcommand %q is missing from the gate", sub)
				continue
			}
		}
		got := map[string]bool{}
		for f := range spec.valued {
			got[f] = true
		}
		for _, f := range spec.bare {
			got[f] = false
		}
		if !reflect.DeepEqual(got, flags) {
			t.Errorf("%s: gate flags %v, contract %v", sub, got, flags)
		}
	}
	cfgDir, fakeDir := gateFake(t, fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
	refused := 0
	for sub, flags := range contractFlags {
		prefix := "nau job " + sub
		if sub == "outbox" {
			prefix = "nau outbox"
		}
		for flag, valued := range isValued {
			cmd := prefix
			if _, hasID := flags["--id"]; hasID && flag != "--id" {
				cmd += " --id J1"
			}
			cmd += " " + flag
			if valued {
				cmd += " " + contractSample[flag]
			}
			if _, allowed := flags[flag]; allowed {
				if _, class := gateSplit(cmd, runtime.GOOS); class != "" {
					t.Errorf("%q is in the contract but refused (%s)", cmd, class)
				}
				continue
			}
			refused++
			stdout, _, exit, stdin := runGate(t, cfgDir, gateRun{cmd: cmd, stdin: "unread"})
			if exit != 2 || stdout != refusalLine(gateFlag) || stdin.reads.Load() != 0 {
				t.Errorf("%q: exit %d stdout %q reads %d", cmd, exit, stdout, stdin.reads.Load())
			}
		}
	}
	if refused == 0 {
		t.Fatal("no foreign pair was checked")
	}
	// The reviewer's pair, spelled out.
	if _, _, exit, _ := runGate(t, cfgDir, gateRun{cmd: "nau job status --id J1 --force"}); exit != 2 {
		t.Errorf("job status --id J1 --force: exit %d", exit)
	}
	if calls := gateCalls(t, fakeDir); len(calls) != 0 {
		t.Errorf("soho ran: %q", calls)
	}
}
