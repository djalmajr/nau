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

// The synthetic owner and recovery variables of the serve tests: the
// values are sentinels the tests check never appear in the output.
const (
	hubTestOwnerName  = "Ana Exemplo"
	hubTestOwnerEmail = "ana@example.test"
	hubTestOwnerPass  = "synthetic-owner-pass-1"
	hubTestRootToken  = "synthetic-root-token-0000000000000000"
	hubTestOtherPass  = "synthetic-other-pass-2"
)

// hubTestOwnerVars is the map of the owner variables the serve tests
// pass so the seed of the empty database succeeds.
func hubTestOwnerVars() map[string]string {
	return map[string]string{
		hubEnvOwnerName:  hubTestOwnerName,
		hubEnvOwnerEmail: hubTestOwnerEmail,
		hubEnvOwnerPass:  hubTestOwnerPass,
	}
}

// hubTestServeVars joins the base serve variables with the owner
// variables, overridable by the given values.
func hubTestServeVars(over map[string]string) map[string]string {
	vars := map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvSessionKey: hubTestSessionKey,
	}
	for k, v := range hubTestOwnerVars() {
		vars[k] = v
	}
	for k, v := range over {
		vars[k] = v
	}
	return vars
}

// hubFakeStore is the in-memory store the hub serve tests run against.
// The userCount, the hasOwner and the seed behavior model the owner
// seeding of the empty database: the count is answered from userCount,
// the owner read from hasOwner, and the seed batch applies, fails or
// loses the race as configured. The password rotation batch of the
// recovery reset is answered as configured too.
type hubFakeStore struct {
	mu           sync.Mutex
	versions     []int64
	batchErr     error
	pingErr      error
	userCount    int
	countErr     error
	seedErr      error
	seeded       int
	hasOwner     bool
	ownerReadErr error
	rotateErr    error
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
	case strings.Contains(stmt.SQL, "SELECT COUNT(*) FROM users"):
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.countErr != nil {
			return store.Result{}, f.countErr
		}
		return store.Result{Rows: []store.Row{{int64(f.userCount)}}}, nil
	case strings.Contains(stmt.SQL, "WHERE role = "):
		// The owner read of the seed guard and of the lost-race check.
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.ownerReadErr != nil {
			return store.Result{}, f.ownerReadErr
		}
		if f.hasOwner {
			return store.Result{Rows: []store.Row{{"owner-id", hubTestOwnerEmail, hubTestOwnerEmail, hubTestOwnerName, "owner-hash", int64(1), "owner", int64(0)}}}, nil
		}
		return store.Result{}, nil
	}
	return store.Result{}, errors.New("hubFakeStore: unexpected statement " + stmt.SQL)
}

func (f *hubFakeStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(stmts) > 0 && strings.Contains(stmts[0].SQL, "WHERE NOT EXISTS (SELECT 1 FROM users)") {
		// The owner seed batch: the migrations never carry the
		// conditional insert, so the first statement tells the seed
		// from the migration.
		if f.seedErr != nil {
			return nil, f.seedErr
		}
		f.seeded++
		return nil, nil
	}
	if len(stmts) > 0 && strings.Contains(stmts[0].SQL, "UPDATE users SET password_hash") {
		// The password rotation of the recovery reset.
		if f.rotateErr != nil {
			return nil, f.rotateErr
		}
		return nil, nil
	}
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	f.versions = append(f.versions, 1)
	return nil, nil
}

