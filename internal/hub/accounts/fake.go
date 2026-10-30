package accounts

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Memory is the in-memory AccountStore of the tests: it mirrors the
// SQL semantics (the unique e-mail, the hub_meta bootstrap guard,
// the attempt reservation under the two limits, the conditional
// session create, the auth-version condition of the password change
// and the atomic invite accept) with a mutex, so the scenarios of
// the decision run hermetically, without a database. The mutex is the
// concurrency model of the single-statement guarantees: each guarded
// operation reads and writes under the same lock, like the database
// serializes the statements. It is a test double, not a production
// store.
type Memory struct {
	mu           sync.Mutex
	users        map[string]User // by email_norm
	usersByID    map[string]User
	sessions     map[string]SessionRow // by token_sha
	invites      map[string]Invite     // by id
	invitesByTok map[string]Invite     // by token_hash
	failures     map[string][]int64    // by email_norm
	bootstrapped bool
}

// NewMemory builds the empty in-memory account store.
func NewMemory() *Memory {
	return &Memory{
		users:        make(map[string]User),
		usersByID:    make(map[string]User),
		sessions:     make(map[string]SessionRow),
		invites:      make(map[string]Invite),
		invitesByTok: make(map[string]Invite),
		failures:     make(map[string][]int64),
	}
}

func (m *Memory) CountUsers(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *Memory) BootstrapOwner(_ context.Context, u User, sr SessionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bootstrapped {
		return fmt.Errorf("accounts: %w", ErrAlreadyBootstrapped)
	}
	if _, taken := m.users[u.EmailNorm]; taken {
		return fmt.Errorf("accounts: %w", ErrEmailTaken)
	}
	m.bootstrapped = true
	m.users[u.EmailNorm] = u
	m.usersByID[u.ID] = u
	m.sessions[sr.TokenSHA] = sr
	return nil
}

// ReserveAttempt reserves one attempt while both budgets have room,
// under the same lock that reads the counts and writes the row: the
// reservation is one unit of work, like the single SQL statement.
func (m *Memory) ReserveAttempt(_ context.Context, emailNorm string, atMS int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	since := atMS - failureWindowMS
	if countAfter(m.failures[emailNorm], since) >= perEmailMaxFailures {
		return false, nil
	}
	total := 0
	for _, v := range m.failures {
		total += countAfter(v, since)
	}
	if total >= globalMaxFailures {
		return false, nil
	}
	m.failures[emailNorm] = append(m.failures[emailNorm], atMS)
	limit := since
	for k, v := range m.failures {
		kept := v[:0]
		for _, at := range v {
			if at > limit {
				kept = append(kept, at)
			}
		}
		m.failures[k] = kept
	}
	return true, nil
}

func (m *Memory) FindUserByEmailNorm(_ context.Context, norm string) (User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[norm]
	return u, ok, nil
}

func (m *Memory) FindUserByID(_ context.Context, id string) (User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[id]
	return u, ok, nil
}

func (m *Memory) ChangePasswordWithNewSession(_ context.Context, userID string, authVersion int64, passwordHash string, sr SessionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[userID]
	if !ok {
		return fmt.Errorf("accounts: the user is not found")
	}
	if u.AuthVersion != authVersion {
		// The conditional update changed no row: the guard failure.
		return fmt.Errorf("accounts: the auth version moved")
	}
	u.PasswordHash = passwordHash
	u.AuthVersion++
	m.usersByID[userID] = u
	m.users[u.EmailNorm] = u
	for sha, srw := range m.sessions {
		if srw.UserID == userID && !srw.Revoked {
			srw.Revoked = true
			m.sessions[sha] = srw
		}
	}
	m.sessions[sr.TokenSHA] = sr
	return nil
}

// CreateSessionForUser inserts the session only while the user still
// carries the verified auth version and password hash, under the same
// lock that reads them: the condition and the write are one unit of
// work, like the single conditional statement of the SQL.
func (m *Memory) CreateSessionForUser(_ context.Context, sr SessionRow, userID string, authVersion int64, passwordHash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usersByID[userID]
	if !ok || u.AuthVersion != authVersion || u.PasswordHash != passwordHash {
		return false, nil
	}
	sr.UserID = userID
	m.sessions[sr.TokenSHA] = sr
	return true, nil
}

