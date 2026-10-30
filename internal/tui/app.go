// Package tui implements the interactive terminal user interface.
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nextniclas/tablequery/internal/db"
	"github.com/nextniclas/tablequery/internal/export"
	"github.com/nextniclas/tablequery/internal/importer"
)

type focusArea int

const (
	focusEditor focusArea = iota
	focusGrid
	focusSidebar
)

type promptKind int

const (
	promptOpenDB promptKind = iota
	promptSaveDB
	promptExport
	promptRenameTab
)

type prompt struct {
	kind  promptKind
	title string
	hint  string
	input pathInput
	delim rune
}

type confirmDialog struct {
	text  string
	onYes func() tea.Cmd
}

type (
	tickMsg   struct{}
	statusMsg struct {
		text string
		err  bool
	}
	dbOpenedMsg struct {
		db   *db.DB
		err  error
		note string
	}
)

// Model is the root Bubble Tea model.
type Model struct {
	db *db.DB

	tabs   []*queryTab
	active int
	nextID int

	side     sidebar
	showSide bool
	focus    focusArea

	wizard  *wizard
	prompt  *prompt
	confirm *confirmDialog
	help    bool

	status    string
	statusErr bool
	statusAt  time.Time
	ticking   bool

	initialImport string
	width, height int
}

// New creates the root model. If importPath is set the import wizard opens
// with this file pre-selected.
func New(d *db.DB, importPath string) *Model {
	m := &Model{db: d, side: newSidebar(), showSide: true, initialImport: importPath}
	m.addTab()
	return m
}

// Run starts the TUI.
func Run(d *db.DB, importPath string) error {
	m := New(d, importPath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if fm, ok := final.(*Model); ok && fm.db != nil && fm.db != d {
		fm.db.Close()
	}
	return err
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{loadSchema(m.db), m.tab().editor.Focus()}
	if m.initialImport != "" {
		m.wizard = newWizard(m.db, m.initialImport)
		cmds = append(cmds, m.wizard.openFile(), m.wizard.Init())
	}
	return tea.Batch(cmds...)
}

func (m *Model) tab() *queryTab { return m.tabs[m.active] }

func (m *Model) addTab() *queryTab {
	m.nextID++
	t := newQueryTab(m.nextID)
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.layout()
	return t
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status, m.statusErr, m.statusAt = s, isErr, time.Now()
}

func (m *Model) setFocus(f focusArea) tea.Cmd {
	if f == focusSidebar && !m.showSide {
		f = focusEditor
	}
	m.focus = f
	t := m.tab()
	t.grid.focused = f == focusGrid
	m.side.focused = f == focusSidebar
	if f == focusEditor {
		return t.editor.Focus()
	}
	t.editor.Blur()
	return nil
}

func (m *Model) switchTab(i int) tea.Cmd {
	if i < 0 || i >= len(m.tabs) {
		return nil
	}
	m.tab().editor.Blur()
	m.tab().grid.focused = false
	m.active = i
	m.layout()
	return m.setFocus(m.focus)
}

// ---------------------------------------------------------------- layout

func (m *Model) mainWidth() int {
	w := m.width
	if m.showSide {
		w -= sidebarWidth
	}
	return max(20, w)
}

func (m *Model) bodyHeight() int { return max(8, m.height-3) }

func (m *Model) editorHeight() int { return max(3, m.bodyHeight()/3-2) }

func (m *Model) layout() {
	if m.width == 0 {
		return
	}
	mw := m.mainWidth()
	eh := m.editorHeight()
	for _, t := range m.tabs {
		t.editor.SetWidth(mw - 2)
		t.editor.SetHeight(eh)
		t.grid.width = mw - 2
		t.grid.height = m.bodyHeight() - eh - 4
		t.grid.clamp()
	}
	m.side.height = m.bodyHeight() - 2
	if m.wizard != nil {
		m.wizard.setSize(m.width, m.height)
	}
}

// ---------------------------------------------------------------- update

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case schemaMsg:
		m.side.set(msg)
		return m, nil

	case queryDoneMsg:
		for _, t := range m.tabs {
			if t.id == msg.tabID {
				t.finish(msg)
				if msg.err == nil && !msg.res.IsQuery {
					// DDL/DML may have changed the schema.
					return m, loadSchema(m.db)
				}
			}
		}
		return m, nil

	case tickMsg:
		m.ticking = false
		return m, m.maybeTick()

	case statusMsg:
		m.setStatus(msg.text, msg.err)
		return m, nil

	case dbOpenedMsg:
		if msg.err != nil {
			m.setStatus(msg.err.Error(), true)
			return m, nil
		}
		old := m.db
		m.db = msg.db
		if old != nil && old != msg.db {
			old.Close()
		}
		for _, t := range m.tabs {
			t.result, t.err = nil, nil
			t.grid.Clear()
		}
		m.side = newSidebar()
		m.side.height = m.bodyHeight() - 2
		m.setStatus(msg.note, false)
		return m, loadSchema(m.db)

	case wizardClosedMsg:
		m.wizard = nil
		cmds := []tea.Cmd{loadSchema(m.db), m.setFocus(focusEditor)}
		if r := msg.result; r != nil {
			m.setStatus(fmt.Sprintf("Imported %d rows into %s", r.Rows, r.Table), false)
			t := m.tab()
			if strings.TrimSpace(t.editor.Value()) != "" {
				t = m.addTab()
				cmds = append(cmds, m.setFocus(focusEditor))
			}
			t.editor.SetValue(fmt.Sprintf("SELECT * FROM %s LIMIT 100;", importer.QuoteIdent(r.Table)))
			cmds = append(cmds, t.run(m.db), m.maybeTick())
		}
		return m, tea.Batch(cmds...)

	case importProgressMsg, importDoneMsg:
		if m.wizard != nil {
			return m, m.wizard.Update(msg)
		}
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}

	// Forward other messages (cursor blink etc.).
	if m.wizard != nil {
		return m, m.wizard.Update(msg)
	}
	if m.prompt != nil {
		return m, m.prompt.input.Update(msg)
	}
	var cmd tea.Cmd
	t := m.tab()
	t.editor, cmd = t.editor.Update(msg)
	return m, cmd
}