func (f *hubFakeStore) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
// listen address and the recovery listen address (empty without the
// root token) and the stop function. The tests probe the server
// while it runs, then call stop, which cancels the context (the signal
// path) and waits for the clean exit, reporting stdout, stderr and the
// exit code.
func startHubServe(t *testing.T, vars map[string]string, st *hubFakeStore, args ...string) (string, string, func() (string, string, int)) {
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
	// The state of the fake listeners is shared between the serve
	// goroutine, which calls the fake, and the test goroutine, which
	// reads the addresses after the serving line and closes the
	// listeners in the cleanup: it is kept under a mutex, so even the
	// Fatalf path never races the serve that is still running.
	fakes := &fakeHubListeners{pub: ln}
	oldListen := hubServeListen
	hubServeListen = func(network, address string) (net.Listener, error) {
		fakes.mu.Lock()
		defer fakes.mu.Unlock()
		if fakes.pubAddr == "" {
			fakes.pubAddr = fakes.pub.Addr().String()
			return fakes.pub, nil
		}
		// The recovery listener: a real loopback listener on an
		// ephemeral port, the same way the default binds, so the tests
		// probe a live socket without a fixed port.
		rec, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		fakes.rec = rec
		fakes.recAddr = rec.Addr().String()
		return rec, nil
	}
	oldStore := newHubStore
	newHubStore = func(baseURL, token string) (store.Store, error) { return st, nil }
	// The cancel of the context is created here, in the test
	// goroutine, before the serve goroutine starts: the serve reads
	// it only, so every later read (the stop, the cleanup) carries a
	// happens-before to the write.
	ctx, cancel := context.WithCancel(context.Background())
	oldCtx := hubServeContext
	hubServeContext = func() (context.Context, context.CancelFunc) {
		return ctx, cancel
	}
	done := make(chan int, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		fakes.closeAll()
		// Wait for the serve to return, so the web root it opened is
		// closed before the temporary directory is removed (Windows
		// cannot remove an open directory).
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
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
	// Wait for the serving line: the bound of the deadline is generous
	// under the race detector, where the serve of the tests is slow.
	deadline := time.Now().Add(10 * time.Second)
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
		Status         string `json:"status"`
		Listen         string `json:"listen"`
		RecoveryListen string `json:"recovery_listen"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &serving); err != nil {
		t.Fatalf("the serving line is not JSON: %q", out.String())
	}
	listen, recListen := fakes.addrs()
	if serving.Listen != listen {
		t.Fatalf("the serving line says %q, the listener is %q", serving.Listen, listen)
	}
	if serving.RecoveryListen != recListen {
		t.Fatalf("the recovery listen is %q, the serving line says %q", recListen, serving.RecoveryListen)
	}
	return listen, recListen, func() (string, string, int) {
		cancel()
		select {
		case code := <-done:
			return out.String(), errb.String(), code
		case <-time.After(30 * time.Second):
			t.Fatal("the serve did not stop on the context cancel")
			return "", "", 1
		}
	}
}

// fakeHubListeners is the state of the fake listeners of
// startHubServe: the public listener is created in the test
// goroutine, and the recovery listener is created by the serve
// goroutine inside the fake. The mutex is the synchronization between
// the two, for the address reads and the closes of the cleanup.
type fakeHubListeners struct {
	mu      sync.Mutex
	pub     net.Listener
	rec     net.Listener
	pubAddr string
	recAddr string
}

// addrs returns the listen addresses of the fake listeners, the
// recovery one empty when it did not open.
func (f *fakeHubListeners) addrs() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pubAddr, f.recAddr
}

// closeAll closes the fake listeners, the public one always and the
// recovery one when it opened.
func (f *fakeHubListeners) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pub != nil {
		_ = f.pub.Close()
	}
	if f.rec != nil {
		_ = f.rec.Close()
	}
}

type httpResponse struct {
	status  int
	body    string
	headers http.Header
}

var hubProbeClient = &http.Client{}

func doPost(t *testing.T, url, body string) httpResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hubProbeClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	assertListenerPolicy(t, resp.Header)
	return httpResponse{status: resp.StatusCode, body: string(b), headers: resp.Header}
}

func doGet(t *testing.T, url string) httpResponse {
	t.Helper()
	resp, err := hubProbeClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	assertListenerPolicy(t, resp.Header)
	return httpResponse{status: resp.StatusCode, body: string(b), headers: resp.Header}
}

func TestHubServeStartsAndShutsDownCleanly(t *testing.T) {
	st := newHubFakeStore()
	listen, _, stop := startHubServe(t, hubTestServeVars(nil), st)
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
	// the session is 401, and the root-token route answers the 404 of
	// an unknown route while the root token is not set (it is unset
	// here; the connection is the loopback of the test, so the refusal
	// is the missing token, not the connection).
	session := doGet(t, "http://"+listen+"/api/session")
	if session.status != 401 || session.body != `{"error":"unauthenticated"}` {
		t.Fatalf("GET /api/session without a session = %d %q", session.status, session.body)
	}
	rootToken := doPost(t, "http://"+listen+"/api/session/root-token", `{"token":"x","new":""}`)
	unknown := doPost(t, "http://"+listen+"/api/no-such-route", `{}`)
	if rootToken.status != 404 || rootToken.body != unknown.body {
		t.Fatalf("POST /api/session/root-token without the token = %d %q, want the same 404 of the unknown route %d %q",
			rootToken.status, rootToken.body, unknown.status, unknown.body)
	}
	// The health turns 503 when the store stops answering.
	st.mu.Lock()
	st.pingErr = errors.New("database down")
	st.mu.Unlock()
	health = doGet(t, "http://"+listen+"/api/health")
	st.mu.Lock()
	st.pingErr = nil
	st.mu.Unlock()
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
	listen, _, stop := startHubServe(t, hubTestServeVars(nil), st)
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
	listen, _, stop := startHubServe(t, hubTestServeVars(nil), newHubFakeStore(), "--web", dir)
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
	listen2, _, stop2 := startHubServe(t, hubTestServeVars(nil), newHubFakeStore())
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
		{"recover"},
		{"recover", "--bogus", "x"},
		{"recover", "--url"},
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
		if len(args) > 0 && args[0] == "recover" && !strings.Contains(line["motivo"], "hub recover --url <url>") {
			t.Fatalf("hub %v: the usage does not name the recover command: %q", args, line["motivo"])
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
	listen, _, stop := startHubServe(t, hubTestServeVars(nil), newHubFakeStore(), "--web", web)
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
	// The real client against a loopback server that accepts readiness
	// then returns an error whose message echoes the Authorization
	// header: when the migration fails, the command output must carry
	// none of the server text (message, malformed code) nor the token.
	secret := strings.Repeat("synthetic", 4)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[],"rows":[],"affected_row_count":0}}},{"type":"ok","response":{"type":"close"}}]}`))
			return
		}
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
	stdout, stderr, exit := runHubServe(t, map[string]string{
		hubEnvDBURL:      "http://127.0.0.1:1",
		hubEnvDBToken:    "sekret-token-sentinel",
		hubEnvSessionKey: hubTestSessionKey,
	}, st)
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
	// The real client cannot reach this loopback listener after it closes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	_, stderr, _, listens := startupCommand(t, nil, "http://"+address, func(context.Context, time.Duration) error { return context.DeadlineExceeded })
	if listens != 0 || !strings.Contains(stderr, "hub serve: readiness:") {
		t.Fatalf("listens=%d stderr=%q", listens, stderr)
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

func (f *hubFakeStore) seedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seeded
}

