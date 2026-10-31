package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/soho"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// testStart is the fake-clock origin of every loop test, so the health
// timestamps assert on fixed values.
var testStart = time.UnixMilli(1_700_000_000_000).UTC()

// The machine identity and the hub key of the loop tests.
const (
	testMachineID = "machine-m1"
	testKey       = "sekret-machine-key-1"
)

// hubRecord is one captured post.
type hubRecord struct {
	Path string
	Body []byte
	Auth string
	CT   string
}

// harness is the shared loop-test fixture: the fake soho (installed
// through fakesoho), a fake hub on loopback, the fake clock and sleep,
// and the agent running Run in a goroutine.
type harness struct {
	t          *testing.T
	fs         *fakeSleep
	dir        string
	exe        string
	healthPath string
	hubURL     string
	postRecs   []hubRecord
	postsMu    sync.Mutex
	diagsMu    sync.Mutex
	diagRecs   []string
	cancelCtx  context.CancelFunc

	runMu   sync.Mutex
	runErr  error
	runDone chan struct{}
}

// newHarness installs the fake soho with sohoRules, starts the fake
// hub that answers per post, and starts the agent on the fake clock.
// hubAnswer maps the 1-based post number to the answer; mut, when
// non-nil, adjusts the config before the start; hcBin, when not empty,
// is the waker binary.
func newHarness(t *testing.T, sohoRules []fakesoho.Rule, hubAnswer func(int) (int, map[string]string), hcBin string, mut func(*Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	exe := fakesoho.Install(t, dir, sohoRules...)
	fs := newFakeSleep(testStart)
	h := &harness{t: t, fs: fs, dir: dir, exe: exe, healthPath: dir + "/health.json", runDone: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := hubRecord{Path: r.URL.Path, Body: body, Auth: r.Header.Get("Authorization"), CT: r.Header.Get("Content-Type")}
		h.postsMu.Lock()
		h.postRecs = append(h.postRecs, rec)
		n := len(h.postRecs)
		h.postsMu.Unlock()
		status, headers := hubAnswer(n)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	h.hubURL = srv.URL

	cfg := Config{
		HubURL:    srv.URL,
		MachineID: testMachineID,
		Key:       testKey,
		Runner: soho.Runner{
			Bin:     exe,
			Environ: []string{"HOME=" + dir},
			Diag: func(msg string) {
				h.diagsMu.Lock()
				h.diagRecs = append(h.diagRecs, msg)
				h.diagsMu.Unlock()
			},
		},
		StatePath: h.healthPath,
		HCBin:     hcBin,
		Interval:  15 * time.Second,
		Now:       fs.Now,
		Sleep:     fs.Sleep,
		HTTP:      &http.Client{},
	}
	if mut != nil {
		mut(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancelCtx = cancel
	go func() {
		err := Run(ctx, cfg)
		h.runMu.Lock()
		h.runErr = err
		h.runMu.Unlock()
		close(h.runDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.runDone:
		case <-time.After(15 * time.Second):
			t.Errorf("the agent did not stop within 15 s of the cancel")
		}
	})
	return h
}

// posts returns the posts captured so far.
func (h *harness) posts() []hubRecord {
	h.postsMu.Lock()
	defer h.postsMu.Unlock()
	return append([]hubRecord(nil), h.postRecs...)
}

// waitForPosts blocks (real time, bounded) until the hub received at
// least n posts and returns the first n.
func (h *harness) waitForPosts(t *testing.T, n int) []hubRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if recs := h.posts(); len(recs) >= n {
			return recs[:n]
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d posts, the hub has %d", n, len(h.posts()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// health reads the health file.
func (h *harness) health(t *testing.T) Health {
	t.Helper()
	hh, err := ReadHealth(h.healthPath)
	if err != nil {
		t.Fatalf("read the health file: %v", err)
	}
	return hh
}

// waitForHealth polls the health file until pred accepts it.
func (h *harness) waitForHealth(t *testing.T, pred func(Health) bool) Health {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		hh, err := ReadHealth(h.healthPath)
		if err == nil && pred(hh) {
			return hh
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited 15 s for a matching health file; last read: %v (%v)", hh, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// release ends every pending fake sleep of duration d at its own
// deadline and jumps the fake clock to the latest of those deadlines.
func (h *harness) release(d time.Duration) {
	h.t.Helper()
	if err := h.fs.release(d, 15*time.Second); err != nil {
		h.t.Fatalf("release %v: %v", d, err)
	}
}

// waitForSleep blocks (real time, bounded) until a fake sleep of
// duration d is pending: the runner that registered it is back in its
// wait, so a test that changes a file a runner may exec can do so
// without racing a live child.
func (h *harness) waitForSleep(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !h.fs.hasPending(d) {
		if time.Now().After(deadline) {
			t.Fatalf("waited 15 s for a pending %v sleep", d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runError waits (bounded) for Run to return and gives its error.
func (h *harness) runError(t *testing.T) error {
	t.Helper()
	select {
	case <-h.runDone:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return within 15 s")
	}
	h.runMu.Lock()
	defer h.runMu.Unlock()
	return h.runErr
}

// diags returns the runner diagnostics captured so far.
func (h *harness) diags() []string {
	h.diagsMu.Lock()
	defer h.diagsMu.Unlock()
	return append([]string(nil), h.diagRecs...)
}

// cancel ends the agent and checks it returned the context error.
func (h *harness) cancel(t *testing.T) {
	t.Helper()
	h.cancelCtx()
	if err := h.runError(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run on the cancel returned %v, want the context error", err)
	}
}

// waitForCalls blocks (real time, bounded) until the fake log holds n
// invocations of the given argv.
func (h *harness) waitForCalls(t *testing.T, argv []string, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if countCalls(h.calls(t), argv...) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited 15 s for %d invocations of %v, have %d", n, argv, countCalls(h.calls(t), argv...))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fakeSleep is the deterministic stand-in for Config.Sleep: every
// sleep registers its duration; a test release ends the matching
// sleeps at their deadlines and moves the clock there. A sleep that
// loses a race to a wake or to the context never moves the clock: the
// clock advances only in release.
type fakeSleep struct {
	mu      sync.Mutex
	now     time.Time
	pending []*fsleep
}

type fsleep struct {
	at time.Time
	d  time.Duration
	ch chan struct{}
}

func newFakeSleep(start time.Time) *fakeSleep {
	return &fakeSleep{now: start}
}

// Now is the fake clock.
func (f *fakeSleep) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Sleep registers d and blocks until the test releases it or ctx ends.
func (f *fakeSleep) Sleep(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	s := &fsleep{at: f.now, d: d, ch: make(chan struct{})}
	f.pending = append(f.pending, s)
	f.mu.Unlock()
	select {
	case <-s.ch:
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		for i, p := range f.pending {
			if p == s {
				f.pending = append(f.pending[:i], f.pending[i+1:]...)
			}
		}
		f.mu.Unlock()
		return ctx.Err()
	}
}

// hasPending reports whether a sleep of duration d is registered.
func (f *fakeSleep) hasPending(d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.pending {
		if s.d == d {
			return true
		}
	}
	return false
}

// release ends every pending sleep of duration d at its own deadline
// and jumps the clock to the latest of those deadlines. It blocks
// (real time, bounded) until at least one such sleep is pending.
func (f *fakeSleep) release(d time.Duration, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		f.mu.Lock()
		var latest time.Time
		found := 0
		for _, s := range f.pending {
			if s.d == d {
				found++
				if dl := s.at.Add(s.d); dl.After(latest) {
					latest = dl
				}
			}
		}
		if found > 0 {
			kept := f.pending[:0]
			for _, s := range f.pending {
				if s.d == d {
					close(s.ch)
				} else {
					kept = append(kept, s)
				}
			}
			f.pending = kept
			if latest.After(f.now) {
				f.now = latest
			}
			f.mu.Unlock()
			return nil
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			return fmt.Errorf("no pending sleep of %v after %v", d, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// countCalls counts the fake-soho invocations whose argv is exactly
// argv.
func countCalls(calls []fakesoho.Call, argv ...string) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) == len(argv) {
			same := true
			for i := range argv {
				if c.Argv[i] != argv[i] {
					same = false
					break
				}
			}
			if same {
				n++
			}
		}
	}
	return n
}

// calls reads the fake-soho call log of the main fake install.
func (h *harness) calls(t *testing.T) []fakesoho.Call {
	t.Helper()
	return mustCalls(t, h.dir)
}

// mustCalls reads the fake-soho call log of dir.
func mustCalls(t *testing.T, dir string) []fakesoho.Call {
	t.Helper()
	calls, err := fakesoho.ReadCalls(dir)
	if err != nil {
		t.Fatalf("read the fake call log in %s: %v", dir, err)
	}
	return calls
}

// contextWithCancel gives a unit test a cancellable context that is
// cancelled again in cleanup.
func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}

// runnerFor builds the soho runner a unit test points at the fake.
func runnerFor(t *testing.T, exe string) soho.Runner {
	t.Helper()
	return soho.Runner{Bin: exe, Environ: []string{"HOME=" + t.TempDir()}}
}
