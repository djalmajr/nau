// Package migrate applies the hub schema migrations. The migrations
// are embedded .sql files, numbered (0001_init.sql and so on) and
// applied in order. Each migration is applied once, in one atomic batch
// together with its registration in the schema_migrations table.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/nau/internal/hub/store"
)

//go:embed *.sql
var embedded embed.FS

// fileNamePattern is the name of a migration file: <version>_<name>.sql.
var fileNamePattern = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.sql$`)

// schemaMigrationsCheck reports whether the schema_migrations table
// exists, without failing on a fresh database.
const schemaMigrationsCheck = `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`

// schemaMigrationsList is the list of the applied versions.
const schemaMigrationsList = `SELECT version FROM schema_migrations`

// registration inserts one applied version into schema_migrations.
const registration = `INSERT INTO schema_migrations (version, applied_ms) VALUES (?, ?)`

// Migration is one migration: its version, its file name and its SQL
// text.
type Migration struct {
	// Version is the numeric version of the migration.
	Version int
	// Name is the file name without the extension.
	Name string
	// SQL is the full text of the file.
	SQL string
}

// Load reads the embedded migrations, validates the file names and
// returns them sorted by version. Repeated versions are an error.
func Load() ([]Migration, error) {
	entries, err := embedded.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("migrate: read embedded files: %v", err)
	}
	var out []Migration
	seen := make(map[int]bool)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileNamePattern.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: %q is not a migration file name", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migrate: %q: %v", e.Name(), err)
		}
		if seen[v] {
			return nil, fmt.Errorf("migrate: version %d is repeated", v)
		}
		seen[v] = true
		raw, err := embedded.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %q: %v", e.Name(), err)
		}
		out = append(out, Migration{Version: v, Name: m[0][:len(m[0])-4], SQL: string(raw)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Runner applies the migrations of the hub schema.
type Runner struct {
	// Store is the storage to apply the migrations to.
	Store store.Store
	// Now is the clock; the registration uses its milliseconds.
	Now func() time.Time
	// Migrations is the set to apply; nil loads the embedded ones.
	Migrations []Migration
}

// Apply applies every migration that is not applied yet, in version
// order. Each migration and its registration run in one atomic batch,
// so a failed migration leaves neither its statements nor its
// registration behind.
func (r *Runner) Apply(ctx context.Context) error {
	var migrations []Migration
	if r.Migrations == nil {
		var err error
		migrations, err = Load()
		if err != nil {
			return err
		}
	} else {
		// Copy: Apply must not reorder the caller's slice.
		migrations = make([]Migration, len(r.Migrations))
		copy(migrations, r.Migrations)
	}
	// The embedded set is already ordered; a provided set is sorted so
	// the order never depends on the caller.
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	now := r.Now
	if now == nil {
		now = time.Now
	}
	applied, err := r.appliedVersions(ctx)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		stmts := splitStatements(m.SQL)
		if len(stmts) == 0 {
			return fmt.Errorf("migrate: %s: no statements", m.Name)
		}
		ms := int64(now().UnixMilli())
		version := int64(m.Version)
		stmts = append(stmts, store.Stmt{SQL: registration, Args: []store.Value{version, ms}})
		if _, err := r.Store.Batch(ctx, stmts); err != nil {
			return fmt.Errorf("migrate: %s: %v", m.Name, err)
		}
		applied[m.Version] = true
	}
	return nil
}

// appliedVersions reads the applied versions; a database without the
// schema_migrations table reports none.
func (r *Runner) appliedVersions(ctx context.Context) (map[int]bool, error) {
	out := make(map[int]bool)
	res, err := r.Store.Execute(ctx, store.Stmt{SQL: schemaMigrationsCheck})
	if err != nil {
		return nil, fmt.Errorf("migrate: check schema_migrations: %v", err)
	}
	if len(res.Rows) == 0 {
		return out, nil
	}
	res, err = r.Store.Execute(ctx, store.Stmt{SQL: schemaMigrationsList})
	if err != nil {
		return nil, fmt.Errorf("migrate: list applied versions: %v", err)
	}
	for _, row := range res.Rows {
		if len(row) == 0 {
			continue
		}
		v, ok := row[0].(int64)
		if !ok {
			return nil, fmt.Errorf("migrate: applied version is not an integer")
		}
		out[int(v)] = true
	}
	return out, nil
}

// splitStatements splits the SQL text of a migration file on the
// statement terminators. A terminator is a semicolon that is not part
// of a single-quoted string (” is the quote escape), a double-quoted
// identifier ("" is the quote escape), a line comment (-- to the end
// of the line) or a block comment (/* to */). The statements keep
// their original text, comments included; empty chunks and chunks of
// only comments are dropped.
func splitStatements(sql string) []store.Stmt {
	var out []store.Stmt
	start := 0
	content := false
	inSingle, inDouble, inLine, inBlock := false, false, false, false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inBlock:
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				i++
				inBlock = false
			}
		case inSingle:
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++ // the escaped quote
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if c == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					i++ // the escaped quote
				} else {
					inDouble = false
				}
			}
		default:
			switch {
			case c == '\'':
				inSingle = true
				content = true
			case c == '"':
				inDouble = true
				content = true
			case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
				inLine = true
				i++
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				inBlock = true
				i++
			case c == ';':
				// A terminator outside quotes and comments: the chunk
				// before it is one statement, without the terminator.
				if content && strings.TrimSpace(sql[start:i]) != "" {
					out = append(out, store.Stmt{SQL: strings.TrimSpace(sql[start:i])})
				}
				start = i + 1
				content = false
			default:
				if !isSpaceByte(c) {
					content = true
				}
			}
		}
	}
	if content && strings.TrimSpace(sql[start:]) != "" {
		out = append(out, store.Stmt{SQL: strings.TrimSpace(sql[start:])})
	}
	return out
}

// isSpaceByte reports whether the byte is ASCII whitespace.
func isSpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}
