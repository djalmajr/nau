// Package store defines the storage interface of the hub core and the
// value types it moves. Values are one of: nil, int64, float64, string
// or []byte.
package store

import (
	"context"
	"fmt"
)

// Value is one SQL value: nil, int64, float64, string or []byte.
type Value any

// Stmt is one statement: the SQL text and its positional arguments, in
// order.
type Stmt struct {
	SQL  string
	Args []Value
}

// Row is one result row, the values in column order.
type Row []Value

// Result is the outcome of one statement: the column names, the rows,
// the number of affected rows and the last inserted rowid (0 when the
// statement has none).
type Result struct {
	Cols            []string
	Rows            []Row
	Affected        int64
	LastInsertRowID int64
}

// Store is the hub storage interface. Execute runs one statement. Batch
// runs the statements atomically: everything or nothing, and an error
// means nothing was applied. Ping reports the database reachability.
type Store interface {
	Execute(ctx context.Context, stmt Stmt) (Result, error)
	Batch(ctx context.Context, stmts []Stmt) ([]Result, error)
	Ping(ctx context.Context) error
}

// StatementError identifies one statement that failed in a store call.
// Index is the number of the statement: one-based within the list
// passed to Batch (the same number the error text reports) and zero
// for the single statement of Execute. Code carries an error code the
// store validated for echoing; it is empty when there is none. The
// error never quotes server text.
type StatementError struct {
	Index int
	Code  string
}

// Error is the fixed text of the failure: the statement number, the
// fixed words and, when present, the validated code. Nothing from the
// server is quoted.
func (e StatementError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%d: server error (code %s)", e.Index, e.Code)
	}
	return fmt.Sprintf("%d: server error", e.Index)
}
