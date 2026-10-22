package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/notify"
	"github.com/djalmajr/nau/internal/outbox"
)

func TestA2AAttemptViews(t *testing.T) {
	dir, task, id := retryFixture(t, notify.OutcomeUncertain)
	db := filepath.Join(outbox.StateDir(dir), "registry.db")
	s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	end := serveFixedNow.Add(-125 * time.Second).Format(time.RFC3339)
	if _, err = s.Exec(`UPDATE a2a_deliveries SET last_attempt_end=?, last_attempt_ms=2175 WHERE delivery_id=?`, end, id); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"inbox"}, {"show", "--task", task}} {
		out, stderr, exit := runA2A(t, dir, args...)
		if exit != 0 {
			t.Fatalf("%v = %d %q %q", args, exit, out, stderr)
		}
		for _, field := range []string{`"last_attempt_end":"` + end + `"`, `"last_attempt_ms":2175`, `"uncertain_age_s":125`} {
			if !strings.Contains(out, field) {
				t.Errorf("%v missing %s: %s", args, field, out)
			}
		}
		if strings.Index(out, `"last_error"`) > strings.Index(out, `"last_attempt_end"`) {
			t.Errorf("new fields precede old ones: %s", out)
		}
	}
}

func TestA2AServeAttemptLog(t *testing.T) {
	dir := t.TempDir()
	_, db := a2aReplyActorFixture(t, dir, "local/owner")
	s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "notice-text-canary-400"
	if _, err = s.Exec(`UPDATE a2a_ingress SET job=?`, marker); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	out, stderr := &testLockedBuffer{}, &testLockedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	oldCtx, oldSender := a2aServeContext, newA2aNoticeSender
	a2aServeContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	sender := &fakeA2ASender{script: map[string][]notify.SendResult{}}
	newA2aNoticeSender = func(string, []string) notify.Sender { return sender }
	exits := make(chan int, 1)
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
		a2aServeContext, newA2aNoticeSender = oldCtx, oldSender
	})
	env := a2aTestEnv(t, dir, out, stderr)
	go func() { exits <- cmdA2A([]string{"serve", "--listen", "127.0.0.1:0"}, env) }()
	out.nextLine(t, 10*time.Second)
	deadline := time.After(3 * time.Second)
	for !strings.Contains(stderr.snapshot(), `"delivery"`) {
		select {
		case <-deadline:
			t.Fatalf("no attempt log: %s", stderr.snapshot())
		case <-time.After(10 * time.Millisecond):
		}
	}
	logs := strings.Split(strings.TrimSpace(stderr.snapshot()), "\n")
	if len(logs) != 1 {
		t.Fatalf("attempt logs=%q", logs)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(logs[0]), &fields); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{"delivery": float64(1), "task": "task-actor", "role": "owner", "ref": "local/owner", "state": "accepted", "error": "", "exit": float64(0), "ms": float64(0)} {
		if fields[k] != v {
			t.Errorf("log %s=%v, want %v: %s", k, fields[k], v, logs[0])
		}
	}
	prev := -1
	for _, k := range []string{"delivery", "task", "role", "ref", "state", "error", "exit", "ms"} {
		pos := strings.Index(logs[0], `"`+k+`":`)
		if pos <= prev {
			t.Fatalf("field order %s: %s", k, logs[0])
		}
		prev = pos
	}
	if strings.Contains(stderr.snapshot(), marker) {
		t.Fatalf("notice leaked: %s", stderr.snapshot())
	}
	calls := sender.callsCopy()
	if len(calls) != 1 || !strings.Contains(calls[0].Message, marker) {
		t.Fatalf("synthetic marker was not in the notice: %+v", calls)
	}
}
