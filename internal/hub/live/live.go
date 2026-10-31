// Package live is the board version of the hub: for each developer,
// a short 32-character value derived from what that developer may
// read. The version is always computed from fresh reads of the store
// (the same reads the interface read routes use), never from any
// in-memory state, so every hub instance computes the same value and
// the polling route of a hub without streaming answers from the
// database alone. The package is pure: no net/http, no os; it
// compiles for the wasip1 hub core.
package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/ingest"
	"github.com/djalmajr/nau/internal/hub/store"
	"github.com/djalmajr/nau/internal/hub/visibility"
	"github.com/djalmajr/nau/internal/inventory"
)

// apiLivePath is the polling route of the board version.
const apiLivePath = "/api/live"

// SessionUser resolves the signed-in user of a request, or false.
// The production wiring passes the accounts resolver.
type SessionUser func(ctx context.Context, req hub.Request) (accounts.User, bool)

// AgentsFunc reads the current agents and the hidden counts of the
// machines; nil uses ingest.Agents over the hub store.
type AgentsFunc func(ctx context.Context, st store.Store, machineIDs []string, nowMS int64) (ingest.AgentsResult, error)

// Config is the configuration of the live layer: the hub store, the
// visibility, the agents read, the session resolver and the clock.
// The tests inject fakes for determinism.
type Config struct {
	// Store is the hub store the agents read runs over.
	Store store.Store
	// Sharing is the state and the computation of the sharing;
	// nil uses the visibility over the hub store.
	Sharing visibility.Sharing
	// Agents reads the current agents; nil uses ingest.Agents.
	Agents AgentsFunc
	// SessionUser resolves the signed-in user of a request.
	SessionUser SessionUser
	// Now is the clock.
	Now func() time.Time
}

// Service is the live layer of the hub: the board version and the
// polling route, on top of the visibility and the agents read. It
// holds no state between requests.
type Service struct {
	store       store.Store
	sharing     visibility.Sharing
	agents      AgentsFunc
	sessionUser SessionUser
	now         func() time.Time
}

// New builds the live layer. A missing store or session resolver is
// an error.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("live: the store is required")
	}
	if cfg.SessionUser == nil {
		return nil, errors.New("live: the session resolver is required")
	}
	sharing := cfg.Sharing
	if sharing == nil {
		sharing = visibility.New(cfg.Store)
	}
	agents := cfg.Agents
	if agents == nil {
		agents = ingest.Agents
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:       cfg.Store,
		sharing:     sharing,
		agents:      agents,
		sessionUser: cfg.SessionUser,
		now:         now,
	}, nil
}

// boardDoc is the canonical encoding of the board one developer may
// read: a fixed field order and the slices ordered by id. The
// delivery-only times (the observed_ms of the agents, the
// last_seen_ms of the machines and the generated_ms of the read) are
// absent by construction: they move on every delivery without the
// board changing.
type boardDoc struct {
	Machines []boardMachine `json:"machines"`
	Agents   []boardAgent   `json:"agents"`
	Hidden   []boardHidden  `json:"hidden"`
}

// boardMachine is one visible machine of the encoding: the fields of
// the decision, with the ids of the shared_with of the own machines.
type boardMachine struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Online     bool     `json:"online"`
	Developer  string   `json:"developer"`
	SharedWith []string `json:"shared_with,omitempty"`
}

// boardAgent is one current agent of the encoding, the agent of the
// read of GET /api/agents without the observed_ms.
type boardAgent struct {
	ID          string            `json:"id"`
	Machine     string            `json:"machine"`
	Terminal    string            `json:"terminal"`
	Project     string            `json:"project"`
	Session     string            `json:"session"`
	Name        string            `json:"name"`
	Role        string            `json:"role"`
	Kind        string            `json:"kind"`
	Model       *string           `json:"model"`
	Effort      *string           `json:"effort"`
	State       inventory.State   `json:"state"`
	Reason      *inventory.Reason `json:"reason"`
	Task        *string           `json:"task"`
	ReportReady bool              `json:"report_ready"`
	StartedMS   int64             `json:"started_ms"`
	SinceMS     *int64            `json:"since_ms"`
}

