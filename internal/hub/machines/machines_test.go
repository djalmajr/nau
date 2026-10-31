package machines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// codeShape is the shape of the displayed pairing code: eight
// Crockford letters, XXXX-XXXX.
var codeShape = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// pollTokenShape is the shape of the poll token: the base64url
// without padding of 32 bytes.
var pollTokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// keyShape is the shape of the machine key: the base64url without
// padding of 32 bytes.
var keyShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ---- the fakes of the memory suite -----------------------------------

// fakeClock is the deterministic clock of the suites.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// detReader is the deterministic randomness source: it repeats the
// given bytes, so the tests are reproducible. The scenario suites use
// the counterReader instead (the repeating reader collides the random
// ids after a few draws).
type detReader struct {
	mu sync.Mutex
	b  []byte
	i  int
}

func (r *detReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		p[i] = r.b[r.i%len(r.b)]
		r.i++
	}
	return len(p), nil
}

// counterReader is the deterministic randomness source of the
// scenario suites: one byte of the high part of the SplitMix64
// stream with a fixed start, so the codes, the tokens and the ids
// never collide in the suites (the collision of the codes would
// break the uniqueness of the codes on the real store) and the tests
// stay reproducible.
type counterReader struct {
	mu sync.Mutex
	x  uint64
}

func (r *counterReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.x += 0x9E3779B97F4A7C15
		z := r.x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z = z ^ (z >> 31)
		p[i] = byte(z >> 56)
	}
	return len(p), nil
}

// stubStore is the hub store of the memory suite: the inventories
// never apply through it (the fake apply does), so every statement is
// a failure.
type stubStore struct{}

func (stubStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	return store.Result{}, errors.New("stub: not supported")
}
func (stubStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	return nil, errors.New("stub: not supported")
}
func (stubStore) Ping(ctx context.Context) error {
	return errors.New("stub: not supported")
}

// applyCall is one call of the fake apply.
type applyCall struct {
	machineID  string
	inv        inventory.Inventory
	receivedMS int64
}

// fakeApply is the scripted ingest.Apply of the memory suite.
type fakeApply struct {
	mu     sync.Mutex
	calls  []applyCall
	out    ingest.Result
	outSet bool
	err    error
}

func (f *fakeApply) run(ctx context.Context, st store.Store, machineID string, inv inventory.Inventory, receivedMS int64) (ingest.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, applyCall{machineID: machineID, inv: inv, receivedMS: receivedMS})
	if f.err != nil {
		return ingest.Result{}, f.err
	}
	out := f.out
	if !f.outSet {
		out.Applied = true
	}
	return out, nil
}

func (f *fakeApply) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// validInventoryBody is the body of a valid minimal inventory, with
// the generated_ms of the instant.
func validInventoryBody(generatedMS int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"schema":       1,
		"soho_version": "v0.1.0",
		"generated_ms": generatedMS,
		"projects":     []any{},
		"hidden":       map[string]int{"working": 0, "needs_you": 0, "done": 0, "idle": 0, "unknown": 0},
	})
	return body
}

// ---- the shared scenario surface --------------------------------------

// scenario is the shared surface of the suites: the server under
// test, the way to act as one of the users, the controllable clock and
// the observables. The memory suite fills mem and apply; the sqld
// suite leaves them nil and uses the real clock injection, the real
// accounts and the real ingest.Apply.
type scenario struct {
	t       *testing.T
	ctx     context.Context
	srv     *hub.Server
	store   MachineStore
	mem     *Memory
	apply   *fakeApply
	clock   *fakeClock
	users   map[string]accounts.User
	cookies map[string]string
}

// asUser builds the header that acts as the user of the name: the
// fake session header of the memory suite, the real session cookie of
// the sqld suite.
func (s *scenario) asUser(name string) hub.Header {
	h := hub.Header{}
	if s.mem != nil {
		h.Set("X-Test-User", name)
	} else {
		h.Set("Cookie", "nau_session="+s.cookies[name])
	}
	return h
}

// do runs one request against the server. ct sets the Content-Type
// application/json header when true.
func (s *scenario) do(method, path string, body []byte, hdr hub.Header, ct bool) hub.Response {
	if hdr == nil {
		hdr = hub.Header{}
	}
	if ct {
		hdr.Set("Content-Type", "application/json")
	}
	return s.srv.Serve(s.ctx, hub.Request{
		Method: method,
		Path:   path,
		Header: hdr,
		Body:   body,
	})
}

// doJSON is do with the JSON content type.
func (s *scenario) doJSON(method, path string, v any, hdr hub.Header) hub.Response {
	body, _ := json.Marshal(v)
	return s.do(method, path, body, hdr, true)
}

// nowMS is the clock of the scenario, in milliseconds.
func (s *scenario) nowMS() int64 { return s.clock.Now().UnixMilli() }

// advance moves the clock of the scenario.
func (s *scenario) advance(d time.Duration) { s.clock.Advance(d) }

// createPairing runs the pairing request and answers the parsed
// creation.
type createdPairing struct {
	Code      string `json:"code"`
	PollToken string `json:"poll_token"`
	ExpiresMS int64  `json:"expires_ms"`
}

func (s *scenario) createPairing(name string, hdr hub.Header) (hub.Response, createdPairing) {
	var c createdPairing
	resp := s.doJSON("POST", "/api/pairings", map[string]string{"name": name}, hdr)
	if resp.Status == 201 {
		_ = json.Unmarshal(resp.Body, &c)
	}
	return resp, c
}

// deliver runs one lifecycle (create, confirm, poll) as the user and
// answers the machine id and the key.
type delivered struct {
	MachineID string `json:"machine_id"`
	Key       string `json:"key"`
}

func (s *scenario) deliver(t *testing.T, user, name string) (delivered, createdPairing) {
	t.Helper()
	resp, c := s.createPairing(name, nil)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s, want 201", resp.Status, resp.Body)
	}
	r := s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": lowerNoHyphen(c.Code)}, s.asUser(user))
	if r.Status != 204 {
		t.Fatalf("confirm: %d %s, want 204", r.Status, r.Body)
	}
	r = s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 200 {
		t.Fatalf("poll: %d %s, want 200", r.Status, r.Body)
	}
	var d delivered
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatalf("poll body %s: %v", r.Body, err)
	}
	return d, c
}

