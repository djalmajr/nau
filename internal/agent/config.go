// Package agent is the per-machine core of the hub: it reads the soho
// inventory (`soho inventory --json`, the contract in docs/inventory.md)
// and delivers it to the hub with the machine key, on a period and on
// terminal-server wakes. It keeps only the last inventory read, and the
// machine key never leaves the Authorization header.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/djalmajr/nau/internal/soho"
)

// The fixed bounds and periods of the loop.
const (
	// DefaultInterval is the default read period.
	DefaultInterval = 15 * time.Second
	// MinInterval and MaxInterval bound the accepted Interval.
	MinInterval = 5 * time.Second
	MaxInterval = 30 * time.Second
	// readBound bounds the inventory read.
	readBound = 10 * time.Second
	// sendBound bounds the ingest post.
	sendBound = 10 * time.Second
	// minWakeGap caps wake-driven reads: at most one read every 2 s.
	minWakeGap = 2 * time.Second
	// wakerReconnect is the wait between waker attempts when the
	// events binary is missing or exits.
	wakerReconnect = 60 * time.Second
	// backoffStart and backoffCap bound the exponential hub backoff:
	// 5 s, 10 s, 20 s, 40 s, 60 s, 60 s, and so on.
	backoffStart = 5 * time.Second
	backoffCap   = 60 * time.Second
	// retryAfterCap caps a Retry-After wait in any form.
	retryAfterCap = 60 * time.Second
	// ingestPath is the hub endpoint the inventory is posted to.
	ingestPath = "/api/ingest"
)

// ErrRevoked ends Run when the hub answers 401: the machine key was
// revoked. The message never names the key.
var ErrRevoked = errors.New("agent: the hub revoked the machine key")

// errHubURLInvalid is the fixed text of every hub URL parse or
// validation failure: the rejected URL, or a piece of it, never
// reaches the error.
var errHubURLInvalid = errors.New("agent: the hub URL is not valid")

// hubFile is the shape of the hub.json that another card writes.
type hubFile struct {
	URL       string `json:"url"`
	MachineID string `json:"machine_id"`
	Key       string `json:"key"`
}

// LoadHubFile reads the machine's hub file and returns the agent
// configuration it holds: the hub URL, the machine id and the key.
// Empty fields are refused, and the URL must be https://, or http://
// on loopback only. The Runner, StatePath, clock, sleep, client and
// interval come from the caller.
func LoadHubFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var f hubFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Config{}, fmt.Errorf("agent: hub file: %v", err)
	}
	if f.URL == "" || f.MachineID == "" || f.Key == "" {
		return Config{}, errors.New("agent: hub file: url, machine_id and key are required")
	}
	if err := checkHubURL(f.URL); err != nil {
		return Config{}, err
	}
	return Config{HubURL: f.URL, MachineID: f.MachineID, Key: f.Key}, nil
}

// Config is the agent's configuration.
type Config struct {
	// HubURL is the hub base URL: https://, or http:// on loopback
	// only. The inventory is posted to HubURL + "/api/ingest".
	HubURL string
	// MachineID is the machine identity the hub knows the machine by.
	// The agent never posts, logs or writes it: the posted body is the
	// inventory verbatim and the header carries the key, which is how
	// the hub tells the machines apart.
	MachineID string
	// Key is the machine's hub key. It never appears in a log, an
	// error, the health file or a child environment; it leaves the
	// process only in the Authorization header.
	Key string
	// Runner executes the soho binary (argv subprocess, bounded). Its
	// Environ is the exact child environment; when it is nil, Run uses
	// the process environment.
	Runner soho.Runner
	// StatePath is the health file Run writes atomically on each
	// change.
	StatePath string
	// HCBin is the terminal-server events binary the waker runs as
	// HCBin + " events"; the usual name is "hc". The waker is used
	// only when this field is not empty; when it is, the agent runs on
	// the period alone. When the binary is missing or exits, the waker
	// retries every 60 s.
	HCBin string
	// Interval is the read period: 15 s when zero, 5 s to 30 s.
	// Outside that range Run reports a configuration error.
	Interval time.Duration
	// Now, when nil, is time.Now.
	Now func() time.Time
	// Sleep, when nil, sleeps until d elapses or ctx ends.
	Sleep func(ctx context.Context, d time.Duration) error
	// HTTP, when nil, is a plain *http.Client.
	HTTP *http.Client
}

// checkHubURL reports whether u is a hub URL the agent accepts:
// https:// anywhere, or http:// on a loopback host only. A URL with
// embedded user information is refused: the key travels in the
// Authorization header, never in the URL. Every parse or validation
// failure returns the same fixed text: the rejected URL, or a piece
// of it, is never reflected in the error.
func checkHubURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errHubURLInvalid
	}
	if u.User != nil {
		return errHubURLInvalid
	}
	switch u.Scheme {
	case "https":
		// accepted
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() {
				return nil
			}
			if v4 := ip.To4(); v4 != nil && v4[0] == 127 {
				// the whole 127/8 range is loopback
				return nil
			}
		}
		return errHubURLInvalid
	default:
		return errHubURLInvalid
	}
	if u.Hostname() == "" {
		return errHubURLInvalid
	}
	return nil
}

// hostOnly reduces a hub URL to its scheme and host for the health
// line: no path, no query, no user information.
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// realSleep sleeps until d elapses or ctx ends; it is the default Sleep.
func realSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
