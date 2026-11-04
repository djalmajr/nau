package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/hub"
)

// The policy of the decision: the login attempts are bounded per
// normalized e-mail and in total, the session and the invite live for
// seven days, and the invite role is fixed to member.
const (
	perEmailMaxFailures = 5
	globalMaxFailures   = 100
	failureWindowMS     = int64(15 * 60 * 1000)

	InviteTTL    = 7 * 24 * time.Hour
	RoleOwner    = "owner"
	RoleMember   = "member"
	minKeyBytes  = 32
	randomIDSize = 16
)

// User is one hub account. The PasswordHash and the AuthVersion are
// internal values: they never leave the package in a response. The
// AuthVersion is the version of the credential: a password change
// bumps it, and a session is live only while it carries the current
// version.
type User struct {
	ID           string
	Email        string
	EmailNorm    string
	Name         string
	Role         string
	PasswordHash string
	AuthVersion  int64
	CreatedMS    int64
}

// Invite is one pending invitation issued by the owner. TokenHash is
// the SHA-256 of the random token; the token itself is returned once,
// at creation.
type Invite struct {
	ID        string
	Email     string
	EmailNorm string
	Role      string
	TokenHash string
	CreatedMS int64
	ExpiresMS int64
	Revoked   bool
	Used      bool
}

// The store errors that the handlers translate into the contract
// statuses. The SQL implementation returns them when it recognizes the
// matching unique violation; the memory implementation sets them
// directly.
var (
	ErrEmailTaken  = errors.New("accounts: the e-mail is already taken")
	ErrOwnerExists = errors.New("accounts: the owner already exists")
)

// The root-token log lines carry no token, password or e-mail.
const (
	rootTokenLogWrong       = "hub accounts: the root token of the recovery is invalid"
	rootTokenLogReset       = "hub accounts: the owner password was reset with the root token"
	rootTokenLogClearFailed = "hub accounts: the login failures of the owner were not cleared"
)

// AccountStore is the account storage. Every method is one unit of
// work on the database; the multi-statement operations (the owner seed,
// the user-plus-session create, the password change) are atomic: either
// everything applies or nothing does. There is exactly one owner: the
// seed creates it on the empty database and the invites create members
// only.
type AccountStore interface {
	CountUsers(ctx context.Context) (int, error)
	// SeedOwner creates the initial owner, without a session, only
	// while the database holds no user: the conditional insert and the
	// one-row guard run as one atomic batch, so a database with any
	// user answers the constraint and the whole batch rolls back
	// without writing. A constraint of the operation is
	// ErrOwnerExists; the caller re-reads the state before deciding.
	SeedOwner(ctx context.Context, u User) error
	// FindOwner returns the owner, the only account with the owner
	// role.
	FindOwner(ctx context.Context) (User, bool, error)
	FindUserByEmailNorm(ctx context.Context, norm string) (User, bool, error)
	FindUserByID(ctx context.Context, id string) (User, bool, error)
	// ReserveAttempt reserves one login attempt for the e-mail, before
	// any password verification: the row is inserted only while the
	// per-e-mail and the global counts inside the window stay below
	// the limits, in one statement. It reports false, with no error,
	// when no attempt fits the budget. A wrong attempt leaves the
	// reservation as the failure; a success clears the e-mail.
	ReserveAttempt(ctx context.Context, emailNorm string, atMS int64) (bool, error)
	// CreateSessionForUser inserts the session only while the user
	// still carries the verified auth version and password hash, in
	// one conditional statement: a rotation between the read and the
	// write leaves no row. It reports false, with no error, when the
	// row did not enter.
	CreateSessionForUser(ctx context.Context, s SessionRow, userID string, authVersion int64, passwordHash string) (bool, error)
	// ChangePasswordWithNewSession sets the new hash and bumps the
	// auth version, revokes every session of the user and creates the
	// new one, in one atomic batch. The update is conditional on the
	// auth version read by the caller; when it changed in the
	// meantime the whole batch rolls back and the error is the guard
	// failure the caller classifies by re-reading the user.
	ChangePasswordWithNewSession(ctx context.Context, userID string, authVersion int64, passwordHash string, s SessionRow) error
	CreateSession(ctx context.Context, s SessionRow) error
	FindSession(ctx context.Context, tokenSHA string) (SessionRow, bool, error)
	RevokeSession(ctx context.Context, tokenSHA string) error
	RecordFailure(ctx context.Context, emailNorm string, atMS int64) error
	ClearFailures(ctx context.Context, emailNorm string) error
	CountEmailFailures(ctx context.Context, emailNorm string, sinceMS int64) (int, error)
	CountGlobalFailures(ctx context.Context, sinceMS int64) (int, error)
	CreateInvite(ctx context.Context, inv Invite) error
	FindInviteByTokenHash(ctx context.Context, hash string) (Invite, bool, error)
	FindInviteByID(ctx context.Context, id string) (Invite, bool, error)
	ListPendingInvites(ctx context.Context) ([]Invite, error)
	// AcceptInviteWithSession consumes the invite, creates the user
	// and the session in one atomic batch. The consumption is
	// conditional on the invite still being unused, unrevoked and
	// unexpired at nowMS, and is confirmed by the one-row guard; when
	// the condition was lost the whole batch rolls back, the invite
	// not included.
	AcceptInviteWithSession(ctx context.Context, inviteID string, nowMS int64, u User, s SessionRow) error
	RevokeInvite(ctx context.Context, id string) error
}

