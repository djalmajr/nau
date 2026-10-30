// Package sqld implements the hub storage over the Hrana protocol on
// HTTP (sqld), with only the standard library. The token is carried in
// the Authorization header only: it never appears in an error, a log or
// a response. The same goes for the server's own diagnostics (the
// message of an error result, the batch step errors and the body of a
// non-200 status): they are opaque and never reach an error, a log or a
// response.
package sqld

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/nau/internal/hub/store"
)

// pipelineSuffix is the Hrana pipeline path under the server base URL.
const pipelineSuffix = "/v2/pipeline"

// maxResponse is the limit of a pipeline response body: 16 MiB.
const maxResponse = 16 << 20

// pingSQL is the ping statement.
const pingSQL = "SELECT 1"

// Client is one Hrana-on-HTTP connection to a sqld server. It is
// stateless between calls and safe for concurrent use.
type Client struct {
	// Endpoint is the absolute URL of the pipeline (the base URL with
	// the /v2/pipeline suffix).
	Endpoint string
	// Token is the optional bearer token.
	Token string
	// http is the HTTP client; tests may replace the transport.
	http *http.Client
}

// New builds the client for the sqld base URL. The URL must use the
// http or https scheme; anything else is an error. Token may be empty.
func New(baseURL string, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("sqld: the URL does not parse")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("sqld: the URL must use http or https")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("sqld: the URL must name a host")
	}
	pipeline := *u
	pipeline.Path = strings.TrimSuffix(u.Path, "/") + pipelineSuffix
	pipeline.RawQuery = ""
	pipeline.Fragment = ""
	return &Client{
		Endpoint: pipeline.String(),
		Token:    token,
		http:     &http.Client{},
	}, nil
}

// Ping reports the database reachability with a SELECT 1.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Execute(ctx, store.Stmt{SQL: pingSQL})
	return err
}

// Execute runs one statement in its own pipeline.
func (c *Client) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	sj, err := newStmtJSON(stmt)
	if err != nil {
		return store.Result{}, err
	}
	reqs := []any{
		executeRequest{Type: "execute", Stmt: sj},
		closeRequest{Type: "close"},
	}
	raw, err := c.pipeline(ctx, reqs)
	if err != nil {
		return store.Result{}, err
	}
	resp := raw[0].Response
	if resp.Type != "execute" {
		return store.Result{}, errors.New("sqld: unexpected response type")
	}
	var wr wireResult
	if err := json.Unmarshal(resp.Result, &wr); err != nil {
		return store.Result{}, errors.New("sqld: malformed execute result")
	}
	return convertResult(wr)
}

