package importer

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ColType is the logical type of an imported column.
type ColType string

const (
	TypeText     ColType = "TEXT"
	TypeInteger  ColType = "INTEGER"
	TypeReal     ColType = "REAL"
	TypeDate     ColType = "DATE"
	TypeDateTime ColType = "DATETIME"
	TypeBoolean  ColType = "BOOLEAN"
)

// AllTypes lists the selectable column types in cycling order.
var AllTypes = []ColType{TypeText, TypeInteger, TypeReal, TypeDate, TypeDateTime, TypeBoolean}

// ParseColType parses a type name (case-insensitive).
func ParseColType(s string) (ColType, error) {
	u := ColType(strings.ToUpper(strings.TrimSpace(s)))
	for _, t := range AllTypes {
		if t == u {
			return t, nil
		}
	}
	return "", fmt.Errorf("unknown column type %q", s)
}

// Next returns the following type in AllTypes (wrapping around).
func (t ColType) Next(delta int) ColType {
	for i, c := range AllTypes {
		if c == t {
			n := (i + delta + len(AllTypes)) % len(AllTypes)
			return AllTypes[n]
		}
	}
	return TypeText
}

// SQLType is the column type used in CREATE TABLE. Dates are stored as ISO
// text and booleans as 0/1 so SQLite's date functions and SUM() work.
func (t ColType) SQLType() string {
	switch t {
	case TypeInteger, TypeBoolean:
		return "INTEGER"
	case TypeReal:
		return "REAL"
	case TypeDate, TypeDateTime:
		return "TEXT"
	default:
		return "TEXT"
	}
}

var dateLayouts = []string{
	"2006-01-02",
	"02.01.2006",
	"2.1.2006",
	"02.01.06",
	"2006/01/02",
	"01/02/2006",
	"1/2/2006",
	"02/01/2006",
	"2/1/2006",
	"01-02-06",
	"2006-1-2",
}

var dateTimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"02.01.2006 15:04:05",
	"02.01.2006 15:04",
	"2.1.2006 15:04:05",
	"2.1.2006 15:04",
	"01/02/2006 15:04:05",
	"01/02/2006 15:04",
	"1/2/2006 15:04",
	"02/01/2006 15:04:05",
	"02/01/2006 15:04",
}

var trueWords = map[string]bool{"true": true, "yes": true, "ja": true, "wahr": true, "y": true, "j": true}
var falseWords = map[string]bool{"false": true, "no": true, "nein": true, "falsch": true, "n": true}

func parseBool(s string) (bool, bool) {
	l := strings.ToLower(strings.TrimSpace(s))
	if trueWords[l] {
		return true, true
	}
	if falseWords[l] {
		return false, true
	}
	return false, false
}

// Column describes how a source column is mapped into the target table.
type Column struct {
	Index      int     // position in the source row
	Source     string  // original header text
	Name       string  // target column name
	Type       ColType // target type
	Include    bool    // false = skip this column
	Decimal    byte    // decimal separator used for numbers: '.' or ','
	DateLayout string  // layout detected for DATE/DATETIME ("" = try all)
}

// numberShape checks if s is a number using dec as decimal separator and
// the other of '.'/',' as optional thousands separator (in groups of three).
// It returns the canonical form ("-1234.5") and whether it has a fraction.
func numberShape(s string, dec byte) (canon string, isFloat bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false, false
	}
	thou := byte(',')
	if dec == ',' {
		thou = '.'
	}
	var b strings.Builder
	i := 0
	if s[0] == '-' || s[0] == '+' {
		if s[0] == '-' {
			b.WriteByte('-')
		}
		i++
	}
	intStart := i
	digits := 0
	groupLen := -1 // -1: no thousands separator seen yet
	firstGroup := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			b.WriteByte(c)
			digits++
			if groupLen >= 0 {
				groupLen++
			} else {
				firstGroup++
			}
			continue
		}
		if c == thou {
			if groupLen == -1 {
				if firstGroup == 0 || firstGroup > 3 {
					return "", false, false
				}
			} else if groupLen != 3 {
				return "", false, false
			}
			groupLen = 0
			continue
		}
		break
	}
	if groupLen != -1 && groupLen != 3 {
		return "", false, false
	}
	if digits == 0 {
		return "", false, false
	}
	intPart := s[intStart:i]
	// Leading zeros (postal codes, IDs) are not numbers: "01234".
	if len(intPart) > 1 && intPart[0] == '0' && groupLen == -1 {
		return "", false, false
	}
	if i < len(s) && s[i] == dec {
		i++
		fracDigits := 0
		b.WriteByte('.')
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			b.WriteByte(s[i])
			fracDigits++
		}
		if fracDigits == 0 {
			return "", false, false
		}
		isFloat = true
	}
	// Optional exponent.
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		b.WriteByte('e')
		i++
		if i < len(s) && (s[i] == '-' || s[i] == '+') {
			b.WriteByte(s[i])
			i++
		}
		expDigits := 0
		for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
			b.WriteByte(s[i])
			expDigits++
		}
		if expDigits == 0 {
			return "", false, false
		}
		isFloat = true
	}
	if i != len(s) {
		return "", false, false
	}
	return b.String(), isFloat, true
}

