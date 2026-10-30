package cli

import (
	"encoding/json"
	"fmt"
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

func TestConfigList(t *testing.T) {
	// The list shows the effective (resolved) value of herdr_soho_bin;
	// the PATH is controlled so the resolution is deterministic: a soho
	// executable in a known directory resolves to its path.
	sohoDir := sohoPathDir(t)
	t.Setenv("PATH", sohoDir)
	dir := t.TempDir()
	if _, _, exit := runCLI(t, dir, false, "config", "set", "dispatcher_url", "set-value"); exit != 0 {
		t.Fatal("setup: config set dispatcher_url")
	}
	stdout, _, exit := runCLI(t, dir, false, "config", "list")
	if exit != 0 {
		t.Fatalf("config list exit = %d", exit)
	}
	want := `{"config":{"machine_label":"","dispatcher_url":"set-value","herdr_soho_bin":"` + jsonText(filepath.Join(sohoDir, exeName("soho"))) + `","push_timeout_s":15,"herdr_bin":"herdr","route_machines":"","route_disabled":"","route_orchestrator_name":"orchestrator","route_probe_timeout_s":20}}
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

func TestConfigPushTimeout(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "push_timeout_s", "30")
	if exit != 0 {
		t.Fatalf("set 30: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "push_timeout_s")
	if exit != 0 || stdout != "{\"key\":\"push_timeout_s\",\"value\":\"30\"}\n" {
		t.Errorf("get push_timeout_s = %q (exit %d)", stdout, exit)
	}
	for _, bad := range []string{"0", "-5", "abc", "1.5", ""} {
		stdout, _, exit := runCLI(t, dir, false, "config", "set", "push_timeout_s", bad)
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
			t.Errorf("set %q: exit %d, stdout %q; want exit 2", bad, exit, stdout)
		}
	}
	// The valid value survives the rejected writes.
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "push_timeout_s")
	if exit != 0 || stdout != "{\"key\":\"push_timeout_s\",\"value\":\"30\"}\n" {
		t.Errorf("get after bad sets = %q (exit %d)", stdout, exit)
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
	// An unknown key already in the file is also refused at read time.
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("machine_label=lab\nbogus=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "machine_label")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
		t.Errorf("get with unknown key in file: exit %d, stdout %q", exit, stdout)
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

func TestConfigRouteGetDefaults(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		key  string
		want string
	}{
		{"herdr_bin", `{"key":"herdr_bin","value":"herdr"}`},
		{"route_machines", `{"key":"route_machines","value":""}`},
		{"route_disabled", `{"key":"route_disabled","value":""}`},
		{"route_orchestrator_name", `{"key":"route_orchestrator_name","value":"orchestrator"}`},
		{"route_probe_timeout_s", `{"key":"route_probe_timeout_s","value":"20"}`},
	} {
		stdout, _, exit := runCLI(t, dir, false, "config", "get", tc.key)
		if exit != 0 {
			t.Fatalf("config get %s exit = %d, stdout %q", tc.key, exit, stdout)
		}
		if stdout != tc.want+"\n" {
			t.Errorf("config get %s = %q, want %q", tc.key, stdout, tc.want)
		}
	}
}

func TestConfigRouteSetGetNormalization(t *testing.T) {
	dir := t.TempDir()
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", " mac-a , win-a ")
	if exit != 0 {
		t.Fatalf("config set exit = %d, stdout %q", exit, stdout)
	}
	// set prints the stored (normalized) value.
	if stdout != "{\"key\":\"route_machines\",\"value\":\"mac-a,win-a\"}\n" {
		t.Errorf("config set route_machines = %q", stdout)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "route_machines=mac-a,win-a\n") {
		t.Errorf("config file holds the unnormalized value:\n%s", data)
	}
	// get prints the effective (normalized) value.
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_machines")
	if exit != 0 || stdout != "{\"key\":\"route_machines\",\"value\":\"mac-a,win-a\"}\n" {
		t.Errorf("config get route_machines = %q (exit %d)", stdout, exit)
	}
	// route_disabled normalizes the same way.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "route_disabled", " win-a , mac-a ")
	if exit != 0 {
		t.Fatalf("set route_disabled: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_disabled")
	if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"win-a,mac-a\"}\n" {
		t.Errorf("config get route_disabled = %q (exit %d)", stdout, exit)
	}
	// herdr_bin is stored trimmed.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "herdr_bin", "  herdr-x  ")
	if exit != 0 {
		t.Fatalf("set herdr_bin: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "herdr_bin")
	if exit != 0 || stdout != "{\"key\":\"herdr_bin\",\"value\":\"herdr-x\"}\n" {
		t.Errorf("config get herdr_bin = %q (exit %d)", stdout, exit)
	}
	// The empty value is valid and stored empty.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "route_machines", "")
	if exit != 0 {
		t.Fatalf("set empty route_machines: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_machines")
	if exit != 0 || stdout != "{\"key\":\"route_machines\",\"value\":\"\"}\n" {
		t.Errorf("config get empty route_machines = %q (exit %d)", stdout, exit)
	}
	// route_orchestrator_name round trip.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "route_orchestrator_name", "orch-2")
	if exit != 0 {
		t.Fatalf("set route_orchestrator_name: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_orchestrator_name")
	if exit != 0 || stdout != "{\"key\":\"route_orchestrator_name\",\"value\":\"orch-2\"}\n" {
		t.Errorf("config get route_orchestrator_name = %q (exit %d)", stdout, exit)
	}
}

func TestConfigRouteSetInvalid(t *testing.T) {
	dir := t.TempDir()
	// A refused set in a fresh dir creates no file.
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "a,,b")
	if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
		t.Fatalf("set a,,b: exit %d, stdout %q; want exit 2", exit, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "config")); !os.IsNotExist(err) {
		t.Fatalf("refused set created the config file: %v", err)
	}
	// A valid set first, so each refusal must leave the file unchanged.
	if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "mac-a"); exit != 0 {
		t.Fatal("setup: config set route_machines mac-a")
	}
	before, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	var many []string
	for i := 0; i < 33; i++ {
		many = append(many, fmt.Sprintf("m%02d", i))
	}
	invalid := []struct {
		key string
		val string
	}{
		{"herdr_bin", ""},
		{"herdr_bin", "   "},
		{"route_machines", "-x"},
		{"route_machines", "a b"},
		{"route_machines", "a,,b"},
		{"route_machines", "a,"},
		{"route_machines", ",a"},
		{"route_machines", "mac-a,mac-a"},
		{"route_machines", "a/b"},
		{"route_machines", strings.Join(many, ",")}, // 33 items
		{"route_disabled", "-x"},
		{"route_disabled", "win-a,win-a"},
		{"route_orchestrator_name", ""},
		{"route_orchestrator_name", "Orchestrator"},
		{"route_orchestrator_name", "-x"},
		{"route_orchestrator_name", "9name"},
		{"route_orchestrator_name", strings.Repeat("a", 33)},
		{"route_probe_timeout_s", "0"},
		{"route_probe_timeout_s", "121"},
		{"route_probe_timeout_s", "x"},
	}
	for _, tc := range invalid {
		stdout, _, exit := runCLI(t, dir, false, "config", "set", tc.key, tc.val)
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, tc.key) {
			t.Errorf("set %s %q: exit %d, stdout %q; want exit 2 naming the key", tc.key, tc.val, exit, stdout)
		}
		after, err := os.ReadFile(filepath.Join(dir, "config"))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Errorf("set %s %q changed the file:\n%s", tc.key, tc.val, after)
		}
	}
}

func TestConfigRouteProbeTimeoutBounds(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"0", "121", "x"} {
		stdout, _, exit := runCLI(t, dir, false, "config", "set", "route_probe_timeout_s", bad)
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
			t.Errorf("set %q: exit %d, stdout %q; want exit 2", bad, exit, stdout)
		}
	}
	// 1 is the lower bound and valid.
	stdout, _, exit := runCLI(t, dir, false, "config", "set", "route_probe_timeout_s", "1")
	if exit != 0 {
		t.Fatalf("set 1: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_probe_timeout_s")
	if exit != 0 || stdout != "{\"key\":\"route_probe_timeout_s\",\"value\":\"1\"}\n" {
		t.Errorf("get after set 1 = %q (exit %d)", stdout, exit)
	}
	// 120 is the upper bound and valid.
	stdout, _, exit = runCLI(t, dir, false, "config", "set", "route_probe_timeout_s", "120")
	if exit != 0 {
		t.Fatalf("set 120: exit %d, stdout %q", exit, stdout)
	}
	stdout, _, exit = runCLI(t, dir, false, "config", "get", "route_probe_timeout_s")
	if exit != 0 || stdout != "{\"key\":\"route_probe_timeout_s\",\"value\":\"120\"}\n" {
		t.Errorf("get after set 120 = %q (exit %d)", stdout, exit)
	}
}

func TestConfigListInvalidRouterValue(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		line string
		key  string
	}{
		{"route_machines=mac-a,mac-a\n", "route_machines"},
		{"route_machines=-x\n", "route_machines"},
		{"route_machines=a,,b\n", "route_machines"},
		{"route_disabled=win-a,win-a\n", "route_disabled"},
		{"route_disabled=a,\n", "route_disabled"},
		{"route_orchestrator_name=Orchestrator\n", "route_orchestrator_name"},
		{"route_orchestrator_name=9name\n", "route_orchestrator_name"},
		{"route_probe_timeout_s=0\n", "route_probe_timeout_s"},
		{"route_probe_timeout_s=121\n", "route_probe_timeout_s"},
		{"route_probe_timeout_s=abc\n", "route_probe_timeout_s"},
		{"herdr_bin=\n", "herdr_bin"},
	} {
		if err := os.WriteFile(filepath.Join(dir, "config"), []byte(tc.line), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, _, exit := runCLI(t, dir, false, "config", "list")
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) {
			t.Errorf("config list with %q: exit %d, stdout %q; want exit 2", tc.line, exit, stdout)
		}
		if !strings.Contains(stdout, tc.key) {
			t.Errorf("config list with %q: the error does not name the key: %q", tc.line, stdout)
		}
	}
}

// TestConfigSetRouteUnmatchedWarning: a successful set of route_disabled or
// route_machines prints one warning line on stderr (after the stdout line,
// exit 0) when the stored route_disabled labels are not in route_machines;
// no other command or key warns, and a refused set prints no warning.
func TestConfigSetRouteUnmatchedWarning(t *testing.T) {
	warn := "nau: config: warning: route_disabled labels not in route_machines exclude nothing: "

	t.Run("empty fleet staging", func(t *testing.T) {
		dir := t.TempDir()
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "ghost")
		if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"ghost\"}\n" || stderr != "" {
			t.Fatalf("staged exclusion: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		stdout, stderr, exit = runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a")
		if exit != 0 || stdout != "{\"key\":\"route_machines\",\"value\":\"local,win-a\"}\n" {
			t.Fatalf("fleet set: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		if stderr != warn+"ghost\n" {
			t.Errorf("fleet set stderr = %q, want the warning naming ghost", stderr)
		}
	})

	t.Run("matching", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a"); exit != 0 {
			t.Fatalf("setup fleet: exit %d", exit)
		}
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "win-a")
		if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"win-a\"}\n" || stderr != "" {
			t.Fatalf("matching exclusion: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
	})

	t.Run("unmatched", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a"); exit != 0 {
			t.Fatalf("setup fleet: exit %d", exit)
		}
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "ghost-box")
		if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"ghost-box\"}\n" {
			t.Fatalf("unmatched set: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		if stderr != warn+"ghost-box\n" {
			t.Errorf("unmatched set stderr = %q, want the warning naming ghost-box", stderr)
		}
		stdout, stderr, exit = runCLI(t, dir, false, "config", "set", "route_disabled", "")
		if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"\"}\n" || stderr != "" {
			t.Fatalf("empty exclusions: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
	})

	t.Run("changed fleet", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a"); exit != 0 {
			t.Fatalf("setup fleet: exit %d", exit)
		}
		if _, stderr, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "win-a"); exit != 0 || stderr != "" {
			t.Fatalf("exclusion before the fleet change: exit %d, stderr %q", exit, stderr)
		}
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local")
		if exit != 0 || stdout != "{\"key\":\"route_machines\",\"value\":\"local\"}\n" {
			t.Fatalf("shrunken fleet: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		if stderr != warn+"win-a\n" {
			t.Errorf("shrunken fleet stderr = %q, want the warning naming win-a", stderr)
		}
		stdout, stderr, exit = runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a")
		if exit != 0 || stdout != "{\"key\":\"route_machines\",\"value\":\"local,win-a\"}\n" || stderr != "" {
			t.Fatalf("restored fleet: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
	})

	t.Run("other keys never warn", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a"); exit != 0 {
			t.Fatalf("setup fleet: exit %d", exit)
		}
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "ghost"); exit != 0 {
			t.Fatalf("setup exclusion: exit %d", exit)
		}
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_probe_timeout_s", "5")
		if exit != 0 || stdout != "{\"key\":\"route_probe_timeout_s\",\"value\":\"5\"}\n" || stderr != "" {
			t.Fatalf("timeout set: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		stdout, stderr, exit = runCLI(t, dir, false, "config", "get", "route_disabled")
		if exit != 0 || stdout != "{\"key\":\"route_disabled\",\"value\":\"ghost\"}\n" || stderr != "" {
			t.Fatalf("get: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		stdout, stderr, exit = runCLI(t, dir, false, "config", "list")
		if exit != 0 || stderr != "" || !strings.Contains(stdout, `"route_disabled":"ghost"`) {
			t.Fatalf("list: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
	})

	t.Run("invalid value warns nothing", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, exit := runCLI(t, dir, false, "config", "set", "route_machines", "local,win-a"); exit != 0 {
			t.Fatalf("setup fleet: exit %d", exit)
		}
		stdout, stderr, exit := runCLI(t, dir, false, "config", "set", "route_disabled", "a b")
		if exit != 2 || !strings.Contains(stdout, `"status":"2"`) || !strings.Contains(stdout, "route_disabled") {
			t.Fatalf("invalid set: exit %d, stdout %q, stderr %q", exit, stdout, stderr)
		}
		if strings.Contains(stderr, "warning:") {
			t.Errorf("invalid set stderr carries the warning line: %q", stderr)
		}
	})
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
