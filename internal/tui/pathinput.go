package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// pathInput is a text input for file paths with Tab completion.
type pathInput struct {
	input   textinput.Model
	exts    []string // allowed file extensions (lower case, with dot); nil = all
	matches []string
}

func newPathInput(placeholder string, exts []string) pathInput {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = "› "
	ti.Width = 60
	ti.Focus()
	return pathInput{input: ti, exts: exts}
}

func (p *pathInput) SetValue(s string) {
	p.input.SetValue(s)
	p.input.CursorEnd()
}

func (p *pathInput) Value() string { return expandHome(strings.TrimSpace(p.input.Value())) }

func (p *pathInput) SetWidth(w int) { p.input.Width = max(10, w) }

func (p *pathInput) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "tab" {
		p.complete()
		return nil
	}
	p.matches = nil
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func (p *pathInput) allowed(name string) bool {
	if len(p.exts) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range p.exts {
		if e == ext {
			return true
		}
	}
	return false
}

func (p *pathInput) complete() {
	raw := p.input.Value()
	val := expandHome(raw)
	dir, prefix := filepath.Split(val)
	readDir := dir
	if readDir == "" {
		readDir = "."
	}
	entries, err := os.ReadDir(readDir)
	if err != nil {
		p.matches = nil
		return
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(readDir, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir {
			names = append(names, name+string(os.PathSeparator))
		} else if p.allowed(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		p.matches = nil
	case 1:
		p.SetValue(dir + names[0])
		p.matches = nil
	default:
		common := names[0]
		for _, n := range names[1:] {
			for !strings.HasPrefix(strings.ToLower(n), strings.ToLower(common)) {
				common = common[:len(common)-1]
			}
		}
		if len(common) > len(prefix) {
			p.SetValue(dir + common)
		}
		p.matches = names
	}
}

func (p *pathInput) View() string {
	v := p.input.View()
	if len(p.matches) > 0 {
		shown := p.matches
		more := ""
		if len(shown) > 12 {
			more = styleMuted.Render("  … " + itoa(len(shown)-12) + " more")
			shown = shown[:12]
		}
		v += "\n" + styleMuted.Width(p.input.Width+2).PaddingLeft(2).Render(strings.Join(shown, "  "))
		if more != "" {
			v += "\n" + more
		}
	}
	return v
}
