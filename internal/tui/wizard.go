package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/nextniclas/tablequery/internal/db"
	"github.com/nextniclas/tablequery/internal/importer"
)

type wizardStep int

const (
	stepFile wizardStep = iota
	stepSource
	stepColumns
	stepTarget
	stepRunning
	stepDone
)

var stepNames = []string{"File", "Source", "Columns", "Target", "Import"}

const previewRows = 200

// Source option fields.
const (
	fieldSheet = iota
	fieldDelimiter
	fieldEncoding
	fieldHeaderRow
	fieldStartRow
	fieldSkipEmpty
	fieldDecimal
)

var decimalModes = []byte{0, '.', ','}

type importProgressMsg struct{ p importer.Progress }
type importDoneMsg struct {
	res *importer.Result
	err error
}

// wizardClosedMsg is sent to the app when the wizard is closed. result is
// nil if the import was cancelled.
type wizardClosedMsg struct{ result *importer.Result }

type colStats struct {
	samples []string
	bad     []bool
	errors  int
}

type wizard struct {
	db   *db.DB
	step wizardStep

	file   pathInput
	files  []string // all files to import; files[0] defines the columns
	path   string   // == files[0]
	format importer.Format
	sheets []string
	opts   importer.SourceOptions

	sniffDelim rune // delimiter/encoding detected for the first CSV file
	sniffEnc   string

	preview    [][]string
	previewErr error
	previewKey string
	srcField   int

	analysis  *importer.Analysis
	cols      []importer.Column
	stats     []colStats
	colCursor int
	colOffset int
	editing   bool
	nameInput textinput.Model

	tableInput  textinput.Model
	ifExists    int
	targetField int
	tableExists bool
	srcColOn    bool
	srcColInput textinput.Model

	progress importer.Progress
	result   *importer.Result
	err      error
	cancel   context.CancelFunc
	ch       chan tea.Msg

	width, height int
}

var importExts = []string{".csv", ".tsv", ".txt", ".tab", ".dat", ".xlsx", ".xlsm", ".xltx", ".xltm"}

func newWizard(d *db.DB, path string) *wizard {
	w := &wizard{db: d, file: newPathInput("path/to/file.csv or .xlsx  (Tab completes)", importExts)}
	w.nameInput = textinput.New()
	w.nameInput.Prompt = ""
	w.nameInput.CharLimit = 128
	w.tableInput = textinput.New()
	w.tableInput.Prompt = "› "
	w.tableInput.CharLimit = 128
	w.srcColInput = textinput.New()
	w.srcColInput.Prompt = ""
	w.srcColInput.CharLimit = 64
	w.srcColInput.Width = 20
	w.srcColInput.SetValue("source_file")
	if path != "" {
		w.file.SetValue(path)
	}
	return w
}

func (w *wizard) Init() tea.Cmd { return textinput.Blink }

func (w *wizard) setSize(width, height int) {
	w.width, w.height = width, height
	w.file.SetWidth(min(80, width-8))
	w.tableInput.Width = 40
}

func (w *wizard) srcFields() []int {
	if w.format == importer.FormatXLSX {
		return []int{fieldSheet, fieldHeaderRow, fieldStartRow, fieldSkipEmpty, fieldDecimal}
	}
	return []int{fieldDelimiter, fieldEncoding, fieldHeaderRow, fieldStartRow, fieldSkipEmpty, fieldDecimal}
}

func closeWizard(res *importer.Result) tea.Cmd {
	return func() tea.Msg { return wizardClosedMsg{result: res} }
}

func (w *wizard) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case importProgressMsg:
		w.progress = msg.p
		return waitForImport(w.ch)
	case importDoneMsg:
		w.cancel = nil
		w.result, w.err = msg.res, msg.err
		w.step = stepDone
		return nil
	case tea.KeyMsg:
		return w.handleKey(msg)
	}
	switch w.step {
	case stepFile:
		return w.file.Update(msg)
	}
	return nil
}

