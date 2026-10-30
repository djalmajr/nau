package sqld

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

const sentinelToken = "sekret-token-0123456789"

// fakeServer is an httptest server that speaks the pipeline protocol:
// it records the requests and answers with the scripted results, or
// with a fixed status and body when status is set.
type fakeServer struct {
	t       *testing.T
	srv     *httptest.Server
	status  int
	body    []byte
	handler func(req *requestWire) []itemWire
	auth    string
	lastRaw string
}

type requestWire struct {
	Baton    *json.RawMessage  `json:"baton"`
	Requests []json.RawMessage `json:"requests"`
}

type itemWire struct {
	Type     string        `json:"type"`
	Response *responseWire `json:"response,omitempty"`
	Error    *errorWire    `json:"error,omitempty"`
}

type responseWire struct {
	Type   string          `json:"type"`
	Result json.RawMessage `json:"result"`
}

type errorWire struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// okExecute builds one ok execute item with the raw result JSON.
func okExecute(result string) itemWire {
	return itemWire{Type: "ok", Response: &responseWire{Type: "execute", Result: json.RawMessage(result)}}
}

// okBatch builds one ok batch item with the raw result JSON.
func okBatch(result string) itemWire {
	return itemWire{Type: "ok", Response: &responseWire{Type: "batch", Result: json.RawMessage(result)}}
}

func okClose() itemWire {
	return itemWire{Type: "ok", Response: &responseWire{Type: "close", Result: json.RawMessage(`{}`)}}
}

func errItem(message, code string) itemWire {
	return itemWire{Type: "error", Error: &errorWire{Message: message, Code: code}}
}

