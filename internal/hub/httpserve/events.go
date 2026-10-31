package httpserve

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
)

// The constants of the events stream.
const (
	// eventsPath is the streaming route, outside the pure router:
	// the pure core has no notion of a long-lived response.
	eventsPath = "/api/events"

	// sseRetryMS is the reconnect interval the browser gets on the
	// opening of the stream.
	sseRetryMS = 3000
)

// The defaults of the stream. The tests inject shorter intervals and
// smaller limits; the production wiring keeps the defaults.
const (
	defaultPollInterval       = 2 * time.Second
	defaultPingInterval       = 15 * time.Second
	defaultStreamLifetime     = 10 * time.Minute
	defaultStreamWriteTimeout = 30 * time.Second
	defaultMaxOpenStreams     = 256
	defaultMaxStreamsPerUser  = 8
)

// EventsConfig is the configuration of the events stream: the version
// reader, the session resolver and the clock, with the intervals and
// the per-process limits, all injectable. The zero intervals and
// limits take the defaults.
type EventsConfig struct {
	// Version computes the board version of the developer. Required.
	Version func(ctx context.Context, developerID string) (string, error)
	// SessionUser resolves the signed-in user of a request, or
	// false: the same check RequireSession applies. Required.
	SessionUser func(ctx context.Context, req hub.Request) (accounts.User, bool)
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Poll is the interval of the version reads.
	Poll time.Duration
	// Ping is the interval of the ping comments.
	Ping time.Duration
	// Lifetime is how long one stream stays open before it closes
	// so the browser reconnects with the last event id.
	Lifetime time.Duration
	// WriteDeadline is the deadline of one write; the WriteTimeout
	// of the server keeps applying to the other routes, and the
	// stream renews its own on every write.
	WriteDeadline time.Duration
	// MaxStreams is the limit of open streams of the process.
	MaxStreams int
	// MaxPerUser is the limit of open streams of one user.
	MaxPerUser int
}

// Events is the GET /api/events stream: it opens one Server-Sent
// Events stream per signed-in developer and sends one board event
// whenever the board version of that developer changes. No board
// data goes on the event: the interface refetches the read routes.
// It holds no board state: the open-stream counters are resource
// limits of the process, re-read from the database on every poll.
type Events struct {
	version       func(ctx context.Context, developerID string) (string, error)
	sessionUser   func(ctx context.Context, req hub.Request) (accounts.User, bool)
	now           func() time.Time
	poll          time.Duration
	ping          time.Duration
	lifetime      time.Duration
	writeDeadline time.Duration
	maxStreams    int
	maxPerUser    int

	mu      sync.Mutex
	open    int
	perUser map[string]int

	stopOnce sync.Once
	stopped  chan struct{}
}

