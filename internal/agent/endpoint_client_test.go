package agent

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
)

// TestIngestEndpointKeepsEncodedBaseSeparators checks the joins of the
// base URL: with and without the trailing slash, with a base path, with
// an encoded separator in the base path, which stays encoded, and with
// segments the join must not normalize (a // and a ..).
func TestIngestEndpointKeepsEncodedBaseSeparators(t *testing.T) {
	cases := []struct{ base, want string }{
		{"https://hub.example", "https://hub.example/api/ingest"},
		{"https://hub.example/", "https://hub.example/api/ingest"},
		{"https://hub.example/base/", "https://hub.example/base/api/ingest"},
		{"https://hub.example/base%2Ftenant", "https://hub.example/base%2Ftenant/api/ingest"},
		{"https://hub.example/base%2Ftenant/", "https://hub.example/base%2Ftenant/api/ingest"},
		{"https://hub.example/a//b", "https://hub.example/a//b/api/ingest"},
		{"https://hub.example/a/../b/", "https://hub.example/a/../b/api/ingest"},
		{"https://hub.example/caf%C3%A9", "https://hub.example/caf%C3%A9/api/ingest"},
	}
	for _, c := range cases {
		got, err := ingestEndpoint(c.base)
		if err != nil {
			t.Fatalf("ingestEndpoint(%q): %v", c.base, err)
		}
		if got != c.want {
			t.Errorf("ingestEndpoint(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

// TestAgentTransportLeavesTheCallerClient checks that the policy goes
// on a copy: the caller's client keeps its own redirect hook, and two
// runs sharing one client do not write it (the race detector sees a
// write to the shared client otherwise).
func TestAgentTransportLeavesTheCallerClient(t *testing.T) {
	errHook := errors.New("the caller's hook")
	hook := func(*http.Request, []*http.Request) error { return errHook }
	shared := &http.Client{CheckRedirect: hook}

	cp := agentTransport(shared)
	if cp == shared {
		t.Fatal("agentTransport returned the caller's client, want a copy")
	}
	if err := shared.CheckRedirect(nil, nil); !errors.Is(err, errHook) {
		t.Fatalf("the caller's CheckRedirect = %v, want its own hook", err)
	}
	if err := cp.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("the agent's CheckRedirect = %v, want ErrUseLastResponse", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Run(ctx, Config{HTTP: shared, HubURL: "https://hub.example", MachineID: "m1", Key: testKey})
		}()
	}
	wg.Wait()
	if err := shared.CheckRedirect(nil, nil); !errors.Is(err, errHook) {
		t.Fatalf("after Run, the caller's CheckRedirect = %v, want its own hook", err)
	}
}
