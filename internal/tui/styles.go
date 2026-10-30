package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#5A3FD6", Dark: "#9D8CFF"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	colorError  = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#FF6B6B"}
	colorOK     = lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#7BD88F"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#FFC66D"}
	colorSelBg  = lipgloss.AdaptiveColor{Light: "#E3DDFF", Dark: "#3B3361"}
	colorBarBg  = lipgloss.AdaptiveColor{Light: "#ECECEC", Dark: "#262626"}

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleMuted    = lipgloss.NewStyle().Foreground(colorMuted)
	styleError    = lipgloss.NewStyle().Foreground(colorError)
	styleOK       = lipgloss.NewStyle().Foreground(colorOK)
	styleWarn     = lipgloss.NewStyle().Foreground(colorWarn)
	styleBold     = lipgloss.NewStyle().Bold(true)
	styleSelected = lipgloss.NewStyle().Background(colorSelBg)
	styleKey      = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleNull     = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleStrike   = lipgloss.NewStyle().Foreground(colorMuted).Strikethrough(true)

	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colorAccent).Padding(0, 1)
	styleTabInactive = lipgloss.NewStyle().Foreground(colorMuted).Background(colorBarBg).Padding(0, 1)
	styleStatusBar   = lipgloss.NewStyle().Background(colorBarBg)

	stylePanel        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorMuted)
	stylePanelFocused = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent)
	styleDialog       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(1, 2)
)

// keyHelp renders "key desc · key desc".
func keyHelp(pairs ...string) string {
	out := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			out += styleMuted.Render(" · ")
		}
		out += styleKey.Render(pairs[i]) + " " + styleMuted.Render(pairs[i+1])
	}
	return out
}
