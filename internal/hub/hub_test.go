package hub

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// fakeStore is an in-memory store for the core tests.
type fakeStore struct {
	pingErr     error
	pingWaitCtx bool // block until the context is done
	executeErr  error
	schemaValue store.Value // the value of the MAX(version) row; nil means NULL
	schemaErr   error
	executedSQL []string
	pingCalls   int
	// retainErr fails the retention batch; retainCutoffs records the
	// cutoffs of the retention batch, in statement order.
	retainErr     error
	retainCutoffs []store.Value
}

func (f *fakeStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	f.executedSQL = append(f.executedSQL, stmt.SQL)
	if f.executeErr != nil {
		return store.Result{}, f.executeErr
	}
	if stmt.SQL == `SELECT MAX(version) FROM schema_migrations` {
		if f.schemaErr != nil {
			return store.Result{}, f.schemaErr
		}
		return store.Result{Cols: []string{"MAX(version)"}, Rows: []store.Row{{f.schemaValue}}}, nil
	}
	return store.Result{}, errors.New("fakeStore: unexpected statement " + stmt.SQL)
}

func (f *fakeStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	if f.retainErr != nil {
		return nil, f.retainErr
	}
	var results []store.Result
	for i, s := range stmts {
		switch {
		case strings.HasPrefix(s.SQL, "DELETE FROM agent_history WHERE at_ms <"),
			strings.HasPrefix(s.SQL, "DELETE FROM working_hourly WHERE hour_ms <"):
			f.retainCutoffs = append(f.retainCutoffs, s.Args...)
		default:
			// The statement the store does not model: a statement
			// failure with its number, like the real store reports.
			return nil, &store.StatementError{Index: i + 1}
		}
		results = append(results, store.Result{Affected: 1})
	}
	return results, nil
}

func (f *fakeStore) Ping(ctx context.Context) error {
	f.pingCalls++
	if f.pingWaitCtx {
		<-ctx.Done()
	}
	return f.pingErr
}

func newTestServer(t *testing.T, fsStore *fakeStore, static fs.FS) *Server {
	t.Helper()
	return New(Deps{
		Store:   fsStore,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1.2.3",
	}, static)
}

func TestHealthOK(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/health"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestHealthDown(t *testing.T) {
	st := &fakeStore{pingErr: errors.New("connection refused to 127.0.0.1")}
	s := newTestServer(t, st, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/health"})
	if resp.Status != 503 {
		t.Fatalf("status = %d, want 503", resp.Status)
	}
	if string(resp.Body) != `{"ok":false}` {
		t.Fatalf("body = %q", resp.Body)
	}
	if bytes.Contains(resp.Body, []byte("connection refused")) {
		t.Fatalf("the database error leaked into the response")
	}
	if st.pingCalls != 1 {
		t.Fatalf("ping calls = %d, want 1", st.pingCalls)
	}
}

func TestHealthBoundedByTwoSeconds(t *testing.T) {
	if healthTimeout != 2*time.Second {
		t.Fatalf("healthTimeout = %s, want 2s", healthTimeout)
	}
	old := healthTimeout
	healthTimeout = 50 * time.Millisecond
	defer func() { healthTimeout = old }()
	st := &fakeStore{pingWaitCtx: true, pingErr: errors.New("ping deadline")}
	s := newTestServer(t, st, nil)
	start := time.Now()
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/health"})
	if time.Since(start) > time.Second {
		t.Fatalf("the health endpoint was not bounded: took %s", time.Since(start))
	}
	if resp.Status != 503 {
		t.Fatalf("status = %d, want 503", resp.Status)
	}
}

func TestVersionOK(t *testing.T) {
	st := &fakeStore{schemaValue: int64(7)}
	s := newTestServer(t, st, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/version"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"version":"v1.2.3","schema":7}` {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestVersionNoSchema(t *testing.T) {
	// A NULL highest version (no applied migration row) reports 0.
	st := &fakeStore{schemaValue: nil}
	s := newTestServer(t, st, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/version"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"version":"v1.2.3","schema":0}` {
		t.Fatalf("body = %q", resp.Body)
	}
}

func TestVersionStoreFailure(t *testing.T) {
	st := &fakeStore{schemaErr: errors.New("database unavailable")}
	s := newTestServer(t, st, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/version"})
	if resp.Status != 503 {
		t.Fatalf("status = %d, want 503", resp.Status)
	}
	if string(resp.Body) != `{"error":"unavailable"}` {
		t.Fatalf("body = %q", resp.Body)
	}
	if bytes.Contains(resp.Body, []byte("database unavailable")) {
		t.Fatalf("the database error leaked into the response")
	}
}

func TestWrongMethodAnswers405WithAllow(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	resp := s.Serve(context.Background(), Request{Method: "POST", Path: "/api/health"})
	if resp.Status != 405 {
		t.Fatalf("status = %d, want 405", resp.Status)
	}
	if got := resp.Header.Get("Allow"); got != "GET" {
		t.Fatalf("Allow = %q, want GET", got)
	}
	if len(resp.Body) != 0 {
		t.Fatalf("body = %q, want empty", resp.Body)
	}
}

func TestUnknownAPIPathAnswers404(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	for _, path := range []string{"/api/nope", "/api", "/api/health/extra", "/api/v2/health"} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: path})
		if resp.Status != 404 {
			t.Fatalf("%s: status = %d, want 404", path, resp.Status)
		}
		if string(resp.Body) != `{"error":"not_found"}` {
			t.Fatalf("%s: body = %q", path, resp.Body)
		}
	}
}

