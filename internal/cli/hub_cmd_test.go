package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// hubTestSessionKey is a fixed test session key: the base64 of 34
// bytes, well above the 32-byte bound. It is a test value, never a
// secret.
const hubTestSessionKey = base64Encode34

// base64Encode34 is the base64 of 34 fixed bytes, inlined so the test
// has no import cycle risk and stays a literal.
const base64Encode34 = "dGVzdC1zZXNzaW9uLWtleS0wMDAwMDAwMDAwMDAwMDAwMDAwMDA="

// hubFakeStore is the in-memory store the hub serve tests run against.
type hubFakeStore struct {
	mu       sync.Mutex
	versions []int64
	batchErr error
	pingErr  error
}

func newHubFakeStore() *hubFakeStore { return &hubFakeStore{} }

func (f *hubFakeStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	switch {
	case strings.Contains(stmt.SQL, "sqlite_master"):
		// The schema_migrations table exists only after the first batch.
		f.mu.Lock()
		exists := len(f.versions) > 0
		f.mu.Unlock()
		if exists {
			return store.Result{Rows: []store.Row{{"schema_migrations"}}}, nil
		}
		return store.Result{}, nil
	case strings.Contains(stmt.SQL, "SELECT version FROM schema_migrations"):
		f.mu.Lock()
		var rows []store.Row
		for _, v := range f.versions {
			rows = append(rows, store.Row{v})
		}
		f.mu.Unlock()
		return store.Result{Rows: rows}, nil
	case strings.Contains(stmt.SQL, "MAX(version)"):
		f.mu.Lock()
		var max int64
		for _, v := range f.versions {
			if v > max {
				max = v
			}
		}
		none := len(f.versions) == 0
		f.mu.Unlock()
		if none {
			return store.Result{Rows: []store.Row{{store.Value(nil)}}}, nil
		}
		return store.Result{Rows: []store.Row{{max}}}, nil
	}
	return store.Result{}, errors.New("hubFakeStore: unexpected statement " + stmt.SQL)
}

func (f *hubFakeStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	f.versions = append(f.versions, 1)
	return nil, nil
}

func (f *hubFakeStore) Ping(ctx context.Context) error {
	return f.pingErr
}

