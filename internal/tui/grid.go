package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/nextniclas/tablequery/internal/db"
)

const (
	maxColWidth = 40
	minColWidth = 3
	colSep      = " │ "
)

// grid is a scrollable result table with horizontal column scrolling.
type grid struct {
	cols    []string
	rows    [][]db.Value
	widths  []int
	numeric []bool

	cursor int // selected row
	offset int // first visible row
	colOff int // first visible column

	width, height int
	focused       bool
}

func (g *grid) SetData(cols []string, rows [][]db.Value) {
	g.cols, g.rows = cols, rows
	g.cursor, g.offset, g.colOff = 0, 0, 0
	g.widths = make([]int, len(cols))
	g.numeric = make([]bool, len(cols))
	for i, c := range cols {
		g.widths[i] = max(minColWidth, runewidth.StringWidth(c))
	}
	sample := rows
	if len(sample) > 1000 {
		sample = sample[:1000]
	}
	for i := range cols {
		num, seen := true, false
		for _, r := range sample {
			if i >= len(r) {
				continue
			}
			v := r[i]
			w := 4 // NULL
			if !v.Null {
				w = runewidth.StringWidth(cleanCell(v.S))
				if _, err := strconv.ParseFloat(v.S, 64); err != nil {
					num = false
				}
				seen = true
			}
			if w > g.widths[i] {
				g.widths[i] = w
			}
		}
		g.numeric[i] = num && seen
		if g.widths[i] > maxColWidth {
			g.widths[i] = maxColWidth
		}
	}
}

func (g *grid) Clear() { g.SetData(nil, nil) }

func (g *grid) bodyHeight() int { return max(1, g.height-2) }

func (g *grid) Update(msg tea.KeyMsg) {
	page := g.bodyHeight()
	switch msg.String() {
	case "up", "k":
		g.cursor--
	case "down", "j":
		g.cursor++
	case "pgup":
		g.cursor -= page
	case "pgdown", " ":
		g.cursor += page
	case "home", "g":
		g.cursor = 0
	case "end", "G":
		g.cursor = len(g.rows) - 1
	case "left", "h":
		if g.colOff > 0 {
			g.colOff--
		}
	case "right", "l":
		if g.colOff < len(g.cols)-1 {
			g.colOff++
		}
	case "shift+left", "0":
		g.colOff = 0
	case "shift+right", "$":
		g.colOff = max(0, len(g.cols)-1)
	}
	g.clamp()
}

func (g *grid) clamp() {
	if g.cursor >= len(g.rows) {
		g.cursor = len(g.rows) - 1
	}
	if g.cursor < 0 {
		g.cursor = 0
	}
	page := g.bodyHeight()
	if g.cursor < g.offset {
		g.offset = g.cursor
	}
	if g.cursor >= g.offset+page {
		g.offset = g.cursor - page + 1
	}
	if g.offset < 0 {
		g.offset = 0
	}
}

func cleanCell(s string) string {
	if strings.ContainsAny(s, "\n\r\t") {
		s = strings.NewReplacer("\r\n", "↵", "\n", "↵", "\r", "↵", "\t", " ").Replace(s)
	}
	return s
}

func fit(s string, w int, right bool) string {
	s = runewidth.Truncate(s, w, "…")
	pad := w - runewidth.StringWidth(s)
	if pad <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", pad) + s
	}
	return s + strings.Repeat(" ", pad)
}

// visibleCols returns the column indexes that fit into the width, starting
// at colOff. The last column may be shrunk.
func (g *grid) visibleCols() ([]int, []int) {
	var idx, widths []int
	used := 0
	for i := g.colOff; i < len(g.cols); i++ {
		w := g.widths[i]
		sep := 0
		if len(idx) > 0 {
			sep = runewidth.StringWidth(colSep)
		}
		if used+sep+w > g.width {
			rest := g.width - used - sep
			if rest >= minColWidth+1 || len(idx) == 0 {
				idx = append(idx, i)
				widths = append(widths, max(1, rest))
			}
			break
		}
		idx = append(idx, i)
		widths = append(widths, w)
		used += sep + w
	}
	return idx, widths
}

func (g *grid) View() string {
	if len(g.cols) == 0 {
		return ""
	}
	idx, widths := g.visibleCols()
	var b strings.Builder
	sep := styleMuted.Render(colSep)

	// Header
	var hdr []string
	for k, i := range idx {
		hdr = append(hdr, styleHeader.Render(fit(g.cols[i], widths[k], g.numeric[i])))
	}
	b.WriteString(strings.Join(hdr, sep))
	b.WriteByte('\n')
	var line []string
	for _, w := range widths {
		line = append(line, strings.Repeat("─", w))
	}
	b.WriteString(styleMuted.Render(strings.Join(line, "─┼─")))

	end := min(len(g.rows), g.offset+g.bodyHeight())
	for r := g.offset; r < end; r++ {
		b.WriteByte('\n')
		row := g.rows[r]
		selected := g.focused && r == g.cursor
		var cells []string
		for k, i := range idx {
			var cell string
			switch {
			case i >= len(row):
				cell = fit("", widths[k], false)
			case row[i].Null:
				cell = fit("NULL", widths[k], g.numeric[i])
				if !selected {
					cell = styleNull.Render(cell)
				}
			default:
				cell = fit(cleanCell(row[i].S), widths[k], g.numeric[i])
			}
			cells = append(cells, cell)
		}
		var text string
		if selected {
			text = strings.Join(cells, colSep)
			if pad := g.width - runewidth.StringWidth(text); pad > 0 {
				text += strings.Repeat(" ", pad)
			}
			text = styleSelected.Render(text)
		} else {
			text = strings.Join(cells, sep)
		}
		b.WriteString(text)
	}
	return b.String()
}

// position returns a "row x/y, col a/b" indicator.
func (g *grid) position() string {
	if len(g.cols) == 0 {
		return ""
	}
	r := 0
	if len(g.rows) > 0 {
		r = g.cursor + 1
	}
	return "row " + strconv.Itoa(r) + "/" + strconv.Itoa(len(g.rows)) +
		" · col " + strconv.Itoa(g.colOff+1) + "/" + strconv.Itoa(len(g.cols))
}
