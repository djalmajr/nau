package agent

// The probes of the agent core review (the seven findings), adapted to
// the package: each test fails with the mutation of the mechanism it
// guards, and each passes on the fixed code.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/soho"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// reviewRT is the stub transport of the review probes: it captures the
// requests and answers per call.
type reviewRT func(*http.Request) (*http.Response, error)

func (f reviewRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reviewResp(r *http.Request, status int, h http.Header) *http.Response {
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}
}

// reviewState is the loop state of the probes, on the fake clock and
// over the stub transport. The client gets the same transport policy
// Run applies (agentTransport), so the probes exercise the policy the
// agent runs with.
func reviewState(rt reviewRT) *state {
	c := &http.Client{Transport: rt}
	agentTransport(c)
	return &state{
		now:      func() time.Time { return testStart },
		http:     c,
		url:      "https://hub.example",
		key:      testKey,
		interval: 15 * time.Second,
		state:    StateHubUnreachable,
	}
}

// TestReviewRedirectTransportPolicy: the client does not follow a
// redirect, the 3xx answer is classified hub_unreachable with the
// backoff, and nothing is resent: the rejected destination (an
// off-loopback http URL checkHubURL refuses) is never reached, and the
// key and the inventory travel once, nowhere else.
func TestReviewRedirectTransportPolicy(t *testing.T) {
	body := validFixture(t)
	calls := 0
	var auth, exact, insecure bool
	st := reviewState(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return reviewResp(r, 307, http.Header{"Location": []string{"http://hub.example/api/ingest"}}), nil
		}
		b, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		auth = r.Header.Get("Authorization") == "Bearer "+testKey
		exact = bytes.Equal(b, body)
		insecure = checkHubURL(r.URL.String()) != nil
		return reviewResp(r, 204, http.Header{}), nil
	})
	if e := checkHubURL(st.url); e != nil {
		t.Fatalf("the initial URL must be valid, got %v", e)
	}
	_ = st.send(context.Background(), body)
	fmt.Printf("redirect calls=%d rejected_destination_reached=%t auth_forwarded=%t exact_inventory_forwarded=%t final_state=%s\n",
		calls, insecure, auth, exact, st.state)
	if calls != 1 {
		t.Fatalf("the client made %d calls, want 1 (a redirect must not be followed and nothing resent)", calls)
	}
	if insecure || auth || exact {
		t.Fatal("the redirect destination was reached with the auth and the inventory, want no second request at all")
	}
	if st.state != StateHubUnreachable {
		t.Fatalf("final state = %s, want hub_unreachable (a 3xx is a hub answer, not a delivery)", st.state)
	}
	if st.backoff != backoffStart {
		t.Fatalf("backoff = %s, want the backoff start after a 3xx", st.backoff)
	}
}

// TestReviewRetryAfterHTTPDate: a Retry-After in HTTP-date form is
// honored against the agent's clock, instead of falling to the 5 s
// backoff.
func TestReviewRetryAfterHTTPDate(t *testing.T) {
	date := testStart.Add(45 * time.Second).Format(http.TimeFormat)
	if _, e := http.ParseTime(date); e != nil {
		t.Fatal(e)
	}
	st := reviewState(func(r *http.Request) (*http.Response, error) {
		return reviewResp(r, 429, http.Header{"Retry-After": []string{date}}), nil
	})
	_ = st.send(context.Background(), validFixture(t))
	d := st.nextWait()
	fmt.Printf("retry_after_http_date wait_ms=%d requested_ms=45000 state=%s\n", d.Milliseconds(), st.state)
	if d != 45*time.Second {
		t.Fatalf("wait = %s, want the 45 s the HTTP date announces", d)
	}
	if st.state != StateRateLimited {
		t.Fatalf("state = %s, want rate_limited", st.state)
	}
}

