package importer

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"path/filepath"
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
	Table        string
	IfExists     string
	Columns      []Column
	SourceColumn string // if set, an extra TEXT column receives the source file name
}

// FileResult summarizes the import of one file.
type FileResult struct {
	Path     string
	Rows     int
	Warnings int
	Missing  []string // target columns not found in this file (stored as NULL)
	Extra    []string // columns of this file that are not imported
}

// Result summarizes an import.
type Result struct {
	Table    string
	Rows     int
	Warnings int      // values that could not be converted (stored as text)
	Samples  []string // first few warning messages
	Files    []FileResult

	// Appending to an existing table: file columns that had no counterpart
	// in the table, and table columns that received no value.
	Ignored []string
	Nulled  []string
}

// Progress is reported while importing.
type Progress struct {
	Rows  int // rows imported so far (all files)
	File  int // 1-based index of the current file
	Files int // number of files
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
	if t.SourceColumn != "" && seen[strings.ToLower(strings.TrimSpace(t.SourceColumn))] {
		return fmt.Errorf("source column name %q is already used by an imported column", t.SourceColumn)
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
	if t.SourceColumn != "" {
		defs = append(defs, fmt.Sprintf("  %s TEXT", QuoteIdent(t.SourceColumn)))
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

// MapColumns maps the columns of the first file onto another file by header:
// case-insensitive source header first, then the normalized name. Columns
// not found get Index -1 (NULL) and are reported in missing; headers that
// match no column of first are reported in extra. Without headers (nil) the
// columns are mapped by position.
func MapColumns(first []Column, headers []string) (cols []Column, missing, extra []string) {
	cols = append([]Column(nil), first...)
	if headers == nil {
		return cols, nil, nil
	}
	byName := map[string]int{}
	byNorm := map[string]int{}
	for i, h := range headers {
		if k := strings.ToLower(strings.TrimSpace(h)); k != "" {
			if _, ok := byName[k]; !ok {
				byName[k] = i
			}
		}
		if k := NormalizeName(h); k != "" {
			if _, ok := byNorm[k]; !ok {
				byNorm[k] = i
			}
		}
	}
	used := map[int]bool{}
	for i := range cols {
		idx, ok := byName[strings.ToLower(strings.TrimSpace(cols[i].Source))]
		if !ok {
			idx, ok = byNorm[NormalizeName(cols[i].Source)]
		}
		if !ok {
			cols[i].Index = -1
			if cols[i].Include {
				missing = append(missing, cols[i].Name)
			}
			continue
		}
		cols[i].Index = idx
		used[idx] = true
	}
	for i, h := range headers {
		if !used[i] && strings.TrimSpace(h) != "" {
			extra = append(extra, strings.TrimSpace(h))
		}
	}
	return cols, missing, extra
}

// fileHeaders reads the header row of a file (nil if there is none).
func fileHeaders(path string, opts SourceOptions) ([]string, error) {
	if opts.HeaderRow == 0 {
		return nil, nil
	}
	rows, err := ReadRaw(path, opts, opts.HeaderRow)
	if err != nil {
		return nil, err
	}
	if len(rows) < opts.HeaderRow {
		return nil, fmt.Errorf("file has no header row %d", opts.HeaderRow)
	}
	return rows[opts.HeaderRow-1], nil
}

// detectDecimals re-detects the decimal separator of the numeric columns for
// a further file (e.g. 1,5 in a German export vs 1.5 in the first file).
func detectDecimals(path string, opts SourceOptions, cols []Column) error {
	numeric := false
	for _, c := range cols {
		if c.Include && c.Index >= 0 && (c.Type == TypeInteger || c.Type == TypeReal) {
			numeric = true
		}
	}
	if !numeric {
		return nil
	}
	a, err := Analyze(path, opts, DefaultSampleSize)
	if err != nil {
		return err
	}
	for i := range cols {
		c := &cols[i]
		if !c.Include || c.Index < 0 || (c.Type != TypeInteger && c.Type != TypeReal) {
			continue
		}
		var vals []string
		for _, row := range a.Sample {
			if c.Index < len(row) {
				vals = append(vals, row[c.Index])
			}
		}
		c.Decimal = DetectDecimal(vals, c.Decimal)
	}
	return nil
}

// PlanFiles reports, without importing, how the columns of the first file map
// onto every file (missing and extra columns).
func PlanFiles(paths []string, opts SourceOptions, cols []Column) ([]FileResult, error) {
	opts.Normalize()
	var out []FileResult
	for i, path := range paths {
		fr := FileResult{Path: path}
		if i > 0 {
			fo, err := OptionsFor(path, opts, false)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			headers, err := fileHeaders(path, fo)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			_, fr.Missing, fr.Extra = MapColumns(cols, headers)
		}
		out = append(out, fr)
	}
	return out, nil
}

// CommonTableName suggests a table name for several files: the common
// prefix of the file names without trailing digits/separators.
func CommonTableName(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	prefix := ""
	for i, p := range paths {
		b := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if i == 0 {
			prefix = b
			continue
		}
		n := 0
		for n < len(prefix) && n < len(b) && prefix[n] == b[n] {
			n++
		}
		prefix = prefix[:n]
	}
	prefix = strings.TrimRight(prefix, "0123456789_-. ")
	if n := NormalizeName(prefix); n != "" {
		return n
	}
	return ""
}

// Import imports a single file; see ImportFiles.
func Import(ctx context.Context, db *sql.DB, path string, opts SourceOptions, t Target, progress func(Progress)) (*Result, error) {
	return ImportFiles(ctx, db, []string{path}, opts, t, progress)
}

// ImportFiles reads all data rows of the files and writes them into the
// target table inside a single transaction (all or nothing). t.Columns
// describe the first file; other files are mapped onto them by header (see
// MapColumns). progress (may be nil) is called periodically.
func ImportFiles(ctx context.Context, db *sql.DB, paths []string, opts SourceOptions, t Target, progress func(Progress)) (*Result, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no files to import")
	}
	if err := CheckSameFormat(paths); err != nil {
		return nil, err
	}
	opts.Normalize()
	if t.IfExists == "" {
		t.IfExists = IfExistsFail
	}
	t.SourceColumn = strings.TrimSpace(t.SourceColumn)
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
	// Appending maps the file onto the columns and types of the existing
	// table instead of using the names derived from the file.
	var plan *AppendPlan
	if exists && t.IfExists == IfExistsAppend {
		if plan, err = PlanAppend(ctx, db, t.Table, t.Columns, t.SourceColumn); err != nil {
			return nil, err
		}
		t.Columns = plan.Mapped
	}

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
	if t.SourceColumn != "" {
		names = append(names, QuoteIdent(t.SourceColumn))
		marks = append(marks, "?")
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		QuoteIdent(t.Table), strings.Join(names, ", "), strings.Join(marks, ", ")))
	if err != nil {
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	multi := len(paths) > 1
	res := &Result{Table: t.Table}
	if plan != nil {
		res.Ignored, res.Nulled = plan.Ignored, plan.Nulled
	}
	for i, path := range paths {
		fail := func(err error) (*Result, error) {
			if multi {
				err = fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			return nil, err
		}
		fo, err := OptionsFor(path, opts, i == 0)
		if err != nil {
			return fail(err)
		}
		fileCols, missing, extra := cols, []string(nil), []string(nil)
		if i > 0 {
			headers, err := fileHeaders(path, fo)
			if err != nil {
				return fail(err)
			}
			fileCols, missing, extra = MapColumns(cols, headers)
			if fo.Decimal == 0 {
				if err := detectDecimals(path, fo, fileCols); err != nil {
					return fail(err)
				}
			}
		}
		var source any
		if t.SourceColumn != "" {
			source = filepath.Base(path)
		}
		fr := FileResult{Path: path, Missing: missing, Extra: extra}
		report := func(n int) {
			if progress != nil {
				progress(Progress{Rows: res.Rows + n, File: i + 1, Files: len(paths)})
			}
		}
		if err := importFile(ctx, stmt, path, fo, fileCols, source, res, &fr, multi, report); err != nil {
			return fail(err)
		}
		res.Rows += fr.Rows
		res.Warnings += fr.Warnings
		res.Files = append(res.Files, fr)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(Progress{Rows: res.Rows, File: len(paths), Files: len(paths)})
	}
	return res, nil
}

// importFile inserts the data rows of one file.
func importFile(ctx context.Context, stmt *sql.Stmt, path string, opts SourceOptions, cols []Column,
	source any, res *Result, fr *FileResult, multi bool, report func(int)) error {
	r, err := OpenRows(path, opts)
	if err != nil {
		return err
	}
	defer r.Close()

	n := len(cols)
	if source != nil {
		n++
	}
	args := make([]any, n)
	if source != nil {
		args[n-1] = source
	}
	for rowNum := 1; ; rowNum++ {
		row, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("row %d: %w", rowNum, err)
		}
		if rowNum < opts.StartRow || (opts.SkipEmpty && isEmptyRow(row)) {
			continue
		}
		for i := range cols {
			raw := ""
			if idx := cols[i].Index; idx >= 0 && idx < len(row) {
				raw = row[idx]
			}
			v, err := cols[i].Convert(raw)
			if err != nil {
				fr.Warnings++
				if len(res.Samples) < 10 {
					msg := fmt.Sprintf("row %d, %s: %v", rowNum, cols[i].Name, err)
					if multi {
						msg = filepath.Base(path) + " " + msg
					}
					res.Samples = append(res.Samples, msg)
				}
			}
			args[i] = v
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return fmt.Errorf("row %d: %w", rowNum, err)
		}
		fr.Rows++
		if fr.Rows%1000 == 0 {
			report(fr.Rows)
		}
		if fr.Rows%5000 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
}
