package tui

import "github.com/charmbracelet/x/ansi"

func truncateANSI(s string, w int) string { return ansi.Truncate(s, w, "…") }

func ansiStrip(s string) string { return ansi.Strip(s) }
