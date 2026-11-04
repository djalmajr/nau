// Package hub is the core of the hub server. The handlers are pure
// functions of a context and a request: no in-memory state between
// requests, no cgo. The dependencies of the core come from a Deps struct
// assembled at startup (the store, the clock and the binary version): it
// is configuration, never cache or state.
package hub

import (
	"context"
	"crypto/subtle"
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

	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
)

// apiHealthPath is the health endpoint.
const apiHealthPath = "/api/health"

// apiCronRetentionPath is the retention route of the cron job.
const apiCronRetentionPath = "/api/cron/retention"

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
	// PathParams carries the values of the named path segments of the
	// route the request matched, when the route has any.
	PathParams map[string]string
}

// PathParam returns the value of the named path segment of the matched
// route, or the empty string when the route has no such segment.
func (r Request) PathParam(name string) string {
	return r.PathParams[name]
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
// store, the clock, the binary version and the retention token.
type Deps struct {
	// Store is the hub storage.
	Store store.Store
	// Now is the clock.
	Now func() time.Time
	// Version is the version of the binary, the same value the
	// `nau version` command prints.
	Version string
	// CronToken is the bearer token of the retention route; empty
	// disables the route.
	CronToken string
}

// route is one registered route: its method, the path segments and the
// named parameters. A segment of the form {name} is a parameter: it
// matches any single segment, including the empty one, and the value
// reaches the handler through Request.PathParam. Matching is exact by
// segment: the route and the path have the same number of segments, no
// prefix is ever a wildcard.
type route struct {
	method  string
	segs    []string
	params  map[string]int
	handler Handler
}

// Server routes the requests to the handlers by exact method and path
// (with named segments), serves the API endpoints (health and version)
// and the static files, and answers 405 with Allow or 404 not_found
// where no handler fits.
type Server struct {
	deps   Deps
	routes []route
	static fs.FS
}

// New builds the server with the standard API routes registered. static
// is the filesystem of the static files outside /api/, or nil to refuse
// every path outside /api/ with 404. The retention route is registered
// only when the CronToken is set.
func New(deps Deps, static fs.FS) *Server {
	s := &Server{
		deps:   deps,
		static: static,
	}
	s.Handle("GET", apiHealthPath, s.health)
	s.Handle("GET", apiVersionPath, s.version)
	if deps.CronToken != "" {
		s.Handle("POST", apiCronRetentionPath, s.cronRetention)
	}
	return s
}

// Handle registers h for the method and path. A path segment of the
// form {name} is a named parameter. A malformed pattern (an empty name
// or a brace inside the name) registers nothing.
func (s *Server) Handle(method, path string, h Handler) {
	segs := splitPath(path)
	r := route{params: make(map[string]int)}
	for i, seg := range segs {
		name, isParam := segName(seg)
		if isParam && name == "" {
			return
		}
		if isParam {
			r.params[name] = i
		}
	}
	r.method = method
	r.segs = segs
	r.handler = h
	s.routes = append(s.routes, r)
}

// splitPath is the path without the leading slash, by segment.
func splitPath(path string) []string {
	return strings.Split(strings.TrimPrefix(path, "/"), "/")
}

// segName reports whether the segment is a named parameter and returns
// its name.
func segName(seg string) (string, bool) {
	if len(seg) > 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
		return seg[1 : len(seg)-1], true
	}
	return "", false
}

// matchSegs reports whether the route pattern matches the path
// segments and, when it does, returns the parameter values.
func (r *route) matchSegs(segs []string) (map[string]string, bool) {
	if len(r.segs) != len(segs) {
		return nil, false
	}
	out := make(map[string]string, len(r.params))
	for i, p := range r.segs {
		if name, isParam := segName(p); isParam {
			out[name] = segs[i]
			continue
		}
		if p != segs[i] {
			return nil, false
		}
	}
	return out, true
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

// api routes the requests of the API space to the handlers. It picks
// among the routes whose shape matches the path: a route without
// parameters wins over a parameterized one, and the registration order
// breaks the tie. When the path matches routes but not the method, it
// answers 405 with the Allow of every method registered for the shape.
func (s *Server) api(ctx context.Context, req Request) Response {
	segs := splitPath(req.Path)
	var matched []route
	for _, r := range s.routes {
		if _, ok := r.matchSegs(segs); ok {
			matched = append(matched, r)
		}
	}
	if len(matched) == 0 {
		return Response{Status: 404, Body: []byte(notFoundBody)}
	}
	for _, prefer := range []bool{true, false} {
		for _, r := range matched {
			if r.method != req.Method {
				continue
			}
			isLiteral := len(r.params) == 0
			if isLiteral != prefer {
				continue
			}
			params, _ := r.matchSegs(segs)
			req.PathParams = params
			return r.handler(ctx, req)
		}
	}
	allow := make([]string, 0, len(matched))
	seen := make(map[string]bool, len(matched))
	for _, r := range matched {
		if !seen[r.method] {
			seen[r.method] = true
			allow = append(allow, r.method)
		}
	}
	sort.Strings(allow)
	resp := Response{Status: 405, Header: make(Header)}
	resp.Header.Set("Allow", strings.Join(allow, ", "))
	return resp
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

// cronRetention runs the retention of the hub state. The bearer token
// of the request is compared with the CronToken in constant time; a
// mismatch answers 401 and a store failure 503 without the error.
func (s *Server) cronRetention(ctx context.Context, req Request) Response {
	if !s.cronAuthorized(req) {
		return Response{Status: 401, Body: []byte(`{"error":"unauthorized"}`)}
	}
	if err := ingest.Retain(ctx, s.deps.Store, s.deps.Now().UnixMilli()); err != nil {
		return Response{Status: 503, Body: []byte(`{"error":"unavailable"}`)}
	}
	return Response{Status: 204}
}

// cronAuthorized compares the bearer token of the request with the
// CronToken in constant time; the header must be exactly
// "Bearer <token>".
func (s *Server) cronAuthorized(req Request) bool {
	const prefix = "Bearer "
	header := req.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.deps.CronToken)) == 1
}