// Batch runs the statements atomically in one batch pipeline: BEGIN,
// the statements chained on the previous step being ok, COMMIT on the
// last ok, and ROLLBACK when the COMMIT did not succeed. It reports the
// results of the statements; on an error nothing was applied.
func (c *Client) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	n := len(stmts)
	steps := make([]step, 0, len(stmts)+3)
	steps = append(steps, step{Stmt: stmtJSON{SQL: "BEGIN", Args: []any{}}})
	for i, s := range stmts {
		sj, err := newStmtJSON(s)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step{Condition: &condition{Type: "ok", Step: stepRef(i)}, Stmt: sj})
	}
	commitIdx := len(steps)
	steps = append(steps, step{Condition: &condition{Type: "ok", Step: stepRef(commitIdx - 1)}, Stmt: stmtJSON{SQL: "COMMIT", Args: []any{}}})
	steps = append(steps, step{
		Condition: &condition{Type: "not", Cond: &condition{Type: "ok", Step: stepRef(commitIdx)}},
		Stmt:      stmtJSON{SQL: "ROLLBACK", Args: []any{}},
	})
	reqs := []any{
		batchRequest{Type: "batch", Batch: batchJSON{Steps: steps}},
		closeRequest{Type: "close"},
	}
	raw, err := c.pipeline(ctx, reqs)
	if err != nil {
		return nil, err
	}
	resp := raw[0].Response
	if resp.Type != "batch" {
		return nil, errors.New("sqld: unexpected response type")
	}
	var batch struct {
		StepResults []json.RawMessage `json:"step_results"`
		StepErrors  []json.RawMessage `json:"step_errors"`
	}
	if err := json.Unmarshal(resp.Result, &batch); err != nil {
		return nil, errors.New("sqld: malformed batch result")
	}
	// The steps are: BEGIN (0), the n statements (1..n), COMMIT (n+1)
	// and ROLLBACK (n+2).
	if len(batch.StepResults) != n+3 || len(batch.StepErrors) != n+3 {
		return nil, fmt.Errorf("sqld: batch has %d steps, want %d", len(batch.StepResults), n+3)
	}
	// The first step error, if any, is the failure: everything after
	// it was skipped and nothing was applied. The error is opaque:
	// only the local step index and a well-formed code are reported,
	// never the server message.
	for i, e := range batch.StepErrors {
		if jsonNullOrMissing(e) {
			continue
		}
		var we wireError
		if err := json.Unmarshal(e, &we); err != nil {
			return nil, errors.New("sqld: malformed batch step error")
		}
		if i >= 1 && i <= n {
			return nil, fmt.Errorf("sqld: batch statement %d: server error%s", i, codeSuffix(we.Code))
		}
		return nil, fmt.Errorf("sqld: batch step %d: server error%s", i, codeSuffix(we.Code))
	}
	out := make([]store.Result, 0, n)
	for i := 1; i <= n; i++ {
		if jsonNullOrMissing(batch.StepResults[i]) {
			return nil, fmt.Errorf("sqld: batch step %d: no result", i)
		}
		var wr wireResult
		if err := json.Unmarshal(batch.StepResults[i], &wr); err != nil {
			return nil, fmt.Errorf("sqld: batch step %d: malformed result", i)
		}
		res, err := convertResult(wr)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// stepRef is a pointer to n, for the step indexes of the conditions.
func stepRef(n int) *int { return &n }

// condition is one Hrana batch step condition.
type condition struct {
	Type string     `json:"type"`
	Step *int       `json:"step,omitempty"`
	Cond *condition `json:"cond,omitempty"`
}

// stmtJSON is one Hrana statement.
type stmtJSON struct {
	SQL  string `json:"sql"`
	Args []any  `json:"args"`
}

// newStmtJSON converts one store statement to its wire form, encoding
// each argument as a Hrana value. An empty argument list stays an empty
// list, never null.
func newStmtJSON(s store.Stmt) (stmtJSON, error) {
	args := make([]any, 0, len(s.Args))
	for _, a := range s.Args {
		enc, err := encodeValue(a)
		if err != nil {
			return stmtJSON{}, err
		}
		args = append(args, enc)
	}
	return stmtJSON{SQL: s.SQL, Args: args}, nil
}

// encodeValue converts one store value to its Hrana wire form. Only the
// five value types of the store contract are accepted; anything else is
// an error, never a silently encoded value.
func encodeValue(v store.Value) (map[string]any, error) {
	switch t := v.(type) {
	case nil:
		return map[string]any{"type": "null"}, nil
	case int64:
		return map[string]any{"type": "integer", "value": strconv.FormatInt(t, 10)}, nil
	case float64:
		return map[string]any{"type": "float", "value": t}, nil
	case string:
		return map[string]any{"type": "text", "value": t}, nil
	case []byte:
		return map[string]any{"type": "blob", "base64": base64.StdEncoding.EncodeToString(t)}, nil
	default:
		return nil, fmt.Errorf("sqld: unsupported argument type %T", v)
	}
}

// step is one Hrana batch step; the first step has no condition.
type step struct {
	Condition *condition `json:"condition,omitempty"`
	Stmt      stmtJSON   `json:"stmt"`
}

type executeRequest struct {
	Type string   `json:"type"`
	Stmt stmtJSON `json:"stmt"`
}

type batchJSON struct {
	Steps []step `json:"steps"`
}

type batchRequest struct {
	Type  string    `json:"type"`
	Batch batchJSON `json:"batch"`
}

type closeRequest struct {
	Type string `json:"type"`
}

type pipelineRequest struct {
	Baton    *string `json:"baton"`
	Requests []any   `json:"requests"`
}

// wireValue is one Hrana value on the wire.
type wireValue struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value"`
	Base64 string          `json:"base64"`
}

type wireCol struct {
	Name string `json:"name"`
}

// wireResult is one Hrana execute result (and one batch step result).
type wireResult struct {
	Cols             []wireCol     `json:"cols"`
	Rows             [][]wireValue `json:"rows"`
	AffectedRowCount int64         `json:"affected_row_count"`
	LastInsertRowID  *string       `json:"last_insert_rowid"`
}

// wireError is the error part of a Hrana item. Only the code is kept
// (gated by hranaCodePattern); the message is an opaque server
// diagnostic and is never read into an error.
type wireError struct {
	Code string `json:"code"`
}

// hranaCodePattern is the shape of a Hrana error code that may be
// echoed in a local error: an uppercase identifier of at most 64
// characters. Anything else is omitted, so no server text of an
// uncontrolled shape reaches the error.
var hranaCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// codeSuffix builds the " (code ...)" part of an error from a Hrana
// code; an empty or malformed code yields no suffix.
func codeSuffix(code string) string {
	if !hranaCodePattern.MatchString(code) {
		return ""
	}
	return " (code " + code + ")"
}

