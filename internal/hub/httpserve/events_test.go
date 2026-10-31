package httpserve

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
)

// streamFixture is the version reader and the session resolver of the
// events tests: the stored values plus the failure and the user
// switch, under a lock (the stream polls from its goroutine).
type streamFixture struct {
	mu           sync.Mutex
	ver          string
	versionErr   error
	failNext     int
	sessionOK    bool
	versionCalls int
	sessionCalls int
}

func (f *streamFixture) versionOf(ctx context.Context, developerID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versionCalls++
	if f.failNext > 0 {
		f.failNext--
		return "", context.DeadlineExceeded
	}
	if f.versionErr != nil {
		return "", f.versionErr
	}
	return f.ver, nil
}

func (f *streamFixture) sessionUser(ctx context.Context, req hub.Request) (accounts.User, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionCalls++
	if !f.sessionOK {
		return accounts.User{}, false
	}
	id := req.Header.Get("X-Test-User")
	if id == "" {
		id = "dev-1"
	}
	return accounts.User{ID: id}, true
}

func (f *streamFixture) setVersion(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ver = v
}

// revokeWith is the mid-stream revocation of the fixture: the session
// stops being valid and the board moves, in one lock section, so the
// stream that rechecks the session never sends the move.
func (f *streamFixture) revokeWith(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionOK = false
	f.ver = v
}

// setSessionOK switches the session of the fixture on or off.
func (f *streamFixture) setSessionOK(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionOK = ok
}

// failNextCalls makes the next n version reads fail.
func (f *streamFixture) failNextCalls(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

func (f *streamFixture) setVersionErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versionErr = err
}

// eventsTest is one events server with the injectable intervals.
type eventsTest struct {
	srv *httptest.Server
	fx  *streamFixture
	ev  *Events
}

func newEventsTest(t *testing.T, poll, ping, lifetime time.Duration, maxStreams, maxPerUser int) *eventsTest {
	t.Helper()
	fx := &streamFixture{ver: "v0", sessionOK: true}
	ev, err := NewEvents(EventsConfig{
		Version:       fx.versionOf,
		SessionUser:   fx.sessionUser,
		Now:           time.Now,
		Poll:          poll,
		Ping:          ping,
		Lifetime:      lifetime,
		WriteDeadline: 5 * time.Second,
		MaxStreams:    maxStreams,
		MaxPerUser:    maxPerUser,
	})
	if err != nil {
		t.Fatalf("NewEvents: %v", err)
	}
	srv := httptest.NewServer(ev)
	t.Cleanup(func() {
		ev.Stop()
		srv.Close()
	})
	return &eventsTest{srv: srv, fx: fx, ev: ev}
}

// sseFrame is one Server-Sent Events frame.
type sseFrame struct {
	comment string
	id      string
	event   string
	data    string
	retry   string
}

// isBoardEvent reports whether the frame is one board event.
func (fr sseFrame) isBoardEvent() bool { return fr.event == "board" }

// sseReader is the buffered reader of one stream: the tests hold the
// same reader across the frame reads, so the buffer of one read
// survives into the next.
func sseReader(resp *http.Response) *bufio.Reader {
	return bufio.NewReader(resp.Body)
}

// readSSE reads one frame of the stream, with the 5 s bound so a
// missing frame fails the test instead of hanging it. It reports
// false when the stream ended before the frame.
func readSSE(t *testing.T, r *bufio.Reader) (sseFrame, bool) {
	t.Helper()
	type out struct {
		fr sseFrame
		ok bool
	}
	ch := make(chan out, 1)
	go func() {
		var fr sseFrame
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- out{sseFrame{}, false}
				return
			}
			line = strings.TrimSuffix(line, "\n")
			if line == "" {
				ch <- out{fr, true}
				return
			}
			if strings.HasPrefix(line, ":") {
				fr.comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
				continue
			}
			name, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "id":
				fr.id = value
			case "event":
				fr.event = value
			case "data":
				fr.data = value
			case "retry":
				fr.retry = value
			}
		}
	}()
	select {
	case res := <-ch:
		return res.fr, res.ok
	case <-time.After(5 * time.Second):
		t.Fatal("the frame did not arrive in 5 s")
		return sseFrame{}, false
	}
}

// readSSEUntil reads frames until the condition holds or the stream
// ends, returning the frames read.
func readSSEUntil(t *testing.T, r *bufio.Reader, cond func([]sseFrame) bool) []sseFrame {
	t.Helper()
	frames := make([]sseFrame, 0, 16)
	for {
		if cond(frames) {
			return frames
		}
		fr, ok := readSSE(t, r)
		if !ok {
			return frames
		}
		frames = append(frames, fr)
		if len(frames) > 100 {
			t.Fatal("the stream wrote more than 100 frames without satisfying the condition")
		}
	}
}

