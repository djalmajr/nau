package a2astore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/djalmajr/nau/internal/a2acore"
	"github.com/djalmajr/nau/internal/notify"
	"github.com/djalmajr/nau/internal/registry"
)

func attemptFixture(t *testing.T, clk *clock) (*Store, a2acore.DeliveryClaim) {
	t.Helper()
	s, _ := openTest(t, clk, "local")
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
	return s, claims[0]
}

func attemptFields(t *testing.T, s *Store, id int64) (sql.NullString, sql.NullInt64) {
	t.Helper()
	var end sql.NullString
	var ms sql.NullInt64
	if err := s.db.QueryRow(`SELECT last_attempt_end, last_attempt_ms FROM a2a_deliveries WHERE delivery_id = ?`, id).Scan(&end, &ms); err != nil {
		t.Fatal(err)
	}
	return end, ms
}

func TestAttemptCompletionTiming(t *testing.T) {
	for _, outcome := range []string{notify.OutcomeUncertain, notify.OutcomeAccepted, notify.OutcomeRejected, notify.OutcomeTransient, "exhausted"} {
		t.Run(outcome, func(t *testing.T) {
			clk := &clock{t: testNow.Add(123 * time.Millisecond)}
			s, c := attemptFixture(t, clk)
			if outcome == "exhausted" {
				c.Attempt = notify.MaxAttempts
				if _, err := s.db.Exec(`UPDATE a2a_deliveries SET attempts = ? WHERE delivery_id = ?`, c.Attempt, c.DeliveryID); err != nil {
					t.Fatal(err)
				}
			}
			start := clk.Now()
			clk.Add(2175 * time.Millisecond)
			r := notify.SendResult{Outcome: outcome, Exit: 15, Category: "deadline"}
			if outcome == "exhausted" {
				r.Outcome = notify.OutcomeTransient
			}
			if err := s.Complete(context.Background(), c, r, clk.Now()); err != nil {
				t.Fatal(err)
			}
			end, ms := attemptFields(t, s, c.DeliveryID)
			if !end.Valid || end.String != clk.Now().Format(time.RFC3339Nano) || !ms.Valid || ms.Int64 != 2175 {
				t.Fatalf("end=%+v ms=%+v start=%s now=%s", end, ms, start, clk.Now())
			}
			row := readDelivery(t, s, c.DeliveryID)
			if row.last != start.Format(tsLayout) {
				t.Fatalf("last_attempt changed semantics: %q", row.last)
			}
			// A stale completion must not replace the recorded timing.
			clk.Add(time.Hour)
			if err := s.Complete(context.Background(), c, r, clk.Now()); err != nil {
				t.Fatal(err)
			}
			end2, ms2 := attemptFields(t, s, c.DeliveryID)
			if end2 != end || ms2 != ms {
				t.Fatalf("stale completion overwrote timing: %+v %+v", end2, ms2)
			}
		})
	}
}

func TestAttemptReleaseKeepsTiming(t *testing.T) {
	clk := &clock{t: testNow}
	s, c := attemptFixture(t, clk)
	if err := s.Release(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	end, ms := attemptFields(t, s, c.DeliveryID)
	if end.Valid || ms.Valid {
		t.Fatalf("unsent release recorded timing: %+v %+v", end, ms)
	}
	claims, err := s.ClaimDue(context.Background(), clk.Now(), 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v %v", claims, err)
	}
	clk.Add(time.Second)
	if err := s.Complete(context.Background(), claims[0], notify.SendResult{Outcome: notify.OutcomeTransient}, clk.Now()); err != nil {
		t.Fatal(err)
	}
	end, ms = attemptFields(t, s, c.DeliveryID)
	clk.Add(notify.RetrySchedule[0])
	claims, err = s.ClaimDue(context.Background(), clk.Now(), 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%+v %v", claims, err)
	}
	if err := s.Release(context.Background(), claims[0]); err != nil {
		t.Fatal(err)
	}
	end2, ms2 := attemptFields(t, s, c.DeliveryID)
	if end != end2 || ms != ms2 {
		t.Fatalf("release erased previous timing: %+v %+v", end2, ms2)
	}
}

func TestAttemptMigrationAndLegacyAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry ? # .db")
	reg, err := registry.Open(path, registry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	if _, err = reg.DB().Exec(retryBaselineDDL); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.DB().Exec(`INSERT INTO a2a_deliveries(ingress_id,task_id,role,state,attempts,last_attempt,created,created_unix) VALUES(1,'t1','owner','uncertain',1,? ,?,1)`, testNow.Format(tsLayout), testNow.Format(tsLayout)); err != nil {
		t.Fatal(err)
	}
	reg.Close()
	clk := &clock{t: testNow.Add(125 * time.Second)}
	health, err := ReadUncertain(context.Background(), path, clk.Now())
	if err != nil || health == nil || health.Uncertain != 1 || health.Oldest == nil || *health.Oldest != 125 {
		t.Fatalf("legacy read-only summary=%+v %v", health, err)
	}
	for i := 0; i < 2; i++ {
		s, err := OpenFile(path, Options{Now: clk.Now})
		if err != nil {
			t.Fatal(err)
		}
		end, ms := attemptFields(t, s, 1)
		if end.Valid || ms.Valid {
			s.Close()
			t.Fatal("migration fabricated a completed attempt")
		}
		d, err := s.deliveriesOf(context.Background(), "t1")
		if err != nil || len(d) != 1 {
			s.Close()
			t.Fatalf("legacy delivery=%+v %v", d, err)
		}
		b, _ := json.Marshal(d[0])
		if !strings.Contains(string(b), `"uncertain_age_s":125`) || strings.Contains(string(b), `"last_attempt_end"`) || strings.Contains(string(b), `"last_attempt_ms"`) {
			s.Close()
			t.Fatalf("legacy json=%s", b)
		}
		s.Close()
	}
}