func (m *Memory) CreateSession(_ context.Context, sr SessionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sr.TokenSHA] = sr
	return nil
}

func (m *Memory) FindSession(_ context.Context, tokenSHA string) (SessionRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.sessions[tokenSHA]
	return r, ok, nil
}

func (m *Memory) RevokeSession(_ context.Context, tokenSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.sessions[tokenSHA]; ok {
		r.Revoked = true
		m.sessions[tokenSHA] = r
	}
	return nil
}

// RecordFailure appends one failure at atMS and drops the failures at
// or before the window limit, mirroring the SQL prune.
func (m *Memory) RecordFailure(_ context.Context, emailNorm string, atMS int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := atMS - failureWindowMS
	kept := m.failures[emailNorm][:0]
	for _, at := range m.failures[emailNorm] {
		if at > limit {
			kept = append(kept, at)
		}
	}
	m.failures[emailNorm] = append(kept, atMS)
	for k, v := range m.failures {
		kept = v[:0]
		for _, at := range v {
			if at > limit {
				kept = append(kept, at)
			}
		}
		m.failures[k] = kept
	}
	return nil
}

func (m *Memory) ClearFailures(_ context.Context, emailNorm string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.failures, emailNorm)
	return nil
}

func (m *Memory) CountEmailFailures(_ context.Context, emailNorm string, sinceMS int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return countAfter(m.failures[emailNorm], sinceMS), nil
}

func (m *Memory) CountGlobalFailures(_ context.Context, sinceMS int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	total := 0
	for _, v := range m.failures {
		total += countAfter(v, sinceMS)
	}
	return total, nil
}

func countAfter(ats []int64, sinceMS int64) int {
	n := 0
	for _, at := range ats {
		if at > sinceMS {
			n++
		}
	}
	return n
}

func (m *Memory) CreateInvite(_ context.Context, inv Invite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.invitesByTok[inv.TokenHash]; dup {
		return fmt.Errorf("accounts: the invite token is repeated")
	}
	m.invites[inv.ID] = inv
	m.invitesByTok[inv.TokenHash] = inv
	return nil
}

func (m *Memory) FindInviteByTokenHash(_ context.Context, hash string) (Invite, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invitesByTok[hash]
	return inv, ok, nil
}

func (m *Memory) FindInviteByID(_ context.Context, id string) (Invite, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[id]
	return inv, ok, nil
}

func (m *Memory) ListPendingInvites(context.Context) ([]Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Invite
	for _, inv := range m.invites {
		if !inv.Used && !inv.Revoked {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedMS != out[j].CreatedMS {
			return out[i].CreatedMS < out[j].CreatedMS
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// AcceptInviteWithSession consumes the invite, creates the user and
// the session under the same lock that reads the invite: when the
// invite was used, revoked or expired in between, nothing is written,
// the consumption not included; when the e-mail is taken, the invite
// is not consumed either.
func (m *Memory) AcceptInviteWithSession(_ context.Context, inviteID string, nowMS int64, u User, sr SessionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[inviteID]
	if !ok || inv.Used || inv.Revoked || nowMS >= inv.ExpiresMS {
		// The conditional consumption changed no row: the guard failure.
		return fmt.Errorf("accounts: the invite condition was lost")
	}
	if _, taken := m.users[u.EmailNorm]; taken {
		return fmt.Errorf("accounts: %w", ErrEmailTaken)
	}
	inv.Used = true
	m.invites[inviteID] = inv
	m.invitesByTok[inv.TokenHash] = inv
	u.AuthVersion = 1
	m.users[u.EmailNorm] = u
	m.usersByID[u.ID] = u
	sr.UserID = u.ID
	m.sessions[sr.TokenSHA] = sr
	return nil
}

func (m *Memory) RevokeInvite(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inv, ok := m.invites[id]; ok {
		inv.Revoked = true
		m.invites[id] = inv
		m.invitesByTok[inv.TokenHash] = inv
	}
	return nil
}