// mutexWriter is a race-free writer with a snapshot reader: the serve
// goroutine writes while the test reads.
type mutexWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *mutexWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *mutexWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// startHubServe runs cmdHubServe with a captured listener and a
// cancellable context, waits for the serving line, and returns the
// listen address and the stop function. The tests probe the server
// while it runs, then call stop, which cancels the context (the signal
// path) and waits for the clean exit, reporting stdout, stderr and the
// exit code.
func startHubServe(t *testing.T, vars map[string]string, st *hubFakeStore, args ...string) (string, func() (string, string, int)) {
	t.Helper()
	out, errb := &mutexWriter{}, &mutexWriter{}
	env := Env{
		Stdout: out,
		Stderr: errb,
		Getenv: func(k string) string { return vars[k] },
		Now:    func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		Sleep:  sleepCtx,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var listen string
	oldListen := hubServeListen
	hubServeListen = func(network, address string) (net.Listener, error) {
		listen = ln.Addr().String()
		return ln, nil
	}
	oldStore := newHubStore
	newHubStore = func(baseURL, token string) (store.Store, error) { return st, nil }
	var cancel context.CancelFunc
	oldCtx := hubServeContext
	hubServeContext = func() (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		return ctx, c
	}
	done := make(chan int, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		if cancel != nil {
			cancel()
		}
		_ = ln.Close()
		// Wait for the serve to return, so the web root it opened is
		// closed before the temporary directory is removed (Windows
		// cannot remove an open directory).
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("the serve did not return after the cleanup cancel")
		}
		hubServeListen = oldListen
		newHubStore = oldStore
		hubServeContext = oldCtx
	})
	go func() {
		done <- cmdHubServe(args, env)
		close(finished)
	}()
	// Wait for the serving line.
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(out.String(), `"status":"serving"`) {
		select {
		case code := <-done:
			t.Fatalf("the serve returned before it listened: code=%d out=%q err=%q", code, out.String(), errb.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the serve did not print the serving line: out=%q err=%q", out.String(), errb.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	var serving struct {
		Status string `json:"status"`
		Listen string `json:"listen"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &serving); err != nil {
		t.Fatalf("the serving line is not JSON: %q", out.String())
	}
	if serving.Listen != listen {
		t.Fatalf("the serving line says %q, the listener is %q", serving.Listen, listen)
	}
	return listen, func() (string, string, int) {
		cancel()
		select {
		case code := <-done:
			return out.String(), errb.String(), code
		case <-time.After(5 * time.Second):
			t.Fatal("the serve did not stop on the context cancel")
			return "", "", 1
		}
	}
}

type httpResponse struct {
	status  int
	body    string
	headers http.Header
}

func doPost(t *testing.T, url, body string) httpResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return httpResponse{status: resp.StatusCode, body: string(b), headers: resp.Header}
}

func doGet(t *testing.T, url string) httpResponse {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return httpResponse{status: resp.StatusCode, body: string(b), headers: resp.Header}
}

func TestHubServeStartsAndShutsDownCleanly(t *testing.T) {
	st := newHubFakeStore()
	listen, stop := startHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}, st)
	// The server answers while it runs: health and version.
	health := doGet(t, "http://"+listen+"/api/health")
	if health.status != 200 || health.body != `{"ok":true}` {
		t.Fatalf("health = %d %q", health.status, health.body)
	}
	version := doGet(t, "http://"+listen+"/api/version")
	if version.status != 200 {
		t.Fatalf("version status = %d, want 200", version.status)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(version.body), &v); err != nil {
		t.Fatalf("version body = %q", version.body)
	}
	if v["schema"] != float64(1) {
		t.Fatalf("version schema = %v, want 1", v["schema"])
	}
	if v["version"] == "" {
		t.Fatal("the version is empty")
	}
	// The API headers.
	if health.headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("health Cache-Control = %q", health.headers.Get("Cache-Control"))
	}
	// The accounts routes are registered: without a session the GET of
	// the session is 401, and the bootstrap answers 404 when the
	// bootstrap token is not set (it is optional and unset here).
	session := doGet(t, "http://"+listen+"/api/session")
	if session.status != 401 || session.body != `{"error":"unauthenticated"}` {
		t.Fatalf("GET /api/session without a session = %d %q", session.status, session.body)
	}
	bootstrap := doPost(t, "http://"+listen+"/api/bootstrap", `{"token":"x","name":"x","email":"x","password":"x"}`)
	if bootstrap.status != 404 || bootstrap.body != `{"error":"not_found"}` {
		t.Fatalf("POST /api/bootstrap without the token = %d %q", bootstrap.status, bootstrap.body)
	}
	// The health turns 503 when the store stops answering.
	st.pingErr = errors.New("database down")
	health = doGet(t, "http://"+listen+"/api/health")
	st.pingErr = nil
	if health.status != 503 || health.body != `{"ok":false}` {
		t.Fatalf("health after the store failed = %d %q", health.status, health.body)
	}
	// A signal (the context cancel) shuts the server down cleanly.
	out, errb, code := stop()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
	}
	if !strings.Contains(out, `"status":"serving"`) {
		t.Fatalf("stdout = %q, want the serving line", out)
	}
}

func TestHubServeRegistersTheLiveRoutes(t *testing.T) {
	st := newHubFakeStore()
	listen, stop := startHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}, st)
	// The two routes are registered: without a session they answer
	// the 401 of the session check, not the 404 of the router.
	live := doGet(t, "http://"+listen+"/api/live")
	if live.status != 401 || live.body != `{"error":"unauthenticated"}` {
		t.Fatalf("GET /api/live without a session = %d %q, want the 401", live.status, live.body)
	}
	events := doGet(t, "http://"+listen+"/api/events")
	if events.status != 401 || events.body != `{"error":"unauthenticated"}` {
		t.Fatalf("GET /api/events without a session = %d %q, want the 401", events.status, events.body)
	}
	out, _, code := stop()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q)", code, out)
	}
}

func TestHubServesStaticFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>hub</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	listen, stop := startHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}, newHubFakeStore(), "--web", dir)
	// The root, the SPA fallback and the API, all while it runs.
	resp := doGet(t, "http://"+listen+"/")
	if resp.status != 200 || resp.body != "<html>hub</html>" {
		t.Fatalf("GET / = %d %q", resp.status, resp.body)
	}
	resp = doGet(t, "http://"+listen+"/deep/spa/route")
	if resp.status != 200 || resp.body != "<html>hub</html>" {
		t.Fatalf("GET /deep/spa/route = %d %q (the SPA fallback)", resp.status, resp.body)
	}
	resp = doGet(t, "http://"+listen+"/api/health")
	if resp.status != 200 || resp.body != `{"ok":true}` {
		t.Fatalf("GET /api/health = %d %q", resp.status, resp.body)
	}
	if _, _, code := stop(); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	// Without a web dir every path outside /api/ is 404.
	listen2, stop2 := startHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}, newHubFakeStore())
	resp = doGet(t, "http://"+listen2+"/index.html")
	if resp.status != 404 {
		t.Fatalf("GET /index.html without --web = %d, want 404", resp.status)
	}
	if _, _, code := stop2(); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestHubServeBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"other"},
		{"serve", "--bogus", "x"},
		{"serve", "--listen"},
		{"serve", "--listen", "a", "--listen", "b"},
		{"serve", "extra"},
		{"serve", "--web"},
	} {
		full := append([]string{"hub"}, args...)
		stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{}, full...)
		if exit != 2 {
			t.Fatalf("hub %v: exit = %d, want 2 (stdout %q)", args, exit, stdout)
		}
		if !strings.Contains(stderr, "hub serve [--listen <host:port>] [--web <dir>]") {
			t.Fatalf("hub %v: stderr does not contain the hub serve usage", args)
		}
		var line map[string]string
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil {
			t.Fatalf("hub %v: stdout = %q is not one JSON line", args, stdout)
		}
		if line["status"] != "2" {
			t.Fatalf("hub %v: status = %q, want 2", args, line["status"])
		}
	}
}

func TestHubServeWebDirMissingIsBadUsage(t *testing.T) {
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL: "http://127.0.0.1:1",
	}, "hub", "serve", "--web", filepath.Join(t.TempDir(), "absent"))
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", exit, stdout)
	}
	if !strings.Contains(stderr, "hub serve: --web is not a directory") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestHubServeWebSymlinkOutsideIsNotServed(t *testing.T) {
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>hub</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(web, "link.txt")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(web, "inlink.txt")); err != nil {
		t.Skipf("cannot create symbolic links here: %v", err)
	}
	listen, stop := startHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}, newHubFakeStore(), "--web", web)
	// The link that escapes the web root is not served, even with an
	// index available; the one that stays inside is.
	resp := doGet(t, "http://"+listen+"/link.txt")
	if resp.status != 404 {
		t.Fatalf("GET /link.txt = %d %q, want 404", resp.status, resp.body)
	}
	if resp.body == "outside-fixture" {
		t.Fatal("the file outside the web root was served")
	}
	resp = doGet(t, "http://"+listen+"/inlink.txt")
	if resp.status != 200 || resp.body != "inside" {
		t.Fatalf("GET /inlink.txt = %d %q, want 200 and the inside content", resp.status, resp.body)
	}
	if _, _, code := stop(); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestHubServeEmptyHostnameIsBadUsage(t *testing.T) {
	for _, dbURL := range []string{"http://:0", "https://:8443"} {
		stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
			hubEnvDBURL: dbURL,
		}, "hub", "serve")
		if exit != 2 {
			t.Fatalf("%s: exit = %d, want 2 (stdout %q)", dbURL, exit, stdout)
		}
		if !strings.Contains(stderr, "must be an http:// or https:// URL") {
			t.Fatalf("%s: stderr = %q", dbURL, stderr)
		}
	}
}

func TestHubServeMigrationFailureDropsServerMessage(t *testing.T) {
	// The real client against a loopback server that answers every
	// pipeline with an error whose message echoes the Authorization
	// header: when the migration fails, the command output must carry
	// none of the server text (message, malformed code) nor the token.
	secret := strings.Repeat("synthetic", 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"results":[{"type":"error","error":{"message":"Bearer %s was rejected","code":"BAD-LOW"}},{"type":"ok","response":{"type":"close"}}]}`, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL:      srv.URL,
		hubEnvDBToken:    secret,
		hubEnvSessionKey: hubTestSessionKey,
	}, "hub", "serve")
	if exit != 4 {
		t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, "hub serve: migrations:") {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, s := range []string{stdout, stderr} {
		if strings.Contains(s, secret) {
			t.Fatalf("the token leaked into the output: %q", s)
		}
		if strings.Contains(s, "was rejected") {
			t.Fatalf("the server message leaked into the output: %q", s)
		}
		if strings.Contains(s, "BAD-LOW") {
			t.Fatalf("the malformed code leaked into the output: %q", s)
		}
	}
}

func TestHubServeMissingDBURLIsBadUsage(t *testing.T) {
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{}, "hub", "serve")
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", exit, stdout)
	}
	if !strings.Contains(stderr, "NAU_HUB_DB_URL is required") {
		t.Fatalf("stderr = %q", stderr)
	}
	var line map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil {
		t.Fatalf("stdout = %q", stdout)
	}
	if line["status"] != "2" {
		t.Fatalf("status = %q, want 2", line["status"])
	}
}

