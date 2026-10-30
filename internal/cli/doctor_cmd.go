package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

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

// cmdDoctor implements `doctor`: a read-only JSON line with the version,
// the config directory, the machine label and the soho backend block. It
// works under NAU_NOWRITE=1 and creates no file; the version probe is a
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
		Version      string     `json:"version"`
		ConfigDir    string     `json:"config_dir"`
		MachineLabel string     `json:"machine_label"`
		HerdrSoho    doctorSoho `json:"herdr_soho"`
	}{
		Version:      currentVersion(),
		ConfigDir:    env.ConfigDir,
		MachineLabel: cfg.MachineLabel,
		HerdrSoho:    so,
	}
	_, _ = fmt.Fprintf(env.Stdout, "%s\n", mustJSON(line))
	return 0
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
