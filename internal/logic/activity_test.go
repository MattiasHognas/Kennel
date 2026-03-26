package model

import (
	"context"
	"testing"

	eventbus "MattiasHognas/Kennel/internal/events"
	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"
)

func TestShutdownStopsRunningAgentsAndPersistsActivity(t *testing.T) {
	repo := newTestRepository(t)

	storedProject, err := repo.CreateProject(context.Background(), "Test Project", `C:\src\test-project`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	runningAgentRecord, err := repo.AddAgentToProject(context.Background(), storedProject.ID, "Runner")
	if err != nil {
		t.Fatalf("create running agent: %v", err)
	}

	stoppedAgentRecord, err := repo.AddAgentToProject(context.Background(), storedProject.ID, "Idle")
	if err != nil {
		t.Fatalf("create stopped agent: %v", err)
	}

	runningAgent := agent.NewAgent("Runner")
	runningAgent.Run(context.Background())
	stoppedAgent := agent.NewAgent("Idle")

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: storedProject.ID,
			Name:      storedProject.Name,
		},
		Runtime: ProjectRuntime{
			Agents:   []agent.AgentContract{runningAgent, stoppedAgent},
			AgentIDs: []int64{runningAgentRecord.ID, stoppedAgentRecord.ID},
		},
	}}, repo)

	m.Shutdown()

	project, err := repo.ReadProject(context.Background(), storedProject.ID)
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
	if len(m.projects[0].Runtime.Activities) != 1 || m.projects[0].Runtime.Activities[0].Text != "Runner: stopped" {
		t.Fatalf("model activities = %#v, want one stored stop activity", m.projects[0].Runtime.Activities)
	}
	if m.projects[0].State.State != agent.Stopped {
		t.Fatalf("model project state = %s, want %s", m.projects[0].State.State, agent.Stopped)
	}
}

func TestSupervisorSyncAppliesEventWithoutRepositoryRefresh(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, err := repo.CreateProject(context.Background(), "Sync Project", `C:\src\sync-project`, "first line\nsecond line")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{
			ProjectID: storedProject.ID,
			Name:      storedProject.Name,
		},
		State: ProjectState{
			State: agent.Stopped,
		},
	}}, repo)
	m.projectTable.SetCursor(1)

	eb := eventbus.NewEventBus()
	source := supervisorSource{projectIndex: 0, channel: eb.Subscribe(eventbus.SupervisorTopic)}
	m.projects[0].Runtime.SupervisorEvents = source.channel

	eb.Publish(eventbus.SupervisorTopic, eventbus.Event{Payload: eventbus.SupervisorSyncEvent{
		ProjectID: storedProject.ID,
		AgentID:   42,
		Agent:     "planner",
		State:     agent.Completed.String(),
		Activity:  "completed",
	}})

	msg := waitForSupervisorUpdate(source)()
	if msg == nil {
		t.Fatal("expected supervisor sync message")
	}

	updatedModel, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("expected supervisor listener command")
	}
	updated, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}

	if len(updated.projects[0].Runtime.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1", len(updated.projects[0].Runtime.Agents))
	}
	if len(updated.projects[0].Runtime.AgentIDs) != 1 || updated.projects[0].Runtime.AgentIDs[0] != 42 {
		t.Fatalf("agent ids = %#v, want [42]", updated.projects[0].Runtime.AgentIDs)
	}
	if updated.projects[0].Runtime.Agents[0].Name() != "planner" {
		t.Fatalf("agent name = %q, want planner", updated.projects[0].Runtime.Agents[0].Name())
	}
	if updated.projects[0].Runtime.Agents[0].State() != agent.Completed {
		t.Fatalf("agent state = %s, want %s", updated.projects[0].Runtime.Agents[0].State(), agent.Completed)
	}
	if len(updated.projects[0].Runtime.Activities) != 1 || updated.projects[0].Runtime.Activities[0].Text != "planner: completed" {
		t.Fatalf("activities = %#v, want planner completion", updated.projects[0].Runtime.Activities)
	}
	if len(updated.Sources) != 1 {
		t.Fatalf("activity source count = %d, want 1", len(updated.Sources))
	}
	if updated.Sources[0].channel != updated.projects[0].Runtime.Agents[0].SubscribeActivity() {
		t.Fatal("activity source was not built for added agent")
	}

	persistedProject, err := repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if len(persistedProject.Agents) != 0 {
		t.Fatalf("persisted agent count = %d, want 0 to prove no repository refresh", len(persistedProject.Agents))
	}
	if len(persistedProject.Activities) != 0 {
		t.Fatalf("persisted activity count = %d, want 0 to prove no repository refresh", len(persistedProject.Activities))
	}
}

func TestWaitForSupervisorUpdateStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	msg := waitForSupervisorUpdate(supervisorSource{
		projectIndex: 0,
		channel:      make(chan eventbus.Event),
		done:         ctx.Done(),
	})()
	if msg != nil {
		t.Fatalf("message = %#v, want nil", msg)
	}
}
