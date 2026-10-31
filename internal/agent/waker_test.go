package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// hcSecret marks the fake terminal-server lines: it must never reach
// the hub, the health file, a diagnostic or a child environment.
const hcSecret = "hc-event-line-secret-alpha"

// hcRules is the fake terminal-server events command: three lines,
// all carrying the secret, then the child exits.
func hcRules() []fakesoho.Rule {
	return []fakesoho.Rule{{
		Argv:   []string{"events"},
		Stdout: hcSecret + "\n" + hcSecret + "-beta\n" + hcSecret + "-gamma\n",
		Code:   0,
	}}
}

// TestWakerWakeAndBurst: a waker line wakes one extra read; a burst of
// three lines coalesces into at most one read every 2 s; the line
// content never reaches the hub, the health file or a diagnostic.
//
// Fake timeline: t0 first read; the three lines arrive and wake the
// loop, which waits the 2 s gap and takes the second read at t0+2 s;
// the third read is the period at t0+17 s; the waker reconnects after
// its 60 s and the fresh lines take the fourth read at t0+60 s.
func TestWakerWakeAndBurst(t *testing.T) {
	hcDir := t.TempDir()
	hcExe := fakesoho.Install(t, hcDir, hcRules()...)
	h := newHarness(t, inventoryRules(), ok204, hcExe, nil)
	h.waitForPosts(t, 1)
	// The burst woke the loop; it takes one extra read after the 2 s
	// gap, not one per line.
	h.release(2 * time.Second)
	h.waitForPosts(t, 2)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.LastAttemptMS == testStart.Add(2*time.Second).UnixMilli() })
	_ = hh
	// Then the period, then the waker reconnect (60 s) with its fresh
	// lines.
	h.release(15 * time.Second)
	h.waitForPosts(t, 3)
	h.waitForHealth(t, func(hh Health) bool { return hh.LastAttemptMS == testStart.Add(17*time.Second).UnixMilli() })
	h.release(60 * time.Second)
	h.waitForPosts(t, 4)
	h.waitForHealth(t, func(hh Health) bool { return hh.LastAttemptMS == testStart.Add(60*time.Second).UnixMilli() })
	// Exactly four reads: the burst of three lines produced one extra
	// read, and the reconnect produced one.
	if got := h.posts(); len(got) != 4 {
		t.Fatalf("the hub received %d posts, want 4", len(got))
	}
	// The events command ran twice (the start and the reconnect), in
	// the waker's own fake log.
	if got := countCalls(mustCalls(t, hcDir), "events"); got != 2 {
		t.Errorf("the events command ran %d times, want 2", got)
	}
	// The line content never left the fake: not in a post, not in the
	// health file, not in a diagnostic.
	for _, rec := range h.posts() {
		if bytes.Contains(rec.Body, []byte(hcSecret)) {
			t.Error("a posted body carries an events line")
		}
	}
	if raw, err := os.ReadFile(h.healthPath); err == nil && bytes.Contains(raw, []byte(hcSecret)) {
		t.Error("the health file carries an events line")
	}
	for _, d := range h.diags() {
		if strings.Contains(d, hcSecret) {
			t.Errorf("a runner diagnostic carries an events line: %q", d)
		}
	}
	for _, c := range append(h.calls(t), mustCalls(t, hcDir)...) {
		for _, e := range c.Env {
			if strings.Contains(e, hcSecret) {
				t.Error("a child environment carries an events line")
			}
		}
	}
	h.cancel(t)
}

// TestWakerMissingKeepsThePeriodAndReconnects: without an events
// binary the period keeps running and the waker retries every 60 s;
// the binary appears only after the third attempt, and its lines wake
// one extra read. After each release the test waits for the waker to
// be back in its 60 s wait before it touches the binary, so an attempt
// never execs a file the test changes.
func TestWakerMissingKeepsThePeriodAndReconnects(t *testing.T) {
	hcDir := t.TempDir()
	h := newHarness(t, inventoryRules(), ok204, filepath.Join(hcDir, "soho"), nil)
	h.waitForPosts(t, 1)
	// The period keeps running while the waker has no binary.
	h.release(15 * time.Second)
	h.waitForPosts(t, 2)
	// The first two reconnects find no binary at all.
	h.release(60 * time.Second)
	h.waitForSleep(t, 60*time.Second)
	h.release(60 * time.Second)
	h.waitForSleep(t, 60*time.Second)
	// The binary appears while the waker is in its 60 s wait; the next
	// attempt's lines wake one extra read.
	fakesoho.Install(t, hcDir, hcRules()...)
	h.release(60 * time.Second)
	h.waitForPosts(t, 3)
	h.waitForHealth(t, func(hh Health) bool { return hh.LastAttemptMS == testStart.Add(180*time.Second).UnixMilli() })
	if got := h.posts(); len(got) != 3 {
		t.Fatalf("the hub received %d posts, want 3", len(got))
	}
	// The attempts before the install left no log (there was no
	// binary); the successful one logged the events invocation.
	if got := countCalls(mustCalls(t, hcDir), "events"); got != 1 {
		t.Errorf("the events command ran %d times, want 1 logged attempt", got)
	}
	h.cancel(t)
}

// TestLineSignalsCoalesceAndBound: one signal per newline-terminated
// line; a burst into a full channel coalesces; a partial line without
// a newline is completed by the next write; a runaway partial line is
// dropped instead of growing the buffer.
func TestLineSignalsCoalesceAndBound(t *testing.T) {
	ch := make(chan struct{}, 8)
	l := &lineSignal{ch: ch}
	if _, err := l.Write([]byte("a\nb\npartial")); err != nil {
		t.Fatalf("write: %v", err)
	}
	signals := 0
	for {
		select {
		case <-ch:
			signals++
		default:
		}
		if signals == 2 {
			break
		}
	}
	if _, err := l.Write([]byte("\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("the completed partial line did not signal")
	}

	// A burst into a single-slot channel leaves one signal.
	single := make(chan struct{}, 1)
	l2 := &lineSignal{ch: single}
	for i := 0; i < 5; i++ {
		if _, err := l2.Write([]byte("line\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	pending := 0
drain:
	for {
		select {
		case <-single:
			pending++
		default:
			break drain
		}
	}
	if pending != 1 {
		t.Errorf("a burst left %d signals, want 1 coalesced", pending)
	}

	// A partial line past the cap is dropped, not buffered.
	capL := &lineSignal{ch: make(chan struct{}, 1)}
	if _, err := capL.Write(make([]byte, lineSignalCap+1)); err != nil {
		t.Fatalf("write: %v", err)
	}
	capL.mu.Lock()
	pend := len(capL.pend)
	capL.mu.Unlock()
	if pend != 0 {
		t.Errorf("the runaway partial line kept %d bytes, want it dropped", pend)
	}
}

// TestHCEventsStopsOnCancel: when the context ends, the events child
// is ended and Done closes.
func TestHCEventsStopsOnCancel(t *testing.T) {
	exe := fakesoho.Install(t, t.TempDir(), hcRules()...)
	ctx, cancel := contextWithCancel(t)
	w := NewHCEvents(runnerFor(t, exe))
	w.Start(ctx)
	cancel()
	select {
	case <-w.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the waker did not end within 15 s of the cancel")
	}
}
