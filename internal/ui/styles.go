package ui

import (
	table "MattiasHognas/Kennel/internal/ui/table"

	lipgloss "charm.land/lipgloss/v2"
	tint "github.com/lrstanley/bubbletint/v2"
)

var Theme = tint.TintBirdsOfParadise

var (
	HeaderStyle          = lipgloss.NewStyle().Bold(true).Foreground(Theme.Fg).Padding(0, 1).Border(lipgloss.NormalBorder()).BorderBottom(true).BorderLeft(false).BorderRight(false).BorderTop(false)
	CellStyle            = lipgloss.NewStyle().Padding(0, 1)
	SelectedFocusedStyle = lipgloss.NewStyle().Bold(true).Background(Theme.Bg).Foreground(Theme.Fg)
	SelectedBlurredStyle = lipgloss.NewStyle().Foreground(Theme.Fg)

	EditorBorderStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(Theme.Fg).Foreground(Theme.Fg)
	ButtonActiveStyle   = lipgloss.NewStyle().Bold(true).Reverse(true).Foreground(Theme.Fg).Background(Theme.Bg)
	ButtonInactiveStyle = lipgloss.NewStyle().Bold(true).Foreground(Theme.Fg).Background(Theme.Bg)
	ErrorStyle          = lipgloss.NewStyle().Foreground(Theme.White).Background(Theme.Red)

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
