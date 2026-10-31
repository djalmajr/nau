package hubtest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

// fakeLockStore is the store of the lock unit tests: it simulates the
// single row of the lock with the same statements of the helper, and
// can hold the lock for a scripted number of reads or lose the race of
// a steal. It runs the whole loop hermetically, without a server.
type fakeLockStore struct {
	t            *testing.T
	holder       string // the current owner of the row ("" when it is free)
	holderAtMS   int64
	inserts      int
	updates      int
	deletedOwner []string
	loseSteal    bool // the first steal loses the race
	releaseAfter int  // the row frees after this many reads (0: never)
}

func (f *fakeLockStore) Execute(ctx context.Context, s store.Stmt) (store.Result, error) {
	switch {
	case strings.Contains(s.SQL, "CREATE TABLE IF NOT EXISTS hubtest_lock"):
		return store.Result{}, nil
	case strings.Contains(s.SQL, "INSERT OR IGNORE INTO hubtest_lock"):
		f.inserts++
		if f.holder == "" {
			f.holder = s.Args[0].(string)
			f.holderAtMS = s.Args[1].(int64)
			return store.Result{Affected: 1}, nil
		}
		return store.Result{Affected: 0}, nil
	case strings.Contains(s.SQL, "SELECT owner, at_ms FROM hubtest_lock WHERE id = 1"):
		if f.releaseAfter > 0 {
			f.releaseAfter--
			if f.releaseAfter == 0 {
				f.holder = ""
			}
		}
		if f.holder == "" {
			return store.Result{}, nil
		}
		return store.Result{Rows: []store.Row{{f.holder, f.holderAtMS}}}, nil
	case strings.Contains(s.SQL, "UPDATE hubtest_lock SET owner"):
		f.updates++
		if f.loseSteal {
			f.loseSteal = false
			return store.Result{Affected: 0}, nil
		}
		threshold, _ := s.Args[2].(int64)
		if f.holder != "" && f.holderAtMS <= threshold {
			f.holder = s.Args[0].(string)
			f.holderAtMS = s.Args[1].(int64)
			return store.Result{Affected: 1}, nil
		}
		return store.Result{Affected: 0}, nil
	case strings.Contains(s.SQL, "DELETE FROM hubtest_lock WHERE id = 1 AND owner"):
		own := s.Args[0].(string)
		f.deletedOwner = append(f.deletedOwner, own)
		if f.holder == own {
			f.holder = ""
			return store.Result{Affected: 1}, nil
		}
		return store.Result{Affected: 0}, nil
	}
	f.t.Fatalf("unexpected SQL in the lock fake: %s", s.SQL)
	return store.Result{}, nil
}

func (f *fakeLockStore) Batch(ctx context.Context, stmts []store.Stmt) ([]store.Result, error) {
	f.t.Fatalf("the lock code must not use a batch, got %d statements", len(stmts))
	return nil, nil
}

func (f *fakeLockStore) Ping(ctx context.Context) error {
	return nil
}

// quietWait makes the retry loop of the tests fast: the pause between
// the attempts goes to 1 ms (the loop still runs, the wait does not).
func quietWait(t *testing.T) {
	t.Helper()
	prev := lockWait
	lockWait = time.Millisecond
	t.Cleanup(func() { lockWait = prev })
}

func TestAcquireTakesTheFreeLock(t *testing.T) {
	f := &fakeLockStore{t: t}
	if err := acquire(context.Background(), f, "owner-a", time.Now); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if f.inserts != 1 {
		t.Fatalf("inserts = %d, want 1 (the free row is taken on the first attempt)", f.inserts)
	}
	if f.holder != "owner-a" {
		t.Fatalf("holder = %q, want owner-a", f.holder)
	}
}

func TestAcquireWaitsThenTakesTheHeldLock(t *testing.T) {
	quietWait(t)
	f := &fakeLockStore{t: t, holder: "owner-b", holderAtMS: time.Now().UnixMilli(), releaseAfter: 2}
	if err := acquire(context.Background(), f, "owner-a", time.Now); err != nil {
		t.Fatalf("acquire: %v (it must wait and take the lock after the release)", err)
	}
	if f.inserts < 2 {
		t.Fatalf("inserts = %d, want at least 2 (the held row, then the free one)", f.inserts)
	}
	if f.holder != "owner-a" {
		t.Fatalf("holder = %q, want owner-a", f.holder)
	}
}

func TestAcquireTakesTheAbandonedLock(t *testing.T) {
	quietWait(t)
	f := &fakeLockStore{t: t, holder: "owner-b", holderAtMS: time.Now().Add(-6 * time.Minute).UnixMilli()}
	if err := acquire(context.Background(), f, "owner-a", time.Now); err != nil {
		t.Fatalf("acquire: %v (an abandoned lock must be taken, not waited)", err)
	}
	if f.updates != 1 {
		t.Fatalf("steals = %d, want 1 (the steal, no retry)", f.updates)
	}
	if f.holder != "owner-a" {
		t.Fatalf("holder = %q, want owner-a", f.holder)
	}
}

func TestAcquireRetriesWhenTheStealRaces(t *testing.T) {
	quietWait(t)
	f := &fakeLockStore{t: t, holder: "owner-b", holderAtMS: time.Now().Add(-6 * time.Minute).UnixMilli(), loseSteal: true}
	if err := acquire(context.Background(), f, "owner-a", time.Now); err != nil {
		t.Fatalf("acquire: %v (a lost steal must be retried)", err)
	}
	if f.updates != 2 {
		t.Fatalf("steals = %d, want 2 (the lost one, then the one that wins)", f.updates)
	}
	if f.inserts != 2 {
		t.Fatalf("inserts = %d, want 2 (one per attempt)", f.inserts)
	}
	if f.holder != "owner-a" {
		t.Fatalf("holder = %q, want owner-a", f.holder)
	}
}

func TestAcquireStopsWhenTheContextIsDone(t *testing.T) {
	quietWait(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeLockStore{t: t, holder: "owner-b", holderAtMS: time.Now().UnixMilli()}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := acquire(ctx, f, "owner-a", time.Now); err == nil {
		t.Fatal("acquire: want the error of the cancelled context, got nil")
	}
}

func TestReleaseDeletesOnlyTheOwnRow(t *testing.T) {
	f := &fakeLockStore{t: t, holder: "owner-a", holderAtMS: time.Now().UnixMilli()}
	ctx := context.Background()
	if err := release(ctx, f, "owner-b"); err != nil {
		t.Fatalf("release of the foreign owner: %v", err)
	}
	if f.holder != "owner-a" {
		t.Fatalf("holder = %q, want owner-a (the foreign release must not free the lock)", f.holder)
	}
	if err := release(ctx, f, "owner-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if f.holder != "" {
		t.Fatalf("holder = %q, want empty (the own release frees the lock)", f.holder)
	}
	if len(f.deletedOwner) != 2 || f.deletedOwner[0] != "owner-b" || f.deletedOwner[1] != "owner-a" {
		t.Fatalf("deleted owners = %v, want [owner-b owner-a] (the argument is always the own owner)", f.deletedOwner)
	}
}

func TestSQLDSkipsWithoutURL(t *testing.T) {
	t.Setenv("NAU_TEST_SQLD_URL", "")
	called := false
	t.Run("inner", func(it *testing.T) {
		SQLD(it)
		called = true
	})
	if called {
		t.Fatal("SQLD returned without skipping: the empty URL must skip the test")
	}
}
