package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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

// fakePairingKey is the machine key of the fake hub: the base64url
// without padding of 32 bytes, the shape of the real key (it is the
// encoding of 32 fixed bytes, a test value, never a secret).
const fakePairingKey = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI"

func init() {
	if b, err := base64.RawURLEncoding.DecodeString(fakePairingKey); err != nil || len(b) != 32 {
		// The constant is the encoding of 32 bytes: a broken constant
		// would fail the command validation of the tests.
		panic("the fake pairing key is not the base64url of 32 bytes")
	}
}

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

// ---- the fake transport of the connect tests -------------------------

// connectRecording is one request the fake transport saw: the url,
// the method, the body and the deadline of the context the client
// carried.
type connectRecording struct {
	url         string
	method      string
	body        string
	deadline    time.Time
	hasDeadline bool
}

// fakeConnectTransport is the transport of the connect tests: it
// answers the scripted response without sockets and records the
// requests, so the redirect policy and the deadline of the real
// http.Client of the command are what the tests exercise.
type fakeConnectTransport struct {
	mu   sync.Mutex
	reqs []connectRecording
	resp func(*http.Request) *http.Response
}

func (f *fakeConnectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	var dl time.Time
	var hasDL bool
	if d, ok := req.Context().Deadline(); ok {
		dl, hasDL = d, true
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, connectRecording{url: req.URL.String(), method: req.Method, body: string(body), deadline: dl, hasDeadline: hasDL})
	f.mu.Unlock()
	return f.resp(req), nil
}

// fakeConnectResponse builds the scripted response of the fake
// transport.
func fakeConnectResponse(req *http.Request, status int, body []byte, loc string) *http.Response {
	hdr := http.Header{}
	if loc != "" {
		hdr.Set("Location", loc)
	}
	if len(body) > 0 {
		hdr.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: status,
		Header:     hdr,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Request:    req,
	}
}

// connectTestHub is the url of the fake transport tests: a https url
// the usage accepts, and no socket is opened.
const connectTestHub = "https://127.0.0.1:7440"

// withFakeTransport runs the connect with the fake transport and the
// injected signal, restoring both.
func withFakeTransport(t *testing.T, f *fakeConnectTransport, sig func() (context.Context, context.CancelFunc), env Env, args []string) int {
	t.Helper()
	oldRT, oldSig := connectRoundTrip, connectSignal
	connectRoundTrip = f
	if sig != nil {
		connectSignal = sig
	}
	defer func() { connectRoundTrip, connectSignal = oldRT, oldSig }()
	return Run(args, env)
}

// TestConnectRefusesRedirects is the redirect of the connect: the
// client follows none. A 307 or a 308 to another origin — a remote
// http or a different origin — is the exit 4, without writing
// anything and without resending the poll_token to the new
// destination.
func TestConnectRefusesRedirects(t *testing.T) {
	cases := []struct {
		name   string
		status int
		loc    string
		stage  string // create or poll
	}{
		{"create 307 remote http", http.StatusTemporaryRedirect, "http://remote.invalid", "create"},
		{"poll 307 remote http", http.StatusTemporaryRedirect, "http://remote.invalid", "poll"},
		{"create 308 other origin", http.StatusPermanentRedirect, "https://other.example", "create"},
		{"poll 308 other origin", http.StatusPermanentRedirect, "https://other.example", "poll"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			env, out, errb := connectTestEnv(t, cfgDir)
			f := &fakeConnectTransport{}
			createBody, _ := json.Marshal(map[string]any{
				"code":       fakePairingCode,
				"poll_token": "polltoken0000000000000000000000000001",
				"expires_ms": 9999999999999,
			})
			f.resp = func(req *http.Request) *http.Response {
				if req.URL.Path == "/api/pairings" {
					if c.stage == "create" {
						return fakeConnectResponse(req, c.status, nil, c.loc)
					}
					return fakeConnectResponse(req, http.StatusCreated, createBody, "")
				}
				return fakeConnectResponse(req, c.status, nil, c.loc)
			}
			code := withFakeTransport(t, f, nil, env, []string{"connect", connectTestHub, "--name", "laptop"})
			if code != 4 {
				t.Fatalf("connect = %d (stdout %s stderr %s), want 4", code, out.String(), errb.String())
			}
			if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
				t.Fatal("hub.json was written, want nothing")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			want := 2
			if c.stage == "create" {
				want = 1
			}
			if len(f.reqs) != want {
				t.Fatalf("the transport saw %d requests, want %d (no redirect is followed)", len(f.reqs), want)
			}
			for i, r := range f.reqs {
				if !strings.HasPrefix(r.url, connectTestHub) {
					t.Fatalf("request %d went to %s, want the hub origin", i, r.url)
				}
				if strings.Contains(r.url, "remote.invalid") || strings.Contains(r.url, "other.example") {
					t.Fatalf("request %d reached the redirect destination %s", i, r.url)
				}
			}
			if c.stage == "poll" {
				// The poll_token went to the hub exactly once and
				// nowhere else.
				n := 0
				for _, r := range f.reqs {
					if strings.Contains(r.body, "polltoken0000000000000000000000000001") {
						n++
					}
				}
				if n != 1 {
					t.Fatalf("the poll_token was sent in %d requests, want 1", n)
				}
			}
		})
	}
}

