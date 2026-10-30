package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The session cookie and the session lifetime of the decision.
const (
	CookieName = "nau_session"

	// SessionTTL is the absolute expiration of a session: 7 days.
	SessionTTL = 7 * 24 * time.Hour

	sessionIDSize = 32
	sessionMaxAge = int64(7 * 24 * 60 * 60)
)

// b64urlNoPad is the unpadded base64url alphabet of the cookie and of
// the invite tokens.
var b64urlNoPad = base64.RawURLEncoding

// SessionRow is one stored session: the SHA-256 of the 32-byte random
// id (the id itself lives only in the cookie), the user, the creation
// and the absolute expiration, and the revocation flag.
type SessionRow struct {
	TokenSHA  string
	UserID    string
	CreatedMS int64
	ExpiresMS int64
	Revoked   bool
}

// NewSession builds one session: a 32-byte random id, its SHA-256 for
// the store and the signed cookie value <id>.<HMAC-SHA256(id, key)>,
// both parts unpadded base64url. now is the caller's clock (the Auth
// configuration), never the package clock.
func NewSession(rand io.Reader, key []byte, now time.Time) (row SessionRow, cookieValue string, err error) {
	id := make([]byte, sessionIDSize)
	if _, err := io.ReadFull(rand, id); err != nil {
		return SessionRow{}, "", fmt.Errorf("accounts: read the session id: %w", err)
	}
	mac := signSession(key, id)
	row = SessionRow{
		TokenSHA:  sha256Hex(id),
		CreatedMS: now.UnixMilli(),
		ExpiresMS: now.Add(SessionTTL).UnixMilli(),
	}
	cookieValue = b64urlNoPad.EncodeToString(id) + "." + b64urlNoPad.EncodeToString(mac)
	return row, cookieValue, nil
}

// signSession is the HMAC-SHA256 of the session id with the session key.
func signSession(key, id []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(id)
	return m.Sum(nil)
}

// ParseSession reads the signed cookie value and checks the signature
// in constant time (hmac.Equal). Any malformed value is a mismatch,
// never a panic; on success it returns the SHA-256 of the id, the key
// the store looks the row up with.
func ParseSession(cookieValue string, key []byte) (string, bool) {
	parts := strings.Split(cookieValue, ".")
	if len(parts) != 2 {
		return "", false
	}
	id, err := b64urlNoPad.DecodeString(parts[0])
	if err != nil || len(id) != sessionIDSize {
		return "", false
	}
	mac, err := b64urlNoPad.DecodeString(parts[1])
	if err != nil || len(mac) != sha256.Size {
		return "", false
	}
	if !hmac.Equal(mac, signSession(key, id)) {
		return "", false
	}
	return sha256Hex(id), true
}

// SessionCookie is the Set-Cookie value of a live session.
func SessionCookie(cookieValue string) string {
	return CookieName + "=" + cookieValue + cookieAttrs(sessionMaxAge)
}

// ClearedSessionCookie is the Set-Cookie value of the logout: the empty
// value with Max-Age=0, so the browser drops the cookie.
func ClearedSessionCookie() string {
	return CookieName + "=" + cookieAttrs(0)
}

// cookieAttrs is the attribute suffix of the session cookie: Path=/,
// HttpOnly, Secure, SameSite=Lax and the Max-Age in seconds, preceded
// by the separator after the value.
func cookieAttrs(maxAgeSec int64) string {
	return fmt.Sprintf("; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=%s",
		strconv.FormatInt(maxAgeSec, 10))
}

// ParseCookieValue pulls the session value out of one cookie header:
// the cookie named nau_session, or an error when it is absent.
func ParseCookieValue(header string) (string, error) {
	if header == "" {
		return "", errors.New("accounts: no cookie header")
	}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if i := strings.IndexByte(part, '='); i > 0 {
			if strings.EqualFold(strings.TrimSpace(part[:i]), CookieName) {
				return part[i+1:], nil
			}
		}
	}
	return "", errors.New("accounts: no session cookie")
}

// ValidateSession is the whole session check of one request: the
// signature, the stored row, the absolute expiration and the
// revocation. It reports the session row, the user id and whether the
// session is valid for now.
func ValidateSession(row SessionRow, nowMS int64) bool {
	return !row.Revoked && nowMS < row.ExpiresMS
}