func TestHubServeBadSchemeIsBadUsage(t *testing.T) {
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL: "postgres://127.0.0.1:1",
	}, "hub", "serve")
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", exit, stdout)
	}
	if !strings.Contains(stderr, "must be an http:// or https:// URL") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestHubServeMigrationFailureExits4WithoutToken(t *testing.T) {
	st := newHubFakeStore()
	st.batchErr = errors.New("the database did not accept the batch")
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvDBToken:    "sekret-token-sentinel",
		hubEnvSessionKey: hubTestSessionKey,
	}, "hub", "serve")
	if exit != 4 {
		t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	// One line on stderr, without the token.
	if !strings.Contains(stderr, "hub serve: migrations:") {
		t.Fatalf("stderr = %q", stderr)
	}
	for _, s := range []string{stdout, stderr} {
		if strings.Contains(s, "sekret-token-sentinel") {
			t.Fatalf("the token leaked into the output: %q", s)
		}
	}
	var line map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil {
		t.Fatalf("stdout = %q", stdout)
	}
	if line["status"] != "4" {
		t.Fatalf("status = %q, want 4", line["status"])
	}
}

func TestHubServeUnreachableDatabaseExits4(t *testing.T) {
	// The real client against a loopback port with no listener: the
	// startup migration cannot run, the serve exits 4 with one stderr
	// line and the token never appears.
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvDBToken:    "sekret-token-unreachable",
		hubEnvSessionKey: hubTestSessionKey,
	}, "hub", "serve")
	if exit != 4 {
		t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, "hub serve: migrations:") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stderr+stdout, "sekret-token-unreachable") {
		t.Fatalf("the token leaked into the output: %q %q", stderr, stdout)
	}
}

