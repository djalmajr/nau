package accounts

import (
	"context"
	"fmt"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
)

// SQL is the AccountStore over the hub storage: one statement per
// simple operation and one atomic batch per multi-statement operation
// (the bootstrap, the user-plus-session create, the password change).
// A unique violation of the schema is translated to the sentinel error
// the handlers turn into the contract status; every other error is a
// store failure.
type SQL struct {
	store store.Store
}

// NewSQL builds the SQL account store over the hub storage.
func NewSQL(s store.Store) *SQL {
	return &SQL{store: s}
}

// mapUnique translates the unique violations of the schema to the
// sentinel errors. The constraint name comes from the database server;
// it is read to pick the error and never echoed to a response.
func mapUnique(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(err.Error(), "UNIQUE constraint failed: users.email_norm"):
		return fmt.Errorf("accounts: %w", ErrEmailTaken)
	case strings.Contains(err.Error(), "UNIQUE constraint failed: hub_meta.key"):
		return fmt.Errorf("accounts: %w", ErrAlreadyBootstrapped)
	default:
		return err
	}
}

// intResult reads the single integer of one result row.
func intResult(res store.Result) (int64, error) {
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		return 0, fmt.Errorf("accounts: no row in the result")
	}
	v, ok := res.Rows[0][0].(int64)
	if !ok {
		return 0, fmt.Errorf("accounts: the result is not an integer")
	}
	return v, nil
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// userRow reads one user out of the select columns: id, email,
// email_norm, name, password_hash, auth_version, role, created_ms. It
// reports false with no error when there is no row, and an error when
// the shape of the row is not the expected one.
func userRow(res store.Result) (User, bool, error) {
	if len(res.Rows) == 0 {
		return User{}, false, nil
	}
	row := res.Rows[0]
	if len(row) != 8 {
		return User{}, false, fmt.Errorf("accounts: the user row has %d columns, want 8", len(row))
	}
	id, ok := row[0].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the user id is not text")
	}
	email, ok := row[1].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the user e-mail is not text")
	}
	norm, ok := row[2].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the normalized e-mail is not text")
	}
	name, ok := row[3].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the user name is not text")
	}
	hash, ok := row[4].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the password hash is not text")
	}
	authVersion, ok := row[5].(int64)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the auth version is not an integer")
	}
	role, ok := row[6].(string)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the user role is not text")
	}
	created, ok := row[7].(int64)
	if !ok {
		return User{}, false, fmt.Errorf("accounts: the user creation is not an integer")
	}
	return User{ID: id, Email: email, EmailNorm: norm, Name: name, Role: role, PasswordHash: hash, AuthVersion: authVersion, CreatedMS: created}, true, nil
}

// sessionRow reads one session out of the select columns: token_sha,
// user_id, created_ms, expires_ms, revoked, auth_version.
func sessionRow(res store.Result) (SessionRow, bool, error) {
	if len(res.Rows) == 0 {
		return SessionRow{}, false, nil
	}
	row := res.Rows[0]
	if len(row) != 6 {
		return SessionRow{}, false, fmt.Errorf("accounts: the session row has %d columns, want 6", len(row))
	}
	sha, ok := row[0].(string)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session token is not text")
	}
	userID, ok := row[1].(string)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session user is not text")
	}
	created, ok := row[2].(int64)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session creation is not an integer")
	}
	expires, ok := row[3].(int64)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session expiration is not an integer")
	}
	revoked, ok := row[4].(int64)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session revocation is not an integer")
	}
	authVersion, ok := row[5].(int64)
	if !ok {
		return SessionRow{}, false, fmt.Errorf("accounts: the session auth version is not an integer")
	}
	return SessionRow{TokenSHA: sha, UserID: userID, CreatedMS: created, ExpiresMS: expires, Revoked: revoked == 1, AuthVersion: authVersion}, true, nil
}

