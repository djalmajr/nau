package machines

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/inventory"
)

// FuzzIngestHTTPFraming catches decoding just the first JSON value and
// ignoring trailing data. Only a suffix is varied: a prefix and suffix
// together could wrap the object in another valid inventory.
func FuzzIngestHTTPFraming(f *testing.F) {
	for _, suffix := range [][]byte{
		{}, []byte(" \t\r\n"), []byte("{}"), []byte(" null"),
		{0xff}, {0}, []byte("\v"), []byte("\u00a0"),
		[]byte("}"), []byte(","), []byte("/* trailing */"),
		bytes.Repeat([]byte(" "), 4096),
	} {
		f.Add(suffix)
	}
	f.Fuzz(func(t *testing.T, suffix []byte) {
		// Bound throughput independently of the separately tested 4 MiB limit.
		if len(suffix) > 4096 {
			t.Skip("suffix exceeds the 4096-byte campaign bound")
		}
		valid := true
		for _, b := range suffix {
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				valid = false
				break
			}
		}
		checkIngestHTTPFraming(t, nil, suffix, valid)
	})
}

func TestIngestHTTPPrecedingDocument(t *testing.T) {
	checkIngestHTTPFraming(t, []byte("{}\n"), nil, false)
}

func checkIngestHTTPFraming(t *testing.T, prefix, suffix []byte, valid bool) {
	t.Helper()
	clock := newFakeClock()
	mem := NewMemory()
	apply := &fakeApply{}
	// Synthetic fixture only; the real machine middleware resolves its hash.
	key := bytes.Repeat([]byte("k"), 32)
	hash := sha256.Sum256(key)
	const machineID = "machine-framing"
	mem.machines[machineID] = Machine{ID: machineID, DeveloperID: "user-framing", Name: "laptop-a", KeyHash: hex.EncodeToString(hash[:])}
	svc, err := New(Config{
		Store: stubStore{}, Machines: mem, Apply: apply.run, Now: clock.Now,
		SessionUser: func(context.Context, hub.Request) (accounts.User, bool) {
			return accounts.User{}, false
		},
	})
	if err != nil {
		t.Fatal("construct machine service")
	}
	srv := hub.New(hub.Deps{Store: stubStore{}, Now: clock.Now, Version: "dev"}, nil)
	svc.Register(srv)
	body := append(append(append([]byte(nil), prefix...), validInventoryBody(clock.Now().UnixMilli())...), suffix...)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(key))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	httpserve.New(srv).ServeHTTP(recorder, req)
	if !valid {
		if recorder.Code != http.StatusBadRequest || recorder.Body.String() != `{"error":"bad_inventory"}` {
			t.Errorf("framing refusal: status %d, want 400 and exact redacted bad_inventory body", recorder.Code)
		}
		if apply.count() != 0 {
			t.Error("refused framing reached application")
		}
		return
	}
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		t.Fatalf("valid framing: status %d, want 204 and empty body", recorder.Code)
	}
	calls := apply.callsLocked()
	if len(calls) != 1 {
		t.Fatalf("valid framing: apply calls %d, want 1", len(calls))
	}
	want := inventory.Inventory{Schema: 1, SohoVersion: "v0.1.0", GeneratedMS: clock.Now().UnixMilli(), Projects: []inventory.Project{}}
	if calls[0].machineID != machineID || calls[0].receivedMS != clock.Now().UnixMilli() || !reflect.DeepEqual(calls[0].inv, want) {
		t.Error("application did not receive the fixture machine, clock and complete minimal inventory")
	}
}
