package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/nau/internal/config"
)

// exeName is the file name of an executable on the running platform:
// on Windows the PATH lookup only finds names with a PATHEXT extension.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// binPathDir builds a PATH directory that holds exactly the named
// executable files (a non-runnable placeholder body is enough:
// resolution only looks the names up).
func binPathDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, exeName(n)), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestResolveBin covers automatic and explicit backend selections.
func TestResolveBin(t *testing.T) {
	empty := t.TempDir()
	onlySoho := binPathDir(t, "soho")
	both := binPathDir(t, "soho")
	onlyOther := binPathDir(t, "other")
	cases := []struct {
		name   string
		stored string
		path   string
		want   string
	}{
		{"auto: soho found", "", both, filepath.Join(both, exeName("soho"))},
		{"auto: nothing found", "", empty, "soho"},
		{"auto: only soho is found", "", onlySoho, filepath.Join(onlySoho, exeName("soho"))},
		{"explicit bare name passes through", "soho", onlyOther, "soho"},
		{"explicit relative path passes through", "./custom", empty, "./custom"},
		{"explicit absolute path passes through", "/opt/bin/backend", empty, "/opt/bin/backend"},
		{"explicit other name passes through", "backend", both, "backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			if got := config.ResolveBin(tc.stored); got != tc.want {
				t.Errorf("ResolveBin(%q) with PATH=%s = %q, want %q", tc.stored, tc.path, got, tc.want)
			}
		})
	}
}

// TestLoadResolvesSohoBin checks resolution of an absent selection.
func TestLoadResolvesSohoBin(t *testing.T) {
	both := binPathDir(t, "soho")
	t.Setenv("PATH", both)

	// Fresh: nothing stored, soho resolves.
	fresh := t.TempDir()
	cfg, err := config.Load(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HerdrSohoBin != filepath.Join(both, exeName("soho")) {
		t.Errorf("fresh config: HerdrSohoBin = %q, want %q", cfg.HerdrSohoBin, filepath.Join(both, exeName("soho")))
	}
}

func TestHerdrSohoBinStored(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cases := []struct {
		name string
		data string
		want string
	}{
		{name: "missing file", want: ""},
		{name: "missing key", data: "machine_label=machine-a\n", want: ""},
		{name: "empty auto", data: "herdr_soho_bin=\n", want: ""},
		{name: "explicit name", data: "herdr_soho_bin=backend\n", want: "backend"},
		{name: "explicit path", data: "herdr_soho_bin=/opt/bin/backend\n", want: "/opt/bin/backend"},
		{name: "last occurrence wins", data: "herdr_soho_bin=first\nherdr_soho_bin=second\n", want: "second"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.data != "" {
				if err := os.WriteFile(filepath.Join(dir, "config"), []byte(tc.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.HerdrSohoBinStored(); got != tc.want {
				t.Errorf("HerdrSohoBinStored() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSetStoresAutoEmpty: setting herdr_soho_bin to the auto value
// (empty) stores the empty auto form in the file; an
// explicit value is stored unchanged. Setting the auto value again is
// idempotent and keeps the auto form.
func TestSetStoresAutoEmpty(t *testing.T) {
	both := binPathDir(t, "soho")
	t.Setenv("PATH", both)

	autoValues := []string{""}
	for _, v := range autoValues {
		dir := t.TempDir()
		if err := config.Set(dir, "herdr_soho_bin", v); err != nil {
			t.Fatalf("Set(herdr_soho_bin=%q): %v", v, err)
		}
		data, err := os.ReadFile(dir + "/config")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "herdr_soho_bin=\n") {
			t.Errorf("Set(%q): the file does not store the empty auto form: %q", v, data)
		}
	}

	// Idempotent: setting the empty value again keeps the stored form
	// and the effective value unchanged.
	dir := t.TempDir()
	if err := config.Set(dir, "herdr_soho_bin", ""); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(dir, "herdr_soho_bin", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dir + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "herdr_soho_bin=\n") || strings.Contains(string(data), "herdr_soho_bin=herdr") {
		t.Errorf("idempotent auto: the file must keep the empty auto form: %q", data)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HerdrSohoBin != filepath.Join(both, exeName("soho")) {
		t.Errorf("effective after the idempotent set = %q, want the resolved soho", cfg.HerdrSohoBin)
	}

	// Explicit values are stored unchanged.
	for v, want := range map[string]string{"herdr" + "-soho": "herdr_soho_bin=" + "herdr" + "-soho", "soho": "herdr_soho_bin=soho", "/opt/bin/backend": "herdr_soho_bin=/opt/bin/backend"} {
		dir := t.TempDir()
		if err := config.Set(dir, "herdr_soho_bin", v); err != nil {
			t.Fatalf("Set(herdr_soho_bin=%q): %v", v, err)
		}
		data, err := os.ReadFile(dir + "/config")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf("Set(%q): the file must store %q: %q", v, want, data)
		}
		cfg, err := config.Load(dir)
		if err != nil || cfg.HerdrSohoBin != v {
			t.Errorf("explicit selection changed on load: got %q error %v", cfg.HerdrSohoBin, err)
		}

	}
}

// TestSetOtherKeysKeepStoredForm: setting another key rewrites the file
// without persisting the resolved absolute path of the backend binary —
// the stored (auto) form is what the file keeps.
func TestSetOtherKeysKeepStoredForm(t *testing.T) {
	both := binPathDir(t, "soho")
	t.Setenv("PATH", both)
	dir := t.TempDir()
	if err := config.Set(dir, "machine_label", "m1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dir + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "machine_label=m1\n" {
		t.Errorf("setting another key must preserve the absent automatic backend record, got %q", data)
	}
	if strings.Contains(string(data), "herdr_soho_bin=soho") {
		t.Errorf("the resolved path must not be persisted: %q", data)
	}
}

// Restoring automatic lookup of the retired name changes the resolved executable.
func TestAutomaticBinIgnoresRetiredName(t *testing.T) {
	retired := "herdr" + "-soho"
	onlyRetired := binPathDir(t, retired)
	t.Setenv("PATH", onlyRetired)
	if got := config.ResolveBin(""); got != "soho" {
		t.Fatalf("automatic resolution=%q, want soho", got)
	}
	if config.IsAutoBin(retired) {
		t.Fatal("a stored retired name is explicit")
	}
	if got := config.ResolveBin(retired); got != retired {
		t.Fatalf("explicit resolution=%q", got)
	}
	current := binPathDir(t, "soho")
	t.Setenv("PATH", current)
	if got := config.ResolveBin(""); got != filepath.Join(current, exeName("soho")) {
		t.Fatalf("automatic resolution=%q", got)
	}
}