func (w *wizard) handleKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	switch w.step {
	case stepFile:
		switch key {
		case "esc":
			return closeWizard(nil)
		case "enter":
			return w.openFile()
		case "ctrl+l":
			return w.addInput(false)
		case "ctrl+x":
			if n := len(w.files); n > 0 {
				w.files = w.files[:n-1]
				w.previewErr = nil
			}
			return nil
		}
		return w.file.Update(k)

	case stepSource:
		return w.updateSource(k)

	case stepColumns:
		return w.updateColumns(k)

	case stepTarget:
		return w.updateTarget(k)

	case stepRunning:
		if key == "esc" && w.cancel != nil {
			w.cancel()
		}
		return nil

	case stepDone:
		switch key {
		case "enter", "esc":
			if w.err != nil {
				w.err = nil
				w.step = stepTarget
				return nil
			}
			return closeWizard(w.result)
		}
	}
	return nil
}

// openFile handles Enter in the file step: a single plain file path continues
// right away, a pattern or directory is added to the list, and Enter on an
// empty line continues with the collected files.
func (w *wizard) openFile() tea.Cmd {
	if strings.TrimSpace(w.file.input.Value()) == "" {
		if len(w.files) == 0 {
			return nil
		}
		return w.proceed()
	}
	return w.addInput(true)
}

// addInput expands the input line (file, glob or directory) and adds the
// files to the list. With fast set, a single plain file continues directly.
func (w *wizard) addInput(fast bool) tea.Cmd {
	raw := strings.TrimSpace(w.file.input.Value())
	if raw == "" {
		return nil
	}
	input := w.file.Value()
	paths, err := importer.ExpandPaths([]string{input})
	if err != nil {
		w.previewErr = err
		return nil
	}
	merged := append(append([]string(nil), w.files...), paths...)
	merged = dedupePaths(merged)
	if err := importer.CheckSameFormat(merged); err != nil {
		w.previewErr = err
		return nil
	}
	plain := !strings.ContainsAny(input, "*?[")
	if st, err := os.Stat(input); err == nil && st.IsDir() {
		plain = false
	}
	w.files = merged
	w.previewErr = nil
	w.file.SetValue("")
	if fast && plain && len(w.files) == 1 {
		return w.proceed()
	}
	return nil
}

func dedupePaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		key := p
		if a, err := filepath.Abs(p); err == nil {
			key = a
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

// proceed opens the first file and moves on to the source options.
func (w *wizard) proceed() tea.Cmd {
	path := w.files[0]
	st, err := os.Stat(path)
	if err != nil {
		w.previewErr = err
		return nil
	}
	if st.IsDir() {
		w.previewErr = fmt.Errorf("%s is a directory", path)
		return nil
	}
	format := importer.DetectFormat(path)
	if format == importer.FormatUnknown {
		w.previewErr = fmt.Errorf("unsupported file type (legacy .xls is not supported, save as .xlsx)")
		return nil
	}
	w.path, w.format = path, format
	w.opts = importer.SourceOptions{HeaderRow: 1, StartRow: 2, SkipEmpty: true}
	w.sheets = nil
	if format == importer.FormatXLSX {
		sheets, err := importer.ListSheets(path)
		if err != nil {
			w.previewErr = err
			return nil
		}
		if len(sheets) == 0 {
			w.previewErr = fmt.Errorf("workbook has no sheets")
			return nil
		}
		w.sheets = sheets
		w.opts.Sheet = sheets[0]
	} else {
		delim, enc, err := importer.SniffCSV(path)
		if err != nil {
			w.previewErr = err
			return nil
		}
		w.opts.Delimiter, w.opts.Encoding = delim, enc
		w.sniffDelim, w.sniffEnc = delim, enc
	}
	w.previewErr = nil
	w.previewKey = ""
	w.srcField = 0
	w.analysis = nil
	w.setTableName(w.defaultTableName())
	w.step = stepSource
	w.loadPreview()
	return nil
}

func (w *wizard) defaultTableName() string {
	if len(w.files) > 1 {
		if n := importer.CommonTableName(w.files); n != "" {
			return n
		}
	}
	return importer.TableNameFor(w.path, w.sheetForName())
}

func (w *wizard) setTableName(s string) {
	w.tableInput.SetValue(s)
	w.tableInput.CursorEnd()
}

func (w *wizard) sheetForName() string {
	if w.format == importer.FormatXLSX && len(w.sheets) > 1 {
		return w.opts.Sheet
	}
	return ""
}

// loadPreview re-reads the raw rows when file-level options changed.
func (w *wizard) loadPreview() {
	key := fmt.Sprintf("%s|%c|%s", w.opts.Sheet, w.opts.Delimiter, w.opts.Encoding)
	if key == w.previewKey {
		return
	}
	w.previewKey = key
	w.preview, w.previewErr = importer.ReadRaw(w.path, w.opts, previewRows)
}

func (w *wizard) updateSource(k tea.KeyMsg) tea.Cmd {
	fields := w.srcFields()
	field := fields[w.srcField]
	key := k.String()
	switch key {
	case "esc":
		w.step = stepFile
		return nil
	case "enter":
		return w.analyze()
	case "up", "shift+tab":
		w.srcField = (w.srcField - 1 + len(fields)) % len(fields)
		return nil
	case "down", "tab":
		w.srcField = (w.srcField + 1) % len(fields)
		return nil
	}
	delta := 0
	switch key {
	case "left", "-":
		delta = -1
	case "right", "+", " ":
		delta = 1
	}
	switch field {
	case fieldSheet:
		if delta != 0 {
			i := indexOf(w.sheets, w.opts.Sheet)
			w.opts.Sheet = w.sheets[(i+delta+len(w.sheets))%len(w.sheets)]
			w.setTableName(w.defaultTableName())
		}
	case fieldDelimiter:
		cands := importer.DelimiterCandidates
		if delta != 0 {
			i := -1
			for j, c := range cands {
				if c == w.opts.Delimiter {
					i = j
				}
			}
			if i < 0 && delta < 0 {
				i = 0
			}
			w.opts.Delimiter = cands[(i+delta+len(cands))%len(cands)]
		} else if len(k.Runes) == 1 && k.Type == tea.KeyRunes {
			// Any other printable character becomes a custom delimiter.
			w.opts.Delimiter = k.Runes[0]
		}
	case fieldEncoding:
		if delta != 0 {
			i := indexOf(importer.Encodings, w.opts.Encoding)
			w.opts.Encoding = importer.Encodings[(i+delta+len(importer.Encodings))%len(importer.Encodings)]
		}
	case fieldHeaderRow, fieldStartRow:
		p := &w.opts.HeaderRow
		if field == fieldStartRow {
			p = &w.opts.StartRow
		}
		switch {
		case delta != 0:
			*p += delta
		case key == "backspace":
			*p /= 10
		case len(k.Runes) == 1 && k.Runes[0] >= '0' && k.Runes[0] <= '9':
			*p = min(*p*10+int(k.Runes[0]-'0'), 9_999_999)
		}
		if field == fieldHeaderRow {
			if w.opts.HeaderRow < 0 {
				w.opts.HeaderRow = 0
			}
			// Keep the data directly below the header unless the user moved it.
			if delta != 0 || w.opts.StartRow <= w.opts.HeaderRow {
				w.opts.StartRow = w.opts.HeaderRow + 1
			}
		} else if w.opts.StartRow < 1 {
			w.opts.StartRow = 1
		}
	case fieldSkipEmpty:
		if delta != 0 || key == "x" {
			w.opts.SkipEmpty = !w.opts.SkipEmpty
		}
	case fieldDecimal:
		if delta != 0 {
			i := 0
			for j, d := range decimalModes {
				if d == w.opts.Decimal {
					i = j
				}
			}
			w.opts.Decimal = decimalModes[(i+delta+len(decimalModes))%len(decimalModes)]
		}
	}
	w.loadPreview()
	return nil
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return 0
}

func (w *wizard) analyze() tea.Cmd {
	if w.opts.StartRow <= w.opts.HeaderRow {
		w.previewErr = fmt.Errorf("start row must be after the header row")
		return nil
	}
	a, err := importer.Analyze(w.path, w.opts, importer.DefaultSampleSize)
	if err != nil {
		w.previewErr = err
		return nil
	}
	if len(a.Columns) == 0 {
		w.previewErr = fmt.Errorf("no columns found with these settings")
		return nil
	}
	w.previewErr = nil
	w.analysis = a
	w.cols = append([]importer.Column(nil), a.Columns...)
	w.stats = make([]colStats, len(w.cols))
	for i := range w.cols {
		w.computeStats(i)
	}
	w.colCursor, w.colOffset = 0, 0
	w.editing = false
	w.step = stepColumns
	return nil
}

// computeStats converts the sample values with the current type.
func (w *wizard) computeStats(i int) {
	c := &w.cols[i]
	st := colStats{}
	for _, row := range w.analysis.Sample {
		raw := ""
		if c.Index < len(row) {
			raw = row[c.Index]
		}
		v, err := c.Convert(raw)
		if err != nil {
			st.errors++
		}
		if len(st.samples) < 4 && strings.TrimSpace(raw) != "" {
			st.samples = append(st.samples, fmt.Sprint(v))
			st.bad = append(st.bad, err != nil)
		}
	}
	w.stats[i] = st
}

func (w *wizard) colPageSize() int { return max(3, w.height-14) }

func (w *wizard) updateColumns(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if w.editing {
		switch key {
		case "enter":
			name := strings.TrimSpace(w.nameInput.Value())
			if name != "" {
				w.cols[w.colCursor].Name = name
			}
			w.editing = false
			w.nameInput.Blur()
			return nil
		case "esc":
			w.editing = false
			w.nameInput.Blur()
			return nil
		}
		var cmd tea.Cmd
		w.nameInput, cmd = w.nameInput.Update(k)
		return cmd
	}
	n := len(w.cols)
	switch key {
	case "esc":
		w.step = stepSource
	case "enter":
		if err := (importer.Target{Table: "x", Columns: w.cols}).Validate(); err != nil {
			w.previewErr = err
			return nil
		}
		w.previewErr = nil
		w.step = stepTarget
		w.targetField = 0
		w.tableInput.Focus()
		w.checkTableExists()
		return textinput.Blink
	case "up", "k":
		w.colCursor--
	case "down", "j":
		w.colCursor++
	case "pgup":
		w.colCursor -= w.colPageSize()
	case "pgdown":
		w.colCursor += w.colPageSize()
	case "home":
		w.colCursor = 0
	case "end":
		w.colCursor = n - 1
	case " ", "x":
		w.cols[w.colCursor].Include = !w.cols[w.colCursor].Include
	case "a":
		for i := range w.cols {
			w.cols[i].Include = true
		}
	case "n":
		for i := range w.cols {
			w.cols[i].Include = false
		}
	case "i":
		for i := range w.cols {
			w.cols[i].Include = !w.cols[i].Include
		}
	case "right", "t":
		w.cols[w.colCursor].Type = w.cols[w.colCursor].Type.Next(1)
		w.computeStats(w.colCursor)
	case "left", "T":
		w.cols[w.colCursor].Type = w.cols[w.colCursor].Type.Next(-1)
		w.computeStats(w.colCursor)
	case "e", "r", "f2":
		w.editing = true
		w.nameInput.SetValue(w.cols[w.colCursor].Name)
		w.nameInput.CursorEnd()
		w.nameInput.Width = 24
		return w.nameInput.Focus()
	}
	w.colCursor = max(0, min(n-1, w.colCursor))
	page := w.colPageSize()
	if w.colCursor < w.colOffset {
		w.colOffset = w.colCursor
	}
	if w.colCursor >= w.colOffset+page {
		w.colOffset = w.colCursor - page + 1
	}
	return nil
}

func (w *wizard) checkTableExists() {
	name := strings.TrimSpace(w.tableInput.Value())
	w.tableExists = false
	if name != "" {
		w.tableExists, _ = importer.TableExists(context.Background(), w.db.DB, name)
	}
}

func (w *wizard) target() importer.Target {
	t := importer.Target{
		Table:    strings.TrimSpace(w.tableInput.Value()),
		IfExists: importer.IfExistsModes[w.ifExists],
		Columns:  w.cols,
	}
	if w.srcColOn {
		t.SourceColumn = strings.TrimSpace(w.srcColInput.Value())
	}
	return t
}

func (w *wizard) updateTarget(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	switch key {
	case "esc":
		w.step = stepColumns
		w.tableInput.Blur()
		return nil
	case "enter":
		t := w.target()
		if err := t.Validate(); err != nil {
			w.previewErr = err
			return nil
		}
		if w.tableExists && t.IfExists == importer.IfExistsFail {
			w.previewErr = fmt.Errorf("table %q exists: choose replace or append (or another name)", t.Table)
			return nil
		}
		w.previewErr = nil
		return w.startImport(t)
	case "up", "down", "tab", "shift+tab":
		if key == "up" || key == "shift+tab" {
			w.targetField = (w.targetField + 2) % 3
		} else {
			w.targetField = (w.targetField + 1) % 3
		}
		w.tableInput.Blur()
		w.srcColInput.Blur()
		switch {
		case w.targetField == 0:
			return w.tableInput.Focus()
		case w.targetField == 2 && w.srcColOn:
			return w.srcColInput.Focus()
		}
		return nil
	}
	switch w.targetField {
	case 1:
		switch key {
		case "left":
			w.ifExists = (w.ifExists + len(importer.IfExistsModes) - 1) % len(importer.IfExistsModes)
		case "right", " ":
			w.ifExists = (w.ifExists + 1) % len(importer.IfExistsModes)
		}
		return nil
	case 2:
		switch key {
		case "left", "right", " ":
			w.srcColOn = !w.srcColOn
			if w.srcColOn {
				return w.srcColInput.Focus()
			}
			w.srcColInput.Blur()
			return nil
		}
		if w.srcColOn {
			var cmd tea.Cmd
			w.srcColInput, cmd = w.srcColInput.Update(k)
			return cmd
		}
		return nil
	}
	var cmd tea.Cmd
	w.tableInput, cmd = w.tableInput.Update(k)
	w.checkTableExists()
	return cmd
}

func (w *wizard) startImport(t importer.Target) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.progress = importer.Progress{}
	w.step = stepRunning
	ch := make(chan tea.Msg, 4)
	w.ch = ch
	files, opts, d := append([]string(nil), w.files...), w.opts, w.db
	// Re-detect delimiter/encoding per file unless the user changed them.
	opts.AutoDetect = w.format == importer.FormatCSV && opts.Delimiter == w.sniffDelim && opts.Encoding == w.sniffEnc
	go func() {
		defer close(ch)
		res, err := importer.ImportFiles(ctx, d.DB, files, opts, t, func(p importer.Progress) {
			select {
			case ch <- importProgressMsg{p: p}:
			default: // drop progress updates if the UI is busy
			}
		})
		ch <- importDoneMsg{res: res, err: err}
	}()
	return waitForImport(ch)
}