// TestConnectUntrustedResponse is the validation of the hub answer:
// the code with a terminal control, the code outside the displayed
// alphabet, the non-positive expires_ms, the key that does not decode
// to 32 bytes and the machine id outside the shape are the exit 4,
// without writing anything and without a raw value of the hub on the
// terminal.
func TestConnectUntrustedResponse(t *testing.T) {
	rawKey := base64.RawURLEncoding.EncodeToString(make([]byte, 33)) // 44 letters, 33 bytes
	cases := []struct {
		name    string
		create  map[string]any
		deliver map[string]any
		raw     string
	}{
		{
			name:   "code with a terminal control",
			create: map[string]any{"code": "\u001b[31m7K4P-9M2N", "poll_token": "polltoken0000000000000000000000000001", "expires_ms": 9999999999999},
			raw:    "\u001b[31m7K4P-9M2N",
		},
		{
			name:   "code outside the alphabet",
			create: map[string]any{"code": "i000-l000", "poll_token": "polltoken0000000000000000000000000001", "expires_ms": 9999999999999},
			raw:    "i000-l000",
		},
		{
			name:   "non-positive expires_ms",
			create: map[string]any{"code": fakePairingCode, "poll_token": "polltoken0000000000000000000000000001", "expires_ms": 0},
		},
		{
			name:    "key that is not 32 bytes",
			create:  map[string]any{"code": fakePairingCode, "poll_token": "polltoken0000000000000000000000000001", "expires_ms": 9999999999999},
			deliver: map[string]any{"machine_id": "machine-1", "key": rawKey},
			raw:     rawKey,
		},
		{
			name:    "machine id outside the shape",
			create:  map[string]any{"code": fakePairingCode, "poll_token": "polltoken0000000000000000000000000001", "expires_ms": 9999999999999},
			deliver: map[string]any{"machine_id": "../evil", "key": fakePairingKey},
			raw:     "../evil",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			env, out, errb := connectTestEnv(t, cfgDir)
			f := &fakeConnectTransport{}
			createBody, _ := json.Marshal(c.create)
			deliverBody, _ := json.Marshal(map[string]any{"machine_id": "machine-1", "key": fakePairingKey})
			if c.deliver != nil {
				deliverBody, _ = json.Marshal(c.deliver)
			}
			f.resp = func(req *http.Request) *http.Response {
				if req.URL.Path == "/api/pairings" {
					return fakeConnectResponse(req, http.StatusCreated, createBody, "")
				}
				return fakeConnectResponse(req, http.StatusOK, deliverBody, "")
			}
			code := withFakeTransport(t, f, nil, env, []string{"connect", connectTestHub, "--name", "laptop"})
			if code != 4 {
				t.Fatalf("connect = %d (stdout %s stderr %s), want 4", code, out.String(), errb.String())
			}
			if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
				t.Fatal("hub.json was written, want nothing")
			}
			if c.raw != "" {
				if strings.Contains(out.String(), c.raw) || strings.Contains(errb.String(), c.raw) {
					t.Fatalf("the raw value of the hub reached the terminal: stdout %q stderr %q", out.String(), errb.String())
				}
			}
		})
	}
}

