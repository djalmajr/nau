package agent

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/djalmajr/nau/internal/soho"
)

// Waker signals that a terminal-server event happened: the agent runs
// the inventory again, at most once every 2 s.
type Waker interface {
	// Wake returns the channel a terminal-server line signals. The
	// signal carries no content: whatever the events printed is never
	// stored, logged or forwarded.
	Wake() <-chan struct{}
}

// lineSignalCap bounds the partial line lineSignal keeps between writes.
const lineSignalCap = 64 << 10

// lineSignal is an io.Writer that signals ch once per
// newline-terminated line. It holds at most the in-flight partial line
// and keeps no other trace of the content; a send that finds the
// channel full is dropped, so a burst of lines coalesces into one
// signal the loop drains on its own schedule.
type lineSignal struct {
	ch   chan struct{}
	mu   sync.Mutex
	pend []byte
}

func (l *lineSignal) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pend = append(l.pend, p...)
	for {
		i := bytes.IndexByte(l.pend, '\n')
		if i < 0 {
			break
		}
		l.pend = l.pend[i+1:]
		select {
		case l.ch <- struct{}{}:
		default:
		}
	}
	if len(l.pend) > lineSignalCap {
		// A line without a newline past the cap: drop it. A wake hint
		// is worth no unbounded memory, and the period read covers
		// whatever state it carried.
		l.pend = nil
	}
	return len(p), nil
}

// HCEvents is the Waker that runs the terminal-server events command as
// an argv subprocess through the same bounded runner as soho
// (internal/soho, tree-isolated by internal/proctree). Every line the
// child prints signals the wake channel; the content is not stored,
// logged or forwarded. When the binary is missing or exits, the loop
// waits and retries, so the agent never runs without its period.
type HCEvents struct {
	// Runner executes the events binary. Its Bin is the HCBin name and
	// its Environ the exact child environment.
	Runner soho.Runner
	// Sleep, when nil, is the context-bounded real sleep. It is the
	// agent's injected sleep, so a test drives the reconnect waits on
	// its fake clock.
	Sleep func(ctx context.Context, d time.Duration) error
	// Reconnect, when zero, is the default 60 s wait between attempts.
	Reconnect time.Duration

	ch   chan struct{}
	done chan struct{}
}

// NewHCEvents builds the waker over runner. The copy it keeps never
// carries a capture sink: a line of the events stream is a signal, not
// content, and nothing of the child's stdout may reach the caller's
// buffers. Start runs it; it ends when the Start context ends.
func NewHCEvents(runner soho.Runner) *HCEvents {
	runner.Capture = nil // the events runner keeps no content sink
	return &HCEvents{
		Runner:    runner,
		Reconnect: wakerReconnect,
		ch:        make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
}

// Wake returns the channel a terminal-server line signals.
func (h *HCEvents) Wake() <-chan struct{} { return h.ch }

// Done returns the channel closed once the run loop has ended: after
// the Start context ends and the last events child is gone.
func (h *HCEvents) Done() <-chan struct{} { return h.done }

// Start begins the events loop. It must be called once.
func (h *HCEvents) Start(ctx context.Context) {
	go h.run(ctx)
}

func (h *HCEvents) run(ctx context.Context) {
	defer close(h.done)
	sleep := h.Sleep
	if sleep == nil {
		sleep = realSleep
	}
	reconnect := h.Reconnect
	if reconnect <= 0 {
		reconnect = wakerReconnect
	}
	for {
		if ctx.Err() != nil {
			return
		}
		// One attempt: run the events command until the child exits on
		// its own, ends on the runner's bound, or the context ends. A
		// missing or dying binary is the ordinary case: wait and retry.
		// The bound is the runner's own default, because the events
		// stream has no caller-specific bound of its own; when the
		// bound ends a healthy stream, the retry below restarts it.
		w := &lineSignal{ch: h.ch}
		_, _ = h.Runner.Run(ctx, []string{"events"}, nil, w, io.Discard, 0)
		if ctx.Err() != nil {
			return
		}
		if err := sleep(ctx, reconnect); err != nil {
			return
		}
	}
}