func countBoard(frames []sseFrame) int {
	n := 0
	for _, fr := range frames {
		if fr.isBoardEvent() {
			n++
		}
	}
	return n
}

func countPings(frames []sseFrame) int {
	n := 0
	for _, fr := range frames {
		if fr.comment == "ping" {
			n++
		}
	}
	return n
}

// openStream starts one stream and returns its response; the caller
// closes the body.
func openStream(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// readBody reads the whole body of a closed stream, bounded.
func readStreamBody(t *testing.T, r io.Reader) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		ch <- string(b)
	}()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end in 5 s")
		return ""
	}
}

func TestEventsNoSessionIs401WithoutStream(t *testing.T) {
	e := newEventsTest(t, 20*time.Millisecond, 500*time.Millisecond, time.Second, 4, 4)
	e.fx.setSessionOK(false)
	resp := openStream(t, e.srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if body != `{"error":"unauthenticated"}` {
		t.Fatalf("body = %q, want the 401 JSON", body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := resp.Header.Get("Content-Type"); strings.Contains(got, "event-stream") {
		t.Fatalf("the refused stream carries the event-stream type: %q", got)
	}
	if e.fx.versionCalls != 0 {
		t.Fatalf("the version reader was called %d times, want 0", e.fx.versionCalls)
	}
}

func TestEventsOpensWithRetryAndTheStreamHeaders(t *testing.T) {
	e := newEventsTest(t, 50*time.Millisecond, 500*time.Millisecond, time.Second, 4, 4)
	resp := openStream(t, e.srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	defer func() {
		e.ev.Stop()
		resp.Body.Close()
	}()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	fr, ok := readSSE(t, respr)
	if !ok || fr.retry != "3000" {
		t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
	}
}

func TestEventsResumeSemantics(t *testing.T) {
	t.Run("NoLastEventID", func(t *testing.T) {
		e := newEventsTest(t, 50*time.Millisecond, 500*time.Millisecond, time.Second, 4, 4)
		resp := openStream(t, e.srv.URL+eventsPath, nil)
		respr := sseReader(resp)
		defer func() {
			e.ev.Stop()
			resp.Body.Close()
		}()
		// The opening retry, then the immediate event of the current
		// version.
		if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
			t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
		}
		fr, ok := readSSE(t, respr)
		if !ok || !fr.isBoardEvent() || fr.id != "v0" || fr.data != `{"version":"v0"}` {
			t.Fatalf("the immediate event = %+v, want the v0 board event", fr)
		}
	})
	t.Run("MatchingLastEventIDSendsNothing", func(t *testing.T) {
		e := newEventsTest(t, 50*time.Millisecond, 300*time.Millisecond, time.Second, 4, 4)
		resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
		respr := sseReader(resp)
		defer func() {
			e.ev.Stop()
			resp.Body.Close()
		}()
		if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
			t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
		}
		frames := readSSEUntil(t, respr, func(frames []sseFrame) bool { return countPings(frames) >= 2 })
		if n := countBoard(frames); n != 0 {
			t.Fatalf("the stream sent %d board events with a matching Last-Event-ID, want 0", n)
		}
	})
	t.Run("DifferingLastEventIDSendsAtOnce", func(t *testing.T) {
		e := newEventsTest(t, 50*time.Millisecond, 500*time.Millisecond, time.Second, 4, 4)
		resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v-old"})
		respr := sseReader(resp)
		defer func() {
			e.ev.Stop()
			resp.Body.Close()
		}()
		if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
			t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
		}
		fr, ok := readSSE(t, respr)
		if !ok || !fr.isBoardEvent() || fr.id != "v0" {
			t.Fatalf("the immediate event = %+v, want the v0 board event", fr)
		}
	})
}

func TestEventsEventOnlyWhenTheVersionChanges(t *testing.T) {
	e := newEventsTest(t, 20*time.Millisecond, 150*time.Millisecond, time.Second, 4, 4)
	resp := openStream(t, e.srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	defer func() {
		e.ev.Stop()
		resp.Body.Close()
	}()
	// The opening and the immediate v0 event.
	if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
		t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
	}
	fr, ok := readSSE(t, respr)
	if !ok || fr.id != "v0" {
		t.Fatalf("the immediate event = %+v, want v0", fr)
	}
	// Three pings with no event in between.
	frames := readSSEUntil(t, respr, func(frames []sseFrame) bool { return countPings(frames) >= 3 })
	if n := countBoard(frames); n != 0 {
		t.Fatalf("the stream sent %d board events with a stable version, want 0", n)
	}
	// The change: one event, and only one.
	e.fx.setVersion("v1")
	frames = readSSEUntil(t, respr, func(frames []sseFrame) bool { return countBoard(frames) >= 1 })
	if n := countBoard(frames); n != 1 {
		t.Fatalf("the stream sent %d board events for one change, want 1", n)
	}
	var got string
	for _, fr := range frames {
		if fr.isBoardEvent() {
			got = fr.id
		}
	}
	if got != "v1" {
		t.Fatalf("the event id = %q, want v1", got)
	}
	// And it does not repeat while the version holds.
	frames = readSSEUntil(t, respr, func(frames []sseFrame) bool { return countPings(frames) >= 3 })
	if n := countBoard(frames); n != 0 {
		t.Fatalf("the stream repeated the v1 event %d times", n)
	}
}

func TestEventsPingKeepsTheStream(t *testing.T) {
	e := newEventsTest(t, 50*time.Millisecond, 40*time.Millisecond, 2*time.Second, 4, 4)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	respr := sseReader(resp)
	defer func() {
		e.ev.Stop()
		resp.Body.Close()
	}()
	if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
		t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
	}
	frames := readSSEUntil(t, respr, func(frames []sseFrame) bool { return countPings(frames) >= 3 })
	if n := countPings(frames); n < 3 {
		t.Fatalf("the stream sent %d pings, want at least 3", n)
	}
	if n := countBoard(frames); n != 0 {
		t.Fatalf("the stable stream sent %d board events, want 0", n)
	}
}

