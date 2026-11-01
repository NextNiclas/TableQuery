package db

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	got := SplitStatements(`
		-- comment; with semicolon
		CREATE TABLE t (a TEXT); INSERT INTO t VALUES ('x;y'), ("q""uo;te");
		/* block ; comment */ SELECT * FROM t;
		;
	`)
	want := []string{
		"-- comment; with semicolon\n\t\tCREATE TABLE t (a TEXT)",
		`INSERT INTO t VALUES ('x;y'), ("q""uo;te")`,
		"/* block ; comment */ SELECT * FROM t",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRunScriptAndTables(t *testing.T) {
	d, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	res, err := d.RunScript(ctx, `
		CREATE TABLE sales (region TEXT, amount REAL);
		INSERT INTO sales VALUES ('Nord', 10.5), ('Süd', 20), ('Nord', NULL);
		SELECT region, sum(amount) AS total FROM sales GROUP BY region ORDER BY region`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsQuery || !reflect.DeepEqual(res.Columns, []string{"region", "total"}) || len(res.Rows) != 2 {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Rows[0][1].S != "10.5" || res.Rows[1][1].S != "20" {
		t.Fatalf("rows %+v", res.Rows)
	}

	res, err = d.Run(ctx, "SELECT * FROM sales", 2)
	if err != nil || len(res.Rows) != 2 || !res.Truncated {
		t.Fatalf("limit: %+v %v", res, err)
	}
	res, err = d.Run(ctx, "UPDATE sales SET amount = 0 WHERE amount IS NULL", 0)
	if err != nil || res.IsQuery || res.Affected != 1 {
		t.Fatalf("update: %+v %v", res, err)
	}

	tables, err := d.Tables(ctx)
	if err != nil || len(tables) != 1 || tables[0].Rows != 3 || len(tables[0].Columns) != 2 {
		t.Fatalf("tables %+v %v", tables, err)
	}

	// Save in-memory database to a file and reopen it.
	path := filepath.Join(t.TempDir(), "saved.db")
	if err := d.SaveAs(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if tables, err := f.Tables(ctx); err != nil || len(tables) != 1 {
		t.Fatalf("saved tables %+v %v", tables, err)
	}
}

func TestStatementError(t *testing.T) {
	d, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = d.RunScript(context.Background(), "SELECT 1; SELEC 2", 0)
	se, ok := err.(*StatementError)
	if !ok || se.Index != 2 {
		t.Fatalf("got %v", err)
	}
}
