package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDefaults: a missing file or directory yields the defaults
// without error and without creating anything.
func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MachineLabel != "" {
		t.Errorf("MachineLabel = %q, want empty", cfg.MachineLabel)
	}
	if cfg.HerdrSohoBin != "soho" {
		t.Errorf("HerdrSohoBin = %q, want the bare name soho (PATH empty)", cfg.HerdrSohoBin)
	}
	if cfg.HerdrSohoBinStored() != "" {
		t.Errorf("HerdrSohoBinStored = %q, want empty (the auto form)", cfg.HerdrSohoBinStored())
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Errorf("Load created the config file: %v", err)
	}
}

// TestLoadIgnoresRemovedKeys: an old configuration file that carries keys
// removed from the configuration still reads; the removed keys are
// ignored, the kept key takes its value, and the stored backend form is
// preserved.
func TestLoadIgnoresRemovedKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	data := "machine_label=m1\ndispatcher_url=http://x.invalid\npush_timeout_s=19\nroute_machines=mac-a,win-a\nherdr_soho_bin=/abs/soho\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load with removed keys: %v", err)
	}
	if cfg.MachineLabel != "m1" {
		t.Errorf("MachineLabel = %q, want m1", cfg.MachineLabel)
	}
	if cfg.HerdrSohoBin != "/abs/soho" || cfg.HerdrSohoBinStored() != "/abs/soho" {
		t.Errorf("HerdrSohoBin = %q (stored %q), want /abs/soho", cfg.HerdrSohoBin, cfg.HerdrSohoBinStored())
	}
	// The kept key of an all-removed file: an empty label.
	removedOnly := t.TempDir()
	if err := os.WriteFile(filepath.Join(removedOnly, "config"), []byte("dispatcher_url=http://x.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(removedOnly)
	if err != nil {
		t.Fatalf("Load of an all-removed file: %v", err)
	}
	if cfg.MachineLabel != "" || cfg.HerdrSohoBinStored() != "" {
		t.Errorf("all-removed file = %q stored %q, want the defaults", cfg.MachineLabel, cfg.HerdrSohoBinStored())
	}
}

// TestLoadMalformedLineStillErrors: a line without = is a format error
// whatever the key, and a removed key with a value is not one.
func TestLoadMalformedLineStillErrors(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantErr bool
	}{
		{"no equals", "machine_label", true},
		{"no equals removed key", "dispatcher_url", true},
		{"removed key with value", "dispatcher_url=http://x.invalid", false},
		{"unknown key with value", "no_such_key=1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config"), []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if (err != nil) != tc.wantErr {
				t.Errorf("Load %q: err = %v, want error %t", tc.line, err, tc.wantErr)
			}
		})
	}
}

// TestSetRefusesRemovedKey: setting a key the configuration no longer
// defines is refused as an unknown key, before anything is read or
// written; the refusal names the key.
func TestSetRefusesRemovedKey(t *testing.T) {
	for _, key := range []string{"dispatcher_url", "push_timeout_s", "herdr_bin", "route_machines", "route_disabled", "route_orchestrator_name", "route_probe_timeout_s", "no_such_key"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config")
			const initial = "machine_label=m1\n"
			if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			err := Set(dir, key, "value")
			if err == nil {
				t.Fatalf("Set %q: err = nil, want unknown key", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("Set %q error %q does not name the key", key, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != initial {
				t.Fatalf("refusal changed config: %q, %v", got, err)
			}
		})
	}
	// Refused on a fresh directory too: no file is created.
	dir := t.TempDir()
	if err := Set(dir, "dispatcher_url", "value"); err == nil {
		t.Fatal("Set removed key on a fresh dir: err = nil, want unknown key")
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Errorf("refused Set created the config file: %v", err)
	}
}

// TestValueKnownKeys: Value returns the effective values of the two keys
// and refuses a removed key with the unknown-key error that names it.
func TestValueKnownKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if err := Set(dir, "machine_label", "m1"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := cfg.Value("machine_label"); err != nil || v != "m1" {
		t.Errorf("Value(machine_label) = %q (err %v), want m1", v, err)
	}
	if v, err := cfg.Value("herdr_soho_bin"); err != nil || v != "soho" {
		t.Errorf("Value(herdr_soho_bin) = %q (err %v), want soho", v, err)
	}
	for _, key := range []string{"dispatcher_url", "push_timeout_s", "herdr_bin", "route_machines"} {
		if _, err := cfg.Value(key); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("Value(%s) = (err %v), want the unknown-key error naming the key", key, err)
		}
	}
}

// TestKeys: the two keys in canonical order.
func TestKeys(t *testing.T) {
	want := "machine_label,herdr_soho_bin"
	var got []string
	for _, k := range Keys() {
		got = append(got, k)
	}
	if strings.Join(got, ",") != want {
		t.Errorf("Keys = %v, want %v", got, strings.Split(want, ","))
	}
}
