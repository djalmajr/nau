package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// docReadmePath is the repository README relative to this package's working
// directory.
const docReadmePath = "../../README.md"

// docLoadReadme reads the repository README and normalizes CRLF to LF.
func docLoadReadme(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(docReadmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// docFirstLine returns the first line of s without its newline.
func docFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestDocsHelpIsPlainText keeps the README output contract for the
// plain-text exceptions: `help` and running `nau` with no arguments print
// the usage on stdout and exit 0 instead of a JSON line.
func TestDocsHelpIsPlainText(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "help", args: []string{"help"}},
		{name: "noargs", args: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, exit := runCLI(t, t.TempDir(), false, tc.args...)
			if exit != 0 {
				t.Fatalf("exit = %d, want 0", exit)
			}
			wantUsage := usageText(Env{})
			if stdout != wantUsage {
				t.Fatalf("stdout does not equal the usage text byte for byte (len %d, want %d)", len(stdout), len(wantUsage))
			}
			if strings.Count(stdout, "\n") < 2 {
				t.Fatalf("stdout has %d newlines, want more than one line", strings.Count(stdout, "\n"))
			}
			if json.Valid([]byte(docFirstLine(stdout))) {
				t.Fatalf("first line %q is valid JSON, want plain text", docFirstLine(stdout))
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
		})
	}
}

// TestDocsHelpBadUsageKeepsJSON keeps the README output contract for bad
// usage: the usage text goes to stderr and stdout still carries exactly one
// JSON error line with status 2.
func TestDocsHelpBadUsageKeepsJSON(t *testing.T) {
	stdout, stderr, exit := runCLI(t, t.TempDir(), false, "help", "extra")
	if exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stdout does not end with a newline: %q", stdout)
	}
	body := strings.TrimSuffix(stdout, "\n")
	if strings.Count(body, "\n") != 0 {
		t.Fatalf("stdout has more than one line: %q", stdout)
	}
	var line map[string]string
	if err := json.Unmarshal([]byte(body), &line); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if line["status"] != "2" {
		t.Fatalf("status = %q, want %q", line["status"], "2")
	}
	if !strings.Contains(stderr, usageText(Env{})) {
		t.Fatalf("stderr does not contain the usage text")
	}
}

// TestDocsSingleJSONLineCommands keeps the README output contract for the
// default case: version, config get and config list print exactly one valid
// JSON line on stdout.
func TestDocsSingleJSONLineCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "version", args: []string{"version"}},
		{name: "config get", args: []string{"config", "get", "machine_label"}},
		{name: "config list", args: []string{"config", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, exit := runCLI(t, t.TempDir(), false, tc.args...)
			if exit != 0 {
				t.Fatalf("exit = %d, want 0", exit)
			}
			if !strings.HasSuffix(stdout, "\n") {
				t.Fatalf("stdout does not end with exactly one newline: %q", stdout)
			}
			body := strings.TrimSuffix(stdout, "\n")
			if strings.Count(body, "\n") != 0 {
				t.Fatalf("stdout has more than one line: %q", stdout)
			}
			if !json.Valid([]byte(body)) {
				t.Fatalf("stdout is not valid JSON: %q", body)
			}
		})
	}
}

// TestDocsReadmeOutputContract keeps the README aligned with the real
// output contract: it leads with the product phrase, states the under
// construction status with the soho inventory as the only source, and
// carries no bridge wording.
func TestDocsReadmeOutputContract(t *testing.T) {
	readme := docLoadReadme(t)
	for _, want := range []string{
		"Nau is a read-only hub of the coding agents at work: which agents are working, on which machines, for which developers.",
		"The hub is under construction; the soho inventory is the only source.",
		"The exit codes are `0`, `2`, `4`.",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not contain %q", want)
		}
	}
	for _, gone := range []string{
		"dispatcher", "outbox", "bridge", "wake", "capabilities", "auth login",
	} {
		if i := strings.Index(strings.ToLower(readme), gone); i >= 0 {
			t.Errorf("README still carries the bridge wording %q", gone)
		}
	}
}