func (f *hubFakeStore) versionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.versions)
}

// runHubServe runs cmdHubServe with the fake store over the early
// exits of the serve, where the server never listens: the listener is
// replaced by a failure, so a path that reaches the listen is a test
// failure, and the command output and the exit code are returned
// once the command ends.
func runHubServe(t *testing.T, vars map[string]string, st *hubFakeStore, args ...string) (string, string, int) {
	t.Helper()
	out, errb := &mutexWriter{}, &mutexWriter{}
	env := Env{
		Stdout: out,
		Stderr: errb,
		Getenv: func(k string) string { return vars[k] },
		Now:    func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		Sleep:  sleepCtx,
	}
	oldListen := hubServeListen
	hubServeListen = func(string, string) (net.Listener, error) {
		return nil, errors.New("hubTest: the listener must not open")
	}
	oldStore := newHubStore
	newHubStore = func(baseURL, token string) (store.Store, error) { return st, nil }
	// The cancel of the context is created here, in the test
	// goroutine, before the serve goroutine starts: the serve reads
	// it only, so every later read (the stop, the cleanup) carries a
	// happens-before to the write.
	ctx, cancel := context.WithCancel(context.Background())
	oldCtx := hubServeContext
	hubServeContext = func() (context.Context, context.CancelFunc) {
		return ctx, cancel
	}
	t.Cleanup(func() {
		cancel()
		hubServeListen = oldListen
		newHubStore = oldStore
		hubServeContext = oldCtx
	})
	done := make(chan int, 1)
	go func() { done <- cmdHubServe(args, env) }()
	select {
	case code := <-done:
		return out.String(), errb.String(), code
	case <-time.After(10 * time.Second):
		t.Fatal("the serve did not return in time")
		return "", "", 1
	}
}

// TestHubServeSeedsOwnerOnEmptyDatabase starts the hub over the empty
// database with the three owner variables: the seed is applied once
// and the serve exits 0 on the clean shutdown.
func TestHubServeSeedsOwnerOnEmptyDatabase(t *testing.T) {
	st := newHubFakeStore()
	_, _, stop := startHubServe(t, hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken}), st)
	out, errb, code := stop()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
	}
	if st.seedCount() != 1 {
		t.Fatalf("the seed = %d, want 1 (stdout %q)", st.seedCount(), out)
	}
}

