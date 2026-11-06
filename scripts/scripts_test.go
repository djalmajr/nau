// Package scripts_test checks the install scripts hermetically: the
// POSIX shell syntax and the --help output of install.sh, and the
// PowerShell syntax and the -Help output of install.ps1 (when a
// PowerShell is available). The checks parse, print usage or run the
// install with fixture executables and temporary state, without network
// or services.
package scripts_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shAvailable returns the path of sh, skipping the test on Windows or
// when sh is not on the PATH.
func shAvailable(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh is not available on windows")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available: " + err.Error())
	}
	return sh
}

// installerRunBound bounds one installer run. A run takes 10-12 s on a
// Windows node and has crossed 20 s on a busy Windows CI runner (DJA-488).
const installerRunBound = 90 * time.Second

func installerBuild(t *testing.T, output, source string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, source)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build installer fixture: %v\n%s", err, out)
	}
}

func TestInstallShBackendPin(t *testing.T) {
	runInstallerPinCases(t, shAvailable(t), false)
}

func TestInstallPs1BackendPin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell installer runtime requires Windows; exercised by Windows CI")
	}
	runInstallerPinCases(t, powershellAvailable(t), true)
}

func runInstallerPinCases(t *testing.T, shell string, windows bool) {
	t.Helper()
	buildDir := t.TempDir()
	suffix := ""
	if windows {
		suffix = ".exe"
	}
	nau := filepath.Join(buildDir, "nau"+suffix)
	helper := filepath.Join(buildDir, "helper"+suffix)
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	const configWhitespace = "\t\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"
	cases := []struct {
		name, config, backend                                  string
		explicit, missing, failGo, failSet, repeat, unreadable bool
		// emptyHome runs with HOME empty (Unix only); noXDG also drops XDG_CONFIG_HOME.
		emptyHome, noXDG bool
		relativeCurrent  bool
		unsafeKind       string
	}{
		{name: "no file", missing: true, backend: "soho"},
		{name: "missing key", config: "# configuration\nmachine_label=node\n", backend: "soho"},
		{name: "empty", config: "herdr_soho_bin=\n", backend: "soho"},
		{name: "retired explicit", config: "herdr_soho_bin=herdr" + "-soho\n", backend: "soho", explicit: true},
		{name: "absolute explicit", config: "absolute", explicit: true, backend: "soho"},
		{name: "bare explicit", config: "herdr_soho_bin=soho\n", explicit: true, backend: "soho"},
		{name: "relative explicit", config: "herdr_soho_bin=./bin/soho\n", explicit: true, backend: "soho"},
		{name: "idempotent", missing: true, backend: "soho", repeat: true},
		{name: "retired only", missing: true, backend: "herdr" + "-soho"},
		{name: "no backend no file", missing: true},
		{name: "no backend auto file", config: "herdr_soho_bin=\n"},
		{name: "go failure no file", missing: true, backend: "soho", failGo: true},
		{name: "go failure auto file", config: "herdr_soho_bin=\n", backend: "soho", failGo: true},
		{name: "pin write failure", missing: true, backend: "soho", failSet: true},
		{name: "trim last auto wins", config: " # comment\r\n herdr_soho_bin = soho\r\n\t herdr_soho_bin = \t\r\n", backend: "soho"},
		{name: "trim last explicit wins", config: "herdr_soho_bin=\n herdr_soho_bin = ./bin/soho \t\n", backend: "soho", explicit: true},
		{name: "first equals only", config: "herdr_soho_bin=./bin/soho=custom\n", backend: "soho", explicit: true},
		{name: "Unicode whitespace explicit", config: configWhitespace + "herdr_soho_bin" + configWhitespace + "=" + configWhitespace + "soho" + configWhitespace + "\n", backend: "soho", explicit: true},
		{name: "Unicode whitespace auto", config: "herdr_soho_bin=" + configWhitespace + configWhitespace + "\n", backend: "soho"},
		{name: "Unicode nonspace explicit", config: "herdr_soho_bin=\u200bsoho\u200b\n", backend: "soho", explicit: true},
		// A BOM-prefixed key line is an unknown key: it is ignored on load
		// and the pin appends the clean record.
		{name: "ignored BOM key", config: "\ufeffherdr_soho_bin=soho\n", backend: "soho"},
		{name: "unloadable line", config: "herdr_soho_bin\n", backend: "soho", unreadable: true},
		// os.UserConfigDir accepts an absolute XDG_CONFIG_HOME without HOME on Unix.
		{name: "XDG without HOME", missing: true, backend: "soho", emptyHome: true},
		{name: "no config dir", missing: true, backend: "soho", emptyHome: true, noXDG: true},
		{name: "newline path refused", config: "# keep\nmachine_label=original\nherdr_soho_bin=\n", backend: "soho", unsafeKind: "newline"},
		{name: "writable backend refused", config: "# keep\nmachine_label=original\nherdr_soho_bin=\n", backend: "soho", unsafeKind: "writable"},
		{name: "relative PATH preserves absolute discovery", missing: true, backend: "soho", relativeCurrent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if windows && (tc.unsafeKind != "" || tc.relativeCurrent) {
				t.Skip("POSIX path and mode fixtures; Windows ACL cases are in config tests")
			}
			if tc.emptyHome && windows {
				t.Skip("an empty HOME only has a defined config directory on Unix")
			}
			root := filepath.Join(t.TempDir(), "fixture space")
			home := filepath.Join(root, "home space")
			app := filepath.Join(root, "config space")
			local := filepath.Join(root, "local space")
			goDir := filepath.Join(root, "go space")
			backendDir := filepath.Join(root, "backend space")
			if tc.unsafeKind == "newline" {
				backendDir += "\nmachine_label=injected\n#"
			}
			tmp := filepath.Join(root, "temp space")
			bin := filepath.Join(root, "bin space")
			tasks := filepath.Join(root, "tasks space")
			for _, dir := range []string{home, app, local, goDir, backendDir, tmp, tasks} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(goDir, "go"+suffix), helperBytes)
			backendPath := filepath.Join(backendDir, tc.backend+suffix)
			if tc.backend != "" {
				write(backendPath, helperBytes)
			}
			if tc.relativeCurrent {
				backendPath = filepath.Join(goDir, "soho"+suffix)
				write(backendPath, helperBytes)
			}
			if tc.unsafeKind == "writable" {
				if err := os.Chmod(backendDir, 0777); err != nil {
					t.Fatal(err)
				}
			}
			// The env sets XDG_CONFIG_HOME=app (honored on darwin and
			// linux) and APPDATA=app (windows), so the config root is
			// app on every OS.
			cfgDir := filepath.Join(app, "nau")
			cfgFile := filepath.Join(cfgDir, "config")
			initial := tc.config
			if initial == "absolute" {
				initial = "herdr_soho_bin=" + backendPath + "\n"
			}
			if !tc.missing {
				if err := os.MkdirAll(cfgDir, 0700); err != nil {
					t.Fatal(err)
				}
				write(cfgFile, []byte(initial))
			}
			paths := []string{goDir, backendDir}
			if tc.relativeCurrent {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				paths[1], err = filepath.Rel(cwd, backendDir)
				if err != nil {
					t.Fatal(err)
				}
			}
			if windows {
				system := filepath.Join(os.Getenv("SystemRoot"), "System32")
				paths = append(paths, system, filepath.Join(system, "WindowsPowerShell", "v1.0"))
			} else {
				paths = append(paths, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
			}
			env := []string{"HOME=" + home, "USERPROFILE=" + home, "APPDATA=" + app, "LOCALAPPDATA=" + local, "XDG_CONFIG_HOME=" + app, "TMPDIR=" + tmp, "TEMP=" + tmp, "TMP=" + tmp, "PATH=" + strings.Join(paths, string(os.PathListSeparator)), "INSTALL_FIXTURE_NAU=" + nau}
			if tc.emptyHome {
				for i, kv := range env {
					if strings.HasPrefix(kv, "HOME=") {
						env[i] = "HOME="
					}
				}
				if tc.noXDG {
					kept := env[:0]
					for _, kv := range env {
						if !strings.HasPrefix(kv, "XDG_CONFIG_HOME=") {
							kept = append(kept, kv)
						}
					}
					env = kept
				}
			}
			if windows {
				// The no-flag re-run may at most query the agent task, and
				// even that query is a fixture: nothing reaches the real
				// Task Scheduler.
				env = append(env, "SystemRoot="+os.Getenv("SystemRoot"), "WINDIR="+os.Getenv("WINDIR"), "PATHEXT=.COM;.EXE;.BAT;.CMD", "NAU_TASKS_TRACE="+filepath.Join(tasks, "tasks.trace"), "NAU_TASKS_PRESENT="+filepath.Join(tasks, "task-present"), "NAU_TASKS_STATE="+filepath.Join(app, "nau", "agent-state.json"))
			}
			if tc.failGo {
				env = append(env, "INSTALL_FIXTURE_FAIL=1")
			}
			if tc.failSet {
				env = append(env, "NAU_NOWRITE=1")
			}
			if tc.relativeCurrent {
				// Shells differ: some return a relative lookup and others
				// prefix the working directory. Assert the actual discovery
				// contract before running the real installer.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				lookup := exec.CommandContext(ctx, shell, "-c", "command -v soho")
				lookup.Env, lookup.WaitDelay = env, time.Second
				out, err := lookup.Output()
				if err != nil {
					t.Fatal(err)
				}
				discovered := strings.TrimSuffix(string(out), "\n")
				if filepath.IsAbs(discovered) {
					backendPath = discovered
				}
				t.Logf("shell discovery=%q; expected pin=%q", discovered, backendPath)
			}
			run := func(want int) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
				defer cancel()
				args := []string{"install.sh", "--bin-dir", bin}
				if windows {
					script, err := filepath.Abs("install.ps1")
					if err != nil {
						t.Fatal(err)
					}
					args = []string{"-NoProfile", "-NonInteractive", "-Command", psTaskSchedulerFixtures("& " + psQuote(script) + " -BinDir " + psQuote(bin))}
				}
				cmd := exec.CommandContext(ctx, shell, args...)
				cmd.WaitDelay = time.Second
				cmd.Env = env
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				out := stdout.String() + stderr.String()
				if err != nil && ctx.Err() != nil {
					// A killed process exits 1 on Windows; never report the
					// bound as an installer failure.
					t.Fatalf("installer did not finish within %s: %s", installerRunBound, out)
				}
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal(err)
					}
					code = exit.ExitCode()
				}
				if code != want {
					t.Fatalf("installer exit=%d want=%d: %s", code, want, out)
				}
				if windows && want == 0 {
					// A successful no-flag install only queries the agent
					// task (which may be absent): stop, start, register
					// and unregister must not run.
					for _, line := range psTraceLines(t, filepath.Join(tasks, "tasks.trace")) {
						if !strings.HasPrefix(line, "get ") {
							t.Fatalf("a service cmdlet ran during the pin install: %s", line)
						}
					}
				}
				if !windows && tc.backend != "soho" && !strings.Contains(stderr.String(), "nau install: warning: soho not found on the PATH") {
					t.Fatal("missing backend warning on stderr")
				}
				entries, err := os.ReadDir(tmp)
				if err != nil || len(entries) != 0 {
					t.Fatal("installer left temporary build state behind")
				}
				return out
			}
			wantExit := 0
			if tc.failGo || tc.failSet {
				wantExit = 1
			}
			output := run(wantExit)
			data, readErr := os.ReadFile(cfgFile)
			if tc.failGo || tc.failSet || tc.backend != "soho" || tc.explicit || tc.unreadable || tc.noXDG || tc.unsafeKind != "" {
				if tc.missing {
					if !os.IsNotExist(readErr) {
						t.Fatal("config unexpectedly created")
					}
				} else if readErr != nil || string(data) != initial {
					t.Fatal("stored config bytes changed")
				}
				if tc.failGo {
					if _, err := os.Stat(bin); !os.IsNotExist(err) {
						t.Fatal("go failure touched bin directory")
					}
				}
				if tc.unsafeKind != "" && !strings.Contains(output, "unsafe backend") {
					t.Fatalf("unsafe backend warning absent: %s", output)
				}
				if tc.explicit && !strings.Contains(output, "herdr_soho_bin is set explicitly; keeping it") {
					t.Fatal("missing explicit-value message")
				}
				if tc.backend != "soho" && !strings.Contains(output, "nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic") {
					t.Fatal("missing backend warning")
				}
				if tc.noXDG && !strings.Contains(output, "nau install: warning: cannot read the config file; herdr_soho_bin unchanged") {
					t.Fatal("missing config-directory warning")
				}
				if tc.unreadable && !strings.Contains(output, "nau install: warning: cannot read the config file; herdr_soho_bin unchanged") {
					t.Fatal("missing unreadable-config warning")
				}
				if tc.failSet && !strings.Contains(output, "nau install: nau config set herdr_soho_bin failed") {
					t.Fatal("missing pin failure diagnostic")
				}
				return
			}
			if readErr != nil {
				t.Fatalf("pinned config absent: %v", readErr)
			}
			stored := ""
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if ok && strings.TrimSpace(key) == "herdr_soho_bin" {
					stored = strings.TrimSpace(value)
				}
			}
			if stored != backendPath {
				t.Fatalf("stored backend=%q want=%q; installer output=%s", stored, backendPath, output)
			}
			// The pin must preserve every other byte, including duplicates,
			// comments, whitespace and CRLF terminators.
			lines := strings.SplitAfter(initial, "\n")
			last := -1
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") {
					continue
				}
				key, _, ok := strings.Cut(trimmed, "=")
				if ok && strings.TrimSpace(key) == "herdr_soho_bin" {
					last = i
				}
			}
			want := initial
			if last >= 0 {
				ending := "\n"
				if strings.HasSuffix(lines[last], "\r\n") {
					ending = "\r\n"
				}
				lines[last] = "herdr_soho_bin=" + backendPath + ending
				want = strings.Join(lines, "")
			} else {
				want += "herdr_soho_bin=" + backendPath + "\n"
			}
			if string(data) != want {
				t.Fatalf("unrelated config bytes changed: got=%q want=%q", data, want)
			}
			if tc.repeat {
				second := run(0)
				after, err := os.ReadFile(cfgFile)
				if err != nil || !bytes.Equal(data, after) {
					t.Fatal("second install changed config bytes")
				}
				if !strings.Contains(second, "herdr_soho_bin is set explicitly; keeping it") {
					t.Fatal("second install did not keep pin")
				}
			}
		})
	}
}