// TestConnectLocalDeadline is the local deadline of the connect: the
// command gives up 10 minutes after the start, or sooner when the hub
// answers a closer validity. The deadline bounds the requests and the
// wait between the polls, and the exit is the expired one without
// persisting.
func TestConnectLocalDeadline(t *testing.T) {
	cases := []struct {
		name      string
		expiresMS int64 // the hub validity, from the start
	}{
		{"the local 10 minutes", 24 * time.Hour.Milliseconds()},
		{"the closer hub validity", 5 * time.Minute.Milliseconds()},
		{"a hub validity off the poll grid", 5*time.Minute.Milliseconds() + 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			now := start
			cfgDir := t.TempDir()
			var out, errb bytes.Buffer
			env := Env{
				Stdout:    &out,
				Stderr:    &errb,
				Getenv:    func(string) string { return "" },
				ConfigDir: cfgDir,
				Now:       func() time.Time { return now },
				Sleep: func(ctx context.Context, d time.Duration) error {
					now = now.Add(d)
					return ctx.Err()
				},
			}
			f := &fakeConnectTransport{}
			expires := start.Add(time.Duration(c.expiresMS) * time.Millisecond).UnixMilli()
			createBody, _ := json.Marshal(map[string]any{
				"code":       fakePairingCode,
				"poll_token": "polltoken0000000000000000000000000001",
				"expires_ms": expires,
			})
			f.resp = func(req *http.Request) *http.Response {
				if req.URL.Path == "/api/pairings" {
					return fakeConnectResponse(req, http.StatusCreated, createBody, "")
				}
				return fakeConnectResponse(req, http.StatusAccepted, []byte(`{"status":"pending"}`), "")
			}
			t0 := time.Now()
			code := withFakeTransport(t, f, nil, env, []string{"connect", connectTestHub, "--name", "laptop"})
			if code != 3 {
				t.Fatalf("connect = %d (stdout %s stderr %s), want 3 (the expired)", code, out.String(), errb.String())
			}
			if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
				t.Fatal("hub.json was written, want nothing")
			}
			// The command gave up exactly at the effective deadline:
			// the smaller between the local 10 minutes and the hub
			// validity. The last wait is cut to the time left, so the
			// fake clock never passes it.
			want := connectDeadline
			if hub := time.Duration(c.expiresMS) * time.Millisecond; hub < want {
				want = hub
			}
			if got := now.Sub(start); got != want {
				t.Fatalf("the command ran for %s, want exactly %s (the effective deadline)", got, want)
			}
			// The poll request runs under a deadline context: each
			// request is bounded (the per-request timeout of 10 s, the
			// closer of the two), and the wait between the polls is
			// bounded by the effective deadline (the exit 3 above).
			f.mu.Lock()
			defer f.mu.Unlock()
			var poll *connectRecording
			for i := range f.reqs {
				if f.reqs[i].url == connectTestHub+"/api/pairings/poll" {
					poll = &f.reqs[i]
					break
				}
			}
			if poll == nil || !poll.hasDeadline {
				t.Fatalf("the poll request carried no deadline (requests: %+v)", f.reqs)
			}
			if got := poll.deadline.Sub(t0); got > connectDeadline+time.Second {
				t.Fatalf("the deadline of the poll request is %s after the start, want at most the local deadline", got.Round(time.Millisecond))
			}
		})
	}
}

