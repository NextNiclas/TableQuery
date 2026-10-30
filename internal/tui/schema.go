package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/nextniclas/tablequery/internal/db"
)

const sidebarWidth = 34

type schemaMsg struct {
	tables []db.Table
	err    error
}

func loadSchema(d *db.DB) tea.Cmd {
	return func() tea.Msg {
		t, err := d.Tables(context.Background())
		return schemaMsg{tables: t, err: err}
	}
}

// sidebar shows tables and their columns.
type sidebar struct {
	tables   []db.Table
	err      error
	cursor   int
	expanded map[string]bool
	offset   int
	height   int
	focused  bool
}

func newSidebar() sidebar {
	return sidebar{expanded: map[string]bool{}}
}

func (s *sidebar) set(msg schemaMsg) {
	s.tables, s.err = msg.tables, msg.err
	if s.cursor >= len(s.tables) {
		s.cursor = max(0, len(s.tables)-1)
	}
}

func (s *sidebar) selected() (db.Table, bool) {
	if s.cursor < len(s.tables) {
		return s.tables[s.cursor], true
	}
	return db.Table{}, false
}

func (s *sidebar) Update(msg tea.KeyMsg) {
	switch msg.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.tables)-1 {
			s.cursor++
		}
	case "right", "l", " ":
		if t, ok := s.selected(); ok {
			s.expanded[t.Name] = !s.expanded[t.Name] || msg.String() == "right"
		}
	case "left", "h":
		if t, ok := s.selected(); ok {
			s.expanded[t.Name] = false
		}
	}
}

func (s *sidebar) View() string {
	w := sidebarWidth - 4
	var lines []string
	cursorLine := 0
	lines = append(lines, styleTitle.Render("Tables"))
	if s.err != nil {
		lines = append(lines, styleError.Render(runewidth.Truncate(s.err.Error(), w, "…")))
	}
	if len(s.tables) == 0 {
		lines = append(lines, styleMuted.Render("(none)"), "", styleMuted.Render("Ctrl+F to import"), styleMuted.Render("a CSV/Excel file"))
	}
	for i, t := range s.tables {
		icon := "▸"
		if s.expanded[t.Name] {
			icon = "▾"
		}
		count := ""
		if t.IsView {
			count = "view"
		} else if t.Rows >= 0 {
			count = humanCount(t.Rows)
		}
		name := runewidth.Truncate(t.Name, w-3-len(count), "…")
		pad := w - 2 - runewidth.StringWidth(name) - len(count)
		line := icon + " " + name + strings.Repeat(" ", max(1, pad)) + count
		if i == s.cursor {
			cursorLine = len(lines)
			if s.focused {
				line = styleSelected.Render(runewidth.FillRight(line, w))
			} else {
				line = styleBold.Render(line)
			}
		} else {
			line = styleBold.Render(icon+" "+name) + strings.Repeat(" ", max(1, pad)) + styleMuted.Render(count)
		}
		lines = append(lines, line)
		if s.expanded[t.Name] {
			for _, c := range t.Columns {
				typ := strings.ToLower(c.Type)
				cn := runewidth.Truncate(c.Name, w-4-len(typ), "…")
				pad := w - 4 - runewidth.StringWidth(cn) - len(typ)
				lines = append(lines, "    "+cn+strings.Repeat(" ", max(1, pad))+styleMuted.Render(typ))
			}
		}
	}
	// Scroll so the cursor stays visible.
	h := max(1, s.height)
	if cursorLine < s.offset {
		s.offset = cursorLine
	}
	if cursorLine >= s.offset+h {
		s.offset = cursorLine - h + 1
	}
	if s.offset > len(lines)-1 {
		s.offset = max(0, len(lines)-1)
	}
	end := min(len(lines), s.offset+h)
	return strings.Join(lines[s.offset:end], "\n")
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}