func TestEventsSessionRevokedMidStreamEndsWithoutEvent(t *testing.T) {
	e := newEventsTest(t, 20*time.Millisecond, 2*time.Second, 10*time.Second, 4, 4)
	resp := openStream(t, e.srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	// The opening and the immediate v0 event.
	if fr, ok := readSSE(t, respr); !ok || fr.retry != "3000" {
		t.Fatalf("the opening frame = %+v, want retry: 3000", fr)
	}
	if fr, ok := readSSE(t, respr); !ok || fr.id != "v0" {
		t.Fatalf("the immediate event = %+v, want v0", fr)
	}
	// The board moves and the session dies together: the move never
	// reaches the client and the stream ends.
	e.fx.revokeWith("v1")
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if strings.Contains(body, `"version":"v1"`) {
		t.Fatal("the stream sent the v1 event after the session was revoked")
	}
	if strings.Contains(body, "error") {
		t.Fatalf("the closed stream carries error text: %q", body)
	}
}

func TestEventsProcessLimitIs503(t *testing.T) {
	e := newEventsTest(t, 100*time.Millisecond, 50*time.Millisecond, 400*time.Millisecond, 2, 8)
	r1 := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u1"})
	r1r := sseReader(r1)
	r2 := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u2"})
	r2r := sseReader(r2)
	if r1.StatusCode != http.StatusOK || r2.StatusCode != http.StatusOK {
		t.Fatalf("the first two streams: %d %d, want 200 200", r1.StatusCode, r2.StatusCode)
	}
	// The process is at the limit: the third stream is refused.
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u3"})
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("the third stream: status = %d, want 503", resp.StatusCode)
	}
	if body != `{"error":"unavailable"}` {
		t.Fatalf("the third stream: body = %q, want the 503 JSON", body)
	}
	// The two streams end on their own lifetime and free their slots:
	// the same third stream then opens.
	deadline := time.Now().Add(3 * time.Second)
	var r4 *http.Response
	for {
		r4 = openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u3"})
		if r4.StatusCode == http.StatusOK {
			break
		}
		b := readStreamBody(t, r4.Body)
		r4.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the freed slot was not reused: status = %d body = %q", r4.StatusCode, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	r4r := sseReader(r4)
	e.ev.Stop()
	b1 := readStreamBody(t, r1r)
	b2 := readStreamBody(t, r2r)
	b4 := readStreamBody(t, r4r)
	r1.Body.Close()
	r2.Body.Close()
	r4.Body.Close()
	if strings.Contains(b1+b2+b4, "error") {
		t.Fatalf("the streams carried error text: %q %q %q", b1, b2, b4)
	}
}

func TestEventsPerUserLimitIs429(t *testing.T) {
	e := newEventsTest(t, 500*time.Millisecond, 2*time.Second, 10*time.Second, 10, 2)
	streams := make([]*http.Response, 0, 2)
	for i := 0; i < 2; i++ {
		resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u1"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d of u1: status = %d, want 200", i, resp.StatusCode)
		}
		streams = append(streams, resp)
	}
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u1"})
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the third stream of u1: status = %d, want 429", resp.StatusCode)
	}
	if body != `{"error":"rate_limited"}` {
		t.Fatalf("the third stream of u1: body = %q, want the 429 JSON", body)
	}
	// Another user is not limited by the limit of u1.
	resp = openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the stream of u2: status = %d, want 200", resp.StatusCode)
	}
	streams = append(streams, resp)
	e.ev.Stop()
	for _, s := range streams {
		readStreamBody(t, s.Body)
		s.Body.Close()
	}
}

