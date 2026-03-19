package ui

import (
	"image/color"

	"MattiasHognas/Kennel/internal/ui/table"
	lipgloss "charm.land/lipgloss/v2"
	lipgloss1 "github.com/charmbracelet/lipgloss"
	tint "github.com/lrstanley/bubbletint"
)

var Theme = tint.TintBirdsOfParadise

// c converts a bubbletint (lipgloss v1) TerminalColor to a charm.land/lipgloss/v2 TerminalColor.
func c(t lipgloss1.TerminalColor) color.Color {
	if colorStr, ok := t.(lipgloss1.Color); ok {
		return lipgloss.Color(string(colorStr))
	}
	// Fallback if not string (unlikely for bubbletint default tints)
	return lipgloss.Color("#ffffff")
}

var (
	FocusedColor = c(Theme.BrightPurple())
	HeaderFg     = c(Theme.BrightPurple())
	SelectedBg   = c(Theme.BrightPurple())
	ErrorColor   = c(Theme.Red())

	Fg = c(Theme.Fg())
	Bg = c(Theme.Bg())
)

var (
	HeaderStyle          = lipgloss.NewStyle().Bold(true).Foreground(HeaderFg).Padding(0, 1).Border(lipgloss.NormalBorder()).BorderBottom(true).BorderLeft(false).BorderRight(false).BorderTop(false)
	CellStyle            = lipgloss.NewStyle().Padding(0, 1)
	SelectedFocusedStyle = lipgloss.NewStyle().Bold(true).Background(SelectedBg)
	SelectedBlurredStyle = lipgloss.NewStyle()

	EditorBorderStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(FocusedColor)
	ButtonActiveStyle   = lipgloss.NewStyle().Bold(true).Reverse(true)
	ButtonInactiveStyle = lipgloss.NewStyle().Bold(true)
	ErrorStyle          = lipgloss.NewStyle().Foreground(ErrorColor)

	SpacedTableStyle = lipgloss.NewStyle().MarginRight(2)
)

func NewTableStyles() (table.Styles, table.Styles) {
	return table.Styles{
			Header:   HeaderStyle,
			Cell:     CellStyle,
			Selected: SelectedFocusedStyle,
		}, table.Styles{
			Header:   HeaderStyle,
			Cell:     CellStyle,
			Selected: SelectedBlurredStyle,
		}
}
