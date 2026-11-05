package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/store"
)

type startupStore struct {
	*hubFakeStore
	t                            *testing.T
	ctx                          context.Context
	pings, batches, seeds, reads int
	failures                     int
}

func (s *startupStore) checkContext(ctx context.Context) {
	s.t.Helper()
	if s.ctx == nil {
		s.ctx = ctx
	}
	if ctx != s.ctx {
		s.t.Fatal("startup phases did not share one context")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > hubStartupTimeout {
		s.t.Fatalf("startup deadline = %v, present = %v", deadline, ok)
	}
}
func (s *startupStore) Ping(ctx context.Context) error {
	s.checkContext(ctx)
	s.pings++
	if s.pings <= s.failures {
		return errors.New("database unavailable")
	}
	return nil
}
func (s *startupStore) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	s.checkContext(ctx)
	s.reads++
	if s.pings <= s.failures {
		return store.Result{}, errors.New("database unavailable")
	}
	return s.hubFakeStore.Execute(ctx, stmt)
}
func (s *startupStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	s.checkContext(ctx)
	if strings.Contains(stmts[0].SQL, "WHERE NOT EXISTS (SELECT 1 FROM users)") {
		s.seeds++
	} else {
		s.batches++
	}
	return s.hubFakeStore.Batch(ctx, stmts)
}

// The listener seam stops the successful startup at its first bind, after
// real migration and owner-seed composition, without starting a server.
func startupCommand(t *testing.T, st store.Store, dbURL string, sleep func(context.Context, time.Duration) error) (string, string, int, int) {
	t.Helper()
	oldStore, oldListen := newHubStore, hubServeListen
	t.Cleanup(func() { newHubStore, hubServeListen = oldStore, oldListen })
	if st != nil {
		newHubStore = func(string, string) (store.Store, error) { return st, nil }
	}
	listens := 0
	hubServeListen = func(string, string) (net.Listener, error) { listens++; return nil, errors.New("test bind boundary") }
	vars := hubTestServeVars(map[string]string{hubEnvRootToken: hubTestRootToken, hubEnvDBToken: "synthetic-database-token"})
	if dbURL != "" {
		vars[hubEnvDBURL] = dbURL
	}
	var out, errb bytes.Buffer
	code := cmdHubServe(nil, Env{Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return vars[k] }, Now: time.Now, Sleep: sleep})
	var line map[string]string
	if err := json.Unmarshal(out.Bytes(), &line); err != nil || strings.Count(out.String(), "\n") != 1 || line["status"] != "4" {
		t.Fatalf("output contract: %q (%v)", out.String(), err)
	}
	if code != 4 {
		t.Fatalf("exit = %d, stdout = %q", code, out.String())
	}
	for _, secret := range []string{"synthetic-database-token", hubTestRootToken, hubTestOwnerPass, hubTestOwnerEmail} {
		if strings.Contains(out.String()+errb.String(), secret) {
			t.Fatal("secret in output")
		}
	}
	return out.String(), errb.String(), code, listens
}

func TestHubStartupPhases(t *testing.T) {
	migrations, err := migrate.Load()
	if err != nil {
		t.Fatal(err)
	}
	batches := len(migrations)
	for _, tc := range []struct {
		name                                                       string
		failures                                                   int
		batchErr, seedErr                                          bool
		wantPings, wantSleeps, wantBatches, wantSeeds, wantListens int
	}{
		{"healthy", 0, false, false, 1, 0, batches, 1, 1},
		{"transient", 1, false, false, 2, 1, batches, 1, 1},
		{"migration failure", 0, true, false, 1, 0, 1, 0, 0},
		{"seed failure", 0, false, true, 1, 0, batches, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &startupStore{hubFakeStore: newHubFakeStore(), t: t, failures: tc.failures}
			if tc.batchErr {
				s.batchErr = errors.New("migration failure")
			}
			if tc.seedErr {
				s.seedErr = errors.New("seed failure")
			}
			sleeps := 0
			_, _, _, listens := startupCommand(t, s, "", func(ctx context.Context, d time.Duration) error {
				s.checkContext(ctx)
				sleeps++
				if d <= 0 || d >= hubStartupTimeout {
					t.Fatalf("retry interval %v", d)
				}
				if sleeps > 2 {
					return context.DeadlineExceeded
				}
				return nil
			})
			if s.pings != tc.wantPings || sleeps != tc.wantSleeps || s.batches != tc.wantBatches || s.seeds != tc.wantSeeds || listens != tc.wantListens {
				t.Fatalf("pings/sleeps/migrations/seeds/listens = %d/%d/%d/%d/%d, want %d/%d/%d/%d/%d", s.pings, sleeps, s.batches, s.seeds, listens, tc.wantPings, tc.wantSleeps, tc.wantBatches, tc.wantSeeds, tc.wantListens)
			}
			if s.ctx.Err() != context.Canceled {
				t.Fatalf("startup context not released: %v", s.ctx.Err())
			}
		})
	}
}

