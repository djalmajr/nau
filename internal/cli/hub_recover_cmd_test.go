package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The recovery inputs of the recover tests are sentinel values: they
// must never appear in the output of the command, on any path.
const (
	recoverTestToken = "synthetic-root-token-0000000000000000"
	recoverTestPass  = "synthetic-new-pass-000000"
)

// fakeRecoverRT is the recording round tripper of the recover tests:
// it records the request and answers the configured status and body
// without sockets.
type fakeRecoverRT struct {
	status   int
	body     string
	calls    int
	lastURL  string
	lastBody []byte
}

func (f *fakeRecoverRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	f.lastURL = req.URL.String()
	b, _ := io.ReadAll(req.Body)
	f.lastBody = b
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

// withRecoverRoundTrip runs the test body with the given round
// tripper installed and restores the previous one.
func withRecoverRoundTrip(t *testing.T, rt http.RoundTripper, body func()) {
	t.Helper()
	old := recoverRoundTrip
	recoverRoundTrip = rt
	defer func() { recoverRoundTrip = old }()
	body()
}

// runRecover runs `hub recover` with the given stdin and arguments and
// returns stdout, stderr and the exit code.
func runRecover(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin:     strings.NewReader(stdin),
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    func(string) string { return "" },
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: t.TempDir(),
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run(append([]string{"hub", "recover"}, args...), env)
	return out.String(), errb.String(), exit
}

// assertNoLeak requires that the sentinel token and password do not
// appear in any of the given output strings.
func assertNoLeak(t *testing.T, outs ...string) {
	t.Helper()
	for _, s := range outs {
		if strings.Contains(s, recoverTestToken) || strings.Contains(s, recoverTestPass) {
			t.Fatalf("a sentinel value leaked into the output: %q", s)
		}
	}
}

// TestHubRecoverSuccess runs the recovery against the fake hub that
// confirms with the owner e-mail: the exit is 0, the stdout line is
// the confirmation with the e-mail, the request goes to the recovery
// route with the token and the new password, and the sentinels never
// appear in the output.
func TestHubRecoverSuccess(t *testing.T) {
	rt := &fakeRecoverRT{
		status: 200,
		body:   `{"user":{"id":"u1","name":"Ana Exemplo","email":"ana@example.test","role":"owner"}}`,
	}
	withRecoverRoundTrip(t, rt, func() {
		stdout, stderr, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "http://127.0.0.1:7440")
		if exit != 0 {
			t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		var line struct {
			Status string `json:"status"`
			Email  string `json:"email"`
		}
		if err := json.Unmarshal([]byte(stdout), &line); err != nil || line.Status != "recovered" || line.Email != "ana@example.test" {
			t.Fatalf("stdout = %q, want the confirmation with the owner e-mail", stdout)
		}
		if rt.lastURL != "http://127.0.0.1:7440/api/session/root-token" {
			t.Fatalf("the request url = %q", rt.lastURL)
		}
		var sent struct {
			Token string `json:"token"`
			New   string `json:"new"`
		}
		if err := json.Unmarshal(rt.lastBody, &sent); err != nil || sent.Token != recoverTestToken || sent.New != recoverTestPass {
			t.Fatalf("the sent body = %q, want the token and the new password", rt.lastBody)
		}
		assertNoLeak(t, stdout, stderr)
	})
}

// TestHubRecoverRefusals maps each refusal of the hub to its own
// message and the exit 4, without the sentinels in the output.
func TestHubRecoverRefusals(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStderr string
	}{
		{"not found", 404, `{"error":"not_found"}`, "recovery port"},
		{"not found", 404, `{"error":"not_found"}`, "NAU_HUB_ROOT_TOKEN"},
		{"wrong token", 401, `{"error":"invalid_token"}`, "the root token is not valid"},
		{"weak password", 400, `{"error":"weak_password"}`, "the new password must be 12 to 128 bytes"},
		{"server error", 500, `{"error":"unavailable"}`, "the hub refused the recovery"},
		{"a 3xx is not followed", 301, ``, "the hub refused the recovery"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.wantStderr, func(t *testing.T) {
			rt := &fakeRecoverRT{status: tc.status, body: tc.body}
			withRecoverRoundTrip(t, rt, func() {
				stdout, stderr, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "http://127.0.0.1:7440")
				if exit != 4 {
					t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
				}
				if !strings.Contains(stderr, tc.wantStderr) {
					t.Fatalf("stderr = %q, want %q", stderr, tc.wantStderr)
				}
				if tc.status == 301 && rt.calls != 1 {
					t.Fatalf("the redirect was followed: %d calls", rt.calls)
				}
				assertNoLeak(t, stdout, stderr)
			})
		})
	}
}