func newFakeServer(t *testing.T, handler func(req *requestWire) []itemWire) *fakeServer {
	t.Helper()
	f := &fakeServer{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		f.lastRaw = string(raw)
		if r.URL.Path != "/v2/pipeline" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
			if f.body != nil {
				_, _ = w.Write(f.body)
			}
			return
		}
		var req requestWire
		if err := json.Unmarshal(raw, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		results := handler(&req)
		body, _ := json.Marshal(struct {
			Baton   *string    `json:"baton"`
			BaseURL *string    `json:"base_url"`
			Results []itemWire `json:"results"`
		}{Baton: nil, BaseURL: nil, Results: results})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// clientFor builds a client over the fake server with the token.
func (f *fakeServer) clientFor(t *testing.T, token string) *Client {
	t.Helper()
	c, err := New(f.srv.URL, token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// assertPipelineEndedWithClose requires the last request of the last
// raw pipeline to be a close.
func (f *fakeServer) assertPipelineEndedWithClose(t *testing.T) {
	t.Helper()
	var req requestWire
	if err := json.Unmarshal([]byte(f.lastRaw), &req); err != nil {
		t.Fatalf("last pipeline did not parse: %v", err)
	}
	if len(req.Requests) == 0 {
		t.Fatal("the pipeline had no requests")
	}
	last := string(req.Requests[len(req.Requests)-1])
	var closeReq struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(last), &closeReq); err != nil {
		t.Fatalf("last request did not parse: %v", err)
	}
	if closeReq.Type != "close" {
		t.Fatalf("last request type = %q, want close", closeReq.Type)
	}
}

func TestExecuteValueTypes(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		if len(req.Requests) != 2 {
			t.Fatalf("the pipeline has %d requests, want 2", len(req.Requests))
		}
		var exec struct {
			Type string `json:"type"`
			Stmt struct {
				SQL  string `json:"sql"`
				Args []any  `json:"args"`
			} `json:"stmt"`
		}
		if err := json.Unmarshal(req.Requests[0], &exec); err != nil {
			t.Fatalf("first request did not parse: %v", err)
		}
		if exec.Type != "execute" || exec.Stmt.SQL != "SELECT v" {
			t.Fatalf("first request = %+v", exec)
		}
		return []itemWire{
			okExecute(`{"cols":[{"name":"i","decltype":null},{"name":"f","decltype":null},{"name":"s","decltype":null},{"name":"b","decltype":null},{"name":"n","decltype":null}],"rows":[[{"type":"integer","value":"42"},{"type":"float","value":1.5},{"type":"text","value":"olá"},{"type":"blob","base64":"AP8="},{"type":"null"}]],"affected_row_count":0,"last_insert_rowid":null}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	res, err := c.Execute(context.Background(), stmtForTest("SELECT v", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	f.assertPipelineEndedWithClose(t)
	if f.auth != "" {
		t.Fatalf("Authorization = %q, want absent", f.auth)
	}
	if len(res.Cols) != 5 || res.Cols[0] != "i" || res.Cols[4] != "n" {
		t.Fatalf("cols = %v", res.Cols)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %v", res.Rows)
	}
	row := res.Rows[0]
	if v, ok := row[0].(int64); !ok || v != 42 {
		t.Fatalf("row[0] = %v (%T), want int64 42", row[0], row[0])
	}
	if v, ok := row[1].(float64); !ok || v != 1.5 {
		t.Fatalf("row[1] = %v (%T), want float64 1.5", row[1], row[1])
	}
	if v, ok := row[2].(string); !ok || v != "olá" {
		t.Fatalf("row[2] = %v (%T), want string olá", row[2], row[2])
	}
	b, ok := row[3].([]byte)
	if !ok || len(b) != 2 || b[0] != 0x00 || b[1] != 0xff {
		t.Fatalf("row[3] = %v (%T), want []byte 00ff", row[3], row[3])
	}
	if row[4] != nil {
		t.Fatalf("row[4] = %v, want nil", row[4])
	}
}

func TestExecuteLastInsertRowID(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okExecute(`{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"7"}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	res, err := c.Execute(context.Background(), stmtForTest("INSERT", nil))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.LastInsertRowID != 7 {
		t.Fatalf("LastInsertRowID = %d, want 7", res.LastInsertRowID)
	}
	if res.Affected != 1 {
		t.Fatalf("Affected = %d, want 1", res.Affected)
	}
}

func TestExecuteArgsEncodedAsWireValues(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		var exec struct {
			Type string `json:"type"`
			Stmt struct {
				SQL  string            `json:"sql"`
				Args []json.RawMessage `json:"args"`
			} `json:"stmt"`
		}
		if err := json.Unmarshal(req.Requests[0], &exec); err != nil {
			t.Fatalf("first request did not parse: %v", err)
		}
		want := []string{
			`{"type":"integer","value":"1"}`,
			`{"type":"text","value":"t"}`,
			`{"type":"float","value":1.5}`,
			`{"base64":"AA==","type":"blob"}`,
			`{"type":"null"}`,
		}
		if len(exec.Stmt.Args) != len(want) {
			t.Fatalf("args = %v, want %d values", exec.Stmt.Args, len(want))
		}
		for i, w := range want {
			if string(exec.Stmt.Args[i]) != w {
				t.Fatalf("arg %d = %s, want %s", i, exec.Stmt.Args[i], w)
			}
		}
		return []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":null}`), okClose()}
	})
	c := f.clientFor(t, "")
	if _, err := c.Execute(context.Background(), stmtForTest("INSERT", []any{int64(1), "t", 1.5, []byte{0}, nil})); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestExecuteEmptyArgsAreAnEmptyList(t *testing.T) {
	var argsSeen json.RawMessage
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		var exec struct {
			Stmt struct {
				Args json.RawMessage `json:"args"`
			} `json:"stmt"`
		}
		if err := json.Unmarshal(req.Requests[0], &exec); err != nil {
			t.Fatalf("first request did not parse: %v", err)
		}
		argsSeen = exec.Stmt.Args
		return []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}
	})
	c := f.clientFor(t, "")
	if _, err := c.Execute(context.Background(), stmtForTest("SELECT", nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(argsSeen) != "[]" {
		t.Fatalf("args = %s, want []", argsSeen)
	}
}

func TestExecuteRejectsUnsupportedArgType(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		t.Fatal("the request must not reach the server")
		return nil
	})
	c := f.clientFor(t, "")
	_, err := c.Execute(context.Background(), stmtForTest("INSERT", []any{3}))
	if err == nil {
		t.Fatal("Execute: want an error for the int argument")
	}
	if !strings.Contains(err.Error(), "unsupported argument type") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteServerError(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{errItem("SQLite error: no such table: t", "SQLITE_UNKNOWN"), okClose()}
	})
	c := f.clientFor(t, sentinelToken)
	_, err := c.Execute(context.Background(), stmtForTest("SELECT * FROM t", nil))
	if err == nil {
		t.Fatal("Execute: want an error")
	}
	// The error names the local request index and the well-formed
	// code, and none of the server text (message or token).
	if !strings.Contains(err.Error(), "request 0") {
		t.Fatalf("error = %v, want the request index", err)
	}
	if !strings.Contains(err.Error(), "server error") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "code SQLITE_UNKNOWN") {
		t.Fatalf("error = %v, want the well-formed code", err)
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Fatalf("the server message leaked into the error: %v", err)
	}
	if strings.Contains(err.Error(), sentinelToken) {
		t.Fatalf("the token leaked into the error: %v", err)
	}
}

