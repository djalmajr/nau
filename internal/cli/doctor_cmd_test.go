package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/config"
	"github.com/djalmajr/nau/internal/testutil/fakesoho"
)

// fakeChildEnv is the controlled environment every doctor test passes to
// the subprocesses (also asserted on in the fake call log).
var fakeChildEnv = []string{"PATH=/bin"}

// installFakeSoho installs the fake soho and returns (exePath, dir).
func installFakeSoho(t *testing.T, rules ...fakesoho.Rule) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return fakesoho.Install(t, dir, rules...), dir
}

// setSohoConfig writes the config pointing herdr_soho_bin at the fake,
// with the given machine label ("" for none).
func setSohoConfig(t *testing.T, cfgDir, exe, label string) {
	t.Helper()
	if err := config.Set(cfgDir, "herdr_soho_bin", exe); err != nil {
		t.Fatalf("config.Set herdr_soho_bin: %v", err)
	}
	if label != "" {
		if err := config.Set(cfgDir, "machine_label", label); err != nil {
			t.Fatalf("config.Set machine_label: %v", err)
		}
	}
}

// runDoctor runs doctor with the test env (controlled child environment)
// and returns stdout, stderr and the exit code.
func runDoctor(t *testing.T, cfgDir string, nowrite bool) (string, string, int) {
	t.Helper()
	var out, errb strings.Builder
	env := Env{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    getenvFor(nowrite),
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: cfgDir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run([]string{"doctor"}, env)
	return out.String(), errb.String(), exit
}

// doctorLine is the doctor JSON line.
type doctorLine struct {
	Version      string `json:"version"`
	ConfigDir    string `json:"config_dir"`
	MachineLabel string `json:"machine_label"`
	HerdrSoho    struct {
		Found   bool   `json:"found"`
		Bin     string `json:"bin"`
		Version string `json:"version"`
	} `json:"herdr_soho"`
	Agent struct {
		Connected bool    `json:"connected"`
		Hub       *string `json:"hub"`
		MachineID *string `json:"machine_id"`
		Health    *struct {
			State         string `json:"state"`
			LastOKMS      int64  `json:"last_ok_ms"`
			LastAttemptMS int64  `json:"last_attempt_ms"`
			LastError     string `json:"last_error"`
		} `json:"health"`
	} `json:"agent"`
}

// TestDoctorFound: a found backend that answers --version, with a set
// machine label, reports the version, the config directory, the label and
// the resolved backend block.
func TestDoctorFound(t *testing.T) {
	exe, _ := installFakeSoho(t,
		fakesoho.Rule{Argv: []string{"--version"}, Stdout: "soho 0.3.0\n", Code: 0},
	)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "machine-a")
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if line.Version != currentVersion() {
		t.Errorf("version = %q, want the version command value %q", line.Version, currentVersion())
	}
	if line.ConfigDir != cfgDir {
		t.Errorf("config_dir = %q, want %q", line.ConfigDir, cfgDir)
	}
	if line.MachineLabel != "machine-a" {
		t.Errorf("machine_label = %q, want machine-a", line.MachineLabel)
	}
	if !line.HerdrSoho.Found {
		t.Errorf("herdr_soho.found = false, want true: %q", stdout)
	}
	if line.HerdrSoho.Bin != exe {
		t.Errorf("herdr_soho.bin = %q, want the resolved path %q", line.HerdrSoho.Bin, exe)
	}
	if line.HerdrSoho.Version != "soho 0.3.0" {
		t.Errorf("herdr_soho.version = %q, want soho 0.3.0", line.HerdrSoho.Version)
	}
	if stderr != "" {
		t.Errorf("doctor stderr = %q, want empty", stderr)
	}
}

// TestDoctorVersionMatchesVersionCommand: the doctor version field is the
// same string the version command prints.
func TestDoctorVersionMatchesVersionCommand(t *testing.T) {
	exe, _ := installFakeSoho(t,
		fakesoho.Rule{Argv: []string{"--version"}, Stdout: "soho 0.3.0\n", Code: 0},
	)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "")
	dout, _, dexit := runDoctor(t, cfgDir, false)
	var dline doctorLine
	if dexit != 0 || json.Unmarshal([]byte(dout), &dline) != nil {
		t.Fatalf("doctor: exit %d, stdout %q", dexit, dout)
	}
	vout, _, vexit := runCLI(t, cfgDir, false, "version")
	if vexit != 0 {
		t.Fatalf("version exit = %d, stdout %q", vexit, vout)
	}
	var vline struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(vout)), &vline); err != nil {
		t.Fatalf("version stdout %q is not a JSON line: %v", vout, err)
	}
	if dline.Version != vline.Version {
		t.Errorf("doctor version = %q, version command = %q", dline.Version, vline.Version)
	}
}