func TestHubStartupPermanentFailure(t *testing.T) {
	for _, stop := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(stop.Error(), func(t *testing.T) {
			s := &startupStore{hubFakeStore: newHubFakeStore(), t: t, failures: 100}
			sleeps := 0
			_, stderr, _, listens := startupCommand(t, s, "", func(ctx context.Context, d time.Duration) error {
				s.checkContext(ctx)
				sleeps++
				if sleeps == 2 {
					return stop
				}
				return nil
			})
			if s.pings != 2 || sleeps != 2 || s.reads != 0 || s.batches != 0 || s.seeds != 0 || listens != 0 || !strings.Contains(stderr, stop.Error()) {
				t.Fatalf("pings=%d sleeps=%d reads=%d batches=%d seeds=%d listens=%d stderr=%q", s.pings, sleeps, s.reads, s.batches, s.seeds, listens, stderr)
			}
		})
	}
}

func TestHubStartupLoopbackUnavailable(t *testing.T) {
	for _, transient := range []bool{true, false} {
		t.Run(map[bool]string{true: "transient", false: "permanent"}[transient], func(t *testing.T) {
			requests, sleeps := 0, 0
			var statements []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Requests []struct {
						Stmt struct {
							SQL string `json:"sql"`
						} `json:"stmt"`
					} `json:"requests"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests++
				statements = append(statements, body.Requests[0].Stmt.SQL)
				if requests == 1 || !transient {
					http.Error(w, "synthetic-private-response", 503)
					return
				}
				if body.Requests[0].Stmt.SQL == "SELECT 1" {
					_, _ = w.Write([]byte(`{"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[],"rows":[],"affected_row_count":0}}},{"type":"ok","response":{"type":"close"}}]}`))
					return
				}
				http.Error(w, "synthetic-migration-failure", 500)
			}))
			defer srv.Close()
			out, stderr, _, listens := startupCommand(t, nil, srv.URL, func(context.Context, time.Duration) error {
				sleeps++
				if sleeps == 2 {
					return context.DeadlineExceeded
				}
				return nil
			})
			wantRequests, wantSleeps := 2, 2
			if transient {
				wantRequests, wantSleeps = 3, 1
			}
			if requests != wantRequests || sleeps != wantSleeps || listens != 0 || statements[0] != "SELECT 1" || (transient && !strings.Contains(stderr, "migrations:")) {
				t.Fatalf("requests=%d sleeps=%d listens=%d statements=%q stderr=%q", requests, sleeps, listens, statements, stderr)
			}
			if strings.Contains(out+stderr, "synthetic-") {
				t.Fatal("server response leaked")
			}
		})
	}
}

func TestHubReadinessCanceledContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "expired"}[expired], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			st := &startupStore{hubFakeStore: newHubFakeStore(), t: t}
			err := waitHubReady(ctx, st, func(context.Context, time.Duration) error { t.Fatal("sleep after cancellation"); return nil })
			if !errors.Is(err, ctx.Err()) || st.pings != 0 {
				t.Fatalf("err=%v pings=%d", err, st.pings)
			}
		})
	}
}