func TestExecuteErrorDropsMalformedCode(t *testing.T) {
	for _, code := range []string{"", "lower-case", "1CODE", "HAS-DASH", strings.Repeat("A", 65), "CODE WITH SPACE"} {
		f := newFakeServer(t, func(req *requestWire) []itemWire {
			return []itemWire{errItem("diagnostic "+code, code), okClose()}
		})
		c := f.clientFor(t, "")
		_, err := c.Execute(context.Background(), stmtForTest("SELECT 1", nil))
		if err == nil {
			t.Fatal("Execute: want an error")
		}
		if strings.Contains(err.Error(), "(code") {
			t.Fatalf("code %q: a malformed code reached the error: %v", code, err)
		}
		if strings.Contains(err.Error(), "diagnostic") {
			t.Fatalf("code %q: the server message leaked into the error: %v", code, err)
		}
	}
}

func TestPingErrorDropsServerMessage(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{errItem("Bearer "+sentinelToken+" was rejected", "SQLITE_ERROR"), okClose()}
	})
	c := f.clientFor(t, sentinelToken)
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping: want an error")
	}
	if strings.Contains(err.Error(), sentinelToken) {
		t.Fatalf("the token leaked into the ping error: %v", err)
	}
	if strings.Contains(err.Error(), "rejected") {
		t.Fatalf("the server message leaked into the ping error: %v", err)
	}
	if !strings.Contains(err.Error(), "code SQLITE_ERROR") {
		t.Fatalf("error = %v, want the well-formed code", err)
	}
}

func TestExecuteNon200IsAnErrorWithoutBody(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		t.Fatal("the response is scripted by status, not by handler")
		return nil
	})
	f.status = 500
	f.body = []byte("server secret diagnostic")
	c := f.clientFor(t, "")
	_, err := c.Execute(context.Background(), stmtForTest("SELECT 1", nil))
	if err == nil {
		t.Fatal("Execute: want an error")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "server secret diagnostic") {
		t.Fatalf("the response body leaked into the error: %v", err)
	}
}

func TestResponseOverTheLimitIsAnError(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return nil
	})
	f.status = http.StatusOK
	f.body = bytesOverLimit()
	c := f.clientFor(t, "")
	_, err := c.Execute(context.Background(), stmtForTest("SELECT 1", nil))
	if err == nil {
		t.Fatal("Execute: want an error for the oversized response")
	}
	if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error = %v", err)
	}
}

func TestBearerTokenIsSentWhenSet(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}
	})
	c := f.clientFor(t, sentinelToken)
	if _, err := c.Execute(context.Background(), stmtForTest("SELECT 1", nil)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.auth != "Bearer "+sentinelToken {
		t.Fatalf("Authorization = %q, want the bearer token", f.auth)
	}
}

