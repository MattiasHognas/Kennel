package table

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestSelectedStyleAppliesToEveryCell(t *testing.T) {
	cellStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ebdbb2")).Padding(0, 1)
	selectedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#1d2021")).
		Background(lipgloss.Color("#00ff00"))

	tableModel := New(
		WithColumns([]Column{
			{Title: "State", Width: 8},
			{Title: "Name", Width: 12},
		}),
		WithRows([]Row{{"Running", "Planner"}}),
		WithStyles(Styles{
			Header:   lipgloss.NewStyle(),
			Cell:     cellStyle,
			Selected: selectedStyle,
		}),
		WithHeight(1),
	)

	tableModel.Focus()
	view := tableModel.View()
	lines := strings.Split(view, "\n")
	if len(lines) < 2 {
		t.Fatalf("view has %d lines, want at least 2", len(lines))
	}

	selectedRow := lines[1]

	// Compose the style that should be applied to each selected cell:
	// start from the base cell style (for padding, etc.) and override
	// foreground/background to match the selected style.
	selectedCellStyle := cellStyle.Copy().
		Foreground(lipgloss.Color("#1d2021")).
		Background(lipgloss.Color("#00ff00"))

	runningCell := selectedCellStyle.Render("Running")
	plannerCell := selectedCellStyle.Render("Planner")

	if !strings.Contains(selectedRow, runningCell) || !strings.Contains(selectedRow, plannerCell) {
		t.Fatalf("selected row %q does not contain expected styled cells %q and %q", selectedRow, runningCell, plannerCell)
	}
}
