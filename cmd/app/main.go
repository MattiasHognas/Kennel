package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	data "MattiasHognas/Kennel/internal/data"
	logic "MattiasHognas/Kennel/internal/logic"
	ui "MattiasHognas/Kennel/internal/ui"
	workers "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
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
		panic(fmt.Sprintf("Something broke: %v", err))
	}
}

func initialModel() (logic.Model, func()) {
	focusedStyles, blurredStyles := ui.NewTableStyles()
	repository, err := data.NewSQLiteRepository("data/kennel.db")
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize repository: %v", err))
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

func sampleProjects() []logic.Project {

	var seedConfig = logic.ProjectConfig{Name: "Sample project", Workplace: sampleProjectWorkplace(), Instructions: "Build a simple dotnet 10 web api returning funny or bad jokes and a frontend where you can cycle trough the jokes"}

	projects := make([]logic.Project, 0)
	agents := make([]workers.AgentContract, 0)

	projects = append(projects, logic.Project{
		Config: logic.ProjectConfig{
			Name:         seedConfig.Name,
			Workplace:    seedConfig.Workplace,
			Instructions: seedConfig.Instructions,
		},
		State: logic.ProjectState{
			State: workers.Stopped,
		},
		Runtime: logic.ProjectRuntime{
			Agents:     agents,
			AgentIDs:   nil,
			Activities: []logic.ActivityEntry{},
		},
	})

	return projects
}

func sampleProjectWorkplace() string {
	absWorkplace, err := filepath.Abs("test_project")
	if err != nil {
		return "test_project"
	}

	return absWorkplace
}

func loadProjects(repository *data.SQLiteRepository) []logic.Project {

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

	projects := make([]logic.Project, 0, len(storedProjects))
	for _, storedProject := range storedProjects {
		agents := make([]workers.AgentContract, 0, len(storedProject.Agents))
		agentIDs := make([]int64, 0, len(storedProject.Agents))
		for _, storedAgent := range storedProject.Agents {
			agents = append(agents, restoreAgentState(storedAgent.Name, storedAgent.State))
			agentIDs = append(agentIDs, storedAgent.ID)
		}

		activities := make([]logic.ActivityEntry, 0, len(storedProject.Activities))
		for _, activity := range storedProject.Activities {
			activities = append(activities, logic.ActivityEntry{
				Timestamp: activity.CreatedAt.Format("15:04:05"),
				Text:      activity.Text,
			})
		}

		projects = append(projects, logic.Project{
			Config: logic.ProjectConfig{
				ProjectID:    storedProject.ID,
				Name:         storedProject.Name,
				Workplace:    storedProject.Workplace,
				Instructions: storedProject.Instructions,
			},
			State: logic.ProjectState{
				State: restoreState(storedProject.State),
			},
			Runtime: logic.ProjectRuntime{
				Agents:     agents,
				AgentIDs:   agentIDs,
				Plan:       logic.RestorePlanFromStoredAgents(storedProject.Agents),
				Activities: activities,
			},
		})
	}

	return projects
}

func seedSampleProjects(repository *data.SQLiteRepository) error {
	for _, definition := range sampleProjects() {
		workplace := definition.Config.Workplace
		if workplace != "" {
			absWP, err := filepath.Abs(workplace)
			if err != nil {
				return fmt.Errorf("resolve workplace directory %q: %w", workplace, err)
			}
			if err := os.MkdirAll(absWP, 0o755); err != nil {
				return fmt.Errorf("create workplace directory %q: %w", absWP, err)
			}
			workplace = absWP
		}

		project, err := repository.CreateProject(context.Background(), definition.Config.Name, workplace, definition.Config.Instructions)
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

func restoreAgentState(name string, persistedState string) workers.AgentContract {
	a := workers.NewAgent(name)
	a.Hydrate(restoreState(persistedState))
	return a
}

func restoreState(persistedState string) workers.AgentState {
	switch persistedState {
	case workers.Running.String():
		return workers.Running
	case workers.Completed.String():
		return workers.Completed
	case workers.Failed.String():
		return workers.Failed
	default:
		return workers.Stopped
	}
}
