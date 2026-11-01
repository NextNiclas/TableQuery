package importer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xuri/excelize/v2"
	_ "modernc.org/sqlite"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Anzahl Einwohner":   "anzahl_einwohner",
		"  Größe (m²) ":      "groesse_m",
		"Straße/Hausnr.":     "strasse_hausnr",
		"Café":               "cafe",
		"2023 Umsatz":        "c_2023_umsatz",
		"AnzahlEinwohner":    "anzahl_einwohner",
		"userID":             "user_id",
		"PLZ":                "plz",
		"Order":              "order_",
		"---":                "",
		"Einwohner je km²":   "einwohner_je_km",
		"Ümlaut__Test  Name": "uemlaut_test_name",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeNamesDedupe(t *testing.T) {
	got := NormalizeNames([]string{"Name", "name", "", "Name", "NAME_2"})
	want := []string{"name", "name_2", "col_3", "name_3", "name_2_2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestInferColumn(t *testing.T) {
	cases := []struct {
		vals []string
		hint byte
		typ  ColType
		dec  byte
	}{
		{[]string{"1", "2", "-3", ""}, '.', TypeInteger, '.'},
		{[]string{"1.5", "2", "3.25"}, '.', TypeReal, '.'},
		{[]string{"1,5", "2", "3,25"}, '.', TypeReal, ','},
		{[]string{"1.234,56", "12,5"}, '.', TypeReal, ','},
		{[]string{"1,234.56", "12.5"}, ',', TypeReal, '.'},
		{[]string{"1.234", "5.678"}, ',', TypeInteger, ','},
		{[]string{"01234", "12345"}, '.', TypeText, '.'},
		{[]string{"ja", "nein", "Ja"}, '.', TypeBoolean, '.'},
		{[]string{"2024-01-15", "2023-12-31"}, '.', TypeDate, '.'},
		{[]string{"15.01.2024", "1.2.2023"}, '.', TypeDate, '.'},
		{[]string{"2024-01-15 10:30:00", "2023-12-31 23:59:59"}, '.', TypeDateTime, '.'},
		{[]string{"hello", "1"}, '.', TypeText, '.'},
		{[]string{"", ""}, '.', TypeText, '.'},
	}
	for _, c := range cases {
		typ, dec, _ := InferColumn(c.vals, 0, c.hint)
		if typ != c.typ || (typ != TypeText && dec != c.dec) {
			t.Errorf("InferColumn(%v) = %s %q, want %s %q", c.vals, typ, dec, c.typ, c.dec)
		}
	}
}

func TestConvert(t *testing.T) {
	c := Column{Type: TypeReal, Decimal: ','}
	if v, err := c.Convert("1.234,5"); err != nil || v != 1234.5 {
		t.Errorf("got %v %v", v, err)
	}
	c = Column{Type: TypeInteger, Decimal: '.'}
	if v, err := c.Convert("1,234"); err != nil || v != int64(1234) {
		t.Errorf("got %v %v", v, err)
	}
	if v, err := c.Convert("abc"); err == nil || v != "abc" {
		t.Errorf("expected error, got %v %v", v, err)
	}
	if v, err := c.Convert("  "); err != nil || v != nil {
		t.Errorf("expected nil, got %v %v", v, err)
	}
	c = Column{Type: TypeDate, DateLayout: "02.01.2006"}
	if v, err := c.Convert("15.01.2024"); err != nil || v != "2024-01-15" {
		t.Errorf("got %v %v", v, err)
	}
	c = Column{Type: TypeBoolean}
	if v, err := c.Convert("Nein"); err != nil || v != int64(0) {
		t.Errorf("got %v %v", v, err)
	}
}

func TestSniffDelimiter(t *testing.T) {
	cases := map[string]rune{
		"a;b;c\n1;2;3\n4;5;6\n":               ';',
		"a,b,c\n1,2,3\n":                      ',',
		"a\tb\n1\t2\n":                        '\t',
		"name;desc\n\"x, y\";z\n\"a, b\";c\n": ';',
		"a|b|c\n1|2|3\n":                      '|',
	}
	for in, want := range cases {
		if got := sniffDelimiter(in); got != want {
			t.Errorf("sniffDelimiter(%q) = %q, want %q", in, got, want)
		}
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	return d
}

func TestCSVImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "städte.csv")
	// Windows-1252 encoded "München" plus a title line before the header.
	content := "Report 2024;;\nStadt;Anzahl Einwohner;Fl\xe4che km\xb2;Gegr\xfcndet\nM\xfcnchen;1.512.491;310,7;1158\nBerlin;3.755.251;891,1;1237\n;;;\nHamburg;1.892.122;755,2;\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	delim, enc, err := SniffCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if delim != ';' || enc != "windows-1252" {
		t.Fatalf("sniff got %q %q", delim, enc)
	}
	opts := SourceOptions{Delimiter: delim, Encoding: enc, HeaderRow: 2, SkipEmpty: true}
	a, err := Analyze(path, opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var types []ColType
	for _, c := range a.Columns {
		names = append(names, c.Name)
		types = append(types, c.Type)
	}
	if want := []string{"stadt", "anzahl_einwohner", "flaeche_km", "gegruendet"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names %v, want %v", names, want)
	}
	if want := []ColType{TypeText, TypeInteger, TypeReal, TypeInteger}; !reflect.DeepEqual(types, want) {
		t.Fatalf("types %v, want %v", types, want)
	}

	cols := a.Columns
	cols[3].Include = false // skip "Gegründet"
	d := openTestDB(t)
	res, err := Import(context.Background(), d, path, opts, Target{Table: "staedte", Columns: cols}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 3 || res.Warnings != 0 {
		t.Fatalf("result %+v", res)
	}
	var sum int64
	var area float64
	var city string
	if err := d.QueryRow(`SELECT sum(anzahl_einwohner), max(flaeche_km), min(stadt) FROM staedte`).Scan(&sum, &area, &city); err != nil {
		t.Fatal(err)
	}
	if sum != 1512491+3755251+1892122 || area != 891.1 || city != "Berlin" {
		t.Fatalf("got %d %v %s", sum, area, city)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM pragma_table_info('staedte')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("expected 3 columns, got %d %v", n, err)
	}
	if err := d.QueryRow(`SELECT stadt FROM staedte WHERE anzahl_einwohner = 1512491`).Scan(&city); err != nil || city != "München" {
		t.Fatalf("encoding: got %q %v", city, err)
	}

	// Import again: fail, then append, then replace.
	if _, err := Import(context.Background(), d, path, opts, Target{Table: "staedte", Columns: cols}, nil); err == nil {
		t.Fatal("expected error for existing table")
	}
	if _, err := Import(context.Background(), d, path, opts, Target{Table: "staedte", Columns: cols, IfExists: IfExistsAppend}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), d, path, opts, Target{Table: "staedte", Columns: cols, IfExists: IfExistsReplace}, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM staedte`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("expected 3 rows after replace, got %d %v", n, err)
	}
}

func TestXLSXImport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.xlsx")
	f := excelize.NewFile()
	if _, err := f.NewSheet("Umsatz"); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		{"Region", "Umsatz €", "Datum", "Aktiv"},
		{"Nord", 1234.5, nil, true},
		{"Süd", 99, nil, false},
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Umsatz", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	style, err := f.NewStyle(&excelize.Style{NumFmt: 14})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Umsatz", "C2", 45306.0); err != nil { // 2024-01-15
		t.Fatal(err)
	}
	if err := f.SetCellValue("Umsatz", "C3", 45307.0); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStyle("Umsatz", "C2", "C3", style); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	sheets, err := ListSheets(path)
	if err != nil || len(sheets) != 2 || sheets[1] != "Umsatz" {
		t.Fatalf("sheets %v %v", sheets, err)
	}
	opts := SourceOptions{Sheet: "Umsatz", HeaderRow: 1}
	a, err := Analyze(path, opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Columns) != 4 {
		t.Fatalf("columns %+v", a.Columns)
	}
	// Excel booleans arrive as 0/1 and are stored as INTEGER either way.
	wantTypes := []ColType{TypeText, TypeReal, TypeDate, TypeInteger}
	for i, c := range a.Columns {
		if c.Type != wantTypes[i] {
			t.Errorf("column %s: type %s, want %s (sample %v)", c.Name, c.Type, wantTypes[i], a.Sample)
		}
	}
	if a.Columns[1].Name != "umsatz" {
		t.Errorf("name %q", a.Columns[1].Name)
	}
	d := openTestDB(t)
	if _, err := Import(context.Background(), d, path, opts, Target{Table: "umsatz", Columns: a.Columns}, nil); err != nil {
		t.Fatal(err)
	}
	var date string
	var total float64
	if err := d.QueryRow(`SELECT min(datum), sum(umsatz) FROM umsatz`).Scan(&date, &total); err != nil {
		t.Fatal(err)
	}
	if date != "2024-01-15" || total != 1333.5 {
		t.Fatalf("got %q %v", date, total)
	}
}
