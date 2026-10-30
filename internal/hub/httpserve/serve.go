// Package httpserve is the thin net/http adapter of the hub core: it
// translates one HTTP request into a hub request, calls the handler,
// and writes the response. It bounds the request body, refuses paths
// that are not in the origin form, and adds the API response headers.
package httpserve

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/hub"
)

// maxBody is the limit of a request body: 1 MiB.
const maxBody = 1 << 20

// The timeouts of the server built by NewServer.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 120 * time.Second
)

// Handler adapts a hub server to net/http.
type Handler struct {
	server *hub.Server
}

// New builds the adapter over a hub server.
func New(server *hub.Server) *Handler {
	return &Handler{server: server}
}

// ServeHTTP implements http.Handler. A body larger than the limit
// answers 413 without calling the handler; a path that is not in the
// origin form answers 404.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, over, err := readBody(r.Body)
	if err != nil || over {
		// Over the limit or unreadable: refuse without the handler.
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	req := hub.Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: headersFrom(r.Header),
		Body:   body,
	}
	if isAPIPath(r.URL.Path) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
	}
	resp := h.server.Serve(r.Context(), req)
	for name, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// NewServer builds the http.Server with the hub timeouts around the
// given handler.
func NewServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
	}
}

// readBody reads the request body up to the limit plus one byte; it
// reports whether the body is over the limit.
func readBody(r io.Reader) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxBody {
		return nil, true, nil
	}
	return data, false, nil
}

// isAPIPath reports whether the path is in the API space: /api itself
// or anything below /api/.
func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// headersFrom copies one http.Header to a hub header.
func headersFrom(src http.Header) hub.Header {
	h := make(hub.Header, len(src))
	for k, v := range src {
		h[strings.ToLower(k)] = append([]string(nil), v...)
	}
	return h
}
