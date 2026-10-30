// Package machines is the machine-connect layer of the hub: the
// pairing codes that a machine requests and the developer confirms,
// the per-machine keys delivered once at the confirmation, the machine
// listing and revocation, and the delivery route of the inventories.
// It talks to the store contract and to the accounts' session
// resolver only: no http, no os, no network. It compiles for the
// wasip1 hub core.
package machines

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/inventory"
)

// The time and count bounds of the machine connect.
const (
	// pairingTTL is the validity of a pairing.
	pairingTTL = 10 * time.Minute
	// pairingCreateWindow is the window of the creation limit.
	pairingCreateWindow = 10 * time.Minute
	// maxPendingPairings is the bound of pending pairings of the hub.
	maxPendingPairings = 100
	// maxPairingsCreated is the bound of pairings created in the window.
	maxPairingsCreated = 30
	// confirmWindow is the window of the wrong-confirmation limit.
	confirmWindow = 15 * time.Minute
	// maxConfirmErrors is the bound of wrong confirmations of a user.
	maxConfirmErrors = 10
	// deliveryWindow is the window of the delivery limit.
	deliveryWindow = 1 * time.Minute
	// maxDeliveries is the bound of deliveries of a machine in the
	// window.
	maxDeliveries = 30
	// machineKeySize is the size, in bytes, of a machine key.
	machineKeySize = 32
	// pollTokenSize is the size, in bytes, of a poll token.
	pollTokenSize = 32
	// randomIDSize is the size, in bytes, of a random textual id.
	randomIDSize = 16
)

// Machine is one machine of the hub. KeyHash is the SHA-256 of the
// random key; the key itself was returned once, at the pairing
// delivery.
type Machine struct {
	ID          string
	DeveloperID string
	Name        string
	KeyHash     string
	CreatedMS   int64
	// RevokedMS is nil while the machine is active.
	RevokedMS *int64
}

// Pairing is one pairing of a machine: the random code the developer
// confirms and the random poll token the machine polls. Both are
// stored only as SHA-256 hashes. ApprovedBy is nil until the
// confirmation and MachineID is nil until the delivery of the key.
type Pairing struct {
	ID         string
	CodeHash   string
	PollHash   string
	Name       string
	CreatedMS  int64
	ExpiresMS  int64
	ApprovedBy *string
	MachineID  *string
	Consumed   bool
}

// MachineStore is the machine-connect storage. Every method is one
// unit of work on the database; the multi-statement operations (the
// pairing create with the retention, the confirm, the delivery) are
// atomic: either everything applies or nothing does.
type MachineStore interface {
	// CreatePairing stores one pairing and prunes the rows outside
	// the retention, in one atomic batch.
	CreatePairing(ctx context.Context, p Pairing, nowMS int64) error
	// PendingPairings counts the pairings that were not consumed.
	PendingPairings(ctx context.Context) (int, error)
	// PairingsCreatedSince counts the pairings created after the
	// instant.
	PairingsCreatedSince(ctx context.Context, sinceMS int64) (int, error)
	// FindPairingByCodeHash finds the pairing of a normalized code
	// hash.
	FindPairingByCodeHash(ctx context.Context, codeHash string) (Pairing, bool, error)
	// FindPairingByPollHash finds the pairing of a poll token hash.
	FindPairingByPollHash(ctx context.Context, pollHash string) (Pairing, bool, error)
	// FindPairingByID finds one pairing by its id.
	FindPairingByID(ctx context.Context, id string) (Pairing, bool, error)
	// ConfirmPairing links the pairing to the user when it is not
	// confirmed yet: true when the call confirmed it, false when the
	// pairing was already confirmed.
	ConfirmPairing(ctx context.Context, pairingID, userID string) (bool, error)
	// RecordPairingError stores one wrong confirmation and prunes the
	// rows outside the window, in one atomic batch.
	RecordPairingError(ctx context.Context, userID string, atMS int64) error
	// CountPairingErrors counts the wrong confirmations of the user
	// after the instant.
	CountPairingErrors(ctx context.Context, userID string, sinceMS int64) (int, error)
	// DeliverMachine consumes the pairing (exactly once) and stores
	// the machine in one atomic batch, guarded: true when the call
	// consumed the pairing, false when it was already consumed.
	DeliverMachine(ctx context.Context, pairingID string, m Machine) (bool, error)
	// FindMachineByKeyHash finds the machine of a key hash.
	FindMachineByKeyHash(ctx context.Context, keyHash string) (Machine, bool, error)
	// FindMachineByID finds one machine.
	FindMachineByID(ctx context.Context, id string) (Machine, bool, error)
	// ListMachinesByDeveloper lists the machines of one developer.
	ListMachinesByDeveloper(ctx context.Context, developerID string) ([]Machine, error)
	// ListAllMachines lists every machine of the hub.
	ListAllMachines(ctx context.Context) ([]Machine, error)
	// RevokeMachine revokes the machine when it is not revoked yet:
	// true when the call revoked it, false when it was already
	// revoked.
	RevokeMachine(ctx context.Context, id string, atMS int64) (bool, error)
	// LastSeen returns, for each of the machine ids that holds at
	// least one inventory snapshot, the received_ms of its last
	// inventory.
	LastSeen(ctx context.Context, machineIDs []string) (map[string]int64, error)
	// RecordDelivery stores one accepted delivery and prunes the rows
	// outside the window, in one atomic batch.
	RecordDelivery(ctx context.Context, machineID string, atMS int64) error
	// CountDeliveries counts the accepted deliveries of a machine
	// after the instant.
	CountDeliveries(ctx context.Context, machineID string, sinceMS int64) (int, error)
}

// SessionUser resolves the signed-in user of a request, or false. The
// production wiring passes the accounts resolver.
type SessionUser func(ctx context.Context, req hub.Request) (accounts.User, bool)

