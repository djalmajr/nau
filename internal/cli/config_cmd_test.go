package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigGetDefaults(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exit := runCLI(t, dir, false, "config", "get", "machine_label")
	if exit != 0 {
		t.Fatalf("config get exit = %d, stdout %q", exit, stdout)
	}
	if stdout != "{\"key\":\"machine_label\",\"value\":\"\"}\n" {
		t.Errorf("config get = %q", stdout)
	}
	// Read-only: a fresh config dir is not created.
	assertDirEmpty(t, dir)
}

func TestConfigConditionalRefusals(t *testing.T) {
	t.Run("NOWRITE", func(t *testing.T) {
		dir := t.TempDir()
		stdout, _, code := runCLI(t, dir, true, "config", "set", "--if-automatic", "herdr_soho_bin", "/backend")
		if code != 2 || !strings.Contains(stdout, "nowrite") {
			t.Fatalf("exit=%d stdout=%q", code, stdout)
		}
		assertDirEmpty(t, dir)
	})
	for _, tc := range []struct {
		key, value string
		code       int
		warning    string
	}{
		{"machine_label", "node", 2, ""},
		{"herdr_soho_bin", "relative", 0, "unsafe backend"},
		{"herdr_soho_bin", "/missing\nlabel=x", 0, "unsafe backend"},
		{"herdr_soho_bin", "/missing\rlabel=x", 0, "unsafe backend"},
		{"herdr_soho_bin", "/missing\x00", 0, "unsafe backend"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config")
		const initial = "# keep\nmachine_label=original\n"
		if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runCLI(t, dir, false, "config", "set", "--if-automatic", tc.key, tc.value)
		if code != tc.code || !strings.Contains(stderr, tc.warning) || strings.Count(stdout, "\n") != 1 || !json.Valid([]byte(stdout)) {
			t.Fatalf("key=%q value=%q exit=%d stdout=%q stderr=%q", tc.key, tc.value, code, stdout, stderr)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != initial {
			t.Fatalf("config changed: %q %v", data, err)
		}
	}
	for _, sep := range []string{"\r", "\n", "\x00"} {
		dir := t.TempDir()
		_, _, code := runCLI(t, dir, false, "config", "set", "machine_label", "node"+sep+"x=y")
		if code != 2 {
			t.Fatalf("record separator exit=%d", code)
		}
		assertDirEmpty(t, dir)
	}
}

func TestConfigSetGet(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "machine_label", "lab-1")
	if exit != 0 {
		t.Fatalf("config set exit = %d, stdout %q", exit, stdout)
	}
	if stdout != "{\"key\":\"machine_label\",\"value\":\"lab-1\"}\n" {
		t.Errorf("config set = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "machine_label")
	if exit != 0 || stdout != "{\"key\":\"machine_label\",\"value\":\"lab-1\"}\n" {
		t.Errorf("config get after set = %q (exit %d)", stdout, exit)
	}
	// Other keys keep their defaults without materializing absent records.
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	want := "machine_label=lab-1\n"
	if string(data) != want {
		t.Errorf("config file = %q, want %q", data, want)
	}
}

// TestConfigList: the list shows the effective (resolved) value of
// herdr_soho_bin and the machine label; the PATH is controlled so the
// resolution is deterministic: a soho executable in a known directory
// resolves to its path.
func TestConfigList(t *testing.T) {
	sohoDir := sohoPathDir(t)
	t.Setenv("PATH", sohoDir)
	dir := t.TempDir()
	if _, _, exit := runCLI(t, dir, false, "config", "set", "machine_label", "m1"); exit != 0 {
		t.Fatal("setup: config set machine_label")
	}
	stdout, _, exit := runCLI(t, dir, false, "config", "list")
	if exit != 0 {
		t.Fatalf("config list exit = %d", exit)
	}
	want := `{"config":{"machine_label":"m1","herdr_soho_bin":"` + jsonText(filepath.Join(sohoDir, exeName("soho"))) + `"}}
`
	if stdout != want {
		t.Errorf("config list = %q, want %q", stdout, want)
	}
}

// exeName is the file name of an executable on the running platform:
// on Windows the PATH lookup only finds names with a PATHEXT extension.
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// jsonText is s as it appears inside a JSON string (a Windows path has
// its backslashes escaped).
func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// sohoPathDir builds a PATH directory that holds exactly a soho
// executable (a placeholder body is enough: resolution only looks the
// name up).
func sohoPathDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, exeName("soho")), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestConfigGetSohoBinResolved: `config get` and `config list` show the
// effective (resolved) value of herdr_soho_bin, and `config set
// herdr_soho_bin ""` is the auto form: the file stores the empty value
// and the get shows the resolved one.
func TestConfigGetSohoBinResolved(t *testing.T) {
	sohoDir := sohoPathDir(t)
	t.Setenv("PATH", sohoDir)
	dir := t.TempDir()
	// A fresh config: get shows the resolved soho on the PATH.
	stdout, _, exit := runCLI(t, dir, false, "config", "get", "herdr_soho_bin")
	if exit != 0 || stdout != "{\"key\":\"herdr_soho_bin\",\"value\":\""+jsonText(filepath.Join(sohoDir, exeName("soho")))+"\"}\n" {
		t.Errorf("config get (fresh) = %q (exit %d), want the resolved soho", stdout, exit)
	}
	// Setting the empty value is auto: the file stores the empty form
	// and the get still shows the resolved soho.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "herdr_soho_bin", "")
	if exit != 0 {
		t.Fatalf("config set herdr_soho_bin empty exit = %d, stdout %q", exit, stdout)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "herdr_soho_bin=\n") {
		t.Errorf("the file must store the empty auto form, got %q", data)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "herdr_soho_bin")
	if exit != 0 || stdout != "{\"key\":\"herdr_soho_bin\",\"value\":\""+jsonText(filepath.Join(sohoDir, exeName("soho")))+"\"}\n" {
		t.Errorf("config get after auto set = %q (exit %d), want the resolved soho", stdout, exit)
	}
	// An explicit selection is stored and shown unchanged.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "herdr_soho_bin", "/opt/soho")
	if exit != 0 {
		t.Fatalf("config set explicit exit = %d, stdout %q", exit, stdout)
	}
	data, err = os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "herdr_soho_bin=/opt/soho\n") {
		t.Errorf("the explicit value must be stored unchanged, got %q", data)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "herdr_soho_bin")
	if exit != 0 || stdout != "{\"key\":\"herdr_soho_bin\",\"value\":\"/opt/soho\"}\n" {
		t.Errorf("config get after explicit set = %q (exit %d)", stdout, exit)
	}
}

func TestConfigUnknownKey(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "not_a_key", "x")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, "not_a_key") {
		t.Errorf("set unknown: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "not_a_key")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
		t.Errorf("get unknown: exit %d, stdout %q", exit, stdout)
	}
	// A removed key already in the file is ignored on read: the kept key
	// still reads, while the removed key itself is refused at get and set.
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("machine_label=lab\ndispatcher_url=http://x.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "machine_label")
	if exit != 0 || stdout != "{\"key\":\"machine_label\",\"value\":\"lab\"}\n" {
		t.Errorf("get with a removed key in the file: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "dispatcher_url")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, "dispatcher_url") {
		t.Errorf("get of a removed key: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "dispatcher_url", "http://y.invalid")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, "dispatcher_url") {
		t.Errorf("set of a removed key: exit %d, stdout %q", exit, stdout)
	}
}

func TestConfigCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	content := "# machine notes\n\nmachine_label=lab-9\n   # indented comment\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, exit := runCLI(t, dir, false, "config", "get", "machine_label")
	if exit != 0 || stdout != "{\"key\":\"machine_label\",\"value\":\"lab-9\"}\n" {
		t.Errorf("config get with comments = %q (exit %d)", stdout, exit)
	}
	// Set changes only the requested record and preserves surrounding text.
	if _, _, exit := runCLI(t, dir, false, "config", "set", "machine_label", "lab-10"); exit != 0 {
		t.Fatal("config set")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(content, "machine_label=lab-9", "machine_label=lab-10", 1); string(data) != want {
		t.Errorf("config = %q, want %q", data, want)
	}
}

func TestConfigUsage(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"get"},
		{"get", "a", "b"},
		{"set", "machine_label"},
		{"set", "machine_label", "x", "y"},
		{"list", "extra"},
	} {
		stdout, stderr, exit := runCLI(t, dir, false, append([]string{"config"}, args...)...)
		if exit != 2 || !strings.Contains(stderr, "commands:") || !strings.Contains(stdout, `"status":"2"`) {
			t.Errorf("config %v: exit %d, stderr %q, stdout %q", args, exit, stderr, stdout)
		}
	}
}
