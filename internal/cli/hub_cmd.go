package cli

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"runtime/debug"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/sqld"
	"github.com/djalmajr/nau/internal/hub/store"
)

const (
	hubDefaultListen   = "127.0.0.1:7440"
	hubStartupTimeout  = 10 * time.Second
	hubShutdownTimeout = 10 * time.Second
	hubEnvDBURL        = "NAU_HUB_DB_URL"
	hubEnvDBToken      = "NAU_HUB_DB_TOKEN"
	hubUsage           = "usage: hub serve [--listen <host:port>] [--web <dir>]"
)

// newHubStore builds the hub store over the sqld URL; the tests replace
// it with an in-memory fake.
var newHubStore = func(baseURL, token string) (store.Store, error) {
	return sqld.New(baseURL, token)
}

// hubServeListen opens the listener socket; the tests replace it to
// capture the listener.
var hubServeListen = net.Listen

// hubFlags is the parsed form of the hub serve arguments: each flag in
// the space form, at most once.
type hubFlags struct {
	listen    string
	listenSet bool
	web       string
	webSet    bool
}

// parseHubFlags accepts only the flags, in the space form, each at most
// once, followed by a value; anything else is malformed.
func parseHubFlags(args []string) (hubFlags, bool) {
	var f hubFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--listen":
			if f.listenSet || i+1 >= len(args) {
				return f, false
			}
			f.listen, f.listenSet = args[i+1], true
			i++
		case "--web":
			if f.webSet || i+1 >= len(args) {
				return f, false
			}
			f.web, f.webSet = args[i+1], true
			i++
		default:
			return f, false
		}
	}
	return f, true
}

// cmdHub runs the hub commands; serve is the hub server.
func cmdHub(args []string, env Env) int {
	if len(args) == 0 || args[0] != "serve" {
		badUsage(env, hubUsage)
		return 2
	}
	return cmdHubServe(args[1:], env)
}

// cmdHubServe runs the hub server: it applies the migrations first,
// then serves the API and the static files until a signal. It prints
// one JSON line once it listens; a bad usage exits 2, an unreachable
// database or a failed migration exits 4 with one stderr line (never
// the token), and a clean shutdown exits 0.
func cmdHubServe(args []string, env Env) int {
	f, ok := parseHubFlags(args)
	if !ok {
		badUsage(env, hubUsage)
		return 2
	}
	listen := f.listen
	if !f.listenSet {
		listen = hubDefaultListen
	}
	var static fs.FS
	if f.webSet {
		st, err := os.Stat(f.web)
		if err != nil || !st.IsDir() {
			fail(env, 2, "hub serve: --web is not a directory")
			return 2
		}
		static = os.DirFS(f.web)
	}
	dbURL := env.Getenv(hubEnvDBURL)
	if dbURL == "" {
		fail(env, 2, "hub serve: "+hubEnvDBURL+" is required")
		return 2
	}
	if u, err := url.Parse(dbURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail(env, 2, "hub serve: "+hubEnvDBURL+" must be an http:// or https:// URL")
		return 2
	}
	token := env.Getenv(hubEnvDBToken)
	st, err := newHubStore(dbURL, token)
	if err != nil {
		fail(env, 2, "hub serve: "+err.Error())
		return 2
	}
	// The migrations run at startup, bounded; a failure exits 4 with a
	// stderr line. The errors of the store never carry the token.
	migCtx, migCancel := context.WithTimeout(context.Background(), hubStartupTimeout)
	if err := (&migrate.Runner{Store: st, Now: env.Now}).Apply(migCtx); err != nil {
		migCancel()
		fail(env, 4, "hub serve: migrations: "+err.Error())
		return 4
	}
	migCancel()
	ln, err := hubServeListen("tcp", listen)
	if err != nil {
		fail(env, 4, "hub serve: cannot listen on "+listen)
		return 4
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); !ok || ta.Port <= 0 {
		_ = ln.Close()
		fail(env, 4, "hub serve: cannot listen on "+listen)
		return 4
	}
	// The version is the same value the `nau version` command prints:
	// keep this resolution in sync with cmdVersion.
	v := version
	if v == "" {
		v = "dev"
	}
	if v == "dev" {
		v = devVersion(debug.ReadBuildInfo())
	}
	srv := httpserve.NewServer(httpserve.New(hub.New(hub.Deps{
		Store:   st,
		Now:     env.Now,
		Version: v,
	}, static)))
	_, _ = fmt.Fprintf(env.Stdout, `{"status":"serving","listen":%q}`+"\n", ln.Addr().String())

	ctx, cancel := serveContext()
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errCh:
		// The listener failed before any shutdown: the serving line is
		// already on stdout, so only stderr says why.
		_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: %v\n", env.program(), err)
		code = 4
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), hubShutdownTimeout)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: shutdown: %v\n", env.program(), err)
	}
	shutdownCancel()
	return code
}
