package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	table "MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestNewProjectWithoutAgentsCanToggleState(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord, err := repo.CreateProject(context.Background(), "Agentless", `C:\src\agentless`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID:    projectRecord.ID,
			Name:         projectRecord.Name,
			Workplace:    projectRecord.Workplace,
			Instructions: projectRecord.Instructions,
		},
		State: ProjectState{
			State: workers.Stopped,
		},
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	updatedModel, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].State.State != workers.Running {
		t.Fatalf("state after start = %s, want %s", modelAfterStart.projects[0].State.State, workers.Running)
	}

	updatedModel, _ = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].State.State != workers.Stopped {
		t.Fatalf("state after stop = %s, want %s", modelAfterStop.projects[0].State.State, workers.Stopped)
	}
}

func runCmdWithTimeout(t *testing.T, cmd tea.Cmd, timeout time.Duration) tea.Msg {
	t.Helper()

	if cmd == nil {
		return nil
	}

	result := make(chan tea.Msg, 1)
	go func() {
		result <- cmd()
	}()

	select {
	case msg := <-result:
		return msg
	case <-time.After(timeout):
		t.Fatalf("command timed out after %s", timeout)
		return nil
	}
}

func TestStartSelectedProjectCommandReturnsWhenSupervisorExitsEarly(t *testing.T) {
	repo := newTestRepository(t)

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: 1,
			Name:      "Missing Project",
		},
		State: ProjectState{
			State: workers.Stopped,
		},
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd == nil {
		t.Fatal("expected supervisor listener command")
	}
	msg := runCmdWithTimeout(t, cmd, 2*time.Second)
	if _, ok := msg.(supervisorCompletedMsg); !ok {
		t.Fatalf("supervisor listener message = %#v, want supervisorCompletedMsg on early supervisor exit", msg)
	}

	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].State.State != workers.Running {
		t.Fatalf("state after start = %s, want %s", modelAfterStart.projects[0].State.State, workers.Running)
	}
	if modelAfterStart.projects[0].Runtime.SupervisorDone == nil {
		t.Fatal("expected supervisor done signal to be tracked")
	}
	if modelAfterStart.projects[0].Runtime.SupervisorEvents == nil {
		t.Fatal("expected supervisor event subscription to be tracked")
	}
	if modelAfterStart.projects[0].Runtime.CancelCtx == nil {
		t.Fatal("expected supervisor cancel function to be tracked")
	}
	if modelAfterStart.projects[0].Runtime.Supervisor == nil {
		t.Fatal("expected supervisor instance to be tracked")
	}
	if modelAfterStart.projects[0].Runtime.SupervisorDone != nil {
		select {
		case <-modelAfterStart.projects[0].Runtime.SupervisorDone:
		default:
			t.Fatal("expected supervisor done signal to close after early exit")
		}
	}
	if modelAfterStart.projects[0].Runtime.SupervisorEvents == nil {
		t.Fatal("expected supervisor events to remain available for cleanup")
	}

	modelAfterStart.stopSelectedProject()
	if modelAfterStart.projects[0].Runtime.Supervisor != nil {
		t.Fatal("expected supervisor to be cleared on stop")
	}
}

func TestFocusedAgentCyclesBetweenRunningAndStopped(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject(context.Background(), "With Agent", `C:\src\with-agent`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "Worker")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	worker := workers.NewAgent("Worker")
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: projectRecord.ID,
			Name:      projectRecord.Name,
		},
		State: ProjectState{
			State: workers.Stopped,
		},
		Runtime: ProjectRuntime{
			Agents:   []workers.AgentContract{worker},
			AgentIDs: []int64{agentRecord.ID},
		},
	}}, repo)
	m.SetFocus(1)
	m.projectTable.SetCursor(1)
	m.agentTable.SetCursor(0)

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	runCmdWithTimeout(t, cmd, 2*time.Second)
	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].Runtime.Agents[0].State() != workers.Running {
		t.Fatalf("agent state after start = %s, want %s", modelAfterStart.projects[0].Runtime.Agents[0].State(), workers.Running)
	}

	updatedModel, cmd = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	runCmdWithTimeout(t, cmd, 2*time.Second)
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].Runtime.Agents[0].State() != workers.Stopped {
		t.Fatalf("agent state after stop = %s, want %s", modelAfterStop.projects[0].Runtime.Agents[0].State(), workers.Stopped)
	}

	persistedProject, err := repo.ReadProject(context.Background(), projectRecord.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if persistedProject.Agents[0].State != workers.Stopped.String() {
		t.Fatalf("stored agent state = %q, want %q", persistedProject.Agents[0].State, workers.Stopped.String())
	}
}

func mustUpdateModel(t *testing.T, m Model, msg tea.Msg) (Model, bool) {
	t.Helper()

	updatedModel, cmd := m.Update(msg)
	runCmdWithTimeout(t, cmd, 2*time.Second)
	result, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
		return Model{}, false
	}
	return result, true
}

