//go:build sqld

// The external test package breaks the import cycle: the helper opens
// the client of this package, and the tests of this package use the
// helper.
package sqld_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/hubtest"
	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/store"
)

// Every test of this file starts from the helper: it opens the sqld
// named by NAU_TEST_SQLD_URL (skipping when it is empty), takes the
// lock of the shared database, reinitializes the schema and holds the
// lock for the whole test, so the tests run on a fresh schema and the
// tagged suites run without -p 1.

func ctxForTest() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func TestSQLDPing(t *testing.T) {
	st := hubtest.SQLD(t)
	ctx, cancel := ctxForTest()
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestSQLDMigrationsFromZero(t *testing.T) {
	want, err := migrate.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := hubtest.SQLD(t)
	ctx, cancel := ctxForTest()
	defer cancel()
	// A fresh schema namespace under the lock: the helper left the
	// hub schema, and this test checks the migrations themselves from
	// an empty database.
	hubtest.DropAll(t, st)
	r := &migrate.Runner{Store: st, Now: func() time.Time { return time.Now() }}
	if err := r.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The initial tables are in the schema.
	for _, table := range []string{"schema_migrations", "hub_meta"} {
		if !tableExists(t, st, ctx, table) {
			t.Fatalf("the table %s is not in the schema", table)
		}
	}
	// Every embedded version is registered, in order.
	rows, err := st.Execute(ctx, store.Stmt{SQL: `SELECT version FROM schema_migrations ORDER BY version`})
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(rows.Rows) != len(want) {
		t.Fatalf("versions = %v, want %d rows (one per embedded migration)", rows.Rows, len(want))
	}
	for i, m := range want {
		v, ok := rows.Rows[i][0].(int64)
		if !ok || v != int64(m.Version) {
			t.Fatalf("versions = %v, want the embedded versions in order", rows.Rows)
		}
	}
	// A second apply changes nothing.
	if err := r.Apply(ctx); err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	rows, err = st.Execute(ctx, store.Stmt{SQL: `SELECT version FROM schema_migrations ORDER BY version`})
	if err != nil {
		t.Fatalf("list versions again: %v", err)
	}
	if len(rows.Rows) != len(want) {
		t.Fatalf("versions = %v, want exactly %d rows after the second apply", rows.Rows, len(want))
	}
}

func TestSQLDBatchIsAtomic(t *testing.T) {
	st := hubtest.SQLD(t)
	ctx, cancel := ctxForTest()
	defer cancel()
	if _, err := st.Execute(ctx, store.Stmt{SQL: `DROP TABLE IF EXISTS hub_atomic_test`}); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := st.Execute(ctx, store.Stmt{SQL: `CREATE TABLE hub_atomic_test (id INTEGER PRIMARY KEY, name TEXT)`}); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := ctxForTest()
		defer cancel()
		_, _ = st.Execute(ctx, store.Stmt{SQL: `DROP TABLE IF EXISTS hub_atomic_test`})
	})
	// The batch inserts, then fails on a statement in the middle, then
	// inserts again: none of the inserts may survive.
	_, err := st.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO hub_atomic_test (name) VALUES (?)`, Args: []store.Value{"first"}},
		{SQL: `INSERT INTO hub_no_such_table (name) VALUES (?)`, Args: []store.Value{"middle"}},
		{SQL: `INSERT INTO hub_atomic_test (name) VALUES (?)`, Args: []store.Value{"last"}},
	})
	if err == nil {
		t.Fatal("Batch: want an error for the invalid statement")
	}
	if !strings.Contains(err.Error(), "statement 2") {
		t.Fatalf("error = %v, want the failed statement named", err)
	}
	res, err := st.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM hub_atomic_test`})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n, ok := res.Rows[0][0].(int64); !ok || n != 0 {
		t.Fatalf("rows left after the failed batch = %v, want 0", res.Rows[0])
	}
	// The table still exists: the rollback undid the inserts, not the
	// table.
	if !tableExists(t, st, ctx, "hub_atomic_test") {
		t.Fatal("the table hub_atomic_test is gone after the failed batch")
	}
	// And a good batch applies.
	res2, err := st.Batch(ctx, []store.Stmt{
		{SQL: `INSERT INTO hub_atomic_test (name) VALUES (?)`, Args: []store.Value{"kept"}},
	})
	if err != nil {
		t.Fatalf("Batch again: %v", err)
	}
	if len(res2) != 1 {
		t.Fatalf("batch results = %d, want 1", len(res2))
	}
	count, err := st.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM hub_atomic_test`})
	if err != nil {
		t.Fatalf("count again: %v", err)
	}
	if n, ok := count.Rows[0][0].(int64); !ok || n != 1 {
		t.Fatalf("rows after the good batch = %v, want 1", count.Rows[0])
	}
}

// tableExists reports whether the table is in the schema.
func tableExists(t *testing.T, st store.Store, ctx context.Context, name string) bool {
	t.Helper()
	res, err := st.Execute(ctx, store.Stmt{
		SQL:  `SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		Args: []store.Value{name},
	})
	if err != nil {
		t.Fatalf("check %s: %v", name, err)
	}
	return len(res.Rows) == 1
}

// TestSQLDQuotedSemicolon applies a valid migration with a semicolon
// inside a string literal: the splitter must keep the statement whole.
func TestSQLDQuotedSemicolon(t *testing.T) {
	st := hubtest.SQLD(t)
	ctx, cancel := ctxForTest()
	defer cancel()
	r := &migrate.Runner{Store: st, Now: func() time.Time { return time.Now() }, Migrations: []migrate.Migration{{
		Version: 97,
		Name:    "quoted_semicolon",
		SQL:     `INSERT INTO hub_meta (key, value) VALUES ('quoted-semicolon', 'a;b');`,
	}}}
	if err := r.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v (the semicolon inside the string must not split the statement)", err)
	}
	res, err := st.Execute(ctx, store.Stmt{SQL: `SELECT value FROM hub_meta WHERE key = 'quoted-semicolon'`})
	if err != nil {
		t.Fatalf("read the value: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "a;b" {
		t.Fatalf("value = %v, want a;b (the literal kept whole)", res.Rows)
	}
	ver, err := st.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM schema_migrations WHERE version = 97`})
	if err != nil {
		t.Fatalf("count the version: %v", err)
	}
	if n, ok := ver.Rows[0][0].(int64); !ok || n != 1 {
		t.Fatalf("version rows = %v, want 1", ver.Rows[0])
	}
	// Leave the shared test database as found.
	if _, err := st.Execute(ctx, store.Stmt{SQL: `DELETE FROM schema_migrations WHERE version = 97`}); err != nil {
		t.Fatalf("clean the version: %v", err)
	}
	if _, err := st.Execute(ctx, store.Stmt{SQL: `DELETE FROM hub_meta WHERE key = 'quoted-semicolon'`}); err != nil {
		t.Fatalf("clean the value: %v", err)
	}
}