// Config is the configuration of the accounts: the store, the session
// key (at least 32 bytes), the optional root token of the password
// recovery, the optional log writer, and the time and the randomness,
// injected so the tests are deterministic.
type Config struct {
	// Store is the account storage.
	Store AccountStore
	// SessionKey signs the session cookies.
	SessionKey []byte
	// RootToken is the token of the password recovery: when it is set
	// the recovery route, registered on the recovery server, answers
	// the reset; when it is empty the route answers the 404 of a
	// route that does not exist.
	RootToken string
	// Log receives the lines of the recovery route: one per wrong
	// token and one per password reset, with no token, password or
	// e-mail. Nil discards them.
	Log io.Writer
	// Now is the clock.
	Now func() time.Time
	// Rand is the randomness source; nil uses crypto/rand.
	Rand io.Reader
}

// Auth is the accounts layer of the hub: the owner seed, the login, the
// session endpoints, the root-token password recovery and the
// owner-issued invites, on top of one AccountStore. It holds no state
// between requests.
type Auth struct {
	store     AccountStore
	key       []byte
	rootToken string
	log       io.Writer
	now       func() time.Time
	rand      io.Reader
}

// New builds the accounts layer. A session key shorter than 32
// decodes bytes or a missing store is an error.
func New(cfg Config) (*Auth, error) {
	if cfg.Store == nil {
		return nil, errors.New("accounts: the store is required")
	}
	if len(cfg.SessionKey) < minKeyBytes {
		return nil, errors.New("accounts: the session key must decode to at least 32 bytes")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	rnd := cfg.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	return &Auth{
		store:     cfg.Store,
		key:       cfg.SessionKey,
		rootToken: cfg.RootToken,
		log:       cfg.Log,
		now:       now,
		rand:      rnd,
	}, nil
}

// Register adds the account routes to the public hub server: the
// session, the password change and the invites. The recovery route is
// not among them: it belongs to the recovery server, registered by
// RegisterRecovery on its loopback-only listener.
func (a *Auth) Register(s *hub.Server) {
	s.Handle("GET", "/api/session", a.getSession)
	s.Handle("POST", "/api/session", a.login)
	s.Handle("DELETE", "/api/session", a.logout)
	s.Handle("POST", "/api/session/password", a.RequireSession(a.changePassword))
	s.Handle("POST", "/api/invites", a.RequireSession(a.createInvite))
	s.Handle("GET", "/api/invites", a.RequireSession(a.listInvites))
	s.Handle("DELETE", "/api/invites/{id}", a.RequireSession(a.revokeInvite))
	s.Handle("POST", "/api/invites/accept", a.acceptInvite)
}

// RegisterRecovery adds the password recovery route to the recovery
// server. The hub serve binds that server to a loopback listener that
// is separate from the public one, so the route never exists on the
// public port: the absence of the port is the protection, and the
// handler answers the 404 of a route that does not exist when the
// root token is not set.
func (a *Auth) RegisterRecovery(s *hub.Server) {
	s.Handle("POST", "/api/session/root-token", a.rootTokenReset)
}

// HasOwner reports whether the database has the owner account, the only
// account with the owner role: the empty-database guard of the seed and
// the lost-race check re-read the state through it.
func (a *Auth) HasOwner(ctx context.Context) (bool, error) {
	_, found, err := a.store.FindOwner(ctx)
	return found, err
}

// RequireSession wraps a handler that needs the signed-in user: it
// resolves the session (signature, stored row, expiration, revocation)
// and passes the user to the handler, answering 401 unauthenticated
// when the session does not hold.
func (a *Auth) RequireSession(h func(ctx context.Context, req hub.Request, u User) hub.Response) hub.Handler {
	return func(ctx context.Context, req hub.Request) hub.Response {
		u, ok := a.sessionUser(ctx, req)
		if !ok {
			return errResponse(401, "unauthenticated")
		}
		return h(ctx, req, u)
	}
}

// SessionUser resolves the signed-in user of a request, or false:
// the session cookie is checked (signature, stored row, expiration,
// revocation). The other hub layers (the machines) use it to read the
// session without owning the cookie.
func (a *Auth) SessionUser(ctx context.Context, req hub.Request) (User, bool) {
	return a.sessionUser(ctx, req)
}

// ---- the shared protections -----------------------------------------

// checkContentType enforces the common protection of the decision: a
// POST, PUT or DELETE in the API space needs the Content-Type
// application/json (parameters such as charset are accepted), the
// form-based CSRF defense alongside SameSite=Lax.
func (a *Auth) checkContentType(req hub.Request) bool {
	ct := req.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	media, _, err := mime.ParseMediaType(ct)
	return err == nil && media == "application/json"
}

var errTrailingData = errors.New("accounts: trailing data after the JSON body")

// decodeStrict decodes the request body into v: invalid JSON or an
// unknown field is a bad request, and a body with data after the JSON
// object is one too.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
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

func withCookie(status int, cookie string, body []byte) hub.Response {
	h := make(hub.Header)
	h.Set("Set-Cookie", cookie)
	return hub.Response{Status: status, Header: h, Body: body}
}

// userBody is the session body of the contract: the user with the role.
type userBody struct {
	User userJSON `json:"user"`
}

type userJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func sessionBody(u User) []byte {
	b, _ := json.Marshal(userBody{User: userJSON{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role}})
	return b
}

// normalizeEmail is the normalized e-mail of the decision: lowercase
// and without the edge spaces.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// randomID is one random textual id (16 bytes, unpadded base64url):
// never sequential.
func (a *Auth) randomID() (string, error) {
	b := make([]byte, randomIDSize)
	if _, err := io.ReadFull(a.rand, b); err != nil {
		return "", err
	}
	return b64urlNoPad.EncodeToString(b), nil
}

// sessionUser resolves the signed-in user of the request, or false.
func (a *Auth) sessionUser(ctx context.Context, req hub.Request) (User, bool) {
	if _, u, ok := a.sessionRowAndUser(ctx, req); ok {
		return u, true
	}
	return User{}, false
}

// sessionRowAndUser resolves the live session row and its user.
func (a *Auth) sessionRowAndUser(ctx context.Context, req hub.Request) (SessionRow, User, bool) {
	value, err := ParseCookieValue(req.Header.Get("Cookie"))
	if err != nil {
		return SessionRow{}, User{}, false
	}
	tokenSHA, ok := ParseSession(value, a.key)
	if !ok {
		return SessionRow{}, User{}, false
	}
	row, found, err := a.store.FindSession(ctx, tokenSHA)
	if err != nil || !found {
		return SessionRow{}, User{}, false
	}
	if !ValidateSession(row, a.now().UnixMilli()) {
		return SessionRow{}, User{}, false
	}
	u, found, err := a.store.FindUserByID(ctx, row.UserID)
	if err != nil || !found {
		return SessionRow{}, User{}, false
	}
	if row.AuthVersion != u.AuthVersion {
		return SessionRow{}, User{}, false
	}
	return row, u, true
}

// ---- the owner seed --------------------------------------------------

// SeedOwner builds the initial owner from the given name, e-mail and
// password and seeds it into the empty database, without a session: the
// fresh hash and the id come out before any write, and the store keeps
// the operation atomic, refusing it while any user exists. It reports
// ErrOwnerExists when the database already has a user (a legacy user of
// any other e-mail, or another seed that won the race); the caller
// re-reads the state before deciding.
func (a *Auth) SeedOwner(ctx context.Context, name, email, password string) error {
	hash, err := HashPassword(a.rand, password)
	if err != nil {
		return err
	}
	id, err := a.randomID()
	if err != nil {
		return err
	}
	now := a.now()
	user := User{
		ID:           id,
		Email:        email,
		EmailNorm:    normalizeEmail(email),
		Name:         name,
		PasswordHash: hash,
		Role:         RoleOwner,
		AuthVersion:  1,
		CreatedMS:    now.UnixMilli(),
	}
	return a.store.SeedOwner(ctx, user)
}

// ---- the root-token password recovery --------------------------------

type rootTokenBody struct {
	Token string `json:"token"`
	New   string `json:"new"`
}

// logf writes one line of the recovery route to the configured writer
// (a nil writer discards it). The two lines carry no token, no password
// and no e-mail.
func (a *Auth) logf(line string) {
	if a.log != nil {
		_, _ = fmt.Fprintln(a.log, line)
	}
}

// rootTokenReset redefines the password of the owner with the root
// token, the emergency exit of a forgotten password. The route exists
// only on the recovery listener, bound to the loopback of the hub, so
// the public port has no such route at all. The order of the checks is
// the contract: the 404 of a route that does not exist (the root token
// not set) comes before the content type, and the token check,
// compared on the SHA-256 in constant time so the length of the
// presented token never leaks, comes before the password policy. There
// is no attempt limit here: the public login feeds the limiter, and an
// attacker could exhaust it and close the emergency exit; the token is
// at least 32 bytes and the route is not reachable from the
// public URL. The success redefines the password through the same
// atomic change of the session: the auth version bumps, every session
// of the owner is revoked and the new one is created.
func (a *Auth) rootTokenReset(ctx context.Context, req hub.Request) hub.Response {
	if a.rootToken == "" {
		return errResponse(404, "not_found")
	}
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var b rootTokenBody
	if err := decodeStrict(req.Body, &b); err != nil {
		return errResponse(400, "bad_request")
	}
	got, want := sha256.Sum256([]byte(b.Token)), sha256.Sum256([]byte(a.rootToken))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		a.logf(rootTokenLogWrong)
		return errResponse(401, "invalid_token")
	}
	if !PolicyOK(b.New) {
		return errResponse(400, "weak_password")
	}
	owner, found, err := a.store.FindOwner(ctx)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	if !found {
		return errResponse(409, "no_owner")
	}
	hash, err := HashPassword(a.rand, b.New)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row, cookie, err := NewSession(a.rand, a.key, a.now())
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row.UserID = owner.ID
	row.AuthVersion = owner.AuthVersion + 1
	if err := a.store.ChangePasswordWithNewSession(ctx, owner.ID, owner.AuthVersion, hash, row); err != nil {
		// The batch failed: the guard lost the race only when a
		// concurrent change moved the version in between; anything
		// else is a store failure.
		if cur, found, rerr := a.store.FindUserByID(ctx, owner.ID); rerr == nil && found && cur.AuthVersion != owner.AuthVersion {
			return errResponse(401, "unauthenticated")
		}
		return errResponse(503, "unavailable")
	}
	if err := a.store.ClearFailures(ctx, owner.EmailNorm); err != nil {
		a.logf(rootTokenLogClearFailed)
	}
	a.logf(rootTokenLogReset)
	return withCookie(200, SessionCookie(cookie), sessionBody(owner))
}

