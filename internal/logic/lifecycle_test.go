package model

import (
	"context"
	"testing"
	"time"

	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"

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
			State: agent.Stopped,
		},
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	updatedModel, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].State.State != agent.Running {
		t.Fatalf("state after start = %s, want %s", modelAfterStart.projects[0].State.State, agent.Running)
	}

	updatedModel, _ = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].State.State != agent.Stopped {
		t.Fatalf("state after stop = %s, want %s", modelAfterStop.projects[0].State.State, agent.Stopped)
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
			State: agent.Stopped,
		},
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	if cmd == nil {
		t.Fatal("expected supervisor listener command")
	}
	if msg := runCmdWithTimeout(t, cmd, 2*time.Second); msg != nil {
		t.Fatalf("supervisor listener message = %#v, want nil on early supervisor exit", msg)
	}

	modelAfterStart, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStart.projects[0].State.State != agent.Running {
		t.Fatalf("state after start = %s, want %s", modelAfterStart.projects[0].State.State, agent.Running)
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

	worker := agent.NewAgent("Worker")
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: projectRecord.ID,
			Name:      projectRecord.Name,
		},
		State: ProjectState{
			State: agent.Stopped,
		},
		Runtime: ProjectRuntime{
			Agents:   []agent.AgentContract{worker},
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
	if modelAfterStart.projects[0].Runtime.Agents[0].State() != agent.Running {
		t.Fatalf("agent state after start = %s, want %s", modelAfterStart.projects[0].Runtime.Agents[0].State(), agent.Running)
	}

	updatedModel, cmd = modelAfterStart.Update(tea.KeyPressMsg(tea.Key{Code: ' '}))
	runCmdWithTimeout(t, cmd, 2*time.Second)
	modelAfterStop, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	if modelAfterStop.projects[0].Runtime.Agents[0].State() != agent.Stopped {
		t.Fatalf("agent state after stop = %s, want %s", modelAfterStop.projects[0].Runtime.Agents[0].State(), agent.Stopped)
	}

	persistedProject, err := repo.ReadProject(context.Background(), projectRecord.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if persistedProject.Agents[0].State != agent.Stopped.String() {
		t.Fatalf("stored agent state = %q, want %q", persistedProject.Agents[0].State, agent.Stopped.String())
	}
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

	worker := agent.NewAgent("Worker")
	worker.Complete()
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: projectRecord.ID,
			Name:      projectRecord.Name,
		},
		State: ProjectState{
			State: agent.Completed,
		},
		Runtime: ProjectRuntime{
			Agents:   []agent.AgentContract{worker},
			AgentIDs: []int64{agentRecord.ID},
		},
	}}, repo)
	m.projectTable.SetCursor(1)

	projectModel, ok := mustUpdateModel(t, m, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != agent.Completed {
		t.Fatalf("project state after space = %s, want %s", projectModel.projects[0].State.State, agent.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != agent.Completed {
		t.Fatalf("project state after start = %s, want %s", projectModel.projects[0].State.State, agent.Completed)
	}

	projectModel, ok = mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if projectModel.projects[0].State.State != agent.Completed {
		t.Fatalf("project state after stop = %s, want %s", projectModel.projects[0].State.State, agent.Completed)
	}

	projectModel.SetFocus(1)
	projectModel.agentTable.SetCursor(0)

	agentModel, ok := mustUpdateModel(t, projectModel, tea.KeyPressMsg(tea.Key{Code: ' '}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after space = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), agent.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 's'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after start = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), agent.Completed)
	}

	agentModel, ok = mustUpdateModel(t, agentModel, tea.KeyPressMsg(tea.Key{Code: 'p'}))
	if !ok {
		return
	}
	if agentModel.projects[0].Runtime.Agents[0].State() != agent.Completed {
		t.Fatalf("agent state after stop = %s, want %s", agentModel.projects[0].Runtime.Agents[0].State(), agent.Completed)
	}
}
