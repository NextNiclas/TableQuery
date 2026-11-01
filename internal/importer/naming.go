package importer

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var umlautReplacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ẞ", "Ss",
)

// sqlKeywords contains reserved words that would need quoting when used as
// identifiers. Normalized names that collide get a trailing underscore.
var sqlKeywords = map[string]bool{
	"abort": true, "action": true, "add": true, "after": true, "all": true, "alter": true,
	"analyze": true, "and": true, "as": true, "asc": true, "attach": true, "autoincrement": true,
	"before": true, "begin": true, "between": true, "by": true, "cascade": true, "case": true,
	"cast": true, "check": true, "collate": true, "column": true, "commit": true, "conflict": true,
	"constraint": true, "create": true, "cross": true, "current_date": true, "current_time": true,
	"current_timestamp": true, "database": true, "default": true, "deferrable": true,
	"deferred": true, "delete": true, "desc": true, "detach": true, "distinct": true, "drop": true,
	"each": true, "else": true, "end": true, "escape": true, "except": true, "exclusive": true,
	"exists": true, "explain": true, "fail": true, "for": true, "foreign": true, "from": true,
	"full": true, "glob": true, "group": true, "having": true, "if": true, "ignore": true,
	"immediate": true, "in": true, "index": true, "indexed": true, "initially": true, "inner": true,
	"insert": true, "instead": true, "intersect": true, "into": true, "is": true, "isnull": true,
	"join": true, "key": true, "left": true, "like": true, "limit": true, "match": true,
	"natural": true, "no": true, "not": true, "notnull": true, "null": true, "of": true,
	"offset": true, "on": true, "or": true, "order": true, "outer": true, "plan": true,
	"pragma": true, "primary": true, "query": true, "raise": true, "recursive": true,
	"references": true, "regexp": true, "reindex": true, "release": true, "rename": true,
	"replace": true, "restrict": true, "right": true, "rollback": true, "row": true, "rowid": true,
	"savepoint": true, "select": true, "set": true, "table": true, "temp": true, "temporary": true,
	"then": true, "to": true, "transaction": true, "trigger": true, "union": true, "unique": true,
	"update": true, "using": true, "vacuum": true, "values": true, "view": true, "virtual": true,
	"when": true, "where": true, "with": true, "without": true,
}

// NormalizeName turns an arbitrary header ("Anzahl Einwohner") into a
// snake_case SQL identifier ("anzahl_einwohner"). It returns "" if nothing
// usable is left.
func NormalizeName(s string) string {
	s = umlautReplacer.Replace(strings.TrimSpace(s))
	// Strip remaining diacritics (é -> e).
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	if out, _, err := transform.String(t, s); err == nil {
		s = out
	}

	var b strings.Builder
	prevUnderscore := true // avoid leading underscore
	prevLower := false
	for _, r := range s {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			// camelCase -> camel_case
			if unicode.IsUpper(r) && prevLower && !prevUnderscore {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
			prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
			prevLower = false
		}
	}
	name := strings.TrimRight(b.String(), "_")
	if name == "" {
		return ""
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "c_" + name
	}
	if sqlKeywords[name] {
		name += "_"
	}
	return name
}

// NormalizeNames normalizes a list of headers and makes the result unique.
// Empty results are replaced by col_<n> (1-based position).
func NormalizeNames(headers []string) []string {
	out := make([]string, len(headers))
	for i, h := range headers {
		n := NormalizeName(h)
		if n == "" {
			n = fmt.Sprintf("col_%d", i+1)
		}
		out[i] = n
	}
	return Dedupe(out)
}

// Dedupe appends _2, _3, ... to repeated names.
func Dedupe(names []string) []string {
	used := make(map[string]bool, len(names))
	out := make([]string, len(names))
	for i, n := range names {
		cand := n
		for k := 2; used[cand]; k++ {
			cand = fmt.Sprintf("%s_%d", n, k)
		}
		used[cand] = true
		out[i] = cand
	}
	return out
}

// TableNameFor suggests a table name from a file path and optional sheet.
func TableNameFor(path, sheet string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	src := base
	if sheet != "" {
		src = base + "_" + sheet
	}
	n := NormalizeName(src)
	if n == "" {
		n = "imported"
	}
	return n
}

// QuoteIdent quotes an SQL identifier for SQLite.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