// TestReviewRetryAfterBoundsAndPastDate: a future HTTP date past the
// cap waits the cap; a past date and an invalid value fall to the
// backoff, and integer seconds past the cap wait the cap.
func TestReviewRetryAfterBoundsAndPastDate(t *testing.T) {
	cases := []struct {
		name string
		ra   string
		want time.Duration
	}{
		{"http date past the cap", testStart.Add(2 * time.Minute).Format(http.TimeFormat), retryAfterCap},
		{"past http date", testStart.Add(-10 * time.Second).Format(http.TimeFormat), backoffStart},
		{"invalid value", "garbage", backoffStart},
		{"seconds past the cap", "90", retryAfterCap},
		{"seconds", "7", 7 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := reviewState(func(r *http.Request) (*http.Response, error) {
				return reviewResp(r, 429, http.Header{"Retry-After": []string{tc.ra}}), nil
			})
			_ = st.send(context.Background(), validFixture(t))
			if got := st.nextWait(); got != tc.want {
				t.Fatalf("wait = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestReviewRepeated429Backoff: 429s without the header advance the
// backoff across the refusals (5, 10, 20, 40) and the progression
// returns to the start only after a success.
func TestReviewRepeated429Backoff(t *testing.T) {
	st := reviewState(func(r *http.Request) (*http.Response, error) {
		return reviewResp(r, 429, http.Header{}), nil
	})
	got := []int64{}
	for i := 0; i < 4; i++ {
		_ = st.send(context.Background(), validFixture(t))
		got = append(got, st.nextWait().Milliseconds())
		st.clearWaitOverride()
	}
	fmt.Printf("repeated_429 wait_ms=%v\n", got)
	want := []int64{5000, 10000, 20000, 40000}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("waits = %v, want %v (the 429 progression advances and is not restarted by each refusal)", got, want)
		}
	}
	// A success restarts the progression: the next refusal starts at
	// 5 s again.
	ok := reviewState(func(r *http.Request) (*http.Response, error) {
		return reviewResp(r, 204, http.Header{}), nil
	})
	ok.backoff = 40 * time.Second
	_ = ok.send(context.Background(), validFixture(t))
	if ok.backoff != 0 {
		t.Fatalf("after a success the backoff = %s, want the reset progression", ok.backoff)
	}
}

// TestReviewTrailingSlashEndpoint: a base URL with a trailing slash is
// accepted, and the inventory still reaches /api/ingest.
func TestReviewTrailingSlashEndpoint(t *testing.T) {
	path := ""
	st := reviewState(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		status := 204
		if path != "/api/ingest" {
			status = 404
		}
		return reviewResp(r, status, http.Header{}), nil
	})
	st.url = "http://127.0.0.1/"
	if e := checkHubURL(st.url); e != nil {
		t.Fatalf("the loopback base with the trailing slash must be accepted, got %v", e)
	}
	_ = st.send(context.Background(), validFixture(t))
	fmt.Printf("trailing_slash path=%q state=%s\n", path, st.state)
	if path != "/api/ingest" {
		t.Fatalf("the ingest path = %q, want /api/ingest (the trailing slash must not leak)", path)
	}
	if st.state != StateOK {
		t.Fatalf("state = %s, want ok (the inventory reached the endpoint)", st.state)
	}
}

// TestReviewEndpointJoin: the endpoint is joined with the base path,
// with or without its trailing slash.
func TestReviewEndpointJoin(t *testing.T) {
	cases := []struct{ base, want string }{
		{"https://hub.example", "https://hub.example/api/ingest"},
		{"https://hub.example/", "https://hub.example/api/ingest"},
		{"https://hub.example/base", "https://hub.example/base/api/ingest"},
		{"https://hub.example/base/", "https://hub.example/base/api/ingest"},
		{"http://127.0.0.1/", "http://127.0.0.1/api/ingest"},
	}
	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			got, err := ingestEndpoint(tc.base)
			if err != nil {
				t.Fatalf("ingestEndpoint(%q): %v", tc.base, err)
			}
			if got != tc.want {
				t.Fatalf("ingestEndpoint(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// TestReviewRejectedURLDoesNotEchoCredential: a malformed URL that
// carries the key as user information is refused, and the error is the
// fixed text: no piece of the URL, and no credential.
func TestReviewRejectedURLDoesNotEchoCredential(t *testing.T) {
	raw := "https://user:" + testKey + "@hub.example/%zz"
	data, e := json.Marshal(map[string]string{"url": raw, "machine_id": testMachineID, "key": testKey})
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(t.TempDir(), "hub.json")
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	_, e = LoadHubFile(file)
	if e == nil {
		t.Fatal("the malformed URL must be refused")
	}
	leak := strings.Contains(e.Error(), testKey)
	fmt.Printf("rejected_url refused=true credential_in_error=%t\n", leak)
	if leak {
		t.Fatal("the error reflects the credential in the rejected user information")
	}
	if e.Error() != "agent: the hub URL is not valid" {
		t.Fatalf("error = %q, want the fixed text (no piece of the URL)", e.Error())
	}
	// The same fixed text for a refusal that is not a parse failure:
	// no piece of the URL there either.
	other := "https://user:pass@hub.example"
	if got := checkHubURL(other); got != errHubURLInvalid {
		t.Fatalf("checkHubURL(userinfo) = %v, want the fixed text", got)
	}
}

// TestReviewEventsCaptureIsCleared: the runner the waker runs keeps no
// capture sink: whatever the events stream printed reaches the line
// reader as signals and nothing else, not the caller's buffer.
func TestReviewEventsCaptureIsCleared(t *testing.T) {
	var capture bytes.Buffer
	exe := fakesoho.Install(t, t.TempDir(), hcRules()...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewHCEvents(soho.Runner{Bin: exe, Environ: []string{}, Capture: &capture})
	w.Sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	w.Start(ctx)
	select {
	case <-w.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the waker did not finish")
	}
	select {
	case <-w.Runner.DrainDone():
	case <-time.After(time.Second):
		t.Fatal("the waker output was not drained")
	}
	leak := bytes.Contains(capture.Bytes(), []byte(hcSecret))
	fmt.Printf("events_capture content_retained=%t capture_bytes=%d\n", leak, capture.Len())
	if leak {
		t.Fatal("raw events reached the caller's capture buffer")
	}
	if capture.Len() != 0 {
		t.Fatalf("the capture buffer holds %d bytes, want none (the events runner keeps no content sink)", capture.Len())
	}
}

// TestReviewExistingHealthDirectoryMode: a health directory that
// already exists is still brought to the private mode after the write.
func TestReviewExistingHealthDirectoryMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if e := os.Mkdir(dir, 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Chmod(dir, 0755); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(dir, "health.json")
	if e := writeAtomic(file, []byte("{}\n")); e != nil {
		t.Fatalf("writeAtomic: %v", e)
	}
	d, e := os.Stat(dir)
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Printf("health_permissions directory=%04o file=%04o\n", d.Mode().Perm(), f.Mode().Perm())
	if d.Mode().Perm() != 0700 || f.Mode().Perm() != 0600 {
		t.Fatalf("directory = %04o, file = %04o, want 0700 and 0600 (an existing directory must meet the private mode contract)", d.Mode().Perm(), f.Mode().Perm())
	}
}

// TestHealthDirModeSentinel: the directory-mode failure is an internal
// category that never hides a real write error, and a write error is
// never masked by the best-effort mode: the agent maps the category to
// its internal record and goes on, and only a real write failure stops
// it.
func TestHealthDirModeSentinel(t *testing.T) {
	modeErr := os.ErrPermission
	writeErr := os.ErrClosed
	if err := modeErrIfNone(modeErr, writeErr); !errors.Is(err, writeErr) {
		t.Fatalf("write error present: got %v, want the write error (the mode failure never hides it)", err)
	}
	if err := modeErrIfNone(modeErr, nil); !errors.Is(err, errHealthDirMode) {
		t.Fatalf("mode failure only: got %v, want the internal category (the write landed)", err)
	}
	if err := modeErrIfNone(nil, writeErr); !errors.Is(err, writeErr) {
		t.Fatalf("got %v, want the write error", err)
	}
	if err := modeErrIfNone(nil, nil); err != nil {
		t.Fatalf("got %v, want nil (both clean)", err)
	}
}
