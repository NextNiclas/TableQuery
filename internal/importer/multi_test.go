package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func multiFixture(t *testing.T) (dir string, paths []string) {
	dir = t.TempDir()
	paths = []string{
		// comma, Latin text, reference layout
		writeFile(t, dir, "sales_1.csv", "Region,Umsatz,Menge\nNord,1.5,2\nSüd,2.5,3\n"),
		// semicolon, windows-1252, columns reordered, German decimals
		writeFile(t, dir, "sales_2.csv", "Menge;Region;Umsatz\n4;M\xfcnchen;10,25\n"),
		// missing "Menge", extra "Notiz"
		writeFile(t, dir, "sales_3.csv", "region,notiz,umsatz\nWest,x,7\n"),
	}
	return dir, paths
}

func firstTarget(t *testing.T, paths []string, opts SourceOptions) (Target, SourceOptions) {
	t.Helper()
	d, e, err := SniffCSV(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	opts.Delimiter, opts.Encoding, opts.HeaderRow, opts.SkipEmpty = d, e, 1, true
	opts.AutoDetect = true
	a, err := Analyze(paths[0], opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	return Target{Table: "sales", Columns: a.Columns}, opts
}

func TestImportFilesMapsByHeader(t *testing.T) {
	_, paths := multiFixture(t)
	tgt, opts := firstTarget(t, paths, SourceOptions{})
	tgt.SourceColumn = "source_file"
	d := openTestDB(t)
	res, err := ImportFiles(context.Background(), d, paths, opts, tgt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 4 || len(res.Files) != 3 {
		t.Fatalf("result %+v", res)
	}
	if got := res.Files[2]; !reflect.DeepEqual(got.Missing, []string{"menge"}) || !reflect.DeepEqual(got.Extra, []string{"notiz"}) {
		t.Fatalf("file 3 report %+v", got)
	}

	rows, err := d.Query(`SELECT region, umsatz, menge, source_file FROM sales ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		Region string
		Umsatz float64
		Menge  *int64
		Source string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Region, &r.Umsatz, &r.Menge, &r.Source); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	i := func(v int64) *int64 { return &v }
	want := []row{
		{"Nord", 1.5, i(2), "sales_1.csv"},
		{"Süd", 2.5, i(3), "sales_1.csv"},
		{"München", 10.25, i(4), "sales_2.csv"},
		{"West", 7, nil, "sales_3.csv"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestImportFilesAppendAndSourceClash(t *testing.T) {
	_, paths := multiFixture(t)
	tgt, opts := firstTarget(t, paths, SourceOptions{})
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := ImportFiles(ctx, d, paths[:1], opts, tgt, nil); err != nil {
		t.Fatal(err)
	}
	tgt.IfExists = IfExistsAppend
	if _, err := ImportFiles(ctx, d, paths[1:], opts, tgt, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM sales`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("rows %d %v", n, err)
	}
	// Appending with a source column the table does not have fails cleanly.
	tgt.SourceColumn = "source_file"
	if _, err := ImportFiles(ctx, d, paths[:1], opts, tgt, nil); err == nil {
		t.Fatal("expected error for missing source column in existing table")
	}
	tgt.SourceColumn = "region"
	if err := tgt.Validate(); err == nil {
		t.Fatal("expected clash error")
	}
}

func TestImportFilesRollsBack(t *testing.T) {
	dir, paths := multiFixture(t)
	// A second file without the header row the options ask for.
	paths = append(paths[:1], writeFile(t, dir, "empty.csv", ""))
	tgt, opts := firstTarget(t, paths[:1], SourceOptions{})
	d := openTestDB(t)
	if _, err := ImportFiles(context.Background(), d, paths, opts, tgt, nil); err == nil {
		t.Fatal("expected error")
	}
	if ok, _ := TableExists(context.Background(), d, "sales"); ok {
		t.Fatal("table must not exist after a failed multi-file import")
	}
}

func TestMixedFormatsRejected(t *testing.T) {
	dir := t.TempDir()
	csvPath := writeFile(t, dir, "a.csv", "a\n1\n")
	xl := filepath.Join(dir, "b.xlsx")
	f := excelize.NewFile()
	_ = f.SetCellValue("Sheet1", "A1", "a")
	if err := f.SaveAs(xl); err != nil {
		t.Fatal(err)
	}
	if err := CheckSameFormat([]string{csvPath, xl}); err == nil {
		t.Fatal("expected error for mixed formats")
	}
	if err := CheckSameFormat([]string{csvPath, filepath.Join(dir, "c.xls")}); err == nil {
		t.Fatal("expected error for .xls")
	}
}

func TestExpandPaths(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.csv", "a.csv", "c.txt", "notes.md", ".hidden.csv", "~$lock.xlsx"} {
		writeFile(t, dir, n, "x\n")
	}
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)

	base := func(ps []string) (out []string) {
		for _, p := range ps {
			out = append(out, filepath.Base(p))
		}
		return
	}
	got, err := ExpandPaths([]string{dir})
	if err != nil || !reflect.DeepEqual(base(got), []string{"a.csv", "b.csv", "c.txt"}) {
		t.Fatalf("dir: %v %v", got, err)
	}
	got, err = ExpandPaths([]string{filepath.Join(dir, "*.csv"), filepath.Join(dir, "a.csv")})
	if err != nil || !reflect.DeepEqual(base(got), []string{"a.csv", "b.csv"}) {
		t.Fatalf("glob+dedupe: %v %v", got, err)
	}
	if _, err := ExpandPaths([]string{filepath.Join(dir, "*.xlsx")}); err == nil {
		t.Fatal("expected error for pattern without matches")
	}
	if _, err := ExpandPaths([]string{filepath.Join(dir, "missing.csv")}); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestImportFilesExcelSheetFallback(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, sheets ...string) string {
		f := excelize.NewFile()
		f.SetSheetName("Sheet1", sheets[0])
		for _, s := range sheets[1:] {
			_, _ = f.NewSheet(s)
		}
		_ = f.SetSheetRow(sheets[0], "A1", &[]any{"Name", "Wert"})
		_ = f.SetSheetRow(sheets[0], "A2", &[]any{name, 1})
		p := filepath.Join(dir, name+".xlsx")
		if err := f.SaveAs(p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := mk("a", "Daten", "Info")
	b := mk("b", "Tabelle1") // single sheet with another name -> used
	opts := SourceOptions{Sheet: "Daten", HeaderRow: 1, SkipEmpty: true}
	an, err := Analyze(a, opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	d := openTestDB(t)
	res, err := ImportFiles(context.Background(), d, []string{a, b}, opts, Target{Table: "t", Columns: an.Columns, SourceColumn: "datei"}, nil)
	if err != nil || res.Rows != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	// Two sheets, none matching -> clear error.
	c := mk("c", "X", "Y")
	if _, err := ImportFiles(context.Background(), d, []string{a, c}, opts, Target{Table: "t2", Columns: an.Columns}, nil); err == nil {
		t.Fatal("expected sheet error")
	}
}
