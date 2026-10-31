package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"time"

	"github.com/djalmajr/nau/internal/config"
)

const (
	connectUsage        = "usage: connect <hub-url> [--name <nome>]"
	connectPollInterval = 2 * time.Second
	connectRequestTTL   = 10 * time.Second
	// connectDeadline is the local deadline of the command: 10
	// minutes from the start; the effective deadline is the smaller
	// between it and the expires_ms the hub answers. It bounds the
	// requests and the wait between the polls; the per-request
	// timeout protects only each request.
	connectDeadline = 10 * time.Minute
	// connectExitInterrupted is the exit of the Ctrl-C: the command
	// stops without writing anything.
	connectExitInterrupted = 130
)

// The shapes of the hub answers the command trusts before printing or
// storing anything: a hostile hub answer never reaches the terminal
// raw and never lands in the credential.
var (
	// connectCodeShape is the displayed form of the pairing code: the
	// eight letters of the Crockford alphabet the server uses.
	connectCodeShape = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	// connectMachineShape is the form of the machine id the server
	// issues.
	connectMachineShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// validConnectKey reports whether the hub-delivered key is the
// base64url without padding of exactly 32 bytes.
func validConnectKey(key string) bool {
	b, err := base64.RawURLEncoding.DecodeString(key)
	return err == nil && len(b) == 32
}

// hubJSONFile is the local machine credential, written to the config
// directory when the pairing is confirmed.
type hubJSONFile struct {
	URL       string `json:"url"`
	MachineID string `json:"machine_id"`
	Key       string `json:"key"`
}

// connectRoundTrip is the transport of the HTTP client of the
// command; the tests replace it with a recording fake that answers
// without sockets.
var connectRoundTrip http.RoundTripper

// connectPost is the HTTP call of the command; the tests replace it to
// run against the fake hub. The client follows no redirect: a 3xx is
// an error for the caller, and the poll token and the key never travel
// to another origin.
var connectPost = func(ctx context.Context, base, path string, body any) (int, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: connectRequestTTL,
	}
	if connectRoundTrip != nil {
		client.Transport = connectRoundTrip
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, out, nil
}

// connectSignal is the interrupt context of the command; the tests
// replace it with a context they cancel.
var connectSignal = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// parseConnectArgs accepts the hub url and the optional name, in the
// space form, each at most once; anything else is malformed.
func parseConnectArgs(args []string) (hubURL, name string, ok bool) {
	nameSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			if nameSet || i+1 >= len(args) {
				return "", "", false
			}
			name, nameSet = args[i+1], true
			i++
		default:
			if hubURL != "" {
				return "", "", false
			}
			hubURL = args[i]
		}
	}
	return hubURL, name, hubURL != ""
}

// validConnectURL reports whether the hub url is a https url or a
// http url of a loopback host.
func validConnectURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return true
		}
		return net.ParseIP(host).IsLoopback()
	}
	return false
}

// connectErrExit maps the failed context of the command: the local
// deadline is the expiry (3), the interrupt is the Ctrl-C (130).
func connectErrExit(env Env, ctx context.Context) int {
	if ctx.Err() == context.DeadlineExceeded {
		fail(env, 3, "connect: the pairing expired")
		return 3
	}
	interrupted(env)
	return connectExitInterrupted
}

