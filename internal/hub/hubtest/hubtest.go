// Package hubtest is the test helper of the tagged suites of the hub
// packages: one helper that serializes the suites on the shared
// database and reinitializes the schema, so the suites run without
// -p 1 and no suite waits for the schema to be unchanged.
//
// The helper is used only by tests; the production code never imports
// it.
package hubtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/migrate"
	"github.com/djalmajr/nau/internal/hub/sqld"
	"github.com/djalmajr/nau/internal/hub/store"
)

// The constants of the lock: the pause between the attempts, the total
// wait of one suite and the age after which a lock is considered
// abandoned. They are variables so the unit tests shorten the wait.
var (
	lockWait    = 100 * time.Millisecond
	lockTimeout = 60 * time.Second
	lockStale   = 5 * time.Minute
)

// lockTableCreate makes the lock table: the single row that serializes
// the suites, with the owner of the lock and the moment it was taken.
const lockTableCreate = `CREATE TABLE IF NOT EXISTS hubtest_lock (id INTEGER PRIMARY KEY CHECK (id = 1), owner TEXT NOT NULL, at_ms INTEGER NOT NULL)`

// hubTables is the drop order of every table the hub migrations create:
// the children of the foreign keys first (the sessions and the
// machines reference the users), so the foreign keys never block the
// drop. The lock table is not here on purpose: it survives the reset.
var hubTables = []string{
	"pairings", "pairing_attempts", "ingest_deliveries", "shares",
	"agent_history", "agents_current", "hidden_counts", "working_hourly",
	"inventory_snapshots", "sessions", "machines",
	"login_failures", "invites",
	"users",
	"pairing_guard", "ingest_guard", "accounts_guard",
	"schema_migrations", "hub_meta",
}

// errLocked is the internal outcome of one attempt that finds the
// lock held by a fresh owner, or a steal that lost the race.
var errLocked = errors.New("hubtest: the lock is held")

// SQLD opens the sqld named by NAU_TEST_SQLD_URL (skipping the test
// when it is empty), takes the lock of the shared database, drops
// every table except the lock, applies the hub migrations from zero
// and registers the release of the lock at the end of the test. The
// lock stays held for the whole test, so no other package touches the
// schema in the middle of it: the tagged suites run without -p 1.
func SQLD(t *testing.T) store.Store {
	t.Helper()
	u := os.Getenv("NAU_TEST_SQLD_URL")
	if u == "" {
		t.Skip("set NAU_TEST_SQLD_URL to run the tagged suites against a real sqld")
	}
	c, err := sqld.New(u, os.Getenv("NAU_TEST_SQLD_TOKEN"))
	if err != nil {
		t.Fatalf("open the sqld client: %v", err)
	}
	owner := newOwner()
	if err := acquire(t.Context(), c, owner, time.Now); err != nil {
		t.Fatalf("take the lock of the shared database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := release(ctx, c, owner); err != nil {
			t.Logf("release the lock: %v", err)
		}
	})
	resetSchema(t, c)
	return c
}

// release frees the lock, deleting only the row of this owner: a lock
// that was abandoned, stolen and re-taken in the meantime belongs to
// another process and survives.
func release(ctx context.Context, st store.Store, owner string) error {
	_, err := st.Execute(ctx, store.Stmt{
		SQL:  `DELETE FROM hubtest_lock WHERE id = 1 AND owner = ?`,
		Args: []store.Value{owner},
	})
	if err != nil {
		return fmt.Errorf("release the lock: %w", err)
	}
	return nil
}

// resetSchema drops every table except the lock and applies the hub
// migrations from zero. It expects the lock to be held.
func resetSchema(t *testing.T, c store.Store) {
	t.Helper()
	if err := dropAll(c, t.Context()); err != nil {
		t.Fatalf("drop the tables of the previous runs: %v", err)
	}
	if err := (&migrate.Runner{Store: c, Now: time.Now}).Apply(t.Context()); err != nil {
		t.Fatalf("apply the migrations: %v", err)
	}
}

// DropAll drops every table of the database except the lock table and
// the internal tables of SQLite, whatever the foreign keys between
// them. It expects the caller to hold the lock: it is the way a test
// that checks the migrations themselves gets an empty database after
// the SQLD reset.
func DropAll(t *testing.T, st store.Store) {
	t.Helper()
	if err := dropAll(st, t.Context()); err != nil {
		t.Fatalf("drop the tables: %v", err)
	}
}

// maxDropPasses bounds the passes of dropAll: each pass drops at least
// one table or the reset gives up, so the bound only guards against a
// database that keeps growing under the reset.
const maxDropPasses = 64