// lowerNoHyphen is the lowercase form of the code without the hyphen.
func lowerNoHyphen(code string) string {
	out := make([]byte, 0, len(code))
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c == '-' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// bearer is the header of the machine key.
func bearer(key string) hub.Header {
	h := hub.Header{}
	h.Set("Authorization", "Bearer "+key)
	return h
}

// clientIP is one deterministic ip of the suites.
func clientIP(i int) string {
	return fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
}

// ipHeader is the header the hub serve adapter sets from the
// RemoteAddr of the connection (without the port): the suites set it
// the same way.
func ipHeader(ip string) hub.Header {
	h := hub.Header{}
	h.Set(RemoteIPHeader, ip)
	return h
}

// withValue returns h with one header added.
func withValue(h hub.Header, name, value string) hub.Header {
	h.Set(name, value)
	return h
}

// headerValues is the header of one name holding all the values (the
// hub Set replaces the values, it does not append them).
func headerValues(name string, values ...string) hub.Header {
	h := hub.Header{}
	for _, v := range values {
		k := strings.ToLower(name)
		h[k] = append(h[k], v)
	}
	return h
}

// ingestWith delivers one inventory with the machine key.
func (s *scenario) ingestWith(key string, body []byte) hub.Response {
	return s.do("POST", "/api/ingest", body, bearer(key), true)
}

// runMachinesScenarios is the shared scenario suite of the memory and
// the sqld implementations.
func runMachinesScenarios(t *testing.T, fresh func(t *testing.T) *scenario) {
	t.Run("PairingLifecycle", func(t *testing.T) { testPairingLifecycle(t, fresh) })
	t.Run("ConfirmErrors", func(t *testing.T) { testConfirmErrors(t, fresh) })
	t.Run("ConfirmRateLimit", func(t *testing.T) { testConfirmRateLimit(t, fresh) })
	t.Run("PollExpired", func(t *testing.T) { testPollExpired(t, fresh) })
	t.Run("CreateRateLimits", func(t *testing.T) { testCreateRateLimits(t, fresh) })
	t.Run("CreateCapacityRace", func(t *testing.T) { testCreateCapacityRace(t, fresh) })
	t.Run("ExpiredFreesSlots", func(t *testing.T) { testExpiredFreesSlots(t, fresh) })
	t.Run("ConfirmQuotaRace", func(t *testing.T) { testConfirmQuotaRace(t, fresh) })
	t.Run("IngestQuotaRace", func(t *testing.T) { testIngestQuotaRace(t, fresh) })
	t.Run("ClientIP", func(t *testing.T) { testClientIP(t, fresh) })
	t.Run("RevocationCutsImmediately", func(t *testing.T) { testRevocationCutsImmediately(t, fresh) })
	t.Run("ForeignUser", func(t *testing.T) { testForeignUser(t, fresh) })
	t.Run("KeyHasNoRead", func(t *testing.T) { testKeyHasNoRead(t, fresh) })
	t.Run("InvalidInventory", func(t *testing.T) { testInvalidInventory(t, fresh) })
	t.Run("IngestOutcomes", func(t *testing.T) { testIngestOutcomes(t, fresh) })
	t.Run("IngestRateLimit", func(t *testing.T) { testIngestRateLimit(t, fresh) })
	t.Run("ContentType", func(t *testing.T) { testContentType(t, fresh) })
	t.Run("StrictBody", func(t *testing.T) { testStrictBody(t, fresh) })
	t.Run("NameValidation", func(t *testing.T) { testNameValidation(t, fresh) })
	t.Run("StoreFailures", func(t *testing.T) { testStoreFailures(t, fresh) })
}

// testPairingLifecycle is the full flow: the machine requests the
// pairing, the developer confirms the code (lowercase, with the
// hyphen), the machine receives the key once, and the machine appears
// in the listing.
func testPairingLifecycle(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	resp, c := s.createPairing("laptop", nil)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s, want 201", resp.Status, resp.Body)
	}
	if !codeShape.MatchString(c.Code) {
		t.Fatalf("code %q does not match the shape XXXX-XXXX of the Crockford alphabet", c.Code)
	}
	if !pollTokenShape.MatchString(c.PollToken) {
		t.Fatalf("poll token %q does not match the base64url shape of 32 bytes", c.PollToken)
	}
	if c.ExpiresMS != s.nowMS()+pairingTTL.Milliseconds() {
		t.Fatalf("expires_ms = %d, want now + 10 min", c.ExpiresMS)
	}
	// The hub stores only the hashes: the memory store holds the
	// hashes of the code and of the token, never the values.
	if s.mem != nil {
		if p, ok := s.mem.PairingByCodeHash(hashHex([]byte(normalizeCode(c.Code)))); !ok || p.CodeHash != hashHex([]byte(normalizeCode(c.Code))) {
			t.Fatalf("the pairing of the code is not stored by the code hash")
		}
		if _, ok := s.mem.PairingByCodeHash(c.Code); ok {
			t.Fatalf("the raw code is stored, want only the hash")
		}
	}
	// While nobody confirmed: pending.
	r := s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 202 || string(r.Body) != `{"status":"pending"}` {
		t.Fatalf("poll before the confirm: %d %s, want 202 {\"status\":\"pending\"}", r.Status, r.Body)
	}
	// Without a session the confirm is refused.
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": lowerNoHyphen(c.Code)}, nil)
	if r.Status != 401 {
		t.Fatalf("confirm without session: %d, want 401", r.Status)
	}
	// The developer confirms the code, lowercase and with the hyphen.
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c.Code}, s.asUser("alice"))
	if r.Status != 204 {
		t.Fatalf("confirm: %d %s, want 204", r.Status, r.Body)
	}
	// The machine receives the key, once.
	r = s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 200 {
		t.Fatalf("poll after the confirm: %d %s, want 200", r.Status, r.Body)
	}
	var d delivered
	if err := json.Unmarshal(r.Body, &d); err != nil {
		t.Fatalf("poll body %s: %v", r.Body, err)
	}
	if !keyShape.MatchString(d.Key) {
		t.Fatalf("key %q does not match the base64url shape of 32 bytes", d.Key)
	}
	if d.MachineID == "" {
		t.Fatal("the machine id is empty")
	}
	// The key is stored only as the hash.
	if s.mem != nil {
		mcs := s.mem.Machines()
		if len(mcs) != 1 || mcs[0].KeyHash != shaOf(d.Key) || mcs[0].DeveloperID != s.users["alice"].ID {
			t.Fatalf("the machine = %+v, want the key hash and the developer", mcs)
		}
	}
	// The second poll is 404.
	r = s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 404 {
		t.Fatalf("second poll: %d %s, want 404", r.Status, r.Body)
	}
	// The machine is in the listing of the developer.
	r = s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 {
		t.Fatalf("machines: %d %s, want 200", r.Status, r.Body)
	}
	var list struct {
		Machines []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			CreatedMS  int64  `json:"created_ms"`
			Revoked    bool   `json:"revoked"`
			LastSeenMS *int64 `json:"last_seen_ms"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("machines body %s: %v", r.Body, err)
	}
	if len(list.Machines) != 1 {
		t.Fatalf("machines = %v, want 1", list.Machines)
	}
	m := list.Machines[0]
	if m.ID != d.MachineID || m.Name != "laptop" || m.Revoked || m.LastSeenMS != nil || m.CreatedMS != s.nowMS() {
		t.Fatalf("machine = %+v, want the delivered one, unrevoked, without inventories", m)
	}
	// Without a session the listing is refused.
	r = s.do("GET", "/api/machines", nil, nil, false)
	if r.Status != 401 {
		t.Fatalf("machines without session: %d, want 401", r.Status)
	}
}

// shaOf is the hex of the SHA-256 of the decoded key.
func shaOf(b64urlKey string) string {
	key, err := b64urlNoPadDecode(b64urlKey)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}

func b64urlNoPadDecode(s string) ([]byte, error) {
	return b64urlNoPad.DecodeString(s)
}

// testConfirmErrors is the invalid answers of the confirm: the unknown
// code, the code outside the alphabet, the expired code, and the
// double confirmation.
func testConfirmErrors(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	// The unknown code (inside the alphabet).
	r := s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("alice"))
	if r.Status != 400 || string(r.Body) != `{"error":"invalid_code"}` {
		t.Fatalf("unknown code: %d %s, want 400 invalid_code", r.Status, r.Body)
	}
	// The code outside the alphabet (I and L are not Crockford).
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "I000-L000"}, s.asUser("alice"))
	if r.Status != 400 {
		t.Fatalf("code outside the alphabet: %d %s, want 400 invalid_code", r.Status, r.Body)
	}
	// The expired code.
	_, c := s.createPairing("laptop", nil)
	s.advance(pairingTTL + time.Minute)
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c.Code}, s.asUser("alice"))
	if r.Status != 400 || string(r.Body) != `{"error":"invalid_code"}` {
		t.Fatalf("expired code: %d %s, want 400 invalid_code", r.Status, r.Body)
	}
	// The double confirmation: the second is invalid.
	s2 := fresh(t)
	_, c2 := s2.createPairing("laptop", nil)
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c2.Code}, s2.asUser("alice"))
	if r.Status != 204 {
		t.Fatalf("first confirm: %d %s, want 204", r.Status, r.Body)
	}
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c2.Code}, s2.asUser("bob"))
	if r.Status != 400 || string(r.Body) != `{"error":"invalid_code"}` {
		t.Fatalf("second confirm: %d %s, want 400 invalid_code", r.Status, r.Body)
	}
}

// testConfirmRateLimit is the per-user limit of the wrong
// confirmations: ten in the window, and the eleventh is 429, without
// affecting the other users; a correct confirmation clears the quota
// of the user.
func testConfirmRateLimit(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	for i := 0; i < maxConfirmErrors; i++ {
		r := s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("alice"))
		if r.Status != 400 {
			t.Fatalf("wrong confirm %d: %d %s, want 400", i+1, r.Status, r.Body)
		}
	}
	r := s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("alice"))
	if r.Status != 429 || string(r.Body) != `{"error":"rate_limited"}` {
		t.Fatalf("eleventh wrong confirm: %d %s, want 429 rate_limited", r.Status, r.Body)
	}
	// The limit is per user.
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("bob"))
	if r.Status != 400 {
		t.Fatalf("the other user: %d %s, want 400", r.Status, r.Body)
	}
	// The correct confirmation clears the quota of the user: nine
	// wrong confirmations, one correct one, and the full budget of
	// wrong confirmations again.
	s2 := fresh(t)
	for i := 0; i < maxConfirmErrors-1; i++ {
		r := s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s2.asUser("alice"))
		if r.Status != 400 {
			t.Fatalf("wrong confirm %d: %d %s, want 400", i+1, r.Status, r.Body)
		}
	}
	_, c := s2.createPairing("laptop", nil)
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c.Code}, s2.asUser("alice"))
	if r.Status != 204 {
		t.Fatalf("correct confirm: %d %s, want 204", r.Status, r.Body)
	}
	for i := 0; i < maxConfirmErrors; i++ {
		r := s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s2.asUser("alice"))
		if r.Status != 400 {
			t.Fatalf("wrong confirm after the clear %d: %d %s, want 400", i+1, r.Status, r.Body)
		}
	}
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s2.asUser("alice"))
	if r.Status != 429 || string(r.Body) != `{"error":"rate_limited"}` {
		t.Fatalf("wrong confirm after the cleared budget: %d %s, want 429 rate_limited", r.Status, r.Body)
	}
}

// testPollExpired is the poll of the pairing beyond the validity and
// the unknown token.
func testPollExpired(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	_, c := s.createPairing("laptop", nil)
	s.advance(pairingTTL + time.Minute)
	r := s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 410 || string(r.Body) != `{"error":"expired"}` {
		t.Fatalf("expired poll: %d %s, want 410 expired", r.Status, r.Body)
	}
	// The unknown token.
	r = s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAE="}, nil)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("unknown token: %d %s, want 404 not_found", r.Status, r.Body)
	}
	// The malformed token.
	r = s.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": "not-a-token"}, nil)
	if r.Status != 404 {
		t.Fatalf("malformed token: %d %s, want 404 not_found", r.Status, r.Body)
	}
}

// testCreateRateLimits is the creation limits: the creations of one
// client in the window (five, the sixth is 429) and the pending bound
// of the hub (the last admitted holds the bound, the next is 429).
func testCreateRateLimits(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	for i := 0; i < maxPairingsPerClient; i++ {
		r, _ := s.createPairingWithHeader(ipHeader(clientIP(1)), "laptop")
		if r.Status != 201 {
			t.Fatalf("create %d: %d %s, want 201", i+1, r.Status, r.Body)
		}
	}
	r, _ := s.createPairingWithHeader(ipHeader(clientIP(1)), "laptop")
	if r.Status != 429 || string(r.Body) != `{"error":"rate_limited"}` {
		t.Fatalf("creation above the per-client limit: %d %s, want 429 rate_limited", r.Status, r.Body)
	}
	// The other clients are not affected.
	r, _ = s.createPairingWithHeader(ipHeader(clientIP(2)), "laptop")
	if r.Status != 201 {
		t.Fatalf("the other client: %d %s, want 201", r.Status, r.Body)
	}
	// The window slides: after the window the client creates again.
	s.advance(pairingCreateWindow + time.Second)
	r, _ = s.createPairingWithHeader(ipHeader(clientIP(1)), "laptop")
	if r.Status != 201 {
		t.Fatalf("creation after the window: %d %s, want 201", r.Status, r.Body)
	}
	// The pending bound of the hub: the rows are seeded directly on
	// the store (the handler limit would refuse them), each with its
	// own client, and the creation of the handler is refused.
	s2 := fresh(t)
	for i := 0; i < maxPendingPairings; i++ {
		ok, err := s2.store.ReservePairing(s2.ctx, Pairing{
			ID:         fmt.Sprintf("seed-%d", i),
			CodeHash:   fmt.Sprintf("seed-code-%d", i),
			PollHash:   fmt.Sprintf("seed-poll-%d", i),
			Name:       "seed",
			ClientHash: hashHex([]byte(clientIP(i + 1000))),
			CreatedMS:  s2.nowMS(),
			ExpiresMS:  s2.nowMS() + pairingTTL.Milliseconds(),
		}, s2.nowMS())
		if err != nil || !ok {
			t.Fatalf("seed %d: %v (reserved=%v)", i+1, err, ok)
		}
	}
	r, _ = s2.createPairingWithHeader(ipHeader(clientIP(99999)), "laptop")
	if r.Status != 429 || string(r.Body) != `{"error":"rate_limited"}` {
		t.Fatalf("pending above the bound: %d %s, want 429 rate_limited", r.Status, r.Body)
	}
}

// testCreateCapacityRace is the adaptation of the probe of the
// review: the concurrent creations cannot exceed the pending bound of
// the hub; the admitted ones hold exactly the bound and the rest is
// refused with 429. The pool of workers keeps the concurrency of the
// local database server inside its default limit; the race is at the
// database level, where the reservation is atomic.
func testCreateCapacityRace(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	total := maxPendingPairings + 5
	statuses := make([]int, total)
	start := make(chan struct{})
	sem := make(chan struct{}, 100)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			sem <- struct{}{} // at most 100 requests in flight
			defer func() { <-sem }()
			r, _ := s.createPairingWithHeader(ipHeader(clientIP(i)), "laptop")
			statuses[i] = r.Status
		}(i)
	}
	close(start)
	wg.Wait()
	var admitted, limited int
	for _, st := range statuses {
		switch st {
		case 201:
			admitted++
		case 429:
			limited++
		default:
			t.Fatalf("concurrent create: status %d, want 201 or 429", st)
		}
	}
	if admitted != maxPendingPairings || limited != total-maxPendingPairings {
		t.Fatalf("concurrent creates: %d admitted, %d refused; want %d admitted and %d refused", admitted, limited, maxPendingPairings, total-maxPendingPairings)
	}
}

// testExpiredFreesSlots is the recovery: the pairings that expire do
// not count as pending — after the validity a new client is admitted
// again, without manual cleanup of the store.
func testExpiredFreesSlots(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	// Fill the pending bound with pairings of distinct clients.
	for i := 0; i < maxPendingPairings; i++ {
		if r, _ := s.createPairingWithHeader(ipHeader(clientIP(i)), "laptop"); r.Status != 201 {
			t.Fatalf("seed %d: %d %s, want 201", i+1, r.Status, r.Body)
		}
	}
	// The bound holds while the pairings are live.
	if r, _ := s.createPairingWithHeader(ipHeader(clientIP(maxPendingPairings+100)), "laptop"); r.Status != 429 {
		t.Fatalf("create while the bound is full: %d %s, want 429", r.Status, r.Body)
	}
	// After the validity the expired pairings go away and free the
	// slots.
	s.advance(pairingTTL + time.Second)
	if r, _ := s.createPairingWithHeader(ipHeader(clientIP(maxPendingPairings+101)), "laptop"); r.Status != 201 {
		t.Fatalf("create after the expiry: %d %s, want 201", r.Status, r.Body)
	}
}

// testConfirmQuotaRace is the adaptation of the probe of the review:
// the concurrent wrong confirmations of one user cannot exceed the
// limit; the counted ones hold exactly the limit and the rest is
// refused with 429.
func testConfirmQuotaRace(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	total := maxConfirmErrors + 2
	statuses := make([]int, total)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("alice")).Status
		}(i)
	}
	close(start)
	wg.Wait()
	var counted, limited int
	for _, st := range statuses {
		switch st {
		case 400:
			counted++
		case 429:
			limited++
		default:
			t.Fatalf("concurrent confirm: status %d, want 400 or 429", st)
		}
	}
	if counted != maxConfirmErrors || limited != total-maxConfirmErrors {
		t.Fatalf("concurrent confirms: %d counted, %d refused; want %d counted and %d refused", counted, limited, maxConfirmErrors, total-maxConfirmErrors)
	}
	// The next attempt of the user is refused while the window holds.
	if r := s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, s.asUser("alice")); r.Status != 429 {
		t.Fatalf("confirm after the race: %d %s, want 429", r.Status, r.Body)
	}
}

// testIngestQuotaRace is the adaptation of the probe of the review:
// the concurrent deliveries of one machine cannot exceed the limit;
// a delivery always counts, applied or refused.
func testIngestQuotaRace(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	total := maxDeliveries + 2
	// The old generated_ms keeps the real apply of the sqld suite on
	// the ignored outcome (202); the memory suite applies (204). The
	// quota is on the deliveries, not on the outcomes.
	base := s.nowMS() - time.Hour.Milliseconds()
	statuses := make([]int, total)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = s.ingestWith(d.Key, validInventoryBody(base+int64(i)*1000)).Status
		}(i)
	}
	close(start)
	wg.Wait()
	var admitted, limited int
	for _, st := range statuses {
		switch st {
		case 202, 204, 409:
			// Applied (202/204), or refused by the guard of the
			// concurrent apply of the same machine in the real store
			// (409); a refusal still consumes the reserved slot.
			admitted++
		case 429:
			limited++
		default:
			t.Fatalf("concurrent delivery: status %d, want 202/204, 409 or 429", st)
		}
	}
	if admitted != maxDeliveries || limited != total-maxDeliveries {
		t.Fatalf("concurrent deliveries: %d admitted, %d refused; want %d admitted and %d refused", admitted, limited, maxDeliveries, total-maxDeliveries)
	}
	// The next delivery of the machine is refused while the window
	// holds.
	if r := s.ingestWith(d.Key, validInventoryBody(base+int64(total)*1000)); r.Status != 429 {
		t.Fatalf("delivery after the race: %d %s, want 429", r.Status, r.Body)
	}
}

// testClientIP is the client of the creation limit: when the hub is
// configured with the trusted header, the ip of the request is the
// first value of the header when it validates as an ip, and it falls
// back to the RemoteAddr of the connection when it is absent or
// invalid. The pairing stores the ip only as the SHA-256 hash.
func testClientIP(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	// The trusted header: five creations of the ip fit, the sixth is
	// refused; the other ip is not affected.
	for i := 0; i < maxPairingsPerClient; i++ {
		if r, _ := s.createPairingWithHeader(headerValues("X-Test-Client-IP", "203.0.113.7"), "laptop"); r.Status != 201 {
			t.Fatalf("trusted create %d: %d %s, want 201", i+1, r.Status, r.Body)
		}
	}
	if r, _ := s.createPairingWithHeader(headerValues("X-Test-Client-IP", "203.0.113.7"), "laptop"); r.Status != 429 {
		t.Fatalf("trusted above the per-client limit: %d %s, want 429", r.Status, r.Body)
	}
	if r, _ := s.createPairingWithHeader(headerValues("X-Test-Client-IP", "203.0.113.9"), "laptop"); r.Status != 201 {
		t.Fatalf("the other ip: %d %s, want 201", r.Status, r.Body)
	}
	// The first value of the header is the one that counts: the
	// second one would still have room.
	for i := 0; i < maxPairingsPerClient; i++ {
		if r, _ := s.createPairingWithHeader(headerValues("X-Test-Client-IP", "203.0.113.11", "203.0.113.13"), "laptop"); r.Status != 201 {
			t.Fatalf("first-value create %d: %d %s, want 201", i+1, r.Status, r.Body)
		}
	}
	if r, _ := s.createPairingWithHeader(headerValues("X-Test-Client-IP", "203.0.113.11", "203.0.113.13"), "laptop"); r.Status != 429 {
		t.Fatalf("first-value above the per-client limit: %d %s, want 429 (the second value would have room)", r.Status, r.Body)
	}
	// An invalid value falls back to the RemoteAddr of the
	// connection: the two remote ips hold separate buckets.
	for i := 0; i < maxPairingsPerClient; i++ {
		if r, _ := s.createPairingWithHeader(withValue(ipHeader("198.51.100.5"), "X-Test-Client-IP", "not-an-ip"), "laptop"); r.Status != 201 {
			t.Fatalf("fallback create %d: %d %s, want 201", i+1, r.Status, r.Body)
		}
	}
	if r, _ := s.createPairingWithHeader(withValue(ipHeader("198.51.100.5"), "X-Test-Client-IP", "not-an-ip"), "laptop"); r.Status != 429 {
		t.Fatalf("fallback above the per-client limit: %d %s, want 429", r.Status, r.Body)
	}
	if r, _ := s.createPairingWithHeader(withValue(ipHeader("198.51.100.6"), "X-Test-Client-IP", "not-an-ip"), "laptop"); r.Status != 201 {
		t.Fatalf("the other remote ip: %d %s, want 201", r.Status, r.Body)
	}
	// The ip is stored only as the SHA-256 hash (the memory suite
	// holds the pairings; the SQL suite stores the same column).
	if s.mem != nil {
		raw := []string{"203.0.113.7", "203.0.113.9", "203.0.113.11", "203.0.113.13", "198.51.100.5", "198.51.100.6"}
		s.mem.mu.Lock()
		found := false
		for _, p := range s.mem.pairings {
			if p.ClientHash == hashHex([]byte("203.0.113.7")) {
				found = true
			}
			for _, ip := range raw {
				if p.ClientHash == ip {
					t.Fatal("the raw ip is stored in the pairing")
				}
			}
		}
		s.mem.mu.Unlock()
		if !found {
			t.Fatal("the pairing of the trusted ip is not stored by the hash of the ip")
		}
	}
}

// testRevocationCutsImmediately is the revocation: the machine is
// revoked and the next delivery of the inventory is refused on the
// spot.
func testRevocationCutsImmediately(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	// The delivery of the inventory goes through.
	r := s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
	if r.Status != 204 {
		t.Fatalf("ingest before the revocation: %d %s, want 204", r.Status, r.Body)
	}
	// The revocation.
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("revoke: %d %s, want 204", r.Status, r.Body)
	}
	// The next delivery is refused on the spot.
	r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
	if r.Status != 401 || string(r.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("ingest after the revocation: %d %s, want 401 unauthenticated", r.Status, r.Body)
	}
	// The machine shows revoked in the listing.
	r = s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 {
		t.Fatalf("machines: %d %s, want 200", r.Status, r.Body)
	}
	if jsonCountField(t, r.Body, "machines", "revoked", true) != 1 {
		t.Fatalf("machines body %s, want one revoked machine", r.Body)
	}
	// The second revocation is 204 (the operation is idempotent).
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 204 {
		t.Fatalf("second revoke: %d %s, want 204", r.Status, r.Body)
	}
}

// jsonCountField counts the items of the array field of the body that
// hold the bool field with the value want.
func jsonCountField(t *testing.T, body []byte, array, field string, want bool) int {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	var items []map[string]any
	if err := json.Unmarshal(root[array], &items); err != nil {
		t.Fatalf("the field %s of %s: %v", array, body, err)
	}
	n := 0
	for _, it := range items {
		if v, ok := it[field].(bool); ok && v == want {
			n++
		}
	}
	return n
}

// testForeignUser is the isolation of the machines between the users:
// the other user does not see nor revokes the machine, and the owner
// of the hub sees and revokes all of them.
func testForeignUser(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	db, _ := s.deliver(t, "bob", "bobbox")
	// The other user sees no machine.
	r := s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 {
		t.Fatalf("machines of alice: %d %s, want 200", r.Status, r.Body)
	}
	var list struct {
		Machines []any `json:"machines"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("machines body %s: %v", r.Body, err)
	}
	if len(list.Machines) != 0 {
		t.Fatalf("machines of alice = %v, want none", list.Machines)
	}
	// The other user does not revoke the machine (the 404 does not
	// reveal its existence).
	r = s.do("DELETE", "/api/machines/"+db.MachineID, nil, s.asUser("alice"), true)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("revoke of the foreign machine: %d %s, want 404 not_found", r.Status, r.Body)
	}
	// The machine is still active for the owner.
	r = s.ingestWith(db.Key, validInventoryBody(s.nowMS()))
	if r.Status != 204 {
		t.Fatalf("ingest after the refused revoke: %d %s, want 204", r.Status, r.Body)
	}
	// The owner of the hub sees all the machines.
	r = s.do("GET", "/api/machines", nil, s.asUser("owner"), false)
	if r.Status != 200 {
		t.Fatalf("machines of the owner: %d %s, want 200", r.Status, r.Body)
	}
	var ownerList struct {
		Machines []map[string]any `json:"machines"`
	}
	if err := json.Unmarshal(r.Body, &ownerList); err != nil {
		t.Fatalf("machines body %s: %v", r.Body, err)
	}
	if len(ownerList.Machines) != 1 || ownerList.Machines[0]["id"] != db.MachineID {
		t.Fatalf("machines of the owner = %v, want the machine of bob", ownerList.Machines)
	}
	// The owner of the hub revokes the machine.
	r = s.do("DELETE", "/api/machines/"+db.MachineID, nil, s.asUser("owner"), true)
	if r.Status != 204 {
		t.Fatalf("revoke by the owner: %d %s, want 204", r.Status, r.Body)
	}
	// The machine is revoked for its developer.
	r = s.ingestWith(db.Key, validInventoryBody(s.nowMS()))
	if r.Status != 401 {
		t.Fatalf("ingest after the owner revoke: %d %s, want 401", r.Status, r.Body)
	}
	// Without a session the revoke is refused.
	da, _ := s.deliver(t, "bob", "bobbox-2")
	r = s.do("DELETE", "/api/machines/"+da.MachineID, nil, nil, true)
	if r.Status != 401 {
		t.Fatalf("revoke without session: %d, want 401", r.Status)
	}
	// The unknown machine is 404.
	r = s.do("DELETE", "/api/machines/unknown-id", nil, s.asUser("owner"), true)
	if r.Status != 404 {
		t.Fatalf("revoke of the unknown machine: %d %s, want 404", r.Status, r.Body)
	}
}

