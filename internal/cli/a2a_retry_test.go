package cli

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/a2astore"
	"github.com/djalmajr/nau/internal/notify"
)

func retryFixture(t *testing.T, outcome string) (string, string, int64) {
	t.Helper()
	dir := t.TempDir()
	task, db := a2aReplyActorFixture(t, dir, "local/owner")
	a2aReplyActorRegisterOwner(t, dir, "org/example", "local/owner")
	s, err := a2astore.OpenFile(db, a2astore.Options{Now: func() time.Time { return serveFixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	claims, err := s.ClaimDue(context.Background(), serveFixedNow, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	for attempt := 0; ; attempt++ {
		if err := s.Complete(context.Background(), claims[0], notify.SendResult{Outcome: outcome, Exit: 15, Category: "deadline"}, serveFixedNow); err != nil {
			t.Fatal(err)
		}
		if outcome != notify.OutcomeTransient || attempt+1 == notify.MaxAttempts {
			break
		}
		claims, err = s.ClaimDue(context.Background(), serveFixedNow.Add(time.Hour), 1, time.Minute)
		if err != nil || len(claims) != 1 {
			t.Fatalf("retry claims = %+v, %v", claims, err)
		}
	}
	return dir, task, claims[0].DeliveryID
}

func TestA2ARetryExhausted(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeTransient)
	db := filepath.Join(dir, "state", "registry.db")
	args := []string{"retry", "--task", task, "--delivery", fmt.Sprint(id), "--db", db}
	out, stderr, exit := runA2A(t, dir, args...)
	if exit != 0 || out != "{\"retried\":true,\"ref\":\"local/owner\"}\n" || stderr != "" {
		t.Fatalf("exhausted retry = %d %q %q", exit, out, stderr)
	}
	out, _, exit = runA2A(t, dir, args...)
	if exit != 2 || !strings.Contains(out, "refused") {
		t.Fatalf("second exhausted retry = %d %q", exit, out)
	}
}

func TestA2ARetryOnce(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeUncertain)
	// The serve is already alive when retry changes its durable queue.
	_, exits, cancel, _, sender := startA2AServe(t, dir, "--listen", "127.0.0.1:0")
	t.Cleanup(func() {
		cancel()
		select {
		case exit := <-exits:
			if exit != 0 {
				t.Errorf("serve exit = %d", exit)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	})
	args := []string{"retry", "--task", task, "--delivery", fmt.Sprint(id)}
	out, stderr, exit := runA2A(t, dir, args...)
	if exit != 0 || out != "{\"retried\":true,\"ref\":\"local/owner\"}\n" || stderr != "" {
		t.Fatalf("retry = %d %q %q", exit, out, stderr)
	}
	out, _, exit = runA2A(t, dir, args...)
	if exit != 2 || !strings.Contains(out, "refused") {
		t.Fatalf("second retry = %d %q", exit, out)
	}
	deadline := time.After(5 * time.Second)
	for {
		out, _, exit = runA2A(t, dir, "show", "--task", task)
		if exit == 0 && strings.Contains(out, `"state":"accepted"`) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("retry not delivered: %d %q", exit, out)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if sender.count() != 1 {
		t.Fatalf("sends = %d, want 1", sender.count())
	}
}

func TestA2ARetryRefusals(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeAccepted)
	for _, args := range [][]string{
		{"retry", "--task", task, "--delivery", fmt.Sprint(id)},
		{"retry", "--task", "missing", "--delivery", fmt.Sprint(id)},
		{"retry", "--task", task, "--delivery", "999999"},
		{"retry", "--task", task, "--delivery", "0"},
		{"retry", "--task", task, "--delivery", "bad"},
		{"retry", "--task", task},
		{"retry", "--delivery", fmt.Sprint(id)},
		{"retry", "--task", task, "--delivery", fmt.Sprint(id), "--delivery", "2"},
	} {
		out, _, exit := runA2A(t, dir, args...)
		if exit != 2 {
			t.Errorf("%v = %d %q, want exit 2", args, exit, out)
		}
		a2aOneJSONLine(t, "retry refusal", out)
	}
	missing := filepath.Join(t.TempDir(), "absent", "registry.db")
	_, _, exit := runA2A(t, dir, "retry", "--task", task, "--delivery", fmt.Sprint(id), "--db", missing)
	if exit != 2 {
		t.Fatalf("missing db exit = %d", exit)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("retry created a missing database: %v", err)
	}
}

func TestA2ARetryNowrite(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeUncertain)
	var out, stderr bytes.Buffer
	env := Env{ConfigDir: dir, Stdout: &out, Stderr: &stderr, Getenv: func(k string) string {
		if k == nowriteVar {
			return "1"
		}
		return ""
	}}
	if exit := cmdA2A([]string{"retry", "--task", task, "--delivery", fmt.Sprint(id)}, env); exit != 2 || out.String() != nowriteErrorJSON+"\n" {
		t.Fatalf("nowrite = %d %q", exit, out.String())
	}
	stdout, _, exit := runA2A(t, dir, "retry", "--task", task, "--delivery", fmt.Sprint(id))
	if exit != 0 {
		t.Fatalf("retry after nowrite = %d %q", exit, stdout)
	}
}

func TestA2ARetryTerminalTask(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeUncertain)
	s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "state", "registry.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(`UPDATE a2a_tasks SET state='completed'`); err != nil {
		t.Fatal(err)
	}
	out, _, exit := runA2A(t, dir, "retry", "--task", task, "--delivery", fmt.Sprint(id))
	if exit != 2 || !strings.Contains(out, "non-terminal task") {
		t.Fatalf("terminal retry=%d %q", exit, out)
	}
}

func TestA2ARetryCurrentRecipient(t *testing.T) {
	for _, ref := range []string{"local/new-owner", "local/owner"} {
		t.Run(ref, func(t *testing.T) {
			dir, task, id := retryFixture(t, notify.OutcomeUncertain)
			a2aReplyActorRegisterOwner(t, dir, "org/example", ref)
			_, exits, cancel, _, sender := startA2AServe(t, dir, "--listen", "127.0.0.1:0")
			t.Cleanup(func() {
				cancel()
				select {
				case exit := <-exits:
					if exit != 0 {
						t.Errorf("serve exit=%d", exit)
					}
				case <-time.After(10 * time.Second):
					t.Error("serve did not stop")
				}
			})
			out, stderr, exit := runA2A(t, dir, "retry", "--task", task, "--delivery", fmt.Sprint(id))
			want := fmt.Sprintf("{\"retried\":true,\"ref\":%q}\n", ref)
			if exit != 0 || out != want || stderr != "" {
				t.Errorf("retry=%d %q %q; want %q", exit, out, stderr, want)
			}
			deadline := time.After(5 * time.Second)
			for {
				out, _, exit = runA2A(t, dir, "show", "--task", task)
				if exit == 0 && strings.Contains(out, `"state":"accepted"`) {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("retry not accepted: %d %q", exit, out)
				case <-time.After(10 * time.Millisecond):
				}
			}
			calls := sender.callsCopy()
			if len(calls) != 1 || calls[0].Ref != ref {
				t.Fatalf("sends=%+v, want ref %q", calls, ref)
			}
		})
	}
}

func TestA2ARetryNoRoute(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeUncertain)
	out, stderr, exit := runNotifyFlow(t, dir, &testClock{t: serveFixedNow}, nil, "", "notify", "unregister", "owner", "--projeto", "org/example")
	if exit != 0 {
		t.Fatalf("unregister=%d %q %q", exit, out, stderr)
	}
	before, _, exit := runA2A(t, dir, "show", "--task", task)
	if exit != 0 {
		t.Fatal(before)
	}
	args := []string{"retry", "--task", task, "--delivery", fmt.Sprint(id)}
	out, stderr, exit = runA2A(t, dir, args...)
	if exit != 2 || !strings.Contains(out, "no_route") || stderr != "" {
		t.Errorf("retry without registration=%d %q %q", exit, out, stderr)
	}
	after, _, exit := runA2A(t, dir, "show", "--task", task)
	if exit != 0 || after != before {
		t.Errorf("refusal changed delivery: before=%s after=%s", before, after)
	}
	a2aReplyActorRegisterOwner(t, dir, "org/example", "local/restored-owner")
	out, _, exit = runA2A(t, dir, args...)
	if exit != 0 || !strings.Contains(out, `"ref":"local/restored-owner"`) {
		t.Fatalf("retry after restoring registration=%d %q", exit, out)
	}
}
