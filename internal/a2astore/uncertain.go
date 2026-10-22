package a2astore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/a2acore"
)

// UncertainSummary is the optional A2A block of the local doctor output.
type UncertainSummary struct {
	Uncertain int    `json:"uncertain"`
	Oldest    *int64 `json:"uncertain_oldest_s,omitempty"`
}

// ReadUncertain reads every uncertain delivery without installing or migrating
// a schema. A missing file or A2A table returns nil; old schemas use last_attempt.
// The SQLite connection is read-only and sees the live WAL of a running serve.
func ReadUncertain(ctx context.Context, path string, now time.Time) (*UncertainSummary, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("a2astore: database is not a regular file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(filepath.ToSlash(abs), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	query := "?mode=ro&_pragma=busy_timeout(10000)"
	db, err := sql.Open("sqlite", "file:"+strings.Join(parts, "/")+query)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='a2a_deliveries'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('a2a_deliveries') WHERE name='last_attempt_end'`).Scan(&n); err != nil {
		return nil, err
	}
	stamp := "last_attempt"
	if n > 0 {
		stamp = "CASE WHEN julianday(last_attempt_end) >= julianday(last_attempt) THEN last_attempt_end ELSE last_attempt END"
	}
	filter := ""
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='a2a_tasks'`).Scan(&n); err != nil {
		return nil, err
	}
	var args []any
	if n > 0 {
		filter = ` AND EXISTS (SELECT 1 FROM a2a_tasks t WHERE t.task_id=d.task_id AND t.state NOT IN (?, ?, ?, ?))`
		args = []any{a2acore.StateCompleted, a2acore.StateCanceled, a2acore.StateFailed, a2acore.StateRejected}
	}
	rows, err := db.QueryContext(ctx, `SELECT `+stamp+` FROM a2a_deliveries d WHERE d.state='uncertain'`+filter, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summary := &UncertainSummary{}
	var oldest int64
	for rows.Next() {
		var timestamp sql.NullString
		if err := rows.Scan(&timestamp); err != nil {
			return nil, err
		}
		summary.Uncertain++
		if age := uncertainAge(timestamp.String, now); age > oldest {
			oldest = age
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if summary.Uncertain > 0 {
		summary.Oldest = &oldest
	}
	return summary, nil
}
