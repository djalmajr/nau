package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/agent"
	"github.com/djalmajr/nau/internal/config"
	"github.com/djalmajr/nau/internal/soho"
)

// doctorSoho is the herdr_soho block of the doctor line: the resolved
// backend executable and the version it reports.
type doctorSoho struct {
	Found   bool   `json:"found"`
	Bin     string `json:"bin"`
	Version string `json:"version"`
}

// doctorAgentHealth is the health sub-block of the doctor's agent
// block: the saved health without the hub field.
type doctorAgentHealth struct {
	State         string `json:"state"`
	LastOKMS      int64  `json:"last_ok_ms"`
	LastAttemptMS int64  `json:"last_attempt_ms"`
	LastError     string `json:"last_error"`
}

// doctorAgent is the agent block of the doctor line: the connection
// from hub.json and the saved health. It never carries the key, and
// the hub is reduced to its scheme and host.
type doctorAgent struct {
	Connected bool               `json:"connected"`
	Hub       *string            `json:"hub"`
	MachineID *string            `json:"machine_id"`
	Health    *doctorAgentHealth `json:"health"`
}

// doctorAgentBlock reads hub.json and the saved health from the config
// directory. connected is true when hub.json is valid; the health
// block is present whenever a health file has been written, whatever
// the connection says.

// cmdDoctor implements `doctor`: a read-only JSON line with the version,
// the config directory, the machine label, the soho backend block and the
// agent block (the hub connection and the saved agent health). It works
// under NAU_NOWRITE=1 and creates no file; the version probe is a
// bounded `--version` run of the resolved backend, and a missing or
// non-answering backend reports found=false with an empty version rather
// than a failure. It exits 0 whatever the backend reports, and 2 on bad
// usage or an unreadable configuration.
func cmdDoctor(args []string, env Env) int {
	if len(args) != 0 {
		badUsage(env, "usage: doctor")
		return 2
	}
	cfg, err := config.Load(env.ConfigDir)
	if err != nil {
		fail(env, 2, "config: "+err.Error())
		return 2
	}
	so := doctorSoho{Bin: cfg.HerdrSohoBin}
	resolved, lerr := exec.LookPath(cfg.HerdrSohoBin)
	if lerr == nil {
		so.Found = true
		so.Bin = resolved
		so.Version = sohoVersion(env, resolved)
	}
	line := struct {
		Version      string      `json:"version"`
		ConfigDir    string      `json:"config_dir"`
		MachineLabel string      `json:"machine_label"`
		HerdrSoho    doctorSoho  `json:"herdr_soho"`
		Agent        doctorAgent `json:"agent"`
	}{
		Version:      currentVersion(),
		ConfigDir:    env.ConfigDir,
		MachineLabel: cfg.MachineLabel,
		HerdrSoho:    so,
		Agent:        doctorAgentBlock(env.ConfigDir),
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(line))
	return 0
}

// doctorAgentBlock builds the agent block from the config directory:

// doctorAgentBlock builds the agent block from the config directory:
// the hub connection from hub.json and the last saved health. The key
// never reaches the line.
func doctorAgentBlock(cfgDir string) doctorAgent {
	block := doctorAgent{}
	if hubCfg, err := agent.LoadHubFile(filepath.Join(cfgDir, "hub.json")); err == nil {
		hub := agent.HostOnly(hubCfg.HubURL)
		machineID := hubCfg.MachineID
		block.Connected = true
		block.Hub = &hub
		block.MachineID = &machineID
	}
	if h, err := agent.ReadHealth(filepath.Join(cfgDir, "agent-state.json")); err == nil {
		health := doctorAgentHealth{
			State:         h.State,
			LastOKMS:      h.LastOKMS,
			LastAttemptMS: h.LastAttemptMS,
			LastError:     h.LastError,
		}
		block.Health = &health
	}
	return block
}

// currentVersion returns the version string the version command prints.
func currentVersion() string {
	v := version
	if v == "" {
		v = "dev"
	}
	if v == "dev" {
		info, ok := debug.ReadBuildInfo()
		v = devVersion(info, ok)
	}
	return v
}

// sohoVersion runs `--version` on the resolved backend (bounded) and
// reports the trimmed last non-empty line of its stdout; a failed run, a
// non-zero exit or empty output reports an empty version.
func sohoVersion(env Env, bin string) string {
	runner := soho.Runner{Bin: bin, Environ: childEnviron(env)}
	var out bytes.Buffer
	exit, err := runner.Run(context.Background(), []string{"--version"}, nil, &out, io.Discard, 0)
	if err != nil || exit != 0 {
		return ""
	}
	select {
	case <-runner.DrainDone():
	case <-time.After(10 * time.Second):
		return ""
	}
	lines := strings.Split(out.String(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// childEnviron returns the exact environment forwarded to subprocesses.
func childEnviron(env Env) []string {
	if env.Environ == nil {
		return os.Environ()
	}
	return env.Environ()
}
