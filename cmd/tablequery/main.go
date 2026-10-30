// Command tablequery imports CSV/Excel files into SQLite and queries them
// with SQL in a terminal UI.
package main

import (
	"fmt"
	"os"

	"github.com/nextniclas/tablequery/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
