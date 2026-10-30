package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/testutil/fakeinventory"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// validFixture is the exact document the fake soho prints for the
// inventory command.
func validFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "valid.json"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	return data
}

// inventoryRules is the happy-path fake: capabilities advertises the
// inventory capability and the inventory command prints the fixture.
func inventoryRules() []fakesoho.Rule {
	return fakeinventory.Rules(filepath.Join("testdata", "valid.json"))
}

func ok204(int) (int, map[string]string) { return 204, nil }

// TestValidInventoryPostedVerbatim is the core contract: the hub
// receives the exact bytes soho printed, the key in the
// Authorization header, the content type, and the path.
func TestValidInventoryPostedVerbatim(t *testing.T) {
	fixture := validFixture(t)
	h := newHarness(t, inventoryRules(), ok204, "", nil)
	recs := h.waitForPosts(t, 1)
	if recs[0].Path != "/api/ingest" {
		t.Errorf("post path = %q, want %q", recs[0].Path, "/api/ingest")
	}
	if !bytes.Equal(recs[0].Body, fixture) {
		t.Errorf("the posted body must be the exact bytes soho printed\ngot:  %q\nwant: %q", recs[0].Body, fixture)
	}
	if recs[0].Auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want %q", recs[0].Auth, "Bearer "+testKey)
	}
	if recs[0].CT != "application/json" {
		t.Errorf("Content-Type = %q, want %q", recs[0].CT, "application/json")
	}
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateOK })
	if hh.LastError != "" || hh.LastOKMS <= 0 {
		t.Errorf("ok health = %+v, want last_error empty and last_ok_ms set", hh)
	}
	if hh.LastOKMS != h.fs.Now().UnixMilli() {
		t.Errorf("last_ok_ms = %d, want the fake clock now %d", hh.LastOKMS, h.fs.Now().UnixMilli())
	}
	if hh.Hub != h.hubURL {
		t.Errorf("hub = %q, want the scheme and host of %q", hh.Hub, h.hubURL)
	}
	// The machine key never appears in the health file.
	if raw, err := os.ReadFile(h.healthPath); err == nil && bytes.Contains(raw, []byte(testKey)) {
		t.Error("the health file carries the machine key")
	}
	h.cancel(t)
}

// TestNoInventoryCapabilitySendsNothing: without the capability,
// nothing is read or sent and the health says so, on every period.
func TestNoInventoryCapabilitySendsNothing(t *testing.T) {
	h := newHarness(t, fakeinventory.NoCapability(), ok204, "", nil)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateSohoLacksInventory })
	if hh.LastError != StateSohoLacksInventory || hh.LastAttemptMS != 0 {
		t.Errorf("lacks health = %+v, want last_error soho_lacks_inventory and no attempt", hh)
	}
	// Two more periods: the capability is re-checked, still nothing.
	for i := 0; i < 2; i++ {
		h.release(15 * time.Second)
	}
	h.waitForCalls(t, []string{"capabilities", "--json"}, 3)
	if got := h.posts(); len(got) != 0 {
		t.Fatalf("the hub received %d posts, want 0", len(got))
	}
	if got := countCalls(h.calls(t), "inventory", "--json"); got != 0 {
		t.Fatalf("the inventory ran %d times, want 0", got)
	}
	h.cancel(t)
}

// TestSohoExit4: soho exit 4 delivers nothing; the health says
// soho_unavailable and the next period retries the read.
func TestSohoExit4(t *testing.T) {
	rules := []fakesoho.Rule{{Argv: []string{"capabilities", "--json"}, Stdout: fakeinventory.CapabilitiesLine + "\n", Code: 0}}
	rules = append(rules, fakeinventory.Unavailable()...)
	h := newHarness(t, rules, ok204, "", nil)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateSohoUnavailable })
	if hh.LastError != StateSohoUnavailable || hh.LastAttemptMS != testStart.UnixMilli() {
		t.Errorf("unavailable health = %+v, want the first attempt at the clock origin", hh)
	}
	if got := h.posts(); len(got) != 0 {
		t.Fatalf("the hub received %d posts, want 0", len(got))
	}
	h.release(15 * time.Second)
	h.waitForCalls(t, []string{"inventory", "--json"}, 2)
	if got := h.posts(); len(got) != 0 {
		t.Fatalf("the hub received %d posts after the retry, want 0", len(got))
	}
	h.cancel(t)
}

