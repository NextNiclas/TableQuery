package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// IfExists modes for CreateFromQuery.
const (
	IfExistsFail    = "fail"
	IfExistsReplace = "replace"
	IfExistsAppend  = "append"
)

// DeriveOptions control CreateFromQuery.
type DeriveOptions struct {
	View     bool   // create a view instead of a table
	IfExists string // fail (default), replace or append (tables only)
}

// QueryStatement returns the last statement of a script if it is a
// row-returning query (SELECT, WITH, VALUES, ...).
func QueryStatement(script string) (string, error) {
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		return "", errEmpty
	}
	q := stmts[len(stmts)-1]
	if !returnsRows(q) || strings.HasPrefix(strings.ToUpper(StripComments(q)), "PRAGMA") {
		return "", errors.New("the last statement must be a query (SELECT, WITH or VALUES)")
	}
	return q, nil
}

// CreateFromQuery stores the result of a query as a new table (CREATE TABLE
// AS) or view. It returns the number of rows in the table (-1 for views).
func (d *DB) CreateFromQuery(ctx context.Context, name, query string, opts DeriveOptions) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("name must not be empty")
	}
	q, err := QueryStatement(query)
	if err != nil {
		return 0, err
	}
	mode := opts.IfExists
	if mode == "" {
		mode = IfExistsFail
	}
	if mode != IfExistsFail && mode != IfExistsReplace && mode != IfExistsAppend {
		return 0, fmt.Errorf("invalid if-exists mode %q", mode)
	}
	if opts.View && mode == IfExistsAppend {
		return 0, errors.New("append is not possible for views")
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var existing, existingName string
	err = tx.QueryRowContext(ctx,
		`SELECT type, name FROM sqlite_master WHERE type IN ('table','view') AND lower(name) = lower(?)`, name).
		Scan(&existing, &existingName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	ident := quoteIdent(name)
	create := true
	if existing != "" {
		switch mode {
		case IfExistsFail:
			return 0, fmt.Errorf("%s %q already exists (choose replace or append)", existing, existingName)
		case IfExistsReplace:
			if _, err := tx.ExecContext(ctx, "DROP "+strings.ToUpper(existing)+" "+quoteIdent(existingName)); err != nil {
				return 0, err
			}
		case IfExistsAppend:
			if existing != "table" {
				return 0, fmt.Errorf("cannot append to view %q", existingName)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+quoteIdent(existingName)+" "+q); err != nil {
				return 0, err
			}
			ident = quoteIdent(existingName)
			create = false
		}
	}
	if create {
		kind := "TABLE"
		if opts.View {
			kind = "VIEW"
		}
		if _, err := tx.ExecContext(ctx, "CREATE "+kind+" "+ident+" AS "+q); err != nil {
			return 0, err
		}
	}
	var rows int64 = -1
	if !opts.View {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+ident).Scan(&rows); err != nil {
			return 0, err
		}
	}
	return rows, tx.Commit()
}
