package httpserve

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/store"
)

// countingStore is a store that counts the pings and answers 200.
type countingStore struct {
	pings int
	err   error
}

func (c *countingStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	return store.Result{}, nil
}

func (c *countingStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	return nil, nil
}

func (c *countingStore) Ping(ctx context.Context) error {
	c.pings++
	return c.err
}

// newTestHandler builds the adapter over a core with the counting store.
func newTestHandler(store *countingStore) (*Handler, *hub.Server) {
	s := hub.New(hub.Deps{
		Store:   store,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, nil)
	return New(s), s
}

func TestAPIHeadersOnResponses(t *testing.T) {
	h, _ := newTestHandler(&countingStore{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, path := range []string{"/api/health", "/api/version", "/api/unknown"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s: Content-Type = %q", path, got)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control = %q", path, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q", path, got)
		}
	}
}

func TestWrongMethodThroughAdapter(t *testing.T) {
	h, _ := newTestHandler(&countingStore{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/health", "application/json", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != "GET" {
		t.Fatalf("Allow = %q, want GET", got)
	}
}

func TestBodyOverOneMiBIs413WithoutHandler(t *testing.T) {
	st := &countingStore{}
	h, _ := newTestHandler(st)
	srv := httptest.NewServer(h)
	defer srv.Close()
	body := bytes.Repeat([]byte("a"), maxBody+1)
	resp, err := http.Post(srv.URL+"/api/health", "text/plain", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if st.pings != 0 {
		t.Fatalf("the handler was called %d times, want 0", st.pings)
	}
	// The API headers are set before the early 413 answer.
	for name, want := range map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Fatalf("413: %s = %q, want %q", name, got, want)
		}
	}
	// A non-API path over the limit still answers 413, without the
	// API headers.
	resp, err = http.Post(srv.URL+"/static/file.js", "text/plain", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("static status = %d, want 413", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "" {
		t.Fatalf("static 413: Cache-Control = %q, want none", got)
	}
}

func TestBodyAtOneMiBPasses(t *testing.T) {
	st := &countingStore{}
	s := hub.New(hub.Deps{
		Store:   st,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, nil)
	// A POST handler that counts, so the 1 MiB body is routed to a
	// handler and not answered 405 by the router.
	s.Handle("POST", "/api/health", func(ctx context.Context, req hub.Request) hub.Response {
		return hub.Response{Status: 200, Body: []byte(`{"ok":true}`)}
	})
	h := New(s)
	srv := httptest.NewServer(h)
	defer srv.Close()
	body := bytes.Repeat([]byte("a"), maxBody)
	req, _ := http.NewRequest("POST", srv.URL+"/api/health", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if st.pings != 0 {
		t.Fatalf("the fake store was used %d times, want the custom handler", st.pings)
	}
}

func TestPercentEncodedTraversalIs404(t *testing.T) {
	h, _ := newTestHandler(&countingStore{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, path := range []string{"/%2e%2e", "/%2e%2e/%2e%2e/secret.txt", "/a/..%2f..%2fsecret.txt"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, resp.StatusCode)
		}
		if len(b) != 0 {
			t.Fatalf("%s: body = %q, want empty", path, b)
		}
	}
}

func TestDotSlashTraversalIs404(t *testing.T) {
	h, _ := newTestHandler(&countingStore{})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/a/../../../etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(b) != 0 {
		t.Fatalf("body = %q, want empty", b)
	}
}

func TestRequestHeadersReachTheHandler(t *testing.T) {
	st := &countingStore{}
	s := hub.New(hub.Deps{
		Store:   st,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, nil)
	var got string
	s.Handle("GET", "/api/echo", func(ctx context.Context, req hub.Request) hub.Response {
		got = req.Header.Get("X-Test-Header")
		return hub.Response{Status: 200}
	})
	h := New(s)
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/echo", nil)
	req.Header.Set("X-Test-Header", "value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got != "value" {
		t.Fatalf("the handler saw %q, want value", got)
	}
}

func TestStaticContentTypes(t *testing.T) {
	st := &countingStore{}
	s := hub.New(hub.Deps{
		Store:   st,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, staticDirFS(t))
	h := New(s)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	// The API headers must not be added on the static space.
	if got := resp.Header.Get("Cache-Control"); got == "no-store" {
		t.Fatalf("Cache-Control = %q on a static response, want absent", got)
	}
}

// TestIngestRouteBodyLimit: the ingest route, POST /api/ingest on the
// exact path, takes the parser's document limit, and one byte over it
// answers 413 before the handler. Every other method and path keeps
// the default limit: one byte over it is 413 before the handler, and
// the exact default limit passes. The limits are written as literals,
// not the implementation's constants. The accepted bodies reach the
// handler intact, and every answer keeps the API headers.
func TestIngestRouteBodyLimit(t *testing.T) {
	// The parser's limit and the default limit, as independent
	// literals of the test.
	const ingestLimit = 4 * 1024 * 1024
	const defaultLimit = 1024 * 1024

	st := &countingStore{}
	s := hub.New(hub.Deps{
		Store:   st,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, nil)
	type capture struct {
		calls int
		body  []byte
	}
	caps := map[string]*capture{}
	for _, p := range []string{"/api/ingest", "/api/ingest/", "/api/ingest-extra"} {
		c := &capture{}
		caps[p] = c
		s.Handle("POST", p, func(ctx context.Context, req hub.Request) hub.Response {
			c.calls++
			c.body = req.Body
			return hub.Response{Status: 200, Body: []byte(`{"ok":true}`)}
		})
	}
	h := New(s)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// A client with a finite timeout: a regression that leaves the
	// body unread must not hang the proof without its own bound.
	client := &http.Client{Timeout: 30 * time.Second}
	do := func(method, path string, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s %s: build the request: %v", method, path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: the request: %v", method, path, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	// A valid inventory document, small.
	doc := []byte(`{"schema":1,"soho_version":"v0.3.1","generated_ms":1793376000000,"projects":[{"name":"acme/web","sessions":[{"id":"w1","agents":[{"terminal":"w1:p1","name":"agent-1","role":"implementer","kind":"cli-b","state":"working","report_ready":false,"started_ms":1793372400000}]}]}],"hidden":{"working":0,"needs_you":0,"done":0,"idle":0,"unknown":0}}`)
	// The same document padded with whitespace to exactly the parser's
	// limit, the newline at the last byte: valid JSON at the boundary.
	var sb strings.Builder
	sb.Write(doc)
	sb.WriteString(strings.Repeat(" ", ingestLimit-len(doc)-1))
	sb.WriteByte('\n')
	exact := []byte(sb.String())
	if len(exact) != ingestLimit {
		t.Fatalf("padded document = %d bytes, want %d", len(exact), ingestLimit)
	}

	// A small valid document reaches the ingest handler intact.
	resp := do("POST", "/api/ingest", doc)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("small: status = %d, want 200", resp.StatusCode)
	}
	if c := caps["/api/ingest"]; c.calls != 1 || !bytes.Equal(c.body, doc) {
		t.Fatalf("small: the handler calls = %d and the body intact = %v, want 1 and true", c.calls, bytes.Equal(c.body, doc))
	}

	// The valid document of exactly the parser's limit, with the
	// newline near the end, is accepted and reaches the handler intact.
	resp = do("POST", "/api/ingest", exact)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d MiB: status = %d, want 200", ingestLimit>>20, resp.StatusCode)
	}
	if c := caps["/api/ingest"]; c.calls != 2 || !bytes.Equal(c.body, exact) {
		t.Fatalf("%d MiB: the handler calls = %d and the body intact = %v, want 2 and true", ingestLimit>>20, c.calls, bytes.Equal(c.body, exact))
	}

	// One byte over the parser's limit is refused before the handler,
	// with the API headers on the answer.
	resp = do("POST", "/api/ingest", append(append([]byte(nil), exact...), ' '))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("%d MiB + 1: status = %d, want 413", ingestLimit>>20, resp.StatusCode)
	}
	if c := caps["/api/ingest"]; c.calls != 2 {
		t.Fatalf("%d MiB + 1: the handler calls = %d, want 2 (the handler never called)", ingestLimit>>20, c.calls)
	}
	for name, want := range map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Fatalf("%d MiB + 1: %s = %q, want %q", ingestLimit>>20, name, got, want)
		}
	}

	// Every other method and path keeps the default limit: one byte
	// over it is refused before the handler, the existing endpoint
	// included.
	over := bytes.Repeat([]byte("a"), defaultLimit+1)
	resp = do("GET", "/api/ingest", over)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("GET /api/ingest %d MiB + 1: status = %d, want 413", defaultLimit>>20, resp.StatusCode)
	}
	resp = do("POST", "/api/ingest/", over)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/ingest/ %d MiB + 1: status = %d, want 413", defaultLimit>>20, resp.StatusCode)
	}
	if c := caps["/api/ingest/"]; c.calls != 0 {
		t.Fatalf("POST /api/ingest/ %d MiB + 1: the handler calls = %d, want 0", defaultLimit>>20, c.calls)
	}
	resp = do("POST", "/api/ingest-extra", over)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/ingest-extra %d MiB + 1: status = %d, want 413", defaultLimit>>20, resp.StatusCode)
	}
	if c := caps["/api/ingest-extra"]; c.calls != 0 {
		t.Fatalf("POST /api/ingest-extra %d MiB + 1: the handler calls = %d, want 0", defaultLimit>>20, c.calls)
	}
	resp = do("POST", "/api/health", over)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/health %d MiB + 1: status = %d, want 413", defaultLimit>>20, resp.StatusCode)
	}
	if st.pings != 0 {
		t.Fatalf("POST /api/health %d MiB + 1: the handler pings = %d, want 0", defaultLimit>>20, st.pings)
	}

	// The exact default limit passes on the other paths and reaches
	// the handler intact.
	atDefault := bytes.Repeat([]byte("a"), defaultLimit)
	resp = do("POST", "/api/ingest/", atDefault)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/ingest/ %d MiB: status = %d, want 200", defaultLimit>>20, resp.StatusCode)
	}
	if c := caps["/api/ingest/"]; c.calls != 1 || !bytes.Equal(c.body, atDefault) {
		t.Fatalf("POST /api/ingest/ %d MiB: the handler calls = %d and the body intact = %v, want 1 and true", defaultLimit>>20, c.calls, bytes.Equal(c.body, atDefault))
	}
}

// staticDirFS is the static fixture of the adapter tests.
func staticDirFS(t *testing.T) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"index.html": {Data: []byte("<html>hub</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
}

func TestNewServerTimeouts(t *testing.T) {
	srv := NewServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %s, want 30s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 30*time.Second {
		t.Fatalf("WriteTimeout = %s, want 30s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %s, want 120s", srv.IdleTimeout)
	}
}

func TestStaticFallbackThroughAdapter(t *testing.T) {
	st := &countingStore{}
	s := hub.New(hub.Deps{
		Store:   st,
		Now:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Version: "v1",
	}, staticDirFS(t))
	h := New(s)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/some/spa/route")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(b) != "<html>hub</html>" {
		t.Fatalf("body = %q", b)
	}
}
