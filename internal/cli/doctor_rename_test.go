package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// countEnv reports how many entries of env carry the given key (exact,
// unix semantics), and whether every one of them equals want.
func countEnv(env []string, key, want string) (n int, allWant bool) {
	allWant = true
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			n++
			if kv != want {
				allWant = false
			}
		}
	}
	return
}

// TestDoctorConfigChildNowrite verifies a single current NOWRITE flag.
func TestDoctorConfigChildNowrite(t *testing.T) {
	exe, fakeDir := installFakeSoho(t,
		capsRule(capsJSON, 0),
		fakesoho.Rule{Argv: []string{"config"}, Stdout: configTable(wakeRow), Code: 0},
	)
	cases := []struct {
		name   string
		parent []string
	}{
		{"parent without NOWRITE", fakeChildEnv},
		{"parent with SOHO_NOWRITE", append(append([]string{}, fakeChildEnv...), "SOHO_NOWRITE=0")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			setSohoConfig(t, cfgDir, exe, "")
			var out, errb bytes.Buffer
			env := Env{
				Stdin:     strings.NewReader(""),
				Stdout:    &out,
				Stderr:    &errb,
				Getenv:    func(string) string { return "" },
				Environ:   func() []string { return tc.parent },
				ConfigDir: cfgDir,
				Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
				Sleep:     sleepCtx,
			}
			exit := Run([]string{"doctor"}, env)
			if exit != 0 {
				t.Fatalf("doctor exit = %d, stderr %q", exit, errb.String())
			}
			if !strings.Contains(out.String(), `"wake_hook":"true"`) {
				t.Fatalf("the wake hook classification must not depend on the parent NOWRITE variables, stdout %q", out.String())
			}
			calls, err := fakesoho.ReadCalls(fakeDir)
			if err != nil {
				t.Fatalf("read the fake call log: %v", err)
			}
			var configCall *fakesoho.Call
			for i := len(calls) - 1; i >= 0; i-- {
				if len(calls[i].Argv) == 1 && calls[i].Argv[0] == "config" {
					configCall = &calls[i]
					break
				}
			}
			if configCall == nil {
				t.Fatalf("no config call in the fake log: %v", calls)
			}
			nSoho, okSoho := countEnv(configCall.Env, "SOHO_NOWRITE", "SOHO_NOWRITE=1")
			if nSoho != 1 || !okSoho {
				t.Errorf("config child environment has %d SOHO_NOWRITE entries (all =1: %t), want exactly SOHO_NOWRITE=1: %v", nSoho, okSoho, configCall.Env)
			}
			nLegacy, _ := countEnv(configCall.Env, "HERDR_"+"SOHO_NOWRITE", "")
			if nLegacy != 0 {
				t.Errorf("config child environment has %d retired NOWRITE entries, want none", nLegacy)
			}
		})
	}
}