func TestCompletedStatesAreNotChangedByUserControls(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject(context.Background(), "Completed Project", `C:\src\completed-project`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "Worker")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	worker := workers.NewAgent("Worker")
	worker.Complete()
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: projectRecord.ID,
			Name:      projectRecord.Name,
		},
		State: ProjectState{
			State: workers.Completed,
		},
		Runtime: ProjectRuntime{
			Agents:   []workers.AgentContract{worker},
			AgentIDs: []int64{agentRecord.ID},
		},
	}}, repo)
	m.projectTable.SetCursor(1)

	projectModel, ok := mustUpdateModel(t, m, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != workers.Completed {
		t.Fatalf("project state after space = %s, want %s", projectModel.projects[0].State.State, workers.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != workers.Completed {
		t.Fatalf("project state after start = %s, want %s", projectModel.projects[0].State.State, workers.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != workers.Completed {
		t.Fatalf("project state after stop = %s, want %s", projectModel.projects[0].State.State, workers.Completed)
	}

	projectModel.SetFocus(1)
	projectModel.agentTable.SetCursor(0)

	agentModel, ok := mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != workers.Completed {
		t.Fatalf("agent state after space = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), workers.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != workers.Completed {
		t.Fatalf("agent state after start = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), workers.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != workers.Completed {
		t.Fatalf("agent state after stop = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), workers.Completed)
	}
}

func TestProjectCompletesAutomaticallyWhenSupervisorFinishes(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject(context.Background(), "Auto Complete", `C:\src\auto-complete`, "do work")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Simulate a project that is running with a supervisor whose done channel
	// is already closed, as if RunPlan returned successfully.
	done := make(chan struct{})
	close(done)
	eventChan := make(chan data.Event)
	source := supervisorSource{
		projectIndex: 0,
		channel:      eventChan,
		done:         done,
	}

	_, cancelCtx := context.WithCancel(context.Background())
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID:    projectRecord.ID,
			Name:         projectRecord.Name,
			Workplace:    projectRecord.Workplace,
			Instructions: projectRecord.Instructions,
		},
		State: ProjectState{
			State: workers.Running,
		},
		Runtime: ProjectRuntime{
			SupervisorEvents: source.channel,
			SupervisorDone:   source.done,
			CancelCtx:        cancelCtx,
		},
	}}, repo)

	// The waitForSupervisorUpdate command should immediately return a
	// supervisorCompletedMsg because the done channel is already closed.
	completedMsg := runCmdWithTimeout(t, waitForSupervisorUpdate(source), 2*time.Second)
	if _, ok := completedMsg.(supervisorCompletedMsg); !ok {
		t.Fatalf("expected supervisorCompletedMsg, got %T: %#v", completedMsg, completedMsg)
	}

	// Feed the completion message into the model.
	modelAfterComplete, _ := m.Update(completedMsg)
	finalModel, ok := modelAfterComplete.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", modelAfterComplete)
	}
	if finalModel.projects[0].State.State != workers.Completed {
		t.Fatalf("state after supervisor finish = %s, want %s", finalModel.projects[0].State.State, workers.Completed)
	}

	// Verify the completed state is persisted.
	stored, err := repo.ReadProject(context.Background(), projectRecord.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if stored.State != workers.Completed.String() {
		t.Fatalf("stored project state = %q, want %q", stored.State, workers.Completed.String())
	}
}

func TestProjectStopsInsteadOfCompletingWhenSupervisorFails(t *testing.T) {
	repo := newTestRepository(t)

	projectRecord, err := repo.CreateProject(context.Background(), "Failed Run", `C:\src\failed-run`, "do work")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	done := make(chan struct{})
	close(done)
	result := make(chan error, 1)
	result <- errors.New("planner failed")
	close(result)
	eventChan := make(chan data.Event)
	source := supervisorSource{
		projectIndex: 0,
		channel:      eventChan,
		done:         done,
		result:       result,
	}

	_, cancelCtx := context.WithCancel(context.Background())
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID:    projectRecord.ID,
			Name:         projectRecord.Name,
			Workplace:    projectRecord.Workplace,
			Instructions: projectRecord.Instructions,
		},
		State: ProjectState{
			State: workers.Running,
		},
		Runtime: ProjectRuntime{
			SupervisorEvents: source.channel,
			SupervisorDone:   source.done,
			SupervisorResult: source.result,
			CancelCtx:        cancelCtx,
		},
	}}, repo)

	completedMsg := runCmdWithTimeout(t, waitForSupervisorUpdate(source), 2*time.Second)
	supervisorMsg, ok := completedMsg.(supervisorCompletedMsg)
	if !ok {
		t.Fatalf("expected supervisorCompletedMsg, got %T: %#v", completedMsg, completedMsg)
	}
	if supervisorMsg.err == nil {
		t.Fatal("expected supervisorCompletedMsg to carry run error")
	}

	modelAfterComplete, _ := m.Update(completedMsg)
	finalModel, ok := modelAfterComplete.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", modelAfterComplete)
	}
	if finalModel.projects[0].State.State != workers.Stopped {
		t.Fatalf("state after supervisor failure = %s, want %s", finalModel.projects[0].State.State, workers.Stopped)
	}
	if finalModel.projects[0].Runtime.Supervisor != nil {
		t.Fatal("expected supervisor to be cleared after failure")
	}

	stored, err := repo.ReadProject(context.Background(), projectRecord.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if stored.State != workers.Stopped.String() {
		t.Fatalf("stored project state = %q, want %q", stored.State, agent.Stopped.String())
	}
}
