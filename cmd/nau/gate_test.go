// Process-level contract for `nau gate`, the forced command of the dispatch
// ssh key: the real binary (built once by TestMain in main_test.go) gets the
// text of SSH_ORIGINAL_COMMAND in its environment and 200 KiB on a real
// stdin pipe, and the bytes and exit code of the fake soho must come back
// unchanged; a refusal must come back as exit 2 without running anything.
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// gateProcess runs `nau gate` with the given SSH_ORIGINAL_COMMAND (unset
// when unset is true) and stdin, against a fake soho scripted with rules.
// It returns stdout, stderr, the exit code and the fake's calls.
func gateProcess(t *testing.T, command string, unset bool, stdin []byte, rules ...fakesoho.Rule) ([]byte, []byte, int, []fakesoho.Call) {
	t.Helper()
	root := t.TempDir()
	fakeDir := filepath.Join(root, "fake")
	exe := fakesoho.Install(t, fakeDir, rules...)
	cfgDir := cfgDirFor(root)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte("herdr_soho_bin="+exe+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	env := []string{
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg"),
		"APPDATA=" + filepath.Join(root, "appdata"),
	}
	if !unset {
		env = append(env, "SSH_ORIGINAL_COMMAND="+command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, nauBin, "gate")
	cmd.Env = env
	cmd.WaitDelay = graceWaitDelay
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run nau gate: %v", err)
		}
		exit = ee.ExitCode()
	}
	calls, err := fakesoho.ReadCalls(fakeDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadCalls: %v", err)
	}
	return stdout.Bytes(), stderr.Bytes(), exit, calls
}

// gateBody is n bytes of valid UTF-8 with bytes of 0x80 and above, CRLF
// line ends and no NUL, so the fake's JSON call log keeps it exactly.
func gateBody(n int) []byte {
	unit := []byte("σ\r\nçã\n")
	var b []byte
	for len(b) < n {
		b = append(b, unit...)
	}
	// Cut on a character boundary: drop a split character whole.
	b = b[:n]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	for len(b) < n {
		b = append(b, '.')
	}
	return b
}

// TestProcessGatePassthrough: through the real binary, a forced command
// returns the fake soho's stdout, stderr and exit code byte for byte and
// hands it the exact argv and the whole 200 KiB stdin.
func TestProcessGatePassthrough(t *testing.T) {
	body := gateBody(200 * 1024)
	// Line ends and high bytes the channel must not translate.
	wantOut := []byte("{\"id\":\"J1\"}\r\nσ \xff\x80 end\n")
	wantErr := []byte("warn\r\n\xc3\xa7\x00\n")
	for _, tc := range []struct {
		name    string
		command string
		argv    []string
	}{
		{"start", "nau job start --id J1 --repo org/repo --brief -", []string{"job", "start", "--id", "J1", "--repo", "org/repo", "--brief", "-"}},
		{"status", "nau job status --id J1", []string{"job", "status", "--id", "J1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit, calls := gateProcess(t, tc.command, false, body, fakesoho.Rule{
				Argv: tc.argv, StdoutBytes: wantOut, StderrBytes: wantErr, Code: 19,
			})
			if exit != 19 {
				t.Errorf("exit = %d, want 19 (stdout %q stderr %q)", exit, stdout, stderr)
			}
			if !bytes.Equal(stdout, wantOut) {
				t.Errorf("stdout = %q, want %q", stdout, wantOut)
			}
			if !bytes.Equal(stderr, wantErr) {
				t.Errorf("stderr = %q, want %q", stderr, wantErr)
			}
			if len(calls) != 1 {
				t.Fatalf("fake calls = %d, want 1", len(calls))
			}
			if !reflect.DeepEqual(calls[0].Argv, tc.argv) {
				t.Errorf("fake argv = %q, want %q", calls[0].Argv, tc.argv)
			}
			if calls[0].Stdin != string(body) {
				t.Errorf("fake stdin = %d bytes, want %d identical bytes", len(calls[0].Stdin), len(body))
			}
		})
	}
}

// TestProcessGateRefusal: through the real binary a refusal exits 2 with
// the fixed stdout and stderr lines, never runs the fake soho and does not
// repeat the command.
func TestProcessGateRefusal(t *testing.T) {
	body := gateBody(200 * 1024)
	for _, tc := range []struct {
		name    string
		command string
		unset   bool
		class   string
	}{
		{"unset", "", true, "empty"},
		{"route", "nau route", false, "command"},
		{"metacharacter", "nau job list; id", false, "bad-char"},
		{"internal", "nau job note --id J1", false, "subcommand"},
		{"brief file", "nau job start --id J1 --repo org/repo --brief brief.md", false, "value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit, calls := gateProcess(t, tc.command, tc.unset, body,
				fakesoho.Rule{AnyArgs: true, Stdout: "must not run\n"})
			if exit != 2 {
				t.Errorf("exit = %d, want 2", exit)
			}
			if want := `{"status":"2","motivo":"gate: ` + tc.class + `"}` + "\n"; string(stdout) != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if want := "nau: gate: " + tc.class + "\n"; string(stderr) != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if len(calls) != 0 {
				t.Errorf("the fake soho ran: %q", calls)
			}
			if tc.command != "" && strings.Contains(string(stdout)+string(stderr), tc.command) {
				t.Errorf("the output repeats the command")
			}
		})
	}
}