// ---- the session endpoints -------------------------------------------

// getSession is the GET of the session: the signed-in user, or 401.
func (a *Auth) getSession(ctx context.Context, req hub.Request) hub.Response {
	u, ok := a.sessionUser(ctx, req)
	if !ok {
		return errResponse(401, "unauthenticated")
	}
	return hub.Response{Status: 200, Body: sessionBody(u)}
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// login is the POST of the session. The attempt is reserved in the
// database before any verification, so the per-e-mail and the global
// budgets hold between instances and between concurrent requests; an
// e-mail that does not fit the budget answers 429 before any password
// check. An unknown e-mail verifies against the fixed dummy hash, so
// the time does not reveal whether the e-mail exists; the dummy result
// never authenticates, and the unknown e-mail counts as a failure like
// the known one. A success clears the counter of the e-mail, and the
// session is created only while the verified credential version and
// hash still hold.
func (a *Auth) login(ctx context.Context, req hub.Request) hub.Response {
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var b loginBody
	if err := decodeStrict(req.Body, &b); err != nil {
		return errResponse(400, "bad_request")
	}
	norm := normalizeEmail(b.Email)
	now := a.now().UnixMilli()
	res, err := a.store.ReserveAttempt(ctx, norm, now)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	if !res {
		return errResponse(429, "rate_limited")
	}
	u, found, err := a.store.FindUserByEmailNorm(ctx, norm)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	hash := dummy()
	if found {
		hash = u.PasswordHash
	}
	verified := VerifyPassword(b.Password, hash)
	if !found || !verified {
		// The reservation stays as the failure of this attempt, known
		// or unknown e-mail alike.
		return errResponse(401, "invalid_credentials")
	}
	row, cookie, err := NewSession(a.rand, a.key, a.now())
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row.UserID = u.ID
	row.AuthVersion = u.AuthVersion
	applied, err := a.store.CreateSessionForUser(ctx, row, u.ID, u.AuthVersion, u.PasswordHash)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	if !applied {
		// The credential rotated between the read and the write: the
		// same 401 as a wrong password, and the reservation stays as
		// the failure.
		return errResponse(401, "invalid_credentials")
	}
	if err := a.store.ClearFailures(ctx, norm); err != nil {
		return errResponse(503, "unavailable")
	}
	return withCookie(200, SessionCookie(cookie), sessionBody(u))
}

// logout revokes the session row when it is live and always answers 204
// with the cleared cookie, so the answer never reveals whether a
// session existed.
func (a *Auth) logout(ctx context.Context, req hub.Request) hub.Response {
	// The logout is a DELETE under /api/, so it is gated by the content
	// type like every write (the CSRF defense of the decision), even
	// though it decodes no body.
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	resp := hub.Response{Status: 204}
	resp.Header = make(hub.Header)
	resp.Header.Set("Set-Cookie", ClearedSessionCookie())
	row, _, ok := a.sessionRowAndUser(ctx, req)
	if !ok {
		return resp
	}
	if err := a.store.RevokeSession(ctx, row.TokenSHA); err != nil {
		return errResponse(503, "unavailable")
	}
	return resp
}

type changePasswordBody struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword checks the current password, applies the new one,
// bumps the auth version of the credential, revokes every session of
// the user and issues a new one for the caller. The update is
// conditional on the auth version the request was authenticated with:
// when a concurrent change committed in between, nothing is written
// and the answer is 401 unauthenticated.
func (a *Auth) changePassword(ctx context.Context, req hub.Request, u User) hub.Response {
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var b changePasswordBody
	if err := decodeStrict(req.Body, &b); err != nil {
		return errResponse(400, "bad_request")
	}
	if !PolicyOK(b.New) {
		return errResponse(400, "weak_password")
	}
	if !VerifyPassword(b.Current, u.PasswordHash) {
		return errResponse(401, "invalid_credentials")
	}
	hash, err := HashPassword(a.rand, b.New)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row, cookie, err := NewSession(a.rand, a.key, a.now())
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row.UserID = u.ID
	row.AuthVersion = u.AuthVersion + 1
	if err := a.store.ChangePasswordWithNewSession(ctx, u.ID, u.AuthVersion, hash, row); err != nil {
		// The batch failed: the guard lost the race only when a
		// concurrent change moved the version in between; anything
		// else is a store failure.
		if cur, found, rerr := a.store.FindUserByID(ctx, u.ID); rerr == nil && found && cur.AuthVersion != u.AuthVersion {
			return errResponse(401, "unauthenticated")
		}
		return errResponse(503, "unavailable")
	}
	return withCookie(204, SessionCookie(cookie), nil)
}

// ---- the invites -----------------------------------------------------

type createInviteBody struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type inviteResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	ExpiresMS int64  `json:"expires_ms"`
	Token     string `json:"token"`
}

