package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/nextniclas/tablequery/internal/db"
	"github.com/nextniclas/tablequery/internal/importer"
)

// deriveDialog creates a table or view from the current tab's query.
type deriveDialog struct {
	query    string
	name     textinput.Model
	field    int // 0 name, 1 kind, 2 if exists
	view     bool
	ifExists int
	err      error
	busy     bool
}

type deriveDoneMsg struct {
	name string
	view bool
	rows int64
	err  error
}

var deriveModes = []string{db.IfExistsFail, db.IfExistsReplace, db.IfExistsAppend}

func newDeriveDialog(query, defaultName string) *deriveDialog {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.CharLimit = 128
	ti.Width = 40
	ti.SetValue(defaultName)
	ti.CursorEnd()
	ti.Focus()
	return &deriveDialog{query: query, name: ti}
}

func (d *deriveDialog) kindName() string {
	if d.view {
		return "view"
	}
	return "table"
}

// Update handles keys. It returns the command to run and whether the
// dialog should be closed.
func (d *deriveDialog) Update(k tea.KeyMsg, database *db.DB) (tea.Cmd, bool) {
	if d.busy {
		return nil, false
	}
	switch k.String() {
	case "esc":
		return nil, true
	case "enter":
		name := strings.TrimSpace(d.name.Value())
		if name == "" {
			d.err = fmt.Errorf("name must not be empty")
			return nil, false
		}
		d.err = nil
		d.busy = true
		opts := db.DeriveOptions{View: d.view, IfExists: deriveModes[d.ifExists]}
		query := d.query
		return func() tea.Msg {
			rows, err := database.CreateFromQuery(context.Background(), name, query, opts)
			return deriveDoneMsg{name: name, view: opts.View, rows: rows, err: err}
		}, false
	case "up", "shift+tab":
		d.setField((d.field + 2) % 3)
		return nil, false
	case "down", "tab":
		d.setField((d.field + 1) % 3)
		return nil, false
	}
	switch d.field {
	case 1:
		switch k.String() {
		case "left", "right", " ":
			d.view = !d.view
			if d.view && deriveModes[d.ifExists] == db.IfExistsAppend {
				d.ifExists = 0
			}
		}
	case 2:
		delta := 0
		switch k.String() {
		case "left":
			delta = -1
		case "right", " ":
			delta = 1
		}
		if delta != 0 {
			n := len(deriveModes)
			d.ifExists = (d.ifExists + delta + n) % n
			// Views cannot be appended to.
			if d.view && deriveModes[d.ifExists] == db.IfExistsAppend {
				d.ifExists = (d.ifExists + delta + n) % n
			}
		}
	default:
		var cmd tea.Cmd
		d.name, cmd = d.name.Update(k)
		return cmd, false
	}
	return nil, false
}

func (d *deriveDialog) setField(f int) {
	d.field = f
	if f == 0 {
		d.name.Focus()
	} else {
		d.name.Blur()
	}
}

func (d *deriveDialog) View(width int) string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("New table from query") + "\n\n")
	cursor := func(i int) string {
		if d.field == i {
			return styleKey.Render("▶ ")
		}
		return "  "
	}
	choice := func(i int, v string) string {
		if d.field == i {
			return styleSelected.Render(" ‹ " + v + " › ")
		}
		return "   " + v
	}
	b.WriteString(cursor(0) + fmt.Sprintf("%-11s", "Name") + d.name.View() + "\n")
	b.WriteString(cursor(1) + fmt.Sprintf("%-11s", "Create") + choice(1, d.kindName()) + "\n")
	b.WriteString(cursor(2) + fmt.Sprintf("%-11s", "If exists") + choice(2, deriveModes[d.ifExists]) + "\n\n")

	w := max(20, min(90, width-12))
	lines := strings.Split(d.query, "\n")
	if len(lines) > 6 {
		lines = append(lines[:5], "…")
	}
	for i, l := range lines {
		lines[i] = runewidth.Truncate(strings.ReplaceAll(l, "\t", "  "), w, "…")
	}
	b.WriteString(styleMuted.Render(strings.Join(lines, "\n")) + "\n\n")
	if d.view {
		b.WriteString(styleMuted.Render("A view stores the query, not the data: it always reflects the current tables.") + "\n\n")
	} else {
		b.WriteString(styleMuted.Render("The table stores a snapshot of the query result.") + "\n\n")
	}
	switch {
	case d.busy:
		b.WriteString(styleWarn.Render("Creating…"))
	case d.err != nil:
		b.WriteString(styleError.Render("⚠ "+oneLine(d.err.Error())) + "\n\n")
		fallthrough
	default:
		if !d.busy {
			b.WriteString(keyHelp("↑/↓", "field", "←/→", "change", "Enter", "create", "Esc", "cancel"))
		}
	}
	return styleDialog.Render(b.String())
}

// defaultDeriveName suggests a table name for a tab.
func defaultDeriveName(tabTitle string) string {
	n := importer.NormalizeName(tabTitle)
	if n == "" {
		n = "query_result"
	}
	return n
}
