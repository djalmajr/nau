package migrate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// fakeDB is a minimal in-memory model of the storage contract for the
// migration tests: it tracks the created tables and the registered
// versions, and applies a batch atomically (all statements or none).
type fakeDB struct {
	mu         sync.Mutex
	tables     map[string]bool
	versions   []int64
	batchLogs  [][]store.Stmt
	failOn     string // a statement containing this fails the batch
	executeErr error
}

func newFakeDB() *fakeDB {
	return &fakeDB{tables: make(map[string]bool)}
}

func (f *fakeDB) Execute(ctx context.Context, stmt store.Stmt) (store.Result, error) {
	if f.executeErr != nil {
		return store.Result{}, f.executeErr
	}
	switch {
	case strings.Contains(stmt.SQL, "sqlite_master"):
		if f.tables["schema_migrations"] {
			return store.Result{Rows: []store.Row{{"schema_migrations"}}}, nil
		}
		return store.Result{}, nil
	case strings.Contains(stmt.SQL, "SELECT version FROM schema_migrations"):
		var rows []store.Row
		for _, v := range f.versions {
			rows = append(rows, store.Row{v})
		}
		return store.Result{Rows: rows}, nil
	default:
		return store.Result{}, errors.New("fakeDB: unexpected statement " + stmt.SQL)
	}
}

func (f *fakeDB) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchLogs = append(f.batchLogs, stmts)
	// Shadow pass: if any statement fails, nothing is applied.
	for _, s := range stmts {
		if f.failOn != "" && strings.Contains(s.SQL, f.failOn) {
			return nil, errors.New("fakeDB: statement refused: " + f.failOn)
		}
	}
	var results []store.Result
	for _, s := range stmts {
		if strings.Contains(s.SQL, "CREATE TABLE") {
			name := createTableName(s.SQL)
			f.tables[name] = true
		}
		if strings.Contains(s.SQL, "INSERT INTO schema_migrations") {
			if len(s.Args) != 2 {
				return nil, errors.New("fakeDB: the registration needs two arguments")
			}
			v, ok := s.Args[0].(int64)
			if !ok {
				return nil, errors.New("fakeDB: the registration version is not an int64")
			}
			ms, ok := s.Args[1].(int64)
			if !ok || ms <= 0 {
				return nil, errors.New("fakeDB: the registration timestamp is not a positive int64")
			}
			f.versions = append(f.versions, v)
		}
		results = append(results, store.Result{Affected: 1})
	}
	return results, nil
}

func (f *fakeDB) Ping(ctx context.Context) error { return nil }

// createTableName pulls the table name out of a CREATE TABLE statement.
func createTableName(sql string) string {
	i := strings.Index(sql, "CREATE TABLE")
	rest := sql[i+len("CREATE TABLE"):]
	rest = strings.TrimLeft(rest, " \t\n")
	if j := strings.IndexAny(rest, " \t\n)"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func fixedNow() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }

func TestLoadEmbeddedMigrations(t *testing.T) {
	migrations, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("migrations = %d, want 1", len(migrations))
	}
	m := migrations[0]
	if m.Version != 1 || m.Name != "0001_init" {
		t.Fatalf("migration = %+v", m)
	}
	if !strings.Contains(m.SQL, "CREATE TABLE schema_migrations") {
		t.Fatal("0001 does not create schema_migrations")
	}
	if !strings.Contains(m.SQL, "CREATE TABLE hub_meta") {
		t.Fatal("0001 does not create hub_meta")
	}
	if countCreateTables(m.SQL) != 2 {
		t.Fatal("0001 creates more than schema_migrations and hub_meta")
	}
}

// countCreateTables counts the CREATE TABLE statements of a migration.
func countCreateTables(sql string) int {
	return strings.Count(sql, "CREATE TABLE")
}

func TestApplyFromZero(t *testing.T) {
	db := newFakeDB()
	r := &Runner{Store: db, Now: fixedNow}
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.batchLogs) != 1 {
		t.Fatalf("batches = %d, want 1", len(db.batchLogs))
	}
	batch := db.batchLogs[0]
	if len(batch) != 3 {
		t.Fatalf("the batch has %d statements, want 3 (2 creates + the registration)", len(batch))
	}
	if !strings.Contains(batch[0].SQL, "CREATE TABLE schema_migrations") {
		t.Fatalf("statement 0 = %q", batch[0].SQL)
	}
	if !strings.Contains(batch[1].SQL, "CREATE TABLE hub_meta") {
		t.Fatalf("statement 1 = %q", batch[1].SQL)
	}
	if !strings.Contains(batch[2].SQL, "INSERT INTO schema_migrations") {
		t.Fatalf("statement 2 = %q, want the registration", batch[2].SQL)
	}
	if len(db.versions) != 1 || db.versions[0] != 1 {
		t.Fatalf("versions = %v, want [1]", db.versions)
	}
	if !db.tables["schema_migrations"] || !db.tables["hub_meta"] {
		t.Fatalf("tables = %v", db.tables)
	}
}