// NewEvents builds the events stream. A missing version reader or
// session resolver is an error.
func NewEvents(cfg EventsConfig) (*Events, error) {
	if cfg.Version == nil {
		return nil, errors.New("httpserve: the version reader of the events is required")
	}
	if cfg.SessionUser == nil {
		return nil, errors.New("httpserve: the session resolver of the events is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	poll := cfg.Poll
	if poll <= 0 {
		poll = defaultPollInterval
	}
	ping := cfg.Ping
	if ping <= 0 {
		ping = defaultPingInterval
	}
	lifetime := cfg.Lifetime
	if lifetime <= 0 {
		lifetime = defaultStreamLifetime
	}
	writeDeadline := cfg.WriteDeadline
	if writeDeadline <= 0 {
		writeDeadline = defaultStreamWriteTimeout
	}
	maxStreams := cfg.MaxStreams
	if maxStreams <= 0 {
		maxStreams = defaultMaxOpenStreams
	}
	maxPerUser := cfg.MaxPerUser
	if maxPerUser <= 0 {
		maxPerUser = defaultMaxStreamsPerUser
	}
	return &Events{
		version:       cfg.Version,
		sessionUser:   cfg.SessionUser,
		now:           now,
		poll:          poll,
		ping:          ping,
		lifetime:      lifetime,
		writeDeadline: writeDeadline,
		maxStreams:    maxStreams,
		maxPerUser:    maxPerUser,
		perUser:       map[string]int{},
		stopped:       make(chan struct{}),
	}, nil
}

// Stop ends every open stream and refuses the new ones: it runs when
// the server starts to shut down, so a clean Shutdown does not wait
// the lifetime of a stream.
func (e *Events) Stop() {
	e.stopOnce.Do(func() { close(e.stopped) })
}

// ServeHTTP serves one request of the events route. Without a live
// session it answers 401 in JSON, without opening the stream; above
// the process limit it answers 503 unavailable and above the per-user
// limit 429 rate_limited.
func (e *Events) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	req := hub.Request{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: headersFrom(r.Header),
	}
	u, ok := e.sessionUser(ctx, req)
	if !ok {
		e.writeJSON(w, http.StatusUnauthorized, `{"error":"unauthenticated"}`)
		return
	}
	status, body := e.admit(u.ID)
	if status != 0 {
		e.writeJSON(w, status, body)
		return
	}
	defer e.release(u.ID)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	e.stream(ctx, w, req, u.ID, r.Header.Get("Last-Event-ID"))
}

// WithEvents routes the GET /api/events to the events stream and every
// other request to h: the streaming layer stays beside the pure
// router, which never holds a connection open.
func WithEvents(h http.Handler, ev *Events) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == eventsPath {
			ev.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// admit counts the open stream against the per-process and the
// per-user limits, in one lock.
func (e *Events) admit(devID string) (int, string) {
	select {
	case <-e.stopped:
		return http.StatusServiceUnavailable, `{"error":"unavailable"}`
	default:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open >= e.maxStreams {
		return http.StatusServiceUnavailable, `{"error":"unavailable"}`
	}
	if e.perUser[devID] >= e.maxPerUser {
		return http.StatusTooManyRequests, `{"error":"rate_limited"}`
	}
	e.open++
	e.perUser[devID]++
	return 0, ""
}

// release returns the slot of one closed stream.
func (e *Events) release(devID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.open--
	e.perUser[devID]--
	if e.perUser[devID] == 0 {
		delete(e.perUser, devID)
	}
}

// stream keeps one stream open: it sends the retry of the opening,
// runs the first poll at once (the immediate event when the version
// differs from the Last-Event-ID), and then re-reads the version on
// the poll interval and pings on the ping interval, until the context
// ends, the server stops, the lifetime is spent or three version
// reads fail in a row.
func (e *Events) stream(ctx context.Context, w http.ResponseWriter, req hub.Request, devID, lastEventID string) {
	if !e.writeFrame(w, "retry: "+strconv.Itoa(sseRetryMS)+"\n\n") {
		return
	}
	lastSent := lastEventID
	consecutive := 0
	if !e.pollOnce(ctx, w, req, devID, &lastSent, &consecutive) {
		return
	}
	poll := time.NewTicker(e.poll)
	defer poll.Stop()
	ping := time.NewTicker(e.ping)
	defer ping.Stop()
	lifetime := time.NewTimer(e.lifetime)
	defer lifetime.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopped:
			return
		case <-lifetime.C:
			return
		case <-poll.C:
			if !e.pollOnce(ctx, w, req, devID, &lastSent, &consecutive) {
				return
			}
		case <-ping.C:
			if !e.writeFrame(w, ": ping\n\n") {
				return
			}
		}
	}
}

// pollOnce rechecks the session and re-reads the version. It reports
// whether the stream keeps: the session that stopped being valid
// ends the stream without an event; a failed read counts toward the
// three consecutive failures; and the event goes out only when the
// version changed. The session is checked again before the write, so
// a session that dies in between the read and the write never sees
// the event.
func (e *Events) pollOnce(ctx context.Context, w http.ResponseWriter, req hub.Request, devID string, lastSent *string, consecutive *int) bool {
	if _, ok := e.sessionUser(ctx, req); !ok {
		return false
	}
	v, err := e.version(ctx, devID)
	if err != nil {
		*consecutive++
		return *consecutive < 3
	}
	*consecutive = 0
	if v != *lastSent {
		if _, ok := e.sessionUser(ctx, req); !ok {
			return false
		}
		if !e.writeEvent(w, v) {
			return false
		}
		*lastSent = v
	}
	return true
}

// writeEvent writes one board event and flushes it: the id is the
// version, the data is the version alone.
func (e *Events) writeEvent(w http.ResponseWriter, v string) bool {
	return e.writeFrame(w, "id: "+v+"\nevent: board\ndata: {\"version\":\""+v+"\"}\n\n")
}

// writeFrame writes one frame with the per-write deadline and
// flushes it, so the WriteTimeout of the server keeps applying to the
// other routes and the stream renews its own on every write.
func (e *Events) writeFrame(w http.ResponseWriter, frame string) bool {
	_ = http.NewResponseController(w).SetWriteDeadline(e.now().Add(e.writeDeadline))
	if _, err := w.Write([]byte(frame)); err != nil {
		return false
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

// writeJSON is the JSON answer of the stream refusals, before the
// stream opens.
func (e *Events) writeJSON(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
