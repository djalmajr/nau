package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"strings"
	"testing"
)

//go:embed session.go
var sessionSrc string

// TestSessionComparisonIsConstantTime is the structural check the
// decision allows for the constant-time property: the source of the
// session comparison must use hmac.Equal, not a string or byte == that
// a mutation could introduce and a behavioral test could not catch.
func TestSessionComparisonIsConstantTime(t *testing.T) {
	if !strings.Contains(sessionSrc, "hmac.Equal(") {
		t.Fatal("the session comparison does not use hmac.Equal")
	}
	// The naive comparison a mutation would introduce must be absent
	// from the ParseSession path.
	if strings.Contains(sessionSrc, "string(mac) == string(") ||
		strings.Contains(sessionSrc, "mac == signSession") ||
		strings.Contains(sessionSrc, "bytes.Equal(mac, signSession") {
		t.Fatal("the session comparison uses a non-constant-time equality")
	}
}

func TestNewSessionAndParseRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	row, cookie, err := NewSession(&deterministicReader{bytes: make([]byte, 64)}, key, fixedNow())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// The cookie is <id>.<mac>, both unpadded base64url.
	parts := strings.Split(cookie, ".")
	if len(parts) != 2 {
		t.Fatalf("the cookie has %d parts", len(parts))
	}
	id, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(id) != sessionIDSize {
		t.Fatalf("the cookie id = %d bytes (err %v)", len(id), err)
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(mac) != sha256.Size {
		t.Fatalf("the cookie mac = %d bytes (err %v)", len(mac), err)
	}
	// The stored token is the SHA-256 of the id, never the id itself.
	sum := sha256.Sum256(id)
	if row.TokenSHA != sha256Hex(id) {
		t.Fatalf("the stored token = %q, want the sha of the id", row.TokenSHA)
	}
	_ = sum
	// The mac is the HMAC of the id with the key.
	wantMac := hmacMAC(key, id)
	if !hmac.Equal(mac, wantMac) {
		t.Fatal("the cookie mac is not the HMAC of the id")
	}
	got, ok := ParseSession(cookie, key)
	if !ok || got != row.TokenSHA {
		t.Fatalf("ParseSession = %q, %v, want %q true", got, ok, row.TokenSHA)
	}
}

// hmacMAC is the reference HMAC for the tests.
func hmacMAC(key, id []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(id)
	return m.Sum(nil)
}

func TestParseSessionRejectsBadValues(t *testing.T) {
	key := make([]byte, 32)
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(i + 1)
	}
	row, cookie, err := NewSession(&deterministicReader{bytes: make([]byte, 64)}, key, fixedNow())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cases := map[string]struct {
		value string
		key   []byte
	}{
		"tampered id":   {mutateID(cookie, 1), key},
		"tampered mac":  {mutateMac(cookie, 1), key},
		"wrong key":     {cookie, other},
		"truncated":     {cookie[:len(cookie)-3], key},
		"no dot":        {strings.Replace(cookie, ".", "", 1), key},
		"two dots":      {cookie + ".x", key},
		"bad base64 id": {"!!!!." + macPart(cookie), key},
		"short id":      {"AAAA." + macPart(cookie), key},
		"empty":         {"", key},
		"only mac":      {".zzzz", key},
	}
	for name, c := range cases {
		if _, ok := ParseSession(c.value, c.key); ok {
			t.Fatalf("%s: the bad value parses", name)
		}
	}
	_ = row
}

func macPart(cookie string) string {
	p := strings.SplitN(cookie, ".", 2)
	if len(p) != 2 {
		return ""
	}
	return p[1]
}

// mutateID flips one byte of the id part of the cookie.
func mutateID(cookie string, pos int) string {
	p := strings.SplitN(cookie, ".", 2)
	id, _ := base64.RawURLEncoding.DecodeString(p[0])
	id[pos] ^= 0xff
	return base64.RawURLEncoding.EncodeToString(id) + "." + p[1]
}

// mutateMac flips one byte of the mac part of the cookie.
func mutateMac(cookie string, pos int) string {
	p := strings.SplitN(cookie, ".", 2)
	mac, _ := base64.RawURLEncoding.DecodeString(p[1])
	mac[pos] ^= 0xff
	return p[0] + "." + base64.RawURLEncoding.EncodeToString(mac)
}

func TestSessionCookieAttributes(t *testing.T) {
	c := SessionCookie("idvalue.macvalue")
	for _, want := range []string{CookieName + "=idvalue.macvalue", "Path=/", "HttpOnly", "Secure", "SameSite=Lax", "Max-Age=604800"} {
		if !strings.Contains(c, want) {
			t.Fatalf("the cookie %q lacks %q", c, want)
		}
	}
	// The cleared cookie is empty with Max-Age=0.
	cl := ClearedSessionCookie()
	if !strings.Contains(cl, CookieName+"=") || strings.Contains(cl, "Max-Age=604800") {
		t.Fatalf("the cleared cookie = %q", cl)
	}
	if !strings.Contains(cl, "Max-Age=0") {
		t.Fatalf("the cleared cookie lacks Max-Age=0: %q", cl)
	}
}

func TestParseCookieValue(t *testing.T) {
	v, err := ParseCookieValue("other=1; " + CookieName + "=id.mac; x=2")
	if err != nil || v != "id.mac" {
		t.Fatalf("ParseCookieValue = %q, %v", v, err)
	}
	if _, err := ParseCookieValue("other=1"); err == nil {
		t.Fatal("a header without the session cookie is not an error")
	}
	if _, err := ParseCookieValue(""); err == nil {
		t.Fatal("an empty header is not an error")
	}
	// Case-insensitive on the cookie name.
	v, err = ParseCookieValue("NAU_SESSION=up.mac")
	if err != nil || v != "up.mac" {
		t.Fatalf("case-insensitive name = %q, %v", v, err)
	}
}

func TestValidateSession(t *testing.T) {
	base := fixedNow().UnixMilli()
	row := SessionRow{TokenSHA: "t", UserID: "u", CreatedMS: base, ExpiresMS: base + 1000, Revoked: false}
	if !ValidateSession(row, base) {
		t.Fatal("a live session is not valid")
	}
	if ValidateSession(row, base+1000) {
		t.Fatal("an expired session is valid at its expiration")
	}
	r := row
	r.Revoked = true
	if ValidateSession(r, base) {
		t.Fatal("a revoked session is valid")
	}
}
