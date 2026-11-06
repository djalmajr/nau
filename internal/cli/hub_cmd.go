package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
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
	hubDefaultListen         = "127.0.0.1:7440"
	hubDefaultRecoveryListen = "127.0.0.1:7441"
	hubStartupTimeout        = 10 * time.Second
	hubReadinessInterval     = 100 * time.Millisecond
	hubShutdownTimeout       = 10 * time.Second
	hubEnvDBURL              = "NAU_HUB_DB_URL"
	hubEnvDBToken            = "NAU_HUB_DB_TOKEN"
	hubEnvCronToken          = "NAU_HUB_CRON_TOKEN"
	hubEnvSessionKey         = "NAU_HUB_SESSION_KEY"
	hubEnvOwnerName          = "NAU_HUB_OWNER_NAME"
	hubEnvOwnerEmail         = "NAU_HUB_OWNER_EMAIL"
	hubEnvOwnerPass          = "NAU_HUB_OWNER_PASSWORD"
	hubEnvRootToken          = "NAU_HUB_ROOT_TOKEN"
	hubEnvClientIPHdr        = "NAU_HUB_CLIENT_IP_HEADER"
	hubUsage                 = "usage: hub serve [--listen <host:port>] [--web <dir>] [--recovery-listen <host:port>] | hub recover --url <url>"
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
	listen            string
	listenSet         bool
	web               string
	webSet            bool
	recoveryListen    string
	recoveryListenSet bool
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
		case "--recovery-listen":
			if f.recoveryListenSet || i+1 >= len(args) {
				return f, false
			}
			f.recoveryListen, f.recoveryListenSet = args[i+1], true
			i++
		default:
			return f, false
		}
	}
	return f, true
}

// validRecoveryListen reports whether the recovery listener address is
// a loopback host and port: the recovery port must bind to the loopback
// of the hub, a literal loopback ip (127.0.0.1, [::1]), so a remote
// connection never reaches the recovery route.
func validRecoveryListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hubServeContext is the signal-bound context of the hub server; the
// tests replace it with a context they cancel.
var hubServeContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// seedOwnerOfEmptyHub seeds the initial owner of the empty database,
// without a session. It returns the exit code: 0 when the hub may
// start, 2 when an owner variable is missing, empty or fails the
// password policy (the stderr line names the variable, never the
// value), and 4 when the database does not answer the count, the
// owner read or the seed, or holds users and no owner. With a user
// already created and the owner confirmed, the owner variables are
// ignored and the warnings say so on stderr, the same way it says when
// there is no root token and no way to recover the access of an
// existing account. A seed that lost the race to another instance
// counts as the success only when the re-read confirms the owner.
func seedOwnerOfEmptyHub(env Env, auth *accounts.Auth, st store.Store, ctx context.Context) int {
	acc := accounts.NewSQL(st)
	count, err := acc.CountUsers(ctx)
	if err != nil {
		fail(env, 4, "hub serve: the user count: "+err.Error())
		return 4
	}
	if count > 0 {
		owner, err := auth.HasOwner(ctx)
		if err != nil {
			fail(env, 4, "hub serve: the owner read: "+err.Error())
			return 4
		}
		if !owner {
			fail(env, 4, "hub serve: the database has users but no owner")
			return 4
		}
		if env.Getenv(hubEnvOwnerName) != "" || env.Getenv(hubEnvOwnerEmail) != "" || env.Getenv(hubEnvOwnerPass) != "" {
			_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: %s, %s and %s are ignored while a user already exists\n",
				env.program(), hubEnvOwnerName, hubEnvOwnerEmail, hubEnvOwnerPass)
		}
		if env.Getenv(hubEnvRootToken) == "" {
			_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: %s is not set: there is no way to reset the password of an existing account\n",
				env.program(), hubEnvRootToken)
		}
		return 0
	}
	name := strings.TrimSpace(env.Getenv(hubEnvOwnerName))
	if name == "" {
		fail(env, 2, "hub serve: "+hubEnvOwnerName+" is required")
		return 2
	}
	email := strings.TrimSpace(env.Getenv(hubEnvOwnerEmail))
	if email == "" {
		fail(env, 2, "hub serve: "+hubEnvOwnerEmail+" is required")
		return 2
	}
	// The password keeps its original bytes for a valid value; a
	// whitespace-only value is a missing variable, not a weak one.
	pass := env.Getenv(hubEnvOwnerPass)
	if strings.TrimSpace(pass) == "" {
		fail(env, 2, "hub serve: "+hubEnvOwnerPass+" is required")
		return 2
	}
	if !accounts.PolicyOK(pass) {
		fail(env, 2, "hub serve: "+hubEnvOwnerPass+" must be 12 to 128 bytes")
		return 2
	}
	if err := auth.SeedOwner(ctx, name, email, pass); err != nil {
		if !errors.Is(err, accounts.ErrOwnerExists) {
			fail(env, 4, "hub serve: the owner seed: "+err.Error())
			return 4
		}
		// A user was created between the count and the insert and the
		// seed lost the race: the hub starts only when the re-read
		// confirms the user is the owner.
		owner, err := auth.HasOwner(ctx)
		if err != nil {
			fail(env, 4, "hub serve: the owner read: "+err.Error())
			return 4
		}
		if !owner {
			fail(env, 4, "hub serve: the database has users but no owner")
			return 4
		}
	}
	return 0
}