// TestHubRecoverUnreachableRefusesWith4 runs the real client against a
// loopback port with no listener: the recovery exits 4 with the
// unreachable message and no sentinel in the output.
func TestHubRecoverUnreachableRefusesWith4(t *testing.T) {
	withRecoverRoundTrip(t, nil, func() {
		stdout, stderr, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "http://127.0.0.1:1")
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "the hub is unreachable") {
			t.Fatalf("stderr = %q", stderr)
		}
		assertNoLeak(t, stdout, stderr)
	})
}

// TestHubRecoverBadSuccessBody exits 4 when the hub answers 200 with a
// body the command does not trust: no confirmation is printed.
func TestHubRecoverBadSuccessBody(t *testing.T) {
	for name, body := range map[string]string{
		"not json":  `not json`,
		"no e-mail": `{"user":{"id":"u1"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := &fakeRecoverRT{status: 200, body: body}
			withRecoverRoundTrip(t, rt, func() {
				stdout, stderr, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "http://127.0.0.1:7440")
				if exit != 4 {
					t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
				}
				if !strings.Contains(stderr, "did not confirm the recovery") {
					t.Fatalf("stderr = %q", stderr)
				}
				assertNoLeak(t, stdout, stderr)
			})
		})
	}
}

// TestHubRecoverNonLoopbackURLIsRefused refuses a url that is not the
// loopback of the recovery port: the exit is 2 before any request and
// before the stdin is read, and the stderr says the port-forward must
// point at the recovery port.
func TestHubRecoverNonLoopbackURLIsRefused(t *testing.T) {
	stdin := recoverTestToken + "\n" + recoverTestPass + "\n"
	for _, url := range []string{
		"http://192.0.2.1:7441",
		"https://hub.example.test",
		"http://localhost:7441",
		"http://[fd00::1]:7441",
		"http://0.0.0.0:7441",
	} {
		t.Run(url, func(t *testing.T) {
			rt := &fakeRecoverRT{status: 200, body: `{}`}
			in := &countingStdin{r: strings.NewReader(stdin)}
			var out, errb bytes.Buffer
			env := Env{
				Stdin:     in,
				Stdout:    &out,
				Stderr:    &errb,
				Getenv:    func(string) string { return "" },
				Environ:   func() []string { return fakeChildEnv },
				ConfigDir: t.TempDir(),
				Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
				Sleep:     sleepCtx,
			}
			exit := Run(append([]string{"hub", "recover"}, "--url", url), env)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, out.String(), errb.String())
			}
			if !strings.Contains(errb.String(), "recovery port") {
				t.Fatalf("stderr = %q, want the recovery port in the message", errb.String())
			}
			if rt.calls != 0 {
				t.Fatalf("the request ran: %d calls, want 0", rt.calls)
			}
			if in.reads != 0 {
				t.Fatalf("the stdin was read: %d reads, want 0", in.reads)
			}
			assertNoLeak(t, out.String(), errb.String())
		})
	}
}

// countingStdin is the stdin of the recover tests that counts the
// reads: the refusal of a bad url must not consume the secrets.
type countingStdin struct {
	r     *strings.Reader
	reads int
}

func (c *countingStdin) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// TestHubRecoverLoopbackURLsWork runs the recovery against the loopback
// of the recovery port in the two spellings, over the fake hub: the
// request reaches the recovery route of the given url.
func TestHubRecoverLoopbackURLsWork(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:7441", "http://[::1]:7441"} {
		t.Run(u, func(t *testing.T) {
			rt := &fakeRecoverRT{status: 200, body: `{"user":{"email":"ana@example.test"}}`}
			withRecoverRoundTrip(t, rt, func() {
				stdout, stderr, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", u)
				if exit != 0 {
					t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", exit, stdout, stderr)
				}
				if rt.lastURL != u+"/api/session/root-token" {
					t.Fatalf("the request url = %q, want %q", rt.lastURL, u)
				}
				assertNoLeak(t, stdout, stderr)
			})
		})
	}
}

// TestHubRecoverBadUsage exits 2 on the malformed calls: the url
// missing or malformed, the remote or named host, the extra argument
// and the repeated flag.
func TestHubRecoverBadUsage(t *testing.T) {
	stdin := recoverTestToken + "\n" + recoverTestPass + "\n"
	for name, args := range map[string][]string{
		"missing --url":  {},
		"no value":       {"--url"},
		"repeated --url": {"--url", "http://127.0.0.1:7440", "--url", "http://127.0.0.1:7440"},
		"extra argument": {"--url", "http://127.0.0.1:7440", "extra"},
		"bad scheme":     {"--url", "ftp://127.0.0.1:7440"},
		"no host":        {"--url", "http://"},
		"not a url":      {"--url", "not a url"},
		"remote host":    {"--url", "http://hub.example.test:7441"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, exit := runRecover(t, stdin, args...)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
			}
			var line map[string]string
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil || line["status"] != "2" {
				t.Fatalf("stdout = %q, want one JSON line with status 2", stdout)
			}
			assertNoLeak(t, stdout, stderr)
		})
	}
}

// TestHubRecoverMissingOrEmptyLines exits 2 when a line of stdin is
// missing or empty, before the request.
func TestHubRecoverMissingOrEmptyLines(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
	}{
		{"empty stdin", ""},
		{"empty first line", "\n" + recoverTestPass + "\n"},
		{"missing second line", recoverTestToken + "\n"},
		{"empty second line", recoverTestToken + "\n\n"},
		{"only a newline second line", recoverTestToken + "\n\nextra\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRecoverRT{status: 200, body: `{}`}
			withRecoverRoundTrip(t, rt, func() {
				stdout, stderr, exit := runRecover(t, tc.stdin, "--url", "http://127.0.0.1:7440")
				if exit != 2 {
					t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
				}
				if rt.calls != 0 {
					t.Fatalf("the request ran: %d calls, want 0", rt.calls)
				}
				assertNoLeak(t, stdout, stderr)
			})
		})
	}
}

// TestHubRecoverCRLFEndings removes only the end of line: a CRLF input
// reaches the hub without the carriage return.
func TestHubRecoverCRLFEndings(t *testing.T) {
	rt := &fakeRecoverRT{status: 200, body: `{"user":{"email":"ana@example.test"}}`}
	withRecoverRoundTrip(t, rt, func() {
		_, _, exit := runRecover(t, recoverTestToken+"\r\n"+recoverTestPass+"\r\n", "--url", "http://127.0.0.1:7440")
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		var sent struct {
			Token string `json:"token"`
			New   string `json:"new"`
		}
		if err := json.Unmarshal(rt.lastBody, &sent); err != nil {
			t.Fatalf("the sent body %q: %v", rt.lastBody, err)
		}
		if sent.Token != recoverTestToken || sent.New != recoverTestPass {
			t.Fatalf("the end of line leaked into the values: %q %q", sent.Token, sent.New)
		}
	})
}

// TestHubRecoverTerminalStdinIsRefused refuses the terminal stdin with
// the exit 2, because the typed text would appear on the screen. The
// terminal is the character device /dev/null; without it (windows) the
// test skips.
func TestHubRecoverTerminalStdinIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/null character device on windows")
	}
	f, err := os.OpenFile("/dev/null", os.O_RDONLY, 0)
	if err != nil {
		t.Skipf("cannot open the character device: %v", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		t.Skipf("/dev/null is not a character device here: mode=%v err=%v", fi.Mode(), err)
	}
	var out, errb bytes.Buffer
	env := Env{
		Stdin:     f,
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    func(string) string { return "" },
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: t.TempDir(),
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run([]string{"hub", "recover", "--url", "http://127.0.0.1:7440"}, env)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "not the terminal") {
		t.Fatalf("stderr = %q", errb.String())
	}
	assertNoLeak(t, out.String(), errb.String())
}

// Mutation captured: mapping every 401 to invalid_token conceals a lost race.
func TestHubRecoverUnauthorizedCodes(t *testing.T) {
	for _, tc := range []struct{ name, body, message string }{
		{"invalid token", `{"error":"invalid_token"}`, "hub recover: the root token is not valid"},
		{"lost race", `{"error":"unauthenticated"}`, "hub recover: the password changed during the recovery; run it again"},
		{"unknown", `{"error":"other"}`, "hub recover: the hub refused the recovery"},
		{"empty", "", "hub recover: the hub refused the recovery"},
		{"invalid json", "{", "hub recover: the hub refused the recovery"},
		{"wrong type", `{"error":42}`, "hub recover: the hub refused the recovery"},
		{"case", `{"error":"INVALID_TOKEN"}`, "hub recover: the hub refused the recovery"},
		{"hostile", `{"error":"` + recoverTestToken + recoverTestPass + `"}`, "hub recover: the hub refused the recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRecoverRoundTrip(t, &fakeRecoverRT{status: 401, body: tc.body}, func() {
				out, errb, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "http://127.0.0.1:7441")
				var line struct{ Status, Motivo string }
				if err := json.Unmarshal([]byte(out), &line); err != nil || exit != 4 || line.Status != "4" || line.Motivo != tc.message || errb != "nau: "+tc.message+"\n" {
					t.Fatalf("exit=%d stdout=%q stderr=%q, want %q", exit, out, errb, tc.message)
				}
				assertNoLeak(t, out, errb)
			})
		})
	}
}

func TestHubRecoverURLMessage(t *testing.T) {
	const want = "hub recover: the url must be the loopback recovery port of the hub, such as http://127.0.0.1:7441: point the port-forward at the recovery port and define NAU_HUB_ROOT_TOKEN on the hub"
	out, errb, exit := runRecover(t, recoverTestToken+"\n"+recoverTestPass+"\n", "--url", "https://example.test")
	var line struct{ Status, Motivo string }
	if err := json.Unmarshal([]byte(out), &line); err != nil || exit != 2 || line.Status != "2" || line.Motivo != want || errb != "nau: "+want+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out, errb)
	}
	assertNoLeak(t, out, errb)
}
