package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// recoverUsage is the usage text of the recovery command.
	recoverUsage = "usage: hub recover --url <url>"
	// recoverRequestTTL is the timeout of the single request of the
	// command, the same bound the connect gives its requests.
	recoverRequestTTL = 10 * time.Second
)

// recoverRoundTrip is the transport of the HTTP client of the
// command; the tests replace it with a recording fake that answers
// without sockets.
var recoverRoundTrip http.RoundTripper

// recoverPost is the HTTP call of the command; the tests replace it to
// run against the fake hub. The client follows no redirect: a 3xx is
// an error for the caller, and the token and the new password never
// travel to another origin.
var recoverPost = func(ctx context.Context, rawURL string, body any) (int, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: recoverRequestTTL,
	}
	if recoverRoundTrip != nil {
		client.Transport = recoverRoundTrip
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

// parseRecoverArgs accepts the --url flag, at most once, in the space
// form, followed by a value; anything else is malformed.
func parseRecoverArgs(args []string) (hubURL string, ok bool) {
	urlSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if urlSet || i+1 >= len(args) {
				return "", false
			}
			hubURL, urlSet = args[i+1], true
			i++
		default:
			return "", false
		}
	}
	return hubURL, urlSet
}

// validRecoverURL reports whether the recovery url is an http or https
// url whose host is a literal loopback ip (127.0.0.1, [::1]): the
// recovery route lives on the recovery port of the hub, bound to its
// loopback, and a remote or named host can never reach it.
func validRecoverURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// stdinIsTerminal reports whether stdin is a terminal device: reading
// it would echo the typed text, and the recovery refuses to read the
// terminal. A stdin that is not an *os.File (the test readers) is not
// a terminal.
func stdinIsTerminal(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// readRecoverLine reads one line of the recovery input and removes
// only the end of line (a final newline, with the carriage return of a
// CRLF). It reports false when the line is missing (the end of the
// input) or empty after the end of line.
func readRecoverLine(r *bufio.Reader) (string, bool) {
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, line != ""
}

// recoverErrCode reads the error code of one hub error answer
// ({"error":"..."}); the shape of a hostile hub body is validated
// before the code is used.
func recoverErrCode(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return ""
	}
	return e.Error
}

// recoverConfirm validates the answer of the recovery and prints the
// success line with the e-mail of the owner: the e-mail is the only
// value of the answer the command trusts before printing.
func recoverConfirm(env Env, body []byte) int {
	var got struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.User.Email == "" {
		fail(env, 4, "hub recover: the hub did not confirm the recovery")
		return 4
	}
	line, _ := json.Marshal(struct {
		Status string `json:"status"`
		Email  string `json:"email"`
	}{Status: "recovered", Email: got.User.Email})
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", line)
	return 0
}

// cmdHubRecover redefines the password of the owner of a running hub
// with the root token: it reads the token and the new password, one
// per line, from stdin (never the terminal), calls the recovery route
// of the recovery port of the hub without following redirects, and
// confirms with the e-mail of the owner. The url must be the loopback
// of the recovery port, so the token and the password never travel to
// a remote or named host. The command never prints the token or the
// password on its own; the e-mail of the success line comes from the
// answer of the hub. A bad usage, a terminal stdin, a missing or empty
// line, or a url that is not a loopback url exits 2, and a refused or
// unreachable hub exits 4.
func cmdHubRecover(args []string, env Env) int {
	hubURL, ok := parseRecoverArgs(args)
	if !ok {
		badUsage(env, recoverUsage)
		return 2
	}
	if !validRecoverURL(hubURL) {
		fail(env, 2, "hub recover: the url must be the loopback recovery port of the hub, such as http://127.0.0.1:7441: point the port-forward at the recovery port and define NAU_HUB_ROOT_TOKEN on the hub")
		return 2
	}
	if stdinIsTerminal(env.Stdin) {
		fail(env, 2, "hub recover: the token and the password must come from stdin, not the terminal")
		return 2
	}
	r := bufio.NewReader(env.Stdin)
	token, ok := readRecoverLine(r)
	if !ok {
		fail(env, 2, "hub recover: the first line of stdin must carry the root token")
		return 2
	}
	pass, ok := readRecoverLine(r)
	if !ok {
		fail(env, 2, "hub recover: the second line of stdin must carry the new password")
		return 2
	}
	// The route is joined on the escaped path of the url, as the
	// connect does: the base with the trailing slash or with a path
	// keeps the single slash of the route.
	rawURL, err := hubEndpoint(hubURL, "/api/session/root-token")
	if err != nil {
		fail(env, 2, "hub recover: the hub url is not valid")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), recoverRequestTTL)
	defer cancel()
	status, body, err := recoverPost(ctx, rawURL, struct {
		Token string `json:"token"`
		New   string `json:"new"`
	}{Token: token, New: pass})
	if err != nil {
		fail(env, 4, "hub recover: the hub is unreachable")
		return 4
	}
	switch status {
	case http.StatusOK:
		return recoverConfirm(env, body)
	case http.StatusNotFound:
		fail(env, 4, "hub recover: the port-forward must point at the recovery port of the hub, and NAU_HUB_ROOT_TOKEN must be defined on the hub")
		return 4
	case http.StatusUnauthorized:
		switch recoverErrCode(body) {
		case "invalid_token":
			fail(env, 4, "hub recover: the root token is not valid")
		case "unauthenticated":
			fail(env, 4, "hub recover: the password changed during the recovery; run it again")
		default:
			fail(env, 4, "hub recover: the hub refused the recovery")
		}
		return 4
	case http.StatusBadRequest:
		if recoverErrCode(body) == "weak_password" {
			fail(env, 4, "hub recover: the new password must be 12 to 128 bytes")
			return 4
		}
		fallthrough
	default:
		fail(env, 4, "hub recover: the hub refused the recovery")
		return 4
	}
}