// testKeyHasNoRead is the separation of the credentials: the machine
// key reads nothing (the session routes refuse it) and the session
// delivers no inventory (the delivery route reads only the key).
func testKeyHasNoRead(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	// The key does not read the session routes.
	r := s.do("GET", "/api/machines", nil, bearer(d.Key), false)
	if r.Status != 401 {
		t.Fatalf("GET /api/machines with the key: %d %s, want 401", r.Status, r.Body)
	}
	r = s.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": "0000-0000"}, bearer(d.Key))
	if r.Status != 401 {
		t.Fatalf("confirm with the key: %d %s, want 401", r.Status, r.Body)
	}
	// The session does not deliver the inventory.
	r = s.do("POST", "/api/ingest", validInventoryBody(s.nowMS()), s.asUser("alice"), true)
	if r.Status != 401 || string(r.Body) != `{"error":"unauthenticated"}` {
		t.Fatalf("ingest with the session: %d %s, want 401 unauthenticated", r.Status, r.Body)
	}
	// The session cookie on the pairing routes is ignored (they read
	// no session at all): the create works without one, and the poll
	// works with one.
	r, _ = s.createPairing("laptop", s.asUser("alice"))
	if r.Status != 201 {
		t.Fatalf("create with the session: %d %s, want 201", r.Status, r.Body)
	}
}