func TestEventsStopClosesTheOpenStreams(t *testing.T) {
	e := newEventsTest(t, 500*time.Millisecond, 2*time.Second, 10*time.Second, 8, 8)
	r1 := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u1", "Last-Event-ID": "v0"})
	r1r := sseReader(r1)
	r2 := openStream(t, e.srv.URL+eventsPath, map[string]string{"X-Test-User": "u2", "Last-Event-ID": "v0"})
	r2r := sseReader(r2)
	// Both streams are open: the retry frame of each already left.
	if fr, ok := readSSE(t, r1r); !ok || fr.retry != "3000" {
		t.Fatalf("stream 1: the opening frame = %+v", fr)
	}
	if fr, ok := readSSE(t, r2r); !ok || fr.retry != "3000" {
		t.Fatalf("stream 2: the opening frame = %+v", fr)
	}
	e.ev.Stop()
	b1 := readStreamBody(t, r1r)
	b2 := readStreamBody(t, r2r)
	r1.Body.Close()
	r2.Body.Close()
	if strings.Contains(b1, "board") || strings.Contains(b2, "board") {
		t.Fatalf("the stopped streams sent events: %q %q", b1, b2)
	}
	// The new streams are refused while the server is down.
	resp := openStream(t, e.srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after Stop: status = %d, want 503", resp.StatusCode)
	}
	if body != `{"error":"unavailable"}` {
		t.Fatalf("after Stop: body = %q", body)
	}
}

func TestEventsLifetimeClosesTheStream(t *testing.T) {
	e := newEventsTest(t, 10*time.Millisecond, 500*time.Millisecond, 120*time.Millisecond, 4, 4)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if strings.Contains(body, "event: board") {
		t.Fatalf("the lifetime stream sent a board event for a matching version: %q", body)
	}
	if !strings.Contains(body, "retry: 3000") {
		t.Fatalf("the lifetime stream did not open: %q", body)
	}
}

func TestEventsThreeConsecutiveFailuresCloseTheStream(t *testing.T) {
	e := newEventsTest(t, 10*time.Millisecond, 500*time.Millisecond, 10*time.Second, 4, 4)
	e.fx.setVersionErr(context.DeadlineExceeded)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if strings.Contains(body, "error") || strings.Contains(body, "DeadlineExceeded") {
		t.Fatalf("the closed stream carries the failure text: %q", body)
	}
	if e.fx.versionCalls < 3 {
		t.Fatalf("the stream closed after %d failed reads, want three in a row", e.fx.versionCalls)
	}
}

func TestEventsTwoFailuresDoNotCloseTheStream(t *testing.T) {
	e := newEventsTest(t, 10*time.Millisecond, 500*time.Millisecond, 10*time.Second, 4, 4)
	// The first two version reads (the immediate poll and the next)
	// fail; the third succeeds and the stream keeps living.
	e.fx.failNextCalls(2)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	respr := sseReader(resp)
	e.fx.setVersion("v1")
	frames := readSSEUntil(t, respr, func(frames []sseFrame) bool { return countBoard(frames) >= 1 })
	e.ev.Stop()
	resp.Body.Close()
	if n := countBoard(frames); n != 1 {
		t.Fatalf("the recovered stream sent %d board events, want 1", n)
	}
	if e.fx.versionCalls < 3 {
		t.Fatalf("the stream made %d version reads, want the two failures and the read of the change", e.fx.versionCalls)
	}
}

func TestEventsThroughTheRouter(t *testing.T) {
	e := newEventsTest(t, 50*time.Millisecond, 500*time.Millisecond, time.Second, 4, 4)
	st := &countingStore{}
	core := hub.New(hub.Deps{Store: st, Now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }, Version: "v1"}, nil)
	mux := WithEvents(New(core), e.ev)
	e.fx.setSessionOK(false)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// The route answers the stream's 401, not the router's 404.
	resp := openStream(t, srv.URL+eventsPath, nil)
	respr := sseReader(resp)
	body := readStreamBody(t, respr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || body != `{"error":"unauthenticated"}` {
		t.Fatalf("the route: %d %s, want 401 unauthenticated", resp.StatusCode, body)
	}
	// A different method falls through to the pure router: 404.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+eventsPath, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound || string(b2) != `{"error":"not_found"}` {
		t.Fatalf("POST: %d %q, want the 404 of the router", r2.StatusCode, b2)
	}
	// The other routes of the pure router are untouched.
	resp = openStream(t, srv.URL+"/api/health", nil)
	healthR := sseReader(resp)
	body = readStreamBody(t, healthR)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf("the health route: %d %s", resp.StatusCode, body)
	}
}

