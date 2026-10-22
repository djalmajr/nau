package a2astore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/djalmajr/nau/internal/a2acore"
	"github.com/djalmajr/nau/internal/notify"
	"github.com/djalmajr/nau/internal/registry"
)

func TestRetryDeliveryOnce(t *testing.T) {
	for _, state := range []string{a2acore.DeliveryUncertain, a2acore.DeliveryExhausted} {
		t.Run(state, func(t *testing.T) {
			clk := &clock{t: testNow}
			s, path := openTest(t, clk, "local")
			ctx := context.Background()
			if _, err := s.Admit(ctx, admission("local", "m1", "t1", "c1", "org/example", "s1", 1, 1, true, []a2acore.PlannedDelivery{ownerDelivery("local/owner")})); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(ctx, mkTask("t1", "c1", a2a.TaskStateWorking, 1)); err != nil {
				t.Fatal(err)
			}
			claims, err := s.ClaimDue(ctx, clk.Now(), 1, time.Minute)
			if err != nil || len(claims) != 1 {
				t.Fatalf("claims = %+v, %v", claims, err)
			}
			id := claims[0].DeliveryID
			if _, err := s.db.Exec(`UPDATE a2a_deliveries SET state = ?, attempts = ? WHERE delivery_id = ?`, state, notify.MaxAttempts, id); err != nil {
				t.Fatal(err)
			}
			for _, wrong := range []struct {
				task string
				id   int64
			}{{"missing", id}, {"t1", id + 1}} {
				found, err := s.Retry(ctx, wrong.task, wrong.id, retryRoutes{owner: "local/owner"})
				if found != "" || err != nil {
					t.Fatalf("wrong key %+v = %v, %v", wrong, found, err)
				}
			}
			before := readDelivery(t, s, id)
			var wg sync.WaitGroup
			results := make(chan string, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					found, err := s.Retry(ctx, "t1", id, retryRoutes{owner: "local/owner"})
					if err != nil {
						t.Error(err)
					}
					results <- found
				}()
			}
			wg.Wait()
			close(results)
			count := 0
			for found := range results {
				if found != "" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("successful retries = %d, want 1", count)
			}
			after := readDelivery(t, s, id)
			if after.attempts != before.attempts || after.state != a2acore.DeliveryPending || !after.hasNext || after.nextAttempt != clk.Now().Unix() || after.hasLease {
				t.Fatalf("requeued row = %+v, before %+v", after, before)
			}
			claims, err = s.ClaimDue(ctx, clk.Now(), 1, time.Minute)
			if err != nil || len(claims) != 1 || claims[0].Attempt != notify.MaxAttempts+1 {
				t.Fatalf("retry claims = %+v, %v", claims, err)
			}
			// An unsuccessful manual attempt cannot unlock another manual retry.
			if err := s.Complete(ctx, claims[0], notify.SendResult{Outcome: notify.OutcomeTransient, Exit: 1}, clk.Now()); err != nil {
				t.Fatal(err)
			}
			if got := readDelivery(t, s, id).state; got != a2acore.DeliveryExhausted {
				t.Fatalf("state = %s", got)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenFile(path, Options{Now: clk.Now})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			found, err := s.Retry(ctx, "t1", id, retryRoutes{owner: "local/owner"})
			if found != "" || err != nil {
				t.Fatalf("retry after reopen = %v, %v", found, err)
			}
		})
	}
}

