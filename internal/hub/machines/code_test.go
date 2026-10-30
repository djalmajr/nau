package machines

import (
	"context"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
)

// testService is a service over the fakes, for the unit tests of the
// code helpers.
func testService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(Config{
		Store:    stubStore{},
		Machines: NewMemory(),
		SessionUser: func(ctx context.Context, req hub.Request) (accounts.User, bool) {
			return accounts.User{}, false
		},
		Now:  time.Now,
		Rand: &detReader{b: []byte("code-test-source-0123456789")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// TestDrawCode is the shape of the drawn codes: eight letters of the
// Crockford alphabet, shown as XXXX-XXXX.
func TestDrawCode(t *testing.T) {
	svc := testService(t)
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		code, err := svc.drawCode()
		if err != nil {
			t.Fatalf("drawCode %d: %v", i, err)
		}
		if !validCode(code) {
			t.Fatalf("drawCode %d = %q, want eight Crockford letters", i, code)
		}
		if displayCode(code) != code[:4]+"-"+code[4:] {
			t.Fatalf("displayCode(%q) = %q, want the hyphen in the middle", code, displayCode(code))
		}
		seen[code] = true
	}
	// The deterministic reader cycles the source: the codes are not
	// all equal (the reader advances one byte per letter).
	if len(seen) < 2 {
		t.Fatalf("the drawn codes are all equal: %v", seen)
	}
}

// TestNormalizeCode is the canonical form of the typed codes:
// uppercased, without the hyphens, and only the shape of the code is
// valid.
func TestNormalizeCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ABCDEFGH", "ABCDEFGH", true},
		{"abcd-efgh", "ABCDEFGH", true},
		{"ABCD-EFGH", "ABCDEFGH", true},
		{"abcdefgh", "ABCDEFGH", true},
		{"abcd", "ABCD", false},
		{"ABCDEFGHI", "ABCDEFGHI", false},
		{"I0000000", "I0000000", false}, // I is outside the alphabet
		{"L0000000", "L0000000", false}, // L is outside the alphabet
		{"O0000000", "O0000000", false}, // O is outside the alphabet
		{"U0000000", "U0000000", false}, // U is outside the alphabet
		{"0000-0000", "00000000", true}, // the digits are in the alphabet
		{"ABCD-EFGI", "ABCDEFGI", false},
		{"AB-CDE-FGH", "ABCDEFGH", true},  // every hyphen is dropped
		{"AB CDEFGH", "AB CDEFGH", false}, // the space stays and breaks the shape
	}
	for i, c := range cases {
		got := normalizeCode(c.in)
		if got != c.want {
			t.Fatalf("case %d: normalizeCode(%q) = %q, want %q", i, c.in, got, c.want)
		}
		if ok := validCode(got); ok != c.ok {
			t.Fatalf("case %d: validCode(%q) = %v, want %v", i, got, ok, c.ok)
		}
	}
}

// TestHashHexStability is the stored form of the secrets: the hex of
// the SHA-256, stable for the same value and different for another.
func TestHashHexStability(t *testing.T) {
	a := hashHex([]byte("ABCDEFGH"))
	b := hashHex([]byte("ABCDEFGH"))
	c := hashHex([]byte("ABCDEFGI"))
	if a != b {
		t.Fatalf("hashHex is not stable: %s and %s", a, b)
	}
	if a == c {
		t.Fatal("hashHex collides for different values")
	}
	if len(a) != 64 {
		t.Fatalf("hashHex = %q, want the 64 chars of the hex form", a)
	}
}
