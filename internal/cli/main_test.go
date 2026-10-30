package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// TestMain lets the re-executed test binary act as the fake soho.
func TestMain(m *testing.M) {
	fakesoho.Main()
	// Fail closed against a real soho: the test process PATH points at an
	// empty directory, so the default soho name never resolves to a binary
	// on the machine — every test that needs soho passes an absolute fake
	// path — and local runs behave like CI. The directory is removed after
	// the run.
	emptyPath, err := os.MkdirTemp("", "nau-test-path-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli test: create empty PATH dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("PATH", emptyPath); err != nil {
		fmt.Fprintf(os.Stderr, "cli test: set PATH: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(emptyPath)
	os.Exit(code)
}