func TestQueryDoesNotChangeRouting(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/health", Query: map[string][]string{"x": {"1"}}})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
}

func TestMethodMatchingIsExact(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	resp := s.Serve(context.Background(), Request{Method: "get", Path: "/api/health"})
	if resp.Status != 405 {
		t.Fatalf("status = %d, want 405 for the lowercase method", resp.Status)
	}
}

func TestCustomRoute(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	s.Handle("GET", "/api/extra", func(ctx context.Context, req Request) Response {
		return Response{Status: 200, Body: []byte(`{"extra":true}`)}
	})
	if resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/extra"}); resp.Status != 200 {
		t.Fatalf("GET /api/extra = %d, want 200", resp.Status)
	}
	if resp := s.Serve(context.Background(), Request{Method: "DELETE", Path: "/api/extra"}); resp.Status != 405 {
		t.Fatalf("DELETE /api/extra = %d, want 405", resp.Status)
	}
}

var staticFS = fstestMapFS()

// fstestMapFS builds the fixture of the static tests.
func fstestMapFS() fs.FS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<html>hub</html>")},
		"app.js":            {Data: []byte("console.log(1)")},
		"assets/style.css":  {Data: []byte("body{}")},
		"secrets/inner.txt": {Data: []byte("inner")},
	}
}

func TestStaticRootServesIndex(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, staticFS)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != "<html>hub</html>" {
		t.Fatalf("body = %q", resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestStaticFile(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, staticFS)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/assets/style.css"})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != "body{}" {
		t.Fatalf("body = %q", resp.Body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestStaticFallbackToIndex(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, staticFS)
	for _, path := range []string{"/nope", "/deep/route/here", "/index.html"} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: path})
		if resp.Status != 200 {
			t.Fatalf("%s: status = %d, want 200", path, resp.Status)
		}
		if string(resp.Body) != "<html>hub</html>" {
			t.Fatalf("%s: body = %q, want the index fallback", path, resp.Body)
		}
	}
}

func TestStaticRejectsTraversal(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, staticFS)
	// Every hostile form of a traversal must answer 404, never a file.
	for _, path := range []string{
		"/../secrets/inner.txt",
		"/../etc/passwd",
		"/secrets/../../secrets/inner.txt",
		"/..",
		"/../..",
		"/a/../../b",
		"/a//b",
	} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: path})
		if resp.Status != 404 {
			t.Fatalf("%s: status = %d, want 404", path, resp.Status)
		}
		if string(resp.Body) == "inner" || string(resp.Body) == "<html>hub</html>" {
			t.Fatalf("%s: the response carried a file body %q", path, resp.Body)
		}
	}
}

