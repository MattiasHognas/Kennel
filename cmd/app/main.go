package main

import (
	"database/sql"
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

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	shutdownModel := m
	if persistedModel, ok := finalModel.(logic.Model); ok {
		shutdownModel = persistedModel
	}
	shutdownModel.Shutdown()
	cleanup()

	if err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func initialModel() (logic.Model, func()) {
	focusedStyles, blurredStyles := newTableStyles()
	repository, err := repository.NewSQLiteRepository("data/kennel.db")
	if err != nil {
		fatalErr := fmt.Sprintf("Failed to initialize repository: %v", err)
		fmt.Println(fatalErr)
		os.Exit(1)
	}
	sampleProjects := loadProjects(repository)
	m := logic.NewModel(focusedStyles, blurredStyles, sampleProjects, repository)

	m.ResizeTables(logic.DefaultProjectWidth+logic.DefaultAgentWidth+logic.DefaultActivityWidth, logic.DefaultTableHeight+logic.FooterHeight+4)
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
	projectDefinitions := []struct {
		name       string
		agents     []string
		activities []model.ActivityEntry
	}{
		{
			name:   "Barky barky",
			agents: []string{"Planner", "UX", "Frontend Developer", "QA", "DevOps"},
			activities: []model.ActivityEntry{
				{Timestamp: "08:00:00", Text: "Project created"},
				{Timestamp: "08:05:00", Text: "Planner: defined initial scope"},
				{Timestamp: "08:10:00", Text: "UX: sketched primary workflow"},
			},
		},
		{
			name:   "Sniff sniff",
			agents: []string{"Planner", "Backend Developer", "QA"},
			activities: []model.ActivityEntry{
				{Timestamp: "09:00:00", Text: "Project created"},
				{Timestamp: "09:15:00", Text: "Planner: prepared backlog"},
				{Timestamp: "09:30:00", Text: "Backend Developer: designed API contract"},
			},
		},
		{
			name:   "Grr Grr",
			agents: []string{"Planning", "Backend Developer 1", "Backend Developer 2", "QA", "DevOps"},
			activities: []model.ActivityEntry{
				{Timestamp: "10:00:00", Text: "Project created"},
				{Timestamp: "10:10:00", Text: "Planning: split delivery phases"},
				{Timestamp: "10:30:00", Text: "DevOps: prepared deployment pipeline"},
			},
		},
	}

	projects := make([]model.Project, 0, len(projectDefinitions))
	for _, definition := range projectDefinitions {
		agents := make([]agent.AgentContract, 0, len(definition.agents))
		for _, name := range definition.agents {
			agents = append(agents, agent.NewAgent(name))
		}

		projects = append(projects, model.Project{
			Name:       definition.name,
			State:      agent.Stopped,
			Agents:     agents,
			Activities: append([]model.ActivityEntry(nil), definition.activities...),
		})
	}

	return projects
}

func loadProjects(repository *repository.SQLiteRepository) []model.Project {

	storedProjects, err := repository.ReadProjects()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read projects, falling back to samples: %v\n", err)
		_ = repository.Close()
		return sampleProjects()
	}

	if len(storedProjects) == 0 {
		if err := seedSampleProjects(repository); err != nil {
			fmt.Fprintf(os.Stderr, "failed to seed sample projects, falling back to samples: %v\n", err)
			_ = repository.Close()
			return sampleProjects()
		}

		storedProjects, err = repository.ReadProjects()
		if err != nil || len(storedProjects) == 0 {
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to read seeded projects, falling back to samples: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "failed to read seeded projects (empty), falling back to samples\n")
			}
			_ = repository.Close()
			return sampleProjects()
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

		activities := make([]model.ActivityEntry, 0, len(storedProject.Activities))
		for _, activity := range storedProject.Activities {
			activities = append(activities, model.ActivityEntry{
				Timestamp: activity.CreatedAt.Format("15:04:05"),
				Text:      activity.Text,
			})
		}

		projects = append(projects, model.Project{
			ProjectID:  storedProject.ID,
			Name:       storedProject.Name,
			Agents:     agents,
			AgentIDs:   agentIDs,
			Activities: activities,
		})
	}

	return projects
}

func seedSampleProjects(repository *repository.SQLiteRepository) error {
	for _, definition := range sampleProjects() {
		project, err := repository.CreateProject(definition.Name)
		if err != nil {
			return err
		}

		for _, agentInstance := range definition.Agents {
			if _, err := repository.AddAgentToProject(project.ID, agentInstance.Name()); err != nil {
				return err
			}
		}

		for _, activity := range definition.Activities {
			if _, err := repository.NewActivity(project.ID, sql.NullInt64{}, activity.Text); err != nil {
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
