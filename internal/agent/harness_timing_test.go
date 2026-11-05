package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// pausedDone holds Sleep after registration but before it can remove a
// canceled entry. Err still observes the underlying cancellation.
type pausedDone struct {
	context.Context
	entered chan struct{}
	resume  chan struct{}
}

func (c pausedDone) Done() <-chan struct{} {
	close(c.entered)
	<-c.resume
	return c.Context.Done()
}

func TestFakeSleepIgnoresCanceledEntries(t *testing.T) {
	for _, before := range []bool{true, false} {
		name := "after-registration"
		if before {
			name = "before-entry"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeSleep(testStart)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			paused := pausedDone{Context: ctx, entered: make(chan struct{}), resume: make(chan struct{})}
			var once sync.Once
			resume := func() { once.Do(func() { close(paused.resume) }) }
			done := make(chan error, 1)
			if before {
				cancel()
			}
			go func() { done <- f.Sleep(paused, 15*time.Second) }()
			t.Cleanup(func() {
				cancel()
				resume()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("Sleep returned %v, want cancellation", err)
					}
				case <-time.After(15 * time.Second):
					t.Error("Sleep did not stop after cancellation")
				}
			})
			select {
			case <-paused.entered:
			case <-time.After(15 * time.Second):
				t.Fatal("Sleep did not register")
			}
			f.mu.Lock()
			canceledCh := f.pending[0].ch
			f.mu.Unlock()
			cancel()
			if f.hasPending(15 * time.Second) {
				t.Error("canceled sleep is visible as pending")
			}
			if err := f.release(15*time.Second, 0); err == nil {
				t.Error("release accepted only a canceled sleep")
			}
			if got := f.Now(); !got.Equal(testStart) {
				t.Errorf("canceled sleep moved clock to %v", got)
			}

			// A current period must win even while the older canceled entry
			// has not been removed by its goroutine.
			live := &fsleep{ctx: context.Background(), at: testStart.Add(2 * time.Second), d: 15 * time.Second, ch: make(chan struct{})}
			f.mu.Lock()
			f.pending = append(f.pending, live)
			f.mu.Unlock()
			if !f.hasPending(15 * time.Second) {
				t.Fatal("live sleep is not pending")
			}
			if err := f.release(15*time.Second, 0); err != nil {
				t.Fatal(err)
			}
			select {
			case <-live.ch:
			default:
				t.Error("live sleep was not released")
			}
			select {
			case <-canceledCh:
				t.Error("release closed the canceled sleep channel")
			default:
			}
			if got := f.Now(); !got.Equal(testStart.Add(17 * time.Second)) {
				t.Errorf("live period moved clock to %v", got)
			}
		})
	}
}