func (m *Model) maybeTick() tea.Cmd {
	if m.ticking {
		return nil
	}
	for _, t := range m.tabs {
		if t.running {
			m.ticking = true
			return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
		}
	}
	return nil
}

func (m *Model) quit() tea.Cmd {
	for _, t := range m.tabs {
		t.stop()
	}
	if m.wizard != nil && m.wizard.cancel != nil {
		m.wizard.cancel()
	}
	return tea.Quit
}

func (m *Model) handleKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" || key == "ctrl+q" {
		return m.quit()
	}
	if m.wizard != nil {
		return m.wizard.Update(k)
	}
	if m.confirm != nil {
		c := m.confirm
		switch key {
		case "y", "Y", "enter":
			m.confirm = nil
			return c.onYes()
		case "n", "N", "esc":
			m.confirm = nil
		}
		return nil
	}
	if m.prompt != nil {
		return m.updatePrompt(k)
	}
	if m.help {
		m.help = false
		return nil
	}

	t := m.tab()
	switch key {
	case "f1":
		m.help = true
		return nil
	case "?":
		if m.focus != focusEditor {
			m.help = true
			return nil
		}
	case "ctrl+r", "f5":
		m.status = ""
		return tea.Batch(t.run(m.db), m.maybeTick())
	case "ctrl+t":
		m.addTab()
		return m.setFocus(focusEditor)
	case "ctrl+w":
		return m.closeTab()
	case "ctrl+right", "ctrl+pgdown", "f8":
		return m.switchTab((m.active + 1) % len(m.tabs))
	case "ctrl+left", "ctrl+pgup", "f7":
		return m.switchTab((m.active - 1 + len(m.tabs)) % len(m.tabs))
	case "alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9":
		return m.switchTab(int(key[4] - '1'))
	case "f2":
		m.openPrompt(promptRenameTab, "Rename tab", "", t.title)
		return nil
	case "ctrl+f":
		m.wizard = newWizard(m.db, "")
		m.wizard.setSize(m.width, m.height)
		return m.wizard.Init()
	case "ctrl+o":
		m.openPrompt(promptOpenDB, "Open database", "SQLite file (created if missing) or :memory:", "")
		return nil
	case "ctrl+s":
		def := ""
		if !m.db.IsMemory() {
			def = strings.TrimSuffix(m.db.Path, filepath.Ext(m.db.Path)) + "-copy.db"
		}
		title := "Save database as"
		hint := "The in-memory database is written to this file and then opened."
		if !m.db.IsMemory() {
			hint = "A copy of the current database is written to this file."
		}
		m.openPrompt(promptSaveDB, title, hint, def)
		return nil
	case "ctrl+e":
		if t.result == nil || !t.result.IsQuery {
			m.setStatus("Nothing to export: run a query first", true)
			return nil
		}
		m.openPrompt(promptExport, "Export result to CSV", "", safeFileName(t.title)+".csv")
		m.prompt.delim = ','
		return nil
	case "ctrl+b":
		m.showSide = !m.showSide
		m.layout()
		if m.showSide {
			return m.setFocus(focusSidebar)
		}
		return m.setFocus(focusEditor)
	case "tab":
		return m.setFocus(m.nextFocus(1))
	case "shift+tab":
		return m.setFocus(m.nextFocus(-1))
	case "esc":
		if t.running {
			t.stop()
			return nil
		}
		if m.focus != focusEditor {
			return m.setFocus(focusEditor)
		}
		return nil
	}

	switch m.focus {
	case focusGrid:
		t.grid.Update(k)
		return nil
	case focusSidebar:
		return m.updateSidebar(k)
	}
	var cmd tea.Cmd
	t.editor, cmd = t.editor.Update(k)
	return cmd
}

