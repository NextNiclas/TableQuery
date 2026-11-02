package importer

import (
	"context"
	"reflect"
	"testing"
)

func TestImportIntoExistingTable(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	if _, err := d.Exec(`CREATE TABLE umsatz (
		region TEXT, betrag REAL, anzahl INTEGER, tag TEXT, kommentar TEXT, quelle TEXT)`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Different order, German headers/number format, an extra column, no "kommentar".
	path := writeFile(t, dir, "neu.csv",
		"Anzahl;Datum;Region;Betrag;Intern\n3;15.01.2024;Nord;1.234,50;x\n4;16.01.2024;Süd;7;y\n")
	opts := SourceOptions{Delimiter: ';', Encoding: "utf-8", HeaderRow: 1, SkipEmpty: true}
	a, err := Analyze(path, opts, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The user renames "Datum" to match the table column "tag" and skips "Intern".
	for i := range a.Columns {
		switch a.Columns[i].Source {
		case "Datum":
			a.Columns[i].Name = "tag"
		case "Intern":
			a.Columns[i].Include = false
		}
	}

	plan, err := PlanAppend(ctx, d, "umsatz", a.Columns, "quelle")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range plan.Mapped {
		names = append(names, c.Name)
	}
	if want := []string{"region", "betrag", "anzahl", "tag"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("mapped %v, want %v", names, want)
	}
	if !reflect.DeepEqual(plan.Nulled, []string{"kommentar"}) || len(plan.Ignored) != 0 {
		t.Fatalf("plan %+v", plan)
	}

	res, err := ImportFiles(ctx, d, []string{path}, opts,
		Target{Table: "umsatz", IfExists: IfExistsAppend, Columns: a.Columns, SourceColumn: "quelle"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 2 || res.Warnings != 0 || !reflect.DeepEqual(res.Nulled, []string{"kommentar"}) {
		t.Fatalf("result %+v", res)
	}
	var region, tag, quelle string
	var betrag float64
	var anzahl int64
	var kommentar *string
	if err := d.QueryRow(`SELECT region, betrag, anzahl, tag, kommentar, quelle FROM umsatz WHERE region = 'Nord'`).
		Scan(&region, &betrag, &anzahl, &tag, &kommentar, &quelle); err != nil {
		t.Fatal(err)
	}
	if betrag != 1234.5 || anzahl != 3 || tag != "2024-01-15" || kommentar != nil || quelle != "neu.csv" {
		t.Fatalf("row: %v %v %v %q %v %q", region, betrag, anzahl, tag, kommentar, quelle)
	}
}

func TestPlanAppendErrors(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	if _, err := d.Exec(`CREATE TABLE t (a TEXT); CREATE VIEW v AS SELECT a FROM t`); err != nil {
		t.Fatal(err)
	}
	cols := []Column{{Index: 0, Source: "Foo", Name: "foo", Type: TypeText, Include: true}}
	if _, err := PlanAppend(ctx, d, "t", cols, ""); err == nil {
		t.Error("expected error when no column matches")
	}
	if _, err := PlanAppend(ctx, d, "v", cols, ""); err == nil {
		t.Error("expected error for view")
	}
	if _, err := PlanAppend(ctx, d, "missing", cols, ""); err == nil {
		t.Error("expected error for missing table")
	}
	cols[0].Source = "A"
	if _, err := PlanAppend(ctx, d, "t", cols, "file"); err == nil {
		t.Error("expected error when the source column is not in the table")
	}
	p, err := PlanAppend(ctx, d, "t", cols, "")
	if err != nil || len(p.Mapped) != 1 || p.Mapped[0].Name != "a" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestColumnTypeFor(t *testing.T) {
	for in, want := range map[string]ColType{
		"INTEGER": TypeInteger, "bigint": TypeInteger, "REAL": TypeReal, "FLOAT": TypeReal,
		"NUMERIC": TypeReal, "TEXT": TypeText, "": TypeText, "VARCHAR(20)": TypeText,
	} {
		if got := columnTypeFor(in); got != want {
			t.Errorf("columnTypeFor(%q) = %s, want %s", in, got, want)
		}
	}
}
