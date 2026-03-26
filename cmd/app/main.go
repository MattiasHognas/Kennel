package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	data "MattiasHognas/Kennel/internal/data"
	logic "MattiasHognas/Kennel/internal/logic"
	ui "MattiasHognas/Kennel/internal/ui"
	workers "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
)

const appLoggerName = "app"

func main() {
	appLogger := data.NewProjectLogger(".", 0, appLoggerName)
	m, cleanup, err := initialModel()
	if err != nil {
		reportAppError(appLogger, "Failed to initialize application: %v", err)
		return
	}

	p := tea.NewProgram(m)
	finalModel, err := p.Run()
	shutdownModel := m
	if persistedModel, ok := finalModel.(logic.Model); ok {
		shutdownModel = persistedModel
	}
	shutdownModel.Shutdown()
	cleanup()

	if err != nil {
		reportAppError(appLogger, "Something broke: %v", err)
	}
}

func reportAppError(logger *data.ProjectLogger, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if logger != nil {
		logger.LogProjectError(message)
	}
	log.Printf("%s", message)
}

func initialModel() (logic.Model, func(), error) {
	focusedStyles, blurredStyles := ui.NewTableStyles()
	repository, err := data.NewSQLiteRepository("data/kennel.db")
	if err != nil {
		return logic.Model{}, func() {}, fmt.Errorf("initialize repository: %w", err)
	}
	sampleProjects, err := loadProjects(repository)
	if err != nil {
		_ = repository.Close()
		return logic.Model{}, func() {}, fmt.Errorf("load projects: %w", err)
	}
	m := logic.NewModel(focusedStyles, blurredStyles, sampleProjects, repository)

	m.ResizeTables(logic.DefaultProjectWidth+logic.DefaultAgentWidth+logic.DefaultActivityWidth, logic.DefaultTableHeight+logic.FooterHeight+4)
	m.SetFocus(0)

	cleanup := func() {
		_ = repository.Close()
	}

	return m, cleanup, nil
}

func sampleProjects() ([]logic.Project, error) {
	workplace, err := sampleProjectWorkplace()
	if err != nil {
		return nil, err
	}

	var seedConfig = logic.ProjectConfig{Name: "Sample project", Workplace: workplace, Instructions: "Build a simple dotnet 10 web api returning funny or bad jokes and a frontend where you can cycle trough the jokes"}

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

	return projects, nil
}

func sampleProjectWorkplace() (string, error) {
	absWorkplace, err := filepath.Abs("../test_project")
	if err != nil {
		return "", fmt.Errorf("resolve sample project workplace directory: %w", err)
	}

	return absWorkplace, nil
}

func loadProjects(repository *data.SQLiteRepository) ([]logic.Project, error) {

	storedProjects, err := repository.ReadProjects(context.Background())
	if err != nil {
		return nil, fmt.Errorf("read projects: %w", err)
	}

	if len(storedProjects) == 0 {
		if err := seedSampleProjects(repository); err != nil {
			return nil, fmt.Errorf("seed sample projects: %w", err)
		}

		storedProjects, err = repository.ReadProjects(context.Background())
		if err != nil || len(storedProjects) == 0 {
			if err != nil {
				return nil, fmt.Errorf("read seeded projects: %w", err)
			} else {
				return nil, fmt.Errorf("read seeded projects: empty result")
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

	return projects, nil
}

func seedSampleProjects(repository *data.SQLiteRepository) error {
	definitions, err := sampleProjects()
	if err != nil {
		return err
	}

	for _, definition := range definitions {
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