// dropAll is the shared implementation of the reset. PRAGMA
// foreign_keys is per connection and the client opens one per call, so
// the foreign keys stay on: the drop goes in passes instead, one
// statement per table, the known tables of the hub first (children
// before parents) and the rest after them. A table whose drop the
// foreign keys block (a parent whose children still hold rows) is
// retried on the next pass, once its children are gone. A pass that
// drops nothing ends the reset with the last error.
func dropAll(st store.Store, ctx context.Context) error {
	for pass := 0; pass < maxDropPasses; pass++ {
		names, err := dropOrder(ctx, st)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return nil
		}
		dropped := 0
		var lastErr error
		for _, name := range names {
			if _, err := st.Execute(ctx, store.Stmt{SQL: `DROP TABLE IF EXISTS ` + quoteIdent(name)}); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr = fmt.Errorf("drop %s: %w", name, err)
				continue
			}
			dropped++
		}
		if dropped == 0 {
			return fmt.Errorf("drop the tables: no table could be dropped: %w", lastErr)
		}
	}
	return fmt.Errorf("drop the tables: still tables left after %d passes", maxDropPasses)
}

// quoteIdent quotes a table name for SQL, doubling the quotes inside.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// dropOrder lists the tables to drop, the lock table and the internal
// ones left out: the known tables of the hub in their order, then the
// others in the order of the listing.
func dropOrder(ctx context.Context, st store.Store) ([]string, error) {
	res, err := st.Execute(ctx, store.Stmt{
		SQL: `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'hubtest_lock'`,
	})
	if err != nil {
		return nil, fmt.Errorf("list the tables: %w", err)
	}
	present := make(map[string]bool, len(res.Rows))
	var listed []string
	for _, row := range res.Rows {
		name, ok := row[0].(string)
		if !ok {
			continue
		}
		present[name] = true
		listed = append(listed, name)
	}
	known := make(map[string]bool, len(hubTables))
	var names []string
	for _, name := range hubTables {
		known[name] = true
		if present[name] {
			names = append(names, name)
		}
	}
	for _, name := range listed {
		if !known[name] {
			names = append(names, name)
		}
	}
	return names, nil
}

// newOwner builds the identity of this process in the lock row: the
// pid and a random nonce. It never leaves the database.
func newOwner() string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		panic("hubtest: the nonce of the lock: " + err.Error())
	}
	return fmt.Sprintf("pid-%d-%s", os.Getpid(), hex.EncodeToString(nonce))
}

// acquire takes the lock: it inserts the row when it is free, and when
// the row already exists it waits for the owner to release it,
// stealing a lock that is abandoned (older than lockStale). The wait
// respects the context of the test and gives up after lockTimeout.
func acquire(ctx context.Context, st store.Store, owner string, now func() time.Time) error {
	if _, err := st.Execute(ctx, store.Stmt{SQL: lockTableCreate}); err != nil {
		return fmt.Errorf("create the lock table: %w", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tryTake(ctx, st, owner, now); err == nil {
			return nil
		} else if !errors.Is(err, errLocked) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the lock of the shared database is held by another suite and did not free in %s", lockTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockWait):
		}
	}
}

// tryTake is one attempt: the INSERT wins the free row, and a row that
// already exists is fresh (the owner waits) or abandoned (the owner
// steals it, guarded by the age so two steals cannot both win).
func tryTake(ctx context.Context, st store.Store, owner string, now func() time.Time) error {
	ms := now().UnixMilli()
	res, err := st.Execute(ctx, store.Stmt{
		SQL:  `INSERT OR IGNORE INTO hubtest_lock (id, owner, at_ms) VALUES (1, ?, ?)`,
		Args: []store.Value{owner, int64(ms)},
	})
	if err != nil {
		return fmt.Errorf("insert the lock: %w", err)
	}
	if res.Affected == 1 {
		return nil
	}
	row, err := st.Execute(ctx, store.Stmt{SQL: `SELECT owner, at_ms FROM hubtest_lock WHERE id = 1`})
	if err != nil {
		return fmt.Errorf("read the lock: %w", err)
	}
	if len(row.Rows) == 0 {
		return errLocked // the row was released in between: retry
	}
	theirs, _ := row.Rows[0][0].(string)
	if theirs == owner {
		return nil // we took it on an earlier attempt
	}
	at, ok := row.Rows[0][1].(int64)
	if !ok {
		return errLocked
	}
	age := now().UnixMilli() - at
	if age >= int64(lockStale.Milliseconds()) {
		upd, err := st.Execute(ctx, store.Stmt{
			SQL:  `UPDATE hubtest_lock SET owner = ?, at_ms = ? WHERE id = 1 AND at_ms <= ?`,
			Args: []store.Value{owner, int64(ms), now().UnixMilli() - int64(lockStale.Milliseconds())},
		})
		if err != nil {
			return fmt.Errorf("steal the abandoned lock: %w", err)
		}
		if upd.Affected == 1 {
			return nil
		}
	}
	return errLocked
}