func TestPingSucceeds(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		var exec struct {
			Stmt struct {
				SQL string `json:"sql"`
			} `json:"stmt"`
		}
		if err := json.Unmarshal(req.Requests[0], &exec); err != nil {
			t.Fatalf("first request did not parse: %v", err)
		}
		if exec.Stmt.SQL != "SELECT 1" {
			t.Fatalf("ping SQL = %q, want SELECT 1", exec.Stmt.SQL)
		}
		return []itemWire{okExecute(`{"cols":[{"name":"1","decltype":null}],"rows":[[{"type":"integer","value":"1"}]],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}
	})
	c := f.clientFor(t, "")
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingFailsOnError(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{errItem("connection lost", "SQLITE_ERROR"), okClose()}
	})
	c := f.clientFor(t, "")
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("Ping: want an error")
	}
}

func TestBatchAtomicShapeAndResults(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		if len(req.Requests) != 2 {
			t.Fatalf("the pipeline has %d requests, want 2", len(req.Requests))
		}
		var batch struct {
			Type  string `json:"type"`
			Batch struct {
				Steps []struct {
					Condition *struct {
						Type string `json:"type"`
						Step *int   `json:"step"`
						Cond *struct {
							Type string `json:"type"`
							Step *int   `json:"step"`
						} `json:"cond"`
					} `json:"condition"`
					Stmt struct {
						SQL string `json:"sql"`
					} `json:"stmt"`
				} `json:"steps"`
			} `json:"batch"`
		}
		if err := json.Unmarshal(req.Requests[0], &batch); err != nil {
			t.Fatalf("batch request did not parse: %v", err)
		}
		if batch.Type != "batch" {
			t.Fatalf("request type = %q, want batch", batch.Type)
		}
		steps := batch.Batch.Steps
		if len(steps) != 5 {
			t.Fatalf("steps = %d, want 5 (BEGIN, 2 statements, COMMIT, ROLLBACK)", len(steps))
		}
		check := func(i int, sql, condType string, step *int, condCond bool, condStep *int) {
			if steps[i].Stmt.SQL != sql {
				t.Fatalf("step %d SQL = %q, want %q", i, steps[i].Stmt.SQL, sql)
			}
			if i == 0 {
				if steps[i].Condition != nil {
					t.Fatalf("step 0 has a condition, want none")
				}
				return
			}
			cond := steps[i].Condition
			if cond == nil || cond.Type != condType {
				t.Fatalf("step %d condition = %+v, want type %q", i, cond, condType)
			}
			if condType == "not" {
				if cond.Cond == nil || cond.Cond.Type != "ok" || cond.Cond.Step == nil || *cond.Cond.Step != *condStep {
					t.Fatalf("step %d not-condition = %+v, want not(ok step %d)", i, cond.Cond, *condStep)
				}
				return
			}
			if cond.Step == nil || *cond.Step != *step {
				t.Fatalf("step %d condition step = %+v, want %d", i, cond.Step, *step)
			}
		}
		check(0, "BEGIN", "", nil, false, nil)
		check(1, "INSERT INTO t VALUES (1)", "ok", intPtr(0), false, nil)
		check(2, "INSERT INTO t VALUES (2)", "ok", intPtr(1), false, nil)
		check(3, "COMMIT", "ok", intPtr(2), false, nil)
		check(4, "ROLLBACK", "not", nil, true, intPtr(3))
		return []itemWire{
			okBatch(`{"step_results":[
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"1"},
				{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"2"},
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}
			],"step_errors":[null,null,null,null,null]}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	stmts := []store.Stmt{
		{SQL: "INSERT INTO t VALUES (1)"},
		{SQL: "INSERT INTO t VALUES (2)"},
	}
	res, err := c.Batch(context.Background(), stmts)
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	f.assertPipelineEndedWithClose(t)
	if len(res) != 2 {
		t.Fatalf("results = %d, want 2 (the user statements)", len(res))
	}
	if res[0].LastInsertRowID != 1 || res[1].LastInsertRowID != 2 {
		t.Fatalf("results = %+v", res)
	}
}

