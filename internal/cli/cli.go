// Package cli implements the command line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nextniclas/tablequery/internal/db"
	"github.com/nextniclas/tablequery/internal/export"
	"github.com/nextniclas/tablequery/internal/importer"
	"github.com/nextniclas/tablequery/internal/tui"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	var memory bool
	var importFile string
	root := &cobra.Command{
		Use:   "tablequery [database.db]",
		Short: "Import CSV/Excel files into SQLite and query them with SQL",
		Long: `TableQuery imports CSV and Excel (.xlsx) files into a SQLite database and
lets you query and aggregate them with SQL in an interactive terminal UI.

Without arguments an in-memory database is used. Pass a file name to open
(or create) a persistent database.`,
		Args:          cobra.MaximumNArgs(1),
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := db.MemoryPath
			if len(args) == 1 && !memory {
				path = args[0]
			}
			d, err := db.Open(path)
			if err != nil {
				return err
			}
			defer d.Close()
			return tui.Run(d, importFile)
		},
	}
	root.Flags().BoolVarP(&memory, "memory", "m", false, "use an in-memory database (default when no file is given)")
	root.Flags().StringVarP(&importFile, "import", "i", "", "open the import wizard with this file")
	root.AddCommand(newImportCmd(), newQueryCmd(), newTablesCmd())
	return root
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func newImportCmd() *cobra.Command {
	var (
		dbPath, table, sheet, delimiter, encoding, ifExists, decimal string
		headerRow, startRow                                          int
		skip, only, types                                            []string
		keepEmpty, dryRun                                            bool
	)
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a CSV or Excel file into a database (non-interactive)",
		Example: `  tablequery import cities.csv --db data.db
  tablequery import report.xlsx --db data.db --sheet "Q1" --header-row 3 --table q1
  tablequery import export.csv --db data.db --delimiter ";" --skip "Kommentar,Intern" --if-exists replace
  tablequery import export.csv --db data.db --type plz=TEXT --type umsatz=REAL --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			format := importer.DetectFormat(path)
			if format == importer.FormatUnknown {
				return fmt.Errorf("unsupported file type: %s", path)
			}
			opts := importer.SourceOptions{Sheet: sheet, HeaderRow: headerRow, StartRow: startRow, SkipEmpty: !keepEmpty}
			if format == importer.FormatCSV {
				d, e, err := importer.SniffCSV(path)
				if err != nil {
					return err
				}
				opts.Delimiter, opts.Encoding = d, e
				if delimiter != "" {
					opts.Delimiter = importer.ParseDelimiter(delimiter)
				}
				if encoding != "" {
					opts.Encoding = encoding
				}
			}
			switch decimal {
			case "", "auto":
			case ".", ",":
				opts.Decimal = decimal[0]
			default:
				return fmt.Errorf("--decimal must be auto, . or ,")
			}
			opts.Normalize()

			a, err := importer.Analyze(path, opts, importer.DefaultSampleSize)
			if err != nil {
				return err
			}
			cols, err := selectColumns(a.Columns, skip, only, types)
			if err != nil {
				return err
			}
			if table == "" {
				s := ""
				if format == importer.FormatXLSX && sheet != "" {
					s = sheet
				}
				table = importer.TableNameFor(path, s)
			}
			t := importer.Target{Table: table, IfExists: ifExists, Columns: cols}
			if err := t.Validate(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			printColumns(out, cols)
			if dryRun {
				fmt.Fprintln(out, "\n"+t.CreateTableSQL())
				return nil
			}
			if dbPath == "" {
				return fmt.Errorf("--db is required (or use --dry-run)")
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			ctx, cancel := signalContext()
			defer cancel()
			res, err := importer.Import(ctx, d.DB, path, opts, t, nil)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "\nImported %d rows into %s (%s)\n", res.Rows, res.Table, dbPath)
			if res.Warnings > 0 {
				fmt.Fprintf(out, "%d values could not be converted and were stored as text, e.g.:\n", res.Warnings)
				for _, s := range res.Samples {
					fmt.Fprintln(out, "  "+s)
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&dbPath, "db", "d", "", "SQLite database file (created if missing)")
	f.StringVarP(&table, "table", "t", "", "target table name (default: derived from file/sheet name)")
	f.StringVar(&sheet, "sheet", "", "Excel sheet name (default: first sheet)")
	f.StringVar(&delimiter, "delimiter", "", `CSV delimiter: ",", ";", "tab", "|" (default: auto-detect)`)
	f.StringVar(&encoding, "encoding", "", "CSV encoding: utf-8, windows-1252, iso-8859-1, utf-16 (default: auto-detect)")
	f.IntVar(&headerRow, "header-row", 1, "1-based row containing column names, 0 = no header")
	f.IntVar(&startRow, "start-row", 0, "1-based first data row (default: header row + 1)")
	f.StringVar(&ifExists, "if-exists", importer.IfExistsFail, "if the table exists: fail, replace or append")
	f.StringVar(&decimal, "decimal", "auto", "decimal separator for numbers: auto, . or ,")
	f.StringSliceVar(&skip, "skip", nil, "columns to skip (source header or normalized name), comma separated")
	f.StringSliceVar(&only, "columns", nil, "only import these columns (source header or normalized name)")
	f.StringArrayVar(&types, "type", nil, "override a column type: name=INTEGER|REAL|TEXT|DATE|DATETIME|BOOLEAN (repeatable)")
	f.BoolVar(&keepEmpty, "keep-empty", false, "keep rows where all cells are empty")
	f.BoolVar(&dryRun, "dry-run", false, "only show the detected columns and the CREATE TABLE statement")
	return cmd
}

func matchesColumn(c importer.Column, name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(c.Source, name) || strings.EqualFold(c.Name, name) ||
		strings.EqualFold(c.Name, importer.NormalizeName(name))
}

func selectColumns(cols []importer.Column, skip, only, types []string) ([]importer.Column, error) {
	out := append([]importer.Column(nil), cols...)
	find := func(name string) (int, error) {
		for i, c := range out {
			if matchesColumn(c, name) {
				return i, nil
			}
		}
		return -1, fmt.Errorf("unknown column %q", name)
	}
	if len(only) > 0 {
		for i := range out {
			out[i].Include = false
		}
		for _, n := range only {
			i, err := find(n)
			if err != nil {
				return nil, err
			}
			out[i].Include = true
		}
	}
	for _, n := range skip {
		i, err := find(n)
		if err != nil {
			return nil, err
		}
		out[i].Include = false
	}
	for _, spec := range types {
		name, typ, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("--type must be name=TYPE, got %q", spec)
		}
		i, err := find(name)
		if err != nil {
			return nil, err
		}
		ct, err := importer.ParseColType(typ)
		if err != nil {
			return nil, err
		}
		out[i].Type = ct
	}
	return out, nil
}

func printColumns(w io.Writer, cols []importer.Column) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\tSOURCE\tCOLUMN\tTYPE")
	for _, c := range cols {
		mark := "+"
		if !c.Include {
			mark = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, c.Source, c.Name, c.Type)
	}
	tw.Flush()
}

func newQueryCmd() *cobra.Command {
	var dbPath, out, format, delimiter string
	cmd := &cobra.Command{
		Use:   "query <sql>",
		Short: "Run SQL against a database and print or export the result",
		Example: `  tablequery query --db data.db "SELECT region, sum(umsatz) FROM sales GROUP BY region"
  tablequery query --db data.db "SELECT * FROM sales" --out sales.csv --delimiter ";"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("database %s: %w", dbPath, err)
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			ctx, cancel := signalContext()
			defer cancel()
			opts := export.Options{Delimiter: importer.ParseDelimiter(delimiter)}
			stmts := db.SplitStatements(args[0])
			if len(stmts) == 0 {
				return fmt.Errorf("empty statement")
			}
			for _, s := range stmts[:len(stmts)-1] {
				if _, err := d.ExecContext(ctx, s); err != nil {
					return err
				}
			}
			last := stmts[len(stmts)-1]
			if out != "" {
				n, err := export.QueryToCSV(ctx, d, last, out, opts)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Exported %d rows to %s\n", n, out)
				return nil
			}
			res, err := d.Run(ctx, last, 0)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if !res.IsQuery {
				fmt.Fprintf(w, "OK, %d rows affected\n", res.Affected)
				return nil
			}
			switch format {
			case "csv":
				return writeCSV(w, res, opts)
			case "table":
				printResult(w, res)
				return nil
			default:
				return fmt.Errorf("--format must be table or csv")
			}
		},
	}
	f := cmd.Flags()
	f.StringVarP(&dbPath, "db", "d", "", "SQLite database file")
	f.StringVarP(&out, "out", "o", "", "write the result to this CSV file")
	f.StringVarP(&format, "format", "f", "table", "stdout format: table or csv")
	f.StringVar(&delimiter, "delimiter", ",", "CSV delimiter")
	return cmd
}

func printResult(w io.Writer, res *db.Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			if v.Null {
				cells[i] = "NULL"
			} else {
				cells[i] = strings.ReplaceAll(v.S, "\n", " ")
			}
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	tw.Flush()
	fmt.Fprintf(w, "(%d rows)\n", len(res.Rows))
}

func writeCSV(w io.Writer, res *db.Result, opts export.Options) error {
	return export.WriteResult(w, res, opts)
}

func newTablesCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "tables",
		Short: "List tables and columns of a database",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dbPath == "" {
				return fmt.Errorf("--db is required")
			}
			if _, err := os.Stat(dbPath); err != nil {
				return fmt.Errorf("database %s: %w", dbPath, err)
			}
			d, err := db.Open(dbPath)
			if err != nil {
				return err
			}
			defer d.Close()
			tables, err := d.Tables(context.Background())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, t := range tables {
				kind := fmt.Sprintf("%d rows", t.Rows)
				if t.IsView {
					kind = "view"
				}
				fmt.Fprintf(w, "%s (%s)\n", t.Name, kind)
				for _, c := range t.Columns {
					fmt.Fprintf(w, "  %-30s %s\n", c.Name, c.Type)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&dbPath, "db", "d", "", "SQLite database file")
	return cmd
}
