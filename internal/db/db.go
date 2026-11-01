// Package db wraps the SQLite database used by TableQuery.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// MemoryPath is the display name of an in-memory database.
const MemoryPath = ":memory:"

// DB is an open SQLite database.
type DB struct {
	*sql.DB
	Path string // file path or MemoryPath
}

// IsMemory reports whether the database only lives in memory.
func (d *DB) IsMemory() bool { return d.Path == MemoryPath }

// Open opens (or creates) a database file. An empty path or ":memory:"
// opens an in-memory database.
func Open(path string) (*DB, error) {
	if path == "" {
		path = MemoryPath
	}
	dsn := path
	if path != MemoryPath {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection keeps an in-memory database alive and shared, and
	// serializes access for file databases.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(0)
	sqlDB.SetConnMaxIdleTime(0)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &DB{DB: sqlDB, Path: path}, nil
}

// SaveAs writes a copy of the database to a file (VACUUM INTO).
func (d *DB) SaveAs(ctx context.Context, path string) error {
	_, err := d.ExecContext(ctx, "VACUUM INTO "+quoteString(path))
	return err
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Column is a column of a table in the schema.
type Column struct {
	Name string
	Type string
}

// Table is a table or view in the schema.
type Table struct {
	Name    string
	IsView  bool
	Columns []Column
	Rows    int64 // -1 if unknown (views)
}

// Tables lists all user tables and views with their columns.
func (d *DB) Tables(ctx context.Context) ([]Table, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []Table
	for rows.Next() {
		var t Table
		var typ string
		if err := rows.Scan(&t.Name, &typ); err != nil {
			rows.Close()
			return nil, err
		}
		t.IsView = typ == "view"
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tables {
		t := &tables[i]
		cr, err := d.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?)`, t.Name)
		if err != nil {
			return nil, err
		}
		for cr.Next() {
			var c Column
			if err := cr.Scan(&c.Name, &c.Type); err != nil {
				cr.Close()
				return nil, err
			}
			t.Columns = append(t.Columns, c)
		}
		cr.Close()
		t.Rows = -1
		if !t.IsView {
			_ = d.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(t.Name)).Scan(&t.Rows)
		}
	}
	return tables, nil
}

// DropTable drops a table or view.
func (d *DB) DropTable(ctx context.Context, t Table) error {
	kind := "TABLE"
	if t.IsView {
		kind = "VIEW"
	}
	_, err := d.ExecContext(ctx, "DROP "+kind+" "+quoteIdent(t.Name))
	return err
}

// Result is the outcome of executing a statement.
type Result struct {
	SQL       string
	Columns   []string
	Rows      [][]Value
	Truncated bool  // more rows exist than were fetched
	Affected  int64 // for statements without result set
	IsQuery   bool
	Duration  time.Duration
}

// Value is a formatted cell value.
type Value struct {
	S    string
	Null bool
}

// returnsRows guesses whether a statement produces a result set.
func returnsRows(query string) bool {
	q := strings.ToUpper(StripComments(query))
	q = strings.TrimLeft(q, " \t\r\n(")
	for _, kw := range []string{"SELECT", "WITH", "PRAGMA", "EXPLAIN", "VALUES"} {
		if strings.HasPrefix(q, kw) {
			return true
		}
	}
	return strings.Contains(q, " RETURNING ")
}

// StripComments removes leading -- and /* */ comments and whitespace.
func StripComments(q string) string {
	for {
		q = strings.TrimSpace(q)
		switch {
		case strings.HasPrefix(q, "--"):
			if i := strings.IndexByte(q, '\n'); i >= 0 {
				q = q[i+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(q, "/*"):
			if i := strings.Index(q, "*/"); i >= 0 {
				q = q[i+2:]
			} else {
				return ""
			}
		default:
			return q
		}
	}
}

// Run executes a statement. For queries at most limit rows are fetched
// (limit <= 0: no limit).
func (d *DB) Run(ctx context.Context, query string, limit int) (*Result, error) {
	start := time.Now()
	res := &Result{SQL: query}
	if strings.TrimSpace(StripComments(query)) == "" {
		return nil, errEmpty
	}
	if !returnsRows(query) {
		r, err := d.ExecContext(ctx, query)
		if err != nil {
			return nil, err
		}
		res.Affected, _ = r.RowsAffected()
		res.Duration = time.Since(start)
		return res, nil
	}
	res.IsQuery = true
	err := d.Stream(ctx, query, func(cols []string) error {
		res.Columns = cols
		return nil
	}, func(row []Value) error {
		if limit > 0 && len(res.Rows) >= limit {
			res.Truncated = true
			return errStop
		}
		res.Rows = append(res.Rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

var (
	errStop  = errors.New("stop")
	errEmpty = errors.New("empty statement")
)

// Stream executes a query and calls onRow for each row. Returning errStop
// (internal) or any error from onRow stops iteration.
func (d *DB) Stream(ctx context.Context, query string, onCols func([]string) error, onRow func([]Value) error) error {
	rows, err := d.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	if err := onCols(cols); err != nil {
		return err
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		row := make([]Value, len(cols))
		for i, v := range raw {
			row[i] = Format(v)
		}
		if err := onRow(row); err != nil {
			if err == errStop {
				return nil
			}
			return err
		}
	}
	return rows.Err()
}

// Format converts a driver value to its display string.
func Format(v any) Value {
	switch x := v.(type) {
	case nil:
		return Value{Null: true}
	case []byte:
		return Value{S: string(x)}
	case string:
		return Value{S: x}
	case int64:
		return Value{S: strconv.FormatInt(x, 10)}
	case float64:
		if a := math.Abs(x); a >= 1e15 || (a != 0 && a < 1e-6) {
			return Value{S: strconv.FormatFloat(x, 'g', -1, 64)}
		}
		return Value{S: strconv.FormatFloat(x, 'f', -1, 64)}
	case bool:
		if x {
			return Value{S: "1"}
		}
		return Value{S: "0"}
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 && x.Nanosecond() == 0 {
			return Value{S: x.Format("2006-01-02")}
		}
		return Value{S: x.Format("2006-01-02 15:04:05")}
	default:
		return Value{S: fmt.Sprint(x)}
	}
}
