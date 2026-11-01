package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type csvReader struct {
	f *os.File
	r *csv.Reader
}

func decoderFor(name string) encoding.Encoding {
	switch strings.ToLower(name) {
	case "windows-1252", "cp1252":
		return charmap.Windows1252
	case "iso-8859-1", "latin1", "latin-1":
		return charmap.ISO8859_1
	case "utf-16":
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	default:
		return nil
	}
}

func openCSV(path string, delim rune, enc string) (RowReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var src io.Reader = f
	if d := decoderFor(enc); d != nil {
		src = transform.NewReader(f, d.NewDecoder())
	}
	br := bufio.NewReader(src)
	// Strip UTF-8 byte order mark.
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	r := csv.NewReader(br)
	if delim == 0 {
		delim = ','
	}
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false
	return &csvReader{f: f, r: r}, nil
}

func (c *csvReader) Next() ([]string, error) {
	return c.r.Read()
}

func (c *csvReader) Close() error { return c.f.Close() }

// DelimiterCandidates are the delimiters offered in the wizard.
var DelimiterCandidates = []rune{',', ';', '\t', '|', ' '}

// DelimiterName returns a human readable name for a delimiter.
func DelimiterName(r rune) string {
	switch r {
	case '\t':
		return "Tab"
	case ' ':
		return "Space"
	default:
		return string(r)
	}
}

// ParseDelimiter parses user input like ";", "tab", "\t".
func ParseDelimiter(s string) rune {
	switch strings.ToLower(s) {
	case "tab", `\t`, "\t":
		return '\t'
	case "space", " ":
		return ' '
	case "semicolon":
		return ';'
	case "comma":
		return ','
	case "pipe":
		return '|'
	}
	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return ','
	}
	return r
}

// SniffCSV inspects the beginning of a file and guesses encoding and
// delimiter.
func SniffCSV(path string) (delim rune, enc string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return ',', "utf-8", err
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]

	enc = "utf-8"
	switch {
	case bytes.HasPrefix(buf, []byte{0xFF, 0xFE}) || bytes.HasPrefix(buf, []byte{0xFE, 0xFF}):
		enc = "utf-16"
		if d, e := decoderFor(enc).NewDecoder().Bytes(buf); e == nil {
			buf = d
		}
	case !utf8.Valid(trimIncompleteRune(buf)):
		enc = "windows-1252"
	}
	return sniffDelimiter(string(buf)), enc, nil
}

func trimIncompleteRune(b []byte) []byte {
	// The sample may end in the middle of a multi-byte rune.
	for i := 0; i < 3 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// sniffDelimiter picks the candidate that occurs most consistently per line.
func sniffDelimiter(sample string) rune {
	lines := strings.Split(strings.ReplaceAll(sample, "\r\n", "\n"), "\n")
	if len(lines) > 1 {
		lines = lines[:len(lines)-1] // last line may be cut off
	}
	if len(lines) > 50 {
		lines = lines[:50]
	}
	best, bestScore := ',', 0.0
	for _, cand := range DelimiterCandidates {
		counts := map[int]int{}
		nonEmpty := 0
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			nonEmpty++
			counts[countOutsideQuotes(l, cand)]++
		}
		if nonEmpty == 0 {
			continue
		}
		// Most frequent per-line count (mode) and how consistent it is.
		mode, modeN := 0, 0
		for c, n := range counts {
			if n > modeN || (n == modeN && c > mode) {
				mode, modeN = c, n
			}
		}
		if mode == 0 {
			continue
		}
		consistency := float64(modeN) / float64(nonEmpty)
		score := consistency * float64(mode)
		if cand == ' ' {
			score *= 0.5 // spaces are common in text, prefer others
		}
		if consistency >= 0.6 && score > bestScore {
			best, bestScore = cand, score
		}
	}
	return best
}

func countOutsideQuotes(line string, r rune) int {
	inQ := false
	n := 0
	for _, c := range line {
		switch {
		case c == '"':
			inQ = !inQ
		case c == r && !inQ:
			n++
		}
	}
	return n
}