// cmdConnect connects the machine to the hub: it requests the
// pairing, shows the code, polls the confirmation and stores the
// machine key in the config directory. The key never reaches stdout,
// stderr or a log; the success is one JSON line without it. The hub
// answers are validated before they are printed or stored, and the
// command gives up at the local deadline of 10 minutes from the start
// (or sooner, at the validity the hub answers).
func cmdConnect(args []string, env Env) int {
	hubURL, name, ok := parseConnectArgs(args)
	if !ok {
		badUsage(env, connectUsage)
		return 2
	}
	if !validConnectURL(hubURL) {
		fail(env, 2, "connect: the hub url must be https:// or http:// on a loopback host")
		return 2
	}
	if name == "" {
		cfg, err := config.Load(env.ConfigDir)
		if err != nil {
			fail(env, 2, "connect: "+err.Error())
			return 2
		}
		name = cfg.MachineLabel
	}
	if name == "" {
		fail(env, 2, "connect: set --name or the machine_label of the config")
		return 2
	}
	ctx, cancel := connectSignal()
	defer cancel()
	sleep := env.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	// The local deadline, in milliseconds on the clock of the
	// command: the hub's expires_ms tightens it when it is closer.
	deadlineMS := env.Now().UnixMilli() + connectDeadline.Milliseconds()
	// bounded returns a context that dies at the effective deadline:
	// the timeout is the remaining time on the clock of the command,
	// so the instant is the same.
	bounded := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, time.Duration(deadlineMS-env.Now().UnixMilli())*time.Millisecond)
	}

	// Step 1: request the pairing.
	reqCtx, cancelReq := bounded()
	status, body, err := connectPost(reqCtx, hubURL, "/api/pairings", struct {
		Name string `json:"name"`
	}{Name: name})
	cancelReq()
	if err != nil || status != http.StatusCreated {
		if ctx.Err() != nil {
			return connectErrExit(env, ctx)
		}
		fail(env, 4, "connect: the hub did not accept the pairing")
		return 4
	}
	var created struct {
		Code      string `json:"code"`
		PollToken string `json:"poll_token"`
		ExpiresMS int64  `json:"expires_ms"`
	}
	// The answer is validated before it is printed or used: the code
	// in the displayed alphabet, a non-empty poll token and a
	// positive validity. A hostile hub answer never reaches the
	// terminal raw.
	if err := json.Unmarshal(body, &created); err != nil ||
		!connectCodeShape.MatchString(created.Code) ||
		created.PollToken == "" ||
		created.ExpiresMS <= 0 {
		fail(env, 4, "connect: the hub did not accept the pairing")
		return 4
	}
	// The code and the instruction, on stderr: the key (to come) is
	// never shown.
	_, _ = fmt.Fprintf(env.Stderr, "%s: connect: confirme este código no hub, em Máquinas → Conectar máquina: %s\n", env.program(), created.Code)
	if created.ExpiresMS < deadlineMS {
		deadlineMS = created.ExpiresMS
	}

	// Step 2: poll the confirmation, every 2 s, until the deadline.
	for {
		select {
		case <-ctx.Done():
			return connectErrExit(env, ctx)
		default:
		}
		if env.Now().UnixMilli() >= deadlineMS {
			fail(env, 3, "connect: the pairing expired")
			return 3
		}
		reqCtx, cancelReq := bounded()
		status, body, err = connectPost(reqCtx, hubURL, "/api/pairings/poll", struct {
			PollToken string `json:"poll_token"`
		}{PollToken: created.PollToken})
		cancelReq()
		if err != nil {
			if ctx.Err() != nil {
				return connectErrExit(env, ctx)
			}
			fail(env, 4, "connect: the hub is unreachable")
			return 4
		}
		switch status {
		case http.StatusAccepted:
			if err := sleep(ctx, connectPollInterval); err != nil {
				return connectErrExit(env, ctx)
			}
			continue
		case http.StatusOK:
			return storeHubKey(ctx, env, hubURL, body)
		case http.StatusGone, http.StatusNotFound:
			fail(env, 3, "connect: the pairing expired")
			return 3
		default:
			fail(env, 4, "connect: the hub refused the poll")
			return 4
		}
	}
}

// interrupted is the stderr line of the Ctrl-C: nothing goes to
// stdout and nothing is written to disk.
func interrupted(env Env) {
	_, _ = fmt.Fprintf(env.Stderr, "%s: connect: interrupted\n", env.program())
}

// storeHubKey reads the key of the confirmed pairing, re-checks the
// context (a cancel that lands after the 200 exits without writing),
// validates the answer and writes hub.json to the config directory
// (atomic, 0600), and prints the success line without the key.
func storeHubKey(ctx context.Context, env Env, hubURL string, body []byte) int {
	if ctx.Err() != nil {
		interrupted(env)
		return connectExitInterrupted
	}
	var delivered struct {
		MachineID string `json:"machine_id"`
		Key       string `json:"key"`
	}
	// The answer is validated before it is stored or printed: the
	// machine id in the issued shape, and the key the base64url
	// without padding of exactly 32 bytes.
	if err := json.Unmarshal(body, &delivered); err != nil ||
		!connectMachineShape.MatchString(delivered.MachineID) ||
		!validConnectKey(delivered.Key) {
		fail(env, 4, "connect: the hub did not deliver the key")
		return 4
	}
	if err := writeHubJSON(env.ConfigDir, hubJSONFile{URL: hubURL, MachineID: delivered.MachineID, Key: delivered.Key}); err != nil {
		fail(env, 4, "connect: "+err.Error())
		return 4
	}
	line, _ := json.Marshal(struct {
		Status    string `json:"status"`
		MachineID string `json:"machine_id"`
	}{Status: "connected", MachineID: delivered.MachineID})
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", line)
	return 0
}

// writeHubJSON writes the machine credential to <dir>/hub.json: the
// directory is created when it does not exist (the first command of a
// fresh profile), a temp file in the same directory, the permission
// 0600, and the rename over the final name.
func writeHubJSON(dir string, file hubJSONFile) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(file)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "hub.json-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, "hub.json"))
}
