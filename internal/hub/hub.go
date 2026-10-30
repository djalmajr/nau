// Package hub is the core of the hub server. The handlers are pure
// functions of a context and a request: no in-memory state between
// requests, no cgo. The dependencies of the core come from a Deps struct
// assembled at startup (the store, the clock and the binary version): it
// is configuration, never cache or state.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// apiHealthPath is the health endpoint.
const apiHealthPath = "/api/health"

// apiVersionPath is the version endpoint.
const apiVersionPath = "/api/version"

// healthTimeout bounds the store ping of the health endpoint.
var healthTimeout = 2 * time.Second

// notFoundBody is the body of the API 404 responses.
const notFoundBody = `{"error":"not_found"}`

// Request is one incoming request, fully read.
type Request struct {
	// Method is the HTTP method, as received.
	Method string
	// Path is the origin-form request path, without the query.
	Path string
	// Query is the decoded query parameters.
	Query url.Values
	// Header carries the request headers.
	Header Header
	// Body is the request body, in bytes (bounded by the adapter).
	Body []byte
}

// Response is one outgoing response.
type Response struct {
	// Status is the HTTP status code.
	Status int
	// Header carries the response headers to write.
	Header Header
	// Body is the response body.
	Body []byte
}

// Header is a set of HTTP headers: lower-cased name to values.
type Header map[string][]string

// Set sets the header name to value, replacing any current values.
func (h Header) Set(name, value string) {
	if h == nil {
		return
	}
	h[strings.ToLower(name)] = []string{value}
}

// Get returns the first value of the header name, or "".
func (h Header) Get(name string) string {
	if v := h[strings.ToLower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Handler is one hub handler: a pure function of the context and the
// request.
type Handler func(ctx context.Context, req Request) Response

// Deps is the configuration of the core, assembled at startup: the
// store, the clock and the binary version.
type Deps struct {
	// Store is the hub storage.
	Store store.Store
	// Now is the clock.
	Now func() time.Time
	// Version is the version of the binary, the same value the
	// `nau version` command prints.
	Version string
}

// Server routes the requests to the handlers by exact method and path,
// serves the API endpoints (health and version) and the static files,
// and answers 405 with Allow or 404 not_found where no handler fits.
type Server struct {
	deps   Deps
	paths  map[string]map[string]Handler
	static fs.FS
}

// New builds the server with the standard API routes registered. static
// is the filesystem of the static files outside /api/, or nil to refuse
// every path outside /api/ with 404.
func New(deps Deps, static fs.FS) *Server {
	s := &Server{
		deps:   deps,
		paths:  make(map[string]map[string]Handler),
		static: static,
	}
	s.Handle("GET", apiHealthPath, s.health)
	s.Handle("GET", apiVersionPath, s.version)
	return s
}

// Handle registers h for the exact method and path.
func (s *Server) Handle(method, path string, h Handler) {
	if s.paths[path] == nil {
		s.paths[path] = make(map[string]Handler)
	}
	s.paths[path][method] = h
}

// Serve routes one request and returns the response. It reads nothing
// and writes nothing: the handler (or the static fallback) produces the
// whole response.
func (s *Server) Serve(ctx context.Context, req Request) Response {
	if s.isAPI(req.Path) {
		return s.api(ctx, req)
	}
	return s.staticFile(req)
}

// isAPI reports whether the path is in the API space: /api itself or
// anything below /api/.
func (s *Server) isAPI(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// api routes the requests of the API space to the exact handlers.
func (s *Server) api(ctx context.Context, req Request) Response {
	methods, ok := s.paths[req.Path]
	if !ok {
		return Response{Status: 404, Body: []byte(notFoundBody)}
	}
	h, ok := methods[req.Method]
	if !ok {
		allow := make([]string, 0, len(methods))
		for m := range methods {
			allow = append(allow, m)
		}
		sort.Strings(allow)
		resp := Response{Status: 405, Header: make(Header)}
		resp.Header.Set("Allow", strings.Join(allow, ", "))
		return resp
	}
	return h(ctx, req)
}

// staticFile serves the path from the static filesystem outside /api/:
// the named file when it exists, the index.html fallback when it does
// not (the SPA routing), and 404 when there is no filesystem or the path
// is not a valid one. A name the filesystem refuses for any reason
// other than absence (a symbolic link that escapes the root, an
// unreadable file) is not a SPA route: it answers 404, never the
// fallback.
func (s *Server) staticFile(req Request) Response {
	if s.static == nil {
		return Response{Status: 404}
	}
	p := strings.TrimPrefix(req.Path, "/")
	// The root path has no file name of its own: it takes the SPA
	// fallback directly.
	if p == "" {
		if r, ok, _ := s.readStatic("index.html"); ok {
			return r
		}
		return Response{Status: 404}
	}
	// Refuse every path the filesystem would not accept after dropping
	// the leading slash: dot segments, empty elements, absolute paths.
	if !fs.ValidPath(p) {
		return Response{Status: 404}
	}
	if r, ok, absent := s.readStatic(p); ok {
		return r
	} else if !absent {
		return Response{Status: 404}
	}
	if r, ok, _ := s.readStatic("index.html"); ok {
		return r
	}
	return Response{Status: 404}
}

// readStatic reads one file of the static filesystem and builds its
// response. It reports whether the file was served and, when it was
// not, whether the cause was absence (ErrNotExist: the caller may fall
// back to the SPA index) or some other refusal (a symbolic link that
// escapes the root, a directory or an unreadable file: the caller
// answers 404).
func (s *Server) readStatic(name string) (Response, bool, bool) {
	f, err := s.static.Open(name)
	if err != nil {
		return Response{}, false, errors.Is(err, fs.ErrNotExist)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Response{}, false, false
	}
	if st.IsDir() {
		return Response{}, false, true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return Response{}, false, false
	}
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := make(Header)
	h.Set("Content-Type", ct)
	return Response{Status: 200, Header: h, Body: data}, true, false
}

// health answers 200 {"ok":true} when the store ping passes within the
// bound, and 503 {"ok":false} otherwise. The database error never goes
// to the response.
func (s *Server) health(ctx context.Context, req Request) Response {
	pingCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if err := s.deps.Store.Ping(pingCtx); err != nil {
		return Response{Status: 503, Body: []byte(`{"ok":false}`)}
	}
	return Response{Status: 200, Body: []byte(`{"ok":true}`)}
}

// version answers 200 with the binary version and the highest applied
// migration. A store failure answers 503 without the error.
func (s *Server) version(ctx context.Context, req Request) Response {
	res, err := s.deps.Store.Execute(ctx, store.Stmt{SQL: `SELECT MAX(version) FROM schema_migrations`})
	if err != nil {
		return Response{Status: 503, Body: []byte(`{"error":"unavailable"}`)}
	}
	var schema int64
	if len(res.Rows) > 0 && len(res.Rows[0]) > 0 {
		if v, ok := res.Rows[0][0].(int64); ok {
			schema = v
		}
	}
	b, _ := json.Marshal(struct {
		Version string `json:"version"`
		Schema  int64  `json:"schema"`
	}{Version: s.deps.Version, Schema: schema})
	return Response{Status: 200, Body: b}
}