type inviteListItem struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	CreatedMS int64  `json:"created_ms"`
	ExpiresMS int64  `json:"expires_ms"`
}

// createInvite issues one invite, for the owner only: a member answers
// 403 forbidden. The token (32 random bytes, unpadded base64url)
// appears only in this answer; the store keeps its SHA-256.
func (a *Auth) createInvite(ctx context.Context, req hub.Request, u User) hub.Response {
	if u.Role != RoleOwner {
		return errResponse(403, "forbidden")
	}
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var b createInviteBody
	if err := decodeStrict(req.Body, &b); err != nil {
		return errResponse(400, "bad_request")
	}
	if b.Role != RoleMember {
		return errResponse(400, "bad_request")
	}
	email := strings.TrimSpace(b.Email)
	if email == "" {
		return errResponse(400, "bad_request")
	}
	token := make([]byte, sessionIDSize)
	if _, err := io.ReadFull(a.rand, token); err != nil {
		return errResponse(503, "unavailable")
	}
	now := a.now()
	id, err := a.randomID()
	if err != nil {
		return errResponse(503, "unavailable")
	}
	inv := Invite{
		ID:        id,
		Email:     email,
		EmailNorm: normalizeEmail(email),
		Role:      RoleMember,
		TokenHash: sha256Hex(token),
		CreatedMS: now.UnixMilli(),
		ExpiresMS: now.Add(InviteTTL).UnixMilli(),
	}
	if err := a.store.CreateInvite(ctx, inv); err != nil {
		return errResponse(503, "unavailable")
	}
	body, _ := json.Marshal(inviteResponse{ID: inv.ID, Email: inv.Email, ExpiresMS: inv.ExpiresMS, Token: b64urlNoPad.EncodeToString(token)})
	return hub.Response{Status: 201, Body: body}
}

