//go:build sqld && webcontract

package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/httpserve"
	"github.com/djalmajr/nau/internal/hub/hubtest"
)

// TestWebReadContract checks observed responses, not a second schema. The caller
// must supply a verified, owned disposable database: SQLD resets its schema.
func TestWebReadContract(t *testing.T) {
	u, err := url.Parse(os.Getenv("NAU_TEST_SQLD_URL"))
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || os.Getenv("NAU_TEST_SQLD_TOKEN") != "" {
		t.Fatal("webcontract requires an owned disposable loopback HTTP database with an explicit port and no token")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("webcontract requires installed Bun")
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	compiler := filepath.Join(web, "node_modules/typescript/bin/tsc")
	if _, err := os.Stat(compiler); err != nil {
		t.Fatal("webcontract requires installed TypeScript")
	}
	st := hubtest.SQLD(t)
	s := freshSQLScenario(t, st)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	s.ctx = ctx
	d, _ := s.deliver(t, "alice", "laptop-a")
	h := httpserve.New(s.srv)
	var source strings.Builder
	module, _ := json.Marshal(filepath.ToSlash(filepath.Join(web, "src/lib/api.ts")))
	fmt.Fprintf(&source, "import type * as API from %s;\ntype ReadonlyView<T> = { readonly [K in keyof T]: ReadonlyView<T[K]> };\n", module)
	index := 0
	observe := func(path, target string) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "nau_session", Value: s.cookies["alice"]})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("GET %s: status %d or invalid JSON", path, rec.Code)
		}
		fmt.Fprintf(&source, "const observed%d = %s as const;\nexport const checked%d: ReadonlyView<API.%s> = observed%d;\n", index, rec.Body.Bytes(), index, target, index)
		index++
		return rec.Body.Bytes()
	}
	observe("/api/machines", "MachinesResponse") // last_seen_ms is absent data before ingest.
	for phase := 0; phase < 2; phase++ {
		body := sharingInventoryBody(s.nowMS())
		if phase == 1 {
			body = []byte(strings.ReplaceAll(string(body), `"state":"working"`, `"state":"idle"`))
		}
		if r := s.ingestWith(d.Key, body); r.Status != 204 {
			t.Fatalf("ingest phase %d: status %d", phase, r.Status)
		}
		agents := parseAgents(t, observe("/api/agents", "AgentsResponse"))
		if len(agents.Agents) != 2 || len(agents.Hidden) == 0 {
			t.Fatal("fixture must populate agents and hidden counts")
		}
		for _, terminal := range []string{"w1:p1", "w1:p2"} {
			raw := observe("/api/agents/"+d.MachineID+"."+terminal, "AgentDetailResponse")
			var detail struct {
				History []json.RawMessage `json:"history"`
			}
			if err := json.Unmarshal(raw, &detail); err != nil || len(detail.History) == 0 {
				t.Fatal("fixture must populate history")
			}
		}
		observe("/api/overview", "OverviewResponse")
		observe("/api/machines", "MachinesResponse")
		observe("/api/developers", "DevelopersResponse")
		s.advance(time.Minute)
	}
	observe("/api/version", "VersionResponse")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "observed.ts"), []byte(source.String()), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]interface{}{"extends": filepath.Join(web, "tsconfig.json"), "include": []string{"observed.ts"}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "tsconfig.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bun, compiler, "--noEmit", "--project", configPath)
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("observed read responses do not satisfy web consumer types: %v\n%s", err, output)
	}
}