func TestStaticNoFSIs404(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	for _, path := range []string{"/", "/index.html", "/a"} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: path})
		if resp.Status != 404 {
			t.Fatalf("%s: status = %d, want 404 without a filesystem", path, resp.Status)
		}
	}
}

// TestStaticSymlinkContainment: a filesystem confined to its root (the
// (*os.Root).FS() the command hands the core for --web) must not serve
// a file a symbolic link points to outside the root, not even through
// the SPA fallback; a link that stays inside the root keeps being
// served.
func TestStaticSymlinkContainment(t *testing.T) {
	dir := t.TempDir()
	web := filepath.Join(dir, "web")
	if err := os.Mkdir(web, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<html>hub</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "deep.txt"), []byte("deep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, l := range []struct{ name, target string }{
		{"link.txt", outside},
		{"inlink.txt", "inside.txt"},
		{"linkdir", sub},
	} {
		if err := os.Symlink(l.target, filepath.Join(web, l.name)); err != nil {
			t.Skipf("cannot create symbolic links here: %v", err)
		}
	}
	root, err := os.OpenRoot(web)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	s := newTestServer(t, &fakeStore{}, root.FS())
	// A link that escapes the root answers 404, even with an index
	// available: it is a refusal, not a missing SPA route.
	for _, path := range []string{"/link.txt", "/linkdir", "/linkdir/deep.txt"} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: path})
		if resp.Status != 404 {
			t.Fatalf("%s: status = %d, want 404 (body %q)", path, resp.Status, resp.Body)
		}
	}
	// A link that stays inside the root keeps being served.
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/inlink.txt"})
	if resp.Status != 200 || string(resp.Body) != "inside" {
		t.Fatalf("/inlink.txt: status = %d body = %q, want 200 and the inside content", resp.Status, resp.Body)
	}
	// Controls: the plain file and the SPA fallback are unchanged.
	resp = s.Serve(context.Background(), Request{Method: "GET", Path: "/inside.txt"})
	if resp.Status != 200 || string(resp.Body) != "inside" {
		t.Fatalf("/inside.txt: status = %d, want 200", resp.Status)
	}
	resp = s.Serve(context.Background(), Request{Method: "GET", Path: "/spa/route"})
	if resp.Status != 200 || string(resp.Body) != "<html>hub</html>" {
		t.Fatalf("/spa/route: status = %d, want the index fallback", resp.Status)
	}
}

