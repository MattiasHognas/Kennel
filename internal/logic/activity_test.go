package model

import (
	"testing"

	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"
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
