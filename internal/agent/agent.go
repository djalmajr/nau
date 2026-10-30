package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/djalmajr/nau/internal/inventory"
	"github.com/djalmajr/nau/internal/soho"
)

// The read-side errors of the loop. They are categories, not free
// text: the health line and the error the loop reports never carry
// child output.
var (
	errSohoUnavailable = errors.New("agent: soho could not deliver the inventory")
	errBadInventory    = errors.New("agent: the inventory document is invalid")
)

// state is the loop's mutable part. It never holds more than the last
// inventory read: the buffer is the post body itself, and after a
// failure the next attempt re-reads and sends the newest.
type state struct {
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	http     *http.Client
	url      string
	key      string
	runner   soho.Runner
	hub      string // scheme + host, for the health line
	interval time.Duration

	state         string
	lastOKMS      int64
	lastAttemptMS int64
	lastError     string
	lastAttempt   time.Time
	backoff       time.Duration
	waitOverride  time.Duration // a Retry-After wait, independent of the backoff
	statePath     string
}

// Run drives the loop: it checks the inventory capability, then reads
// the inventory on the period and on waker wakes and posts it to the
// hub. It returns ErrRevoked when the hub revokes the key, the
// configuration error when the Config does not validate, and the
// context error when ctx ends.
func Run(ctx context.Context, cfg Config) error {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = realSleep
	}
	httpc := cfg.HTTP
	if httpc == nil {
		httpc = &http.Client{}
	}
	interval := cfg.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	if interval < MinInterval || interval > MaxInterval {
		return fmt.Errorf("agent: interval %s out of bounds [%s, %s]", interval, MinInterval, MaxInterval)
	}
	if cfg.HubURL == "" || cfg.MachineID == "" || cfg.Key == "" {
		return errors.New("agent: HubURL, MachineID and Key are required")
	}
	if err := checkHubURL(cfg.HubURL); err != nil {
		return err
	}
	if cfg.StatePath == "" {
		return errors.New("agent: StatePath is required")
	}

	runner := cfg.Runner
	if runner.Environ == nil {
		runner.Environ = os.Environ()
	}
	st := &state{
		now:       now,
		sleep:     sleep,
		http:      httpc,
		url:       cfg.HubURL,
		key:       cfg.Key,
		runner:    runner,
		hub:       hostOnly(cfg.HubURL),
		interval:  interval,
		state:     StateHubUnreachable, // the hub has not accepted this machine yet
		lastError: StateHubUnreachable,
		statePath: cfg.StatePath,
	}

	var wake <-chan struct{}
	var waker *HCEvents
	if cfg.HCBin != "" {
		// The waker runs under a child context: when the loop ends on
		// its own (a revoked key), the defer below cancels it so the
		// last events child ends and Run returns without waiting on
		// the caller's context.
		wctx, wcancel := context.WithCancel(ctx)
		hc := runner
		hc.Bin = cfg.HCBin
		waker = NewHCEvents(hc)
		waker.Sleep = sleep
		waker.Start(wctx)
		defer func() {
			wcancel()
			<-waker.Done()
		}()
		wake = waker.Wake()
	}

	if err := st.writeHealth(); err != nil {
		return err
	}

	// Startup: the inventory runs only when soho advertises it. Until
	// the capability appears, nothing is read or sent, the health says
	// so, and the capability is checked again on every period.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		caps, err := st.runner.Capabilities(ctx)
		if err != nil {
			st.setState(StateSohoUnavailable)
		} else if !caps.Has("inventory") {
			st.setState(StateSohoLacksInventory)
		} else {
			break
		}
		if err := st.writeHealth(); err != nil {
			return err
		}
		if _, err := st.waitNext(ctx, st.interval, wake); err != nil {
			return err
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := st.cycle(ctx); errors.Is(err, ErrRevoked) {
			// The health says revoked before the loop reports it.
			if werr := st.writeHealth(); werr != nil {
				return werr
			}
			return ErrRevoked
		}
		if err := st.writeHealth(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return ctx.Err()
		}
		d := st.nextWait()
		until := st.now().Add(d)
		woke, err := st.waitNext(ctx, d, wake)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !woke {
			st.clearWaitOverride()
			continue
		}
		if st.backoff > 0 || st.waitOverride > 0 {
			// A backoff or a Retry-After is not interrupted by a wake:
			// sleep the remainder and take the next read on schedule.
			if rem := until.Sub(st.now()); rem > 0 {
				if err := st.sleep(ctx, rem); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			st.clearWaitOverride()
			continue
		}
		// A wake-driven read, at most one every minWakeGap: a burst of
		// lines coalesces into reads spaced by the gap, never closer.
		if rem := minWakeGap - st.now().Sub(st.lastAttempt); rem > 0 {
			if err := st.sleep(ctx, rem); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

// nextWait is the wait before the next read: a Retry-After override,
// then the backoff, then the period. It peeks: the override is
// cleared when the wait has been served, so a wake arriving inside a
// backoff or a Retry-After still sees it as active.
func (st *state) nextWait() time.Duration {
	if st.waitOverride > 0 {
		return st.waitOverride
	}
	if st.backoff > 0 {
		return st.backoff
	}
	return st.interval
}

func (st *state) clearWaitOverride() {
	st.waitOverride = 0
}

// cycle runs one read and, when the read succeeded, one send. It sets
// the health state and returns ErrRevoked when the hub revoked the
// key.
func (st *state) cycle(ctx context.Context) error {
	st.lastAttempt = st.now()
	st.lastAttemptMS = st.lastAttempt.UnixMilli()
	body, err := st.readInventory(ctx)
	if err != nil {
		st.setStateFromError(err)
		return nil
	}
	if err := st.send(ctx, body); errors.Is(err, ErrRevoked) {
		return ErrRevoked
	}
	return nil
}

// readInventory runs the inventory command under the read bound,
// validates the document and returns the bytes exactly as soho
// printed them.
func (st *state) readInventory(ctx context.Context) ([]byte, error) {
	var out bytes.Buffer
	exit, err := st.runner.Run(ctx, []string{"inventory", "--json"}, nil, &out, io.Discard, readBound)
	if err != nil {
		// Missing binary, bound expired, or a signal: whatever the
		// cause, soho did not deliver.
		return nil, errSohoUnavailable
	}
	// The delivery to out may still be in flight when Run returns; the
	// bytes are complete only once the drain is done.
	select {
	case <-st.runner.DrainDone():
	case <-time.After(readBound):
		return nil, errSohoUnavailable
	}
	if exit != 0 {
		// 4 is the documented "terminal server cannot be read"; any
		// other non-zero code, soho did not deliver either.
		return nil, errSohoUnavailable
	}
	if _, err := inventory.Parse(out.Bytes()); err != nil {
		return nil, errBadInventory
	}
	return out.Bytes(), nil
}

// send posts the inventory to the hub and maps the answer to the wait
// and the health state the loop takes from it. It returns ErrRevoked
// on 401.
func (st *state) send(ctx context.Context, body []byte) error {
	sctx, cancel := context.WithTimeout(ctx, sendBound)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, st.url+ingestPath, bytes.NewReader(body))
	if err != nil {
		// The URL validated in Run; this fails on the context.
		st.setState(StateHubUnreachable)
		return err
	}
	req.Header.Set("Authorization", "Bearer "+st.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := st.http.Do(req)
	if err != nil {
		// The network, or the bound: back off.
		st.setState(StateHubUnreachable)
		st.backoff = st.nextBackoff()
		return err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused; the
	// answer body is never kept.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted:
		st.lastOKMS = st.now().UnixMilli()
		st.backoff = 0
		st.setState(StateOK)
	case resp.StatusCode == http.StatusUnauthorized:
		st.setState(StateRevoked)
		return ErrRevoked
	case resp.StatusCode == http.StatusConflict:
		// The hub did not accept it; the next read retries. The answer
		// is a hub answer, so the network backoff is dropped and the
		// period decides the next attempt. The state keeps the last
		// one: a 409 has no category of its own.
		st.backoff = 0
	case resp.StatusCode == http.StatusTooManyRequests:
		st.backoff = 0
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
				st.waitOverride = time.Duration(n) * time.Second
			}
		}
		if st.waitOverride == 0 {
			st.waitOverride = st.nextBackoff()
		}
		st.setState(StateRateLimited)
	default:
		// 5xx or anything else: back off.
		st.setState(StateHubUnreachable)
		st.backoff = st.nextBackoff()
	}
	return nil
}

// nextBackoff advances the exponential backoff, capped, and returns
// the new wait.
func (st *state) nextBackoff() time.Duration {
	if st.backoff == 0 {
		st.backoff = backoffStart
	} else {
		st.backoff *= 2
		if st.backoff > backoffCap {
			st.backoff = backoffCap
		}
	}
	return st.backoff
}

// setState records a new state. A non-ok state is its own last_error;
// ok clears it.
func (st *state) setState(s string) {
	st.state = s
	if s == StateOK {
		st.lastError = ""
	} else {
		st.lastError = s
	}
}

func (st *state) setStateFromError(err error) {
	switch {
	case errors.Is(err, errSohoUnavailable):
		st.setState(StateSohoUnavailable)
	case errors.Is(err, errBadInventory):
		st.setState(StateBadInventory)
	}
}

// waitNext sleeps at most d through the injected sleep, waking early
// when the waker signals or ctx ends. It reports the wake. When wake
// is nil it is the plain sleep. The wait sleep runs under a child
// context: a wake that wins the race cancels it, so the sleep is
// dropped instead of lingering as an abandoned wait.
func (st *state) waitNext(ctx context.Context, d time.Duration, wake <-chan struct{}) (bool, error) {
	if wake == nil {
		return false, st.sleep(ctx, d)
	}
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	done := make(chan struct{})
	go func() {
		_ = st.sleep(wctx, d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-wake:
		wcancel()
		return true, nil
	case <-done:
		return false, nil
	}
}
