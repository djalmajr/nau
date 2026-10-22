//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backendFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backend")
	if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConditionalBackendSafety(t *testing.T) {
	for _, kind := range []string{"safe", "spaces", "relative", "LF", "CR", "NUL", "trailing space", "missing", "directory", "not executable", "writable file", "writable parent", "writable ancestor", "sticky trusted", "symlink safe", "symlink writable target", "symlink writable parent", "symlink intermediate writable"} {
		t.Run(kind, func(t *testing.T) {
			path := backendFixture(t)
			candidate := path
			wantSafe := kind == "safe" || kind == "spaces" || kind == "sticky trusted" || kind == "symlink safe"
			switch kind {
			case "spaces":
				candidate = filepath.Join(filepath.Dir(path), "backend café 東京 ;$()'[]")
				if err := os.Rename(path, candidate); err != nil {
					t.Fatal(err)
				}
			case "relative":
				candidate = "./backend"
			case "LF":
				candidate += "\nmachine_label=injected"
			case "CR":
				candidate += "\rmachine_label=injected"
			case "NUL":
				candidate += "\x00"
			case "trailing space":
				// The file with the space exists and is safe, but Load would
				// trim the stored value and select another path.
				candidate = path + " "
				if err := os.Rename(path, candidate); err != nil {
					t.Fatal(err)
				}
			case "missing":
				candidate += "-missing"
			case "directory":
				candidate = filepath.Dir(path)
			case "not executable":
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "writable file":
				if err := os.Chmod(path, 0722); err != nil {
					t.Fatal(err)
				}
			case "writable parent":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			case "sticky trusted":
				if err := os.Chmod(filepath.Dir(path), 0777|os.ModeSticky); err != nil {
					t.Fatal(err)
				}
			case "writable ancestor":
				parent := filepath.Dir(path)
				child := filepath.Join(parent, "child")
				if err := os.Mkdir(child, 0700); err != nil {
					t.Fatal(err)
				}
				candidate = filepath.Join(child, "backend")
				if err := os.Rename(path, candidate); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(parent, 0777); err != nil {
					t.Fatal(err)
				}
			case "symlink safe", "symlink writable target", "symlink writable parent", "symlink intermediate writable":
				linkDir := t.TempDir()
				candidate = filepath.Join(linkDir, "link")
				if kind == "symlink intermediate writable" {
					// The first symlink's destination is writable, although the
					// final executable resolves into a different safe directory.
					middle := t.TempDir()
					if err := os.Symlink(path, filepath.Join(middle, "backend")); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(middle, 0777); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(middle, candidate); err != nil {
						t.Fatal(err)
					}
					candidate = filepath.Join(candidate, "backend")
				} else {
					if err := os.Symlink(path, candidate); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "symlink writable target" {
					if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "symlink writable parent" {
					if err := os.Chmod(linkDir, 0777); err != nil {
						t.Fatal(err)
					}
				}
			}
			dir := t.TempDir()
			file := filepath.Join(dir, "config")
			const initial = "# keep\nmachine_label=original\nherdr_soho_bin=\n"
			if err := os.WriteFile(file, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}
			warning, err := SetIfAutomatic(dir, "herdr_soho_bin", candidate)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if wantSafe {
				want := strings.Replace(initial, "herdr_soho_bin=\n", "herdr_soho_bin="+candidate+"\n", 1)
				if warning != "" || string(data) != want {
					t.Fatalf("safe backend refused: warning=%q bytes=%q", warning, data)
				}
			} else if warning == "" || string(data) != initial {
				t.Fatalf("unsafe backend accepted: warning=%q bytes=%q", warning, data)
			}
		})
	}
}

func TestConfigWritersShareLock(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "conditional"}[automatic], func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "config")
			if err := os.WriteFile(file, []byte("herdr_soho_bin=\n"), 0600); err != nil {
				t.Fatal(err)
			}
			candidate := backendFixture(t)
			unlock, err := lockConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			locked := true
			defer func() {
				if locked {
					unlock()
				}
			}()
			type result struct {
				warning string
				err     error
			}
			finished := make(chan result, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				if automatic {
					w, err := SetIfAutomatic(dir, "herdr_soho_bin", candidate)
					finished <- result{w, err}
				} else {
					finished <- result{err: Set(dir, "machine_label", "new")}
				}
			}()
			<-started
			select {
			case got := <-finished:
				t.Fatalf("writer bypassed held config.lock: %+v", got)
			case <-time.After(100 * time.Millisecond):
			}
			// Commit the competing writer inside its held critical section.
			const concurrent = "# concurrent\nherdr_soho_bin=./chosen-backend\npush_timeout_s=19\n"
			if err := os.WriteFile(file, []byte(concurrent), 0600); err != nil {
				t.Fatal(err)
			}
			unlock()
			locked = false
			select {
			case got := <-finished:
				if got.err != nil {
					t.Fatal(got.err)
				}
				if automatic && !strings.Contains(got.warning, "explicitly") {
					t.Fatalf("missing refusal: %+v", got)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("writer hung after unlock")
			}
			data, err := os.ReadFile(file)
			want := concurrent
			if !automatic {
				want += "machine_label=new\n"
			}
			if err != nil || string(data) != want {
				t.Fatalf("concurrent update lost: %q err=%v", data, err)
			}
		})
	}
}
