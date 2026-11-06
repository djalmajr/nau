package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixturePolicy = `{"coauthor_pattern":"^Co-authored-by:.*Fixture Automaton","signature_pattern":"^Generated with Fixture Engine[.!]?$"}`

func gitFixture(t *testing.T) (string, func(string, ...string) string) {
	t.Helper()
	dir := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + dir, "USERPROFILE=" + dir, "XDG_CONFIG_HOME=" + dir, "TMPDIR=" + dir, "PATH=" + filepath.Dir(git), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_AUTHOR_NAME=Fixture Human", "GIT_AUTHOR_EMAIL=human@example.invalid", "GIT_COMMITTER_NAME=Fixture Human", "GIT_COMMITTER_EMAIL=human@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z"}
	call := func(input string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, git, args...)
		c.Dir = dir
		c.Env = env
		c.WaitDelay = time.Second
		c.Stdin = strings.NewReader(input)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	call("", "init", "--quiet")
	call("", "config", "commit.gpgsign", "false")
	call("", "commit", "--allow-empty", "-m", "base")
	return dir, call
}

func eventFile(t *testing.T, dir, base, head, title, body string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pull_request": map[string]any{"title": title, "body": body, "base": map[string]string{"sha": base}, "head": map[string]string{"sha": head}}})
	p := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckGitAndPR(t *testing.T) {
	for _, tc := range []struct {
		name, msg, title, body string
		want                   int
	}{
		{"clean", "Use Fixture Engine for conversion\n\nCo-authored-by: Fixture Human <human@example.invalid>", "Functional Fixture Engine support", "", 0},
		{"commit_coauthor", "change\n\nCo-authored-by: Fixture Automaton <robot@example.invalid>", "change", "", 4},
		{"commit_signature", "change\n\nGenerated with Fixture Engine", "change", "", 4},
		{"separator", "change\n---\n\x1e\nGenerated with Fixture Engine", "change", "", 4},
		{"crlf_case", "change\r\n\r\ncO-aUtHoReD-bY: Fixture Automaton <robot@example.invalid>\r\n", "change", "", 4},
		{"title_signature", "change", "Generated with Fixture Engine", "", 4},
		{"body_signature", "change", "change", "notes\r\ngenerated WITH Fixture Engine", 4},
		{"title_coauthor", "change", "Co-authored-by: Fixture Automaton", "", 4},
		{"body_coauthor", "change", "change", "Co-authored-by: Fixture Automaton", 4},
		{"repeated", "change\n\nCo-authored-by: Fixture Human <human@example.invalid>\nCo-authored-by: Fixture Automaton <robot@example.invalid>", "change", "", 4},
		{"human_unicode", "change\n\nCo-authored-by: Humán <human@example.invalid>", "change", "   ", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, g := gitFixture(t)
			base := g("", "rev-parse", "HEAD")
			g("", "commit", "--allow-empty", "-m", "earlier clean")
			g("", "commit", "--allow-empty", "-m", tc.msg)
			head := g("", "rev-parse", "HEAD")
			code, r := check(fixturePolicy, eventFile(t, dir, base, head, tc.title, tc.body), dir)
			if code != tc.want {
				t.Fatalf("got %d %+v want %d", code, r, tc.want)
			}
		})
	}
}

func TestInvalidInputs(t *testing.T) {
	dir, g := gitFixture(t)
	sha := g("", "rev-parse", "HEAD")
	valid := eventFile(t, dir, sha, sha, "clean", "")
	for _, p := range []string{"", " ", "{}", `{"coauthor_pattern":"[","signature_pattern":"x"}`, fixturePolicy + " {}", strings.Repeat("x", 16385), `{"coauthor_pattern":"","signature_pattern":"x"}`} {
		if code, _ := check(p, valid, dir); code != 2 {
			t.Errorf("policy accepted: code %d", code)
		}
	}
	for _, raw := range []string{"", `{}`, `{"pull_request":null}`, `{} {}`, "\xff", `{"pull_request":{"title":"\u0000"}}`, strings.Repeat(" ", maxEvent+1)} {
		p := filepath.Join(t.TempDir(), "event")
		os.WriteFile(p, []byte(raw), 0600)
		if c, _ := check(fixturePolicy, p, dir); c != 2 {
			t.Errorf("event accepted: %d", c)
		}
	}
	for _, s := range []string{"--all", "HEAD", strings.Repeat("a", 40), ""} {
		if c, _ := check(fixturePolicy, eventFile(t, dir, sha, s, "clean", ""), dir); c != 2 {
			t.Errorf("sha accepted: %d", c)
		}
	}
	if c, _ := check(fixturePolicy, filepath.Join(dir, "missing"), dir); c != 2 {
		t.Fatal(c)
	}
}

func TestHostileRawCommits(t *testing.T) {
	dir, g := gitFixture(t)
	base := g("", "rev-parse", "HEAD")
	tree := g("", "rev-parse", "HEAD^{tree}")
	header := "tree " + tree + "\nparent " + base + "\nauthor Fixture Human <human@example.invalid> 1 +0000\ncommitter Fixture Human <human@example.invalid> 1 +0000\n\n"
	for _, msg := range []string{"bad\x00\nGenerated with Fixture Engine", "bad\xff", strings.Repeat("a", maxCommit+1)} {
		head := g(header+msg, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
		if c, _ := check(fixturePolicy, eventFile(t, dir, base, head, "clean", ""), dir); c != 2 {
			t.Fatalf("hostile commit accepted: %d", c)
		}
	}
	for _, raw := range []string{"", "tree " + tree + "\n", header[:len(header)-1], strings.Replace(header, "author Fixture Human <human@example.invalid> 1 +0000", "author invalid", 1)} {
		head := g(raw, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
		if code, _ := check(fixturePolicy, eventFile(t, dir, base, head, "clean", ""), dir); code != 2 {
			t.Fatalf("truncated object accepted: %d", code)
		}
		if _, err := commitMessage([]byte(raw)); err == nil {
			t.Fatal("truncated commit accepted")
		}
	}
}

func TestCommandProcess(t *testing.T) {
	if os.Getenv("ATTRIBUTION_TEST_CHILD") == "1" {
		main()
		return
	}
	dir, g := gitFixture(t)
	sha := g("", "rev-parse", "HEAD")
	for _, tc := range []struct {
		policy, title string
		code          int
		status        string
	}{{fixturePolicy, "clean", 0, "ok"}, {"", "clean", 2, "2"}, {fixturePolicy, "Generated with Fixture Engine", 4, "4"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandProcess$")
		c.Dir = dir
		c.WaitDelay = time.Second
		git, _ := exec.LookPath("git")
		c.Env = []string{"ATTRIBUTION_TEST_CHILD=1", "NAU_ATTRIBUTION_POLICY=" + tc.policy, "GITHUB_EVENT_PATH=" + eventFile(t, dir, sha, sha, tc.title, ""), "PATH=" + filepath.Dir(git), "HOME=" + dir, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		var out, diag bytes.Buffer
		c.Stdout = &out
		c.Stderr = &diag
		err := c.Run()
		cancel()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != tc.code || strings.Count(out.String(), "\n") != 1 || !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
		var report result
		if json.Unmarshal(bytes.TrimSpace(out.Bytes()), &report) != nil || report.Status != tc.status {
			t.Fatalf("JSON status=%q, want %q for exit %d", report.Status, tc.status, code)
		}
		t.Logf("process exit=%d stdout=%s", code, strings.TrimSpace(out.String()))
		if strings.Contains(out.String()+diag.String(), "Fixture") {
			t.Fatal("raw input leaked")
		}
	}
}
