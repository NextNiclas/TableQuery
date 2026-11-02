# TableQuery

Import CSV and Excel files into SQLite and query them with SQL — in a terminal UI or from the command line.

- **In-memory or persistent databases** – start without arguments for a throw-away in-memory DB, or pass a file to open/create one. In-memory databases can be saved to a file at any time.
- **Multiple query tabs** – each tab has its own SQL editor and result table (scrollable in both directions).
- **CSV export** of any result (all rows, delimiter selectable).
- **New tables from queries** – store a query result as a table (snapshot) or as a view (always up to date).
- **Many tables per database** – sidebar lists tables, views, row counts and columns.
- **Import wizard with live preview**
  - CSV: auto-detected delimiter (`,` `;` Tab `|` or any custom character) and encoding (UTF-8, Windows-1252, ISO-8859-1, UTF-16)
  - Excel (`.xlsx`, `.xlsm`): sheet selection, date cells converted to ISO dates
  - header row, first data row, skip empty rows, decimal separator (`1.234,56` vs `1,234.56`)
  - column names normalized automatically (`Anzahl Einwohner` → `anzahl_einwohner`, `Fläche km²` → `flaeche_km`), editable
  - column types suggested from the data (INTEGER, REAL, TEXT, DATE, DATETIME, BOOLEAN), changeable, with converted sample values and invalid-value counts
  - skip any columns you don't need
  - target table name and behaviour if the table exists (fail / replace / append)

Pure Go (no CGO): SQLite via `modernc.org/sqlite`, Excel via `excelize`, TUI via Bubble Tea.

## Install

```sh
go install github.com/nextniclas/tablequery/cmd/tablequery@latest
# or from a checkout
go build -o tablequery ./cmd/tablequery
```

## Terminal UI

```sh
tablequery                  # in-memory database
tablequery data.db          # open or create a database file
tablequery -i sales.xlsx    # start with the import wizard for a file
```

| Key | Action |
| --- | --- |
| `Ctrl+R` / `F5` | run the SQL of the current tab (several `;`-separated statements allowed; the last result is shown) |
| `Esc` | cancel a running query / back to the editor |
| `Tab` / `Shift+Tab` | focus editor → results → tables |
| `Ctrl+T` / `Ctrl+W` | new tab / close tab |
| `Ctrl+←/→`, `F7`/`F8`, `Alt+1…9` | switch tabs |
| `F2` | rename tab |
| `Ctrl+F` | import wizard |
| `Ctrl+E` | export result to CSV (`Ctrl+D` in the dialog changes the delimiter) |
| `Ctrl+N` | create a table or view from the query (name, table/view, if exists: fail / replace / append) |
| `Ctrl+O` | open / create a database file (`:memory:` for a new in-memory DB) |
| `Ctrl+S` | save database as file (an in-memory DB is then continued in that file) |
| `Ctrl+B` | show / hide the table sidebar |
| `F1` | help |
| `Ctrl+Q` / `Ctrl+C` | quit |

Results: arrows / `PgUp` `PgDn` `Home` `End` scroll, `0`/`$` first/last column.
Tables sidebar: `Enter` runs `SELECT * … LIMIT 100`, `i` inserts the table name, `m` imports a file into the table, `→`/`Space` shows columns, `d` drops, `r` refreshes.

`Ctrl+N` uses the last statement in the editor, which must be a `SELECT`/`WITH`/`VALUES` query; earlier statements are not executed.

Path inputs support `Tab` completion. The result view holds at most 10,000 rows; exports always contain all rows.

### Import wizard

1. **File** – path to a `.csv`/`.tsv`/`.txt` or `.xlsx`/`.xlsm` file.
2. **Source** – sheet or delimiter/encoding, header row (`0` = none), first data row, skip empty rows, decimal separator. The preview highlights the header row and greys out skipped rows.
3. **Columns** – `Space` include/skip, `a`/`n`/`i` all/none/invert, `←/→` change type, `e` rename.
4. **Target** – table name and what to do if it already exists. Shows the `CREATE TABLE` statement.

Values that cannot be converted to the chosen type are kept as text and reported after the import. Empty cells become `NULL`. Dates are stored as ISO text (`2024-01-15`, `2024-01-15 10:30:00`) so SQLite's date functions work; booleans are stored as `0`/`1`.