func TestStaticTrailingSlashIs404(t *testing.T) {
	// A trailing slash leaves an invalid path element; the rule of the
	// brief is to refuse with 404, not to fall back to the index.
	s := newTestServer(t, &fakeStore{}, staticFS)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/assets/"})
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestStaticDotSegmentIsAcceptedByValidPath(t *testing.T) {
	// "." is a path ValidPath accepts; the file is absent, so the SPA
	// fallback serves the index. The refusal rule covers only the
	// paths ValidPath rejects.
	s := newTestServer(t, &fakeStore{}, staticFS)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/."})
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != "<html>hub</html>" {
		t.Fatalf("body = %q", resp.Body)
	}
}

// newCronTestServer builds the server of the cron route tests, with the
// same fixed clock as newTestServer.
func newCronTestServer(t *testing.T, st *fakeStore, token string) *Server {
	t.Helper()
	return New(Deps{
		Store:     st,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version:   "v1.2.3",
		CronToken: token,
	}, nil)
}

// TestCronRetentionRouteAbsentWithoutToken: without the token the route
// is not registered and answers 404.
func TestCronRetentionRouteAbsentWithoutToken(t *testing.T) {
	s := newCronTestServer(t, &fakeStore{}, "")
	resp := s.Serve(context.Background(), Request{Method: "POST", Path: apiCronRetentionPath})
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
	if string(resp.Body) != `{"error":"not_found"}` {
		t.Fatalf("body = %q", resp.Body)
	}
}

// TestCronRetentionWrongToken: the missing or wrong token answers 401
// and never runs the retention.
func TestCronRetentionWrongToken(t *testing.T) {
	st := &fakeStore{}
	s := newCronTestServer(t, st, "tok-retention-1")
	for _, header := range []string{"", "Bearer wrong", "bearer tok-retention-1", "Bearer ", "Bearer tok-retention-1 extra"} {
		req := Request{Method: "POST", Path: apiCronRetentionPath, Header: make(Header)}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp := s.Serve(context.Background(), req)
		if resp.Status != 401 {
			t.Fatalf("%q: status = %d, want 401", header, resp.Status)
		}
		if string(resp.Body) != `{"error":"unauthorized"}` {
			t.Fatalf("%q: body = %q", header, resp.Body)
		}
	}
	if len(st.retainCutoffs) != 0 {
		t.Fatalf("the 401s ran the retention: %v", st.retainCutoffs)
	}
}

// TestCronRetentionOK: the right token runs the retention with the
// current time and answers 204 with an empty body.
func TestCronRetentionOK(t *testing.T) {
	st := &fakeStore{}
	s := newCronTestServer(t, st, "tok-retention-1")
	req := Request{Method: "POST", Path: apiCronRetentionPath, Header: make(Header)}
	req.Header.Set("Authorization", "Bearer tok-retention-1")
	resp := s.Serve(context.Background(), req)
	if resp.Status != 204 {
		t.Fatalf("status = %d, want 204", resp.Status)
	}
	if len(resp.Body) != 0 {
		t.Fatalf("body = %q, want empty", resp.Body)
	}
	now := int64(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli())
	want := []store.Value{now - 7*24*3600*1000, now - 48*3600*1000}
	if len(st.retainCutoffs) != 2 || st.retainCutoffs[0] != want[0] || st.retainCutoffs[1] != want[1] {
		t.Fatalf("cutoffs = %v, want %v", st.retainCutoffs, want)
	}
}

// TestCronRetentionStoreError: the store failure answers 503 without
// the error detail.
func TestCronRetentionStoreError(t *testing.T) {
	st := &fakeStore{retainErr: errors.New("database unavailable")}
	s := newCronTestServer(t, st, "tok-retention-1")
	req := Request{Method: "POST", Path: apiCronRetentionPath, Header: make(Header)}
	req.Header.Set("Authorization", "Bearer tok-retention-1")
	resp := s.Serve(context.Background(), req)
	if resp.Status != 503 {
		t.Fatalf("status = %d, want 503", resp.Status)
	}
	if string(resp.Body) != `{"error":"unavailable"}` {
		t.Fatalf("body = %q", resp.Body)
	}
	if bytes.Contains(resp.Body, []byte("database unavailable")) {
		t.Fatalf("the database error leaked into the response")
	}
}

// TestCronRetentionWrongMethod: the route answers only POST.
func TestCronRetentionWrongMethod(t *testing.T) {
	s := newCronTestServer(t, &fakeStore{}, "tok-retention-1")
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: apiCronRetentionPath})
	if resp.Status != 405 {
		t.Fatalf("status = %d, want 405", resp.Status)
	}
	if got := resp.Header.Get("Allow"); got != "POST" {
		t.Fatalf("Allow = %q, want POST", got)
	}
}

// paramServer builds a server with one literal and one parameterized
// route on the same shape, for the named-segment tests.
func paramServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t, &fakeStore{}, nil)
	var got, method string
	s.Handle("GET", "/api/things/{id}", func(ctx context.Context, req Request) Response {
		got = req.PathParam("id")
		method = req.Method
		return Response{Status: 200, Body: []byte("got:" + got)}
	})
	s.Handle("DELETE", "/api/things/{id}", func(ctx context.Context, req Request) Response {
		got = req.PathParam("id")
		return Response{Status: 204}
	})
	s.Handle("GET", "/api/things", func(ctx context.Context, req Request) Response {
		return Response{Status: 200, Body: []byte("list")}
	})
	_ = method
	return s
}

func TestParamRouteMatchesOneSegment(t *testing.T) {
	s := paramServer(t)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/things/42"})
	if resp.Status != 200 || string(resp.Body) != "got:42" {
		t.Fatalf("resp = %d %q", resp.Status, resp.Body)
	}
	resp = s.Serve(context.Background(), Request{Method: "DELETE", Path: "/api/things/9"})
	if resp.Status != 204 {
		t.Fatalf("status = %d, want 204", resp.Status)
	}
}