func TestHubServeMissingSessionKeyIsBadUsage(t *testing.T) {
	stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
		hubEnvDBURL: "http://127.0.0.1:1",
	}, "hub", "serve")
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q)", exit, stdout)
	}
	if !strings.Contains(stderr, "NAU_HUB_SESSION_KEY is required") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stderr+stdout, "NAU_HUB_DB_URL=") {
		t.Fatalf("the output carries an env value: %q %q", stderr, stdout)
	}
	var line map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &line); err != nil {
		t.Fatalf("stdout = %q", stdout)
	}
	if line["status"] != "2" {
		t.Fatalf("status = %q, want 2", line["status"])
	}
}

func TestHubServeInvalidSessionKeyIsBadUsage(t *testing.T) {
	for name, key := range map[string]string{
		"not base64":       "@@@-not-base64@@@",
		"under 32 bytes":   "YXNkZg==",
		"padded too short": "YWJj",
	} {
		stdout, stderr, exit := runCLIVars(t, t.TempDir(), map[string]string{
			hubEnvDBURL:      "http://127.0.0.1:1",
			hubEnvSessionKey: key,
		}, "hub", "serve")
		if exit != 2 {
			t.Fatalf("%s: exit = %d, want 2 (stdout %q)", name, exit, stdout)
		}
		if !strings.Contains(stderr, "NAU_HUB_SESSION_KEY must be base64 of at least 32 bytes") {
			t.Fatalf("%s: stderr = %q", name, stderr)
		}
		if strings.Contains(stderr+stdout, key) {
			t.Fatalf("%s: the key leaked into the output: %q %q", name, stderr, stdout)
		}
	}
}

func TestHubCommandNeedsNoConfigDir(t *testing.T) {
	// The hub keeps no local state: it must not refuse without a config
	// dir, unlike the local commands.
	stdout, _, exit := runCLI(t, "", false, "hub", "serve")
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (the missing DB URL, not the config dir)", exit)
	}
	if strings.Contains(stdout, "no_config_dir") {
		t.Fatalf("the hub refused for the config dir: %q", stdout)
	}
}

// runCLIVars is runCLI with an explicit environment map, so a test sets
// exactly the variables the command reads.
func runCLIVars(t *testing.T, dir string, vars map[string]string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	env := Env{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    func(k string) string { return vars[k] },
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: dir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run(args, env)
	return out.String(), errb.String(), exit
}