// TestHubServeSeedRaceLossIsSuccess starts the hub over the empty
// database and makes the seed lose the race to another instance (the
// constraint of the conditional insert, with the owner confirmed by
// the re-read): the serve still exits 0.
func TestHubServeSeedRaceLossIsSuccess(t *testing.T) {
	st := newHubFakeStore()
	st.seedErr = &store.StatementError{Index: 1, Code: "SQLITE_CONSTRAINT"}
	st.hasOwner = true
	_, _, stop := startHubServe(t, hubTestServeVars(nil), st)
	out, errb, code := stop()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
	}
}

// TestHubServeSeedConstraintWithoutOwnerExits4 makes the seed answer
// the constraint of the empty-database guard while the re-read finds
// no owner: the serve exits 4 with the ownerless-database line and no
// serving line, the same way it exits when the database holds users
// and no owner.
func TestHubServeSeedConstraintWithoutOwnerExits4(t *testing.T) {
	t.Run("lost race without an owner", func(t *testing.T) {
		st := newHubFakeStore()
		st.seedErr = &store.StatementError{Index: 1, Code: "SQLITE_CONSTRAINT"}
		stdout, stderr, exit := runHubServe(t, hubTestServeVars(nil), st)
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "the database has users but no owner") {
			t.Fatalf("stderr = %q", stderr)
		}
		if strings.Contains(stdout, "\"status\":\"serving\"") {
			t.Fatalf("the serving line went out: %q", stdout)
		}
	})
	t.Run("users and no owner", func(t *testing.T) {
		st := newHubFakeStore()
		st.userCount = 1
		stdout, stderr, exit := runHubServe(t, hubTestServeVars(nil), st)
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "the database has users but no owner") {
			t.Fatalf("stderr = %q", stderr)
		}
		if strings.Contains(stdout, "\"status\":\"serving\"") {
			t.Fatalf("the serving line went out: %q", stdout)
		}
	})
}

// TestHubServeOwnerReadFailureExits4 makes the owner read of the
// existing-user branch fail: the serve exits 4 with the read error.
func TestHubServeOwnerReadFailureExits4(t *testing.T) {
	st := newHubFakeStore()
	st.userCount = 1
	st.ownerReadErr = errors.New("the owner read did not answer")
	stdout, stderr, exit := runHubServe(t, hubTestServeVars(nil), st)
	if exit != 4 {
		t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, "hub serve: the owner read:") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout, "\"status\":\"serving\"") {
		t.Fatalf("the serving line went out: %q", stdout)
	}
}

// TestHubServeMissingOwnerVariablesExit2 covers the empty database
// with each owner variable missing, empty or failing the password
// policy: the serve exits 2 naming the variable, never the value, and
// nothing is seeded.
func TestHubServeMissingOwnerVariablesExit2(t *testing.T) {
	cases := []struct {
		name, varName, value, want string
	}{
		{"missing name", hubEnvOwnerName, "", "is required"},
		{"empty email", hubEnvOwnerEmail, "", "is required"},
		{"blank password", hubEnvOwnerPass, "", "is required"},
		{"whitespace password", hubEnvOwnerPass, "            ", "is required"},
		{"weak password", hubEnvOwnerPass, "short", "must be 12 to 128 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := hubTestOwnerVars()
			vars[tc.varName] = tc.value
			st := newHubFakeStore()
			stdout, stderr, exit := runHubServe(t, hubTestServeVars(vars), st)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.varName) || !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want the variable named and the reason %q", stderr, tc.want)
			}
			if tc.value != "" && strings.Contains(stdout+stderr, tc.value) {
				t.Fatalf("the value leaked into the output: %q %q", stdout, stderr)
			}
			if st.seedCount() != 0 {
				t.Fatalf("the seed ran: %d, want 0", st.seedCount())
			}
		})
	}
}

