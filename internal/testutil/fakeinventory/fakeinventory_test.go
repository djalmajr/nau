package fakeinventory_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/nau/internal/testutil/fakeinventory"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// childEnv keeps the fake soho's environment minimal, as the real
// invocation does not depend on the parent's.
var childEnv = []string{"PATH=/bin"}

func runFake(t *testing.T, exe string, argv ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(exe, argv...)
	cmd.Env = childEnv
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run %v: %v (stderr: %s)", argv, err, errb.String())
	}
	return code, out.String()
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "inventory", "testdata", name)
}

// TestRules: capabilities advertises the inventory capability and
// inventory prints the fixture content verbatim with exit 0.
func TestRules(t *testing.T) {
	fixtureFile := fixture(t, "none.json")
	data, err := os.ReadFile(fixtureFile)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	dir := t.TempDir()
	exe := fakesoho.Install(t, dir, fakeinventory.Rules(fixtureFile)...)

	code, out := runFake(t, exe, "capabilities", "--json")
	if code != 0 {
		t.Fatalf("capabilities: exit %d, want 0", code)
	}
	if out != fakeinventory.CapabilitiesLine+"\n" {
		t.Errorf("capabilities stdout = %q, want %q", out, fakeinventory.CapabilitiesLine+"\n")
	}

	code, out = runFake(t, exe, "inventory", "--json")
	if code != 0 {
		t.Fatalf("inventory: exit %d, want 0", code)
	}
	if out != string(data) {
		t.Errorf("inventory stdout = %q, want the fixture content", out)
	}
}

// TestUnavailable: the unreadable terminal server exits 4 and prints
// nothing.
func TestUnavailable(t *testing.T) {
	dir := t.TempDir()
	exe := fakesoho.Install(t, dir, fakeinventory.Unavailable()...)
	code, out := runFake(t, exe, "inventory", "--json")
	if code != 4 {
		t.Errorf("exit = %d, want 4", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}

// TestNoCapability: the capabilities answer has no inventory key.
func TestNoCapability(t *testing.T) {
	dir := t.TempDir()
	exe := fakesoho.Install(t, dir, fakeinventory.NoCapability()...)
	code, out := runFake(t, exe, "capabilities", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(out, "inventory") {
		t.Errorf("stdout %q names the inventory capability", out)
	}
	if out != fakeinventory.NoCapabilityLine+"\n" {
		t.Errorf("stdout = %q, want %q", out, fakeinventory.NoCapabilityLine+"\n")
	}
}