// powershellAvailable returns the name of the first available
// PowerShell (pwsh or powershell), skipping the test when neither is
// on the PATH.
func powershellAvailable(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	t.Skip("neither pwsh nor powershell is available")
	return ""
}

// psQuote wraps a value in PowerShell single quotes, doubling any
// embedded quote, for use inside a -Command string.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psTaskSchedulerPreamble defines a fixture function for every Task
// Scheduler cmdlet that install.ps1 uses; the list comes from
// `git grep -n ScheduledTask scripts/install.ps1`, not from anywhere
// else. A PowerShell function shadows the cmdlet of the same name in
// the called script, so every scheduler call the installer makes
// lands in these fixtures and never in the real Task Scheduler, on
// any host. Every fixture appends one line to the trace file named by
// NAU_TASKS_TRACE; Get-ScheduledTask returns a task only when the
// file named by NAU_TASKS_PRESENT exists; Start-ScheduledTask writes
// the fresh health file named by NAU_TASKS_STATE after a short sleep,
// so the installer's mtime check passes at second granularity. The
// installer itself gains no variable or parameter for this: it is
// the test process, not the script, that defines the fixtures.
const psTaskSchedulerPreamble = `function Trace-Task {
    param([string]$What)
    Add-Content -LiteralPath $env:NAU_TASKS_TRACE -Value $What -Encoding ascii
}
function Get-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    Trace-Task ('get ' + $TaskName)
    if (Test-Path -LiteralPath $env:NAU_TASKS_PRESENT) { return @{ TaskName = $TaskName } }
    return $null
}
function Stop-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    Trace-Task ('stop ' + $TaskName)
}
function Start-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    Trace-Task ('start ' + $TaskName)
    Start-Sleep -Milliseconds 1100
    $state = $env:NAU_TASKS_STATE
    [void][System.IO.Directory]::CreateDirectory([System.IO.Path]::GetDirectoryName($state))
    [System.IO.File]::WriteAllText($state, '{}')
}
function Register-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath, $InputObject, [switch]$Force)
    Trace-Task ('register ' + $TaskName)
    return $InputObject
}
function Unregister-ScheduledTask {
    [CmdletBinding(SupportsShouldProcess = $true)]
    param([string]$TaskName, [string]$TaskPath)
    Trace-Task ('unregister ' + $TaskName)
}
function New-ScheduledTaskPrincipal {
    [CmdletBinding()]
    param([string]$UserId, [string]$LogonType, [string]$RunLevel)
    Trace-Task 'new-principal'
    return @{ Principal = $UserId }
}
function New-ScheduledTaskTrigger {
    [CmdletBinding()]
    param([switch]$AtLogOn, [string]$User, [switch]$Once, [datetime]$At, [timespan]$RepetitionInterval)
    Trace-Task 'new-trigger'
    return @{ Trigger = $true }
}
function New-ScheduledTaskSettingsSet {
    [CmdletBinding()]
    param([string]$MultipleInstances, [timespan]$ExecutionTimeLimit, [switch]$AllowStartIfOnBatteries, [switch]$DontStopIfGoingOnBatteries)
    Trace-Task 'new-settings'
    return @{ Settings = $true }
}
function New-ScheduledTaskAction {
    [CmdletBinding()]
    param([string]$Execute, [string]$Argument)
    Trace-Task 'new-action'
    return @{ Action = $Execute }
}
function New-ScheduledTask {
    [CmdletBinding()]
    param($Settings, $Trigger, $Action, $Principal)
    Trace-Task 'new-task'
    return @{ Task = $true }
}`

