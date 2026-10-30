package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePairingHub is the fake hub of the connect tests: it answers the
// pairing request and the poll, and the test confirms the pairing
// between the polls.
type fakePairingHub struct {
	mu          sync.Mutex
	server      *httptest.Server
	confirmed   bool
	delivered   bool
	forced      int // when non-zero, the poll answers with it
	pollEntered chan struct{}
	gotName     string
	key         string
}

const fakePairingCode = "7K4P-9M2N"
const fakePairingKey = "fake-machine-key-for-the-connect-test-000000"

func newFakePairingHub(t *testing.T) *fakePairingHub {
	t.Helper()
	f := &fakePairingHub{
		pollEntered: make(chan struct{}, 1),
		key:         fakePairingKey,
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/pairings":
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.gotName = body.Name
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"code":"` + fakePairingCode + `","poll_token":"polltoken0000000000000000000000000001","expires_ms":` + "9999999999999" + `}`))
		case "POST /api/pairings/poll":
			select {
			case f.pollEntered <- struct{}{}:
			default:
			}
			f.mu.Lock()
			confirmed, delivered, forced := f.confirmed, f.delivered, f.forced
			if confirmed && !delivered {
				f.delivered = true
			}
			f.mu.Unlock()
			switch {
			case forced != 0:
				w.WriteHeader(forced)
				_, _ = w.Write([]byte(`{"error":"expired"}`))
			case !confirmed:
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"pending"}`))
			case delivered:
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not_found"}`))
			default:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"machine_id":"machine-1","key":"` + f.key + `"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// confirm marks the pairing as confirmed (the developer side).
func (f *fakePairingHub) confirm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmed = true
}

// connectTestEnv is the hermetic environment of the connect tests.
func connectTestEnv(t *testing.T, cfgDir string) (Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    func(string) string { return "" },
		ConfigDir: cfgDir,
		Now:       func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		Sleep:     func(ctx context.Context, d time.Duration) error { return ctx.Err() },
	}
	return env, &out, &errb
}

// TestConnectSuccess is the full flow against the fake hub: the
// pairing, the code on stderr, the confirmation, and the key stored
// in hub.json with 0600, without reaching stdout or stderr.
func TestConnectSuccess(t *testing.T) {
	f := newFakePairingHub(t)
	// The config directory of the fresh profile does not exist yet:
	// the command creates it.
	cfgDir := filepath.Join(t.TempDir(), "nested", "cfg")
	env, out, errb := connectTestEnv(t, cfgDir)
	// The confirmation happens between the first and the second
	// poll: the test confirms once the command entered the polling
	// loop.
	go func() {
		<-f.pollEntered
		f.confirm()
	}()
	code := Run([]string{"connect", f.server.URL, "--name", "laptop"}, env)
	if code != 0 {
		t.Fatalf("connect = %d (stderr %s), want 0", code, errb.String())
	}
	want := `{"status":"connected","machine_id":"machine-1"}` + "\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	// The code and the instruction, on stderr.
	if !strings.Contains(errb.String(), "confirme este código no hub, em Máquinas → Conectar máquina: "+fakePairingCode) {
		t.Fatalf("stderr = %q, want the code and the instruction", errb.String())
	}
	// The key never reaches stdout or stderr.
	if strings.Contains(out.String(), fakePairingKey) || strings.Contains(errb.String(), fakePairingKey) {
		t.Fatalf("the key reached the output: stdout %q, stderr %q", out.String(), errb.String())
	}
	// The hub received the name.
	f.mu.Lock()
	if f.gotName != "laptop" {
		t.Fatalf("the name of the pairing = %q, want laptop", f.gotName)
	}
	f.mu.Unlock()
	// hub.json, with 0600 and the credential.
	data, err := os.ReadFile(filepath.Join(cfgDir, "hub.json"))
	if err != nil {
		t.Fatalf("read hub.json: %v", err)
	}
	st, err := os.Stat(filepath.Join(cfgDir, "hub.json"))
	if err != nil {
		t.Fatalf("stat hub.json: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatalf("hub.json mode = %o, want 0600", st.Mode().Perm())
	}
	var file hubJSONFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("hub.json %s: %v", data, err)
	}
	if file.URL != f.server.URL || file.MachineID != "machine-1" || file.Key != fakePairingKey {
		t.Fatalf("hub.json = %+v, want the url, the machine id and the key", file)
	}
	// No temp file left in the config directory.
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatalf("read the config dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "hub.json" {
		t.Fatalf("the config dir holds %v, want only hub.json", entries)
	}
}

// TestConnectExpired is the pairing beyond the validity: the 410 of
// the poll is the exit 3, and nothing is written to disk.
func TestConnectExpired(t *testing.T) {
	f := newFakePairingHub(t)
	f.mu.Lock()
	f.forced = http.StatusGone
	f.mu.Unlock()
	cfgDir := t.TempDir()
	env, out, _ := connectTestEnv(t, cfgDir)
	code := Run([]string{"connect", f.server.URL, "--name", "laptop"}, env)
	if code != 3 {
		t.Fatalf("connect = %d (stdout %s), want 3", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("hub.json was written, want nothing")
	}
	var line struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &line); err != nil {
		t.Fatalf("the stdout %q is not one JSON line", out.String())
	}
	if line.Status != "3" {
		t.Fatalf("the status = %q, want 3", line.Status)
	}
}

