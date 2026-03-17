package model

import (
	"path/filepath"
	"testing"

	repository "MattiasHognas/Kennel/internal/data"
	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
)

func TestShutdownStopsRunningAgentsAndPersistsActivity(t *testing.T) {
	repo := newTestRepository(t)

	storedProject, err := repo.CreateProject("Test Project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	runningAgentRecord, err := repo.AddAgentToProject(storedProject.ID, "Runner")
	if err != nil {
		t.Fatalf("create running agent: %v", err)
	}

	stoppedAgentRecord, err := repo.AddAgentToProject(storedProject.ID, "Idle")
	if err != nil {
		t.Fatalf("create stopped agent: %v", err)
	}

	runningAgent := agent.NewAgent("Runner")
	runningAgent.Run()
	stoppedAgent := agent.NewAgent("Idle")

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: storedProject.ID,
		Name:      storedProject.Name,
		Agents:    []agent.AgentContract{runningAgent, stoppedAgent},
		AgentIDs:  []int64{runningAgentRecord.ID, stoppedAgentRecord.ID},
	}}, repo)

	m.Shutdown()

	project, err := repo.ReadProject(storedProject.ID)
	if err != nil {
		t.Fatalf("read project after shutdown: %v", err)
	}

	if project.Agents[0].State != agent.Stopped.String() {
		t.Fatalf("running agent state = %q, want %q", project.Agents[0].State, agent.Stopped.String())
	}
	if project.Agents[1].State != agent.Stopped.String() {
		t.Fatalf("idle agent state = %q, want %q", project.Agents[1].State, agent.Stopped.String())
	}
	if len(project.Activities) != 1 {
		t.Fatalf("activity count = %d, want 1", len(project.Activities))
	}
	if project.Activities[0].Text != "Runner: stopped" {
		t.Fatalf("stored activity = %q, want %q", project.Activities[0].Text, "Runner: stopped")
	}
	if len(m.projects[0].Activities) != 1 || m.projects[0].Activities[0].Text != "Runner: stopped" {
		t.Fatalf("model activities = %#v, want one stored stop activity", m.projects[0].Activities)
	}
	if m.projects[0].State != agent.Stopped {
		t.Fatalf("model project state = %s, want %s", m.projects[0].State, agent.Stopped)
	}
}

func TestEnterOpensProjectEditorAndClickingOKPersistsWorkplace(t *testing.T) {
	repo := newTestRepository(t)

	storedProject, err := repo.CreateProject("Configured Project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: storedProject.ID,
		Name:      storedProject.Name,
	}}, repo)
	m.SetFocus(0)

	openedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		cmd()
	}
	updatedModel, ok := openedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", openedModel)
	}
	if updatedModel.mode != projectEditorViewMode {
		t.Fatalf("mode = %v, want projectEditorViewMode", updatedModel.mode)
	}

	updatedModel.projectEditor.workplaceInput.SetValue(`C:\work\kennel`)
	updatedModel.projectEditor.instructionsInput.SetValue("step one\nstep two")
	clickedModel, saveCmd := updatedModel.Update(tea.MouseClickMsg(tea.Mouse{X: 1, Y: 13}))
	if saveCmd != nil {
		saveCmd()
	}
	updatedModel, ok = clickedModel.(Model)
	if !ok {
		t.Fatalf("clicked model type = %T, want Model", clickedModel)
	}
	if updatedModel.mode != tableViewMode {
		t.Fatalf("mode after save = %v, want tableViewMode", updatedModel.mode)
	}
	if updatedModel.projects[0].Workplace != `C:\work\kennel` {
		t.Fatalf("model workplace = %q, want %q", updatedModel.projects[0].Workplace, `C:\work\kennel`)
	}
	if updatedModel.projects[0].Instructions != "step one\nstep two" {
		t.Fatalf("model instructions = %q, want %q", updatedModel.projects[0].Instructions, "step one\nstep two")
	}

	persistedProject, err := repo.ReadProject(storedProject.ID)
	if err != nil {
		t.Fatalf("read project after save: %v", err)
	}
	if persistedProject.Workplace != `C:\work\kennel` {
		t.Fatalf("stored workplace = %q, want %q", persistedProject.Workplace, `C:\work\kennel`)
	}
	if persistedProject.Instructions != "step one\nstep two" {
		t.Fatalf("stored instructions = %q, want %q", persistedProject.Instructions, "step one\nstep two")
	}
}

func newTestRepository(t *testing.T) *repository.SQLiteRepository {
	t.Helper()

	repo, err := repository.NewSQLiteRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	return repo
}