// listInvites lists the pending invites (the unused and unrevoked
// ones, expired or not: the owner manages and revokes them), without
// the tokens. Owner only.
func (a *Auth) listInvites(ctx context.Context, req hub.Request, u User) hub.Response {
	if u.Role != RoleOwner {
		return errResponse(403, "forbidden")
	}
	invites, err := a.store.ListPendingInvites(ctx)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	items := make([]inviteListItem, 0, len(invites))
	for _, inv := range invites {
		items = append(items, inviteListItem{ID: inv.ID, Email: inv.Email, Role: inv.Role, CreatedMS: inv.CreatedMS, ExpiresMS: inv.ExpiresMS})
	}
	body, _ := json.Marshal(struct {
		Invites []inviteListItem `json:"invites"`
	}{Invites: items})
	return hub.Response{Status: 200, Body: body}
}

// revokeInvite revokes one invite, by id. Owner only; an unknown id
// answers 404.
func (a *Auth) revokeInvite(ctx context.Context, req hub.Request, u User) hub.Response {
	if u.Role != RoleOwner {
		return errResponse(403, "forbidden")
	}
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	id := req.PathParam("id")
	if id == "" {
		return errResponse(404, "not_found")
	}
	inv, found, err := a.store.FindInviteByID(ctx, id)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	if !found {
		return errResponse(404, "not_found")
	}
	if err := a.store.RevokeInvite(ctx, inv.ID); err != nil {
		return errResponse(503, "unavailable")
	}
	return hub.Response{Status: 204}
}