// TestHubServeShortRootTokenExits2 exits 2 with a defined root token
// of less than 32 bytes, before the store is touched: the error
// names the variable, never the value, and no migration and no seed
// ran.
func TestHubServeShortRootTokenExits2(t *testing.T) {
	const shortToken = "short-synthetic-token"
	st := newHubFakeStore()
	stdout, stderr, exit := runHubServe(t, hubTestServeVars(map[string]string{hubEnvRootToken: shortToken}), st)
	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
	}
	if !strings.Contains(stderr, hubEnvRootToken+" must be at least 32 bytes and not blank") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stdout+stderr, shortToken) {
		t.Fatalf("the token leaked into the output: %q %q", stdout, stderr)
	}
	if st.versionCount() != 0 || st.seedCount() != 0 {
		t.Fatalf("the store was touched: versions=%d seeded=%d", st.versionCount(), st.seedCount())
	}
}

// TestHubServeCountAndSeedFailuresExit4 exits 4 when the database does
// not answer the user count or the owner seed (anything that is not
// the race loss), with one stderr line.
func TestHubServeCountAndSeedFailuresExit4(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		st := newHubFakeStore()
		st.countErr = errors.New("the database did not answer")
		stdout, stderr, exit := runHubServe(t, hubTestServeVars(nil), st)
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "hub serve: the user count:") {
			t.Fatalf("stderr = %q", stderr)
		}
	})
	t.Run("seed", func(t *testing.T) {
		st := newHubFakeStore()
		st.seedErr = errors.New("the database did not accept the seed")
		stdout, stderr, exit := runHubServe(t, hubTestServeVars(nil), st)
		if exit != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", exit, stdout, stderr)
		}
		if !strings.Contains(stderr, "hub serve: the owner seed:") {
			t.Fatalf("stderr = %q", stderr)
		}
	})
}

// TestHubServeExistingUserIgnoresOwnerVariables starts the hub over a
// database that already holds a user: the owner variables are ignored
// (nothing is seeded), and the stderr warns when they are set and when
// there is no root token.
func TestHubServeExistingUserIgnoresOwnerVariables(t *testing.T) {
	t.Run("vars set and no root token", func(t *testing.T) {
		st := newHubFakeStore()
		st.userCount = 1
		st.hasOwner = true
		vars := hubTestServeVars(nil)
		vars[hubEnvOwnerPass] = hubTestOtherPass
		_, _, stop := startHubServe(t, vars, st)
		out, errb, code := stop()
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
		}
		if st.seedCount() != 0 {
			t.Fatalf("the seed ran: %d, want 0", st.seedCount())
		}
		if !strings.Contains(errb, "ignored while a user already exists") {
			t.Fatalf("stderr = %q, want the ignored warning", errb)
		}
		if !strings.Contains(errb, "no way to reset the password") {
			t.Fatalf("stderr = %q, want the no root token warning", errb)
		}
		if strings.Contains(out+errb, hubTestOtherPass) {
			t.Fatalf("the password leaked into the output: %q %q", out, errb)
		}
	})
	t.Run("vars set and root token set", func(t *testing.T) {
		st := newHubFakeStore()
		st.userCount = 1
		st.hasOwner = true
		vars := hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken})
		_, _, stop := startHubServe(t, vars, st)
		out, errb, code := stop()
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
		}
		if !strings.Contains(errb, "ignored while a user already exists") {
			t.Fatalf("stderr = %q, want the ignored warning", errb)
		}
		if strings.Contains(errb, "no way to reset the password") {
			t.Fatalf("stderr = %q, want no no-root-token warning", errb)
		}
	})
	t.Run("vars unset and no root token", func(t *testing.T) {
		st := newHubFakeStore()
		st.userCount = 1
		st.hasOwner = true
		vars := hubTestServeVars(nil)
		delete(vars, hubEnvOwnerName)
		delete(vars, hubEnvOwnerEmail)
		delete(vars, hubEnvOwnerPass)
		_, _, stop := startHubServe(t, vars, st)
		_, errb, code := stop()
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", code, errb)
		}
		if strings.Contains(errb, "ignored while a user already exists") {
			t.Fatalf("stderr = %q, want no ignored warning", errb)
		}
		if !strings.Contains(errb, "no way to reset the password") {
			t.Fatalf("stderr = %q, want the no root token warning", errb)
		}
	})
}

// assertSameResponse requires that the response is the 404 of the
// unknown route: the same status and the same body, byte for byte.
func assertSameResponse(t *testing.T, name string, got, want httpResponse) {
	t.Helper()
	if got.status != want.status || got.body != want.body {
		t.Fatalf("%s = %d %q, want the 404 of the unknown route %d %q", name, got.status, got.body, want.status, want.body)
	}
}