type wireResponse struct {
	Type   string          `json:"type"`
	Result json.RawMessage `json:"result"`
}

type wireItem struct {
	Type     string        `json:"type"`
	Response *wireResponse `json:"response"`
	Error    *wireError    `json:"error"`
}

type wirePipeline struct {
	Baton   *string    `json:"baton"`
	BaseURL *string    `json:"base_url"`
	Results []wireItem `json:"results"`
}

// pipeline runs one Hrana pipeline against the server and returns the
// raw items of the response, in request order.
func (c *Client) pipeline(ctx context.Context, reqs []any) ([]wireItem, error) {
	body, err := json.Marshal(pipelineRequest{Baton: nil, Requests: reqs})
	if err != nil {
		return nil, fmt.Errorf("sqld: encode pipeline: %v", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("sqld: the request could not be built")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		// The transport error can quote a malformed header line from the
		// server or the configured URL: only a fixed text is shown, and
		// the cause stays reachable through errors.Is and errors.As.
		if ctx.Err() != nil {
			return nil, opaqueError{"sqld: the request was canceled or timed out", err}
		}
		return nil, opaqueError{"sqld: the request failed", err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body is an opaque server diagnostic: it is dropped, not
		// echoed, so no content of it reaches an error message.
		return nil, fmt.Errorf("sqld: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		// A read error can quote a malformed trailer from the server.
		return nil, opaqueError{"sqld: the response could not be read", err}
	}
	if len(raw) > maxResponse {
		return nil, fmt.Errorf("sqld: response larger than %d bytes", maxResponse)
	}
	var p wirePipeline
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, errors.New("sqld: malformed response")
	}
	if len(p.Results) != len(reqs) {
		return nil, fmt.Errorf("sqld: response has %d results for %d requests", len(p.Results), len(reqs))
	}
	for i, item := range p.Results {
		if item.Type == "error" {
			// The error result is opaque: only the request index and a
			// well-formed code are reported, never its message.
			code := ""
			if item.Error != nil {
				code = item.Error.Code
			}
			return nil, fmt.Errorf("sqld: request %d: server error%s", i, codeSuffix(code))
		}
		if item.Type != "ok" || item.Response == nil {
			return nil, fmt.Errorf("sqld: request %d: unexpected result type", i)
		}
	}
	return p.Results, nil
}

// jsonNullOrMissing reports whether the raw message is a JSON null or
// absent.
func jsonNullOrMissing(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// convertResult converts one wire result to a store result.
func convertResult(wr wireResult) (store.Result, error) {
	res := store.Result{Affected: wr.AffectedRowCount}
	for _, c := range wr.Cols {
		res.Cols = append(res.Cols, c.Name)
	}
	if wr.LastInsertRowID != nil {
		n, err := strconv.ParseInt(*wr.LastInsertRowID, 10, 64)
		if err != nil {
			return store.Result{}, errors.New("sqld: malformed last_insert_rowid")
		}
		res.LastInsertRowID = n
	}
	for _, row := range wr.Rows {
		r := make(store.Row, 0, len(row))
		for _, v := range row {
			val, err := decodeValue(v)
			if err != nil {
				return store.Result{}, err
			}
			r = append(r, val)
		}
		res.Rows = append(res.Rows, r)
	}
	return res, nil
}

// decodeValue converts one wire value to a store value. An unknown type
// fails the whole result: a response the client does not understand is
// rejected, never partially read.
func decodeValue(wv wireValue) (store.Value, error) {
	switch wv.Type {
	case "null":
		return nil, nil
	case "integer":
		var s string
		if err := json.Unmarshal(wv.Value, &s); err != nil {
			return nil, errors.New("sqld: malformed integer value")
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, errors.New("sqld: malformed integer value")
		}
		return n, nil
	case "float":
		var f float64
		if err := json.Unmarshal(wv.Value, &f); err != nil {
			return nil, errors.New("sqld: malformed float value")
		}
		return f, nil
	case "text":
		var s string
		if err := json.Unmarshal(wv.Value, &s); err != nil {
			return nil, errors.New("sqld: malformed text value")
		}
		return s, nil
	case "blob":
		b, err := base64.StdEncoding.DecodeString(wv.Base64)
		if err != nil {
			return nil, errors.New("sqld: malformed blob value")
		}
		return b, nil
	default:
		return nil, errors.New("sqld: unknown value type")
	}
}

// opaqueError shows a fixed local text and keeps the cause only for
// errors.Is and errors.As: the cause may quote what the server sent.
type opaqueError struct {
	msg   string
	cause error
}

func (e opaqueError) Error() string { return e.msg }

func (e opaqueError) Unwrap() error { return e.cause }