// TestConnectHubDown is the unreachable hub: the exit 4 and nothing
// written to disk.
func TestConnectHubDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	cfgDir := t.TempDir()
	env, out, _ := connectTestEnv(t, cfgDir)
	code := Run([]string{"connect", url, "--name", "laptop"}, env)
	if code != 4 {
		t.Fatalf("connect = %d (stdout %s), want 4", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("hub.json was written, want nothing")
	}
}

// TestConnectBadURL is the url validation: https is accepted (the
// loopback connection is refused by the port, not by the usage), http
// is accepted only on the loopback host, and the other schemes are
// usage errors.
func TestConnectBadURL(t *testing.T) {
	f := newFakePairingHub(t)
	cfgDir := t.TempDir()
	cases := []struct {
		url  string
		code int
	}{
		{"http://192.168.0.10:7440", 2}, // not a loopback host
		{"ftp://127.0.0.1:7440", 2},
		{"127.0.0.1:7440", 2}, // no scheme
		{f.server.URL, 0},     // the loopback http of the fake hub
	}
	for i, c := range cases {
		env, _, _ := connectTestEnv(t, cfgDir)
		// The loopback case needs the confirmation to reach 0.
		if c.code == 0 {
			go func() {
				<-f.pollEntered
				f.confirm()
			}()
		}
		code := Run([]string{"connect", c.url, "--name", "laptop"}, env)
		if code != c.code {
			t.Fatalf("case %d (%s): connect = %d, want %d", i, c.url, code, c.code)
		}
	}
	// The https url is accepted by the validation: the connection to
	// the closed loopback port is the hub-down error, not the usage
	// error.
	env, out, _ := connectTestEnv(t, cfgDir)
	code := Run([]string{"connect", "https://127.0.0.1:1", "--name", "laptop"}, env)
	if code != 4 {
		t.Fatalf("https on the closed loopback port = %d (stdout %s), want 4 (the usage accepted it)", code, out.String())
	}
}

// TestConnectNameFromConfig is the name of the machine: the flag
// wins, and without it the machine_label of the config is used;
// without neither one, the exit 2.
func TestConnectNameFromConfig(t *testing.T) {
	f := newFakePairingHub(t)
	cfgDir := t.TempDir()
	// Without the label and without the flag: the usage error.
	env, out, _ := connectTestEnv(t, cfgDir)
	code := Run([]string{"connect", f.server.URL}, env)
	if code != 2 {
		t.Fatalf("without the name: connect = %d (stdout %s), want 2", code, out.String())
	}
	// The label of the config.
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte("machine_label=laptop-label\n"), 0600); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	env, _, _ = connectTestEnv(t, cfgDir)
	go func() {
		<-f.pollEntered
		f.confirm()
	}()
	code = Run([]string{"connect", f.server.URL}, env)
	if code != 0 {
		t.Fatalf("with the label: connect = %d, want 0", code)
	}
	f.mu.Lock()
	if f.gotName != "laptop-label" {
		t.Fatalf("the name of the pairing = %q, want the label of the config", f.gotName)
	}
	f.mu.Unlock()
}

// TestConnectBadArgs is the argument parsing: the extra positional,
// the flag without value, the repeated flag.
func TestConnectBadArgs(t *testing.T) {
	f := newFakePairingHub(t)
	cfgDir := t.TempDir()
	cases := [][]string{
		{},
		{f.server.URL, "extra"},
		{f.server.URL, "--name"},
		{f.server.URL, "--name", "a", "--name", "b"},
		{f.server.URL, "--unknown"},
	}
	for i, args := range cases {
		env, out, _ := connectTestEnv(t, cfgDir)
		code := Run(append([]string{"connect"}, args...), env)
		if code != 2 {
			t.Fatalf("case %d (%v): connect = %d (stdout %s), want 2", i, args, code, out.String())
		}
	}
}

// TestConnectInterrupted is the Ctrl-C: the command stops without
// writing anything, and the exit is the interrupted one.
func TestConnectInterrupted(t *testing.T) {
	f := newFakePairingHub(t)
	cfgDir := t.TempDir()
	env, out, errb := connectTestEnv(t, cfgDir)
	// The poll of the fake hub blocks, so the interrupt lands in the
	// middle of the polling loop.
	old := connectSignal
	cancelCh := make(chan struct{})
	connectSignal = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-cancelCh
			cancel()
		}()
		return ctx, cancel
	}
	defer func() { connectSignal = old }()
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"connect", f.server.URL, "--name", "laptop"}, env)
	}()
	<-f.pollEntered // the command is in the polling loop.
	close(cancelCh) // the interrupt.
	code := <-done
	if code != connectExitInterrupted {
		t.Fatalf("connect = %d, want %d (the interrupted)", code, connectExitInterrupted)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want nothing", out.String())
	}
	if !strings.Contains(errb.String(), "interrupted") {
		t.Fatalf("stderr = %q, want the interrupted line", errb.String())
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("hub.json was written, want nothing")
	}
}
