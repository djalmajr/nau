package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// runCLI runs Run with a hermetic env over a fresh ConfigDir and returns
// stdout, stderr and the exit code.
func runCLI(t *testing.T, configDir string, nowrite bool, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    getenvFor(nowrite),
		ConfigDir: configDir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run(args, env)
	return out.String(), errb.String(), exit
}

func getenvFor(nowrite bool) func(string) string {
	return func(k string) string {
		if k == nowriteVar && nowrite {
			return "1"
		}
		return ""
	}
}

// assertDirEmpty requires the configuration directory to hold no files at
// all (read-only commands must not create the config dir or any other file).
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read config dir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("config dir %s is not empty: %v", dir, names)
	}
}

var allCommandNames = []string{
	"config", "doctor", "version", "help",
}

var allExitCodes = []string{"0", "2", "4"}

func TestHelp(t *testing.T) {
	stdout, stderr, exit := runCLI(t, t.TempDir(), false, "help")
	if exit != 0 {
		t.Fatalf("help exit = %d, stderr %q", exit, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("help wrote to stderr: %q", stderr)
	}
	for _, name := range allCommandNames {
		if !strings.Contains(stdout, name) {
			t.Errorf("usage does not list command %q", name)
		}
	}
	for _, code := range allExitCodes {
		if !strings.Contains(stdout, code) {
			t.Errorf("usage does not list exit code %q", code)
		}
	}
	// No arguments prints the usage like help.
	stdout, _, exit = runCLI(t, t.TempDir(), false)
	if exit != 0 || !strings.Contains(stdout, "commands:") {
		t.Errorf("no-args exit = %d, stdout %q", exit, stdout)
	}
}

func TestUnknownCommand(t *testing.T) {
	stdout, stderr, exit := runCLI(t, t.TempDir(), false, "frobnicate")
	if exit != 2 {
		t.Fatalf("unknown command exit = %d, want 2", exit)
	}
	if !strings.Contains(stderr, "commands:") {
		t.Errorf("usage not printed to stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, "frobnicate") {
		t.Errorf("stdout = %q", stdout)
	}
	// A bad flag of a known command also exits 2 with the usage on stderr.
	stdout, stderr, exit = runCLI(t, t.TempDir(), false, "version", "--bogus")
	if exit != 2 || !strings.Contains(stderr, "commands:") || !strings.Contains(stdout, `"status":"2"`) {
		t.Errorf("bad flag: exit %d, stderr %q, stdout %q", exit, stderr, stdout)
	}
	// A removed bridge command is an unknown command.
	for _, removed := range []string{"job", "sync", "wake", "push", "outbox", "auth", "session", "decision", "gate", "route", "capabilities"} {
		stdout, _, exit = runCLI(t, t.TempDir(), false, removed)
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, removed) {
			t.Errorf("removed command %s: exit %d, stdout %q", removed, exit, stdout)
		}
	}
}

func TestVersion(t *testing.T) {
	stdout, _, exit := runCLI(t, t.TempDir(), false, "version")
	if exit != 0 {
		t.Fatalf("version exit = %d", exit)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &v); err != nil {
		t.Fatalf("version stdout %q is not a JSON line: %v", stdout, err)
	}
	if v.Version != "dev" && !strings.HasPrefix(v.Version, "dev+") {
		t.Errorf("version = %q, want dev or dev+<revision>", v.Version)
	}
	// Dev builds built inside a VCS checkout append the revision.
	if strings.HasPrefix(v.Version, "dev+") && strings.Count(v.Version, "+") != 1 {
		t.Errorf("version = %q", v.Version)
	}
}

func TestNowrite(t *testing.T) {
	// Read-only commands work and write nothing into a fresh ConfigDir.
	for _, tc := range []struct {
		args   []string
		stdout string
	}{
		{[]string{"config", "get", "machine_label"}, "{\"key\":\"machine_label\",\"value\":\"\"}\n"},
		{[]string{"help"}, ""},
	} {
		dir := t.TempDir()
		stdout, _, exit := runCLI(t, dir, true, tc.args...)
		if exit != 0 {
			t.Fatalf("%v under NAU_NOWRITE: exit %d, stdout %q", tc.args, exit, stdout)
		}
		if tc.stdout != "" && stdout != tc.stdout {
			t.Errorf("%v stdout = %q, want %q", tc.args, stdout, tc.stdout)
		}
		assertDirEmpty(t, dir)
	}
	// config list under NAU_NOWRITE: defaults, nothing written. The
	// herdr_soho_bin value is the resolved effective one; the PATH is
	// controlled so the resolution is deterministic.
	dir := t.TempDir()
	sohoDir := sohoPathDir(t)
	t.Setenv("PATH", sohoDir)
	stdout, _, exit := runCLI(t, dir, true, "config", "list")
	if exit != 0 || !strings.Contains(stdout, `"herdr_soho_bin":"`+jsonText(filepath.Join(sohoDir, exeName("soho")))+`"`) {
		t.Errorf("config list under NAU_NOWRITE: exit %d, stdout %q", exit, stdout)
	}
	assertDirEmpty(t, dir)
	// version under NAU_NOWRITE: nothing written.
	dir = t.TempDir()
	_, _, exit = runCLI(t, dir, true, "version")
	if exit != 0 {
		t.Fatalf("version under NAU_NOWRITE: exit %d", exit)
	}
	assertDirEmpty(t, dir)
	// The one writing command refuses before any side effect.
	dir = t.TempDir()
	stdout, _, exit = runCLI(t, dir, true, "config", "set", "machine_label", "x")
	if exit != 2 {
		t.Fatalf("config set under NAU_NOWRITE: exit %d, want 2", exit)
	}
	if stdout != "{\"status\":\"nowrite\",\"motivo\":\"NAU_NOWRITE=1\"}\n" {
		t.Errorf("config set under NAU_NOWRITE stdout = %q", stdout)
	}
	assertDirEmpty(t, dir)
	// Unknown command still exits 2 under NAU_NOWRITE.
	dir = t.TempDir()
	_, _, exit = runCLI(t, dir, true, "frobnicate")
	if exit != 2 {
		t.Fatalf("unknown command under NAU_NOWRITE: exit %d, want 2", exit)
	}
	assertDirEmpty(t, dir)
}

// dirEntries returns the sorted entry names of a dir (one comma-joined
// string; "" for an empty dir), so a test can prove no file was created.
func dirEntries(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, ",")
}

