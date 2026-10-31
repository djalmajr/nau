package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/agent"
	"github.com/djalmajr/nau/internal/config"
	"github.com/djalmajr/nau/internal/testutil/fakeinventory"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// The agent command fixtures: a machine key that must never reach an
// output surface, and the machine id the hub.json carries.
const (
	agentTestKey       = "sekret-agent-key-1"
	agentTestMachineID = "machine-m1"
)

// syncBuf is a string builder safe for a writer goroutine running the
// command in parallel with the test, with a poll helper.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls until the buffer contains want or the deadline elapses.
func (s *syncBuf) waitFor(t *testing.T, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(s.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited %s for %q in: %s", d, want, s.String())
}

// postLine is one recorded ingest post: path, auth header and body.
type postLine struct {
	Path string
	Auth string
	Body string
}

// newFakeHub returns a loopback hub that answers every ingest post
// with the given status and records one line per post.
func newFakeHub(t *testing.T, status int) (*httptest.Server, *syncBuf) {
	t.Helper()
	posts := &syncBuf{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		// Through the locked Write: the handler runs on the server's
		// own goroutine, in parallel with the test's reads.
		_, _ = posts.Write([]byte(r.URL.Path + "\t" + r.Header.Get("Authorization") + "\t" + string(body) + "\n"))
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, posts
}

// fakeSohoRules is the fake soho of the agent command tests: the
// capabilities line advertises inventory and the inventory command
// prints the valid fixture.
func fakeSohoRules() []fakesoho.Rule {
	return fakeinventory.Rules(filepath.Join("..", "agent", "testdata", "valid.json"))
}

// writeHubFile writes the test hub.json into cfgDir.
func writeHubFile(t *testing.T, cfgDir, url, machineID, key string) {
	t.Helper()
	data := `{"url":` + jsonString(url) + `,"machine_id":` + jsonString(machineID) + `,"key":` + jsonString(key) + `}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "hub.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// agentTestEnv builds the Env of an agent command test with the given
// writers.
func agentTestEnv(t *testing.T, cfgDir string, out, errb *syncBuf) Env {
	t.Helper()
	return Env{
		Stdin:     strings.NewReader(""),
		Stdout:    out,
		Stderr:    errb,
		Getenv:    getenvFor(false),
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: cfgDir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
}

// runAgentCommand runs `agent run <args>` in a goroutine with a
// test-canceled run context, and returns the cancel function and a
// join function reporting the exit code.
func runAgentCommand(t *testing.T, env Env, args ...string) (func(), func() int) {
	t.Helper()
	old := agentRunContext
	ctx, cancel := context.WithCancel(context.Background())
	agentRunContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { agentRunContext = old })
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- Run(append([]string{"agent"}, args...), env)
	}()
	return cancel, func() int {
		select {
		case code := <-codeCh:
			return code
		case <-time.After(30 * time.Second):
			t.Fatalf("agent run did not return within 30 s")
			return 0
		}
	}
}

// startLine is the expected start line for the test fixtures.
func startLine(hubURL, machineID string) string {
	return `{"status":"running","hub":` + jsonString(agent.HostOnly(hubURL)) + `,"machine_id":` + jsonString(machineID) + `}` + "\n"
}

// TestAgentRunStartLine: the start line goes out before the loop, with
// the hub reduced to scheme and host and no key anywhere; the first
// read reaches the hub with the key only in the header, and the context
// cancel ends the run with 0.
func TestAgentRunStartLine(t *testing.T) {
	srv, posts := newFakeHub(t, http.StatusNoContent)
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	writeHubFile(t, cfgDir, srv.URL, agentTestMachineID, agentTestKey)
	var out, errb syncBuf
	env := agentTestEnv(t, cfgDir, &out, &errb)
	cancel, join := runAgentCommand(t, env, "run")
	want := startLine(srv.URL, agentTestMachineID)
	out.waitFor(t, want, 10*time.Second)
	if strings.Contains(out.String(), agentTestKey) {
		t.Fatalf("stdout carries the machine key:\n%s", out.String())
	}
	// The first read reaches the hub; the key travels in the header.
	posts.waitFor(t, "/api/ingest", 10*time.Second)
	if !strings.Contains(posts.String(), "Bearer "+agentTestKey) {
		t.Errorf("the post header lacks the bearer key:\n%s", posts.String())
	}
	cancel()
	if exit := join(); exit != 0 {
		t.Fatalf("exit = %d after the cancel, want 0; stdout %q stderr %q", exit, out.String(), errb.String())
	}
	// Nothing else reached stdout: the start line and nothing after.
	if got := out.String(); got != want {
		t.Errorf("stdout after the run = %q, want only the start line", got)
	}
	// The health file exists and is keyless.
	health, err := os.ReadFile(filepath.Join(cfgDir, "agent-state.json"))
	if err != nil {
		t.Fatalf("the health file was not written: %v", err)
	}
	if strings.Contains(string(health), agentTestKey) {
		t.Fatalf("the health file carries the machine key:\n%s", health)
	}
}

// TestAgentRunRevokedKey: a 401 from the hub ends the run with 4 and
// the revoked motivo, and the health file says revoked.
func TestAgentRunRevokedKey(t *testing.T) {
	srv, _ := newFakeHub(t, http.StatusUnauthorized)
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	writeHubFile(t, cfgDir, srv.URL, agentTestMachineID, agentTestKey)
	var out, errb syncBuf
	env := agentTestEnv(t, cfgDir, &out, &errb)
	_, join := runAgentCommand(t, env, "run")
	if exit := join(); exit != 4 {
		t.Fatalf("exit = %d on a revoked key, want 4; stdout %q", exit, out.String())
	}
	if !strings.Contains(out.String(), `"status":"4"`) || !strings.Contains(out.String(), `"motivo":"revoked"`) {
		t.Errorf("stdout lacks the revoked error line: %q", out.String())
	}
	if strings.Contains(out.String(), agentTestKey) {
		t.Fatalf("stdout carries the machine key:\n%s", out.String())
	}
	h, err := agent.ReadHealth(filepath.Join(cfgDir, "agent-state.json"))
	if err != nil {
		t.Fatalf("parse the health file: %v", err)
	}
	if h.State != agent.StateRevoked {
		t.Errorf("health state = %q, want revoked", h.State)
	}
}

// TestAgentRunMissingHubFile: without hub.json (or with an invalid one)
// the run refuses with 2 and points at nau connect, and writes no
// health file.
func TestAgentRunMissingHubFile(t *testing.T) {
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	for _, tc := range []struct {
		name string
		hub  string // "" for no file
	}{
		{name: "no file"},
		{name: "empty key", hub: `{"url":"http://127.0.0.1:17440","machine_id":"m","key":""}`},
		{name: "http off loopback", hub: `{"url":"http://other.example:17440","machine_id":"m","key":"k"}`},
		{name: "bad json", hub: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			setSohoConfig(t, cfgDir, exe, "")
			if tc.hub != "" {
				if err := os.WriteFile(filepath.Join(cfgDir, "hub.json"), []byte(tc.hub+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out, errb syncBuf
			env := agentTestEnv(t, cfgDir, &out, &errb)
			_, join := runAgentCommand(t, env, "run")
			if exit := join(); exit != 2 {
				t.Fatalf("exit = %d, want 2; stdout %q", exit, out.String())
			}
			if !strings.Contains(out.String(), `"status":"2"`) || !strings.Contains(out.String(), "nau connect") {
				t.Errorf("stdout lacks the connect direction: %q", out.String())
			}
			if _, err := os.Stat(filepath.Join(cfgDir, "agent-state.json")); !os.IsNotExist(err) {
				t.Errorf("the refused run wrote the health file: %v", err)
			}
		})
	}
}

// TestAgentRunBadUsage: bad usage exits 2 with the agent usage in the
// stdout line and the program usage on stderr.
func TestAgentRunBadUsage(t *testing.T) {
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"run", "--bogus"},
		{"run", "--interval"},
		{"run", "--interval", "bogus"},
		{"run", "--interval", "4s"},
		{"run", "--interval", "31s"},
		{"run", "--interval", "15s", "--interval", "10s"},
		{"run", "--hc-bin"},
		{"run", "--hc-bin", "x", "--hc-bin", "y"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var out, errb syncBuf
			env := agentTestEnv(t, cfgDir, &out, &errb)
			_, join := runAgentCommand(t, env, args...)
			if exit := join(); exit != 2 {
				t.Fatalf("%v: exit = %d, want 2", args, exit)
			}
			if !strings.Contains(out.String(), "usage: agent run") || !strings.Contains(out.String(), `"status":"2"`) {
				t.Errorf("%v: stdout %q lacks the usage line", args, out.String())
			}
			// The program usage on stderr lists the agent command.
			if !strings.Contains(errb.String(), "agent run") {
				t.Errorf("%v: stderr lacks the agent entry of the program usage", args)
			}
			if !strings.Contains(errb.String(), "usage: nau <command> [args]") {
				t.Errorf("%v: stderr lacks the program usage", args)
			}
		})
	}
}

// TestAgentRunNowrite: under NAU_NOWRITE=1 the run refuses before any
// side effect, like the other writing commands.
func TestAgentRunNowrite(t *testing.T) {
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	writeHubFile(t, cfgDir, "http://127.0.0.1:17440", agentTestMachineID, agentTestKey)
	stdout, _, exit := runCLI(t, cfgDir, true, "agent", "run")
	if exit != 2 {
		t.Fatalf("agent run under NAU_NOWRITE: exit %d, want 2", exit)
	}
	if stdout != "{\"status\":\"nowrite\",\"motivo\":\"NAU_NOWRITE=1\"}\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "agent-state.json")); !os.IsNotExist(err) {
		t.Errorf("agent run under NAU_NOWRITE wrote the health file: %v", err)
	}
}

// TestAgentRunUsesConfigSoho: the backend is the effective
// herdr_soho_bin, like the doctor's resolution: the inventory the hub
// receives came from the configured fake, not from a soho on the PATH
// (the test PATH is empty, so there is no other candidate).
func TestAgentRunUsesConfigSoho(t *testing.T) {
	srv, posts := newFakeHub(t, http.StatusNoContent)
	exe, _ := installFakeSoho(t, fakeSohoRules()...)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	writeHubFile(t, cfgDir, srv.URL, agentTestMachineID, agentTestKey)
	var out, errb syncBuf
	env := agentTestEnv(t, cfgDir, &out, &errb)
	cancel, join := runAgentCommand(t, env, "run", "--interval", "5s")
	out.waitFor(t, `"status":"running"`, 10*time.Second)
	posts.waitFor(t, "/api/ingest", 10*time.Second)
	cancel()
	if exit := join(); exit != 0 {
		t.Fatalf("exit = %d, want 0; stderr %q", exit, errb.String())
	}
	cfg, err := config.Load(cfgDir)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.HerdrSohoBin != exe {
		t.Errorf("effective backend = %q, want the configured fake %q", cfg.HerdrSohoBin, exe)
	}
}