// inviteRow reads one invite out of the select columns: id, email,
// email_norm, role, token_hash, created_ms, expires_ms, revoked, used.
func inviteRow(res store.Result) (Invite, bool, error) {
	if len(res.Rows) == 0 {
		return Invite{}, false, nil
	}
	row := res.Rows[0]
	if len(row) != 9 {
		return Invite{}, false, fmt.Errorf("accounts: the invite row has %d columns, want 9", len(row))
	}
	id, ok := row[0].(string)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite id is not text")
	}
	email, ok := row[1].(string)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite e-mail is not text")
	}
	norm, ok := row[2].(string)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the normalized e-mail is not text")
	}
	role, ok := row[3].(string)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite role is not text")
	}
	tokenHash, ok := row[4].(string)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite token is not text")
	}
	created, ok := row[5].(int64)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite creation is not an integer")
	}
	expires, ok := row[6].(int64)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite expiration is not an integer")
	}
	revoked, ok := row[7].(int64)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite revocation is not an integer")
	}
	used, ok := row[8].(int64)
	if !ok {
		return Invite{}, false, fmt.Errorf("accounts: the invite use is not an integer")
	}
	return Invite{ID: id, Email: email, EmailNorm: norm, Role: role, TokenHash: tokenHash, CreatedMS: created, ExpiresMS: expires, Revoked: revoked == 1, Used: used == 1}, true, nil
}

const selectUser = `SELECT id, email, email_norm, name, password_hash, auth_version, role, created_ms FROM users`
const selectSession = `SELECT token_sha, user_id, created_ms, expires_ms, revoked, auth_version FROM sessions`
const selectInvite = `SELECT id, email, email_norm, role, token_hash, created_ms, expires_ms, revoked, used FROM invites`

const insertUsers = `INSERT INTO users (id, email_norm, email, name, password_hash, role, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`
const insertSession = `INSERT INTO sessions (token_sha, user_id, created_ms, expires_ms, revoked, auth_version) VALUES (?, ?, ?, ?, 0, ?)`
const guardCheck = `INSERT INTO accounts_guard VALUES (changes())`
const guardClear = `DELETE FROM accounts_guard`

func userArgs(u User) []store.Value {
	return []store.Value{u.ID, u.EmailNorm, u.Email, u.Name, u.PasswordHash, u.Role, u.CreatedMS}
}

func sessionArgs(sr SessionRow) []store.Value {
	return []store.Value{sr.TokenSHA, sr.UserID, sr.CreatedMS, sr.ExpiresMS, sr.AuthVersion}
}

func (s *SQL) CountUsers(ctx context.Context) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM users`})
	if err != nil {
		return 0, err
	}
	v, err := intResult(res)
	return int(v), err
}

// BootstrapOwner creates the initial owner with its first session in
// one atomic batch. The hub_meta row is the race guard: its key is
// unique, so a second concurrent bootstrap fails on it and rolls back.
func (s *SQL) BootstrapOwner(ctx context.Context, u User, sr SessionRow) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO hub_meta (key, value) VALUES ('bootstrapped', ?)`, Args: []store.Value{u.ID}},
		{SQL: insertUsers, Args: userArgs(u)},
		{SQL: insertSession, Args: sessionArgs(sr)},
	})
	return mapUnique(err)
}

func (s *SQL) FindUserByEmailNorm(ctx context.Context, norm string) (User, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectUser + ` WHERE email_norm = ?`, Args: []store.Value{norm}})
	u, found, rowErr := userRow(res)
	return u, found, firstErr(err, rowErr)
}

func (s *SQL) FindUserByID(ctx context.Context, id string) (User, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectUser + ` WHERE id = ?`, Args: []store.Value{id}})
	u, found, rowErr := userRow(res)
	return u, found, firstErr(err, rowErr)
}

// ChangePasswordWithNewSession sets the new hash and bumps the auth
// version, revokes every session of the user and creates the new one,
// in one atomic batch. The update is conditional on the auth version
// the caller read; the one-row guard right after it fails, and rolls
// the batch back, when the version moved in between.
func (s *SQL) ChangePasswordWithNewSession(ctx context.Context, userID string, authVersion int64, passwordHash string, sr SessionRow) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `UPDATE users SET password_hash = ?, auth_version = auth_version + 1 WHERE id = ? AND auth_version = ?`, Args: []store.Value{passwordHash, userID, authVersion}},
		{SQL: guardCheck},
		{SQL: guardClear},
		{SQL: `UPDATE sessions SET revoked = 1 WHERE user_id = ? AND revoked = 0`, Args: []store.Value{userID}},
		{SQL: insertSession, Args: sessionArgs(sr)},
	})
	return mapUnique(err)
}

