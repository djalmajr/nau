package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeHubFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the hub file: %v", err)
	}
	return path
}

func TestLoadHubFileValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"https", "https://hub.example"},
		{"https with port", "https://hub.example:8443"},
		{"http loopback ip", "http://127.0.0.1:8443"},
		{"http loopback ip in the range", "http://127.0.0.7:8443"},
		{"http loopback v6", "http://[::1]:8443"},
		{"http localhost", "http://localhost:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeHubFile(t, `{"url":"`+tc.url+`","machine_id":"m1","key":"k1"}`)
			cfg, err := LoadHubFile(path)
			if err != nil {
				t.Fatalf("LoadHubFile: %v", err)
			}
			if cfg.HubURL != tc.url || cfg.MachineID != "m1" || cfg.Key != "k1" {
				t.Errorf("config = %+v", cfg)
			}
		})
	}
}

func TestLoadHubFileRefusesEmptyFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty url", `{"url":"","machine_id":"m1","key":"k1"}`},
		{"empty machine", `{"url":"https://hub.example","machine_id":"","key":"k1"}`},
		{"empty key", `{"url":"https://hub.example","machine_id":"m1","key":""}`},
		{"missing url", `{"machine_id":"m1","key":"k1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeHubFile(t, tc.content)
			if _, err := LoadHubFile(path); err == nil {
				t.Fatal("LoadHubFile accepted the file, want a refusal")
			}
		})
	}
}

func TestLoadHubFileRefusesBadURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"http off loopback", "http://other.example:8443"},
		{"http on a public hostname", "http://hub.example"},
		{"ftp", "ftp://hub.example"},
		{"no scheme", "hub.example"},
		{"user information", "https://user:pass@hub.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeHubFile(t, `{"url":"`+tc.url+`","machine_id":"m1","key":"k1"}`)
			if _, err := LoadHubFile(path); err == nil {
				t.Fatal("LoadHubFile accepted the URL, want a refusal")
			}
		})
	}
}

func TestLoadHubFileRefusesBadFile(t *testing.T) {
	if _, err := LoadHubFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("LoadHubFile accepted an absent file")
	}
	path := writeHubFile(t, `{"url": oops`)
	if _, err := LoadHubFile(path); err == nil {
		t.Error("LoadHubFile accepted a malformed file")
	}
}

func TestRunIntervalBounds(t *testing.T) {
	// A canceled context: Run that gets past the validation returns
	// the context error at once, without any subprocess or wait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := func(interval time.Duration) Config {
		return Config{
			HubURL:    "http://127.0.0.1:8443",
			MachineID: "m1",
			Key:       "k1",
			StatePath: filepath.Join(t.TempDir(), "health.json"),
			Interval:  interval,
		}
	}
	for _, interval := range []time.Duration{-1 * time.Second, 4 * time.Second, 31 * time.Second, time.Hour} {
		if err := Run(ctx, base(interval)); err == nil || !strings.Contains(err.Error(), "out of bounds") {
			t.Errorf("Run with interval %v: err = %v, want a configuration error", interval, err)
		}
	}
	// The valid bounds and the default get past the validation and
	// stop on the context, never on the config.
	for _, interval := range []time.Duration{0, 5 * time.Second, 15 * time.Second, 30 * time.Second} {
		if err := Run(ctx, base(interval)); !errors.Is(err, context.Canceled) {
			t.Errorf("Run with interval %v: err = %v, want the context error", interval, err)
		}
	}
}

func TestRunRefusesEmptyConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
	}{
		{"empty url", func(c *Config) { c.HubURL = "" }},
		{"empty machine", func(c *Config) { c.MachineID = "" }},
		{"empty key", func(c *Config) { c.Key = "" }},
		{"empty state path", func(c *Config) { c.StatePath = "" }},
		{"bad url", func(c *Config) { c.HubURL = "ftp://hub.example" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				HubURL:    "http://127.0.0.1:8443",
				MachineID: "m1",
				Key:       "k1",
				StatePath: filepath.Join(t.TempDir(), "health.json"),
			}
			tc.mut(&cfg)
			if err := Run(t.Context(), cfg); err == nil {
				t.Fatal("Run accepted the config, want a refusal")
			}
		})
	}
}

func TestHostOnly(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://hub.example", "https://hub.example"},
		{"https://hub.example/base/path", "https://hub.example"},
		{"https://hub.example/base?query=1#frag", "https://hub.example"},
		{"https://user:pass@hub.example:8443/x", "https://hub.example:8443"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080"},
	} {
		if got := hostOnly(tc.raw); got != tc.want {
			t.Errorf("hostOnly(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