// TestDoctorMissingBackend: with an empty PATH the backend is not found:
// found=false, the bare configured name, an empty version, exit 0.
func TestDoctorMissingBackend(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if line.HerdrSoho.Found {
		t.Errorf("herdr_soho.found = true, want false: %q", stdout)
	}
	if line.HerdrSoho.Bin != "soho" {
		t.Errorf("herdr_soho.bin = %q, want the bare configured name soho", line.HerdrSoho.Bin)
	}
	if line.HerdrSoho.Version != "" {
		t.Errorf("herdr_soho.version = %q, want empty for a missing backend", line.HerdrSoho.Version)
	}
	if line.MachineLabel != "" {
		t.Errorf("machine_label = %q, want empty", line.MachineLabel)
	}
}

// TestDoctorVersionProbeFails: a found backend that refuses or fails the
// --version probe reports the empty version, not a failure.
func TestDoctorVersionProbeFails(t *testing.T) {
	cases := []struct {
		name string
		rule fakesoho.Rule
	}{
		{"refusal", fakesoho.Rule{Argv: []string{"--version"}, Code: 2}},
		{"empty output", fakesoho.Rule{Argv: []string{"--version"}, Stdout: "\n", Code: 0}},
		{"no rule", fakesoho.Rule{Argv: []string{"--version"}, Stdout: "", Code: 127}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, _ := installFakeSoho(t, tc.rule)
			cfgDir := t.TempDir()
			setSohoConfig(t, cfgDir, exe, "")
			stdout, stderr, exit := runDoctor(t, cfgDir, false)
			if exit != 0 {
				t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
			}
			var line doctorLine
			if err := json.Unmarshal([]byte(stdout), &line); err != nil {
				t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
			}
			if !line.HerdrSoho.Found || line.HerdrSoho.Bin != exe {
				t.Errorf("herdr_soho = %+v, want found with the resolved path", line.HerdrSoho)
			}
			if line.HerdrSoho.Version != "" {
				t.Errorf("herdr_soho.version = %q, want empty", line.HerdrSoho.Version)
			}
		})
	}
}

// TestDoctorIgnoresRemovedConfigKeys: an old config file with removed keys
// still reads, and the doctor reports the kept keys.
func TestDoctorIgnoresRemovedConfigKeys(t *testing.T) {
	exe, _ := installFakeSoho(t,
		fakesoho.Rule{Argv: []string{"--version"}, Stdout: "soho 0.3.0\n", Code: 0},
	)
	cfgDir := t.TempDir()
	data := "machine_label=m1\ndispatcher_url=http://x.invalid\npush_timeout_s=19\nroute_machines=mac-a\nherdr_soho_bin=" + exe + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor over an old config: exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if line.MachineLabel != "m1" || !line.HerdrSoho.Found {
		t.Errorf("doctor over an old config: label %q found %v: %q", line.MachineLabel, line.HerdrSoho.Found, stdout)
	}
}

// TestDoctorNowriteReadonly: under NAU_NOWRITE=1 the doctor runs and
// creates no file (the version probe is a read-only child).
func TestDoctorNowriteReadonly(t *testing.T) {
	exe, _ := installFakeSoho(t,
		fakesoho.Rule{Argv: []string{"--version"}, Stdout: "soho 0.3.0\n", Code: 0},
	)
	cfgDir := t.TempDir()
	setSohoConfig(t, cfgDir, exe, "machine-a")
	before := dirEntries(t, cfgDir)
	stdout, stderr, exit := runDoctor(t, cfgDir, true)
	if exit != 0 {
		t.Fatalf("doctor under NAU_NOWRITE: exit = %d, stderr %q", exit, stderr)
	}
	if !strings.Contains(stdout, `"machine_label":"machine-a"`) {
		t.Errorf("doctor under NAU_NOWRITE stdout = %q", stdout)
	}
	if after := dirEntries(t, cfgDir); after != before {
		t.Errorf("doctor under NAU_NOWRITE wrote files: before %q after %q", before, after)
	}
}

// TestDoctorBadUsage: an argument refuses with exit 2 and the usage on
// stderr.
func TestDoctorBadUsage(t *testing.T) {
	cfgDir := t.TempDir()
	var out, errb strings.Builder
	env := Env{
		Stdin:     strings.NewReader(""),
		Stdout:    &out,
		Stderr:    &errb,
		Getenv:    getenvFor(false),
		Environ:   func() []string { return fakeChildEnv },
		ConfigDir: cfgDir,
		Now:       func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		Sleep:     sleepCtx,
	}
	exit := Run([]string{"doctor", "--bogus"}, env)
	if exit != 2 {
		t.Fatalf("doctor --bogus exit = %d, want 2", exit)
	}
	if !strings.Contains(errb.String(), "usage: nau <command> [args]") {
		t.Errorf("stderr %q lacks the usage text", firstNLines(errb.String(), 3))
	}
	if !strings.Contains(out.String(), `"usage: doctor"`) {
		t.Errorf("stdout %q lacks the doctor usage motivo", out.String())
	}
	if !strings.Contains(out.String(), `"status":"2"`) {
		t.Errorf("stdout %q lacks the exit-2 line", out.String())
	}
}

