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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		// linuxEmptyHome runs with HOME empty (Linux only); noXDG also drops XDG_CONFIG_HOME.
		linuxEmptyHome, noXDG bool
		relativeCurrent       bool
		unsafeKind            string
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
		// os.UserConfigDir accepts an absolute XDG_CONFIG_HOME without HOME on Linux.
		{name: "XDG without HOME", missing: true, backend: "soho", linuxEmptyHome: true},
		{name: "no config dir", missing: true, backend: "soho", linuxEmptyHome: true, noXDG: true},
		{name: "newline path refused", config: "# keep\nmachine_label=original\nherdr_soho_bin=\n", backend: "soho", unsafeKind: "newline"},
		{name: "writable backend refused", config: "# keep\nmachine_label=original\nherdr_soho_bin=\n", backend: "soho", unsafeKind: "writable"},
		{name: "relative PATH preserves absolute discovery", missing: true, backend: "soho", relativeCurrent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if windows && (tc.unsafeKind != "" || tc.relativeCurrent) {
				t.Skip("POSIX path and mode fixtures; Windows ACL cases are in config tests")
			}
			if tc.linuxEmptyHome && (windows || runtime.GOOS != "linux") {
				t.Skip("an empty HOME only has a defined config directory on Linux")
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
			for _, dir := range []string{home, app, local, goDir, backendDir, tmp} {
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
			cfgRoot := app
			if runtime.GOOS == "darwin" {
				cfgRoot = filepath.Join(home, "Library", "Application Support")
			}
			cfgDir := filepath.Join(cfgRoot, "nau")
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
			if tc.linuxEmptyHome {
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
				env = append(env, "SystemRoot="+os.Getenv("SystemRoot"), "WINDIR="+os.Getenv("WINDIR"), "PATHEXT=.COM;.EXE;.BAT;.CMD")
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
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
					script, err := filepath.Abs("install.ps1")
					if err != nil {
						t.Fatal(err)
					}
					args = []string{"-NoProfile", "-NonInteractive", "-Command", "& " + quote(script) + " -BinDir " + quote(bin)}
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
	hubFile := filepath.Join(home, "Library", "Application Support", "nau", "hub.json")
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
}

func TestInstallPs1Help(t *testing.T) {
	powershell := powershellAvailable(t)
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-File", "install.ps1", "-Help").CombinedOutput()
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