func TestParamRouteDoesNotCrossSegments(t *testing.T) {
	s := paramServer(t)
	for _, p := range []string{"/api/things/42/extra", "/api/things", "/api/other/42"} {
		resp := s.Serve(context.Background(), Request{Method: "GET", Path: p})
		if p == "/api/things" {
			if resp.Status != 200 || string(resp.Body) != "list" {
				t.Fatalf("%s: %d %q, want the literal list", p, resp.Status, resp.Body)
			}
			continue
		}
		if resp.Status != 404 || string(resp.Body) != `{"error":"not_found"}` {
			t.Fatalf("%s: %d %q, want 404", p, resp.Status, resp.Body)
		}
	}
}

func TestParamRouteTrailingSlashIsNotTheRoute(t *testing.T) {
	s := paramServer(t)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/things/42/"})
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestParamRouteEmptySegmentMatches(t *testing.T) {
	// A parameter matches any segment, including the empty one; the
	// hostile input reaches the handler as an empty value and the
	// resource is not found there.
	s := newTestServer(t, &fakeStore{}, nil)
	var got string
	s.Handle("GET", "/api/x/{id}", func(ctx context.Context, req Request) Response {
		got = req.PathParam("id")
		return Response{Status: 200, Body: []byte("x")}
	})
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/x/"})
	if resp.Status != 200 || got != "" {
		t.Fatalf("status = %d, got = %q, want the empty parameter", resp.Status, got)
	}
}

func TestParamRoute405CarriesTheAllowOfTheShape(t *testing.T) {
	s := paramServer(t)
	resp := s.Serve(context.Background(), Request{Method: "PATCH", Path: "/api/things/7"})
	if resp.Status != 405 {
		t.Fatalf("status = %d, want 405", resp.Status)
	}
	if allow := resp.Header.Get("Allow"); allow != "DELETE, GET" {
		t.Fatalf("Allow = %q", allow)
	}
	// The literal path and the parameterized shape share nothing: the
	// literal shape answers its own 405.
	resp = s.Serve(context.Background(), Request{Method: "POST", Path: "/api/things"})
	if resp.Status != 405 || resp.Header.Get("Allow") != "GET" {
		t.Fatalf("status = %d, Allow = %q", resp.Status, resp.Header.Get("Allow"))
	}
}

func TestLiteralRouteWinsOverParamRoute(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	s.Handle("GET", "/api/invites/{id}", func(ctx context.Context, req Request) Response {
		return Response{Status: 200, Body: []byte("param")}
	})
	s.Handle("GET", "/api/invites/accept", func(ctx context.Context, req Request) Response {
		return Response{Status: 200, Body: []byte("literal")}
	})
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/invites/accept"})
	if resp.Status != 200 || string(resp.Body) != "literal" {
		t.Fatalf("resp = %d %q, want the literal route", resp.Status, resp.Body)
	}
	resp = s.Serve(context.Background(), Request{Method: "GET", Path: "/api/invites/zzz"})
	if resp.Status != 200 || string(resp.Body) != "param" {
		t.Fatalf("resp = %d %q, want the param route", resp.Status, resp.Body)
	}
}

func TestMalformedParamPatternRegistersNothing(t *testing.T) {
	s := newTestServer(t, &fakeStore{}, nil)
	s.Handle("GET", "/api/bad/{}/x", func(ctx context.Context, req Request) Response {
		return Response{Status: 200}
	})
	s.Handle("GET", "/api/bad/{a b}", func(ctx context.Context, req Request) Response {
		return Response{Status: 200}
	})
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/bad/1/2"})
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestQueryDoesNotChangeParamRouting(t *testing.T) {
	s := paramServer(t)
	resp := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/things/42"})
	other := s.Serve(context.Background(), Request{Method: "GET", Path: "/api/things/42", Query: map[string][]string{"x": {"1"}}})
	if string(resp.Body) != string(other.Body) || other.Status != 200 {
		t.Fatalf("query changed the routing: %d %q vs %d %q", other.Status, other.Body, resp.Status, resp.Body)
	}
}
