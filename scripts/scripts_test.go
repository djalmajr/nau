// Package scripts_test checks the install scripts hermetically: the
// POSIX shell syntax, the --help output and the --serve-dry-run output
// of install.sh (a LaunchAgent plist on macOS, refused on Linux), and
// the PowerShell syntax, the -Help output and the -ServeDryRun output
// of install.ps1 (when a PowerShell is available). The checks parse,
// print usage or print the service definition; installation tests use
// fixture executables and temporary state, without network or services.
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
	}{
		{name: "no file", missing: true, backend: "soho"},
		{name: "missing key", config: "# configuration\nmachine_label=node\n", backend: "soho"},
		{name: "empty", config: "herdr_soho_bin=\n", backend: "soho"},
		{name: "legacy auto", config: "herdr_soho_bin=herdr-soho\n", backend: "soho"},
		{name: "absolute explicit", config: "absolute", explicit: true, backend: "soho"},
		{name: "bare explicit", config: "herdr_soho_bin=soho\n", explicit: true, backend: "soho"},
		{name: "relative explicit", config: "herdr_soho_bin=./bin/soho\n", explicit: true, backend: "soho"},
		{name: "idempotent", missing: true, backend: "soho", repeat: true},
		{name: "legacy only", missing: true, backend: "herdr-soho"},
		{name: "no backend no file", missing: true},
		{name: "no backend auto file", config: "herdr_soho_bin=herdr-soho\n"},
		{name: "go failure no file", missing: true, backend: "soho", failGo: true},
		{name: "go failure auto file", config: "herdr_soho_bin=\n", backend: "soho", failGo: true},
		{name: "pin write failure", missing: true, backend: "soho", failSet: true},
		{name: "trim last auto wins", config: " # comment\r\n herdr_soho_bin = soho\r\n\t herdr_soho_bin = herdr-soho \t\r\n", backend: "soho"},
		{name: "trim last explicit wins", config: "herdr_soho_bin=\n herdr_soho_bin = ./bin/soho \t\n", backend: "soho", explicit: true},
		{name: "first equals only", config: "herdr_soho_bin=./bin/soho=custom\n", backend: "soho", explicit: true},
		{name: "Unicode whitespace explicit", config: configWhitespace + "herdr_soho_bin" + configWhitespace + "=" + configWhitespace + "soho" + configWhitespace + "\n", backend: "soho", explicit: true},
		{name: "Unicode whitespace auto", config: "herdr_soho_bin=" + configWhitespace + "herdr-soho" + configWhitespace + "\n", backend: "soho"},
		{name: "Unicode nonspace explicit", config: "herdr_soho_bin=\u200bherdr-soho\u200b\n", backend: "soho", explicit: true},
		// A file nau itself cannot load is left alone and the install still succeeds.
		{name: "unloadable BOM key", config: "\ufeffherdr_soho_bin=soho\n", backend: "soho", unreadable: true},
		{name: "unloadable line", config: "herdr_soho_bin\n", backend: "soho", unreadable: true},
		// os.UserConfigDir accepts an absolute XDG_CONFIG_HOME without HOME on Linux.
		{name: "XDG without HOME", missing: true, backend: "soho", linuxEmptyHome: true},
		{name: "no config dir", missing: true, backend: "soho", linuxEmptyHome: true, noXDG: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.linuxEmptyHome && (windows || runtime.GOOS != "linux") {
				t.Skip("an empty HOME only has a defined config directory on Linux")
			}
			root := filepath.Join(t.TempDir(), "fixture space")
			home := filepath.Join(root, "home space")
			app := filepath.Join(root, "config space")
			local := filepath.Join(root, "local space")
			goDir := filepath.Join(root, "go space")
			backendDir := filepath.Join(root, "backend space")
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
			// Both names exercise the resolver's preference for the current name.
			if tc.backend == "soho" {
				write(filepath.Join(backendDir, "herdr-soho"+suffix), helperBytes)
			}
			cfgRoot := app
			if runtime.GOOS == "darwin" {
				cfgRoot = filepath.Join(home, "Library", "Application Support")
			}
			cfgDir := filepath.Join(cfgRoot, "herdr-hermes")
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
				env = append(env, "HERDR_HERMES_NOWRITE=1")
			}
			run := func(want int) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				args := []string{"install.sh", "--no-plugin", "--bin-dir", bin}
				if windows {
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
					script, err := filepath.Abs("install.ps1")
					if err != nil {
						t.Fatal(err)
					}
					// The host's service registry is outside this fixture.
					args = []string{"-NoProfile", "-NonInteractive", "-Command", "function Get-ScheduledTask { }; & " + quote(script) + " -NoPlugin -BinDir " + quote(bin)}
				}
				cmd := exec.CommandContext(ctx, shell, args...)
				cmd.WaitDelay = time.Second
				cmd.Env = env
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				out := stdout.String() + stderr.String()
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
				if !windows && tc.backend == "" && !strings.Contains(stderr.String(), "nau install: warning: soho not found on the PATH") {
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
			if tc.failGo || tc.failSet || tc.backend == "" || tc.explicit || tc.unreadable || tc.noXDG {
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
				if tc.explicit && !strings.Contains(output, "nau install: herdr_soho_bin is set explicitly; keeping it") {
					t.Fatal("missing explicit-value message")
				}
				if tc.backend == "" && !strings.Contains(output, "nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic, and an ssh forced command (nau gate) may not find the backend") {
					t.Fatal("missing backend warning")
				}
				if tc.noXDG && !strings.Contains(output, "nau install: warning: cannot determine the config directory; herdr_soho_bin unchanged") {
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
				t.Fatal("stored backend is not the discovered absolute path")
			}
			if !strings.Contains(output, "nau install: herdr_soho_bin pinned to the absolute path of the backend found on the PATH") {
				t.Fatal("missing pin message")
			}
			if tc.repeat {
				second := run(0)
				after, err := os.ReadFile(cfgFile)
				if err != nil || !bytes.Equal(data, after) {
					t.Fatal("second install changed config bytes")
				}
				if !strings.Contains(second, "nau install: herdr_soho_bin is set explicitly; keeping it") {
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
	for _, opt := range []string{"--ref", "--bin-dir", "--machine-label", "--no-plugin", "--serve", "--serve-dry-run", "--help"} {
		if !strings.Contains(text, opt) {
			t.Errorf("install.sh --help does not list %q:\n%s", opt, text)
		}
	}
}

// TestInstallShServeDryRun runs --serve-dry-run: on macOS it must exit
// 0 and print a plist that passes plutil -lint (skipped when plutil
// is absent); on Linux it must be refused with exit 2 before any step.
func TestInstallShServeDryRun(t *testing.T) {
	sh := shAvailable(t)
	if runtime.GOOS == "linux" {
		cmd := exec.Command(sh, "install.sh", "--serve-dry-run")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("sh install.sh --serve-dry-run: unexpected error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if exitErr.ExitCode() != 2 {
			t.Fatalf("sh install.sh --serve-dry-run: exit %d, want 2\nstdout:\n%s\nstderr:\n%s", exitErr.ExitCode(), stdout.String(), stderr.String())
		}
		return
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("--serve-dry-run is only defined on macOS (GOOS %s)", runtime.GOOS)
	}
	cmd := exec.Command(sh, "install.sh", "--serve-dry-run")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh install.sh --serve-dry-run: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available: " + err.Error())
	}
	lint := exec.Command(plutil, "-lint", "-")
	lint.Stdin = strings.NewReader(stdout.String())
	lintOut, err := lint.CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -lint - on the dry-run plist: %v\n%s\nplist:\n%s", err, lintOut, stdout.String())
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
	for _, opt := range []string{"-Ref", "-BinDir", "-MachineLabel", "-NoPlugin", "-Serve", "-ServeDryRun", "-Help"} {
		if !strings.Contains(text, opt) {
			t.Errorf("install.ps1 -Help does not list %q:\n%s", opt, text)
		}
	}
}

// TestInstallPs1ServeDryRun runs -ServeDryRun on Windows: it must exit
// 0 and print the action executable and arguments, the triggers and
// the settings (one per line). Skipped off Windows.
func TestInstallPs1ServeDryRun(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("install.ps1 -ServeDryRun is only exercised on windows")
	}
	powershell := powershellAvailable(t)
	out, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-File", "install.ps1", "-ServeDryRun").CombinedOutput()
	if err != nil {
		t.Fatalf("%s install.ps1 -ServeDryRun: %v\n%s", powershell, err, out)
	}
	text := string(out)
	for _, want := range []string{"a2a serve", "conhost.exe", "IgnoreNew"} {
		if !strings.Contains(text, want) {
			t.Errorf("install.ps1 -ServeDryRun output does not contain %q:\n%s", want, text)
		}
	}
}