// filesUnder lists every file under root as a sorted comma-joined relative
// path string, so a test can prove nothing was created.
func filesUnder(t *testing.T, root string) string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if rel, err := filepath.Rel(root, p); err == nil {
				names = append(names, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// TestNoConfigDir: when the user config directory is unavailable (ConfigDir
// empty) the commands that need the config dir refuse with exit 2 and the
// no_config_dir JSON; help and version keep working. There is no silent
// temp-dir fallback.
func TestNoConfigDir(t *testing.T) {
	for _, args := range [][]string{
		{"config", "get", "machine_label"},
		{"config", "list"},
		{"config", "set", "machine_label", "x"},
		{"doctor"},
	} {
		stdout, _, exit := runCLI(t, "", false, args...)
		if exit != 2 {
			t.Fatalf("%v with empty ConfigDir: exit %d, stdout %q; want 2", args, exit, stdout)
		}
		if stdout != "{\"status\":\"no_config_dir\",\"motivo\":\"user config directory unavailable\"}\n" {
			t.Errorf("%v stdout = %q; want the no_config_dir JSON", args, stdout)
		}
	}
	// help and version do not need the config dir.
	stdout, _, exit := runCLI(t, "", false, "help")
	if exit != 0 || !strings.Contains(stdout, "usage: nau <command> [args]") {
		t.Errorf("help with empty ConfigDir: exit %d", exit)
	}
	_, _, exit = runCLI(t, "", false, "version")
	if exit != 0 {
		t.Fatalf("version with empty ConfigDir: exit %d", exit)
	}
	// No arguments still prints usage (no config dir needed).
	stdout, _, exit = runCLI(t, "", false)
	if exit != 0 || !strings.Contains(stdout, "usage: nau <command> [args]") {
		t.Errorf("no args with empty ConfigDir: exit %d", exit)
	}
}

// TestProgramNameIdentity covers the identity contract at the Env level:
// the program name, the empty-Program default, the usage text rendered
// with no arguments (the product phrase, no mention of any retired name)
// and the stderr diagnostic prefix.
func TestProgramNameIdentity(t *testing.T) {
	if ProgramNau != "nau" {
		t.Fatalf("name = %q, want %q", ProgramNau, "nau")
	}
	if got := (Env{}).program(); got != ProgramNau {
		t.Errorf("empty Env program = %q, want %q (the default)", got, ProgramNau)
	}
	if got := (Env{Program: ProgramNau}).program(); got != ProgramNau {
		t.Errorf("Env{Program: %q} program = %q, want %q", ProgramNau, got, ProgramNau)
	}
	const title = " - read-only hub of the coding agents at work: which agents are working, on which machines, for which developers"

	// No-args prints the usage on stdout, exit 0, empty stderr, and the
	// usage leads with the title line carrying the program name.
	stdout, stderr, exit := runCLI(t, t.TempDir(), false)
	if exit != 0 {
		t.Fatalf("no-args exit = %d, want 0, stderr %q", exit, stderr)
	}
	if stderr != "" {
		t.Errorf("no-args stderr = %q, want empty", stderr)
	}
	nauUsage := stdout
	if !strings.HasPrefix(nauUsage, ProgramNau+title+"\n") {
		t.Errorf("no-args stdout does not lead with the title line: %q", firstNLines(nauUsage, 2))
	}
	if !strings.Contains(nauUsage, "usage: "+ProgramNau+" <command> [args]\n") {
		t.Errorf("no-args stdout lacks the usage line")
	}

	// The stderr diagnostic prefix follows the program name for the same
	// failure.
	_, stderr, exit = runCLI(t, t.TempDir(), false, "config", "get", "no_such_key")
	if exit != 2 {
		t.Fatalf("config get unknown key exit = %d, want 2, stderr %q", exit, stderr)
	}
	if !strings.HasPrefix(stderr, ProgramNau+": ") {
		t.Errorf("stderr prefix = %q, want %q: ...", firstLineOf(stderr), ProgramNau)
	}
}

// firstNLines returns the first n lines of s, joined with a marker, for
// failure messages.
func firstNLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// firstLineOf returns the first line of s, for failure messages.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