// TestConnectCancelBeforePersistence is the Ctrl-C at the response
// boundary: the cancellation that lands after the 200 of the delivery
// is read, before the file is published, exits 130 without writing
// hub.json.
func TestConnectCancelBeforePersistence(t *testing.T) {
	cfgDir := t.TempDir()
	env, out, errb := connectTestEnv(t, cfgDir)
	var cancelAtBoundary context.CancelFunc
	sig := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelAtBoundary = cancel
		return ctx, cancel
	}
	createBody, _ := json.Marshal(map[string]any{
		"code":       fakePairingCode,
		"poll_token": "polltoken0000000000000000000000000001",
		"expires_ms": 9999999999999,
	})
	deliverBody, _ := json.Marshal(map[string]any{"machine_id": "machine-1", "key": fakePairingKey})
	f := &fakeConnectTransport{}
	f.resp = func(req *http.Request) *http.Response {
		if req.URL.Path == "/api/pairings" {
			return fakeConnectResponse(req, http.StatusCreated, createBody, "")
		}
		// The interrupt lands after the response is read, before
		// the command persists.
		cancelAtBoundary()
		return fakeConnectResponse(req, http.StatusOK, deliverBody, "")
	}
	code := withFakeTransport(t, f, sig, env, []string{"connect", connectTestHub, "--name", "laptop"})
	if code != connectExitInterrupted {
		t.Fatalf("connect = %d (stdout %s), want %d (the interrupted)", code, out.String(), connectExitInterrupted)
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

// TestConnectKeyRawShape checks the raw form of the delivered key: a
// key the base64url decoder accepts only because it skips the line
// break is refused, and so are padding and the wrong length.
func TestConnectKeyRawShape(t *testing.T) {
	good := strings.Repeat("A", 43)
	if !validConnectKey(good) {
		t.Fatalf("validConnectKey(43 base64url chars) = false, want true")
	}
	for name, key := range map[string]string{
		"line feed":         good[:20] + "\n" + good[20:],
		"carriage return":   good[:20] + "\r" + good[20:],
		"padding":           good + "=",
		"short":             good[:42],
		"standard alphabet": good[:42] + "+",
	} {
		if validConnectKey(key) {
			t.Errorf("validConnectKey(%s) = true, want false", name)
		}
	}
}

// TestConnectExpiryInFlight is the effective deadline reached during a
// poll request: the request dies with its bounded context, and the
// command exits 3 (expired), not 4 (unreachable), without writing.
func TestConnectExpiryInFlight(t *testing.T) {
	cfgDir := t.TempDir()
	var out, errb bytes.Buffer
	env := Env{
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    func(string) string { return "" },
		ConfigDir: cfgDir,
		Now:       time.Now,
		Sleep:     sleepCtx,
	}
	f := &fakeConnectTransport{}
	createBody, _ := json.Marshal(map[string]any{
		"code":       fakePairingCode,
		"poll_token": "polltoken0000000000000000000000000001",
		"expires_ms": time.Now().Add(150 * time.Millisecond).UnixMilli(),
	})
	f.resp = func(req *http.Request) *http.Response {
		if req.URL.Path == "/api/pairings" {
			return fakeConnectResponse(req, http.StatusCreated, createBody, "")
		}
		// The poll hangs until its context ends: the hub never answers
		// before the deadline.
		<-req.Context().Done()
		return nil
	}
	start := time.Now()
	code := withFakeTransport(t, f, nil, env, []string{"connect", connectTestHub, "--name", "laptop"})
	if code != 3 {
		t.Fatalf("connect = %d (stdout %s stderr %s), want 3 (the expired)", code, out.String(), errb.String())
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the command took %s, want it to stop at the 150 ms validity", took.Round(time.Millisecond))
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "hub.json")); !os.IsNotExist(err) {
		t.Fatal("hub.json was written, want nothing")
	}
}