// TestHubServeRootTokenRoute checks the recovery route over the real
// adapter and the real listeners: the public port serves no such route
// at all, with or without the root token, so the right body, a
// forwarding header and the wrong method answer the 404 of an unknown
// route, byte for byte; the recovery port serves the route, and with
// the token set a wrong token is 401, the weak password is 400, the
// missing content type is 415 and the right token with a valid
// password is the 200 with the new session cookie. The wrong token
// leaves one stderr line without the value, and the token never
// reaches the output.
func TestHubServeRootTokenRoute(t *testing.T) {
	st := newHubFakeStore()
	listen, _, stop := startHubServe(t, hubTestServeVars(nil), st)
	unknown := doPost(t, "http://"+listen+"/api/no-such-route", `{}`)
	if unknown.status != 404 || unknown.body != `{"error":"not_found"}` {
		t.Fatalf("the reference 404 = %d %q", unknown.status, unknown.body)
	}
	assertSameResponse(t, "the public port without the token",
		doPost(t, "http://"+listen+"/api/session/root-token", `{"token":"x","new":"whatever-pass-1234"}`), unknown)
	assertSameResponse(t, "the public port with the wrong method",
		doGet(t, "http://"+listen+"/api/session/root-token"), unknown)
	if _, _, code := stop(); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	st2 := newHubFakeStore()
	st2.hasOwner = true
	listen2, recListen2, stop2 := startHubServe(t, hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken}), st2)
	if recListen2 == "" {
		t.Fatal("the recovery listener did not open with the token set")
	}
	// The public port serves no recovery route even with the token
	// set: the right token and body, a forwarding header and the wrong
	// method all answer the 404 of the unknown route, byte for byte.
	unknown2 := doPost(t, "http://"+listen2+"/api/no-such-route", `{}`)
	assertSameResponse(t, "the public port with the token and the right body",
		doPost(t, "http://"+listen2+"/api/session/root-token", `{"token":"`+hubTestRootToken+`","new":"whatever-pass-1234"}`), unknown2)
	fwd, err := http.NewRequest(http.MethodPost, "http://"+listen2+"/api/session/root-token",
		strings.NewReader(`{"token":"`+hubTestRootToken+`","new":"whatever-pass-1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	fwd.Header.Set("Content-Type", "application/json")
	fwd.Header.Set("X-Forwarded-For", "10.0.0.1")
	fwd.Header.Set("Forwarded", "proto=https")
	fwd.Header.Set("X-Forwarded-Proto", "https")
	fwdResp, err := hubProbeClient.Do(fwd)
	if err != nil {
		t.Fatal(err)
	}
	assertListenerPolicy(t, fwdResp.Header)
	fwdBody, _ := io.ReadAll(fwdResp.Body)
	fwdResp.Body.Close()
	assertSameResponse(t, "the public port with a forwarding header",
		httpResponse{status: fwdResp.StatusCode, body: string(fwdBody)}, unknown2)
	assertSameResponse(t, "the public port with the wrong method",
		doGet(t, "http://"+listen2+"/api/session/root-token"), unknown2)
	// The recovery port serves the route: the wrong token (any length)
	// is 401 before the password policy, the weak password is 400 and
	// the missing content type is 415.
	const wrongToken = "wrong-token-000000000000000000000000000000"
	base := "http://" + recListen2
	wrong := doPost(t, base+"/api/session/root-token", `{"token":"`+wrongToken+`","new":"whatever-pass-1234"}`)
	if wrong.status != 401 || wrong.body != `{"error":"invalid_token"}` {
		t.Fatalf("wrong token = %d %q, want 401 invalid_token", wrong.status, wrong.body)
	}
	weak := doPost(t, base+"/api/session/root-token", `{"token":"`+hubTestRootToken+`","new":"short"}`)
	if weak.status != 400 || weak.body != `{"error":"weak_password"}` {
		t.Fatalf("weak password = %d %q, want 400 weak_password", weak.status, weak.body)
	}
	rct, err := http.NewRequest(http.MethodPost, base+"/api/session/root-token", strings.NewReader(`{"token":"`+hubTestRootToken+`","new":"whatever-pass-1234"}`))
	if err != nil {
		t.Fatal(err)
	}
	rctResp, err := hubProbeClient.Do(rct)
	if err != nil {
		t.Fatal(err)
	}
	rct.Body.Close()
	if rctResp.StatusCode != 415 {
		t.Fatalf("no content type = %d, want 415", rctResp.StatusCode)
	}
	rctResp.Body.Close()
	// The reset works on the recovery port: the 200 with the session
	// cookie and the owner in the body.
	const newPass = "recovered-pass-1234"
	reset := doPost(t, base+"/api/session/root-token", `{"token":"`+hubTestRootToken+`","new":"`+newPass+`"}`)
	if reset.status != 200 {
		t.Fatalf("the reset = %d %q, want 200", reset.status, reset.body)
	}
	if reset.headers.Get("Set-Cookie") == "" {
		t.Fatalf("the reset did not set the session cookie: %q", reset.body)
	}
	var resetBody struct {
		User struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(reset.body), &resetBody); err != nil {
		t.Fatalf("the reset body is not JSON: %q", reset.body)
	}
	if resetBody.User.Email != hubTestOwnerEmail || resetBody.User.Role != "owner" {
		t.Fatalf("the reset user = %+v, want the owner", resetBody.User)
	}
	// The clean shutdown closes the recovery listener: the port stops
	// answering.
	out, errb, code := stop2()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
	}
	if conn, err := net.Dial("tcp", recListen2); err == nil {
		conn.Close()
		t.Fatalf("the recovery port still answers after the shutdown")
	}
	if !strings.Contains(errb, "root token") {
		t.Fatalf("stderr = %q, want the wrong-token line", errb)
	}
	for _, secret := range []string{wrongToken, hubTestRootToken, newPass} {
		if strings.Contains(out+errb, secret) {
			t.Fatalf("a secret value leaked into the output: %q %q", out, errb)
		}
	}
}

// TestHubServeRecoveryListenerWithoutToken serves the hub without the
// root token: the serving line has no recovery_listen and nothing
// listens on the recovery port, even when the flag names it.
func TestHubServeRecoveryListenerWithoutToken(t *testing.T) {
	// Pick a free loopback port and free it again: the serve must not
	// bind it without the token.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	recPort := free.Addr().String()
	_ = free.Close()
	st := newHubFakeStore()
	_, recListen, stop := startHubServe(t, hubTestServeVars(nil), st, "--recovery-listen", recPort)
	if recListen != "" {
		t.Fatalf("the recovery listener opened without the token: %q", recListen)
	}
	out, errb, code := stop()
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stdout %q, stderr %q)", code, out, errb)
	}
	if strings.Contains(out, "recovery_listen") {
		t.Fatalf("the serving line carries the recovery port: %q", out)
	}
	if conn, err := net.Dial("tcp", recPort); err == nil {
		conn.Close()
		t.Fatalf("something listens on the recovery port without the token")
	}
}

// TestHubServeRecoveryListenValidation exits 2 with a recovery
// listener that is not a literal loopback host:port, before the store
// is touched: the error names the flag, never the address value.
func TestHubServeRecoveryListenValidation(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.0.2.1:0", "localhost:0", "nota"} {
		t.Run(addr, func(t *testing.T) {
			st := newHubFakeStore()
			stdout, stderr, exit := runHubServe(t, hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken}), st, "--recovery-listen", addr)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", exit, stdout, stderr)
			}
			if !strings.Contains(stderr, "--recovery-listen must be a literal loopback host:port") {
				t.Fatalf("stderr = %q", stderr)
			}
			if st.versionCount() != 0 || st.seedCount() != 0 {
				t.Fatalf("the store was touched: versions=%d seeded=%d", st.versionCount(), st.seedCount())
			}
		})
	}
}

// TestHubServeRecoveryListenInUseExits4 makes the recovery port
// already bound: the serve exits 4 with the cannot-listen line, the
// public listener it had opened is closed, and no serving line goes
// out.
func TestHubServeRecoveryListenInUseExits4(t *testing.T) {
	// Occupy the recovery port.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	recPort := busy.Addr().String()
	st := newHubFakeStore()
	out, errb := &mutexWriter{}, &mutexWriter{}
	env := Env{
		Stdout: out,
		Stderr: errb,
		Getenv: func(k string) string {
			return hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken})[k]
		},
		Now:   func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
		Sleep: sleepCtx,
	}
	var pubLn net.Listener
	oldListen := hubServeListen
	hubServeListen = func(network, address string) (net.Listener, error) {
		if address == recPort {
			return nil, errors.New("listen tcp: address already in use")
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		pubLn = ln
		return ln, nil
	}
	oldStore := newHubStore
	newHubStore = func(baseURL, token string) (store.Store, error) { return st, nil }
	ctx, cancel := context.WithCancel(context.Background())
	oldCtx := hubServeContext
	hubServeContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() {
		cancel()
		hubServeListen = oldListen
		newHubStore = oldStore
		hubServeContext = oldCtx
	})
	done := make(chan int, 1)
	go func() { done <- cmdHubServe([]string{"--recovery-listen", recPort}, env) }()
	select {
	case code := <-done:
		if code != 4 {
			t.Fatalf("exit = %d, want 4 (stdout %q, stderr %q)", code, out.String(), errb.String())
		}
		if !strings.Contains(errb.String(), "cannot listen on "+recPort) {
			t.Fatalf("stderr = %q", errb.String())
		}
		if strings.Contains(out.String(), `"status":"serving"`) {
			t.Fatalf("the serving line went out: %q", out.String())
		}
		// The public listener the serve had opened is closed.
		if conn, err := net.Dial("tcp", pubLn.Addr().String()); err == nil {
			conn.Close()
			t.Fatalf("the public listener is still open")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the serve did not return in time")
	}
}

// Mutation captured: removing TrimSpace or lowering the byte boundary opens the store.
func TestHubServeRootTokenInvalidBeforeEffects(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"31 bytes", strings.Repeat("x", 31)},
		{"32 spaces", strings.Repeat(" ", 32)},
		{"40 whitespace", strings.Repeat(" \t\n ", 10)},
		{"unicode whitespace", strings.Repeat("\u2003", 11)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldListen, oldStore := hubServeListen, newHubStore
			t.Cleanup(func() { hubServeListen = oldListen; newHubStore = oldStore })
			listens, stores := 0, 0
			hubServeListen = func(string, string) (net.Listener, error) { listens++; return nil, errors.New("unexpected listen") }
			newHubStore = func(string, string) (store.Store, error) { stores++; return nil, errors.New("unexpected store") }
			var out, errb bytes.Buffer
			vars := hubTestServeVars(map[string]string{hubEnvRootToken: tc.token})
			exit := Run([]string{"hub", "serve"}, Env{Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return vars[k] }})
			const want = "hub serve: NAU_HUB_ROOT_TOKEN must be at least 32 bytes and not blank"
			var line struct{ Status, Motivo string }
			if err := json.Unmarshal(out.Bytes(), &line); err != nil || exit != 2 || line.Status != "2" || line.Motivo != want || errb.String() != "nau: "+want+"\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), errb.String())
			}
			if listens != 0 || stores != 0 {
				t.Fatalf("effects: listens=%d stores=%d", listens, stores)
			}
			if strings.Contains(out.String()+errb.String(), tc.token) {
				t.Fatal("root token leaked")
			}
		})
	}
}

func TestHubServeRootTokenValidBytes(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"32 bytes", strings.Repeat("x", 32)},
		{"32 unicode bytes", strings.Repeat("é", 16)},
		{"40 bytes internal spaces", strings.Repeat("x", 19) + "  " + strings.Repeat("y", 19)},
		{"edge spaces preserved", " " + strings.Repeat("x", 32) + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newHubFakeStore()
			st.hasOwner = true
			_, rec, stop := startHubServe(t, hubTestServeVars(map[string]string{hubEnvRootToken: tc.token}), st)
			if rec == "" {
				t.Fatal("missing recovery listener")
			}
			body, _ := json.Marshal(map[string]string{"token": tc.token, "new": "recovered-pass-1234"})
			resp := doPost(t, "http://"+rec+"/api/session/root-token", string(body))
			if resp.status != 200 || resp.headers.Get("Set-Cookie") == "" {
				t.Fatalf("exact token recovery = %d", resp.status)
			}
			out, errb, exit := stop()
			if exit != 0 {
				t.Fatalf("exit=%d", exit)
			}
			if strings.Contains(out+errb, tc.token) {
				t.Fatal("root token leaked")
			}
		})
	}
}

func assertListenerPolicy(t *testing.T, h http.Header) {
	t.Helper()
	for name, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
	} {
		if values := h.Values(name); len(values) != 1 || values[0] != want {
			t.Errorf("%s = %q, want one %q", name, values, want)
		}
	}
	for _, name := range []string{"Strict-Transport-Security", "Location"} {
		if _, present := h[name]; present {
			t.Errorf("unexpected %s", name)
		}
	}
}
