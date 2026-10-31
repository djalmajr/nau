package accounts

import (
	"testing"
	"time"
)

// fixedNow is the deterministic clock of the accounts tests.
func fixedNow() time.Time {
	return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
}

// deterministicReader is a fixed io.Reader for the tests: it serves the
// given bytes and then stops. A short request never errors, so the
// randomness of an id or a salt is reproducible.
type deterministicReader struct {
	bytes []byte
	pos   int
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.bytes) {
		// Serve zeros past the seed so the reader never fails a
		// ReadFull, keeping the tests deterministic.
		for i := range p {
			p[i] = 0
		}
		return len(p), nil
	}
	n := copy(p, r.bytes[r.pos:])
	r.pos += n
	return n, nil
}

// newAuth builds an Auth over the given store with the deterministic
// clock and a fresh seed.
func newAuth(t *testing.T, st AccountStore, bootstrapToken string) *Auth {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	a, err := New(Config{
		Store:          st,
		SessionKey:     key,
		BootstrapToken: bootstrapToken,
		Now:            fixedNow,
		Rand:           &deterministicReader{bytes: make([]byte, 1024)},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}
