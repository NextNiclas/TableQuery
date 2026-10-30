// Package export writes query results to files.
package export

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"

	"github.com/nextniclas/tablequery/internal/db"
)

// Options control the CSV output.
type Options struct {
	Delimiter rune // default ','
	NullText  string
	BOM       bool // write UTF-8 BOM (helps Excel detect the encoding)
}

// QueryToCSV runs the query and streams all rows into a CSV file. It
// returns the number of data rows written.
func QueryToCSV(ctx context.Context, d *db.DB, query, path string, opts Options) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	n, werr := writeQuery(ctx, d, query, f, opts)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return 0, werr
	}
	return n, nil
}

func writeQuery(ctx context.Context, d *db.DB, query string, f *os.File, opts Options) (int, error) {
	if opts.BOM {
		if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			return 0, err
		}
	}
	w := newWriter(f, opts)
	n := 0
	err := d.Stream(ctx, query, func(cols []string) error {
		return w.Write(cols)
	}, func(row []db.Value) error {
		n++
		return w.Write(record(row, opts))
	})
	if err != nil {
		return 0, err
	}
	w.Flush()
	return n, w.Error()
}

// ResultToCSV writes an already fetched result to a file.
func ResultToCSV(res *db.Result, path string, opts Options) error {
	if res == nil || !res.IsQuery {
		return fmt.Errorf("no result to export")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if opts.BOM {
		if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			f.Close()
			return err
		}
	}
	if err := WriteResult(f, res, opts); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteResult writes a fetched result as CSV.
func WriteResult(out io.Writer, res *db.Result, opts Options) error {
	w := newWriter(out, opts)
	if err := w.Write(res.Columns); err != nil {
		return err
	}
	for _, row := range res.Rows {
		if err := w.Write(record(row, opts)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func newWriter(f io.Writer, opts Options) *csv.Writer {
	w := csv.NewWriter(f)
	if opts.Delimiter != 0 {
		w.Comma = opts.Delimiter
	}
	return w
}

func record(row []db.Value, opts Options) []string {
	rec := make([]string, len(row))
	for i, v := range row {
		if v.Null {
			rec[i] = opts.NullText
		} else {
			rec[i] = v.S
		}
	}
	return rec
}