// CreateSessionForUser inserts the login session only while the user
// still carries the verified auth version and password hash: the
// INSERT … SELECT … WHERE EXISTS is one statement, so a rotation
// between the read and the write leaves no row and reports false.
func (s *SQL) CreateSessionForUser(ctx context.Context, sr SessionRow, userID string, authVersion int64, passwordHash string) (bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL: `INSERT INTO sessions (token_sha, user_id, created_ms, expires_ms, revoked, auth_version)
SELECT ?, ?, ?, ?, 0, ?
WHERE EXISTS (SELECT 1 FROM users WHERE id = ? AND auth_version = ? AND password_hash = ?)`,
		Args: []store.Value{sr.TokenSHA, sr.UserID, sr.CreatedMS, sr.ExpiresMS, sr.AuthVersion, userID, authVersion, passwordHash},
	})
	if err != nil {
		return false, mapUnique(err)
	}
	return res.Affected == 1, nil
}

func (s *SQL) CreateSession(ctx context.Context, sr SessionRow) error {
	_, err := s.store.Execute(ctx, store.Stmt{SQL: insertSession, Args: sessionArgs(sr)})
	return mapUnique(err)
}

func (s *SQL) FindSession(ctx context.Context, tokenSHA string) (SessionRow, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectSession + ` WHERE token_sha = ?`, Args: []store.Value{tokenSHA}})
	r, found, rowErr := sessionRow(res)
	return r, found, firstErr(err, rowErr)
}

func (s *SQL) RevokeSession(ctx context.Context, tokenSHA string) error {
	_, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `UPDATE sessions SET revoked = 1 WHERE token_sha = ? AND revoked = 0`,
		Args: []store.Value{tokenSHA},
	})
	return mapUnique(err)
}

// ReserveAttempt reserves one login attempt in one statement: the row
// enters only while the per-e-mail and the global counts inside the
// window stay below the limits, so concurrent reservations cannot
// exceed the budget. The prune in the same batch keeps the table
// bounded, as the old recording did. It reports whether the attempt
// was reserved.
func (s *SQL) ReserveAttempt(ctx context.Context, emailNorm string, atMS int64) (bool, error) {
	since := atMS - failureWindowMS
	res, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO login_failures (email_norm, at_ms)
SELECT ?, ?
WHERE (SELECT COUNT(*) FROM login_failures WHERE email_norm = ? AND at_ms > ?) < ?
  AND (SELECT COUNT(*) FROM login_failures WHERE at_ms > ?) < ?`,
			Args: []store.Value{emailNorm, atMS, emailNorm, since, int64(perEmailMaxFailures), since, int64(globalMaxFailures)}},
		{SQL: `DELETE FROM login_failures WHERE at_ms <= ?`, Args: []store.Value{since}},
	})
	if err != nil {
		return false, mapUnique(err)
	}
	if len(res) == 0 {
		return false, fmt.Errorf("accounts: the reservation returned no result")
	}
	return res[0].Affected == 1, nil
}

// RecordFailure stores one failed attempt and prunes the failures
// outside the window, in one atomic batch, so the table stays bounded.
func (s *SQL) RecordFailure(ctx context.Context, emailNorm string, atMS int64) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO login_failures (email_norm, at_ms) VALUES (?, ?)`, Args: []store.Value{emailNorm, atMS}},
		{SQL: `DELETE FROM login_failures WHERE at_ms <= ?`, Args: []store.Value{atMS - failureWindowMS}},
	})
	return mapUnique(err)
}

func (s *SQL) ClearFailures(ctx context.Context, emailNorm string) error {
	_, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `DELETE FROM login_failures WHERE email_norm = ?`,
		Args: []store.Value{emailNorm},
	})
	return mapUnique(err)
}