// TestConnectBaseForms is the base of the hub url, in the three
// forms: without the trailing slash, with it, and with a path ending
// in a slash. The command joins the routes on the escaped path of the
// url, without normalizing it: the create and the poll reach the hub
// with the single slash of the route, and the stored url is the base
// as given.
func TestConnectBaseForms(t *testing.T) {
	cases := []struct {
		base   string
		create string
		poll   string
	}{
		{connectTestHub, connectTestHub + "/api/pairings", connectTestHub + "/api/pairings/poll"},
		{connectTestHub + "/", connectTestHub + "/api/pairings", connectTestHub + "/api/pairings/poll"},
		{connectTestHub + "/base/", connectTestHub + "/base/api/pairings", connectTestHub + "/base/api/pairings/poll"},
	}
	for _, c := range cases {
		t.Run(c.base, func(t *testing.T) {
			cfgDir := t.TempDir()
			env, out, errb := connectTestEnv(t, cfgDir)
			f := &fakeConnectTransport{}
			createBody, _ := json.Marshal(map[string]any{
				"code":       fakePairingCode,
				"poll_token": "polltoken0000000000000000000000000001",
				"expires_ms": 9999999999999,
			})
			deliverBody, _ := json.Marshal(map[string]any{"machine_id": "machine-1", "key": fakePairingKey})
			f.resp = func(req *http.Request) *http.Response {
				if strings.HasSuffix(req.URL.Path, "/api/pairings") {
					return fakeConnectResponse(req, http.StatusCreated, createBody, "")
				}
				return fakeConnectResponse(req, http.StatusOK, deliverBody, "")
			}
			code := withFakeTransport(t, f, nil, env, []string{"connect", c.base, "--name", "laptop"})
			if code != 0 {
				t.Fatalf("connect = %d (stdout %s stderr %s), want 0", code, out.String(), errb.String())
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.reqs) != 2 {
				t.Fatalf("the transport saw %d requests, want 2 (the create and the poll)", len(f.reqs))
			}
			if f.reqs[0].url != c.create {
				t.Fatalf("the create went to %s, want %s (the route with the single slash)", f.reqs[0].url, c.create)
			}
			if f.reqs[1].url != c.poll {
				t.Fatalf("the poll went to %s, want %s (the route with the single slash)", f.reqs[1].url, c.poll)
			}
			// The stored url is the base as given, not the joined
			// route.
			data, err := os.ReadFile(filepath.Join(cfgDir, "hub.json"))
			if err != nil {
				t.Fatalf("read hub.json: %v", err)
			}
			var file hubJSONFile
			if err := json.Unmarshal(data, &file); err != nil {
				t.Fatalf("hub.json %s: %v", data, err)
			}
			if file.URL != c.base {
				t.Fatalf("the stored url = %q, want the base as given %q", file.URL, c.base)
			}
		})
	}
}

// TestConnectTrailingSlashBase is the base with the trailing slash
// against the real router of the fake hub: the reported symptom was
// the joined //api/pairings, the 404 of the router and the vague
// "the hub did not accept the pairing". The base with the slash runs
// the full flow and stores the credential.
func TestConnectTrailingSlashBase(t *testing.T) {
	f := newFakePairingHub(t)
	base := f.server.URL + "/"
	cfgDir := t.TempDir()
	env, out, _ := connectTestEnv(t, cfgDir)
	go func() {
		<-f.pollEntered
		f.confirm()
	}()
	code := Run([]string{"connect", base, "--name", "laptop"}, env)
	if code != 0 {
		t.Fatalf("connect with the trailing slash = %d (stdout %s), want 0 (the hub answered the pairing)", code, out.String())
	}
	want := `{"status":"connected","machine_id":"machine-1"}` + "\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	data, err := os.ReadFile(filepath.Join(cfgDir, "hub.json"))
	if err != nil {
		t.Fatalf("read hub.json: %v", err)
	}
	var file hubJSONFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("hub.json %s: %v", data, err)
	}
	if file.URL != base || file.MachineID != "machine-1" || file.Key != fakePairingKey {
		t.Fatalf("hub.json = %+v, want the base as given, the machine id and the key", file)
	}
}
