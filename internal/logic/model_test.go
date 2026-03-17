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
	m.projectTable.SetCursor(1)

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

	updatedModel.projectEditor.nameInput.SetValue("Configured Project Updated")
	updatedModel.projectEditor.workplaceInput.SetValue(`C:\work\kennel`)
	updatedModel.projectEditor.instructionsInput.SetValue("step one\nstep two")
	left, top, _, _ := updatedModel.projectEditorOKButtonBounds()
	clickedModel, saveCmd := updatedModel.Update(tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))
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
	if updatedModel.projects[0].Name != "Configured Project Updated" {
		t.Fatalf("model name = %q, want %q", updatedModel.projects[0].Name, "Configured Project Updated")
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
	if persistedProject.Name != "Configured Project Updated" {
		t.Fatalf("stored name = %q, want %q", persistedProject.Name, "Configured Project Updated")
	}
	if persistedProject.Workplace != `C:\work\kennel` {
		t.Fatalf("stored workplace = %q, want %q", persistedProject.Workplace, `C:\work\kennel`)
	}
	if persistedProject.Instructions != "step one\nstep two" {
		t.Fatalf("stored instructions = %q, want %q", persistedProject.Instructions, "step one\nstep two")
	}
}

func TestCreateNewRowOpensEditorAndCreatesProject(t *testing.T) {
	repo := newTestRepository(t)

	m := NewModel(table.Styles{}, table.Styles{}, nil, repo)
	m.SetFocus(0)

	if !m.isCreateProjectSelected() {
		t.Fatalf("create row should be selected by default")
	}

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

	updatedModel.projectEditor.nameInput.SetValue("New Project")
	updatedModel.projectEditor.workplaceInput.SetValue(`C:\new\project`)
	updatedModel.projectEditor.instructionsInput.SetValue("do this\nand that")
	left, top, _, _ := updatedModel.projectEditorOKButtonBounds()
	clickedModel, saveCmd := updatedModel.Update(tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))
	if saveCmd != nil {
		saveCmd()
	}
	updatedModel, ok = clickedModel.(Model)
	if !ok {
		t.Fatalf("clicked model type = %T, want Model", clickedModel)
	}
	if len(updatedModel.projects) != 1 {
		t.Fatalf("project count = %d, want 1", len(updatedModel.projects))
	}
	if updatedModel.projects[0].Name != "New Project" {
		t.Fatalf("model name = %q, want %q", updatedModel.projects[0].Name, "New Project")
	}
	if updatedModel.projectTable.Cursor() != 1 {
		t.Fatalf("project cursor = %d, want 1", updatedModel.projectTable.Cursor())
	}

	projects, err := repo.ReadProjects()
	if err != nil {
		t.Fatalf("read projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("stored project count = %d, want 1", len(projects))
	}
	if projects[0].Name != "New Project" {
		t.Fatalf("stored name = %q, want %q", projects[0].Name, "New Project")
	}
	if projects[0].Workplace != `C:\new\project` {
		t.Fatalf("stored workplace = %q, want %q", projects[0].Workplace, `C:\new\project`)
	}
	if projects[0].Instructions != "do this\nand that" {
		t.Fatalf("stored instructions = %q, want %q", projects[0].Instructions, "do this\nand that")
	}
}

func TestNewProjectWithoutAgentsCanToggleState(t *testing.T) {
	repo := newTestRepository(t)

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: 1,
		Name:      "Agentless",
		State:     agent.Stopped,
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd != nil {
		cmd()
	}
	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].State != agent.Running {
		t.Fatalf("state after start = %s, want %s", modelAfterStart.projects[0].State, agent.Running)
	}

	updatedModel, cmd = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd != nil {
		cmd()
	}
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].State != agent.Stopped {
		t.Fatalf("state after stop = %s, want %s", modelAfterStop.projects[0].State, agent.Stopped)
	}
}

func TestFocusedAgentCyclesBetweenRunningAndStopped(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject("With Agent")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRecord, err := repo.AddAgentToProject(projectRecord.ID, "Worker")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	worker := agent.NewAgent("Worker")
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: projectRecord.ID,
		Name:      projectRecord.Name,
		State:     agent.Stopped,
		Agents:    []agent.AgentContract{worker},
		AgentIDs:  []int64{agentRecord.ID},
	}}, repo)
	m.SetFocus(1)
	m.projectTable.SetCursor(1)
	m.agentTable.SetCursor(0)

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd != nil {
		cmd()
	}
	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].Agents[0].State() != agent.Running {
		t.Fatalf("agent state after start = %s, want %s", modelAfterStart.projects[0].Agents[0].State(), agent.Running)
	}

	updatedModel, cmd = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd != nil {
		cmd()
	}
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].Agents[0].State() != agent.Stopped {
		t.Fatalf("agent state after stop = %s, want %s", modelAfterStop.projects[0].Agents[0].State(), agent.Stopped)
	}

	persistedProject, err := repo.ReadProject(projectRecord.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if persistedProject.Agents[0].State != agent.Stopped.String() {
		t.Fatalf("stored agent state = %q, want %q", persistedProject.Agents[0].State, agent.Stopped.String())
	}
}

func TestCompletedStatesAreNotChangedByUserControls(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject("Completed Project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRecord, err := repo.AddAgentToProject(projectRecord.ID, "Worker")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	worker := agent.NewAgent("Worker")
	worker.Complete()
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: projectRecord.ID,
		Name:      projectRecord.Name,
		State:     agent.Completed,
		Agents:    []agent.AgentContract{worker},
		AgentIDs:  []int64{agentRecord.ID},
	}}, repo)
	m.projectTable.SetCursor(1)

	projectModel, ok := mustUpdateModel(t, m, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if projectModel.projects[0].State != agent.Completed {
		t.Fatalf("project state after space = %s, want %s", projectModel.projects[0].State, agent.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State != agent.Completed {
		t.Fatalf("project state after start = %s, want %s", projectModel.projects[0].State, agent.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State != agent.Completed {
		t.Fatalf("project state after stop = %s, want %s", projectModel.projects[0].State, agent.Completed)
	}

	projectModel.SetFocus(1)
	projectModel.agentTable.SetCursor(0)

	agentModel, ok := mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if agentModel.projects[0].Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after space = %s, want %s", agentModel.projects[0].Agents[0].State(), agent.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after start = %s, want %s", agentModel.projects[0].Agents[0].State(), agent.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after stop = %s, want %s", agentModel.projects[0].Agents[0].State(), agent.Completed)
	}
}

func mustUpdateModel(t *testing.T, m Model, msg tea.Msg) (Model, bool) {
	t.Helper()

	updatedModel, cmd := m.Update(msg)
	if cmd != nil {
		cmd()
	}
	result, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
		return Model{}, false
	}
	return result, true
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
