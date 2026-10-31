package db

import (
	"context"
	"testing"
)

func TestCreateFromQuery(t *testing.T) {
	d, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.RunScript(ctx, `CREATE TABLE sales (region TEXT, amount REAL);
		INSERT INTO sales VALUES ('Nord', 10), ('Süd', 20), ('Nord', 5)`, 0); err != nil {
		t.Fatal(err)
	}
	agg := "-- totals\nSELECT region, sum(amount) AS total FROM sales GROUP BY region;"

	count := func(name string) int64 {
		t.Helper()
		var n int64
		if err := d.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(name)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	n, err := d.CreateFromQuery(ctx, "by region", agg, DeriveOptions{})
	if err != nil || n != 2 {
		t.Fatalf("create: n=%d err=%v", n, err)
	}
	var total float64
	if err := d.QueryRowContext(ctx, `SELECT total FROM "by region" WHERE region = 'Nord'`).Scan(&total); err != nil || total != 15 {
		t.Fatalf("total=%v err=%v", total, err)
	}

	if _, err := d.CreateFromQuery(ctx, "BY REGION", agg, DeriveOptions{}); err == nil {
		t.Fatal("expected error for existing table")
	}
	if n, err := d.CreateFromQuery(ctx, "by region", agg, DeriveOptions{IfExists: IfExistsAppend}); err != nil || n != 4 {
		t.Fatalf("append: n=%d err=%v", n, err)
	}
	if n, err := d.CreateFromQuery(ctx, "by region", "SELECT 'x' AS region, 1 AS total", DeriveOptions{IfExists: IfExistsReplace}); err != nil || n != 1 {
		t.Fatalf("replace: n=%d err=%v", n, err)
	}

	// Views reflect later changes of the base table.
	if n, err := d.CreateFromQuery(ctx, "v_nord", "SELECT * FROM sales WHERE region = 'Nord'", DeriveOptions{View: true}); err != nil || n != -1 {
		t.Fatalf("view: n=%d err=%v", n, err)
	}
	if _, err := d.ExecContext(ctx, "INSERT INTO sales VALUES ('Nord', 1)"); err != nil {
		t.Fatal(err)
	}
	if c := count("v_nord"); c != 3 {
		t.Fatalf("view rows %d", c)
	}
	if _, err := d.CreateFromQuery(ctx, "v_nord", "SELECT 1", DeriveOptions{View: true, IfExists: IfExistsAppend}); err == nil {
		t.Fatal("expected error for append to view")
	}
	if _, err := d.CreateFromQuery(ctx, "v_nord", "SELECT 1", DeriveOptions{IfExists: IfExistsAppend}); err == nil {
		t.Fatal("expected error for append into existing view")
	}
	// Replacing a view by a table works.
	if n, err := d.CreateFromQuery(ctx, "v_nord", "SELECT * FROM sales", DeriveOptions{IfExists: IfExistsReplace}); err != nil || n != 4 {
		t.Fatalf("replace view by table: n=%d err=%v", n, err)
	}

	for _, bad := range []string{"", "DELETE FROM sales", "SELECT 1; UPDATE sales SET amount = 0", "PRAGMA table_info(sales)"} {
		if _, err := d.CreateFromQuery(ctx, "bad", bad, DeriveOptions{}); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if _, err := d.CreateFromQuery(ctx, " ", agg, DeriveOptions{}); err == nil {
		t.Error("expected error for empty name")
	}
	// The last statement of a script is used.
	if n, err := d.CreateFromQuery(ctx, "last", "UPDATE sales SET amount = 0; SELECT 1 AS a", DeriveOptions{}); err != nil || n != 1 {
		t.Fatalf("script: n=%d err=%v", n, err)
	}
}