func TestEventsNewRefusesMisconfiguration(t *testing.T) {
	if _, err := NewEvents(EventsConfig{SessionUser: func(context.Context, hub.Request) (accounts.User, bool) { return accounts.User{}, false }}); err == nil {
		t.Fatal("a missing version reader must fail")
	}
	if _, err := NewEvents(EventsConfig{Version: func(context.Context, string) (string, error) { return "v", nil }}); err == nil {
		t.Fatal("a missing session resolver must fail")
	}
}

// newBlockedEventsTest is an events server whose version read never
// answers on its own: it returns only when its context ends, like a
// database that keeps the connection open without replying.
func newBlockedEventsTest(t *testing.T, lifetime, readTimeout time.Duration) *eventsTest {
	t.Helper()
	fx := &streamFixture{ver: "v0", sessionOK: true}
	blocked := func(ctx context.Context, developerID string) (string, error) {
		fx.mu.Lock()
		fx.versionCalls++
		fx.mu.Unlock()
		<-ctx.Done()
		return "", ctx.Err()
	}
	ev, err := NewEvents(EventsConfig{
		Version:       blocked,
		SessionUser:   fx.sessionUser,
		Now:           time.Now,
		Poll:          10 * time.Millisecond,
		Ping:          time.Second,
		Lifetime:      lifetime,
		WriteDeadline: 5 * time.Second,
		ReadTimeout:   readTimeout,
		MaxStreams:    4,
		MaxPerUser:    4,
	})
	if err != nil {
		t.Fatalf("NewEvents: %v", err)
	}
	srv := httptest.NewServer(ev)
	t.Cleanup(func() {
		ev.Stop()
		srv.Close()
	})
	return &eventsTest{srv: srv, fx: fx, ev: ev}
}

// openSlots reads the open-stream counter of the process.
func openSlots(ev *Events) int {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	return ev.open
}

// waitNoSlots waits for the stream's slot to be released after its
// handler returns.
func waitNoSlots(t *testing.T, ev *Events) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for openSlots(ev) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("open streams = %d after the stream ended, want 0", openSlots(ev))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEventsBlockedReadEndsAtTheLifetime: a version read that never
// answers does not hold the stream past its lifetime.
func TestEventsBlockedReadEndsAtTheLifetime(t *testing.T) {
	e := newBlockedEventsTest(t, 100*time.Millisecond, time.Minute)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	body := readStreamBody(t, sseReader(resp))
	resp.Body.Close()
	if !strings.Contains(body, "retry: 3000") || strings.Contains(body, "event: board") {
		t.Fatalf("the blocked stream body = %q, want only the opening", body)
	}
	waitNoSlots(t, e.ev)
}

// TestEventsBlockedReadEndsAtStop: a version read that never answers
// does not hold the stream once the server begins to stop.
func TestEventsBlockedReadEndsAtStop(t *testing.T) {
	e := newBlockedEventsTest(t, time.Minute, time.Minute)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	r := sseReader(resp)
	if fr, ok := readSSE(t, r); !ok || fr.retry != "3000" {
		t.Fatalf("the opening frame = %+v", fr)
	}
	e.ev.Stop()
	_ = readStreamBody(t, r)
	resp.Body.Close()
	waitNoSlots(t, e.ev)
}

// TestEventsBlockedReadsHitTheReadTimeout: each poll is bounded by the
// read timeout, so three stuck reads in a row close the stream long
// before its lifetime.
func TestEventsBlockedReadsHitTheReadTimeout(t *testing.T) {
	e := newBlockedEventsTest(t, time.Minute, 30*time.Millisecond)
	resp := openStream(t, e.srv.URL+eventsPath, map[string]string{"Last-Event-ID": "v0"})
	_ = readStreamBody(t, sseReader(resp))
	resp.Body.Close()
	waitNoSlots(t, e.ev)
	e.fx.mu.Lock()
	calls := e.fx.versionCalls
	e.fx.mu.Unlock()
	if calls < 3 {
		t.Fatalf("the stream closed after %d reads, want three timed-out reads in a row", calls)
	}
}
