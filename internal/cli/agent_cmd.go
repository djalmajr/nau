package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/djalmajr/nau/internal/agent"
	"github.com/djalmajr/nau/internal/config"
	"github.com/djalmajr/nau/internal/soho"
)

const (
	agentUsage     = "usage: agent run [--interval <duration>] [--hc-bin <bin>]"
	agentHubFile   = "hub.json"
	agentStateFile = "agent-state.json"
)

// agentRunContext is the signal-bound context of the agent run (SIGINT
// and SIGTERM); the tests replace it with a context they cancel.
var agentRunContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// agentFlags is the parsed form of the agent run arguments: each flag
// in the space form, at most once, followed by a value; anything else
// is malformed.
type agentFlags struct {
	interval    time.Duration
	intervalSet bool
	hcBin       string
	hcBinSet    bool
}

// parseAgentFlags accepts only the flags, in the space form, each at
// most once, followed by a value; anything else is malformed.
func parseAgentFlags(args []string) (agentFlags, bool) {
	var f agentFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--interval":
			if f.intervalSet || i+1 >= len(args) {
				return f, false
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				return f, false
			}
			f.interval, f.intervalSet = d, true
			i++
		case "--hc-bin":
			if f.hcBinSet || i+1 >= len(args) {
				return f, false
			}
			f.hcBin, f.hcBinSet = args[i+1], true
			i++
		default:
			return f, false
		}
	}
	return f, true
}

// cmdAgent implements `agent`: run is the per-machine agent loop. It
// reads hub.json from the config dir, runs the soho backend the doctor
// resolves, and writes the health file on every change. It prints one
// JSON start line (never the key), nothing else on stdout while it
// runs, exits 0 on a terminal signal, 2 on a bad usage or a missing or
// invalid hub file, and 4 when the hub revokes the machine key.
func cmdAgent(args []string, env Env) int {
	if len(args) == 0 || args[0] != "run" {
		badUsage(env, agentUsage)
		return 2
	}
	f, ok := parseAgentFlags(args[1:])
	if !ok {
		badUsage(env, agentUsage)
		return 2
	}
	interval := agent.DefaultInterval
	if f.intervalSet {
		interval = f.interval
	}
	if interval < agent.MinInterval || interval > agent.MaxInterval {
		badUsage(env, agentUsage)
		return 2
	}
	hubCfg, err := agent.LoadHubFile(filepath.Join(env.ConfigDir, agentHubFile))
	if err != nil {
		fail(env, 2, "agent run: no hub file; run nau connect <hub-url> first")
		return 2
	}
	cfg, err := config.Load(env.ConfigDir)
	if err != nil {
		fail(env, 2, "agent run: config: "+err.Error())
		return 2
	}
	// The backend is the same the doctor resolves: the effective
	// herdr_soho_bin through the bounded argv runner.
	runner := soho.Runner{Bin: cfg.HerdrSohoBin, Environ: childEnviron(env)}
	// The start line goes out before the loop starts: the key never
	// leaves the Authorization header, and the hub is reduced to its
	// scheme and host.
	_, _ = fmt.Fprintf(env.Stdout, `{"status":"running","hub":%q,"machine_id":%q}`+"\n",
		agent.HostOnly(hubCfg.HubURL), hubCfg.MachineID)
	acfg := agent.Config{
		HubURL:    hubCfg.HubURL,
		MachineID: hubCfg.MachineID,
		Key:       hubCfg.Key,
		Runner:    runner,
		StatePath: filepath.Join(env.ConfigDir, agentStateFile),
		HCBin:     f.hcBin,
		Interval:  interval,
		Now:       env.Now,
		Sleep:     env.Sleep,
	}
	ctx, cancel := agentRunContext()
	defer cancel()
	if err := agent.Run(ctx, acfg); err != nil {
		if errors.Is(err, agent.ErrRevoked) {
			fail(env, 4, "revoked")
			return 4
		}
		if ctx.Err() != nil {
			// A terminal signal ended the run; it exits 0.
			return 0
		}
		// The other Run errors are the health file failing to be
		// written; the loop errors are states, not returns.
		fail(env, 4, "agent run: "+err.Error())
		return 4
	}
	return 0
}
