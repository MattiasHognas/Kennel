package table

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestSelectedStyleAppliesToEveryCell(t *testing.T) {
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("#00ff00"))

	tableModel := New(
		WithColumns([]Column{
			{Title: "State", Width: 8},
			{Title: "Name", Width: 12},
		}),
		WithRows([]Row{{"Running", "Planner"}}),
		WithStyles(Styles{
			Header:   lipgloss.NewStyle(),
			Cell:     lipgloss.NewStyle().Foreground(lipgloss.Color("#ebdbb2")).Padding(0, 1),
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
	if !strings.Contains(selectedRow, "Running") || !strings.Contains(selectedRow, "Planner") {
		t.Fatalf("selected row %q does not contain both cell values", selectedRow)
	}

	if count := strings.Count(selectedRow, "48;2;0;255;0m"); count != 2 {
		t.Fatalf("selected background count = %d, want 2 in row %q", count, selectedRow)
	}
}