// TestBadInventory: a document inventory.Parse refuses is recorded as
// bad_inventory and never sent; the next period re-reads.
func TestBadInventory(t *testing.T) {
	bad := `{"schema": 99}`
	rules := []fakesoho.Rule{
		{Argv: []string{"capabilities", "--json"}, Stdout: fakeinventory.CapabilitiesLine + "\n", Code: 0},
		{Argv: []string{"inventory", "--json"}, Stdout: bad, Code: 0},
	}
	h := newHarness(t, rules, ok204, "", nil)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateBadInventory })
	if hh.LastError != StateBadInventory || hh.LastAttemptMS != testStart.UnixMilli() {
		t.Errorf("bad-inventory health = %+v, want the first attempt at the clock origin", hh)
	}
	if got := h.posts(); len(got) != 0 {
		t.Fatalf("the hub received %d posts, want 0", len(got))
	}
	h.release(15 * time.Second)
	h.waitForCalls(t, []string{"inventory", "--json"}, 2)
	if got := h.posts(); len(got) != 0 {
		t.Fatalf("the hub received %d posts after the retry, want 0", len(got))
	}
	h.cancel(t)
}

// TestRevoked: a 401 ends Run with ErrRevoked after the health is
// written revoked; nothing more is sent.
func TestRevoked(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) { return 401, nil }, "", nil)
	if err := h.runError(t); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Run returned %v, want ErrRevoked", err)
	}
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateRevoked })
	if hh.LastError != StateRevoked || hh.LastOKMS != 0 {
		t.Errorf("revoked health = %+v, want last_error revoked and no last ok", hh)
	}
	// A beat for a (forbidden) further post: there is none.
	time.Sleep(100 * time.Millisecond)
	if got := h.posts(); len(got) != 1 {
		t.Fatalf("the hub received %d posts, want 1", len(got))
	}
	if raw, err := os.ReadFile(h.healthPath); err == nil && bytes.Contains(raw, []byte(testKey)) {
		t.Error("the health file carries the machine key")
	}
}

// TestRateLimitedWithRetryAfter: a 429 with Retry-After waits the
// announced seconds, records rate_limited, and recovers to ok.
func TestRateLimitedWithRetryAfter(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) {
		if n == 1 {
			return 429, map[string]string{"Retry-After": "7"}
		}
		return 204, nil
	}, "", nil)
	h.waitForPosts(t, 1)
	h.waitForHealth(t, func(hh Health) bool { return hh.State == StateRateLimited })
	h.release(7 * time.Second)
	recs := h.waitForPosts(t, 2)
	_ = recs
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateOK })
	if hh.LastOKMS != testStart.Add(7*time.Second).UnixMilli() {
		t.Errorf("last_ok_ms = %d, want the clock origin plus 7 s", hh.LastOKMS)
	}
}

// TestRateLimitedWithoutRetryAfterFallsBackToBackoff: a 429 without
// the header takes the backoff (5 s at the first).
func TestRateLimitedWithoutRetryAfterFallsBackToBackoff(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) {
		if n == 1 {
			return 429, nil
		}
		return 204, nil
	}, "", nil)
	h.waitForPosts(t, 1)
	h.waitForHealth(t, func(hh Health) bool { return hh.State == StateRateLimited })
	h.release(5 * time.Second)
	h.waitForPosts(t, 2)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.State == StateOK })
	if hh.LastOKMS != testStart.Add(5*time.Second).UnixMilli() {
		t.Errorf("last_ok_ms = %d, want the clock origin plus 5 s", hh.LastOKMS)
	}
}