// psTaskSchedulerFixtures returns the -Command string that defines the
// fixture functions and then runs the installer invocation, so a
// test never runs install.ps1 without the fixtures in the same
// process.
func psTaskSchedulerFixtures(invocation string) string {
	return psTaskSchedulerPreamble + "\n" + invocation
}

// psTraceLines reads the task scheduler trace written by the fixtures,
// one call per line, tolerating the CRLF terminators of Windows.
func psTraceLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the task scheduler trace is missing: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestPsTaskSchedulerFixturesCoverEveryCmdlet: the fixture preamble
// must define a function for every ScheduledTask cmdlet that
// install.ps1 calls, so no installer run from these tests can reach
// the real Task Scheduler. It runs on every platform: it compares the
// fixture definitions with the script's call sites statically.
func TestPsTaskSchedulerFixturesCoverEveryCmdlet(t *testing.T) {
	script, err := os.ReadFile("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, name := range regexp.MustCompile(`[A-Z][A-Za-z]*-ScheduledTask[A-Za-z]*`).FindAllString(string(script), -1) {
		used[name] = true
	}
	if len(used) == 0 {
		t.Fatal("no ScheduledTask cmdlet found in install.ps1; the extraction changed")
	}
	preamble := psTaskSchedulerPreamble
	for name := range used {
		if !strings.Contains(preamble, "function "+name+" {") {
			t.Errorf("install.ps1 uses %s but the fixtures define no replacement for it", name)
		}
	}
}

func TestInstallShSyntax(t *testing.T) {
	sh := shAvailable(t)
	out, err := exec.Command(sh, "-n", "install.sh").CombinedOutput()
	if err != nil {
		t.Fatalf("sh -n install.sh: %v\n%s", err, out)
	}
}

func TestInstallShHelp(t *testing.T) {
	sh := shAvailable(t)
	out, err := exec.Command(sh, "install.sh", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("sh install.sh --help: %v\n%s", sh, out)
	}
	text := string(out)
	for _, opt := range []string{"--ref", "--bin-dir", "--machine-label", "--agent", "--remove-agent", "--agent-dry-run", "--help"} {
		if !strings.Contains(text, opt) {
			t.Errorf("install.sh --help does not list %q:\n%s", opt, text)
		}
	}
}

