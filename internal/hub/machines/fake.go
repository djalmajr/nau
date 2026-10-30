package machines

import (
	"context"
	"sort"
	"sync"
)

// Memory is the in-memory MachineStore of the tests: it keeps the
// semantics of the SQL store (the consume-once of the delivery, the
// confirm-once of the pairing, the retention of the tables) without a
// database, and it holds the last-seen map the ingest layer would
// write to the inventory snapshots.
type Memory struct {
	mu         sync.Mutex
	pairings   map[string]Pairing
	machines   map[string]Machine
	attempts   map[string][]int64
	deliveries map[string][]int64
	lastSeen   map[string]int64

	// The knobs of the store failures the scenario tests exercise.
	CreateErr    error
	ConfirmErr   error
	DeliverErr   error
	RevokeErr    error
	ListErr      error
	CountErr     error
	RecordErr    error
	LastSeenErr  error
	FindPairErr  error
	FindMachErr  error
	FindPairErr2 error // the re-read of the delivery disambiguation
}

// NewMemory builds an empty in-memory machine store.
func NewMemory() *Memory {
	return &Memory{
		pairings:   make(map[string]Pairing),
		machines:   make(map[string]Machine),
		attempts:   make(map[string][]int64),
		deliveries: make(map[string][]int64),
		lastSeen:   make(map[string]int64),
	}
}

// SetLastSeen sets the last-seen instant of one machine (the
// received_ms of the last inventory of the ingest layer).
func (f *Memory) SetLastSeen(machineID string, receivedMS int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSeen[machineID] = receivedMS
}

// PairingByCodeHash is the pairing of a normalized code, for the
// tests.
func (f *Memory) PairingByCodeHash(codeHash string) (Pairing, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pairings {
		if p.CodeHash == codeHash {
			return p, true
		}
	}
	return Pairing{}, false
}

// Machines is the current machines, for the tests.
func (f *Memory) Machines() []Machine {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Machine, 0, len(f.machines))
	for _, m := range f.machines {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *Memory) CreatePairing(ctx context.Context, p Pairing, nowMS int64) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := nowMS - pairingTTL.Milliseconds()
	for id, pp := range f.pairings {
		if pp.ExpiresMS < cutoff {
			delete(f.pairings, id)
		}
	}
	attemptCutoff := nowMS - confirmWindow.Milliseconds()
	for uid, ts := range f.attempts {
		kept := ts[:0]
		for _, at := range ts {
			if at > attemptCutoff {
				kept = append(kept, at)
			}
		}
		f.attempts[uid] = kept
	}
	f.pairings[p.ID] = p
	return nil
}

func (f *Memory) PendingPairings(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.pairings {
		if !p.Consumed {
			n++
		}
	}
	return n, nil
}

func (f *Memory) PairingsCreatedSince(ctx context.Context, sinceMS int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.pairings {
		if p.CreatedMS > sinceMS {
			n++
		}
	}
	return n, nil
}