### Importing into an existing table

Add rows to a table that already exists instead of creating a new one:

- **Tables sidebar:** select the table and press `m`; the wizard opens with this table preselected.
- **Wizard:** in the Target step press `Ctrl+T` to pick one of the existing tables, or type its name and set *If table exists* to `append`.
- **CLI:** `tablequery import march.csv --db data.db --table sales --if-exists append` (`--dry-run` shows the mapping first).

The file's columns are matched to the table's columns by name: the target name from the Columns step (so you can rename a column there to force a match), else the original header or its normalized form, case-insensitive. Values are converted to the **table's column types**. File columns without a counterpart are ignored, table columns without a file column stay empty (`NULL`/default); both are shown before and after the import. If nothing matches, or the target is a view, nothing is imported. With *Source file column* the table needs a column of that name.

### Importing several files into one table

Enter a pattern (`sales_*.csv`) or a folder in the wizard's file step to collect many files, or add single files with `Ctrl+L` (`Ctrl+X` removes the last one). `Enter` on an empty line continues. A single plain file path still continues immediately.

- The **first file** defines column names, types and skipped columns.
- Every other file is mapped **by header** (case-insensitive, or by normalized name), so its column order may differ and its delimiter/encoding is detected on its own. Excel files use the same sheet name (or their only sheet).
- Columns missing in a file become `NULL`; additional columns are ignored. Both are listed per file after the import. Decimal separators are re-detected per file.
- Optionally add a **source file column** (default `source_file`) holding each row's file name.
- All files must be CSV-like or all Excel. The import is all-or-nothing: if one file fails nothing is written.

## Command line

```sh
# import (same detection as the wizard)
tablequery import cities.csv --db data.db
tablequery import report.xlsx --db data.db --sheet Q1 --header-row 3 --table q1
tablequery import export.csv --db data.db --delimiter ";" --skip "Kommentar,Intern" --if-exists replace
tablequery import export.csv --columns stadt,plz --type plz=TEXT --dry-run

# several files into one table (patterns are expanded by the tool, also on Windows)
tablequery import "sales_*.csv" --db data.db --table sales --source-column
tablequery import jan.xlsx feb.xlsx --db data.db --table sales
tablequery import ./monthly-exports/ --db data.db --table sales --if-exists append

# query / aggregate
tablequery query --db data.db "SELECT bundesland, sum(anzahl_einwohner) FROM cities GROUP BY 1"
tablequery query --db data.db "SELECT * FROM cities" --out cities.csv --delimiter ";"
tablequery query --db data.db "SELECT * FROM cities" --format csv

# store query results as new tables or views
tablequery query --db data.db "SELECT bundesland, sum(anzahl_einwohner) AS einwohner FROM cities GROUP BY 1" --into by_state
tablequery query --db data.db "SELECT * FROM cities WHERE anzahl_einwohner > 1000000" --into big_cities --view
tablequery query --db data.db "SELECT ..." --into by_state --if-exists replace   # or append

# schema
tablequery tables --db data.db
```

`--skip`, `--columns` and `--type` accept the original header or the normalized column name.

## Releases

Prebuilt binaries are attached to the [GitHub releases](https://github.com/NextNiclas/TableQuery/releases) (Windows `.zip`; Linux amd64/arm64 and macOS Apple Silicon `.tar.gz`; `checksums.txt`; `tablequery --version` shows the version).

To cut a release, push a version tag:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

The `Release` workflow runs vet and tests and builds the binaries with `CGO_ENABLED=0`. Built platforms: Windows amd64, Linux amd64/arm64 and macOS arm64 (Apple Silicon). macOS amd64 (Intel) is declared in `.github/workflows/release.yml` but switched off to keep the pipeline cheap; enable it by setting its `"enabled"` flag to `true`. The macOS binary is not code-signed: after downloading run `xattr -d com.apple.quarantine tablequery` once. Tags containing a `-` (e.g. `v0.2.0-rc1`) are published as pre-releases.

## Development

```sh
go test ./...
go vet ./...
```

Sample files for trying things out are in `testdata/`.
