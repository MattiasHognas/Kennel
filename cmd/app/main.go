package main

import (
	"fmt"
	"os"

	repository "MattiasHognas/Kennel/internal/data"
	logic "MattiasHognas/Kennel/internal/logic"
	model "MattiasHognas/Kennel/internal/logic"
	agent "MattiasHognas/Kennel/internal/workers"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func main() {
	m, cleanup := initialModel()
	defer cleanup()

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func initialModel() (logic.Model, func()) {
	focusedStyles, blurredStyles := newTableStyles()
	projects, repository := loadProjects()
	m := logic.NewModel(focusedStyles, blurredStyles, projects, repository)

	m.Sources = m.BuildActivitySources()
	m.ResizeTables(logic.DefaultProjectWidth+logic.DefaultAgentWidth+logic.DefaultActivityWidth, logic.DefaultTableHeight+logic.FooterHeight+4)
	m.RefreshAllTables()
	m.SetFocus(0)

	cleanup := func() {
		_ = repository.Close()
	}

	return m, cleanup
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
	projectDefinitions := sampleProjectDefinitions()

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

func sampleProjectDefinitions() []struct {
	name   string
	agents []string
} {
	return []struct {
		name   string
		agents []string
	}{
		{name: "Barky barky", agents: []string{"Planner", "UX", "Frontend Developer", "QA", "DevOps"}},
		{name: "Sniff sniff", agents: []string{"Planner", "Backend Developer", "QA"}},
		{name: "Grr Grr", agents: []string{"Planning", "Backend Developer 1", "Backend Developer 2", "QA", "DevOps"}},
	}
}

func loadProjects() ([]model.Project, *repository.SQLiteRepository) {
	repo, err := repository.NewSQLiteRepository("data/kennel.db")
	if err != nil {
		return sampleProjects(), nil
	}

	storedProjects, err := repo.ReadProjects()
	if err != nil {
		_ = repo.Close()
		return sampleProjects(), nil
	}

	if len(storedProjects) == 0 {
		if err := seedSampleProjects(repo); err != nil {
			_ = repo.Close()
			return sampleProjects(), nil
		}

		storedProjects, err = repo.ReadProjects()
		if err != nil || len(storedProjects) == 0 {
			_ = repo.Close()
			return sampleProjects(), nil
		}
	}

	projects := make([]model.Project, 0, len(storedProjects))
	for _, storedProject := range storedProjects {
		agents := make([]agent.AgentContract, 0, len(storedProject.Agents))
		agentIDs := make([]int64, 0, len(storedProject.Agents))
		for _, storedAgent := range storedProject.Agents {
			agents = append(agents, restoreAgentState(storedAgent.Name, storedAgent.State))
			agentIDs = append(agentIDs, storedAgent.ID)
		}

		activities := make([]string, 0, len(storedProject.Activities))
		for _, activity := range storedProject.Activities {
			activities = append(activities, activity.Text)
		}

		projects = append(projects, model.Project{
			ProjectID:  storedProject.ID,
			Name:       storedProject.Name,
			Agents:     agents,
			AgentIDs:   agentIDs,
			Activities: activities,
		})
	}

	return projects, repo
}

func seedSampleProjects(repo *repository.SQLiteRepository) error {
	for _, definition := range sampleProjectDefinitions() {
		project, err := repo.CreateProject(definition.name)
		if err != nil {
			return err
		}

		for _, agentName := range definition.agents {
			if _, err := repo.AddAgentToProject(project.ID, agentName); err != nil {
				return err
			}
		}
	}

	return nil
}

func restoreAgentState(name string, persistedState string) agent.AgentContract {
	a := agent.NewAgent(name)
	if persistedState == agent.Running.String() {
		a.Run()
	}
	return a
}