func TestRetryDeliveryStates(t *testing.T) {
	clk := &clock{t: testNow}
	s, _ := openTest(t, clk, "local")
	ctx := context.Background()
	for i, state := range []string{a2acore.DeliveryHeld, a2acore.DeliveryPending, a2acore.DeliveryInFlight, a2acore.DeliveryAccepted, a2acore.DeliveryRejected, a2acore.DeliveryNoRoute, a2acore.DeliverySelf, a2acore.DeliveryObsolete} {
		id := fmt.Sprintf("t%d", i)
		if _, err := s.Admit(ctx, admission("local", id, id, id, "org/example", id, 1, 1, true, []a2acore.PlannedDelivery{ownerDelivery("local/owner")})); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Create(ctx, mkTask(id, id, a2a.TaskStateWorking, 1)); err != nil {
			t.Fatal(err)
		}
		var delivery int64
		if err := s.db.QueryRow(`SELECT delivery_id FROM a2a_deliveries WHERE task_id = ?`, id).Scan(&delivery); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE a2a_deliveries SET state = ? WHERE delivery_id = ?`, state, delivery); err != nil {
			t.Fatal(err)
		}
		found, err := s.Retry(ctx, id, delivery, retryRoutes{owner: "local/owner"})
		if found != "" || err != nil {
			t.Fatalf("retry %s = %v, %v", state, found, err)
		}
		if row := readDelivery(t, s, delivery); row.state != state {
			t.Fatalf("retry changed %s to %s", state, row.state)
		}
	}
}

// retryBaselineDDL is the verbatim A2A DDL from base 80fc182.
const retryBaselineDDL = `
CREATE TABLE IF NOT EXISTS a2a_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS a2a_tasks(task_id TEXT PRIMARY KEY, context_id TEXT NOT NULL, owner TEXT NOT NULL, project TEXT NOT NULL, job TEXT, state TEXT NOT NULL, version INTEGER NOT NULL, messages INTEGER NOT NULL, task_json TEXT NOT NULL, created TEXT NOT NULL, created_unix INTEGER NOT NULL, updated TEXT NOT NULL, updated_nano INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS a2a_tasks_owner ON a2a_tasks(owner, updated_nano DESC, task_id DESC);
CREATE INDEX IF NOT EXISTS a2a_tasks_context ON a2a_tasks(context_id);
CREATE TABLE IF NOT EXISTS a2a_ingress(ingress_id INTEGER PRIMARY KEY AUTOINCREMENT, subject TEXT NOT NULL, message_id TEXT NOT NULL, task_id TEXT NOT NULL, context_id TEXT NOT NULL, project TEXT NOT NULL, job TEXT, session TEXT NOT NULL, epoch INTEGER NOT NULL, seq INTEGER NOT NULL, new_task INTEGER NOT NULL, text_bytes INTEGER NOT NULL, received TEXT NOT NULL, received_unix INTEGER NOT NULL, UNIQUE(subject, message_id));
CREATE TABLE IF NOT EXISTS a2a_fences(subject TEXT NOT NULL, session TEXT NOT NULL, epoch INTEGER NOT NULL, last_seq INTEGER NOT NULL, updated TEXT NOT NULL, PRIMARY KEY(subject, session));
CREATE TABLE IF NOT EXISTS a2a_deliveries(delivery_id INTEGER PRIMARY KEY AUTOINCREMENT, ingress_id INTEGER NOT NULL, task_id TEXT NOT NULL, role TEXT NOT NULL, ref TEXT, state TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_unix INTEGER, lease_until_unix INTEGER, first_attempt TEXT, last_attempt TEXT, accepted TEXT, accept_status TEXT, last_exit INTEGER, last_error TEXT, created TEXT NOT NULL, created_unix INTEGER NOT NULL, UNIQUE(ingress_id, role));
CREATE INDEX IF NOT EXISTS a2a_deliveries_due ON a2a_deliveries(state, next_attempt_unix);
CREATE TABLE IF NOT EXISTS a2a_decisions(decision_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, actor TEXT NOT NULL, state TEXT NOT NULL, refusal TEXT, recorded TEXT NOT NULL, recorded_seq INTEGER NOT NULL, applied TEXT);
CREATE TABLE IF NOT EXISTS a2a_push_configs(task_id TEXT NOT NULL, config_id TEXT NOT NULL, url TEXT NOT NULL, auth_scheme TEXT, token_ref TEXT, credentials_ref TEXT, created TEXT NOT NULL, PRIMARY KEY(task_id, config_id));
CREATE TABLE IF NOT EXISTS a2a_secret_cleanup(handle TEXT PRIMARY KEY, scheduled TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, last_attempt TEXT);
`

func TestRetryMigrationFromBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.db")
	reg, err := registry.Open(path, registry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if _, err := reg.DB().Exec(retryBaselineDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.DB().Exec(`INSERT INTO a2a_meta(key,value) VALUES ('schema','1');
        INSERT INTO a2a_tasks(task_id,context_id,owner,project,state,version,messages,task_json,created,created_unix,updated,updated_nano)
        VALUES ('t1','c1','local','org/example','working',1,1,'{}','2026-03-04T05:06:07+02:00',1,'2026-03-04T05:06:07+02:00',1);
        INSERT INTO a2a_deliveries(ingress_id,task_id,role,ref,state,attempts,created,created_unix)
        VALUES (1,'t1','owner','local/owner','uncertain',1,'2026-03-04T05:06:07+02:00',1)`); err != nil {
		t.Fatal(err)
	}
	if err := reg.Close(); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: testNow}
	for i := 0; i < 2; i++ {
		s, err := OpenFile(path, Options{Now: clk.Now})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = s.db.QueryRow(`SELECT COUNT(*) FROM a2a_deliveries WHERE task_id='t1' AND attempts=1`).Scan(&n)
		if err != nil || n != 1 {
			s.Close()
			t.Fatalf("preserved rows = %d, %v", n, err)
		}
		if i == 1 {
			found, err := s.Retry(context.Background(), "t1", 1, retryRoutes{owner: "local/owner"})
			if found == "" || err != nil {
				s.Close()
				t.Fatalf("retry migrated row = %v, %v", found, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Removing Retry's terminal-task filter consumes a retry marker and queues a closed task.
func TestRetryTerminalTasks(t *testing.T) {
	for _, state := range []string{a2acore.StateCompleted, a2acore.StateCanceled, a2acore.StateFailed, a2acore.StateRejected, a2acore.StateWorking} {
		for _, delivery := range []string{a2acore.DeliveryUncertain, a2acore.DeliveryExhausted} {
			t.Run(state+"/"+delivery, func(t *testing.T) {
				clk := &clock{t: testNow}
				s, c := attemptFixture(t, clk)
				if _, err := s.db.Exec(`UPDATE a2a_tasks SET state=?`, state); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE a2a_deliveries SET state=?`, delivery); err != nil {
					t.Fatal(err)
				}
				want := state == a2acore.StateWorking
				found, err := s.Retry(context.Background(), "t1", c.DeliveryID, retryRoutes{owner: "local/owner"})
				if err != nil || (found != "") != want {
					t.Fatalf("retry=%v %v, want %v", found, err, want)
				}
				var markers int
				if err := s.db.QueryRow(`SELECT COUNT(*) FROM a2a_delivery_retries`).Scan(&markers); err != nil {
					t.Fatal(err)
				}
				claims, err := s.ClaimDue(context.Background(), clk.Now(), 1, time.Minute)
				if err != nil || (len(claims) == 1) != want || (markers == 1) != want {
					t.Fatalf("claims=%+v markers=%d err=%v", claims, markers, err)
				}
			})
		}
	}
}

