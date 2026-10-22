package cli

// The DJA-337 round-2 unit tests of the a2a serve URL mask: the mask
// function (a real *url.Error of http.Post, a string with two URLs, a
// string without a URL, a message with a URL) and the handler wiring
// (the message and the string and error attribute values are masked,
// the other values are untouched).

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestA2AMaskURLs covers the mask function over the four cases of the
// card: a real *url.Error of http.Post to a loopback hook with a
// canary in the path, a string with two URLs, a string without a URL
// (unchanged) and a message with a URL.
func TestA2AMaskURLs(t *testing.T) {
	// A real *url.Error of http.Post: the transport fails at the
	// refused loopback port and the error text carries the URL.
	const hookURL = "http://127.0.0.1:9/hook/secret-canary"
	resp, err := http.Post(hookURL, "text/plain", strings.NewReader("x"))
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatalf("http.Post to the closed loopback port succeeded: %v", resp)
	}
	if err == nil {
		t.Fatalf("http.Post: no error, want the transport failure with the URL")
	}
	if _, ok := err.(*url.Error); !ok {
		t.Fatalf("http.Post error = %T (%v), want a *url.Error", err, err)
	}
	if got := a2aMaskURLs(err.Error()); strings.Contains(got, "secret-canary") || strings.Contains(got, "127.0.0.1:9/hook") || !strings.Contains(got, "<url>") {
		t.Fatalf("masked error = %q, want the canary and the URL gone and <url> in its place", got)
	}
	// A string with two URLs: both are replaced.
	if got := a2aMaskURLs("see http://a/one?k=v and https://b/two"); got != "see <url> and <url>" {
		t.Fatalf("two URLs = %q, want both masked", got)
	}
	// A string without a URL stays equal.
	const plain = "no urls in this line"
	if got := a2aMaskURLs(plain); got != plain {
		t.Fatalf("plain = %q, want unchanged %q", got, plain)
	}
	// A message with a URL: the URL is replaced in place.
	if got := a2aMaskURLs("push failed for http://x/y"); got != "push failed for <url>" {
		t.Fatalf("message = %q, want the URL masked", got)
	}
}

// TestA2AServeLoggerMasksURL proves the handler wiring: the message and
// the string and error attribute values are written masked, the other
// values are untouched.
func TestA2AServeLoggerMasksURL(t *testing.T) {
	var buf bytes.Buffer
	logger := a2aServeLogger(&buf)
	logger.Warn("push for http://a/one failed",
		"url", "http://a/one?token=canary",
		"error", &url.Error{Op: "Post", URL: "http://b/two", Err: io.ErrUnexpectedEOF},
		"count", 3,
		"ok", true)
	line := strings.TrimSpace(buf.String())
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("record %q is not JSON: %v", line, err)
	}
	if rec["msg"] != "push for <url> failed" {
		t.Fatalf("msg = %v, want the URL masked", rec["msg"])
	}
	if rec["url"] != "<url>" {
		t.Fatalf("url = %v, want <url>", rec["url"])
	}
	er, _ := rec["error"].(string)
	if er != `Post "<url>": unexpected EOF` {
		t.Fatalf("error = %v, want the masked error string", er)
	}
	if rec["count"] != float64(3) || rec["ok"] != true {
		t.Fatalf("count/ok = %v/%v, want the untouched values", rec["count"], rec["ok"])
	}
	buf.Reset()
	logger.Info("info for http://a/one")
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil || rec["level"] != "INFO" || rec["msg"] != "info for <url>" {
		t.Fatalf("INFO record = %q: %v", buf.String(), err)
	}
	line = buf.String()
	logger.Debug("debug for http://a/one")
	if buf.String() != line {
		t.Fatalf("DEBUG record reached writer: %q", buf.String())
	}
}

// SDK derivation must retain WARN filtering without losing the shared mask.
func TestA2AServeWarnHandler(t *testing.T) {
	for _, name := range []string{"root", "attrs", "group"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			delivery := a2aServeLogger(&buf)
			agent := slog.New(a2aWarnHandler{delivery.Handler()})
			switch name {
			case "attrs":
				agent = agent.With("endpoint", "https://example.test/attribute-canary")
			case "group":
				agent = agent.WithGroup("sdk").With("endpoint", "https://example.test/group-canary")
			}
			agent.Debug("SDK debug")
			agent.Info("SDK info")
			if buf.Len() != 0 {
				t.Fatalf("below-WARN SDK records reached stderr: %s", buf.String())
			}
			delivery.Info("a2a: delivery", "state", "accepted", "ref", "https://example.test/attempt-canary")
			agent.Warn("SDK warning https://example.test/message-canary", "url", "https://example.test/value-canary")
			agent.Error("SDK error")
			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != 3 {
				t.Fatalf("records=%s", buf.String())
			}
			for i, level := range []string{"INFO", "WARN", "ERROR"} {
				var rec map[string]any
				if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
					t.Fatal(err)
				}
				if rec["level"] != level {
					t.Fatalf("record %d=%s", i, lines[i])
				}
				if i == 0 && (rec["msg"] != "a2a: delivery" || rec["ref"] != "<url>") {
					t.Fatalf("attempt=%s", lines[i])
				}
				if i == 1 {
					if rec["msg"] != "SDK warning <url>" {
						t.Fatalf("warning=%s", lines[i])
					}
					attrs := rec
					if name == "group" {
						var ok bool
						attrs, ok = rec["sdk"].(map[string]any)
						if !ok {
							t.Fatalf("missing group: %s", lines[i])
						}
					}
					if attrs["url"] != "<url>" || (name != "root" && attrs["endpoint"] != "<url>") {
						t.Fatalf("attributes lost or unmasked: %s", lines[i])
					}
				}
			}
			if strings.Contains(buf.String(), "canary") || strings.Contains(buf.String(), "example.test") {
				t.Fatalf("URL leaked: %s", buf.String())
			}
		})
	}
}