// Mutation captured: removing the automatic warning makes the warning assertion fail.
func TestDoctorAutomaticBinWarning(t *testing.T) {
	const wantWarning = "nau: doctor: herdr_soho_bin is automatic and resolved from the PATH; an ssh forced command (nau gate) may run with a minimal PATH that omits the backend; 'nau config set herdr_soho_bin <absolute path>' pins it"
	exe, fakeDir := installFakeSoho(t,
		capsRule(capsJSON, 0),
		fakesoho.Rule{Argv: []string{"config"}, Stdout: configTable(wakeRow), Code: 0},
	)
	t.Setenv("PATH", fakeDir)
	for _, tc := range []struct {
		name string
		data *string
	}{
		{name: "missing file"},
		{name: "empty stored value", data: stringPtr("herdr_soho_bin=\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			configPath := filepath.Join(cfgDir, "config")
			var before []byte
			if tc.data != nil {
				before = []byte(*tc.data)
				if err := os.WriteFile(configPath, before, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			stdout, stderr, exit := runHermes(t, cfgDir, false, "", "doctor")
			controlDir := t.TempDir()
			setSohoConfig(t, controlDir, exe, "")
			controlStdout, _, controlExit := runHermes(t, controlDir, false, "", "doctor")
			var line map[string]any
			if err := json.Unmarshal([]byte(stdout), &line); err != nil {
				t.Fatalf("doctor stdout %q is not one JSON line: %v", stdout, err)
			}
			if exit != 0 {
				t.Fatalf("doctor exit = %d, want 0 (stderr %q)", exit, stderr)
			}
			if stdout != controlStdout || exit != controlExit {
				t.Errorf("automatic backend changed the doctor result: stdout=%q exit=%d, explicit control stdout=%q exit=%d", stdout, exit, controlStdout, controlExit)
			}
			if n := strings.Count(stderr, wantWarning); n != 1 {
				t.Errorf("stderr %q must carry exactly one automatic warning, found %d", stderr, n)
			}
			if strings.Contains(stderr, "soho capabilities missing") {
				t.Errorf("valid backend must not report missing capabilities: %q", stderr)
			}
			got, err := os.ReadFile(configPath)
			if tc.data == nil {
				if !os.IsNotExist(err) {
					t.Errorf("automatic doctor created config: read error %v", err)
				}
			} else if err != nil || !bytes.Equal(got, before) {
				t.Errorf("automatic doctor changed config: read error %v or bytes differ", err)
			}
		})
	}
}

func TestDoctorExplicitBinDoesNotWarnAutomatic(t *testing.T) {
	exe, _ := installFakeSoho(t,
		capsRule(capsJSON, 0),
		fakesoho.Rule{Argv: []string{"config"}, Stdout: configTable(wakeRow), Code: 0},
	)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	_, stderr, exit := runHermes(t, cfgDir, false, "", "doctor")
	if exit != 0 {
		t.Fatalf("doctor exit = %d, want 0 (stderr %q)", exit, stderr)
	}
	if strings.Contains(stderr, "herdr_soho_bin is automatic and resolved from the PATH") {
		t.Errorf("explicit backend must not report automatic warning: %q", stderr)
	}
}

// Mutation captured: removing the automatic warning makes the warning assertion fail.
func TestDoctorAutomaticBinWarningKeepsCapabilityExit(t *testing.T) {
	const wantWarning = "nau: doctor: herdr_soho_bin is automatic and resolved from the PATH; an ssh forced command (nau gate) may run with a minimal PATH that omits the backend; 'nau config set herdr_soho_bin <absolute path>' pins it"
	exe, fakeDir := installFakeSoho(t,
		capsRule(`{"schema":1,"ephemeral_job":1}`, 0),
		fakesoho.Rule{Argv: []string{"config"}, Stdout: configTable(wakeRow), Code: 0},
	)
	t.Setenv("PATH", fakeDir)
	cfgDir := t.TempDir()
	configPath := filepath.Join(cfgDir, "config")
	before := []byte("herdr_soho_bin=\n")
	if err := os.WriteFile(configPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	autoStdout, stderr, exit := runHermes(t, cfgDir, false, "", "doctor")
	if exit != 43 {
		t.Fatalf("doctor exit = %d, want 43 (stderr %q)", exit, stderr)
	}
	controlDir := t.TempDir()
	setSohoConfig(t, controlDir, exe, "")
	controlStdout, _, controlExit := runHermes(t, controlDir, false, "", "doctor")
	if autoStdout != controlStdout || exit != controlExit {
		t.Errorf("automatic backend changed the doctor result: stdout=%q exit=%d, explicit control stdout=%q exit=%d", autoStdout, exit, controlStdout, controlExit)
	}
	warningAt := strings.Index(stderr, wantWarning)
	capabilityAt := strings.Index(stderr, "nau: soho capabilities missing")
	if warningAt < 0 || capabilityAt < 0 || warningAt >= capabilityAt {
		t.Errorf("stderr %q must place the automatic warning before the capability warning", stderr)
	}
	got, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(got, before) {
		t.Errorf("automatic doctor changed config: read error %v or bytes differ", err)
	}
}

func TestDoctorAutomaticBinWarningMissingBackend(t *testing.T) {
	const wantWarning = "nau: doctor: herdr_soho_bin is automatic and resolved from the PATH; an ssh forced command (nau gate) may run with a minimal PATH that omits the backend; 'nau config set herdr_soho_bin <absolute path>' pins it"
	t.Setenv("PATH", t.TempDir())
	cfgDir := t.TempDir()
	stdout, stderr, exit := runHermes(t, cfgDir, false, "", "doctor")
	var line map[string]any
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not one JSON line: %v", stdout, err)
	}
	if exit != 43 {
		t.Fatalf("doctor exit = %d, want 43 (stderr %q)", exit, stderr)
	}
	if !strings.Contains(stderr, wantWarning) || !strings.Contains(stderr, "nau: soho capabilities missing") {
		t.Errorf("stderr %q must carry both automatic and capability warnings", stderr)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "config")); !os.IsNotExist(err) {
		t.Errorf("automatic missing-backend doctor created config: stat error %v", err)
	}
}

func stringPtr(value string) *string {
	return &value
}
