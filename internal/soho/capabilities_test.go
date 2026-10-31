package soho_test

import (
	"context"
	"testing"

	"github.com/djalmajr/nau/internal/soho"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// TestSohoCapabilities: the capabilities client runs
// `capabilities --json` and parses the line leniently.
func TestSohoCapabilities(t *testing.T) {
	exe, dir := installFake(t, fakesoho.Rule{
		Argv:   []string{"capabilities", "--json"},
		Stdout: "{\"schema\":1,\"worker_collaboration\":1,\"inventory\":1}\n",
		Code:   0,
	})
	r := soho.Runner{Bin: exe, Environ: childEnv}
	caps, err := r.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	want := "{\"schema\":1,\"worker_collaboration\":1,\"inventory\":1}"
	if caps.Raw != want {
		t.Errorf("Raw = %q, want %q", caps.Raw, want)
	}
	if !caps.Has("inventory") {
		t.Errorf("caps %v: inventory must be present", caps.Raw)
	}
	if caps.Has("unknown_cap") {
		t.Errorf("caps %v: unknown capability reported present", caps.Raw)
	}
	calls, err := fakesoho.ReadCalls(dir)
	if err != nil {
		t.Fatalf("ReadCalls: %v", err)
	}
	if len(calls) != 1 || len(calls[0].Argv) != 2 || calls[0].Argv[0] != "capabilities" || calls[0].Argv[1] != "--json" {
		t.Errorf("argv = %v, want [capabilities --json]", calls)
	}
}

// TestSohoCapabilitiesNonJSON: a non-JSON line is an error, not a silent
// capability set.
func TestSohoCapabilitiesNonJSON(t *testing.T) {
	exe, _ := installFake(t, fakesoho.Rule{
		Argv:   []string{"capabilities", "--json"},
		Stdout: "not json\n",
		Code:   0,
	})
	r := soho.Runner{Bin: exe, Environ: childEnv}
	if _, err := r.Capabilities(context.Background()); err == nil {
		t.Error("Capabilities with non-JSON output: err = nil, want error")
	}
}

// TestSohoCapabilitiesNonZeroExit: a non-zero child exit is an error.
func TestSohoCapabilitiesNonZeroExit(t *testing.T) {
	exe, _ := installFake(t, fakesoho.Rule{
		Argv:   []string{"capabilities", "--json"},
		Stdout: "{}\n",
		Code:   1,
	})
	r := soho.Runner{Bin: exe, Environ: childEnv}
	if _, err := r.Capabilities(context.Background()); err == nil {
		t.Error("Capabilities exit 1: err = nil, want error")
	}
}
