package model

import (
	"testing"

	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
)

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