// testInvalidInventory is the body of the delivery: the invalid
// inventories are 400 without repeating the reason, and the valid one
// applies.
func testInvalidInventory(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	// The invalid JSON.
	r := s.ingestWith(d.Key, []byte("not json"))
	if r.Status != 400 || string(r.Body) != `{"error":"bad_inventory"}` {
		t.Fatalf("invalid json: %d %s, want 400 bad_inventory", r.Status, r.Body)
	}
	// The invalid schema: the reason of the refusal is not repeated.
	r = s.ingestWith(d.Key, []byte(`{"schema":2,"soho_version":"v0.1.0","generated_ms":1,"projects":[],"hidden":{"working":0,"needs_you":0,"done":0,"idle":0,"unknown":0}}`))
	if r.Status != 400 || string(r.Body) != `{"error":"bad_inventory"}` {
		t.Fatalf("invalid schema: %d %s, want 400 bad_inventory (without the reason)", r.Status, r.Body)
	}
	// The missing field.
	r = s.ingestWith(d.Key, []byte(`{"schema":1}`))
	if r.Status != 400 {
		t.Fatalf("missing fields: %d %s, want 400 bad_inventory", r.Status, r.Body)
	}
	// Nothing reached the apply.
	if s.apply != nil && s.apply.count() != 0 {
		t.Fatalf("apply calls = %d, want 0 (the invalid bodies do not apply)", s.apply.count())
	}
	// The valid one applies.
	r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
	if r.Status != 204 {
		t.Fatalf("valid inventory: %d %s, want 204", r.Status, r.Body)
	}
}

