package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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

	var seedConfig = model.ProjectConfig{Name: "Sample project", Workplace: "test_project", Instructions: "Build a simple dotnet 10 web api returning funny or bad jokes and a frontend where you can cycle trough the jokes"}

	projects := make([]model.Project, 0)
	agents := make([]agent.AgentContract, 0)

	projects = append(projects, model.Project{
		Config: model.ProjectConfig{
			Name:         seedConfig.Name,
			Workplace:    seedConfig.Workplace,
			Instructions: seedConfig.Instructions,
		},
		State: model.ProjectState{
			State: agent.Stopped,
		},
		Runtime: model.ProjectRuntime{
			Agents:     agents,
			AgentIDs:   nil,
			Activities: []model.ActivityEntry{},
		},
	})

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
		if wp := definition.Config.Workplace; wp != "" {
			absWP := wp
			if !filepath.IsAbs(absWP) {
				if wd, err := os.Getwd(); err == nil {
					absWP = filepath.Join(wd, absWP)
				}
			}
			if err := os.MkdirAll(absWP, 0o755); err != nil {
				return fmt.Errorf("create workplace directory %q: %w", absWP, err)
			}
		}

		project, err := repository.CreateProject(context.Background(), definition.Config.Name, definition.Config.Workplace, definition.Config.Instructions)
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
	a.Hydrate(restoreState(persistedState))
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
