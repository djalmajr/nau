package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/djalmajr/nau/internal/hub"
	"github.com/djalmajr/nau/internal/hub/accounts"
	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/hub/live"
	"github.com/djalmajr/nau/internal/hub/machines"
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
	hubEnvCronToken    = "NAU_HUB_CRON_TOKEN"
	hubEnvSessionKey   = "NAU_HUB_SESSION_KEY"
	hubEnvBootstrapTok = "NAU_HUB_BOOTSTRAP_TOKEN"
	hubEnvClientIPHdr  = "NAU_HUB_CLIENT_IP_HEADER"
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

// hubServeContext is the signal-bound context of the hub server; the
// tests replace it with a context they cancel.
var hubServeContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	// closeRoot releases the web root; it stays nil without --web. Until
	// the server starts no handler can use the root, so an early return
	// closes it; once serving, only a clean shutdown does.
	var closeRoot func() error
	serving := false
	defer func() {
		if !serving && closeRoot != nil {
			_ = closeRoot()
		}
	}()
	if f.webSet {
		st, err := os.Stat(f.web)
		if err != nil || !st.IsDir() {
			fail(env, 2, "hub serve: --web is not a directory")
			return 2
		}
		// Open the root confined to the directory: a symbolic link
		// that points outside the root is refused by the filesystem,
		// while a link that stays inside keeps being served. The core
		// sees only the fs.FS.
		root, err := os.OpenRoot(f.web)
		if err != nil {
			fail(env, 2, "hub serve: --web is not a directory")
			return 2
		}
		// The root keeps a handle on the directory. It is closed only
		// once no handler can still use it: after a clean shutdown, or
		// on an early return before the server ever started (Windows
		// cannot remove an open directory).
		closeRoot = root.Close
		static = root.FS()
	}
	dbURL := env.Getenv(hubEnvDBURL)
	if dbURL == "" {
		fail(env, 2, "hub serve: "+hubEnvDBURL+" is required")
		return 2
	}
	if u, err := url.Parse(dbURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		fail(env, 2, "hub serve: "+hubEnvDBURL+" must be an http:// or https:// URL")
		return 2
	}
	token := env.Getenv(hubEnvDBToken)
	// The session key signs the session cookies: it must decode to at
	// least 32 bytes. The errors name the variable only, never the
	// value.
	sessionKeyB64 := env.Getenv(hubEnvSessionKey)
	if sessionKeyB64 == "" {
		fail(env, 2, "hub serve: "+hubEnvSessionKey+" is required")
		return 2
	}
	sessionKey, err := base64.StdEncoding.DecodeString(sessionKeyB64)
	if err != nil || len(sessionKey) < 32 {
		fail(env, 2, "hub serve: "+hubEnvSessionKey+" must be base64 of at least 32 bytes")
		return 2
	}
	bootstrapToken := env.Getenv(hubEnvBootstrapTok)
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
	// The accounts are over the same store; the clock and the
	// randomness are the production ones. A bad session key never
	// reaches here: New only fails on a key shorter than 32 bytes.
	auth, err := accounts.New(accounts.Config{
		Store:          accounts.NewSQL(st),
		SessionKey:     sessionKey,
		BootstrapToken: bootstrapToken,
		Now:            env.Now,
	})
	if err != nil {
		fail(env, 2, "hub serve: "+hubEnvSessionKey+" must be base64 of at least 32 bytes")
		return 2
	}
	// The machine connect is over the same store: the sessions it
	// reads are the accounts' sessions, and the inventories apply
	// through the hub store. The client of the creation limit is the
	// ip of the connection; when the hub is behind a proxy that sets
	// the header named by NAU_HUB_CLIENT_IP_HEADER, that header wins
	// (the first value, when it validates as an ip).
	mach, err := machines.New(machines.Config{
		Store:          st,
		Machines:       machines.NewSQL(st),
		SessionUser:    auth.SessionUser,
		ClientIPHeader: env.Getenv(hubEnvClientIPHdr),
		Now:            env.Now,
	})
	if err != nil {
		// The configuration is the fixed one: New cannot fail here.
		fail(env, 2, "hub serve: the machine connect is misconfigured")
		return 2
	}
	// The live board is over the same store: the version of each
	// developer is the hash of what that developer may read, and the
	// two routes are the polling answer and the events stream.
	liveBoard, err := live.New(live.Config{
		Store:       st,
		SessionUser: auth.SessionUser,
		Now:         env.Now,
	})
	if err != nil {
		// The configuration is the fixed one: New cannot fail here.
		fail(env, 2, "hub serve: the live board is misconfigured")
		return 2
	}
	liveEvents, err := httpserve.NewEvents(httpserve.EventsConfig{
		Version:     liveBoard.Version,
		SessionUser: auth.SessionUser,
	})
	if err != nil {
		// The configuration is the fixed one: NewEvents cannot fail
		// here.
		fail(env, 2, "hub serve: the live events are misconfigured")
		return 2
	}
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
	srvHub := hub.New(hub.Deps{
		Store:     st,
		Now:       env.Now,
		Version:   v,
		CronToken: env.Getenv(hubEnvCronToken),
	}, static)
	auth.Register(srvHub)
	mach.Register(srvHub)
	liveBoard.Register(srvHub)
	// The adapter keeps no state of the connection: this wrapper sets
	// the ip of the connection (the RemoteAddr without the port) in
	// the header the machine connect reads as the client of the
	// creation limit. The adapter drops the value the client sends
	// before it sets its own, so the header cannot be spoofed to claim
	// a foreign connection.
	inner := httpserve.WithEvents(httpserve.New(srvHub), liveEvents)
	srv := httpserve.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(machines.RemoteIPHeader)
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			r.Header.Set(machines.RemoteIPHeader, ip)
		}
		inner.ServeHTTP(w, r)
	}))
	_, _ = fmt.Fprintf(env.Stdout, `{"status":"serving","listen":%q}`+"\n", ln.Addr().String())

	ctx, cancel := hubServeContext()
	defer cancel()
	errCh := make(chan error, 1)
	serving = true
	go func() {
		errCh <- srv.Serve(ln)
	}()
	code := 0
	select {
	case <-ctx.Done():
		// The streams end when the server starts to shut down, so a
		// clean Shutdown does not wait the lifetime of a stream.
		liveEvents.Stop()
	case err := <-errCh:
		// The listener failed before any shutdown: the serving line is
		// already on stdout, so only stderr says why.
		_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: %v\n", env.program(), err)
		code = 4
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), hubShutdownTimeout)
	err = srv.Shutdown(shutdownCtx)
	shutdownCancel()
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: shutdown: %v\n", env.program(), err)
		// A handler may still be running past the deadline and read
		// from the web root: the root is left open. The process is
		// exiting; the system releases the root with it.
		return code
	}
	if closeRoot != nil {
		_ = closeRoot()
	}
	return code
}
