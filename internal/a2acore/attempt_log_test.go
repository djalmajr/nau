package a2acore

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/notify"
)

func TestDeliverAttemptLogOutcomes(t *testing.T) {
	for _, tc := range []struct {
		outcome, state string
		attempt        int
	}{
		{notify.OutcomeAccepted, DeliveryAccepted, 1}, {notify.OutcomeUncertain, DeliveryUncertain, 1},
		{notify.OutcomeRejected, DeliveryRejected, 1}, {notify.OutcomeTransient, DeliveryPending, 1}, {notify.OutcomeTransient, DeliveryExhausted, notify.MaxAttempts},
	} {
		t.Run(tc.state, func(t *testing.T) {
			var log bytes.Buffer
			c := deliverClaim(1, tc.attempt)
			c.StartedAt = testBase
			c.Job = "notice-text-canary-400"
			q := &fakeQueue{claims: []DeliveryClaim{c}}
			s := &fakeSender{fixed: notify.SendResult{Outcome: tc.outcome, Exit: 15, Category: "deadline"}}
			_, err := Deliver(context.Background(), q, s, func() time.Time { return testBase.Add(2175 * time.Millisecond) }, DeliverOptions{Concurrency: 1, Logger: slog.New(slog.NewJSONHandler(&log, nil))})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(log.String(), "\n") != 1 {
				t.Fatalf("logs=%q", log.String())
			}
			var fields map[string]any
			if err := json.Unmarshal(log.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			category := "deadline"
			if tc.state == DeliveryAccepted {
				category = ""
			}
			if fields["state"] != tc.state || fields["error"] != category || fields["exit"] != float64(15) || fields["ms"] != float64(2175) {
				t.Fatalf("fields=%+v", fields)
			}
			if strings.Contains(log.String(), c.Job) {
				t.Fatalf("notice text leaked: %s", log.String())
			}
		})
	}
}

func TestDeliverReleasedAttemptNoLog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var log bytes.Buffer
	q := &fakeQueue{claims: []DeliveryClaim{deliverClaim(1, 1)}}
	s := &fakeSender{}
	summary, err := Deliver(ctx, q, s, func() time.Time { return testBase }, DeliverOptions{Logger: slog.New(slog.NewJSONHandler(&log, nil))})
	if err != nil || summary.Deferred != 1 || log.Len() != 0 || len(s.snapshot()) != 0 || q.releasedCount() != 1 {
		t.Fatalf("release=%+v %v log=%q", summary, err, log.String())
	}
}
