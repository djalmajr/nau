package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPolicy  = 16 << 10
	maxEvent   = 2 << 20
	maxCommit  = 2 << 20
	maxCommits = 10000
)

var shaPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
var trailerPattern = regexp.MustCompile(`(?i)^co-authored-by\s*:`)
var identityPattern = regexp.MustCompile(`^(author|committer) [^<>\n]* <[^<>\n]*> -?[0-9]+ [+-][0-9]{4}$`)
var invalid = errors.New("invalid input")

type result struct {
	Status string `json:"status"`
	Motivo string `json:"motivo"`
	Commit string `json:"commit,omitempty"`
}

type policy struct{ coauthor, signature *regexp.Regexp }

func decode(data []byte, dst any) error {
	if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(dst); err != nil {
		return invalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return invalid
	}
	return nil
}

func readPolicy(raw string) (policy, error) {
	if len(raw) == 0 || len(raw) > maxPolicy {
		return policy{}, invalid
	}
	var fields map[string]string
	if decode([]byte(raw), &fields) != nil || len(fields) != 2 {
		return policy{}, invalid
	}
	a, b := fields["coauthor_pattern"], fields["signature_pattern"]
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return policy{}, invalid
	}
	c, err := regexp.Compile("(?i)" + a)
	if err != nil {
		return policy{}, invalid
	}
	s, err := regexp.Compile("(?i)" + b)
	if err != nil {
		return policy{}, invalid
	}
	return policy{c, s}, nil
}

func (p policy) refusal(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if trailerPattern.MatchString(line) && p.coauthor.MatchString(line) {
			return "coauthor"
		}
		// The signature expression defines signature lines; policy should anchor it.
		if p.signature.MatchString(line) {
			return "signature"
		}
	}
	return ""
}

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, invalid
	}
	return b.buffer.Write(p)
}

func git(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.WaitDelay = time.Second
	// Ignore replacement objects and ambient Git configuration, including command injection settings.
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1"}
	out := &boundedBuffer{limit: limit}
	c.Stdout = out
	c.Stderr = io.Discard
	if c.Run() != nil {
		return nil, invalid
	}
	return out.buffer.Bytes(), nil
}

func commitMessage(raw []byte) (string, error) {
	if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
		return "", invalid
	}
	headers, msg, ok := strings.Cut(string(raw), "\n\n")
	if !ok {
		return "", invalid
	}
	lines := strings.Split(headers, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "tree ") || !shaPattern.MatchString(strings.TrimPrefix(lines[0], "tree ")) {
		return "", invalid
	}
	author, committer := false, false
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "parent "):
			if author || !shaPattern.MatchString(strings.TrimPrefix(line, "parent ")) {
				return "", invalid
			}
		case strings.HasPrefix(line, "author "):
			if author || committer || !identityPattern.MatchString(line) {
				return "", invalid
			}
			author = true
		case strings.HasPrefix(line, "committer "):
			if !author || committer || !identityPattern.MatchString(line) {
				return "", invalid
			}
			committer = true
		case strings.HasPrefix(line, " "):
		default:
			if !strings.Contains(line, " ") {
				return "", invalid
			}
		}
	}
	if !author || !committer {
		return "", invalid
	}
	return msg, nil
}

func check(rawPolicy, eventPath, dir string) (int, result) {
	bad := func(category string) (int, result) { return 2, result{Status: "2", Motivo: category} }
	p, err := readPolicy(rawPolicy)
	if err != nil {
		return bad("policy")
	}
	f, err := os.Open(eventPath)
	if err != nil {
		return bad("event")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEvent+1))
	f.Close()
	if err != nil || len(data) > maxEvent {
		return bad("event")
	}
	var event struct {
		PullRequest *struct {
			Title *string `json:"title"`
			Body  *string `json:"body"`
			Base  struct {
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if decode(data, &event) != nil || event.PullRequest == nil || event.PullRequest.Title == nil {
		return bad("event")
	}
	pr := event.PullRequest
	body := ""
	if pr.Body != nil {
		body = *pr.Body
	}
	if strings.ContainsRune(*pr.Title+body, 0) || !utf8.ValidString(*pr.Title+body) {
		return bad("event")
	}
	if !shaPattern.MatchString(pr.Base.SHA) || !shaPattern.MatchString(pr.Head.SHA) {
		return bad("sha")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Read both endpoints even for an empty range, ensuring they are available commits.
	for _, sha := range []string{pr.Base.SHA, pr.Head.SHA} {
		raw, e := git(ctx, dir, maxCommit, "cat-file", "commit", sha)
		if e != nil {
			return bad("commit")
		}
		if _, e = commitMessage(raw); e != nil {
			return bad("commit")
		}
	}
	ids, err := git(ctx, dir, (maxCommits+1)*65, "rev-list", "--max-count=10001", pr.Base.SHA+".."+pr.Head.SHA, "--")
	if err != nil {
		return bad("range")
	}
	commits := strings.Fields(string(ids))
	if len(commits) > maxCommits {
		return bad("range")
	}
	for _, sha := range commits {
		if !shaPattern.MatchString(sha) {
			return bad("range")
		}
		raw, e := git(ctx, dir, maxCommit, "cat-file", "commit", sha)
		if e != nil {
			return bad("commit")
		}
		msg, e := commitMessage(raw)
		if e != nil {
			return bad("commit")
		}
		if category := p.refusal(msg); category != "" {
			return 4, result{Status: "4", Motivo: category, Commit: sha}
		}
	}
	for _, text := range []string{*pr.Title, body} {
		if category := p.refusal(text); category != "" {
			return 4, result{Status: "4", Motivo: "pr_" + category}
		}
	}
	return 0, result{Status: "ok", Motivo: "clean"}
}

func main() {
	code, r := check(os.Getenv("NAU_ATTRIBUTION_POLICY"), os.Getenv("GITHUB_EVENT_PATH"), ".")
	_ = json.NewEncoder(os.Stdout).Encode(r)
	os.Exit(code)
}
