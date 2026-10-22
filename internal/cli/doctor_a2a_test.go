package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/notify"
	"github.com/djalmajr/nau/internal/outbox"
	"github.com/djalmajr/nau/internal/registry"
)

func doctorAt(t *testing.T, dir string) (string, string, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	exit := cmdDoctor(nil, Env{ConfigDir: dir, Now: func() time.Time { return serveFixedNow }, Getenv: getenvFor(true), Environ: func() []string { return fakeChildEnv }, Stdout: &out, Stderr: &stderr})
	return out.String(), stderr.String(), exit
}

func TestDoctorA2AUncertain(t *testing.T) {
	exe, _ := installFakeSoho(t, capsRule(capsJSON, 0))
	dir := t.TempDir()
	setSohoConfig(t, dir, exe, "machine-a")
	baseline, stderr, baselineExit := doctorAt(t, dir)
	if baselineExit != 0 || strings.Contains(baseline, `"a2a"`) || strings.Contains(stderr, "A2A owner") {
		t.Fatalf("baseline=%d %q %q", baselineExit, baseline, stderr)
	}
	if _, err := os.Stat(filepath.Join(outbox.StateDir(dir), "registry.db")); !os.IsNotExist(err) {
		t.Fatalf("doctor created database: %v", err)
	}
	// Legacy timestamps must remain readable without a schema migration.
	_, db := a2aReplyActorFixture(t, dir, "local/owner")
	s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(db))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Exec(`UPDATE a2a_deliveries SET state='uncertain',last_attempt=?`, serveFixedNow.Add(-125*time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Exec(`INSERT INTO a2a_deliveries(ingress_id,task_id,role,state,last_attempt,created,created_unix) VALUES(1,'task-actor','guardian','uncertain',?,?,1)`, serveFixedNow.Add(-300*time.Second).Format(time.RFC3339), serveFixedNow.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	out, stderr, exit := doctorAt(t, dir)
	want := strings.TrimSuffix(strings.TrimSuffix(baseline, "\n"), "}") + `,"a2a":{"uncertain":2,"uncertain_oldest_s":300}}` + "\n"
	if exit != baselineExit || out != want {
		t.Fatalf("doctor=%d %q, want %d %q", exit, out, baselineExit, want)
	}
	warning := "nau: doctor: 2 A2A owner notice(s) uncertain, the oldest for 300s; once you have checked that the owner did not get it, re-queue it with 'nau a2a retry --task <task-id> --delivery <delivery-id>'\n"
	if !strings.Contains(stderr, warning) || strings.Count(stderr, "A2A owner") != 1 {
		t.Fatalf("warning=%q", stderr)
	}
	if _, err = s.Exec(`UPDATE a2a_deliveries SET last_attempt_end=? WHERE role='guardian'`, serveFixedNow.Add(-10*time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	out, stderr, exit = doctorAt(t, dir)
	if exit != baselineExit || !strings.Contains(out, `"a2a":{"uncertain":2,"uncertain_oldest_s":125}`) || !strings.Contains(stderr, "the oldest for 125s") {
		t.Fatalf("end preferred=%d %q %q", exit, out, stderr)
	}
	if _, err = s.Exec(`UPDATE a2a_deliveries SET state=?`, notify.StateAccepted); err != nil {
		t.Fatal(err)
	}
	out, stderr, exit = doctorAt(t, dir)
	want = strings.TrimSuffix(strings.TrimSuffix(baseline, "\n"), "}") + `,"a2a":{"uncertain":0}}` + "\n"
	if exit != baselineExit || out != want || strings.Contains(stderr, "A2A owner") {
		t.Fatalf("zero=%d %q %q", exit, out, stderr)
	}
	if _, err = s.Exec(`UPDATE a2a_deliveries SET state='uncertain'`); err != nil {
		t.Fatal(err)
	}
	setSohoConfig(t, dir, filepath.Join(t.TempDir(), "absent-backend"), "machine-a")
	out, stderr, exit = doctorAt(t, dir)
	if exit != 43 || !strings.Contains(out, `"uncertain":2`) || !strings.Contains(stderr, "A2A owner notice(s) uncertain") {
		t.Fatalf("backend unavailable=%d %q %q", exit, out, stderr)
	}
}

func TestDoctorA2ARegistryOnlyReadOnly(t *testing.T) {
	dir := t.TempDir()
	exe, _ := installFakeSoho(t, capsRule(capsJSON, 0))
	setSohoConfig(t, dir, exe, "machine-a")
	db := filepath.Join(outbox.StateDir(dir), "registry.db")
	s, err := registry.Open(db, registry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, exit := doctorAt(t, dir)
	if exit != 0 || strings.Contains(out, `"a2a"`) || strings.Contains(stderr, "A2A owner") {
		t.Fatalf("registry only=%d %q %q", exit, out, stderr)
	}
	after, err := os.ReadFile(db)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("doctor mutated registry: %v", err)
	}
}

// Removing the terminal-task filter keeps resolved notices in the count and warning.
func TestDoctorA2ATerminalTasks(t *testing.T) {
	exe, _ := installFakeSoho(t, capsRule(capsJSON, 0))
	for _, state := range []string{"completed", "canceled", "failed", "rejected", "working"} {
		t.Run(state, func(t *testing.T) {
			dir, _, _ := retryFixture(t, notify.OutcomeUncertain)
			setSohoConfig(t, dir, exe, "machine-a")
			s, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(outbox.StateDir(dir), "registry.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Exec(`UPDATE a2a_tasks SET state=?`, state); err != nil {
				t.Fatal(err)
			}
			out, stderr, exit := doctorAt(t, dir)
			want := `"a2a":{"uncertain":0}`
			if state == "working" {
				want = `"a2a":{"uncertain":1,"uncertain_oldest_s":0}`
			}
			if exit != 0 || !strings.Contains(out, want) || strings.Contains(stderr, "A2A owner") != (state == "working") {
				t.Fatalf("doctor=%d %q %q want=%s", exit, out, stderr, want)
			}
		})
	}
}

func TestDoctorA2ANowriteExistingDatabase(t *testing.T) {
	dir, _, _ := retryFixture(t, notify.OutcomeUncertain)
	exe, _ := installFakeSoho(t, capsRule(capsJSON, 0))
	setSohoConfig(t, dir, exe, "machine-a")
	state := outbox.StateDir(dir)
	before, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	out, _, exit := doctorAt(t, dir)
	if exit != 0 || !strings.Contains(out, `"uncertain":1`) {
		t.Fatalf("doctor=%d %q", exit, out)
	}
	after, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	names := func(es []os.DirEntry) string {
		var n []string
		for _, e := range es {
			n = append(n, e.Name())
		}
		return strings.Join(n, ",")
	}
	if names(before) != names(after) {
		t.Fatalf("before=%s after=%s", names(before), names(after))
	}
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o700)
	out, _, exit = doctorAt(t, dir)
	if exit != 0 || !strings.Contains(out, `"uncertain":1`) {
		t.Fatalf("read-only doctor=%d %q", exit, out)
	}
}

func TestDoctorA2AReadError(t *testing.T) {
	dir := t.TempDir()
	exe, _ := installFakeSoho(t, capsRule(capsJSON, 0))
	setSohoConfig(t, dir, exe, "machine-a")
	state := outbox.StateDir(dir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "registry.db"), []byte("invalid database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(state, 0o700)
	out, stderr, exit := doctorAt(t, dir)
	if exit != 0 || !strings.Contains(out, `"a2a":{"error":"unavailable"}`) || strings.Contains(out, `"uncertain"`) || strings.Contains(stderr, "A2A owner") {
		t.Fatalf("read error=%d %q %q", exit, out, stderr)
	}
}