// cmdHub runs the hub commands: serve is the hub server and recover
// is the password recovery against a running hub.
func cmdHub(args []string, env Env) int {
	if len(args) == 0 {
		badUsage(env, hubUsage)
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdHubServe(args[1:], env)
	case "recover":
		return cmdHubRecover(args[1:], env)
	default:
		badUsage(env, hubUsage)
		return 2
	}
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
	recoveryListen := f.recoveryListen
	if !f.recoveryListenSet {
		recoveryListen = hubDefaultRecoveryListen
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
	// The root token opens the password recovery route; when it is
	// defined it must be long enough to be unguessable. The error
	// names the variable only, never the value, and exits before the
	// connection opens, with the other environment validations.
	rootToken := env.Getenv(hubEnvRootToken)
	if rootToken != "" && (len(rootToken) < 32 || strings.TrimSpace(rootToken) == "") {
		fail(env, 2, "hub serve: "+hubEnvRootToken+" must be at least 32 bytes and not blank")
		return 2
	}
	// The recovery port binds to the loopback of the hub only: the
	// address must be a literal loopback ip and port, checked with the
	// other validations, before any connection opens.
	if !validRecoveryListen(recoveryListen) {
		fail(env, 2, "hub serve: --recovery-listen must be a literal loopback host:port")
		return 2
	}
	st, err := newHubStore(dbURL, token)
	if err != nil {
		fail(env, 2, "hub serve: "+err.Error())
		return 2
	}
	// Readiness, migrations and owner seed share one startup budget. Only
	// the read-only probe is retried; a write may have committed even when
	// its response was lost.
	migCtx, migCancel := context.WithTimeout(context.Background(), hubStartupTimeout)
	if err := waitHubReady(migCtx, st, env.Sleep); err != nil {
		migCancel()
		fail(env, 4, "hub serve: readiness: "+err.Error())
		return 4
	}
	if err := (&migrate.Runner{Store: st, Now: env.Now}).Apply(migCtx); err != nil {
		migCancel()
		fail(env, 4, "hub serve: migrations: "+err.Error())
		return 4
	}
	// The accounts are over the same store; the clock and the
	// randomness are the production ones. A bad session key never
	// reaches here: New only fails on a key shorter than 32 bytes. The
	// recovery lines go to stderr through the log writer.
	auth, err := accounts.New(accounts.Config{
		Store:      accounts.NewSQL(st),
		SessionKey: sessionKey,
		RootToken:  rootToken,
		Log:        env.Stderr,
		Now:        env.Now,
	})
	if err != nil {
		migCancel()
		fail(env, 2, "hub serve: "+hubEnvSessionKey+" must be base64 of at least 32 bytes")
		return 2
	}
	// The initial owner: on the empty database the hub seeds it from
	// the NAU_HUB_OWNER_* variables, without a session; with a user
	// already there the variables are ignored and the stderr says so,
	// the same way it says when there is no root token and no way to
	// recover the access of an existing account. The count and the
	// seed are the database work here: a failure exits 4, a missing
	// or invalid variable exits 2 naming the variable, never the
	// value, and losing the seed race to another instance is the
	// success.
	if code := seedOwnerOfEmptyHub(env, auth, st, migCtx); code != 0 {
		migCancel()
		return code
	}
	migCancel()
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
	// The recovery listener: it opens only when the root token is set,
	// on the loopback address the flag just validated, before the
	// serving line. A failure closes the public listener that is
	// already open and exits 4; without the token nothing opens.
	var recLn net.Listener
	var recAddr string
	if rootToken != "" {
		recLn, err = hubServeListen("tcp", recoveryListen)
		if err != nil {
			_ = ln.Close()
			fail(env, 4, "hub serve: cannot listen on "+recoveryListen)
			return 4
		}
		if ta, ok := recLn.Addr().(*net.TCPAddr); !ok || ta.Port <= 0 {
			_ = recLn.Close()
			_ = ln.Close()
			fail(env, 4, "hub serve: cannot listen on "+recoveryListen)
			return 4
		}
		recAddr = recLn.Addr().String()
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
	// The recovery server serves the password recovery route alone, on
	// its own loopback listener: no static files, no events stream and
	// no ip header wrapper, and no cron token. The sessions it creates
	// are the same store rows the public server resolves, so the new
	// cookie works on the public port right away.
	var recSrv *http.Server
	if recLn != nil {
		recHub := hub.New(hub.Deps{Store: st, Now: env.Now, Version: v}, nil)
		auth.RegisterRecovery(recHub)
		recSrv = httpserve.NewServer(httpserve.New(recHub))
	}
	// The adapter keeps no state of the connection: this wrapper sets
	// the ip of the connection (the RemoteAddr without the port) in
	// the header the machine connect reads as the client of the
	// creation limit. The adapter drops the value the client sends
	// before it sets its own, so the header cannot be spoofed to claim
	// a foreign connection.
	inner := httpserve.WithEvents(httpserve.New(srvHub), liveEvents)
	ctx, cancel := hubServeContext()
	defer cancel()
	if recAddr != "" {
		_, _ = fmt.Fprintf(env.Stdout, `{"status":"serving","listen":%q,"recovery_listen":%q}`+"\n", ln.Addr().String(), recAddr)
	} else {
		_, _ = fmt.Fprintf(env.Stdout, `{"status":"serving","listen":%q}`+"\n", ln.Addr().String())
	}

	srv := httpserve.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(machines.RemoteIPHeader)
		if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			r.Header.Set(machines.RemoteIPHeader, ip)
		}
		inner.ServeHTTP(w, r)
	}))
	errCh := make(chan error, 2)
	serving = true
	go func() {
		errCh <- srv.Serve(ln)
	}()
	if recSrv != nil {
		go func() {
			errCh <- recSrv.Serve(recLn)
		}()
	}
	code := 0
	select {
	case <-ctx.Done():
		// The streams end when the server starts to shut down, so a
		// clean Shutdown does not wait the lifetime of a stream.
		liveEvents.Stop()
	case err := <-errCh:
		// A listener failed before any shutdown: the serving line is
		// already on stdout, so only stderr says why.
		_, _ = fmt.Fprintf(env.Stderr, "%s: hub serve: %v\n", env.program(), err)
		code = 4
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), hubShutdownTimeout)
	err = srv.Shutdown(shutdownCtx)
	if recSrv != nil {
		if rerr := recSrv.Shutdown(shutdownCtx); rerr != nil && err == nil {
			err = rerr
		}
	}
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

// waitHubReady probes without replaying any migration or owner write.
func waitHubReady(ctx context.Context, st store.Store, sleep func(context.Context, time.Duration) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := st.Ping(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		if err := sleep(ctx, hubReadinessInterval); err != nil {
			return err
		}
	}
}
