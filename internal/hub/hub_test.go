package hub

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
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
	return nil, errors.New("fakeStore: Batch not implemented")
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