func (f *Memory) findPairing(hashField func(Pairing) string, hash string) (Pairing, bool, error) {
	if f.FindPairErr != nil {
		return Pairing{}, false, f.FindPairErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pairings {
		if hashField(p) == hash {
			return p, true, nil
		}
	}
	return Pairing{}, false, nil
}

func (f *Memory) FindPairingByCodeHash(ctx context.Context, codeHash string) (Pairing, bool, error) {
	return f.findPairing(func(p Pairing) string { return p.CodeHash }, codeHash)
}

func (f *Memory) FindPairingByPollHash(ctx context.Context, pollHash string) (Pairing, bool, error) {
	return f.findPairing(func(p Pairing) string { return p.PollHash }, pollHash)
}

func (f *Memory) FindPairingByID(ctx context.Context, id string) (Pairing, bool, error) {
	if f.FindPairErr2 != nil {
		return Pairing{}, false, f.FindPairErr2
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, found := f.pairings[id]
	return p, found, nil
}

func (f *Memory) ConfirmPairing(ctx context.Context, pairingID, userID string) (bool, error) {
	if f.ConfirmErr != nil {
		return false, f.ConfirmErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, found := f.pairings[pairingID]
	if !found || p.ApprovedBy != nil {
		return false, nil
	}
	p.ApprovedBy = &userID
	f.pairings[pairingID] = p
	return true, nil
}

func (f *Memory) RecordPairingError(ctx context.Context, userID string, atMS int64) error {
	if f.RecordErr != nil {
		return f.RecordErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[userID] = append(f.attempts[userID], atMS)
	cutoff := atMS - confirmWindow.Milliseconds()
	kept := f.attempts[userID][:0]
	for _, at := range f.attempts[userID] {
		if at > cutoff {
			kept = append(kept, at)
		}
	}
	f.attempts[userID] = kept
	return nil
}

func (f *Memory) CountPairingErrors(ctx context.Context, userID string, sinceMS int64) (int, error) {
	if f.CountErr != nil {
		return 0, f.CountErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, at := range f.attempts[userID] {
		if at > sinceMS {
			n++
		}
	}
	return n, nil
}

// DeliverMachine consumes the pairing exactly once and stores the
// machine, atomically under the lock: a second call for the same
// pairing answers (false, nil).
func (f *Memory) DeliverMachine(ctx context.Context, pairingID string, m Machine) (bool, error) {
	if f.DeliverErr != nil {
		return false, f.DeliverErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, found := f.pairings[pairingID]
	if !found || p.Consumed || p.ApprovedBy == nil {
		return false, nil
	}
	p.Consumed = true
	p.MachineID = &m.ID
	f.pairings[pairingID] = p
	f.machines[m.ID] = m
	return true, nil
}

func (f *Memory) FindMachineByKeyHash(ctx context.Context, keyHash string) (Machine, bool, error) {
	if f.FindMachErr != nil {
		return Machine{}, false, f.FindMachErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.machines {
		if m.KeyHash == keyHash {
			return m, true, nil
		}
	}
	return Machine{}, false, nil
}

func (f *Memory) FindMachineByID(ctx context.Context, id string) (Machine, bool, error) {
	if f.FindMachErr != nil {
		return Machine{}, false, f.FindMachErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, found := f.machines[id]
	return m, found, nil
}

func (f *Memory) list(devID string, all bool) ([]Machine, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Machine, 0, len(f.machines))
	for _, m := range f.machines {
		if all || m.DeveloperID == devID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Memory) ListMachinesByDeveloper(ctx context.Context, developerID string) ([]Machine, error) {
	return f.list(developerID, false)
}

func (f *Memory) ListAllMachines(ctx context.Context) ([]Machine, error) {
	return f.list("", true)
}

func (f *Memory) RevokeMachine(ctx context.Context, id string, atMS int64) (bool, error) {
	if f.RevokeErr != nil {
		return false, f.RevokeErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, found := f.machines[id]
	if !found || m.RevokedMS != nil {
		return false, nil
	}
	m.RevokedMS = &atMS
	f.machines[id] = m
	return true, nil
}

func (f *Memory) LastSeen(ctx context.Context, machineIDs []string) (map[string]int64, error) {
	if f.LastSeenErr != nil {
		return nil, f.LastSeenErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int64, len(machineIDs))
	for _, id := range machineIDs {
		if ms, ok := f.lastSeen[id]; ok {
			out[id] = ms
		}
	}
	return out, nil
}

func (f *Memory) RecordDelivery(ctx context.Context, machineID string, atMS int64) error {
	if f.RecordErr != nil {
		return f.RecordErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries[machineID] = append(f.deliveries[machineID], atMS)
	cutoff := atMS - deliveryWindow.Milliseconds()
	kept := f.deliveries[machineID][:0]
	for _, at := range f.deliveries[machineID] {
		if at > cutoff {
			kept = append(kept, at)
		}
	}
	f.deliveries[machineID] = kept
	return nil
}

func (f *Memory) CountDeliveries(ctx context.Context, machineID string, sinceMS int64) (int, error) {
	if f.CountErr != nil {
		return 0, f.CountErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, at := range f.deliveries[machineID] {
		if at > sinceMS {
			n++
		}
	}
	return n, nil
}

var _ MachineStore = (*Memory)(nil)