// retryRoutes supplies current registrations, independently of stored plans.
type retryRoutes struct {
	owner, guardian string
}

func (r retryRoutes) OwnerRef(project string) (string, error) {
	if project != "org/example" {
		return "", nil
	}
	return r.owner, nil
}
func (r retryRoutes) GuardianRef() (string, error) { return r.guardian, nil }

func TestRetryCurrentRole(t *testing.T) {
	for _, tc := range []struct {
		name, role, project string
		routes              retryRoutes
		want                string
	}{
		{"owner_changed", a2acore.RoleOwner, "org/example", retryRoutes{owner: "local/new-owner"}, "local/new-owner"},
		{"owner_unchanged", a2acore.RoleOwner, "org/example", retryRoutes{owner: "local/owner"}, "local/owner"},
		{"guardian_with_owner_present", a2acore.RoleGuardian, "org/example", retryRoutes{owner: "local/new-owner", guardian: "local/new-guardian"}, "local/new-guardian"},
		{"owner_missing_with_guardian", a2acore.RoleOwner, "org/example", retryRoutes{guardian: "local/guardian"}, ""},
		{"guardian_missing_with_owner", a2acore.RoleGuardian, "org/example", retryRoutes{owner: "local/owner"}, ""},
		{"task_project_is_authoritative", a2acore.RoleOwner, "org/other", retryRoutes{owner: "local/owner"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := &clock{t: testNow}
			s, c := attemptFixture(t, clk)
			ctx := context.Background()
			if err := s.Complete(ctx, c, notify.SendResult{Outcome: notify.OutcomeUncertain, Exit: 15, Category: "deadline"}, clk.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE a2a_deliveries SET role=?`, tc.role); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE a2a_tasks SET project=?`, tc.project); err != nil {
				t.Fatal(err)
			}
			before := readDelivery(t, s, c.DeliveryID)
			ref, err := s.Retry(ctx, "t1", c.DeliveryID, tc.routes)
			if tc.want == "" {
				if ref != "" || !errors.Is(err, ErrNoRoute) {
					t.Fatalf("ref=%q err=%v", ref, err)
				}
			} else if err != nil || ref != tc.want {
				t.Fatalf("ref=%q err=%v want=%q", ref, err, tc.want)
			}
			var stored string
			var markers int
			if err := s.db.QueryRow(`SELECT ref FROM a2a_deliveries WHERE delivery_id=?`, c.DeliveryID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM a2a_delivery_retries`).Scan(&markers); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if stored != c.Ref || markers != 0 || readDelivery(t, s, c.DeliveryID) != before {
					t.Fatalf("no_route changed row: ref=%q markers=%d", stored, markers)
				}
			} else {
				claims, err := s.ClaimDue(ctx, clk.Now(), 1, time.Minute)
				if err != nil || len(claims) != 1 || claims[0].Ref != tc.want || stored != tc.want || markers != 1 {
					t.Fatalf("claims=%+v ref=%q markers=%d err=%v", claims, stored, markers, err)
				}
			}
		})
	}
}

func TestRetryReferenceRollback(t *testing.T) {
	clk := &clock{t: testNow}
	s, c := attemptFixture(t, clk)
	ctx := context.Background()
	if err := s.Complete(ctx, c, notify.SendResult{Outcome: notify.OutcomeUncertain, Exit: 15}, clk.Now()); err != nil {
		t.Fatal(err)
	}
	before := readDelivery(t, s, c.DeliveryID)
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_retry BEFORE INSERT ON a2a_delivery_retries BEGIN SELECT RAISE(ABORT, 'retry marker rejected'); END`); err != nil {
		t.Fatal(err)
	}
	routes := retryRoutes{owner: "local/new-owner"}
	ref, err := s.Retry(ctx, "t1", c.DeliveryID, routes)
	if err == nil || ref != "" {
		t.Fatalf("failed transaction=%q %v", ref, err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT ref FROM a2a_deliveries WHERE delivery_id=?`, c.DeliveryID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var markers int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM a2a_delivery_retries`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if stored != c.Ref || readDelivery(t, s, c.DeliveryID) != before || markers != 0 {
		t.Fatalf("partial commit: ref=%q markers=%d", stored, markers)
	}
	if _, err := s.db.Exec(`DROP TRIGGER refuse_retry`); err != nil {
		t.Fatal(err)
	}
	ref, err = s.Retry(ctx, "t1", c.DeliveryID, routes)
	if err != nil || ref != routes.owner {
		t.Fatalf("restored retry=%q %v", ref, err)
	}
	claims, err := s.ClaimDue(ctx, clk.Now(), 1, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].Ref != routes.owner {
		t.Fatalf("claims=%+v %v", claims, err)
	}
}
