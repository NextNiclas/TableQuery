package db

import (
	"context"
	"strconv"
	"strings"
)

// SplitStatements splits an SQL script at semicolons that are not inside
// string literals, quoted identifiers or comments. Empty statements are
// dropped.
func SplitStatements(script string) []string {
	var out []string
	start := 0
	add := func(end int) {
		if s := strings.TrimSpace(script[start:end]); StripComments(s) != "" {
			out = append(out, s)
		}
	}
	for i := 0; i < len(script); i++ {
		switch c := script[i]; c {
		case '\'', '"', '`':
			for i++; i < len(script); i++ {
				if script[i] == c {
					if i+1 < len(script) && script[i+1] == c {
						i++ // escaped quote
						continue
					}
					break
				}
			}
		case '[':
			for i++; i < len(script) && script[i] != ']'; i++ {
			}
		case '-':
			if i+1 < len(script) && script[i+1] == '-' {
				for i += 2; i < len(script) && script[i] != '\n'; i++ {
				}
			}
		case '/':
			if i+1 < len(script) && script[i+1] == '*' {
				end := strings.Index(script[i+2:], "*/")
				if end < 0 {
					i = len(script)
				} else {
					i += end + 3
				}
			}
		case ';':
			add(i)
			start = i + 1
		}
	}
	if start < len(script) {
		add(len(script))
	}
	return out
}

// RunScript executes all statements of a script and returns the result of
// the last one.
func (d *DB) RunScript(ctx context.Context, script string, limit int) (*Result, error) {
	stmts := SplitStatements(script)
	if len(stmts) == 0 {
		return nil, errEmpty
	}
	for i, s := range stmts[:len(stmts)-1] {
		if _, err := d.ExecContext(ctx, s); err != nil {
			return nil, &StatementError{Index: i + 1, Err: err}
		}
	}
	res, err := d.Run(ctx, stmts[len(stmts)-1], limit)
	if err != nil && len(stmts) > 1 {
		return nil, &StatementError{Index: len(stmts), Err: err}
	}
	return res, err
}

// StatementError reports which statement of a script failed.
type StatementError struct {
	Index int
	Err   error
}

func (e *StatementError) Error() string {
	return "statement " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}

func (e *StatementError) Unwrap() error { return e.Err }