// boardHidden is the hidden counts of one machine of the encoding.
type boardHidden struct {
	Machine  string `json:"machine"`
	Working  int64  `json:"working"`
	NeedsYou int64  `json:"needs_you"`
	Done     int64  `json:"done"`
	Idle     int64  `json:"idle"`
	Unknown  int64  `json:"unknown"`
}

// Version computes the board version of the developer: the SHA-256 of
// the canonical encoding of what the developer may read — the visible
// machines (the id, the name, the online flag, the developer id and,
// for the own ones, the ids of the shared_with), the current agents of
// those machines and their hidden counts — through the same reads the
// GET /api/agents route uses. It keeps the first 16 bytes of the
// digest, in lowercase hexadecimal (32 characters). Two reads without
// a visible change return the same version; an agent that changes of
// state or of task, a machine that goes online or offline, a share
// given or taken and a revoked machine change the version of the
// affected viewers, and only of them.
func (s *Service) Version(ctx context.Context, developerID string) (string, error) {
	nowMS := s.now().UnixMilli()
	list, err := s.sharing.VisibleMachines(ctx, developerID, nowMS)
	if err != nil {
		return "", err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	ids := make([]string, 0, len(list))
	for _, mc := range list {
		ids = append(ids, mc.ID)
	}
	res := ingest.AgentsResult{Agents: []ingest.AgentRow{}, Hidden: []ingest.Hidden{}}
	if len(ids) > 0 {
		if res, err = s.agents(ctx, s.store, ids, nowMS); err != nil {
			return "", err
		}
	}
	doc := boardDoc{Machines: make([]boardMachine, 0, len(list)), Agents: []boardAgent{}, Hidden: []boardHidden{}}
	for _, mc := range list {
		bm := boardMachine{
			ID:        mc.ID,
			Name:      mc.Name,
			Online:    mc.Online,
			Developer: mc.Developer.ID,
		}
		if mc.Own {
			bm.SharedWith = make([]string, 0, len(mc.SharedWith))
			for _, d := range mc.SharedWith {
				bm.SharedWith = append(bm.SharedWith, d.ID)
			}
		}
		doc.Machines = append(doc.Machines, bm)
	}
	for _, a := range res.Agents {
		doc.Agents = append(doc.Agents, boardAgent{
			ID:          a.MachineID + "." + a.Terminal,
			Machine:     a.MachineID,
			Terminal:    a.Terminal,
			Project:     a.Project,
			Session:     a.Session,
			Name:        a.Name,
			Role:        a.Role,
			Kind:        a.Kind,
			Model:       a.Model,
			Effort:      a.Effort,
			State:       a.State,
			Reason:      a.Reason,
			Task:        a.Task,
			ReportReady: a.ReportReady,
			StartedMS:   a.StartedMS,
			SinceMS:     a.SinceMS,
		})
	}
	sort.Slice(doc.Agents, func(i, j int) bool { return doc.Agents[i].ID < doc.Agents[j].ID })
	for _, h := range res.Hidden {
		doc.Hidden = append(doc.Hidden, boardHidden{
			Machine:  h.MachineID,
			Working:  int64(h.Counts.Working),
			NeedsYou: int64(h.Counts.NeedsYou),
			Done:     int64(h.Counts.Done),
			Idle:     int64(h.Counts.Idle),
			Unknown:  int64(h.Counts.Unknown),
		})
	}
	sort.Slice(doc.Hidden, func(i, j int) bool { return doc.Hidden[i].Machine < doc.Hidden[j].Machine })
	data, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

// Register adds the live route to the hub server.
func (s *Service) Register(hs *hub.Server) {
	hs.Handle("GET", apiLivePath, s.live)
}

// live is the GET /api/live route (session required): the board
// version of the signed-in developer, the polling path of a hub
// without streaming. A store failure answers 503 without the error.
func (s *Service) live(ctx context.Context, req hub.Request) hub.Response {
	u, ok := s.sessionUser(ctx, req)
	if !ok {
		return hub.Response{Status: 401, Body: []byte(`{"error":"unauthenticated"}`)}
	}
	v, err := s.Version(ctx, u.ID)
	if err != nil {
		return hub.Response{Status: 503, Body: []byte(`{"error":"unavailable"}`)}
	}
	out, _ := json.Marshal(struct {
		Version string `json:"version"`
	}{Version: v})
	return hub.Response{Status: 200, Body: out}
}