type acceptInviteBody struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// acceptInvite creates the member with the e-mail of the invite and
// consumes the invite. An unknown, malformed, expired, revoked or
// already-used token answers the same 400 invalid_invite; an e-mail
// that already has an account answers 409 email_taken. The password
// hash and the ids come out before any write, and the consumption,
// the user and the session are one atomic batch conditional on the
// invite still being unused, unrevoked and unexpired: a failure rolls
// back everything, the consumption included.
func (a *Auth) acceptInvite(ctx context.Context, req hub.Request) hub.Response {
	if !a.checkContentType(req) {
		return errResponse(415, "unsupported_media_type")
	}
	var b acceptInviteBody
	if err := decodeStrict(req.Body, &b); err != nil {
		return errResponse(400, "bad_request")
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return errResponse(400, "bad_request")
	}
	if !PolicyOK(b.Password) {
		return errResponse(400, "weak_password")
	}
	token, err := b64urlNoPad.DecodeString(b.Token)
	if err != nil || len(token) != sessionIDSize {
		return errResponse(400, "invalid_invite")
	}
	tokenHash := sha256Hex(token)
	inv, found, err := a.store.FindInviteByTokenHash(ctx, tokenHash)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	now := a.now()
	if !found || inv.Used || inv.Revoked || now.UnixMilli() >= inv.ExpiresMS {
		return errResponse(400, "invalid_invite")
	}
	_, taken, err := a.store.FindUserByEmailNorm(ctx, inv.EmailNorm)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	if taken {
		return errResponse(409, "email_taken")
	}
	hash, err := HashPassword(a.rand, b.Password)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	user := User{
		Email:        inv.Email,
		EmailNorm:    inv.EmailNorm,
		Name:         name,
		PasswordHash: hash,
		Role:         inv.Role,
		AuthVersion:  1,
		CreatedMS:    now.UnixMilli(),
	}
	user.ID, err = a.randomID()
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row, cookie, err := NewSession(a.rand, a.key, now)
	if err != nil {
		return errResponse(503, "unavailable")
	}
	row.UserID = user.ID
	row.AuthVersion = 1
	if err := a.store.AcceptInviteWithSession(ctx, inv.ID, now.UnixMilli(), user, row); err != nil {
		// The batch failed: re-read to tell a lost condition (the
		// invite used, revoked or expired in between) and a taken
		// e-mail from a store failure; the invite survived the
		// rollback in every case.
		if cur, found, rerr := a.store.FindInviteByID(ctx, inv.ID); rerr == nil && found && (cur.Used || cur.Revoked || a.now().UnixMilli() >= cur.ExpiresMS) {
			return errResponse(400, "invalid_invite")
		}
		if _, taken, rerr := a.store.FindUserByEmailNorm(ctx, inv.EmailNorm); rerr == nil && taken {
			return errResponse(409, "email_taken")
		}
		return errResponse(503, "unavailable")
	}
	return withCookie(201, SessionCookie(cookie), sessionBody(user))
}
