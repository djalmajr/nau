package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/djalmajr/nau/internal/config"
)

const (
	connectUsage        = "usage: connect <hub-url> [--name <nome>]"
	connectPollInterval = 2 * time.Second
	connectRequestTTL   = 10 * time.Second
	// connectExitInterrupted is the exit of the Ctrl-C: the command
	// stops without writing anything.
	connectExitInterrupted = 130
)

// hubJSONFile is the local machine credential, written to the config
// directory when the pairing is confirmed.
type hubJSONFile struct {
	URL       string `json:"url"`
	MachineID string `json:"machine_id"`
	Key       string `json:"key"`
}

// connectPost is the HTTP call of the command; the tests replace it to
// run against the fake hub.
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
	client := &http.Client{Timeout: connectRequestTTL}
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

// cmdConnect connects the machine to the hub: it requests the
// pairing, shows the code, polls the confirmation and stores the
// machine key in the config directory. The key never reaches stdout,
// stderr or a log; the success is one JSON line without it.
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

	// Step 1: request the pairing.
	status, body, err := connectPost(ctx, hubURL, "/api/pairings", struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil || status != http.StatusCreated {
		if ctx.Err() != nil {
			interrupted(env)
			return connectExitInterrupted
		}
		fail(env, 4, "connect: the hub did not accept the pairing")
		return 4
	}
	var created struct {
		Code      string `json:"code"`
		PollToken string `json:"poll_token"`
		ExpiresMS int64  `json:"expires_ms"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Code == "" || created.PollToken == "" {
		fail(env, 4, "connect: the hub did not accept the pairing")
		return 4
	}
	// The code and the instruction, on stderr: the key (to come) is
	// never shown.
	_, _ = fmt.Fprintf(env.Stderr, "%s: connect: confirme este código no hub, em Máquinas → Conectar máquina: %s\n", env.program(), created.Code)

	// Step 2: poll the confirmation, every 2 s, until the validity.
	for {
		select {
		case <-ctx.Done():
			// The interrupt stops the command: nothing is written.
			interrupted(env)
			return connectExitInterrupted
		default:
		}
		if env.Now().UnixMilli() >= created.ExpiresMS {
			fail(env, 3, "connect: the pairing expired")
			return 3
		}
		status, body, err = connectPost(ctx, hubURL, "/api/pairings/poll", struct {
			PollToken string `json:"poll_token"`
		}{PollToken: created.PollToken})
		if err != nil {
			if ctx.Err() != nil {
				interrupted(env)
				return connectExitInterrupted
			}
			fail(env, 4, "connect: the hub is unreachable")
			return 4
		}
		switch status {
		case http.StatusAccepted:
			if err := sleep(ctx, connectPollInterval); err != nil {
				interrupted(env)
				return connectExitInterrupted
			}
			continue
		case http.StatusOK:
			return storeHubKey(env, hubURL, body)
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

// storeHubKey reads the key of the confirmed pairing, writes hub.json
// to the config directory (atomic, 0600) and prints the success line
// without the key.
func storeHubKey(env Env, hubURL string, body []byte) int {
	var delivered struct {
		MachineID string `json:"machine_id"`
		Key       string `json:"key"`
	}
	if err := json.Unmarshal(body, &delivered); err != nil || delivered.MachineID == "" || delivered.Key == "" {
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
