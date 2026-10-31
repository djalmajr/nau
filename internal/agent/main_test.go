package agent

import (
	"os"
	"testing"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// TestMain lets the re-executed copy of this test binary play the fake
// soho the tests install (internal/testutil/fakesoho), and runs the
// tests when it is the test binary itself. After the re-executed roles
// (which exit before this point), the test process PATH points at an
// empty directory, so a bare binary name never resolves to a binary on
// the machine: every test that needs a binary passes an absolute fake
// path, and local runs behave like CI.
func TestMain(m *testing.M) {
	fakesoho.Main()
	emptyPath, err := os.MkdirTemp("", "agent-test-path-")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(emptyPath)
	os.Setenv("PATH", emptyPath)
	os.Exit(m.Run())
}
