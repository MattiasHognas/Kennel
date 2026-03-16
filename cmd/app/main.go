package main

import (
	"fmt"
	"os"

	logic "MattiasHognas/Kennel/internal/logic"
	model "MattiasHognas/Kennel/internal/logic"
	agent "MattiasHognas/Kennel/internal/workers"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func initialModel() logic.Model {
	focusedStyles, blurredStyles := newTableStyles()
	projects := sampleProjects()
	m := logic.NewModel(focusedStyles, blurredStyles, projects)

	m.Sources = m.BuildActivitySources()
	m.ResizeTables(logic.DefaultProjectWidth+logic.DefaultAgentWidth+logic.DefaultActivityWidth, logic.DefaultTableHeight+logic.FooterHeight+4)
	m.RefreshAllTables()
	m.SetFocus(0)

	return m
}

func newTableStyles() (table.Styles, table.Styles) {
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("#ebdbb2")).Padding(0, 1)
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fbf1c7")).Background(lipgloss.Color("#3c3836")).Padding(0, 1)
	focusedSelected := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1d2021")).Background(lipgloss.Color("#8ec07c"))
	blurredSelected := lipgloss.NewStyle().Foreground(lipgloss.Color("#d5c4a1")).Background(lipgloss.Color("#504945"))

	return table.Styles{
			Header:   header,
			Cell:     base,
			Selected: focusedSelected,
		}, table.Styles{
			Header:   header,
			Cell:     base,
			Selected: blurredSelected,
		}
}

func sampleProjects() []model.Project {
	projectDefinitions := []struct {
		name   string
		agents []string
	}{
		{name: "Barky barky", agents: []string{"Planner", "UX", "Frontend Developer", "QA", "DevOps"}},
		{name: "Sniff sniff", agents: []string{"Planner", "Backend Developer", "QA"}},
		{name: "Grr Grr", agents: []string{"Planning", "Backend Developer 1", "Backend Developer 2", "QA", "DevOps"}},
	}

	projects := make([]model.Project, 0, len(projectDefinitions))
	for _, definition := range projectDefinitions {
		agents := make([]agent.AgentContract, 0, len(definition.agents))
		for _, name := range definition.agents {
			agents = append(agents, agent.NewAgent(name))
		}

		projects = append(projects, model.Project{
			Name:   definition.name,
			State:  agent.Stopped,
			Agents: agents,
		})
	}

	return projects
}