func (m *Model) nextFocus(delta int) focusArea {
	order := []focusArea{focusEditor, focusGrid}
	if m.showSide {
		order = append(order, focusSidebar)
	}
	for i, f := range order {
		if f == m.focus {
			return order[(i+delta+len(order))%len(order)]
		}
	}
	return focusEditor
}

func (m *Model) closeTab() tea.Cmd {
	t := m.tab()
	t.stop()
	if len(m.tabs) == 1 {
		t.editor.Reset()
		t.result, t.err = nil, nil
		t.grid.Clear()
		return m.setFocus(focusEditor)
	}
	m.tabs = append(m.tabs[:m.active], m.tabs[m.active+1:]...)
	if m.active >= len(m.tabs) {
		m.active = len(m.tabs) - 1
	}
	return m.setFocus(m.focus)
}

func (m *Model) updateSidebar(k tea.KeyMsg) tea.Cmd {
	tbl, ok := m.side.selected()
	switch k.String() {
	case "enter":
		if !ok {
			return nil
		}
		t := m.tab()
		t.editor.SetValue(fmt.Sprintf("SELECT * FROM %s LIMIT 100;", importer.QuoteIdent(tbl.Name)))
		return tea.Batch(t.run(m.db), m.maybeTick())
	case "i":
		if ok {
			m.tab().editor.InsertString(importer.QuoteIdent(tbl.Name))
			return m.setFocus(focusEditor)
		}
	case "r":
		return loadSchema(m.db)
	case "d", "delete":
		if !ok {
			return nil
		}
		kind := "table"
		if tbl.IsView {
			kind = "view"
		}
		m.confirm = &confirmDialog{
			text: fmt.Sprintf("Drop %s %q? This cannot be undone.", kind, tbl.Name),
			onYes: func() tea.Cmd {
				if err := m.db.DropTable(context.Background(), tbl); err != nil {
					m.setStatus(err.Error(), true)
					return nil
				}
				m.setStatus(fmt.Sprintf("Dropped %s %s", kind, tbl.Name), false)
				return loadSchema(m.db)
			},
		}
	default:
		m.side.Update(k)
	}
	return nil
}