// DetectDecimal decides which decimal separator a column of values uses.
// hint is used when the values are ambiguous (e.g. only "1.234").
func DetectDecimal(values []string, hint byte) byte {
	dotVotes, commaVotes := 0, 0
	for _, v := range values {
		v = strings.TrimSpace(v)
		lastDot := strings.LastIndexByte(v, '.')
		lastComma := strings.LastIndexByte(v, ',')
		switch {
		case lastDot >= 0 && lastComma >= 0:
			if lastComma > lastDot {
				commaVotes++
			} else {
				dotVotes++
			}
		case lastComma >= 0:
			if len(v)-lastComma-1 != 3 || strings.Count(v, ",") == 1 && strings.HasPrefix(strings.TrimLeft(v, "+-"), "0,") {
				commaVotes++
			}
		case lastDot >= 0:
			if len(v)-lastDot-1 != 3 || strings.Count(v, ".") == 1 && strings.HasPrefix(strings.TrimLeft(v, "+-"), "0.") {
				dotVotes++
			}
		}
	}
	switch {
	case commaVotes > dotVotes:
		return ','
	case dotVotes > commaVotes:
		return '.'
	case hint == ',':
		return ','
	default:
		return '.'
	}
}

func matchLayout(values []string, layouts []string) (string, bool) {
	for _, l := range layouts {
		all := true
		for _, v := range values {
			if _, err := time.Parse(l, strings.TrimSpace(v)); err != nil {
				all = false
				break
			}
		}
		if all {
			return l, true
		}
	}
	return "", false
}

// InferColumn guesses type, decimal separator and date layout for the
// given sample values of one column. Empty values are ignored. decimal
// forces a decimal separator ('.' or ','); with 0 it is detected and
// decimalHint breaks ties.
func InferColumn(values []string, decimal, decimalHint byte) (ColType, byte, string) {
	var vals []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			vals = append(vals, v)
		}
	}
	dec := decimal
	if dec == 0 {
		dec = DetectDecimal(vals, decimalHint)
	}
	if len(vals) == 0 {
		return TypeText, dec, ""
	}

	allBool, allInt, allNum := true, true, true
	for _, v := range vals {
		if _, ok := parseBool(v); !ok {
			allBool = false
		}
		_, isFloat, ok := numberShape(v, dec)
		if !ok {
			allNum, allInt = false, false
		} else if isFloat {
			allInt = false
		} else if _, err := strconv.ParseInt(canonInt(v, dec), 10, 64); err != nil {
			allInt = false
		}
		if !allBool && !allNum {
			break
		}
	}
	switch {
	case allBool:
		return TypeBoolean, dec, ""
	case allInt:
		return TypeInteger, dec, ""
	case allNum:
		return TypeReal, dec, ""
	}
	if l, ok := matchLayout(vals, dateLayouts); ok {
		return TypeDate, dec, l
	}
	if l, ok := matchLayout(vals, dateTimeLayouts); ok {
		return TypeDateTime, dec, l
	}
	return TypeText, dec, ""
}

func canonInt(v string, dec byte) string {
	c, _, _ := numberShape(v, dec)
	return c
}

// Convert converts a raw string into the value stored in SQLite for the
// column. Empty strings become NULL. If conversion fails the raw string is
// returned together with an error so callers can decide what to do.
func (c *Column) Convert(raw string) (any, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	dec := c.Decimal
	if dec == 0 {
		dec = '.'
	}
	switch c.Type {
	case TypeInteger:
		canon, isFloat, ok := numberShape(v, dec)
		if ok && !isFloat {
			if n, err := strconv.ParseInt(canon, 10, 64); err == nil {
				return n, nil
			}
		}
		if ok {
			// e.g. 1.0 — accept if it is integral.
			if f, err := strconv.ParseFloat(canon, 64); err == nil && f == float64(int64(f)) {
				return int64(f), nil
			}
		}
		if b, ok := parseBool(v); ok {
			if b {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return raw, fmt.Errorf("%q is not an integer", raw)
	case TypeReal:
		if canon, _, ok := numberShape(v, dec); ok {
			if f, err := strconv.ParseFloat(canon, 64); err == nil {
				return f, nil
			}
		}
		// Plain Go float syntax as a fallback (e.g. "1e5", "0012.5").
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, nil
		}
		return raw, fmt.Errorf("%q is not a number", raw)
	case TypeBoolean:
		if b, ok := parseBool(v); ok {
			if b {
				return int64(1), nil
			}
			return int64(0), nil
		}
		switch v {
		case "1":
			return int64(1), nil
		case "0":
			return int64(0), nil
		}
		return raw, fmt.Errorf("%q is not a boolean", raw)
	case TypeDate, TypeDateTime:
		t, err := c.parseTime(v)
		if err != nil {
			return raw, err
		}
		if c.Type == TypeDate {
			return t.Format("2006-01-02"), nil
		}
		return t.Format("2006-01-02 15:04:05"), nil
	default:
		return raw, nil
	}
}

func (c *Column) parseTime(v string) (time.Time, error) {
	if c.DateLayout != "" {
		if t, err := time.Parse(c.DateLayout, v); err == nil {
			return t, nil
		}
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, nil
		}
	}
	for _, l := range dateTimeLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date", v)
}
