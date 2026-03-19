package main

import (
	"context"
	"database/sql"
	"fmt"

	repository "MattiasHognas/Kennel/internal/data"
	model "MattiasHognas/Kennel/internal/logic"
	"MattiasHognas/Kennel/internal/ui"
	agent "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
)

func main() {
	m, cleanup := initialModel()

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	shutdownModel := m
	if persistedModel, ok := finalModel.(model.Model); ok {
		shutdownModel = persistedModel
	}
	shutdownModel.Shutdown()
	cleanup()

	if err != nil {
		panic(fmt.Sprintf("Something broke: %v", err))
	}
}

func initialModel() (model.Model, func()) {
	focusedStyles, blurredStyles := ui.NewTableStyles()
	repository, err := repository.NewSQLiteRepository("data/kennel.db")
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize repository: %v", err))
	}
	sampleProjects := loadProjects(repository)
	m := model.NewModel(focusedStyles, blurredStyles, sampleProjects, repository)

	m.ResizeTables(model.DefaultProjectWidth+model.DefaultAgentWidth+model.DefaultActivityWidth, model.DefaultTableHeight+model.FooterHeight+4)
	m.SetFocus(0)

	cleanup := func() {
		_ = repository.Close()
	}

	return m, cleanup
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
			Config: model.ProjectConfig{
				Name:         definition.name,
				Workplace:    "",
				Instructions: "",
			},
			State: model.ProjectState{
				State: agent.Stopped,
			},
			Runtime: model.ProjectRuntime{
				Agents:     agents,
				AgentIDs:   nil,
				Activities: append([]model.ActivityEntry(nil), definition.activities...),
			},
		})
	}

	return projects
}

func loadProjects(repository *repository.SQLiteRepository) []model.Project {

	storedProjects, err := repository.ReadProjects(context.Background())
	if err != nil {
		_ = repository.Close()
		panic(fmt.Sprintf("Failed to read projects, falling back to samples: %v\n", err))
	}

	if len(storedProjects) == 0 {
		if err := seedSampleProjects(repository); err != nil {
			_ = repository.Close()
			panic(fmt.Sprintf("Failed to seed sample projects, falling back to samples: %v\n", err))
		}

		storedProjects, err = repository.ReadProjects(context.Background())
		if err != nil || len(storedProjects) == 0 {
			_ = repository.Close()
			if err != nil {
				panic(fmt.Sprintf("Failed to read seeded projects, falling back to samples: %v\n", err))
			} else {
				panic("Failed to read seeded projects (empty), falling back to samples\n")
			}
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
			Config: model.ProjectConfig{
				ProjectID:    storedProject.ID,
				Name:         storedProject.Name,
				Workplace:    storedProject.Workplace,
				Instructions: storedProject.Instructions,
			},
			State: model.ProjectState{
				State: restoreState(storedProject.State),
			},
			Runtime: model.ProjectRuntime{
				Agents:     agents,
				AgentIDs:   agentIDs,
				Activities: activities,
			},
		})
	}

	return projects
}

func seedSampleProjects(repository *repository.SQLiteRepository) error {
	for _, definition := range sampleProjects() {
		project, err := repository.CreateProject(context.Background(), definition.Config.Name)
		if err != nil {
			return err
		}

		for _, agentInstance := range definition.Runtime.Agents {
			if _, err := repository.AddAgentToProject(context.Background(), project.ID, agentInstance.Name()); err != nil {
				return err
			}
		}

		for _, activity := range definition.Runtime.Activities {
			if _, err := repository.NewActivity(context.Background(), project.ID, sql.NullInt64{}, activity.Text); err != nil {
				return err
			}
		}
	}

	return nil
}

func restoreAgentState(name string, persistedState string) agent.AgentContract {
	a := agent.NewAgent(name)
	switch persistedState {
	case agent.Running.String():
		a.Run(context.Background())
	case agent.Completed.String():
		a.Complete()
	}
	return a
}

func restoreState(persistedState string) agent.AgentState {
	switch persistedState {
	case agent.Running.String():
		return agent.Running
	case agent.Completed.String():
		return agent.Completed
	default:
		return agent.Stopped
	}
}