// TestBackoffRisesAndResets: network-side failures (5xx) step 5, 10,
// 20, 40, 60 and stop at 60; a success resets the sequence.
func TestBackoffRisesAndResets(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) {
		switch n {
		case 1, 2, 3, 4, 5, 7:
			return 500, nil
		default:
			return 204, nil
		}
	}, "", nil)
	h.waitForPosts(t, 1)
	h.waitForHealth(t, func(hh Health) bool { return hh.State == StateHubUnreachable })
	for _, w := range []time.Duration{5, 10, 20, 40, 60} {
		h.release(w * time.Second)
	}
	h.waitForPosts(t, 6)
	// 5+10+20+40+60 = 135 s after the origin.
	h.waitForHealth(t, func(hh Health) bool {
		return hh.State == StateOK && hh.LastAttemptMS == testStart.Add(135*time.Second).UnixMilli()
	})
	// The period runs again, fails once more, and the backoff has
	// reset: the next wait is 5 s, not 10 s.
	h.release(15 * time.Second)
	h.waitForPosts(t, 7)
	h.waitForHealth(t, func(hh Health) bool {
		return hh.State == StateHubUnreachable && hh.LastAttemptMS == testStart.Add(150*time.Second).UnixMilli()
	})
	h.release(5 * time.Second)
	h.waitForPosts(t, 8)
	h.waitForHealth(t, func(hh Health) bool {
		return hh.State == StateOK && hh.LastAttemptMS == testStart.Add(155*time.Second).UnixMilli()
	})
}

// TestOtherStatusBacksOff: an answer that is not 2xx, 401, 409 or 429
// (here a 404) takes the backoff like a network failure.
func TestOtherStatusBacksOff(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) {
		if n == 1 {
			return 404, nil
		}
		return 204, nil
	}, "", nil)
	h.waitForPosts(t, 1)
	h.waitForHealth(t, func(hh Health) bool { return hh.State == StateHubUnreachable })
	h.release(5 * time.Second)
	h.waitForPosts(t, 2)
	h.waitForHealth(t, func(hh Health) bool { return hh.State == StateOK })
}

// TestConflictRetriesOnPeriod: a 409 keeps the last state (it has no
// category of its own) and the next read happens on the period, not on
// the backoff.
func TestConflictRetriesOnPeriod(t *testing.T) {
	h := newHarness(t, inventoryRules(), func(n int) (int, map[string]string) {
		if n == 1 {
			return 409, nil
		}
		return 204, nil
	}, "", nil)
	h.waitForPosts(t, 1)
	hh := h.waitForHealth(t, func(hh Health) bool { return hh.LastAttemptMS == testStart.UnixMilli() })
	// The first attempt has no previous state: the initial
	// hub_unreachable stands until an answer has a category of its own.
	if hh.State != StateHubUnreachable {
		t.Errorf("health after the 409 = %+v, want the previous state kept", hh)
	}
	h.release(15 * time.Second)
	h.waitForPosts(t, 2)
	hh = h.waitForHealth(t, func(hh Health) bool { return hh.State == StateOK })
	if hh.LastOKMS != testStart.Add(15*time.Second).UnixMilli() {
		t.Errorf("last_ok_ms = %d, want the clock origin plus the period", hh.LastOKMS)
	}
}

// TestKeyNeverLeavesTheProcessSurfaces: the key is in the
// Authorization header only. It is not in the health file, not in the
// posted body, not in the child environment the fake logs, not in a
// runner diagnostic, and not in the error Run returns.
func TestKeyNeverLeavesTheProcessSurfaces(t *testing.T) {
	h := newHarness(t, inventoryRules(), ok204, "", nil)
	h.waitForPosts(t, 1)
	if recs := h.posts(); recs[0].Auth != "Bearer "+testKey {
		t.Fatalf("Authorization = %q, want the key header", recs[0].Auth)
	}
	if raw, err := os.ReadFile(h.healthPath); err == nil {
		if bytes.Contains(raw, []byte(testKey)) {
			t.Error("the health file carries the machine key")
		}
		if bytes.Contains(raw, []byte(testMachineID)) {
			t.Error("the health file carries the machine identity")
		}
	}
	for _, rec := range h.posts() {
		if bytes.Contains(rec.Body, []byte(testKey)) {
			t.Error("the posted body carries the machine key")
		}
	}
	for _, c := range h.calls(t) {
		for _, e := range c.Env {
			if strings.Contains(e, testKey) {
				t.Error("the child environment carries the machine key")
			}
		}
	}
	for _, d := range h.diags() {
		if strings.Contains(d, testKey) {
			t.Errorf("a runner diagnostic carries the machine key: %q", d)
		}
		if strings.Contains(d, testMachineID) {
			t.Errorf("a runner diagnostic carries the machine identity: %q", d)
		}
	}
	h.cancel(t)
	if err := h.runError(t); strings.Contains(err.Error(), testKey) {
		t.Errorf("the Run error carries the machine key: %v", err)
	}
}