// ---------------------------------------------------------------- prompts

func (m *Model) openPrompt(kind promptKind, title, hint, value string) {
	var exts []string
	switch kind {
	case promptOpenDB, promptSaveDB:
		exts = []string{".db", ".sqlite", ".sqlite3", ".db3"}
	case promptExport:
		exts = []string{".csv", ".tsv", ".txt"}
	}
	in := newPathInput("", exts)
	if kind == promptRenameTab {
		in.input.Placeholder = "tab name"
	}
	in.SetWidth(min(70, m.width-12))
	in.SetValue(value)
	m.prompt = &prompt{kind: kind, title: title, hint: hint, input: in}
	m.tab().editor.Blur()
}

func (m *Model) closePrompt() tea.Cmd {
	m.prompt = nil
	return m.setFocus(m.focus)
}

func (m *Model) updatePrompt(k tea.KeyMsg) tea.Cmd {
	p := m.prompt
	switch k.String() {
	case "esc":
		return m.closePrompt()
	case "ctrl+d":
		if p.kind == promptExport {
			p.delim = nextDelim(p.delim)
		}
		return nil
	case "enter":
		val := p.input.Value()
		if p.kind == promptRenameTab {
			if v := strings.TrimSpace(p.input.input.Value()); v != "" {
				m.tab().title = v
			}
			return m.closePrompt()
		}
		if val == "" {
			return nil
		}
		cmd := m.submitPrompt(p, val)
		return tea.Batch(m.closePrompt(), cmd)
	}
	return p.input.Update(k)
}

func nextDelim(r rune) rune {
	order := []rune{',', ';', '\t', '|'}
	for i, d := range order {
		if d == r {
			return order[(i+1)%len(order)]
		}
	}
	return ','
}

