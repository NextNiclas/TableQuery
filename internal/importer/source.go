package importer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Format is the kind of input file.
type Format int

const (
	FormatUnknown Format = iota
	FormatCSV
	FormatXLSX
)

func (f Format) String() string {
	switch f {
	case FormatCSV:
		return "CSV"
	case FormatXLSX:
		return "Excel"
	default:
		return "unknown"
	}
}

// DetectFormat guesses the format from the file extension.
func DetectFormat(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm", ".xltx", ".xltm":
		return FormatXLSX
	case ".csv", ".tsv", ".txt", ".tab", ".dat":
		return FormatCSV
	case ".xls":
		return FormatUnknown
	default:
		return FormatCSV
	}
}

// Encodings supported for CSV input.
var Encodings = []string{"utf-8", "windows-1252", "iso-8859-1", "utf-16"}

// SourceOptions control how raw rows are read from a file.
type SourceOptions struct {
	Sheet     string // Excel only
	Delimiter rune   // CSV only
	Encoding  string // CSV only
	HeaderRow int    // 1-based row containing the header, 0 = no header
	StartRow  int    // 1-based first data row
	SkipEmpty bool   // skip rows where every cell is empty
	Decimal   byte   // 0 = auto, '.' or ','

	// AutoDetect re-detects delimiter and encoding for every CSV file after
	// the first one when several files are imported.
	AutoDetect bool
}

// Normalize fills defaults and fixes inconsistent values.
func (o *SourceOptions) Normalize() {
	if o.Delimiter == 0 {
		o.Delimiter = ','
	}
	if o.Encoding == "" {
		o.Encoding = "utf-8"
	}
	if o.HeaderRow < 0 {
		o.HeaderRow = 0
	}
	if o.StartRow <= o.HeaderRow {
		o.StartRow = o.HeaderRow + 1
	}
	if o.StartRow < 1 {
		o.StartRow = 1
	}
}

// RowReader iterates over the raw rows of a source.
type RowReader interface {
	// Next returns the next row or io.EOF.
	Next() ([]string, error)
	Close() error
}

// OpenRows opens a raw row reader for the file.
func OpenRows(path string, opts SourceOptions) (RowReader, error) {
	switch DetectFormat(path) {
	case FormatXLSX:
		return openXLSX(path, opts.Sheet)
	case FormatCSV:
		return openCSV(path, opts.Delimiter, opts.Encoding)
	default:
		return nil, fmt.Errorf("unsupported file type %q (only .csv/.txt/.tsv and .xlsx/.xlsm are supported)", filepath.Ext(path))
	}
}

// ReadRaw reads up to limit raw rows from the start of the file (limit <= 0
// means all rows).
func ReadRaw(path string, opts SourceOptions, limit int) ([][]string, error) {
	r, err := OpenRows(path, opts)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var rows [][]string
	for limit <= 0 || len(rows) < limit {
		row, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rows, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func isEmptyRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func isSupportedExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".tsv", ".txt", ".tab", ".dat", ".xlsx", ".xlsm", ".xltx", ".xltm":
		return true
	}
	return false
}

// ExpandPaths resolves file names, glob patterns (* ? [) and directories
// (all supported files in them, sorted) into a list of files. Duplicates are
// removed, the order is kept.
func ExpandPaths(args []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		key := p
		if a, err := filepath.Abs(p); err == nil {
			key = a
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if strings.ContainsAny(arg, "*?[") {
			matches, err := filepath.Glob(arg)
			if err != nil {
				return nil, fmt.Errorf("invalid pattern %q: %w", arg, err)
			}
			n := 0
			patBase := filepath.Base(arg)
			for _, m := range matches {
				name := filepath.Base(m)
				if (strings.HasPrefix(name, ".") && !strings.HasPrefix(patBase, ".")) || strings.HasPrefix(name, "~$") {
					continue // hidden and Office lock files, like a shell glob
				}
				if st, err := os.Stat(m); err == nil && !st.IsDir() {
					add(m)
					n++
				}
			}
			if n == 0 {
				return nil, fmt.Errorf("no files match %q", arg)
			}
			continue
		}
		st, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			add(arg)
			continue
		}
		entries, err := os.ReadDir(arg)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "~$") && isSupportedExt(e.Name()) {
				names = append(names, e.Name())
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no CSV or Excel files in %s", arg)
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(arg, n))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files given")
	}
	return out, nil
}

// CheckSameFormat makes sure all files can be imported together: they must
// be supported and all CSV-like or all Excel.
func CheckSameFormat(paths []string) error {
	var first Format
	for i, p := range paths {
		f := DetectFormat(p)
		if f == FormatUnknown {
			return fmt.Errorf("unsupported file type %q (only .csv/.txt/.tsv and .xlsx/.xlsm are supported)", filepath.Ext(p))
		}
		if i == 0 {
			first = f
		} else if f != first {
			return fmt.Errorf("cannot import %s and %s together: %s is %s, %s is %s",
				filepath.Base(paths[0]), filepath.Base(p), filepath.Base(paths[0]), first, filepath.Base(p), f)
		}
	}
	return nil
}

// OptionsFor returns the source options to use for a file. The first file
// uses base as is. For further files the Excel sheet is chosen by name (or
// the only sheet) and, with AutoDetect, CSV delimiter and encoding are
// detected again.
func OptionsFor(path string, base SourceOptions, first bool) (SourceOptions, error) {
	if first {
		return base, nil
	}
	switch DetectFormat(path) {
	case FormatXLSX:
		if base.Sheet == "" {
			return base, nil
		}
		sheets, err := ListSheets(path)
		if err != nil {
			return base, err
		}
		for _, s := range sheets {
			if s == base.Sheet {
				return base, nil
			}
		}
		if len(sheets) == 1 {
			base.Sheet = sheets[0]
			return base, nil
		}
		return base, fmt.Errorf("sheet %q not found (sheets: %s)", base.Sheet, strings.Join(sheets, ", "))
	case FormatCSV:
		if base.AutoDetect {
			d, e, err := SniffCSV(path)
			if err != nil {
				return base, err
			}
			base.Delimiter, base.Encoding = d, e
		}
	}
	return base, nil
}
