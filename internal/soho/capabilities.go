package soho

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Caps is the parsed `soho capabilities --json` output. Raw keeps
// the original line.
type Caps struct {
	Raw  string
	vals map[string]float64
}

// Has reports whether the capability key is present with a value of at
// least 1 (a boolean true counts as 1).
func (c Caps) Has(key string) bool {
	v, ok := c.vals[key]
	return ok && v >= 1
}

// Capabilities runs `soho capabilities --json` (bounded) and parses
// the last line leniently: unknown fields are ignored, numeric values are
// recorded and a boolean true counts as 1. A non-JSON line or a non-zero
// exit is an error.
func (r *Runner) Capabilities(ctx context.Context) (Caps, error) {
	var out bytes.Buffer
	exit, err := r.Run(ctx, []string{"capabilities", "--json"}, nil, &out, io.Discard, defaultBound)
	if err != nil {
		return Caps{}, err
	}
	if err := r.awaitDrainDone(); err != nil {
		return Caps{}, err
	}
	if exit != 0 {
		return Caps{}, fmt.Errorf("soho: capabilities exited %d", exit)
	}
	raw := lastLine(out.String())
	if raw == "" {
		return Caps{}, errors.New("soho: capabilities printed no line")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Caps{}, fmt.Errorf("soho: capabilities output is not a JSON line: %w", err)
	}
	c := Caps{Raw: raw, vals: map[string]float64{}}
	for k, v := range m {
		switch n := v.(type) {
		case float64:
			c.vals[k] = n
		case bool:
			if n {
				c.vals[k] = 1
			}
		}
	}
	return c, nil
}

// lastLine returns the last non-blank line of s, trimmed.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