func (m *Model) submitPrompt(p *prompt, path string) tea.Cmd {
	switch p.kind {
	case promptOpenDB:
		open := func() tea.Cmd {
			return func() tea.Msg {
				d, err := db.Open(path)
				note := ""
				if err == nil {
					note = "Opened " + d.Path
				}
				return dbOpenedMsg{db: d, err: err, note: note}
			}
		}
		if m.db.IsMemory() && len(m.side.tables) > 0 {
			m.confirm = &confirmDialog{
				text:  "The current in-memory database will be discarded. Continue? (Ctrl+S saves it first)",
				onYes: open,
			}
			return nil
		}
		return open()

	case promptSaveDB:
		save := func() tea.Cmd {
			d := m.db
			return func() tea.Msg {
				if _, err := os.Stat(path); err == nil {
					if err := os.Remove(path); err != nil {
						return statusMsg{text: err.Error(), err: true}
					}
				}
				if err := d.SaveAs(context.Background(), path); err != nil {
					return statusMsg{text: "Save failed: " + err.Error(), err: true}
				}
				if !d.IsMemory() {
					return statusMsg{text: "Saved a copy to " + path}
				}
				nd, err := db.Open(path)
				if err != nil {
					return statusMsg{text: "Saved to " + path + " but could not open it: " + err.Error(), err: true}
				}
				return dbOpenedMsg{db: nd, note: "Saved to " + path + " — now working on this file"}
			}
		}
		if abs(path) == abs(m.db.Path) {
			m.setStatus("Cannot save the database onto itself", true)
			return nil
		}
		if _, err := os.Stat(path); err == nil {
			m.confirm = &confirmDialog{text: fmt.Sprintf("%s exists. Overwrite?", path), onYes: save}
			return nil
		}
		return save()

	case promptExport:
		t := m.tab()
		res := t.result
		delim := p.delim
		if strings.EqualFold(filepath.Ext(path), ".tsv") && delim == ',' {
			delim = '\t'
		}
		run := func() tea.Cmd {
			d := m.db
			return func() tea.Msg {
				opts := export.Options{Delimiter: delim}
				var n int
				var err error
				if res.Truncated {
					n, err = export.QueryToCSV(context.Background(), d, res.SQL, path, opts)
				} else {
					n, err = len(res.Rows), export.ResultToCSV(res, path, opts)
				}
				if err != nil {
					return statusMsg{text: "Export failed: " + err.Error(), err: true}
				}
				return statusMsg{text: fmt.Sprintf("Exported %d rows to %s", n, path)}
			}
		}
		if _, err := os.Stat(path); err == nil {
			m.confirm = &confirmDialog{text: fmt.Sprintf("%s exists. Overwrite?", path), onYes: run}
			return nil
		}
		return run()
	}
	return nil
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeFileName(s string) string {
	s = strings.Trim(unsafeChars.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if s == "" {
		return "result"
	}
	return s
}

// ---------------------------------------------------------------- view

func (m *Model) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.wizard != nil {
		return m.wizard.View()
	}
	if m.help {
		return m.center(helpView())
	}
	if m.confirm != nil {
		return m.center(styleDialog.Render(styleWarn.Render(m.confirm.text) + "\n\n" + keyHelp("y/Enter", "yes", "n/Esc", "no")))
	}
	if m.prompt != nil {
		return m.center(m.promptView())
	}

	t := m.tab()
	mw := m.mainWidth()
	eh := m.editorHeight()

	editorStyle, gridStyle := stylePanel, stylePanel
	switch m.focus {
	case focusEditor:
		editorStyle = stylePanelFocused
	case focusGrid:
		gridStyle = stylePanelFocused
	}
	editor := editorStyle.Width(mw - 2).Render(t.editor.View())
	gh := m.bodyHeight() - eh - 4
	gridContent := t.grid.View()
	if gridContent == "" {
		switch {
		case t.result != nil && !t.result.IsQuery:
			gridContent = styleMuted.Render("Statement executed.")
		default:
			gridContent = styleMuted.Render("Results appear here. Ctrl+R/F5 runs the query, Tab switches focus.")
		}
	}
	results := gridStyle.Width(mw - 2).Height(gh).MaxHeight(gh + 2).Render(gridContent)
	main := lipgloss.JoinVertical(lipgloss.Left, editor, results)

	body := main
	if m.showSide {
		sideStyle := stylePanel
		if m.focus == focusSidebar {
			sideStyle = stylePanelFocused
		}
		side := sideStyle.Width(sidebarWidth-2).Height(m.bodyHeight()-2).MaxHeight(m.bodyHeight()).
			Padding(0, 1).Render(m.side.View())
		body = lipgloss.JoinHorizontal(lipgloss.Top, side, main)
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.tabBar(), body, m.statusBar())
}

func (m *Model) center(s string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, s)
}

func (m *Model) tabBar() string {
	var parts []string
	for i, t := range m.tabs {
		label := t.title
		if t.running {
			label += " ⟳"
		}
		if i < 9 {
			label = fmt.Sprintf("%d:%s", i+1, label)
		}
		if i == m.active {
			parts = append(parts, styleTabActive.Render(label))
		} else {
			parts = append(parts, styleTabInactive.Render(label))
		}
	}
	bar := strings.Join(parts, " ")
	dbLabel := "◆ " + m.db.Path
	if m.db.IsMemory() {
		dbLabel = "◆ in-memory database"
	}
	dbLabel = styleMuted.Render(runewidth.Truncate(dbLabel, max(10, m.width/3), "…"))
	gap := m.width - lipgloss.Width(bar) - lipgloss.Width(dbLabel)
	if gap < 1 {
		return truncateANSI(bar, m.width)
	}
	return bar + strings.Repeat(" ", gap) + dbLabel
}