// testIngestOutcomes is the outcomes of the delivery: the applied one
// (204), the old one the call ignored (202) and the conflict (409).
func testIngestOutcomes(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	// The applied one: both suites, with the real or the fake apply.
	r := s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
	if r.Status != 204 {
		t.Fatalf("applied: %d %s, want 204", r.Status, r.Body)
	}
	if s.mem != nil {
		// The old one the call ignored (the fake apply reports the
		// ignored outcome).
		s.apply.out = ingest.Result{Applied: false}
		s.apply.outSet = true
		r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
		if r.Status != 202 {
			t.Fatalf("old inventory: %d %s, want 202", r.Status, r.Body)
		}
		// The conflict.
		s.apply.err = ingest.ErrConflict
		r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
		if r.Status != 409 || string(r.Body) != `{"error":"conflict"}` {
			t.Fatalf("conflict: %d %s, want 409 conflict", r.Status, r.Body)
		}
		// The store failure.
		s.apply.err = errors.New("boom")
		r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()))
		if r.Status != 503 {
			t.Fatalf("store failure: %d %s, want 503 unavailable", r.Status, r.Body)
		}
		return
	}
	// The sqld suite: the real apply. The older inventory is ignored.
	r = s.ingestWith(d.Key, validInventoryBody(s.nowMS()-30*60*1000))
	if r.Status != 202 {
		t.Fatalf("sqld old inventory: %d %s, want 202", r.Status, r.Body)
	}
	// The last-seen of the listing is the received_ms of the last
	// inventory (the applied one: the ignored one did not move the
	// snapshot).
	r = s.do("GET", "/api/machines", nil, s.asUser("alice"), false)
	if r.Status != 200 {
		t.Fatalf("machines: %d %s, want 200", r.Status, r.Body)
	}
	var list struct {
		Machines []struct {
			LastSeenMS *int64 `json:"last_seen_ms"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(r.Body, &list); err != nil {
		t.Fatalf("machines body %s: %v", r.Body, err)
	}
	if len(list.Machines) != 1 || list.Machines[0].LastSeenMS == nil || *list.Machines[0].LastSeenMS != s.nowMS() {
		t.Fatalf("last_seen_ms = %v, want the received_ms of the last inventory", list.Machines)
	}
}

// testIngestRateLimit is the per-machine limit of the deliveries:
// thirty in the minute, and every authenticated delivery counts,
// applied or refused.
func testIngestRateLimit(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	d, _ := s.deliver(t, "alice", "laptop")
	// The generated_ms advances one second per delivery, so the real
	// apply of the sqld suite applies every one (the fake apply of the
	// memory suite applies by default).
	base := s.nowMS()
	for i := 0; i < maxDeliveries-1; i++ {
		r := s.ingestWith(d.Key, validInventoryBody(base+int64(i+1)*1000))
		if r.Status != 204 {
			t.Fatalf("delivery %d: %d %s, want 204", i+1, r.Status, r.Body)
		}
	}
	// A refused delivery counts too: the bad body consumes one slot
	// of the window.
	r := s.ingestWith(d.Key, []byte("not json"))
	if r.Status != 400 || string(r.Body) != `{"error":"bad_inventory"}` {
		t.Fatalf("bad body: %d %s, want 400 bad_inventory", r.Status, r.Body)
	}
	if r := s.ingestWith(d.Key, validInventoryBody(base+int64(maxDeliveries)*1000)); r.Status != 429 || string(r.Body) != `{"error":"rate_limited"}` {
		t.Fatalf("delivery above the limit: %d %s, want 429 rate_limited", r.Status, r.Body)
	}
	// The window slides: the old deliveries leave the window.
	s.advance(deliveryWindow + time.Second)
	r = s.ingestWith(d.Key, validInventoryBody(base+int64(maxDeliveries+1)*1000))
	if r.Status != 204 {
		t.Fatalf("delivery after the window: %d %s, want 204", r.Status, r.Body)
	}
	// The limit is per machine.
	db, _ := s.deliver(t, "bob", "bobbox")
	r = s.ingestWith(db.Key, validInventoryBody(base+int64(maxDeliveries+2)*1000))
	if r.Status != 204 {
		t.Fatalf("the other machine: %d %s, want 204", r.Status, r.Body)
	}
}

// testContentType is the common protection: the writes of the API
// space need the Content-Type application/json.
func testContentType(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	r := s.do("POST", "/api/pairings", []byte(`{"name":"laptop"}`), nil, false)
	if r.Status != 415 || string(r.Body) != `{"error":"unsupported_media_type"}` {
		t.Fatalf("create without content type: %d %s, want 415", r.Status, r.Body)
	}
	r = s.do("POST", "/api/pairings/confirm", []byte(`{"code":"0000-0000"}`), s.asUser("alice"), false)
	if r.Status != 415 {
		t.Fatalf("confirm without content type: %d %s, want 415", r.Status, r.Body)
	}
	r = s.do("POST", "/api/pairings/poll", []byte(`{"poll_token":"x"}`), nil, false)
	if r.Status != 415 {
		t.Fatalf("poll without content type: %d %s, want 415", r.Status, r.Body)
	}
	d, _ := s.deliver(t, "alice", "laptop")
	r = s.do("POST", "/api/ingest", validInventoryBody(s.nowMS()), bearer(d.Key), false)
	if r.Status != 415 {
		t.Fatalf("ingest without content type: %d %s, want 415", r.Status, r.Body)
	}
	r = s.do("DELETE", "/api/machines/"+d.MachineID, nil, s.asUser("alice"), false)
	if r.Status != 415 {
		t.Fatalf("revoke without content type: %d %s, want 415", r.Status, r.Body)
	}
	// The content type with a parameter is accepted.
	hdr := hub.Header{}
	hdr.Set("Content-Type", "application/json; charset=utf-8")
	r, _ = s.createPairingWithHeader(hdr, "laptop")
	if r.Status != 201 {
		t.Fatalf("create with a parameterized content type: %d %s, want 201", r.Status, r.Body)
	}
}

func (s *scenario) createPairingWithHeader(hdr hub.Header, name string) (hub.Response, createdPairing) {
	var c createdPairing
	body, _ := json.Marshal(map[string]string{"name": name})
	resp := s.do("POST", "/api/pairings", body, hdr, true)
	if resp.Status == 201 {
		_ = json.Unmarshal(resp.Body, &c)
	}
	return resp, c
}

// testStrictBody is the strictness of the bodies: the unknown field,
// the invalid JSON and the trailing data are 400.
func testStrictBody(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	r := s.do("POST", "/api/pairings", []byte(`{"name":"laptop","extra":1}`), nil, true)
	if r.Status != 400 || string(r.Body) != `{"error":"bad_request"}` {
		t.Fatalf("unknown field: %d %s, want 400 bad_request", r.Status, r.Body)
	}
	r = s.do("POST", "/api/pairings", []byte(`{invalid`), nil, true)
	if r.Status != 400 {
		t.Fatalf("invalid json: %d %s, want 400 bad_request", r.Status, r.Body)
	}
	r = s.do("POST", "/api/pairings", []byte(`{"name":"laptop"} extra`), nil, true)
	if r.Status != 400 {
		t.Fatalf("trailing data: %d %s, want 400 bad_request", r.Status, r.Body)
	}
	_, c := s.createPairing("laptop", nil)
	r = s.do("POST", "/api/pairings/poll", []byte(`{"poll_token":"`+c.PollToken+`","extra":1}`), nil, true)
	if r.Status != 400 {
		t.Fatalf("unknown field of the poll: %d %s, want 400 bad_request", r.Status, r.Body)
	}
	// The missing token: the same answer of the unknown token.
	r = s.do("POST", "/api/pairings/poll", []byte(`{}`), nil, true)
	if r.Status != 404 || string(r.Body) != `{"error":"not_found"}` {
		t.Fatalf("the poll without the token: %d %s, want 404 not_found", r.Status, r.Body)
	}
}

// testNameValidation is the name of the pairing: one to 64 characters,
// without control characters.
func testNameValidation(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	cases := []struct {
		name   string
		status int
	}{
		{"", 400},
		{strings.Repeat("a", 65), 400},
		{"a\nb", 400},
		{"a\x00b", 400},
		{"laptop", 201},
		{"a", 201},
		{strings.Repeat("a", 64), 201},
		{strings.Repeat("é", 64), 201}, // 64 characters, 128 bytes
	}
	for i, c := range cases {
		r := s.doJSON("POST", "/api/pairings", map[string]string{"name": c.name}, nil)
		if r.Status != c.status {
			t.Fatalf("case %d (name %q): %d %s, want %d", i, c.name, r.Status, r.Body, c.status)
		}
	}
}

// testStoreFailures is the store failures of the memory suite: the
// 503 of the operations, and the 401 of the authentication that never
// reveals the failure.
func testStoreFailures(t *testing.T, fresh func(t *testing.T) *scenario) {
	s := fresh(t)
	if s.mem == nil {
		t.Skip("the store knobs are of the memory suite")
	}
	// The failure of the reservation of the pairing is a 503.
	s.mem.ReserveErr = errors.New("boom")
	r, _ := s.createPairing("laptop", nil)
	if r.Status != 503 {
		t.Fatalf("create on the store failure: %d %s, want 503", r.Status, r.Body)
	}
	s2 := fresh(t)
	_, c := s2.createPairing("laptop", nil)
	// The failure of the reservation of the confirm is a 503, without
	// proceeding to the lookup.
	s2.mem.ReserveConfirmErr = errors.New("boom")
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c.Code}, s2.asUser("alice"))
	if r.Status != 503 {
		t.Fatalf("confirm on the reservation failure: %d %s, want 503", r.Status, r.Body)
	}
	// The failure of the lookup is a 503.
	s2.mem.ReserveConfirmErr = nil
	s2.mem.FindPairErr = errors.New("boom")
	r = s2.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c.Code}, s2.asUser("alice"))
	if r.Status != 503 {
		t.Fatalf("confirm on the store failure: %d %s, want 503", r.Status, r.Body)
	}
	r = s2.doJSON("POST", "/api/pairings/poll", map[string]string{"poll_token": c.PollToken}, nil)
	if r.Status != 503 {
		t.Fatalf("poll on the store failure: %d %s, want 503", r.Status, r.Body)
	}
	// The failure of the clear of a successful confirm is a 503: the
	// error is not discarded.
	s3 := fresh(t)
	_, c3 := s3.createPairing("laptop", nil)
	s3.mem.ClearErr = errors.New("boom")
	r = s3.doJSON("POST", "/api/pairings/confirm", map[string]string{"code": c3.Code}, s3.asUser("alice"))
	if r.Status != 503 {
		t.Fatalf("confirm on the clear failure: %d %s, want 503", r.Status, r.Body)
	}
	s4 := fresh(t)
	d, _ := s4.deliver(t, "alice", "laptop")
	s4.mem.RevokeErr = errors.New("boom")
	r = s4.do("DELETE", "/api/machines/"+d.MachineID, nil, s4.asUser("alice"), true)
	if r.Status != 503 {
		t.Fatalf("revoke on the store failure: %d %s, want 503", r.Status, r.Body)
	}
	// The failure of the lookup of the key is a 401: the answer does
	// not reveal that the store failed.
	s5 := fresh(t)
	dd, _ := s5.deliver(t, "alice", "laptop")
	s5.mem.FindMachErr = errors.New("boom")
	r = s5.ingestWith(dd.Key, validInventoryBody(s5.nowMS()))
	if r.Status != 401 {
		t.Fatalf("ingest on the key lookup failure: %d %s, want 401", r.Status, r.Body)
	}
	// The failure of the reservation of the delivery is a 503.
	s6 := fresh(t)
	de, _ := s6.deliver(t, "alice", "laptop")
	s6.mem.ReserveDelErr = errors.New("boom")
	r = s6.ingestWith(de.Key, validInventoryBody(s6.nowMS()))
	if r.Status != 503 {
		t.Fatalf("ingest on the reservation failure: %d %s, want 503", r.Status, r.Body)
	}
}

// TestMemoryMachinesScenarios runs the whole scenario suite over the
// in-memory store: the sessions are the fake header of the suite, the
// inventories apply through the fake apply, and the clock is the
// injected one.
func TestMemoryMachinesScenarios(t *testing.T) {
	runMachinesScenarios(t, func(t *testing.T) *scenario {
		t.Helper()
		clock := newFakeClock()
		users := map[string]accounts.User{
			"owner": {ID: "user-owner", Email: "owner@example.test", EmailNorm: "owner@example.test", Name: "Owner", Role: accounts.RoleOwner},
			"alice": {ID: "user-alice", Email: "alice@example.test", EmailNorm: "alice@example.test", Name: "Alice", Role: accounts.RoleMember},
			"bob":   {ID: "user-bob", Email: "bob@example.test", EmailNorm: "bob@example.test", Name: "Bob", Role: accounts.RoleMember},
		}
		mem := NewMemory()
		apply := &fakeApply{}
		svc, err := New(Config{
			Store:          stubStore{},
			Machines:       mem,
			ClientIPHeader: "X-Test-Client-IP",
			SessionUser: func(ctx context.Context, req hub.Request) (accounts.User, bool) {
				n := req.Header.Get("X-Test-User")
				u, ok := users[n]
				return u, ok
			},
			Apply: apply.run,
			Now:   clock.Now,
			Rand:  &counterReader{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		srv := hub.New(hub.Deps{Store: stubStore{}, Now: clock.Now, Version: "dev"}, nil)
		svc.Register(srv)
		return &scenario{
			t:       t,
			ctx:     context.Background(),
			srv:     srv,
			store:   mem,
			mem:     mem,
			apply:   apply,
			clock:   clock,
			users:   users,
			cookies: map[string]string{},
		}
	})
}