func waitForImport(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// ---------------------------------------------------------------- views

func (w *wizard) View() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Import wizard") + "   " + w.stepBar() + "\n")
	if w.path != "" && w.step > stepFile {
		info := fmt.Sprintf("%s (%s)", w.path, w.format)
		if n := len(w.files); n > 1 {
			info = fmt.Sprintf("%s (%s) + %d more files — columns of the first file, others matched by header", w.path, w.format, n-1)
		}
		b.WriteString(styleMuted.Render(runewidth.Truncate(info, w.width-4, "…")) + "\n")
	}
	b.WriteString("\n")
	switch w.step {
	case stepFile:
		b.WriteString("File to import (.csv, .tsv, .txt, .xlsx, .xlsm) — or a pattern like sales_*.csv, or a folder:\n\n")
		b.WriteString(w.file.View())
		if n := len(w.files); n > 0 {
			b.WriteString("\n\n" + styleTitle.Render(fmt.Sprintf("%d file(s) selected", n)) + "\n")
			shown := w.files
			if len(shown) > 8 {
				shown = shown[len(shown)-8:]
				b.WriteString(styleMuted.Render(fmt.Sprintf("  … %d more above", n-8)) + "\n")
			}
			for _, f := range shown {
				b.WriteString("  " + runewidth.Truncate(f, max(20, w.width-10), "…") + "\n")
			}
			b.WriteString("\n" + keyHelp("Enter", "continue (empty line) / add", "Ctrl+L", "add file", "Ctrl+X", "remove last", "Tab", "complete", "Esc", "cancel"))
		} else {
			b.WriteString("\n\n" + keyHelp("Enter", "continue", "Ctrl+L", "add more files", "Tab", "complete", "Esc", "cancel"))
		}
	case stepSource:
		b.WriteString(w.viewSource())
	case stepColumns:
		b.WriteString(w.viewColumns())
	case stepTarget:
		b.WriteString(w.viewTarget())
	case stepRunning:
		msg := fmt.Sprintf("Importing… %d rows", w.progress.Rows)
		if w.progress.Files > 1 {
			msg = fmt.Sprintf("Importing… file %d/%d, %d rows", w.progress.File, w.progress.Files, w.progress.Rows)
		}
		b.WriteString(styleWarn.Render(msg))
		b.WriteString("\n\n" + keyHelp("Esc", "cancel"))
	case stepDone:
		b.WriteString(w.viewDone())
	}
	if w.previewErr != nil && w.step != stepDone {
		b.WriteString("\n\n" + styleError.Render("⚠ "+oneLine(w.previewErr.Error())))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

func (w *wizard) stepBar() string {
	var parts []string
	cur := int(w.step)
	if cur > len(stepNames)-1 {
		cur = len(stepNames) - 1
	}
	for i, n := range stepNames {
		label := fmt.Sprintf("%d %s", i+1, n)
		switch {
		case i == cur:
			parts = append(parts, styleTabActive.Render(label))
		case i < cur:
			parts = append(parts, styleOK.Render(label))
		default:
			parts = append(parts, styleMuted.Render(label))
		}
	}
	return strings.Join(parts, styleMuted.Render(" › "))
}

func (w *wizard) fieldLine(field int, label, value, hint string) string {
	fields := w.srcFields()
	focused := fields[w.srcField] == field
	cursor := "  "
	if focused {
		cursor = styleKey.Render("▶ ")
		value = styleSelected.Render(" ‹ " + value + " › ")
	} else {
		value = "   " + value + "   "
	}
	return cursor + fmt.Sprintf("%-16s", label) + value + "  " + styleMuted.Render(hint)
}

func (w *wizard) viewSource() string {
	var b strings.Builder
	for _, f := range w.srcFields() {
		var line string
		switch f {
		case fieldSheet:
			line = w.fieldLine(f, "Sheet", w.opts.Sheet, fmt.Sprintf("%d of %d sheets", indexOf(w.sheets, w.opts.Sheet)+1, len(w.sheets)))
		case fieldDelimiter:
			line = w.fieldLine(f, "Delimiter", importer.DelimiterName(w.opts.Delimiter), "←/→ cycle, or type any character")
		case fieldEncoding:
			line = w.fieldLine(f, "Encoding", w.opts.Encoding, "")
		case fieldHeaderRow:
			v := itoa(w.opts.HeaderRow)
			hint := "row with column names, 0 = no header"
			if w.opts.HeaderRow == 0 {
				v = "none"
			}
			line = w.fieldLine(f, "Header row", v, hint)
		case fieldStartRow:
			line = w.fieldLine(f, "First data row", itoa(w.opts.StartRow), "type digits or ←/→")
		case fieldSkipEmpty:
			v := "no"
			if w.opts.SkipEmpty {
				v = "yes"
			}
			line = w.fieldLine(f, "Skip empty rows", v, "")
		case fieldDecimal:
			v := "auto"
			if w.opts.Decimal != 0 {
				v = string(w.opts.Decimal)
			}
			line = w.fieldLine(f, "Decimal sep.", v, "for number detection (1.234,5 vs 1,234.5)")
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + styleTitle.Render("Preview") + "  " +
		styleHeader.Render("■ header") + "  " + "■ data" + "  " + styleMuted.Render("■ skipped") + "\n")
	avail := w.height - len(w.srcFields()) - 13
	b.WriteString(w.viewRaw(max(3, avail)))
	b.WriteString("\n\n" + keyHelp("↑/↓", "field", "←/→", "change", "Enter", "analyze columns", "Esc", "back"))
	return b.String()
}

// viewRaw renders raw preview rows with row numbers.
func (w *wizard) viewRaw(maxRows int) string {
	if len(w.preview) == 0 {
		return styleMuted.Render("(no rows)")
	}
	ncols := 0
	for _, r := range w.preview {
		ncols = max(ncols, len(r))
	}
	widths := make([]int, ncols)
	for _, r := range w.preview[:min(len(w.preview), 50)] {
		for i, c := range r {
			widths[i] = max(widths[i], min(18, runewidth.StringWidth(cleanCell(c))))
		}
	}
	// Show rows starting a bit before the header/start row.
	first := max(0, min(w.opts.HeaderRow, w.opts.StartRow)-3)
	if w.opts.HeaderRow == 0 {
		first = max(0, w.opts.StartRow-3)
	}
	end := min(len(w.preview), first+maxRows)
	avail := w.width - 12
	var b strings.Builder
	for r := first; r < end; r++ {
		rowNum := r + 1
		row := w.preview[r]
		var cells []string
		used := 0
		for i := 0; i < ncols; i++ {
			c := ""
			if i < len(row) {
				c = cleanCell(row[i])
			}
			cw := max(1, widths[i])
			if used+cw+3 > avail {
				cells = append(cells, "…")
				break
			}
			used += cw + 3
			cells = append(cells, fit(c, cw, false))
		}
		line := strings.Join(cells, " │ ")
		num := styleMuted.Render(fmt.Sprintf("%5d ", rowNum))
		switch {
		case rowNum == w.opts.HeaderRow:
			line = styleHeader.Render(line)
		case rowNum < w.opts.StartRow || (w.opts.SkipEmpty && isBlank(row)):
			line = styleMuted.Render(line)
		}
		b.WriteString(num + line)
		if r < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func isBlank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func (w *wizard) viewColumns() string {
	var b strings.Builder
	included := 0
	for _, c := range w.cols {
		if c.Include {
			included++
		}
	}
	b.WriteString(fmt.Sprintf("%d of %d columns selected · types inferred from %d sample rows",
		included, len(w.cols), len(w.analysis.Sample)))
	if n := len(w.files); n > 1 {
		b.WriteString(styleMuted.Render(fmt.Sprintf(" · applies to all %d files (matched by header)", n)))
	}
	b.WriteString("\n\n")
	const srcW, nameW, typeW = 22, 26, 9
	hdr := fmt.Sprintf("    %-4s %s %s %s %s", "", fit("Source column", srcW, false), fit("Column name", nameW, false),
		fit("Type", typeW, false), "Sample values (converted)")
	b.WriteString(styleHeader.Render(hdr) + "\n")
	page := w.colPageSize()
	end := min(len(w.cols), w.colOffset+page)
	sampleW := max(10, w.width-4-5-srcW-nameW-typeW-10)
	for i := w.colOffset; i < end; i++ {
		c := w.cols[i]
		st := w.stats[i]
		check := "[x]"
		if !c.Include {
			check = "[ ]"
		}
		cursor := "  "
		if i == w.colCursor {
			cursor = styleKey.Render("▶ ")
		}
		name := fit(c.Name, nameW, false)
		if w.editing && i == w.colCursor {
			name = lipgloss.NewStyle().Width(nameW).MaxWidth(nameW).Render(w.nameInput.View())
		}
		typ := fit(string(c.Type), typeW, false)
		var samples []string
		for k, s := range st.samples {
			s = cleanCell(s)
			if st.bad[k] {
				samples = append(samples, styleError.Render(runewidth.Truncate(s, 16, "…")))
			} else {
				samples = append(samples, runewidth.Truncate(s, 16, "…"))
			}
		}
		sampleStr := strings.Join(samples, styleMuted.Render(", "))
		if lipgloss.Width(sampleStr) > sampleW {
			sampleStr = truncateANSI(sampleStr, sampleW)
		}
		if st.errors > 0 {
			sampleStr = styleError.Render(fmt.Sprintf("%d invalid ", st.errors)) + sampleStr
		}
		src := fit(cleanCell(c.Source), srcW, false)
		var line string
		if c.Include {
			line = fmt.Sprintf("%s %s %s %s %s %s", cursor, check, src, styleBold.Render(name), styleKey.Render(typ), sampleStr)
		} else {
			line = fmt.Sprintf("%s %s %s %s %s %s", cursor, styleMuted.Render(check), styleStrike.Render(src),
				styleStrike.Render(name), styleMuted.Render(typ), styleMuted.Render(ansiStrip(sampleStr)))
		}
		b.WriteString(line + "\n")
	}
	if len(w.cols) > page {
		b.WriteString(styleMuted.Render(fmt.Sprintf("  … %d-%d of %d", w.colOffset+1, end, len(w.cols))) + "\n")
	}
	b.WriteString("\n")
	if w.editing {
		b.WriteString(keyHelp("Enter", "save name", "Esc", "cancel"))
	} else {
		b.WriteString(keyHelp("Space", "include/skip", "a/n/i", "all/none/invert", "←/→", "type", "e", "rename", "Enter", "continue", "Esc", "back"))
	}
	return b.String()
}

func (w *wizard) viewTarget() string {
	var b strings.Builder
	t := w.target()
	cursor := func(i int) string {
		if w.targetField == i {
			return styleKey.Render("▶ ")
		}
		return "  "
	}
	b.WriteString(cursor(0) + fmt.Sprintf("%-16s", "Table name") + w.tableInput.View() + "\n")
	mode := t.IfExists
	if w.targetField == 1 {
		mode = styleSelected.Render(" ‹ " + mode + " › ")
	} else {
		mode = "   " + mode
	}
	exists := styleMuted.Render("table does not exist yet")
	if w.tableExists {
		exists = styleWarn.Render("table already exists")
	}
	b.WriteString(cursor(1) + fmt.Sprintf("%-16s", "If table exists") + mode + "  " + exists + "\n")
	src := "off"
	if w.srcColOn {
		src = "on, column "
	}
	switch {
	case w.targetField == 2 && w.srcColOn:
		src = styleSelected.Render(" ‹ on › ") + " column " + w.srcColInput.View()
	case w.targetField == 2:
		src = styleSelected.Render(" ‹ off › ")
	case w.srcColOn:
		src = "   on, column " + w.srcColInput.Value()
	default:
		src = "   " + src
	}
	b.WriteString(cursor(2) + fmt.Sprintf("%-16s", "Source file col.") + src + "  " + styleMuted.Render("file name per row") + "\n\n")

	b.WriteString(styleTitle.Render("Summary") + "\n")
	if n := len(w.files); n > 1 {
		names := make([]string, 0, 4)
		for _, f := range w.files[:min(n, 4)] {
			names = append(names, filepath.Base(f))
		}
		more := ""
		if n > 4 {
			more = fmt.Sprintf(", … +%d", n-4)
		}
		b.WriteString(fmt.Sprintf("Files:   %d (%s%s)\nFirst:   %s", n, strings.Join(names, ", "), more, w.path))
	} else {
		b.WriteString(fmt.Sprintf("Source:  %s", w.path))
	}
	if w.format == importer.FormatXLSX {
		b.WriteString(fmt.Sprintf(" [sheet %s]", w.opts.Sheet))
	} else {
		b.WriteString(fmt.Sprintf(" [delimiter %s, %s]", importer.DelimiterName(w.opts.Delimiter), w.opts.Encoding))
	}
	b.WriteString(fmt.Sprintf("\nRows:    header row %d, data from row %d", w.opts.HeaderRow, w.opts.StartRow))
	skipped := 0
	for _, c := range w.cols {
		if !c.Include {
			skipped++
		}
	}
	b.WriteString(fmt.Sprintf("\nColumns: %d imported, %d skipped\n\n", len(w.cols)-skipped, skipped))
	if t.Table != "" && !(w.tableExists && t.IfExists == importer.IfExistsAppend) {
		sql := t.CreateTableSQL()
		lines := strings.Split(sql, "\n")
		maxLines := max(4, w.height-22)
		if len(lines) > maxLines {
			lines = append(lines[:maxLines-1], fmt.Sprintf("  … (%d more)", len(lines)-maxLines+1))
		}
		b.WriteString(styleMuted.Render(strings.Join(lines, "\n")) + "\n")
	}
	b.WriteString("\n" + keyHelp("↑/↓", "field", "←/→", "change", "Enter", "import", "Esc", "back"))
	return b.String()
}

func (w *wizard) viewDone() string {
	var b strings.Builder
	if w.err != nil {
		b.WriteString(styleError.Render("Import failed: "+oneLine(w.err.Error())) + "\n\n")
		b.WriteString(keyHelp("Enter", "back to target settings"))
		return b.String()
	}
	r := w.result
	b.WriteString(styleOK.Render(fmt.Sprintf("✔ Imported %d rows into %q", r.Rows, r.Table)) + "\n")
	if len(r.Files) > 1 {
		b.WriteString("\n")
		for _, f := range r.Files {
			line := fmt.Sprintf("  %-32s %7d rows", runewidth.Truncate(filepath.Base(f.Path), 32, "…"), f.Rows)
			b.WriteString(line + "\n")
			if len(f.Missing) > 0 {
				b.WriteString(styleWarn.Render("      missing (NULL): "+strings.Join(f.Missing, ", ")) + "\n")
			}
			if len(f.Extra) > 0 {
				b.WriteString(styleMuted.Render("      ignored: "+strings.Join(f.Extra, ", ")) + "\n")
			}
		}
	}
	if r.Warnings > 0 {
		b.WriteString("\n" + styleWarn.Render(fmt.Sprintf("%d values could not be converted and were stored as text:", r.Warnings)) + "\n")
		for _, s := range r.Samples {
			b.WriteString(styleMuted.Render("  • "+s) + "\n")
		}
	}
	b.WriteString("\n" + keyHelp("Enter", "close and query the table"))
	return b.String()
}
