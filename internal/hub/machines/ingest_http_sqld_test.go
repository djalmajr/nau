//go:build sqld

package machines

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/store"
)

// TestSQLDInventoryHTTPBodyLimit is the real integration of the body
// limits: the hub server behind the real HTTP adapter (httpserve.New),
// the machine of the real pairing, the real parser and the real
// ingest.Apply over the real sqld, named by NAU_TEST_SQLD_URL. A small
// inventory and a valid 4 MiB inventory reach the store and persist,
// and the second apply is told from the first by the agent state, the
// hidden counts and the snapshot generated_ms; a body one byte over
// the 4 MiB limit answers 413 before the core and persists nothing.
func TestSQLDInventoryHTTPBodyLimit(t *testing.T) {
	// The parser's limit, as an independent literal of the test.
	const limit = 4 * 1024 * 1024
	st := hubtest.SQLD(t)
	ctx := context.Background()
	// The rows of the fixture go away at the end of the test, before
	// the helper releases the lock (the cleanups run last in, first
	// out), so the shared database never keeps them.
	t.Cleanup(func() {
		stmts := make([]store.Stmt, 0, len(sqldMachineTables))
		for _, table := range sqldMachineTables {
			stmts = append(stmts, store.Stmt{SQL: "DELETE FROM " + table})
		}
		if _, err := st.Batch(ctx, stmts); err != nil {
			t.Errorf("the cleanup of the rows: %v", err)
		}
	})
	s := freshSQLScenario(t, st)
	d, _ := s.deliver(t, "alice", "ingest-body-limit")

	// One working agent, with the state and the hidden counts as
	// parameters: the small control and the 4 MiB body must be told
	// apart by them, so an ignored second apply fails the test.
	doc := func(gen int64, state string, hidden string) []byte {
		return []byte(fmt.Sprintf(`{"schema":1,"soho_version":"v0.3.1","generated_ms":%d,"projects":[{"name":"acme/web","sessions":[{"id":"w1","agents":[{"terminal":"w1:p1","name":"agent-1","role":"implementer","kind":"cli-b","state":"%s","report_ready":false,"started_ms":1793372400000}]}]}],"hidden":%s}`, gen, state, hidden))
	}
	small := doc(1793376000000, "working", `{"working":2,"needs_you":0,"done":0,"idle":0,"unknown":0}`)
	big := doc(1793376000001, "idle", `{"working":0,"needs_you":0,"done":1,"idle":1,"unknown":0}`)
	if len(big) >= limit {
		t.Fatalf("document too large to pad: %d bytes", len(big))
	}
	// The second inventory padded with whitespace to exactly the
	// limit: still a valid document at the boundary.
	big = append(big, strings.Repeat(" ", limit-len(big))...)
	if len(big) != limit {
		t.Fatalf("padded document = %d bytes, want %d", len(big), limit)
	}
	over := append(append([]byte(nil), big...), ' ')

	h := httpserve.New(s.srv)
	srv := httptest.NewServer(httpserve.NewServer(h).Handler)
	defer srv.Close()

	// A client with a finite timeout: a regression that leaves the
	// body unread must not hang the proof without its own bound.
	client := &http.Client{Timeout: 30 * time.Second}
	post := func(body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/ingest", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build the request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+d.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("the request: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	const (
		qState  = `SELECT state FROM agents_current WHERE machine_id = ?`
		qHidden = `SELECT working, done, idle FROM hidden_counts WHERE machine_id = ?`
		qDeliv  = `SELECT COUNT(*) FROM ingest_deliveries WHERE machine_id = ?`
		qGen    = `SELECT generated_ms FROM inventory_snapshots WHERE machine_id = ?`
	)

	// The persisted state of the machine after the applies.
	agentState := func() string {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: qState, Args: []store.Value{d.MachineID}})
		if err != nil {
			t.Fatalf("the agent state: %v", err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("the agents = %d rows, want 1", len(res.Rows))
		}
		state, ok := res.Rows[0][0].(string)
		if !ok {
			t.Fatal("the agent state is not a string")
		}
		return state
	}
	hiddenCounts := func() (working, done, idle int64) {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: qHidden, Args: []store.Value{d.MachineID}})
		if err != nil {
			t.Fatalf("the hidden counts: %v", err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("the hidden rows = %d, want 1", len(res.Rows))
		}
		w, ok1 := res.Rows[0][0].(int64)
		n, ok2 := res.Rows[0][1].(int64)
		i, ok3 := res.Rows[0][2].(int64)
		if !ok1 || !ok2 || !ok3 {
			t.Fatal("the hidden counts are not integers")
		}
		return w, n, i
	}
	one := func(sql string) int64 {
		t.Helper()
		res, err := st.Execute(ctx, store.Stmt{SQL: sql, Args: []store.Value{d.MachineID}})
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", sql, len(res.Rows))
		}
		n, ok := res.Rows[0][0].(int64)
		if !ok {
			t.Fatalf("%s: the value is not an integer", sql)
		}
		return n
	}

	// The small control: the delivery applies and persists its state,
	// counts and generated_ms.
	resp := post(small)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("small: status = %d, want 204", resp.StatusCode)
	}
	if got := agentState(); got != "working" {
		t.Fatalf("small: the agent state = %q, want working", got)
	}
	if w, n, i := hiddenCounts(); w != 2 || n != 0 || i != 0 {
		t.Fatalf("small: the hidden counts = [working %d done %d idle %d], want [2 0 0]", w, n, i)
	}
	if g := one(qGen); g != 1793376000000 {
		t.Fatalf("small: the snapshot generated_ms = %d, want 1793376000000", g)
	}
	if n := one(qDeliv); n != 1 {
		t.Fatalf("small: the deliveries = %d, want 1", n)
	}

	// The valid body of exactly the limit: the adapter passes it, the
	// parser accepts it and the apply replaces the persisted state.
	resp = post(big)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("4 MiB: status = %d, want 204", resp.StatusCode)
	}
	if got := agentState(); got != "idle" {
		t.Fatalf("4 MiB: the agent state = %q, want idle (the second apply was ignored)", got)
	}
	if w, n, i := hiddenCounts(); w != 0 || n != 1 || i != 1 {
		t.Fatalf("4 MiB: the hidden counts = [working %d done %d idle %d], want [0 1 1] (the second apply was ignored)", w, n, i)
	}
	if g := one(qGen); g != 1793376000001 {
		t.Fatalf("4 MiB: the snapshot generated_ms = %d, want 1793376000001 (the second apply was ignored)", g)
	}
	if n := one(qDeliv); n != 2 {
		t.Fatalf("4 MiB: the deliveries = %d, want 2", n)
	}

	// One byte over the limit: the adapter answers 413 before the
	// core, with the API headers, and nothing persists: the persisted
	// state stays the one of the 4 MiB body.
	resp = post(over)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("4 MiB + 1: status = %d, want 413", resp.StatusCode)
	}
	for name, want := range map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Fatalf("4 MiB + 1: %s = %q, want %q", name, got, want)
		}
	}
	if got := agentState(); got != "idle" {
		t.Fatalf("4 MiB + 1: the agent state = %q, want idle (the refused body changed nothing)", got)
	}
	if w, n, i := hiddenCounts(); w != 0 || n != 1 || i != 1 {
		t.Fatalf("4 MiB + 1: the hidden counts = [working %d done %d idle %d], want [0 1 1] (the refused body changed nothing)", w, n, i)
	}
	if g := one(qGen); g != 1793376000001 {
		t.Fatalf("4 MiB + 1: the snapshot generated_ms = %d, want 1793376000001 (the refused body persisted nothing)", g)
	}
	if n := one(qDeliv); n != 2 {
		t.Fatalf("4 MiB + 1: the deliveries = %d, want 2 (the refused body persisted nothing)", n)
	}
}