// TestDoctorAgentNotConnected: without hub.json and without a health
// file the agent block is disconnected with nulls.
func TestDoctorAgentNotConnected(t *testing.T) {
	cfgDir := t.TempDir()
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	if !strings.Contains(stdout, `"agent":{"connected":false,"hub":null,"machine_id":null,"health":null}`) {
		t.Errorf("doctor stdout lacks the disconnected agent block:\n%s", stdout)
	}
}

// TestDoctorAgentConnectedWithHealth: a valid hub.json and a saved
// health file give the connected agent block with the scheme-and-host
// hub and the four health fields, and the key never reaches the line.
func TestDoctorAgentConnectedWithHealth(t *testing.T) {
	const hubKey = "sekret-doctor-agent-key"
	cfgDir := t.TempDir()
	hub := `{"url":"http://127.0.0.1:17440","machine_id":"machine-m1","key":"` + hubKey + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "hub.json"), []byte(hub), 0o600); err != nil {
		t.Fatal(err)
	}
	health := `{"state":"ok","last_ok_ms":1700000000000,"last_attempt_ms":1700000000010,"last_error":"","hub":"http://127.0.0.1:17440"}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "agent-state.json"), []byte(health), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if !line.Agent.Connected {
		t.Errorf("agent.connected = false, want true: %q", stdout)
	}
	if line.Agent.Hub == nil || *line.Agent.Hub != "http://127.0.0.1:17440" {
		t.Errorf("agent.hub = %v, want the scheme and host", line.Agent.Hub)
	}
	if line.Agent.MachineID == nil || *line.Agent.MachineID != "machine-m1" {
		t.Errorf("agent.machine_id = %v, want machine-m1", line.Agent.MachineID)
	}
	if line.Agent.Health == nil {
		t.Fatalf("agent.health = null, want the saved health: %q", stdout)
	}
	if line.Agent.Health.State != "ok" || line.Agent.Health.LastOKMS != 1700000000000 ||
		line.Agent.Health.LastAttemptMS != 1700000000010 || line.Agent.Health.LastError != "" {
		t.Errorf("agent.health = %+v, want the saved health", *line.Agent.Health)
	}
	if strings.Contains(stdout, hubKey) {
		t.Fatalf("doctor stdout carries the machine key:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("doctor stderr = %q, want empty", stderr)
	}
}

// TestDoctorAgentHealthWithoutConnection: a saved health file without
// a valid hub.json still reports the health, disconnected.
func TestDoctorAgentHealthWithoutConnection(t *testing.T) {
	cfgDir := t.TempDir()
	health := `{"state":"hub_unreachable","last_ok_ms":0,"last_attempt_ms":1700000000020,"last_error":"hub_unreachable","hub":"https://hub.example"}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "agent-state.json"), []byte(health), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if line.Agent.Connected || line.Agent.Hub != nil || line.Agent.MachineID != nil {
		t.Errorf("agent = %+v, want disconnected with nulls", line.Agent)
	}
	if line.Agent.Health == nil || line.Agent.Health.State != "hub_unreachable" ||
		line.Agent.Health.LastAttemptMS != 1700000000020 || line.Agent.Health.LastError != "hub_unreachable" {
		t.Errorf("agent.health = %+v, want the saved health", line.Agent.Health)
	}
}

// TestDoctorAgentInvalidHubFile: a hub.json with an empty field is not
// a connection.
func TestDoctorAgentInvalidHubFile(t *testing.T) {
	cfgDir := t.TempDir()
	hub := `{"url":"http://127.0.0.1:17440","machine_id":"machine-m1","key":""}` + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "hub.json"), []byte(hub), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runDoctor(t, cfgDir, false)
	if exit != 0 {
		t.Fatalf("doctor exit = %d, stderr %q", exit, stderr)
	}
	var line doctorLine
	if err := json.Unmarshal([]byte(stdout), &line); err != nil {
		t.Fatalf("doctor stdout %q is not a JSON line: %v", stdout, err)
	}
	if line.Agent.Connected || line.Agent.Hub != nil || line.Agent.MachineID != nil {
		t.Errorf("agent = %+v, want disconnected for an invalid hub file", line.Agent)
	}
}
