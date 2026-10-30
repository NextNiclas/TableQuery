package importer

import (
	"fmt"
	"io"
	"path/filepath"
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
