package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestHealthRoundTrip: the file is written atomically in the shape of
// the contract, 0600, and ReadHealth gives it back.
func TestHealthRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "health.json")
	st := &state{
		state:         StateRateLimited,
		lastError:     StateRateLimited,
		lastOKMS:      1_700_000_000_100,
		lastAttemptMS: 1_700_000_000_200,
		hub:           "https://hub.example",
		statePath:     path,
	}
	if err := st.writeHealth(); err != nil {
		t.Fatalf("writeHealth: %v", err)
	}
	hh, err := ReadHealth(path)
	if err != nil {
		t.Fatalf("ReadHealth: %v", err)
	}
	want := Health{
		State:         StateRateLimited,
		LastOKMS:      1_700_000_000_100,
		LastAttemptMS: 1_700_000_000_200,
		LastError:     StateRateLimited,
		Hub:           "https://hub.example",
	}
	if hh != want {
		t.Errorf("health = %+v, want %+v", hh, want)
	}
	// The file is the exact contract shape, in the field order of the
	// contract.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the raw file: %v", err)
	}
	wantRaw := `{"state":"rate_limited","last_ok_ms":1700000000100,"last_attempt_ms":1700000000200,"last_error":"rate_limited","hub":"https://hub.example"}` + "\n"
	if string(raw) != wantRaw {
		t.Errorf("raw health = %s, want %s", raw, wantRaw)
	}
	// Windows has no POSIX modes (a file reports 0666): the private
	// mode is a Unix contract.
	if fi, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("the health file mode = %v, want 0600", fi.Mode().Perm())
	}
	// No temp file is left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read the health directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the health directory holds %d entries, want only the file", len(entries))
	}
}

// TestHealthKeyNeverStored: the state holds the key, and the file
// never carries it.
func TestHealthKeyNeverStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.json")
	st := &state{
		state:     StateOK,
		lastOKMS:  1,
		hub:       "https://hub.example",
		key:       testKey,
		statePath: path,
	}
	if err := st.writeHealth(); err != nil {
		t.Fatalf("writeHealth: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the raw file: %v", err)
	}
	if bytes.Contains(raw, []byte(testKey)) {
		t.Error("the health file carries the machine key")
	}
}

func TestReadHealthMissing(t *testing.T) {
	if _, err := ReadHealth(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("ReadHealth accepted an absent file")
	}
}

// TestWriteAtomicWhileAReaderHoldsTheFile: a reader that has the
// health file open (nau doctor, for one) must not make the write fail.
// On Windows the rename over an open file fails until the reader
// closes it, and the write waits for that; on Unix the rename never
// waits. The reader here closes the file after 100 ms.
func TestWriteAtomicWhileAReaderHoldsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "health.json")
	if err := writeAtomic(path, []byte("{\"n\":1}\n")); err != nil {
		t.Fatalf("the first write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open the health file as a reader: %v", err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.Close()
		close(released)
	}()
	if err := writeAtomic(path, []byte("{\"n\":2}\n")); err != nil {
		t.Fatalf("a write while a reader holds the file failed: %v", err)
	}
	<-released
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "{\"n\":2}\n" {
		t.Fatalf("health = %q, want the second write", got)
	}
}
