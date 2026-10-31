//go:build sqld

package hubtest

import (
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// TestSQLDResetDropsTablesWhateverTheForeignKeys seeds tables the hub
// does not know, linked by foreign keys with rows that reference each
// other: an extra child of users, and an extra parent created before
// its child, so the listing returns the parent first. The reset still
// drops every table and applies the migrations again.
func TestSQLDResetDropsTablesWhateverTheForeignKeys(t *testing.T) {
	st := SQLD(t)
	ctx := t.Context()
	now := time.Now().UnixMilli()
	seed := []store.Stmt{
		{SQL: `INSERT INTO users (id, email_norm, email, name, password_hash, role, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			Args: []store.Value{"u-reset", "ana@example.test", "ana@example.test", "Ana Exemplo", "not-a-hash", "member", now}},
		{SQL: `CREATE TABLE zz_extra_child_of_users (id INTEGER PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id))`},
		{SQL: `INSERT INTO zz_extra_child_of_users (id, user_id) VALUES (1, 'u-reset')`},
		{SQL: `CREATE TABLE "a ""parent""" (id INTEGER PRIMARY KEY)`},
		{SQL: `CREATE TABLE b_child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES "a ""parent"""(id))`},
		{SQL: `INSERT INTO "a ""parent""" (id) VALUES (1)`},
		{SQL: `INSERT INTO b_child (id, parent_id) VALUES (1, 1)`},
	}
	for i, stmt := range seed {
		if _, err := st.Execute(ctx, stmt); err != nil {
			t.Fatalf("seed statement %d: %v", i, err)
		}
	}

	resetSchema(t, st)

	res, err := st.Execute(ctx, store.Stmt{SQL: `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('zz_extra_child_of_users', 'a "parent"', 'b_child')`})
	if err != nil {
		t.Fatalf("list the extra tables: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("extra tables left after the reset: %v", res.Rows)
	}
	res, err = st.Execute(ctx, store.Stmt{SQL: `SELECT COUNT(*) FROM users`})
	if err != nil {
		t.Fatalf("the users table after the reset: %v", err)
	}
	if n, _ := res.Rows[0][0].(int64); n != 0 {
		t.Fatalf("users after the reset = %v, want an empty table from the migrations", res.Rows[0][0])
	}
}