func TestBatchFailureReportsTheFailedStatement(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okBatch(`{"step_results":[
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"1"},
				null,
				null,
				null,
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}
			],"step_errors":[
				null,
				null,
				{"message":"SQLite error: no such table: nosuchtable","code":"SQLITE_UNKNOWN"},
				null,
				null,
				null
			]}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	stmts := []store.Stmt{
		{SQL: "INSERT INTO t VALUES (1)"},
		{SQL: "INSERT INTO nosuchtable VALUES (1)"},
		{SQL: "INSERT INTO t VALUES (2)"},
	}
	_, err := c.Batch(context.Background(), stmts)
	if err == nil {
		t.Fatal("Batch: want an error")
	}
	if !strings.Contains(err.Error(), "statement 2") {
		t.Fatalf("error = %v, want the failed statement named", err)
	}
	if !strings.Contains(err.Error(), "code SQLITE_UNKNOWN") {
		t.Fatalf("error = %v, want the well-formed code", err)
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Fatalf("the server message leaked into the error: %v", err)
	}
}

func TestBatchFailureAtAControlStep(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okBatch(`{"step_results":[
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				null, null, null
			],"step_errors":[
				{"message":"begin failed","code":"SQLITE_BUSY"},
				null, null, null
			]}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	_, err := c.Batch(context.Background(), []store.Stmt{{SQL: "INSERT INTO t VALUES (1)"}})
	if err == nil {
		t.Fatal("Batch: want an error")
	}
	if !strings.Contains(err.Error(), "step 0") {
		t.Fatalf("error = %v, want the control step named", err)
	}
	if strings.Contains(err.Error(), "begin failed") {
		t.Fatalf("the server message leaked into the error: %v", err)
	}
}

func TestBatchSkippedStepWithoutErrorIsRejected(t *testing.T) {
	// A step result that is null while its error is null is a response
	// the client cannot trust: it is rejected.
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okBatch(`{"step_results":[
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				null,
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}
			],"step_errors":[null,null,null,null]}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	stmts := []store.Stmt{{SQL: "INSERT INTO t VALUES (1)"}}
	if _, err := c.Batch(context.Background(), stmts); err == nil {
		t.Fatal("Batch: want an error for the missing step result")
	}
}

func TestBatchWrongStepCountIsRejected(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okBatch(`{"step_results":[
				{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null},
				{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"1"}
			],"step_errors":[null,null]}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	stmts := []store.Stmt{{SQL: "INSERT INTO t VALUES (1)"}}
	if _, err := c.Batch(context.Background(), stmts); err == nil {
		t.Fatal("Batch: want an error for the wrong step count")
	}
}

func TestPipelineResultCountMismatchIsRejected(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}`)}
	})
	c := f.clientFor(t, "")
	if _, err := c.Execute(context.Background(), stmtForTest("SELECT 1", nil)); err == nil {
		t.Fatal("Execute: want an error for the missing close result")
	}
}

func TestUnknownValueTypeRejectsTheResult(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okExecute(`{"cols":[{"name":"p","decltype":null}],"rows":[[{"type":"point","value":"x"}]],"affected_row_count":0,"last_insert_rowid":null}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	if _, err := c.Execute(context.Background(), stmtForTest("SELECT p", nil)); err == nil {
		t.Fatal("Execute: want an error for the unknown value type")
	}
}

func TestBadIntegerRejectsTheResult(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		return []itemWire{
			okExecute(`{"cols":[{"name":"i","decltype":null}],"rows":[[{"type":"integer","value":"42x"}]],"affected_row_count":0,"last_insert_rowid":null}`),
			okClose(),
		}
	})
	c := f.clientFor(t, "")
	if _, err := c.Execute(context.Background(), stmtForTest("SELECT i", nil)); err == nil {
		t.Fatal("Execute: want an error for the bad integer")
	}
}

