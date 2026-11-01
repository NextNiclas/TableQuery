package importer

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// DefaultSampleSize is the number of data rows used for type inference.
const DefaultSampleSize = 1000

// Analysis is the result of inspecting a source with given options.
type Analysis struct {
	Headers []string   // raw header cells (empty if no header row)
	Columns []Column   // suggested column mapping
	Sample  [][]string // raw data rows used for inference
}

// DecimalHint returns the tie-breaker decimal separator for the options.
func (o SourceOptions) DecimalHint(format Format) byte {
	if format == FormatCSV && o.Delimiter == ';' {
		return ','
	}
	return '.'
}

// Analyze reads the header and a sample of data rows and suggests names
// and types for all columns.
func Analyze(path string, opts SourceOptions, sampleSize int) (*Analysis, error) {
	opts.Normalize()
	if sampleSize <= 0 {
		sampleSize = DefaultSampleSize
	}
	r, err := OpenRows(path, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	a := &Analysis{}
	for rowNum := 1; len(a.Sample) < sampleSize; rowNum++ {
		row, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowNum, err)
		}
		if rowNum == opts.HeaderRow {
			a.Headers = row
		}
		if rowNum < opts.StartRow {
			continue
		}
		if opts.SkipEmpty && isEmptyRow(row) {
			continue
		}
		a.Sample = append(a.Sample, row)
	}

	width := len(a.Headers)
	for _, row := range a.Sample {
		if len(row) > width {
			width = len(row)
		}
	}
	headers := make([]string, width)
	copy(headers, a.Headers)
	names := NormalizeNames(headers)

	hint := opts.DecimalHint(DetectFormat(path))
	a.Columns = make([]Column, width)
	values := make([]string, 0, len(a.Sample))
	for i := 0; i < width; i++ {
		values = values[:0]
		for _, row := range a.Sample {
			if i < len(row) {
				values = append(values, row[i])
			}
		}
		typ, dec, layout := InferColumn(values, opts.Decimal, hint)
		src := headers[i]
		if src == "" {
			src = fmt.Sprintf("(column %d)", i+1)
		}
		a.Columns[i] = Column{
			Index:      i,
			Source:     src,
			Name:       names[i],
			Type:       typ,
			Include:    true,
			Decimal:    dec,
			DateLayout: layout,
		}
	}
	return a, nil
}

// IfExists modes for the target table.
const (
	IfExistsFail    = "fail"
	IfExistsReplace = "replace"
	IfExistsAppend  = "append"
)

// IfExistsModes lists the available modes in cycling order.
var IfExistsModes = []string{IfExistsFail, IfExistsReplace, IfExistsAppend}

// Target describes where and how rows are written.
type Target struct {
	Table    string
	IfExists string
	Columns  []Column
}

// Result summarizes an import.
type Result struct {
	Table    string
	Rows     int
	Warnings int      // values that could not be converted (stored as text)
	Samples  []string // first few warning messages
}

// Validate checks names for emptiness and duplicates.
func (t Target) Validate() error {
	if strings.TrimSpace(t.Table) == "" {
		return fmt.Errorf("table name must not be empty")
	}
	seen := map[string]bool{}
	n := 0
	for _, c := range t.Columns {
		if !c.Include {
			continue
		}
		n++
		name := strings.ToLower(strings.TrimSpace(c.Name))
		if name == "" {
			return fmt.Errorf("column %q has an empty name", c.Source)
		}
		if seen[name] {
			return fmt.Errorf("duplicate column name %q", c.Name)
		}
		seen[name] = true
	}
	if n == 0 {
		return fmt.Errorf("no columns selected for import")
	}
	return nil
}

// CreateTableSQL returns the CREATE TABLE statement for the target.
func (t Target) CreateTableSQL() string {
	var defs []string
	for _, c := range t.Columns {
		if c.Include {
			defs = append(defs, fmt.Sprintf("  %s %s", QuoteIdent(c.Name), c.Type.SQLType()))
		}
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", QuoteIdent(t.Table), strings.Join(defs, ",\n"))
}

// TableExists reports whether a table or view with the name exists.
func TableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','view') AND lower(name) = lower(?)`, name).Scan(&n)
	return n > 0, err
}

// Import reads all data rows from the source and writes them into the
// target table inside a single transaction. progress (may be nil) is called
// periodically with the number of rows imported so far.
func Import(ctx context.Context, db *sql.DB, path string, opts SourceOptions, t Target, progress func(int)) (*Result, error) {
	opts.Normalize()
	if t.IfExists == "" {
		t.IfExists = IfExistsFail
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}

	exists, err := TableExists(ctx, db, t.Table)
	if err != nil {
		return nil, err
	}
	if exists && t.IfExists == IfExistsFail {
		return nil, fmt.Errorf("table %q already exists (choose replace or append)", t.Table)
	}

	r, err := OpenRows(path, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if exists && t.IfExists == IfExistsReplace {
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+QuoteIdent(t.Table)); err != nil {
			return nil, err
		}
		exists = false
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, t.CreateTableSQL()); err != nil {
			return nil, fmt.Errorf("create table: %w", err)
		}
	}

	var cols []Column
	var names, marks []string
	for _, c := range t.Columns {
		if c.Include {
			cols = append(cols, c)
			names = append(names, QuoteIdent(c.Name))
			marks = append(marks, "?")
		}
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		QuoteIdent(t.Table), strings.Join(names, ", "), strings.Join(marks, ", ")))
	if err != nil {
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	res := &Result{Table: t.Table}
	args := make([]any, len(cols))
	for rowNum := 1; ; rowNum++ {
		row, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowNum, err)
		}
		if rowNum < opts.StartRow || (opts.SkipEmpty && isEmptyRow(row)) {
			continue
		}
		for i := range cols {
			raw := ""
			if cols[i].Index < len(row) {
				raw = row[cols[i].Index]
			}
			v, err := cols[i].Convert(raw)
			if err != nil {
				res.Warnings++
				if len(res.Samples) < 10 {
					res.Samples = append(res.Samples, fmt.Sprintf("row %d, %s: %v", rowNum, cols[i].Name, err))
				}
			}
			args[i] = v
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return nil, fmt.Errorf("row %d: %w", rowNum, err)
		}
		res.Rows++
		if progress != nil && res.Rows%1000 == 0 {
			progress(res.Rows)
		}
		if res.Rows%5000 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(res.Rows)
	}
	return res, nil
}