// TestInstallShAgentDryRun: --agent-dry-run prints the plist when a
// hub.json exists and the refusal notice when it does not, and installs
// nothing either way (no plist, no log, no bin dir, no go install).
func TestInstallShAgentDryRun(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "temp")
	backend := filepath.Join(root, "backend")
	for _, dir := range []string{home, tmp, backend} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// A stand-in soho on the PATH, so the service PATH resolves it.
	if err := os.WriteFile(filepath.Join(backend, "soho"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hubFile := filepath.Join(home, ".config", "nau", "hub.json")
	plist := filepath.Join(home, "Library", "LaunchAgents", "djalmajr.nau.agent.plist")
	run := func() (string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, sh, "install.sh", "--agent-dry-run")
		cmd.WaitDelay = time.Second
		cmd.Env = []string{
			"HOME=" + home,
			"TMPDIR=" + tmp,
			"PATH=" + strings.Join([]string{backend, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		return stdout.String() + stderr.String(), code
	}
	// Without hub.json: the refusal notice, nothing installed.
	out, code := run()
	if code != 0 {
		t.Fatalf("dry run without hub.json exit=%d: %s", code, out)
	}
	if !strings.Contains(out, "hub.json") || !strings.Contains(out, "nau connect") {
		t.Fatalf("dry run without hub.json lacks the refusal notice: %s", out)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("dry run without hub.json created the plist: %v", err)
	}
	// With a synthetic hub.json: the expected plist, still nothing
	// installed.
	if err := os.MkdirAll(filepath.Dir(hubFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubFile, []byte(`{"url":"http://127.0.0.1:17440","machine_id":"machine-a","key":"synthetic"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, code = run()
	if code != 0 {
		t.Fatalf("dry run with hub.json exit=%d: %s", code, out)
	}
	for _, want := range []string{
		`<string>djalmajr.nau.agent</string>`,
		"<string>agent</string>",
		"<string>run</string>",
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
		"nau-agent.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run plist lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("dry run with hub.json created the plist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents")); !os.IsNotExist(err) {
		t.Fatalf("dry run created the LaunchAgents directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "Logs", "nau-agent.log")); !os.IsNotExist(err) {
		t.Fatalf("dry run created the log file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin")); !os.IsNotExist(err) {
		t.Fatalf("dry run created the bin directory: %v", err)
	}
}

// TestInstallShAgentDryRunXDGConfigHome: on macOS the resolver follows
// XDG_CONFIG_HOME, so --agent-dry-run carries the variable into the
// plist (escaped), keeps the classic plist shape when it is unset, and
// refuses a relative value before any step.
func TestInstallShAgentDryRunXDGConfigHome(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	plutil, plutilErr := exec.LookPath("plutil")
	lint := func(t *testing.T, name, plist string) {
		t.Helper()
		if plutilErr != nil {
			t.Logf("plutil not available; skipping the plist lint for %s", name)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, plutil, "-lint", "-")
		cmd.WaitDelay = time.Second
		cmd.Stdin = strings.NewReader(plist)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("plutil -lint rejects the %s plist: %v\n%s", name, err, out)
		}
	}
	run := func(env []string) (string, string, int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, sh, "install.sh", "--agent-dry-run")
		cmd.WaitDelay = time.Second
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	// The & in the path is hostile input for the plist: it must come
	// out as the entity and still lint.
	xdg := filepath.Join(root, "config & xdg")
	tmp := filepath.Join(root, "temp")
	for _, dir := range []string{home, xdg, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	hubJSON := []byte(`{"url":"http://127.0.0.1:17440","machine_id":"machine-a","key":"synthetic"}` + "\n")
	base := []string{
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"PATH=" + strings.Join([]string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
	}
	// Absolute XDG_CONFIG_HOME with hub.json inside it: the plist
	// carries the variable, escaped.
	if err := os.MkdirAll(filepath.Join(xdg, "nau"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "nau", "hub.json"), hubJSON, 0600); err != nil {
		t.Fatal(err)
	}
	withXDG := append(append([]string{}, base...), "XDG_CONFIG_HOME="+xdg)
	stdout, stderr, code := run(withXDG)
	if code != 0 {
		t.Fatalf("dry run with absolute XDG_CONFIG_HOME exit=%d: %s%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "<key>XDG_CONFIG_HOME</key>") {
		t.Fatalf("dry run plist lacks the XDG_CONFIG_HOME key:\n%s", stdout)
	}
	if want := "<string>" + strings.ReplaceAll(xdg, "&", "&amp;") + "</string>"; !strings.Contains(stdout, want) {
		t.Fatalf("dry run plist lacks the escaped XDG_CONFIG_HOME value %s:\n%s", want, stdout)
	}
	lint(t, "absolute XDG_CONFIG_HOME", stdout)
	// No XDG_CONFIG_HOME and hub.json in home/.config/nau: the classic
	// plist shape, without the key.
	if err := os.MkdirAll(filepath.Join(home, ".config", "nau"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "nau", "hub.json"), hubJSON, 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = run(base)
	if code != 0 {
		t.Fatalf("dry run without XDG_CONFIG_HOME exit=%d: %s%s", code, stdout, stderr)
	}
	if strings.Contains(stdout, "XDG_CONFIG_HOME") {
		t.Fatalf("dry run plist carries XDG_CONFIG_HOME though it is unset:\n%s", stdout)
	}
	lint(t, "no XDG_CONFIG_HOME", stdout)
	// Relative XDG_CONFIG_HOME: refused before any step, the exact line
	// on stderr and nothing on stdout.
	stdout, stderr, code = run(append(append([]string{}, base...), "XDG_CONFIG_HOME=nau-config"))
	if code != 2 {
		t.Fatalf("dry run with relative XDG_CONFIG_HOME exit=%d want=2: %s%s", code, stdout, stderr)
	}
	if stderr != "nau install: XDG_CONFIG_HOME must be an absolute path\n" {
		t.Fatalf("relative XDG refusal stderr=%q", stderr)
	}
	if stdout != "" {
		t.Fatalf("relative XDG refusal wrote to stdout: %s", stdout)
	}
}

// TestInstallShAgentRefusalWithoutHubFile: with --agent and no hub.json
// the install completes with a warning pointing at nau connect and
// creates no service; on a platform without the plist the flag is
// refused before any step.
func TestInstallShAgentRefusalWithoutHubFile(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer")
	}
	if runtime.GOOS != "darwin" {
		// Refused before the go check.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, sh, "install.sh", "--agent").CombinedOutput()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != 2 || !strings.Contains(string(out), "supported on macOS and Windows only") {
			t.Fatalf("non-macOS refusal: exit=%d out=%s", code, out)
		}
		return
	}
	buildDir := t.TempDir()
	nau := filepath.Join(buildDir, "nau")
	helper := filepath.Join(buildDir, "helper")
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "fixture space")
	home := filepath.Join(root, "home space")
	local := filepath.Join(root, "local space")
	goDir := filepath.Join(root, "go space")
	backendDir := filepath.Join(root, "backend space")
	tmp := filepath.Join(root, "temp space")
	bin := filepath.Join(root, "bin space")
	for _, dir := range []string{home, local, goDir, backendDir, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goDir, "go"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, "soho"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	// No hub.json anywhere: the refusal path.
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"APPDATA=" + filepath.Join(home, "appdata"),
		"LOCALAPPDATA=" + local,
		"TMPDIR=" + tmp,
		"TEMP=" + tmp,
		"TMP=" + tmp,
		"PATH=" + strings.Join([]string{goDir, backendDir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		"INSTALL_FIXTURE_NAU=" + nau,
	}
	ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "install.sh", "--bin-dir", bin, "--agent")
	cmd.WaitDelay = time.Second
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err != nil && ctx.Err() == nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("installer failed: %v\n%s%s", err, stdout.String(), stderr.String())
		}
		if exit.ExitCode() != 0 {
			t.Fatalf("installer exit=%d: %s%s", exit.ExitCode(), stdout.String(), stderr.String())
		}
	}
	output := stdout.String() + stderr.String()
	if !strings.Contains(stderr.String(), "nau connect") || !strings.Contains(stderr.String(), "hub.json") {
		t.Fatalf("missing the connect warning on stderr: %s", output)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "djalmajr.nau.agent.plist")
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("the agent service was created without hub.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bin, "nau")); err != nil {
		t.Fatalf("the install did not complete: %v", err)
	}
}

// TestInstallShAgentRestartOnRerun: a re-run without --agent restarts
// the agent service when the plist exists (so the machine never runs
// the old binary) and waits for its fresh health file; without the
// plist it creates no service state and calls launchctl nothing.
// launchctl is a fixture: it records its calls and, on a (re)start,
// writes the fresh health file after a short sleep, so the mtime test
// of the health check is deterministic at second granularity.
func TestInstallShAgentRestartOnRerun(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	buildDir := t.TempDir()
	nau := filepath.Join(buildDir, "nau")
	helper := filepath.Join(buildDir, "helper")
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "fixture space")
	home := filepath.Join(root, "home space")
	fakeBin := filepath.Join(root, "fake space")
	goDir := filepath.Join(root, "go space")
	backendDir := filepath.Join(root, "backend space")
	tmp := filepath.Join(root, "temp space")
	bin := filepath.Join(root, "bin space")
	for _, dir := range []string{home, fakeBin, goDir, backendDir, tmp, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goDir, "go"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, "soho"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(tmp, "launchctl.trace")
	stateFile := filepath.Join(home, ".config", "nau", "agent-state.json")
	launchctl := `#!/bin/sh
echo "launchctl $*" >> "$FAKE_LAUNCHCTL_TRACE"
case "$1" in
print)
    exit 0
    ;;
kickstart | bootstrap)
    sleep 1.1
    [ -n "$FAKE_LAUNCHCTL_STATE" ] && { mkdir -p "$(dirname -- "$FAKE_LAUNCHCTL_STATE")"; date > "$FAKE_LAUNCHCTL_STATE"; }
    exit 0
    ;;
*)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "launchctl"), []byte(launchctl), 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"APPDATA=" + filepath.Join(home, "appdata"),
		"LOCALAPPDATA=" + filepath.Join(home, "local"),
		"TMPDIR=" + tmp,
		"TEMP=" + tmp,
		"TMP=" + tmp,
		"PATH=" + strings.Join([]string{fakeBin, goDir, backendDir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
		"INSTALL_FIXTURE_NAU=" + nau,
		"FAKE_LAUNCHCTL_TRACE=" + trace,
		"FAKE_LAUNCHCTL_STATE=" + stateFile,
	}
	run := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
		defer cancel()
		cmd := exec.CommandContext(ctx, sh, "install.sh", "--bin-dir", bin)
		cmd.WaitDelay = time.Second
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		out := stdout.String() + stderr.String()
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("installer did not finish within %s: %s", installerRunBound, out)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("installer failed: %v\n%s", err, out)
			}
			if exit.ExitCode() != 0 {
				t.Fatalf("installer exit=%d: %s", exit.ExitCode(), out)
			}
		}
		return out
	}
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plist := filepath.Join(plistDir, "djalmajr.nau.agent.plist")
	plistContent := []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict><key>Label</key><string>djalmajr.nau.agent</string></dict></plist>\n")
	// With the plist present: the re-run without the flag restarts the
	// service over the fresh binary and waits for its fresh health.
	if err := os.MkdirAll(plistDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, plistContent, 0600); err != nil {
		t.Fatal(err)
	}
	out := run()
	traceData, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("launchctl was never called (the service was not restarted): %v\n%s", err, out)
	}
	if !strings.Contains(string(traceData), "kickstart -k gui/") || !strings.Contains(string(traceData), "djalmajr.nau.agent") {
		t.Fatalf("the restart is missing from the launchctl trace:\n%s", traceData)
	}
	if !strings.Contains(out, "nau install: the agent service started; run nau doctor to check delivery") {
		t.Fatalf("the fresh health wait did not report the service started:\n%s", out)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("the fresh health file did not appear after the restart: %v", err)
	}
	after, err := os.ReadFile(plist)
	if err != nil || !bytes.Equal(after, plistContent) {
		t.Fatalf("the restart rewrote the plist: %v\n%s", err, after)
	}
	if _, err := os.Stat(filepath.Join(bin, "nau")); err != nil {
		t.Fatalf("the install did not complete: %v", err)
	}
	// Without the plist: the re-run installs the binary and creates no
	// service state, calling launchctl nothing.
	if err := os.RemoveAll(plistDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(trace); err != nil {
		t.Fatal(err)
	}
	out = run()
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("launchctl was called without an installed service:\n%s%s", trace, out)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("the re-run without the flag created the plist: %v", err)
	}
	if _, err := os.Stat(plistDir); !os.IsNotExist(err) {
		t.Fatalf("the re-run without the flag created the LaunchAgents directory: %v", err)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("the re-run without the flag wrote the health file: %v", err)
	}
	if !strings.Contains(out, "nau install: installed") {
		t.Fatalf("the install did not complete:\n%s", out)
	}
}