func TestContextDeadlineStopsTheCall(t *testing.T) {
	f := newFakeServer(t, func(req *requestWire) []itemWire {
		time.Sleep(300 * time.Millisecond)
		return []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}
	})
	c := f.clientFor(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Execute(ctx, stmtForTest("SELECT 1", nil))
	if err == nil {
		t.Fatal("Execute: want an error for the deadline")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("the call was not stopped by the deadline: %s", time.Since(start))
	}
}

func TestNewURLValidation(t *testing.T) {
	for _, tc := range []struct {
		url  string
		ok   bool
		want string
	}{
		{url: "postgres://h", ok: false},
		{url: "ftp://h", ok: false},
		{url: "http://", ok: false},
		{url: "http://:0", ok: false},
		{url: "https://:8443", ok: false},
		{url: "not a url", ok: false},
		{url: "http://127.0.0.1:8080", ok: true, want: "http://127.0.0.1:8080/v2/pipeline"},
		{url: "http://[::1]:8080", ok: true, want: "http://[::1]:8080/v2/pipeline"},
		{url: "https://h.example/sqld/", ok: true, want: "https://h.example/sqld/v2/pipeline"},
	} {
		_, err := New(tc.url, "")
		if tc.ok && err != nil {
			t.Fatalf("New(%q): %v", tc.url, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("New(%q): want an error", tc.url)
		}
		if tc.ok {
			c, _ := New(tc.url, "")
			if c.Endpoint != tc.want {
				t.Fatalf("Endpoint = %q, want %q", c.Endpoint, tc.want)
			}
		}
	}
}

// stmtForTest builds one store statement.
func stmtForTest(sql string, args []any) store.Stmt {
	vals := make([]store.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a)
	}
	return store.Stmt{SQL: sql, Args: vals}
}

func intPtr(n int) *int { return &n }

// bytesOverLimit builds maxResponse+1 bytes.
func bytesOverLimit() []byte {
	b := make([]byte, maxResponse+1)
	for i := range b {
		b[i] = 'x'
	}
	return b
}

// TestErrorsCarryNoServerText: every field the server controls (result
// and response types, value types, values, the row id, malformed
// results) can echo the bearer token back; none of it reaches the error.
func TestErrorsCarryNoServerText(t *testing.T) {
	tok := sentinelToken
	cases := []struct {
		name  string
		items []itemWire
	}{
		{"result type", []itemWire{{Type: "ok-" + tok}, okClose()}},
		{"response type", []itemWire{{Type: "ok", Response: &responseWire{Type: "execute-" + tok, Result: json.RawMessage(`{}`)}}, okClose()}},
		{"value type", []itemWire{okExecute(`{"cols":[{"name":"p","decltype":null}],"rows":[[{"type":"` + tok + `","value":"x"}]],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}},
		{"integer value", []itemWire{okExecute(`{"cols":[{"name":"i","decltype":null}],"rows":[[{"type":"integer","value":"` + tok + `"}]],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}},
		{"float value", []itemWire{okExecute(`{"cols":[{"name":"f","decltype":null}],"rows":[[{"type":"float","value":"` + tok + `"}]],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}},
		{"blob value", []itemWire{okExecute(`{"cols":[{"name":"b","decltype":null}],"rows":[[{"type":"blob","base64":"` + tok + `!"}]],"affected_row_count":0,"last_insert_rowid":null}`), okClose()}},
		{"row id", []itemWire{okExecute(`{"cols":[],"rows":[],"affected_row_count":1,"last_insert_rowid":"` + tok + `"}`), okClose()}},
		{"malformed result", []itemWire{okExecute(`{"cols":"` + tok + `"}`), okClose()}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			f := newFakeServer(t, func(req *requestWire) []itemWire { return c.items })
			cl := f.clientFor(t, tok)
			_, err := cl.Execute(context.Background(), stmtForTest("SELECT 1", nil))
			if err == nil {
				t.Fatal("Execute: want an error")
			}
			if strings.Contains(err.Error(), tok) {
				t.Fatalf("the token leaked into the error: %v", err)
			}
		})
	}
}
