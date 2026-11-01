package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nextniclas/tablequery/internal/db"
)

// displayLimit caps the number of rows held in memory for display. Export
// re-runs the query and writes all rows.
const displayLimit = 10000

type queryTab struct {
	id     int
	title  string
	editor textarea.Model
	grid   grid

	result  *db.Result
	err     error
	running bool
	cancel  context.CancelFunc
	started time.Time
}

type queryDoneMsg struct {
	tabID int
	res   *db.Result
	err   error
}

func newQueryTab(id int) *queryTab {
	ta := textarea.New()
	ta.Placeholder = "Write SQL here, e.g. SELECT * FROM my_table LIMIT 100;  —  run with Ctrl+R / F5"
	ta.ShowLineNumbers = true
	ta.MaxHeight = 0
	ta.CharLimit = 0
	ta.Prompt = ""
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()
	return &queryTab{id: id, title: "Query " + strconv.Itoa(id), editor: ta}
}

// lastStatement returns the statement whose result is shown (and exported).
func (t *queryTab) lastStatement() string {
	if t.result != nil && t.result.IsQuery {
		return t.result.SQL
	}
	return ""
}

func (t *queryTab) run(d *db.DB) tea.Cmd {
	sql := t.editor.Value()
	if strings.TrimSpace(sql) == "" {
		return nil
	}
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.running = true
	t.started = time.Now()
	t.err = nil
	id := t.id
	return func() tea.Msg {
		res, err := d.RunScript(ctx, sql, displayLimit)
		return queryDoneMsg{tabID: id, res: res, err: err}
	}
}

func (t *queryTab) finish(msg queryDoneMsg) {
	t.running = false
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if msg.err != nil {
		t.err = msg.err
		return
	}
	t.result = msg.res
	if msg.res.IsQuery {
		t.grid.SetData(msg.res.Columns, msg.res.Rows)
	}
}

func (t *queryTab) stop() {
	if t.cancel != nil {
		t.cancel()
	}
}

func (t *queryTab) statusLine() string {
	switch {
	case t.running:
		return styleWarn.Render(fmt.Sprintf("Running… %s  (Esc to cancel)", time.Since(t.started).Truncate(100*time.Millisecond)))
	case t.err != nil:
		return styleError.Render("Error: " + oneLine(t.err.Error()))
	case t.result == nil:
		return styleMuted.Render("No query executed yet")
	case !t.result.IsQuery:
		return styleOK.Render(fmt.Sprintf("OK · %d rows affected · %s", t.result.Affected, fmtDuration(t.result.Duration)))
	default:
		n := len(t.result.Rows)
		s := fmt.Sprintf("%d rows · %d columns · %s", n, len(t.result.Columns), fmtDuration(t.result.Duration))
		if t.result.Truncated {
			s = fmt.Sprintf("first %d rows shown (more available, export writes all) · %d columns · %s",
				n, len(t.result.Columns), fmtDuration(t.result.Duration))
		}
		return styleOK.Render(s) + styleMuted.Render("   "+t.grid.position())
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return d.Truncate(10 * time.Millisecond).String()
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