// TestInstallShAgentBootstrapRetry: a native launchctl bootstrap can
// exit 5 (an observed Input/output error right after the bootout of
// the same job); the installer must retry only that exit, at the same
// domain and plist path, at most 40 attempts 0.5 s apart, bound by
// the attempt count alone. A recovered bootstrap still goes through
// the fresh-health check and the service-started wording; a
// permanent 5 exhausts the bound and exits 5 without reporting the
// install or the service as started; any other nonzero exit fails
// immediately with its own code. launchctl, sleep and date are
// fixtures on the PATH: every native call lands in a trace with the
// exit code the fixture chose, the retry delay is instant, and the
// wall clock can be driven backward.
//
// Mutation captured: removing the retry after the first bootstrap 5
// turns the two recovery cases red (the installer exits 5 on the
// first attempt); dropping the attempt bound turns the exhaustion
// cases red (more than 40 bootstrap attempts).
func TestInstallShAgentBootstrapRetry(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	buildDir := t.TempDir()
	nau := filepath.Join(buildDir, "nau")
	helper := filepath.Join(buildDir, "helper")
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	// The launchctl fixture records every call with the exit code it
	// chose, counts the bootstrap attempts, and writes the fresh
	// health file only when a bootstrap succeeds.
	const launchctlFixture = `#!/bin/sh
code=0
case "$1" in
print)
    code="${FAKE_LAUNCHCTL_PRINT_EXIT:-1}"
    ;;
bootout)
    code=0
    ;;
bootstrap)
    n=1
    if [ -f "$FAKE_LAUNCHCTL_COUNT" ]; then
        n=$(( $(cat "$FAKE_LAUNCHCTL_COUNT") + 1 ))
    fi
    printf '%s\n' "$n" > "$FAKE_LAUNCHCTL_COUNT"
    if [ "$n" -le "${FAKE_LAUNCHCTL_BOOTSTRAP_FAIL_TIMES:-0}" ]; then
        code="${FAKE_LAUNCHCTL_BOOTSTRAP_EXIT:-5}"
    else
        sleep 1.1
        if [ -n "${FAKE_LAUNCHCTL_STATE:-}" ]; then
            mkdir -p "$(dirname -- "$FAKE_LAUNCHCTL_STATE")"
            date > "$FAKE_LAUNCHCTL_STATE"
        fi
        code=0
    fi
    ;;
*)
    code=0
    ;;
esac
echo "launchctl $* exit=$code" >> "$FAKE_LAUNCHCTL_TRACE"
exit "$code"
`
	// The sleep fixture makes the 0.5 s retry delays instant while
	// counting them.
	const sleepFixture = `#!/bin/sh
echo "sleep $*" >> "$FAKE_SLEEP_TRACE"
exit 0
`
	// The date fixture answers every call with a wall clock that
	// moves backward one second per call.
	const dateFixture = `#!/bin/sh
n=1
if [ -f "$FAKE_DATE_COUNT" ]; then
    n=$(( $(cat "$FAKE_DATE_COUNT") + 1 ))
fi
printf '%s\n' "$n" > "$FAKE_DATE_COUNT"
echo "$((1000000 - n))"
`
	cases := []struct {
		name           string
		loaded         bool // launchctl print exits 0: the job is loaded
		prePlist       bool // a pre-existing different plist: a changed rewrite
		failTimes      int  // bootstrap attempts the fixture exits nonzero for first
		failExit       int  // the code of those attempts
		fakeSleep      bool // instant counted sleep on the PATH
		backwardDate   bool // backward wall clock on the PATH
		wantExit       int
		wantBootstraps int
		wantSleeps     int
	}{
		{
			name:           "first bootstrap success stays a single call",
			failTimes:      0,
			failExit:       5,
			wantExit:       0,
			wantBootstraps: 1,
		},
		{name: "changed loaded job recovers from one bootstrap 5", loaded: true, prePlist: true, failTimes: 1, failExit: 5, wantExit: 0, wantBootstraps: 2},
		{name: "unloaded service recovers from one bootstrap 5", failTimes: 1, failExit: 5, wantExit: 0, wantBootstraps: 2},
		{name: "permanent 5 exhausts the 40 attempt bound", failTimes: 100, failExit: 5, fakeSleep: true, wantExit: 5, wantBootstraps: 40, wantSleeps: 39},
		{name: "non-5 bootstrap exit fails immediately with its code", failTimes: 100, failExit: 7, fakeSleep: true, wantExit: 7, wantBootstraps: 1},
		{name: "backward wall clock does not move the attempt bound", failTimes: 100, failExit: 5, fakeSleep: true, backwardDate: true, wantExit: 5, wantBootstraps: 40, wantSleeps: 39},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fixture space")
			home := filepath.Join(root, "home space")
			fakeBin := filepath.Join(root, "fake space")
			goDir := filepath.Join(root, "go space")
			backendDir := filepath.Join(root, "backend space")
			tmp := filepath.Join(root, "temp space")
			bin := filepath.Join(root, "bin space")
			for _, dir := range []string{home, fakeBin, goDir, backendDir, tmp, bin} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for path, data := range map[string][]byte{
				filepath.Join(goDir, "go"):          helperBytes,
				filepath.Join(backendDir, "soho"):   helperBytes,
				filepath.Join(fakeBin, "launchctl"): []byte(launchctlFixture),
			} {
				if err := os.WriteFile(path, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fakeSleep {
				if err := os.WriteFile(filepath.Join(fakeBin, "sleep"), []byte(sleepFixture), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.backwardDate {
				if err := os.WriteFile(filepath.Join(fakeBin, "date"), []byte(dateFixture), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// hub.json, so --agent installs the service.
			cfgDir := filepath.Join(home, ".config", "nau")
			if err := os.MkdirAll(cfgDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfgDir, "hub.json"), []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			plistDir := filepath.Join(home, "Library", "LaunchAgents")
			plist := filepath.Join(plistDir, "djalmajr.nau.agent.plist")
			if tc.prePlist {
				if err := os.MkdirAll(plistDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(plist, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Label</key><string>djalmajr.nau.agent</string></dict></plist>
`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			trace := filepath.Join(tmp, "launchctl.trace")
			sleepTrace := filepath.Join(tmp, "sleep.trace")
			stateFile := filepath.Join(cfgDir, "agent-state.json")
			env := []string{
				"HOME=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"APPDATA=" + filepath.Join(home, "appdata"),
				"LOCALAPPDATA=" + filepath.Join(home, "local"),
				"TMPDIR=" + tmp,
				"TEMP=" + tmp,
				"TMP=" + tmp,
				"PATH=" + strings.Join([]string{fakeBin, goDir, backendDir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
				"INSTALL_FIXTURE_NAU=" + nau,
				"FAKE_LAUNCHCTL_TRACE=" + trace,
				"FAKE_LAUNCHCTL_COUNT=" + filepath.Join(tmp, "bootstrap.count"),
				"FAKE_LAUNCHCTL_BOOTSTRAP_FAIL_TIMES=" + strconv.Itoa(tc.failTimes),
				"FAKE_LAUNCHCTL_BOOTSTRAP_EXIT=" + strconv.Itoa(tc.failExit),
				"FAKE_LAUNCHCTL_STATE=" + stateFile,
			}
			if tc.loaded {
				env = append(env, "FAKE_LAUNCHCTL_PRINT_EXIT=0")
			}
			if tc.fakeSleep {
				env = append(env, "FAKE_SLEEP_TRACE="+sleepTrace)
			}
			if tc.backwardDate {
				env = append(env, "FAKE_DATE_COUNT="+filepath.Join(tmp, "date.count"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "install.sh", "--bin-dir", bin, "--agent")
			cmd.WaitDelay = time.Second
			cmd.Env = env
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			out := stdout.String() + stderr.String()
			code := 0
			if runErr != nil {
				if ctx.Err() != nil {
					t.Fatalf("installer did not finish within %s: %s", installerRunBound, out)
				}
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) {
					t.Fatalf("installer failed: %v\n%s", runErr, out)
				}
				code = exit.ExitCode()
			}
			if code != tc.wantExit {
				t.Fatalf("installer exit=%d want=%d:\n%s", code, tc.wantExit, out)
			}
			traceData, err := os.ReadFile(trace)
			if err != nil {
				t.Fatalf("the launchctl trace is missing: %v\n%s", err, out)
			}
			var lines []string
			for _, line := range strings.Split(string(traceData), "\n") {
				if line != "" {
					lines = append(lines, line)
				}
			}
			// The exact expected sequence: one print (exiting 0 when the
			// job is loaded, 1 otherwise), the bootout when a loaded job
			// is rewritten, then the bootstrap attempts, all at the same
			// domain and plist path.
			printExit := 1
			if tc.loaded {
				printExit = 0
			}
			printRe := regexp.MustCompile(`^launchctl print gui/(\d+)/djalmajr\.nau\.agent exit=` + strconv.Itoa(printExit) + `$`)
			want := []string{}
			uid := ""
			for _, line := range lines {
				if m := printRe.FindStringSubmatch(line); m != nil {
					uid = m[1]
					break
				}
			}
			if uid == "" {
				t.Fatalf("the launchctl trace has no print query:\n%s", traceData)
			}
			want = append(want, "launchctl print gui/"+uid+"/djalmajr.nau.agent exit="+strconv.Itoa(printExit))
			if tc.loaded {
				want = append(want, "launchctl bootout gui/"+uid+"/djalmajr.nau.agent exit=0")
			}
			bootstrapCmd := "launchctl bootstrap gui/" + uid + " " + plist
			for i := 1; i <= tc.wantBootstraps; i++ {
				if i <= tc.failTimes {
					want = append(want, bootstrapCmd+" exit="+strconv.Itoa(tc.failExit))
				} else {
					want = append(want, bootstrapCmd+" exit=0")
				}
			}
			if len(lines) != len(want) {
				t.Fatalf("the launchctl trace has %d calls, want %d:\n%s", len(lines), len(want), traceData)
			}
			for i := range want {
				if lines[i] != want[i] {
					t.Fatalf("launchctl call %d = %q, want %q:\n%s", i+1, lines[i], want[i], traceData)
				}
			}
			sleeps := 0
			if sleepData, err := os.ReadFile(sleepTrace); err == nil {
				for _, line := range strings.Split(string(sleepData), "\n") {
					if line != "" {
						sleeps++
					}
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if sleeps != tc.wantSleeps {
				t.Fatalf("the retry delay ran %d times, want %d", sleeps, tc.wantSleeps)
			}
			started := "nau install: the agent service started; run nau doctor to check delivery"
			if tc.wantExit == 0 {
				if !strings.Contains(stderr.String(), started) {
					t.Fatalf("the fresh health wait did not report the service started:\n%s", out)
				}
				if tc.wantExit == 0 && tc.failTimes > 0 {
					retryNote := "nau install: launchctl bootstrap exited 5 (attempt 1 of 40); retrying"
					if !strings.Contains(stderr.String(), retryNote) {
						t.Fatalf("the retried bootstrap 5 left no diagnostic on stderr:\n%s", stderr.String())
					}
				}
				if _, err := os.Stat(stateFile); err != nil {
					t.Fatalf("the fresh health file did not appear after the recovered bootstrap: %v", err)
				}
			} else {
				if strings.Contains(out, started) {
					t.Fatalf("a failed bootstrap reported the service started:\n%s", out)
				}
				if strings.Contains(out, "nau install: installed") {
					t.Fatalf("a failed bootstrap reported the install completed:\n%s", out)
				}
				if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
					t.Fatalf("a failed bootstrap wrote the health file: %v", err)
				}
			}
			// The executable and the plist publication are preserved
			// over a bootstrap failure: no rollback, the generated plist
			// stays in place.
			if _, err := os.Stat(filepath.Join(bin, "nau")); err != nil {
				t.Fatalf("the install did not reach the binary swap: %v", err)
			}
			plistData, err := os.ReadFile(plist)
			if err != nil {
				t.Fatalf("the plist is missing after the run: %v", err)
			}
			if !strings.Contains(string(plistData), "<key>KeepAlive</key>") {
				t.Fatalf("the plist was not the generated one:\n%s", plistData)
			}
		})
	}
}

// TestInstallShAgentRerunReadsServiceDir: a re-run without --agent keeps
// the plist, so it waits for the health file in the directory that plist
// gives the service, not in the one of its own environment, and warns when
// the two differ. The plist comes from a real dry run. The directory the
// service writes to comes from that plist too, through a parser of its own:
// the fixture never gets told the path the installer expects, and a plutil
// on the PATH that always fails proves the installer does not rely on it.
func TestInstallShAgentRerunReadsServiceDir(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	buildDir := t.TempDir()
	nau := filepath.Join(buildDir, "nau")
	helper := filepath.Join(buildDir, "helper")
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	const launchctl = `#!/bin/sh
case "$1" in
kickstart | bootstrap)
    sleep 1.1
    mkdir -p "$FAKE_LAUNCHCTL_SERVICE_DIR"
    date > "$FAKE_LAUNCHCTL_SERVICE_DIR/agent-state.json"
    ;;
esac
exit 0
`
	cases := []struct {
		name               string
		serviceXDG, runXDG string // relative to the fixture root; empty leaves the variable out
		wantWarning        bool
	}{
		{name: "service without XDG, run with it", runXDG: "run & xdg", wantWarning: true},
		{name: "service with XDG, run without it", serviceXDG: "service & xdg", wantWarning: true},
		{name: "service and run with different XDG", serviceXDG: "service & xdg", runXDG: "run & xdg", wantWarning: true},
		{name: "service and run with the same XDG", serviceXDG: "same & xdg", runXDG: "same & xdg"},
		{name: "the run spells the same XDG with a trailing slash", serviceXDG: "same & xdg", runXDG: "same & xdg/"},
		{name: "the service spells the same XDG with a trailing slash", serviceXDG: "same & xdg//", runXDG: "same & xdg"},
		{name: "neither with XDG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fixture space")
			home := filepath.Join(root, "home space")
			fakeBin := filepath.Join(root, "fake space")
			goDir := filepath.Join(root, "go space")
			backendDir := filepath.Join(root, "backend space")
			tmp := filepath.Join(root, "temp space")
			bin := filepath.Join(root, "bin space")
			for _, dir := range []string{home, fakeBin, goDir, backendDir, tmp, bin} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for path, data := range map[string][]byte{
				filepath.Join(goDir, "go"):          helperBytes,
				filepath.Join(backendDir, "soho"):   helperBytes,
				filepath.Join(fakeBin, "launchctl"): []byte(launchctl),
				filepath.Join(fakeBin, "plutil"):    []byte("#!/bin/sh\nexit 1\n"),
			} {
				if err := os.WriteFile(path, data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			plist := filepath.Join(home, "Library", "LaunchAgents", "djalmajr.nau.agent.plist")
			var serviceDirFromPlist string
			// configDir is the nau directory for an XDG value of the case.
			configDir := func(xdg string) string {
				if xdg == "" {
					return filepath.Join(home, ".config", "nau")
				}
				return filepath.Join(root, xdg, "nau")
			}
			envFor := func(xdg string) []string {
				env := []string{
					"HOME=" + home,
					"TMPDIR=" + tmp,
					"TEMP=" + tmp,
					"TMP=" + tmp,
					"PATH=" + strings.Join([]string{fakeBin, goDir, backendDir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"),
					"INSTALL_FIXTURE_NAU=" + nau,
					"FAKE_LAUNCHCTL_SERVICE_DIR=" + serviceDirFromPlist,
				}
				if xdg != "" {
					// Not filepath.Join, which would clean a trailing slash.
					env = append(env, "XDG_CONFIG_HOME="+root+"/"+xdg)
				}
				return env
			}
			run := func(xdg string, args ...string) (string, string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
				defer cancel()
				cmd := exec.CommandContext(ctx, sh, append([]string{"install.sh", "--bin-dir", bin}, args...)...)
				cmd.WaitDelay = time.Second
				cmd.Env = envFor(xdg)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("install.sh %v: %v\n%s%s", args, err, stdout.String(), stderr.String())
				}
				return stdout.String(), stderr.String()
			}
			// The service as a real install leaves it.
			serviceDir := configDir(tc.serviceXDG)
			if err := os.MkdirAll(serviceDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(serviceDir, "hub.json"), []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			plistContent, _ := run(tc.serviceXDG, "--agent-dry-run")
			if err := os.MkdirAll(filepath.Dir(plist), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plist, []byte(plistContent), 0600); err != nil {
				t.Fatal(err)
			}
			serviceDirFromPlist = filepath.Join(home, ".config", "nau")
			if xdg := plistXDGConfigHome(t, []byte(plistContent)); xdg != "" {
				serviceDirFromPlist = filepath.Join(xdg, "nau")
			}
			runDir := configDir(tc.runXDG)
			_, stderr := run(tc.runXDG)
			if !strings.Contains(stderr, "nau install: the agent service started; run nau doctor to check delivery") {
				t.Fatalf("the health wait did not see the service:\n%s", stderr)
			}
			if _, err := os.Stat(filepath.Join(serviceDir, "agent-state.json")); err != nil {
				t.Fatalf("the service did not write its health file: %v", err)
			}
			warning := "nau install: warning: the agent service reads " + serviceDir + ", not " + runDir + "; run the installer with --agent to move it\n"
			if got := strings.Contains(stderr, warning); got != tc.wantWarning {
				t.Fatalf("warning present = %v, want %v; looked for %q in:\n%s", got, tc.wantWarning, warning, stderr)
			}
			if !tc.wantWarning && strings.Contains(stderr, "the agent service reads") {
				t.Fatalf("a warning with the service in its own directory:\n%s", stderr)
			}
			after, err := os.ReadFile(plist)
			if err != nil || !bytes.Equal(after, []byte(plistContent)) {
				t.Fatalf("the re-run rewrote the plist: %v\n%s", err, after)
			}
			if _, err := os.Stat(filepath.Join(bin, "nau")); err != nil {
				t.Fatalf("the install did not complete: %v", err)
			}
		})
	}
}

// plistStringAfterKey returns the text of the <string> value that
// follows the <key> element named key in the plist, or "" and false
// when the plist parses but carries no such key. It fatals when the
// plist does not parse, and never reports a value of another key:
// afterKey is re-tested on every key element.
func plistStringAfterKey(t *testing.T, data []byte, key string) (string, bool) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	var text string
	afterKey := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return "", false
		}
		if err != nil {
			t.Fatalf("the plist does not parse: %v\n%s", err, data)
		}
		switch el := tok.(type) {
		case xml.CharData:
			text = string(el)
		case xml.EndElement:
			switch el.Name.Local {
			case "key":
				afterKey = text == key
			case "string":
				if afterKey {
					return text, true
				}
			}
		}
	}
}

// plistXDGConfigHome returns the value of the XDG_CONFIG_HOME key of a plist,
// or "" without that key.
func plistXDGConfigHome(t *testing.T, data []byte) string {
	value, _ := plistStringAfterKey(t, data, "XDG_CONFIG_HOME")
	return value
}

// plutilLint lints the generated plist with the system plutil, or logs
// and skips the lint when plutil is not on the PATH.
func plutilLint(t *testing.T, plist string) {
	t.Helper()
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Logf("plutil not available; skipping the plist lint")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, plutil, "-lint", "-")
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(plist)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint rejects the plist: %v\n%s", err, out)
	}
}

// TestInstallShAgentServicePath: the plist that --agent-dry-run prints
// gives the service a PATH of the discovered directories only -- the
// bin dir, the soho dir and the herdr dir, in that order, followed by
// the system entries -- a shared directory stays once and the rest of
// the interactive PATH is left out, with a warning when a backend is
// missing. The fixture paths carry spaces and an XML special character,
// and the service PATH is read back from the generated plist, never
// matched in the script text. The relative case names soho and herdr
// with bare PATH entries and runs the installer from the fixture root
// under a shell whose command -v answers with a relative pathname, so
// the persisted directories must be absolute: the service starts from
// another cwd and must still resolve both executables with the service
// PATH alone.
func TestInstallShAgentServicePath(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS != "darwin" {
		t.Skip("the agent LaunchAgent is macOS only; other platforms refuse the flag")
	}
	// The script path, absolute before the relative case runs the
	// command from the fixture directory.
	installPath, err := filepath.Abs("install.sh")
	if err != nil {
		t.Fatalf("absolute script path: %v", err)
	}
	system := []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	cases := []struct {
		name         string
		sohoDir      string // relative to the fixture root; "" leaves soho off the PATH
		herdrDir     string
		want         []string // relative to the fixture root, in the service PATH order
		herdrWarning bool
		// relative names sohoDir and herdrDir on the PATH as bare
		// entries, runs the installer from the fixture root under a
		// shell whose command -v answers with a relative pathname.
		relative bool
		cdpath   bool
	}{
		{
			name:     "distinct nau, soho and herdr directories",
			sohoDir:  "soho space",
			herdrDir: "herdr & space",
			want:     []string{"bin space", "soho space", "herdr & space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
		{
			name:     "soho and herdr share a directory",
			sohoDir:  "shared space",
			herdrDir: "shared space",
			want:     []string{"bin space", "shared space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
		{
			name:     "soho in the bin directory",
			sohoDir:  "bin space",
			herdrDir: "herdr space",
			want:     []string{"bin space", "herdr space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
		{
			name:         "herdr missing",
			sohoDir:      "soho space",
			herdrDir:     "",
			want:         []string{"bin space", "soho space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
			herdrWarning: true,
		},
		{
			name:     "soho missing",
			sohoDir:  "",
			herdrDir: "herdr space",
			want:     []string{"bin space", "herdr space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
		{
			name:     "relative soho and herdr PATH entries stay absolute in the plist",
			sohoDir:  "rel soho space",
			herdrDir: "rel herdr & space",
			relative: true,
			want:     []string{"bin space", "rel soho space", "rel herdr & space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
		{
			name:     "relative entries ignore exported CDPATH",
			sohoDir:  "rel soho space",
			herdrDir: "rel herdr & space",
			relative: true,
			cdpath:   true,
			want:     []string{"bin space", "rel soho space", "rel herdr & space", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// dash answers command -v with a relative pathname when
			// the PATH entry is relative: the class the service PATH
			// must survive.
			shell := sh
			if tc.relative {
				dash, err := exec.LookPath("dash")
				if err != nil {
					t.Skip("dash is not available; the relative PATH case needs a shell whose command -v answers with a relative pathname")
				}
				shell = dash
			}
			root := t.TempDir()
			home := filepath.Join(root, "home space")
			tmp := filepath.Join(root, "temp space")
			bin := filepath.Join(root, "bin space")
			extra := filepath.Join(root, "extra space")
			dirs := map[string]bool{home: true, tmp: true, bin: true, extra: true}
			if tc.sohoDir != "" {
				dirs[filepath.Join(root, tc.sohoDir)] = true
			}
			if tc.herdrDir != "" {
				dirs[filepath.Join(root, tc.herdrDir)] = true
			}
			for dir := range dirs {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			// hub.json, so the dry run prints the plist.
			if err := os.MkdirAll(filepath.Join(home, ".config", "nau"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".config", "nau", "hub.json"), []byte(`{"url":"http://localhost:17440","machine_id":"machine-a","key":"synthetic"}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// The fixture binaries the installer discovers on the PATH.
			if tc.sohoDir != "" {
				if err := os.WriteFile(filepath.Join(root, tc.sohoDir, "soho"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.herdrDir != "" {
				if err := os.WriteFile(filepath.Join(root, tc.herdrDir, "herdr"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// The interactive PATH the installer sees: the discovery
			// dirs, a dir the service PATH must not copy, and the
			// system dirs. The relative case names the discovery
			// dirs as bare entries, resolved against the installer's
			// cwd, the way a user PATH entry can be relative.
			var paths []string
			if tc.sohoDir != "" {
				if tc.relative {
					paths = append(paths, tc.sohoDir)
				} else {
					paths = append(paths, filepath.Join(root, tc.sohoDir))
				}
			}
			if tc.herdrDir != "" {
				if tc.relative {
					paths = append(paths, tc.herdrDir)
				} else {
					paths = append(paths, filepath.Join(root, tc.herdrDir))
				}
			}
			paths = append(paths, extra)
			paths = append(paths, system...)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, shell, installPath, "--bin-dir", bin, "--agent-dry-run")
			cmd.WaitDelay = time.Second
			if tc.relative {
				cmd.Dir = root
			}
			cmd.Env = []string{
				"HOME=" + home,
				"TMPDIR=" + tmp,
				"PATH=" + strings.Join(paths, ":"),
			}
			if tc.cdpath {
				// A navigation preference must not redirect executable discovery.
				for _, name := range []string{tc.sohoDir, tc.herdrDir} {
					if err := os.MkdirAll(filepath.Join(extra, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				cmd.Env = append(cmd.Env, "CDPATH="+extra+":"+root)
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				out := stdout.String() + stderr.String()
				if ctx.Err() != nil {
					t.Fatalf("dry run did not finish within the bound: %s", out)
				}
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("dry run failed: %v\n%s", err, out)
				}
				t.Fatalf("dry run exit=%d want=0:\n%s", exit.ExitCode(), out)
			}
			// The service PATH read back from the generated plist,
			// never matched in the script text.
			servicePath, ok := plistStringAfterKey(t, []byte(stdout.String()), "PATH")
			if !ok {
				t.Fatalf("the dry run plist carries no PATH entry:\n%s", stdout.String())
			}
			want := make([]string, len(tc.want))
			for i, entry := range tc.want {
				if strings.HasPrefix(entry, "/") {
					want[i] = entry
				} else {
					want[i] = filepath.Join(root, entry)
				}
			}
			// A symlinked fixture root canonicalizes to a different
			// spelling of the same directory; compare canonical forms.
			// An unresolvable answer (for example a relative one) is
			// kept as is, so it can never equal a canonical absolute
			// want entry.
			canonical := func(p string) string {
				c, err := filepath.EvalSymlinks(p)
				if err != nil {
					return p
				}
				return c
			}
			plutilLint(t, stdout.String())
			if tc.relative {
				// The service starts from another cwd: with the
				// service PATH alone, both fixture executables must
				// still resolve there.
				elsewhere := filepath.Join(root, "elsewhere space")
				if err := os.MkdirAll(elsewhere, 0700); err != nil {
					t.Fatal(err)
				}
				resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer resolveCancel()
				resolve := exec.CommandContext(resolveCtx, shell, "-c", "command -v soho && command -v herdr")
				resolve.WaitDelay = time.Second
				resolve.Dir = elsewhere
				resolve.Env = []string{
					"HOME=" + home,
					"TMPDIR=" + tmp,
					"PATH=" + servicePath,
				}
				out, err := resolve.CombinedOutput()
				if err != nil {
					t.Fatalf("the fixture executables do not resolve from another cwd with the service PATH: %v\n%s", err, out)
				}
				wantResolved := []string{
					filepath.Join(root, tc.sohoDir, "soho"),
					filepath.Join(root, tc.herdrDir, "herdr"),
				}
				gotResolved := strings.Split(strings.TrimSpace(string(out)), "\n")
				if len(gotResolved) != len(wantResolved) {
					t.Fatalf("the service PATH resolved %d paths from another cwd, want %d:\n%s", len(gotResolved), len(wantResolved), out)
				}
				for i := range gotResolved {
					if canonical(gotResolved[i]) != canonical(wantResolved[i]) {
						t.Fatalf("the service PATH resolved %q from another cwd, want %q", gotResolved[i], wantResolved[i])
					}
				}
			}
			got := strings.Split(servicePath, ":")
			if len(got) != len(want) {
				t.Fatalf("service PATH has %d entries, want %d:\n%s", len(got), len(want), stdout.String())
			}
			for i := range got {
				got[i] = canonical(got[i])
				want[i] = canonical(want[i])
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("service PATH = %q, want %q:\n%s", got, want, stdout.String())
			}
			for _, entry := range got {
				if entry == canonical(extra) {
					t.Fatalf("the service PATH copied the interactive entry %q", extra)
				}
			}
			herdrWarning := "nau install: warning: herdr not found on the PATH; the agent may report soho_unavailable"
			if gotWarning := strings.Contains(stderr.String(), herdrWarning); gotWarning != tc.herdrWarning {
				t.Fatalf("herdr warning present = %v, want %v:\n%s", gotWarning, tc.herdrWarning, stderr.String())
			}
			sohoWarning := "nau install: warning: soho not found on the PATH"
			if gotWarning := strings.Contains(stderr.String(), sohoWarning); gotWarning != (tc.sohoDir == "") {
				t.Fatalf("soho warning present = %v, want soho missing:\n%s", gotWarning, stderr.String())
			}
			plutilLint(t, stdout.String())
		})
	}
}

func TestInstallPs1Parses(t *testing.T) {
	powershell := powershellAvailable(t)
	// Parse the script with the PowerShell parser and fail when the
	// parser reports any error.
	script := strings.Join([]string{
		"$errors = $null",
		"[void][System.Management.Automation.Language.Parser]::ParseFile((Join-Path (Get-Location).Path 'install.ps1'), [ref]$null, [ref]$errors)",
		"if ($errors.Count -gt 0) {",
		"    foreach ($e in $errors) { Write-Output $e }",
		"    exit 1",
		"}",
	}, "\n")
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%s parse install.ps1: %v\n%s", powershell, err, out)
	}
	// The Task Scheduler fixtures every installer test runs first must
	// parse too: a broken preamble would fail those tests before they
	// reach the installer. The preamble travels in an environment
	// variable, so no quoting of it is needed in the command.
	parsePreamble := strings.Join([]string{
		"$errors = $null",
		"[void][System.Management.Automation.Language.Parser]::ParseInput($env:NAU_TEST_PS_PREAMBLE, [ref]$null, [ref]$errors)",
		"if ($errors.Count -gt 0) {",
		"    foreach ($e in $errors) { Write-Output $e }",
		"    exit 1",
		"}",
	}, "\n")
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", parsePreamble)
	cmd.Env = append(os.Environ(), "NAU_TEST_PS_PREAMBLE="+psTaskSchedulerPreamble)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s parse the Task Scheduler fixtures: %v\n%s", powershell, err, out)
	}
}

func TestInstallPs1Help(t *testing.T) {
	powershell := powershellAvailable(t)
	script, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	// The fixtures are defined even for -Help, which exits before the
	// install: no installer run from these tests can reach the real
	// Task Scheduler.
	command := psTaskSchedulerFixtures("& " + psQuote(script) + " -Help")
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
	if err != nil {
		t.Fatalf("%s install.ps1 -Help: %v\n%s", powershell, err, out)
	}
	text := string(out)
	for _, opt := range []string{"-Ref", "-BinDir", "-MachineLabel", "-Agent", "-RemoveAgent", "-AgentDryRun", "-Help"} {
		if !strings.Contains(text, opt) {
			t.Errorf("install.ps1 -Help does not list %q:\n%s", opt, text)
		}
	}
}

// TestInstallPs1AgentRestartOnRerun: a re-run without -Agent restarts
// the agent task when it is registered (so the machine never runs the
// old binary) and waits for its fresh health file, without re-registering
// the task or rewriting the wrapper; without the task it installs the
// binary and calls no service cmdlet other than the query. The Task
// Scheduler cmdlets are fixtures in the same -Command as the installer
// run: every call is traced, and a start writes the fresh health file
// after a short sleep, so the mtime test of the health check is
// deterministic at second granularity.
func TestInstallPs1AgentRestartOnRerun(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell installer runtime requires Windows; exercised by Windows CI")
	}
	powershell := powershellAvailable(t)
	buildDir := t.TempDir()
	nau := filepath.Join(buildDir, "nau.exe")
	helper := filepath.Join(buildDir, "helper.exe")
	installerBuild(t, nau, "../cmd/nau")
	installerBuild(t, helper, "./testdata/installhelper")
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "fixture space")
	home := filepath.Join(root, "home space")
	app := filepath.Join(root, "config space")
	local := filepath.Join(root, "local space")
	goDir := filepath.Join(root, "go space")
	backendDir := filepath.Join(root, "backend space")
	tmp := filepath.Join(root, "temp space")
	bin := filepath.Join(root, "bin space")
	for _, dir := range []string{home, app, local, goDir, backendDir, tmp, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.exe"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, "soho.exe"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(tmp, "tasks.trace")
	present := filepath.Join(tmp, "task-present")
	stateFile := filepath.Join(app, "nau", "agent-state.json")
	wrapper := filepath.Join(local, "nau", "agent.cmd")
	command := psTaskSchedulerFixtures("& " + psQuote(script) + " -BinDir " + psQuote(bin))
	system := filepath.Join(os.Getenv("SystemRoot"), "System32")
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"APPDATA=" + app,
		"LOCALAPPDATA=" + local,
		"TMPDIR=" + tmp,
		"TEMP=" + tmp,
		"TMP=" + tmp,
		"PATH=" + strings.Join([]string{goDir, backendDir, system, filepath.Join(system, "WindowsPowerShell", "v1.0")}, string(os.PathListSeparator)),
		"INSTALL_FIXTURE_NAU=" + nau,
		"NAU_TASKS_TRACE=" + trace,
		"NAU_TASKS_PRESENT=" + present,
		"NAU_TASKS_STATE=" + stateFile,
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"WINDIR=" + os.Getenv("WINDIR"),
		"PATHEXT=.COM;.EXE;.BAT;.CMD",
	}
	run := func() string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), installerRunBound)
		defer cancel()
		cmd := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command", command)
		cmd.WaitDelay = time.Second
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		out := stdout.String() + stderr.String()
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("installer did not finish within %s: %s", installerRunBound, out)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("installer failed: %v\n%s", err, out)
			}
			if exit.ExitCode() != 0 {
				t.Fatalf("installer exit=%d: %s", exit.ExitCode(), out)
			}
		}
		return out
	}
	// With the task present: the re-run without the flag stops and
	// starts the task over the fresh binary and waits for its fresh
	// health file, without re-registering the task or rewriting the
	// wrapper.
	wrapperSentinel := []byte("@echo off\nREM sentinel: the no-flag re-run must not rewrite the wrapper\n")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, wrapperSentinel, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(present, []byte("present\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := run()
	lines := psTraceLines(t, trace)
	if len(lines) != 3 || lines[0] != "get nau agent" || lines[1] != "stop nau agent" || lines[2] != "start nau agent" {
		t.Fatalf("the no-flag re-run must query, stop and start the task and nothing else:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(out, "nau install: the agent service started; run nau doctor to check delivery") {
		t.Fatalf("the fresh health wait did not report the service started:\n%s", out)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("the fresh health file did not appear after the restart: %v", err)
	}
	after, err := os.ReadFile(wrapper)
	if err != nil || !bytes.Equal(after, wrapperSentinel) {
		t.Fatalf("the restart rewrote the wrapper: %v\n%s", err, after)
	}
	if _, err := os.Stat(filepath.Join(bin, "nau.exe")); err != nil {
		t.Fatalf("the install did not complete: %v", err)
	}
	// Without the task: the re-run installs the binary and calls no
	// service cmdlet other than the query, creating nothing.
	if err := os.Remove(present); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(wrapper)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(trace); err != nil {
		t.Fatal(err)
	}
	out = run()
	lines = psTraceLines(t, trace)
	if len(lines) != 1 || lines[0] != "get nau agent" {
		t.Fatalf("a service cmdlet ran without an installed task:\n%s", strings.Join(lines, "\n"))
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("the re-run without the flag wrote the health file: %v", err)
	}
	if _, err := os.Stat(wrapper); !os.IsNotExist(err) {
		t.Fatalf("the re-run without the flag created the wrapper: %v", err)
	}
	if !strings.Contains(out, "nau install: installed") {
		t.Fatalf("the install did not complete:\n%s", out)
	}
}