func (m *Model) statusBar() string {
	line := m.tab().statusLine()
	if m.status != "" && time.Since(m.statusAt) < 8*time.Second {
		if m.statusErr {
			line = styleError.Render(oneLine(m.status))
		} else {
			line = styleOK.Render(oneLine(m.status))
		}
	}
	line = truncateANSI(line, m.width)
	var hints string
	switch m.focus {
	case focusGrid:
		hints = keyHelp("↑↓←→", "scroll", "PgUp/PgDn", "page", "Ctrl+E", "export", "Tab", "focus", "F1", "help")
	case focusSidebar:
		hints = keyHelp("Enter", "query", "i", "insert name", "→", "columns", "d", "drop", "Ctrl+F", "import", "F1", "help")
	default:
		hints = keyHelp("Ctrl+R", "run", "Ctrl+F", "import", "Ctrl+T/W", "new/close tab", "Ctrl+E", "export", "Ctrl+O", "open DB", "Ctrl+S", "save DB", "F1", "help")
	}
	return line + "\n" + truncateANSI(hints, m.width)
}

func (m *Model) promptView() string {
	p := m.prompt
	var b strings.Builder
	b.WriteString(styleTitle.Render(p.title) + "\n")
	if p.hint != "" {
		b.WriteString(styleMuted.Render(p.hint) + "\n")
	}
	b.WriteString("\n" + p.input.View() + "\n\n")
	switch p.kind {
	case promptExport:
		b.WriteString("Delimiter: " + styleKey.Render(importer.DelimiterName(p.delim)) + styleMuted.Render("  (Ctrl+D to change)") + "\n")
		if r := m.tab().result; r != nil && r.Truncated {
			b.WriteString(styleMuted.Render("The query is re-run to export all rows.") + "\n")
		}
		b.WriteString("\n" + keyHelp("Enter", "export", "Tab", "complete", "Esc", "cancel"))
	case promptRenameTab:
		b.WriteString(keyHelp("Enter", "rename", "Esc", "cancel"))
	default:
		b.WriteString(keyHelp("Enter", "ok", "Tab", "complete", "Esc", "cancel"))
	}
	return styleDialog.Render(b.String())
}

func helpView() string {
	rows := [][2]string{
		{"Ctrl+R / F5", "run the SQL of the current tab (multiple statements allowed)"},
		{"Esc", "cancel a running query / back to editor"},
		{"Tab / Shift+Tab", "cycle focus: editor → results → tables"},
		{"Ctrl+T / Ctrl+W", "new tab / close tab"},
		{"Ctrl+←/→, F7/F8", "previous / next tab"},
		{"Alt+1…9", "jump to tab"},
		{"F2", "rename tab"},
		{"Ctrl+F", "import a CSV or Excel file (wizard)"},
		{"Ctrl+E", "export result to CSV"},
		{"Ctrl+O", "open / create a database file (or :memory:)"},
		{"Ctrl+S", "save database as file"},
		{"Ctrl+B", "show / hide table sidebar"},
		{"F1", "this help"},
		{"Ctrl+Q / Ctrl+C", "quit"},
		{"", ""},
		{"Results", "↑↓ PgUp PgDn Home End rows · ←→ columns · 0/$ first/last column"},
		{"Tables", "Enter query · i insert name · →/Space columns · d drop · r refresh"},
	}
	var b strings.Builder
	b.WriteString(styleTitle.Render("TableQuery — keyboard shortcuts") + "\n\n")
	for _, r := range rows {
		if r[0] == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(styleKey.Render(fmt.Sprintf("%-18s", r[0])) + " " + r[1] + "\n")
	}
	b.WriteString("\n" + styleMuted.Render("press any key to close"))
	return styleDialog.Render(b.String())
}