// ApplyFunc applies one inventory of a machine to the hub state.
type ApplyFunc func(ctx context.Context, st store.Store, machineID string, inv inventory.Inventory, receivedMS int64) (ingest.Result, error)

// Config is the configuration of the machine connect: the hub store
// (the deliveries of the inventories apply through it), the machine
// storage, the session resolver, the time and the randomness, injected
// so the tests are deterministic.
type Config struct {
	// Store is the hub store the inventories apply to.
	Store store.Store
	// Machines is the machine-connect storage.
	Machines MachineStore
	// SessionUser resolves the signed-in user of a request.
	SessionUser SessionUser
	// Apply applies one inventory; nil uses ingest.Apply.
	Apply ApplyFunc
	// Now is the clock.
	Now func() time.Time
	// Rand is the randomness source; nil uses crypto/rand.
	Rand io.Reader
}

// Service is the machine-connect layer of the hub: the pairings, the
// machines and the delivery of the inventories, on top of one
// MachineStore. It holds no state between requests.
type Service struct {
	store       store.Store
	machines    MachineStore
	sessionUser SessionUser
	apply       ApplyFunc
	now         func() time.Time
	rand        io.Reader
}

// New builds the machine-connect layer. A missing store, machine store
// or session resolver is an error.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("machines: the store is required")
	}
	if cfg.Machines == nil {
		return nil, errors.New("machines: the machine store is required")
	}
	if cfg.SessionUser == nil {
		return nil, errors.New("machines: the session resolver is required")
	}
	apply := cfg.Apply
	if apply == nil {
		apply = ingest.Apply
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	rnd := cfg.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	return &Service{
		store:       cfg.Store,
		machines:    cfg.Machines,
		sessionUser: cfg.SessionUser,
		apply:       apply,
		now:         now,
		rand:        rnd,
	}, nil
}

// Register adds the machine-connect routes to the hub server. The
// machine key authenticates the delivery route only (RequireMachine is
// used there and nowhere else); the other routes read the session
// cookie and ignore the Authorization header.
func (m *Service) Register(s *hub.Server) {
	s.Handle("POST", "/api/pairings", m.createPairing)
	s.Handle("POST", "/api/pairings/confirm", m.confirmPairing)
	s.Handle("POST", "/api/pairings/poll", m.pollPairing)
	s.Handle("GET", "/api/machines", m.listMachines)
	s.Handle("DELETE", "/api/machines/{id}", m.revokeMachine)
	s.Handle("POST", "/api/ingest", m.RequireMachine(m.ingestDeliveries))
}

// RequireMachine wraps the delivery handler with the machine
// authentication: the Authorization header must carry the Bearer key
// of the machine, looked up by its hash; an unknown, malformed or
// revoked machine is 401 unauthenticated, without revealing which of
// the three it was.
func (m *Service) RequireMachine(h func(ctx context.Context, req hub.Request, mc Machine) hub.Response) hub.Handler {
	return func(ctx context.Context, req hub.Request) hub.Response {
		mc, ok := m.machineFromKey(ctx, req)
		if !ok {
			return errResponse(401, "unauthenticated")
		}
		return h(ctx, req, mc)
	}
}

// ---- the shared helpers ----------------------------------------------

// checkContentType enforces the common protection of the API space: a
// POST, PUT or DELETE needs the Content-Type application/json
// (parameters such as charset are accepted).
func (m *Service) checkContentType(req hub.Request) bool {
	ct := req.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	media, _, err := mime.ParseMediaType(ct)
	return err == nil && media == "application/json"
}

var errTrailingData = errors.New("machines: trailing data after the JSON body")

// decodeStrict decodes the request body into v: invalid JSON or an
// unknown field is a bad request, and a body with data after the JSON
// object is one too.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return errTrailingData
	}
	return nil
}

func errResponse(status int, err string) hub.Response {
	return hub.Response{Status: status, Body: []byte(`{"error":"` + err + `"}`)}
}

// nowMS is the clock of the service, in milliseconds.
func (m *Service) nowMS() int64 {
	return m.now().UnixMilli()
}

// randomID is one random textual id (16 bytes, unpadded base64url):
// never sequential.
func (m *Service) randomID() (string, error) {
	b := make([]byte, randomIDSize)
	if _, err := io.ReadFull(m.rand, b); err != nil {
		return "", err
	}
	return b64urlNoPad.EncodeToString(b), nil
}

// machineFromKey resolves the machine of the Authorization header of
// the request, or false. The header must carry exactly one Bearer
// token; the token is the base64url (unpadded) form of the 32-byte
// key. Nothing about the lookup is echoed to a response.
func (m *Service) machineFromKey(ctx context.Context, req hub.Request) (Machine, bool) {
	auth := req.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return Machine{}, false
	}
	key, err := b64urlNoPad.DecodeString(token)
	if err != nil || len(key) != machineKeySize {
		return Machine{}, false
	}
	sum := sha256.Sum256(key)
	mc, found, err := m.machines.FindMachineByKeyHash(ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		return Machine{}, false
	}
	return mc, found && mc.RevokedMS == nil
}

// b64urlNoPad is the base64url without padding of the wire forms (the
// poll token and the machine key).
var b64urlNoPad = base64.RawURLEncoding

// badRequest is the common 400 of the machine connect.
func badRequest() hub.Response {
	return errResponse(400, "bad_request")
}

// unavailable is the common 503 of the machine connect.
func unavailable() hub.Response {
	return errResponse(503, "unavailable")
}

// storeErr wraps a store failure for the failure messages of the
// service: the message names the operation, never the values.
func storeErr(op string, err error) error {
	return fmt.Errorf("machines: %s: %w", op, err)
}
