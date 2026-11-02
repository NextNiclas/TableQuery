package importer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TableColumn is a column of an existing table.
type TableColumn struct {
	Name string
	Type string // declared SQL type
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// TableColumns returns the columns of an existing table in table order.
func TableColumns(ctx context.Context, q queryer, table string) ([]TableColumn, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []TableColumn
	for rows.Next() {
		var c TableColumn
		if err := rows.Scan(&c.Name, &c.Type); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// ListTables returns the names of all tables (no views) in the database.
func ListTables(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// objectType returns "table", "view" or "" for the name.
func objectType(ctx context.Context, db *sql.DB, name string) (string, error) {
	var typ string
	err := db.QueryRowContext(ctx,
		`SELECT type FROM sqlite_master WHERE type IN ('table','view') AND lower(name) = lower(?)`, name).Scan(&typ)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return typ, err
}

// columnTypeFor maps a declared SQLite type to the import type used for
// converting values.
func columnTypeFor(sqlType string) ColType {
	t := strings.ToUpper(sqlType)
	switch {
	case strings.Contains(t, "INT"):
		return TypeInteger
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"),
		strings.Contains(t, "NUM"), strings.Contains(t, "DEC"):
		return TypeReal
	default:
		return TypeText
	}
}

// MapToTable maps the (included) file columns onto the columns of an
// existing table. A file column matches a table column by its (possibly
// edited) target name, else by its original header or the normalized header,
// all case-insensitive. The result lists the matched columns in table order,
// renamed to the table's names and converted to the table's types. ignored
// are file columns without counterpart, nulled are table columns that get no
// value (NULL/default). The column named sourceColumn is filled with the file
// name and is neither matched nor reported.
func MapToTable(fileCols []Column, table []TableColumn, sourceColumn string) (mapped []Column, ignored, nulled []string) {
	used := make([]bool, len(fileCols))
	find := func(match func(Column) bool) int {
		for i, c := range fileCols {
			if c.Include && !used[i] && match(c) {
				return i
			}
		}
		return -1
	}
	for _, tc := range table {
		if sourceColumn != "" && strings.EqualFold(tc.Name, sourceColumn) {
			continue
		}
		idx := find(func(c Column) bool { return strings.EqualFold(c.Name, tc.Name) })
		if idx < 0 {
			idx = find(func(c Column) bool {
				return strings.EqualFold(c.Source, tc.Name) || strings.EqualFold(NormalizeName(c.Source), tc.Name)
			})
		}
		if idx < 0 {
			nulled = append(nulled, tc.Name)
			continue
		}
		used[idx] = true
		c := fileCols[idx]
		c.Name = tc.Name
		// Dates are kept as ISO text, so a TEXT column still gets the
		// normalization when the file column was detected as a date.
		if t := columnTypeFor(tc.Type); !(t == TypeText && (c.Type == TypeDate || c.Type == TypeDateTime)) {
			c.Type = t
		}
		mapped = append(mapped, c)
	}
	for i, c := range fileCols {
		if c.Include && !used[i] {
			ignored = append(ignored, c.Source)
		}
	}
	return mapped, ignored, nulled
}

// AppendPlan describes how a file would be appended to an existing table.
type AppendPlan struct {
	Table     string
	Mapped    []Column // matched columns, named like the table's
	Ignored   []string // file columns without a table column
	Nulled    []string // table columns without a file column
	TableCols []TableColumn
}

// PlanAppend computes the column mapping for appending to an existing table.
func PlanAppend(ctx context.Context, db *sql.DB, table string, fileCols []Column, sourceColumn string) (*AppendPlan, error) {
	typ, err := objectType(ctx, db, table)
	if err != nil {
		return nil, err
	}
	switch typ {
	case "":
		return nil, fmt.Errorf("table %q does not exist", table)
	case "view":
		return nil, fmt.Errorf("%q is a view; rows can only be added to tables", table)
	}
	tcols, err := TableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	if sourceColumn != "" {
		found := false
		for _, tc := range tcols {
			found = found || strings.EqualFold(tc.Name, sourceColumn)
		}
		if !found {
			return nil, fmt.Errorf("table %q has no column %q for the source file name", table, sourceColumn)
		}
	}
	p := &AppendPlan{Table: table, TableCols: tcols}
	p.Mapped, p.Ignored, p.Nulled = MapToTable(fileCols, tcols, sourceColumn)
	if len(p.Mapped) == 0 {
		names := make([]string, len(tcols))
		for i, tc := range tcols {
			names[i] = tc.Name
		}
		return nil, fmt.Errorf("no column of the file matches a column of table %q (table columns: %s)", table, strings.Join(names, ", "))
	}
	return p, nil
}