func (s *SQL) CountEmailFailures(ctx context.Context, emailNorm string, sinceMS int64) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT COUNT(*) FROM login_failures WHERE email_norm = ? AND at_ms > ?`,
		Args: []store.Value{emailNorm, sinceMS},
	})
	if err != nil {
		return 0, err
	}
	v, err := intResult(res)
	return int(v), err
}

func (s *SQL) CountGlobalFailures(ctx context.Context, sinceMS int64) (int, error) {
	res, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `SELECT COUNT(*) FROM login_failures WHERE at_ms > ?`,
		Args: []store.Value{sinceMS},
	})
	if err != nil {
		return 0, err
	}
	v, err := intResult(res)
	return int(v), err
}

func (s *SQL) CreateInvite(ctx context.Context, inv Invite) error {
	_, err := s.store.Execute(ctx, store.Stmt{
		SQL: `INSERT INTO invites (id, email, email_norm, role, token_hash, created_ms, expires_ms, revoked, used) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0)`,
		Args: []store.Value{
			inv.ID, inv.Email, inv.EmailNorm, inv.Role, inv.TokenHash, inv.CreatedMS, inv.ExpiresMS,
		},
	})
	return mapUnique(err)
}

func (s *SQL) FindInviteByTokenHash(ctx context.Context, hash string) (Invite, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectInvite + ` WHERE token_hash = ?`, Args: []store.Value{hash}})
	inv, found, rowErr := inviteRow(res)
	return inv, found, firstErr(err, rowErr)
}

func (s *SQL) FindInviteByID(ctx context.Context, id string) (Invite, bool, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectInvite + ` WHERE id = ?`, Args: []store.Value{id}})
	inv, found, rowErr := inviteRow(res)
	return inv, found, firstErr(err, rowErr)
}

// ListPendingInvites lists the unused and unrevoked invites, in
// creation order; the expired ones are listed too, so the owner can
// see and revoke them.
func (s *SQL) ListPendingInvites(ctx context.Context) ([]Invite, error) {
	res, err := s.store.Execute(ctx, store.Stmt{SQL: selectInvite + ` WHERE used = 0 AND revoked = 0 ORDER BY created_ms`})
	if err != nil {
		return nil, err
	}
	out := make([]Invite, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) != 9 {
			return nil, fmt.Errorf("accounts: the invite row has %d columns, want 9", len(row))
		}
		tmp, err := inviteValues(row)
		if err != nil {
			return nil, err
		}
		out = append(out, tmp)
	}
	return out, nil
}

// AcceptInviteWithSession consumes the invite, creates the user and
// the session in one atomic batch. The consumption is conditional on
// the invite still being unused, unrevoked and unexpired, and the
// one-row guard right after it fails, and rolls the batch back, when
// the condition was lost in between; the unique e-mail of the user
// makes the batch fail, and roll back, when the e-mail was taken.
func (s *SQL) AcceptInviteWithSession(ctx context.Context, inviteID string, nowMS int64, u User, sr SessionRow) error {
	_, err := s.store.Batch(ctx, []store.Stmt{
		{SQL: `UPDATE invites SET used = 1 WHERE id = ? AND used = 0 AND revoked = 0 AND expires_ms > ?`, Args: []store.Value{inviteID, nowMS}},
		{SQL: guardCheck},
		{SQL: guardClear},
		{SQL: insertUsers, Args: userArgs(u)},
		{SQL: insertSession, Args: sessionArgs(sr)},
	})
	return mapUnique(err)
}

func (s *SQL) RevokeInvite(ctx context.Context, id string) error {
	_, err := s.store.Execute(ctx, store.Stmt{
		SQL:  `UPDATE invites SET revoked = 1 WHERE id = ? AND revoked = 0`,
		Args: []store.Value{id},
	})
	return mapUnique(err)
}

// inviteValues converts one row of the invite select columns to the
// invite, checking every column type.
func inviteValues(row store.Row) (Invite, error) {
	id, ok := row[0].(string)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite id is not text")
	}
	email, ok := row[1].(string)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite e-mail is not text")
	}
	norm, ok := row[2].(string)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the normalized e-mail is not text")
	}
	role, ok := row[3].(string)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite role is not text")
	}
	tokenHash, ok := row[4].(string)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite token is not text")
	}
	created, ok := row[5].(int64)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite creation is not an integer")
	}
	expires, ok := row[6].(int64)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite expiration is not an integer")
	}
	revoked, ok := row[7].(int64)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite revocation is not an integer")
	}
	used, ok := row[8].(int64)
	if !ok {
		return Invite{}, fmt.Errorf("accounts: the invite use is not an integer")
	}
	return Invite{ID: id, Email: email, EmailNorm: norm, Role: role, TokenHash: tokenHash, CreatedMS: created, ExpiresMS: expires, Revoked: revoked == 1, Used: used == 1}, nil
}
