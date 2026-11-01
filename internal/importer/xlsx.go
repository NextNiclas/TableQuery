package importer

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ListSheets returns the sheet names of an Excel workbook.
func ListSheets(path string) ([]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.GetSheetList(), nil
}

type dateKind int

const (
	notDate dateKind = iota
	kindDate
	kindDateTime
	kindTime
)

type xlsxReader struct {
	f        *excelize.File
	rows     *excelize.Rows
	sheet    string
	rowNum   int
	date1904 bool
	styles   map[int]dateKind
}

func openXLSX(path, sheet string) (RowReader, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	if sheet == "" {
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			f.Close()
			return nil, fmt.Errorf("workbook has no sheets")
		}
		sheet = sheets[0]
	}
	rows, err := f.Rows(sheet)
	if err != nil {
		f.Close()
		return nil, err
	}
	r := &xlsxReader{f: f, rows: rows, sheet: sheet, styles: map[int]dateKind{}}
	if props, err := f.GetWorkbookProps(); err == nil && props.Date1904 != nil {
		r.date1904 = *props.Date1904
	}
	return r, nil
}

func (x *xlsxReader) Next() ([]string, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	x.rowNum++
	cols, err := x.rows.Columns(excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	for i, v := range cols {
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if kind := x.cellDateKind(i+1, x.rowNum); kind != notDate {
			cols[i] = formatExcelDate(f, kind, x.date1904)
		} else {
			cols[i] = cleanFloat(v, f)
		}
	}
	return cols, nil
}

// cleanFloat removes binary floating point noise like 0.30000000000000004.
func cleanFloat(raw string, f float64) string {
	if len(raw) < 15 || math.IsInf(f, 0) || math.IsNaN(f) {
		return raw
	}
	// Excel itself only keeps 15 significant digits.
	r, err := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 15, 64), 64)
	if err != nil {
		return raw
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

func (x *xlsxReader) cellDateKind(col, row int) dateKind {
	cell, err := excelize.CoordinatesToCellName(col, row)
	if err != nil {
		return notDate
	}
	id, err := x.f.GetCellStyle(x.sheet, cell)
	if err != nil || id == 0 {
		return notDate
	}
	if k, ok := x.styles[id]; ok {
		return k
	}
	k := notDate
	if st, err := x.f.GetStyle(id); err == nil && st != nil {
		if st.CustomNumFmt != nil {
			k = classifyFormat(*st.CustomNumFmt)
		} else {
			k = classifyBuiltin(st.NumFmt)
		}
	}
	x.styles[id] = k
	return k
}

func classifyBuiltin(id int) dateKind {
	switch {
	case id >= 14 && id <= 17, id >= 27 && id <= 31, id >= 34 && id <= 36, id >= 50 && id <= 58:
		return kindDate
	case id >= 18 && id <= 21, id >= 45 && id <= 47, id == 32 || id == 33:
		return kindTime
	case id == 22:
		return kindDateTime
	}
	return notDate
}

// classifyFormat inspects a custom number format code like "dd.mm.yyyy hh:mm".
func classifyFormat(code string) dateKind {
	// Only look at the first section, drop quoted literals and [..] blocks.
	if i := strings.IndexByte(code, ';'); i >= 0 {
		code = code[:i]
	}
	var b strings.Builder
	inQuote, inBracket := false, false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '[':
			inBracket = true
		case c == ']':
			inBracket = false
		case inBracket:
		case c == '\\':
			i++
		default:
			b.WriteByte(c)
		}
	}
	s := strings.ToLower(b.String())
	hasDate := strings.ContainsAny(s, "yd") || (strings.Contains(s, "m") && !strings.ContainsAny(s, "hs"))
	hasTime := strings.ContainsAny(s, "hs")
	switch {
	case hasDate && hasTime:
		return kindDateTime
	case hasDate:
		return kindDate
	case hasTime:
		return kindTime
	}
	return notDate
}

func formatExcelDate(f float64, kind dateKind, date1904 bool) string {
	if kind == kindTime && f < 1 {
		secs := int(math.Round(f * 86400))
		return fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs/60%60, secs%60)
	}
	t, err := excelize.ExcelDateToTime(f, date1904)
	if err != nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	// Round to seconds to avoid 23:59:59.999 artifacts.
	t = t.Round(1e9)
	if kind == kindDate || (f == math.Trunc(f) && kind != kindTime) {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func (x *xlsxReader) Close() error {
	_ = x.rows.Close()
	return x.f.Close()
}