func TestApplyAgainIsANoOp(t *testing.T) {
	db := newFakeDB()
	r := &Runner{Store: db, Now: fixedNow}
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.batchLogs) != 1 {
		t.Fatalf("batches = %d, want 1 (the second apply changes nothing)", len(db.batchLogs))
	}
	if len(db.versions) != 1 || db.versions[0] != 1 {
		t.Fatalf("versions = %v, want [1]", db.versions)
	}
}

func TestFailedMigrationRegistersNothing(t *testing.T) {
	db := newFakeDB()
	migrations := []Migration{
		{Version: 1, Name: "0001_init", SQL: "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_ms INTEGER NOT NULL); CREATE TABLE hub_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);"},
		{Version: 2, Name: "0002_fails", SQL: "CREATE TABLE hub_machines (id INTEGER PRIMARY KEY); CREATE TABLE hub_broken (id INTEGER PRIMARY KEY);"},
	}
	db.failOn = "hub_broken"
	r := &Runner{Store: db, Now: fixedNow, Migrations: migrations}
	if err := r.Apply(context.Background()); err == nil {
		t.Fatal("Apply: want an error for the failing migration")
	}
	db.mu.Lock()
	if len(db.versions) != 1 || db.versions[0] != 1 {
		t.Fatalf("versions = %v, want [1] (only the first migration applied)", db.versions)
	}
	if db.tables["hub_machines"] {
		t.Fatal("the failed batch applied part of its statements")
	}
	if !db.tables["schema_migrations"] {
		t.Fatal("the first migration should have been applied")
	}
	db.failOn = ""
	db.mu.Unlock()
	// Re-applying after the failure applies the second migration.
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.versions) != 2 || db.versions[0] != 1 || db.versions[1] != 2 {
		t.Fatalf("versions = %v, want [1 2]", db.versions)
	}
	if !db.tables["hub_broken"] {
		t.Fatal("the re-apply did not create the second migration tables")
	}
}

func TestApplySkipsAlreadyApplied(t *testing.T) {
	db := newFakeDB()
	db.tables["schema_migrations"] = true
	db.versions = []int64{1, 3}
	migrations := []Migration{
		{Version: 1, Name: "0001_init", SQL: "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY);"},
		{Version: 2, Name: "0002_add", SQL: "CREATE TABLE hub_two (id INTEGER PRIMARY KEY);"},
		{Version: 3, Name: "0003_add", SQL: "CREATE TABLE hub_three (id INTEGER PRIMARY KEY);"},
	}
	r := &Runner{Store: db, Now: fixedNow, Migrations: migrations}
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.batchLogs) != 1 {
		t.Fatalf("batches = %d, want 1 (only migration 2)", len(db.batchLogs))
	}
	if len(db.versions) != 3 || db.versions[2] != 2 {
		t.Fatalf("versions = %v, want [1 3 2]", db.versions)
	}
}

func TestApplyInVersionOrder(t *testing.T) {
	db := newFakeDB()
	// Versions out of order in the input: the runner sorts them.
	migrations := []Migration{
		{Version: 2, Name: "0002_add", SQL: "CREATE TABLE hub_two (id INTEGER PRIMARY KEY);"},
		{Version: 1, Name: "0001_init", SQL: "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY);"},
		{Version: 3, Name: "0003_add", SQL: "CREATE TABLE hub_three (id INTEGER PRIMARY KEY);"},
	}
	r := &Runner{Store: db, Now: fixedNow, Migrations: migrations}
	if err := r.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.versions) != 3 || db.versions[0] != 1 || db.versions[1] != 2 || db.versions[2] != 3 {
		t.Fatalf("versions = %v, want [1 2 3]", db.versions)
	}
}

func TestApplyStoreErrorIsReported(t *testing.T) {
	db := newFakeDB()
	db.executeErr = errors.New("database unreachable")
	r := &Runner{Store: db, Now: fixedNow}
	err := r.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply: want an error")
	}
	if !strings.Contains(err.Error(), "database unreachable") {
		t.Fatalf("error = %v", err)
	}
}

func TestSplitStatements(t *testing.T) {
	stmts := splitStatements(" CREATE A (x) ;\n\n CREATE B (y);  \n")
	if len(stmts) != 2 {
		t.Fatalf("statements = %v", stmts)
	}
	if stmts[0].SQL != "CREATE A (x)" || stmts[1].SQL != "CREATE B (y)" {
		t.Fatalf("statements = %v", stmts)
	}
	if len(splitStatements("")) != 0 {
		t.Fatal("an empty file has no statements")
	}
}
