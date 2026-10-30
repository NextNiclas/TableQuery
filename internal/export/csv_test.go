package export

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextniclas/tablequery/internal/db"
)

func TestExport(t *testing.T) {
	d, err := db.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.RunScript(ctx, `CREATE TABLE t (a TEXT, b INTEGER); INSERT INTO t VALUES ('x;y', 1), (NULL, 2)`, 0); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	want := "a;b\n\"x;y\";1\n;2\n"

	p1 := filepath.Join(dir, "q.csv")
	n, err := QueryToCSV(ctx, d, "SELECT * FROM t ORDER BY b", p1, Options{Delimiter: ';'})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got, _ := os.ReadFile(p1); string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}

	res, err := d.Run(ctx, "SELECT * FROM t ORDER BY b", 0)
	if err != nil {
		t.Fatal(err)
	}
	p2 := filepath.Join(dir, "r.csv")
	if err := ResultToCSV(res, p2, Options{Delimiter: ';'}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p2); string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// A failing query must not leave a file behind.
	p3 := filepath.Join(dir, "bad.csv")
	if _, err := QueryToCSV(ctx, d, "SELECT * FROM missing", p3, Options{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(p3); !os.IsNotExist(err) {
		t.Fatal("file should have been removed")
	}
}
