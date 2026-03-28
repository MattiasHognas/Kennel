package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	table "MattiasHognas/Kennel/internal/ui/table"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWaitForAgentRunReturnsResult(t *testing.T) {
	resultCh := make(chan agentRunResult, 1)
	resultCh <- agentRunResult{Output: "done"}
	close(resultCh)

	msg := waitForAgentRun(agentRunSource{projectIndex: 1, agentIndex: 2, result: resultCh})()
	completed, ok := msg.(manualAgentCompletedMsg)
	if !ok {
		t.Fatalf("message type = %T, want manualAgentCompletedMsg", msg)
	}
	if completed.source.projectIndex != 1 || completed.source.agentIndex != 2 {
		t.Fatalf("source = %#v, want project 1 agent 2", completed.source)
	}
	if completed.result.Output != "done" {
		t.Fatalf("output = %q, want done", completed.result.Output)
	}
}

func TestDefaultAgentExecutorReturnsLaunchError(t *testing.T) {
	_, err := defaultAgentExecutor(context.Background(), data.AgentDefinition{
		LaunchConfig: data.LaunchConfig{Binary: "__definitely_missing_binary__"},
	}, t.TempDir(), "tester", "Task: test", nil)
	if err == nil {
		t.Fatal("expected launch error for missing binary")
	}
}

func TestWaitForAgentRunHandlesClosedChannel(t *testing.T) {
	resultCh := make(chan agentRunResult)
	close(resultCh)

	msg := waitForAgentRun(agentRunSource{projectIndex: 0, agentIndex: 0, result: resultCh})()
	completed, ok := msg.(manualAgentCompletedMsg)
	if !ok {
		t.Fatalf("message type = %T, want manualAgentCompletedMsg", msg)
	}
	if completed.result != (agentRunResult{}) {
		t.Fatalf("result = %#v, want zero value", completed.result)
	}
}

func TestHandleAgentRunCompletedMarksAgentFailedAndClearsRunState(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord := newTestProject(t, repo)
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "tester", "")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	tester := workers.NewAgent("tester")
	resultCh := make(chan agentRunResult)
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{ProjectID: projectRecord.ID, Name: projectRecord.Name},
		State:  ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Agents:          []workers.AgentContract{tester},
			AgentIDs:        []int64{agentRecord.ID},
			AgentRunCancels: map[int]context.CancelFunc{0: func() {}},
			AgentRunResults: map[int]chan agentRunResult{0: resultCh},
		},
	}}, repo)

	updated, cmd := m.handleAgentRunCompleted(manualAgentCompletedMsg{
		source: agentRunSource{projectIndex: 0, agentIndex: 0, result: resultCh},
		result: agentRunResult{Err: errors.New("boom")},
	})
	if cmd != nil {
		t.Fatalf("cmd = %#v, want nil", cmd)
	}
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if model.projects[0].Runtime.Agents[0].State() != workers.Failed {
		t.Fatalf("agent state = %s, want %s", model.projects[0].Runtime.Agents[0].State(), workers.Failed)
	}
	if len(model.projects[0].Runtime.AgentRunCancels) != 0 || len(model.projects[0].Runtime.AgentRunResults) != 0 {
		t.Fatalf("run state not cleared: cancels=%v results=%v", model.projects[0].Runtime.AgentRunCancels, model.projects[0].Runtime.AgentRunResults)
	}

	persisted := mustReadProject(t, repo, projectRecord.ID)
	assertAgentState(t, persisted.Agents, "tester", workers.Failed.String(), "")
}

func TestHandleAgentRunCompletedIgnoresCanceledRun(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord := newTestProject(t, repo)
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "tester", "")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	tester := workers.NewAgent("tester")
	resultCh := make(chan agentRunResult)
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{ProjectID: projectRecord.ID, Name: projectRecord.Name},
		State:  ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Agents:          []workers.AgentContract{tester},
			AgentIDs:        []int64{agentRecord.ID},
			AgentRunCancels: map[int]context.CancelFunc{0: func() {}},
			AgentRunResults: map[int]chan agentRunResult{0: resultCh},
		},
	}}, repo)

	updated, _ := m.handleAgentRunCompleted(manualAgentCompletedMsg{
		source: agentRunSource{projectIndex: 0, agentIndex: 0, result: resultCh},
		result: agentRunResult{Err: context.Canceled},
	})
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if model.projects[0].Runtime.Agents[0].State() != workers.Stopped {
		t.Fatalf("agent state = %s, want %s", model.projects[0].Runtime.Agents[0].State(), workers.Stopped)
	}
	if len(model.projects[0].Runtime.AgentRunResults) != 0 {
		t.Fatalf("run results not cleared: %v", model.projects[0].Runtime.AgentRunResults)
	}

	persisted := mustReadProject(t, repo, projectRecord.ID)
	assertAgentState(t, persisted.Agents, "tester", workers.Stopped.String(), "")
}

func TestHandleAgentRunCompletedIgnoresStaleSource(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord := newTestProject(t, repo)
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "tester", "")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}

	tester := workers.NewAgent("tester")
	currentResultCh := make(chan agentRunResult)
	staleResultCh := make(chan agentRunResult)
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{ProjectID: projectRecord.ID, Name: projectRecord.Name},
		State:  ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Agents:          []workers.AgentContract{tester},
			AgentIDs:        []int64{agentRecord.ID},
			AgentRunResults: map[int]chan agentRunResult{0: currentResultCh},
		},
	}}, repo)

	updated, _ := m.handleAgentRunCompleted(manualAgentCompletedMsg{
		source: agentRunSource{projectIndex: 0, agentIndex: 0, result: staleResultCh},
		result: agentRunResult{Output: "ignored"},
	})
	model, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	if model.projects[0].Runtime.Agents[0].State() != workers.Stopped {
		t.Fatalf("agent state = %s, want %s", model.projects[0].Runtime.Agents[0].State(), workers.Stopped)
	}
	if len(model.projects[0].Runtime.AgentRunResults) != 1 {
		t.Fatalf("current run was cleared unexpectedly: %v", model.projects[0].Runtime.AgentRunResults)
	}
}

func TestCancelProjectAgentRunsCancelsAndClearsState(t *testing.T) {
	project := &Project{Runtime: ProjectRuntime{
		AgentRunCancels: map[int]context.CancelFunc{},
		AgentRunResults: map[int]chan agentRunResult{0: make(chan agentRunResult), 1: make(chan agentRunResult)},
	}}

	cancelCount := 0
	project.Runtime.AgentRunCancels[0] = func() { cancelCount++ }
	project.Runtime.AgentRunCancels[1] = nil

	var m Model
	m.cancelProjectAgentRuns(project)

	if cancelCount != 1 {
		t.Fatalf("cancel count = %d, want 1", cancelCount)
	}
	if len(project.Runtime.AgentRunCancels) != 0 || len(project.Runtime.AgentRunResults) != 0 {
		t.Fatalf("run state not cleared: cancels=%v results=%v", project.Runtime.AgentRunCancels, project.Runtime.AgentRunResults)
	}
}

func TestBuildSelectedAgentExecutionRejectsNonPlannedSelection(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord := newTestProject(t, repo)
	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{ProjectID: projectRecord.ID, Name: projectRecord.Name},
		State:  ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Plan: &Plan{Streams: []TaskStream{{{Agent: "tester", Task: "Run tests"}}}},
		},
	}}, repo)
	m.projectTable.SetCursor(1)
	m.agentTableEntries = []agentTableEntry{{Kind: planRowNone, AgentIndex: nonSelectableAgentIndex, StreamIndex: -1, StepIndex: -1}}
	m.agentTable.SetCursor(0)

	_, err := m.buildSelectedAgentExecution(&m.projects[0])
	if err == nil || err.Error() != "selected agent is not a planned task" {
		t.Fatalf("error = %v, want selected agent is not a planned task", err)
	}
}

func TestBuildSelectedAgentExecutionErrorsWhenDefinitionMissing(t *testing.T) {
	repo := newTestRepository(t)
	projectRecord := newTestProject(t, repo)
	agentRecord, err := repo.AddAgentToProject(context.Background(), projectRecord.ID, "planner", "")
	if err != nil {
		t.Fatalf("add planner: %v", err)
	}
	if err := repo.UpdateAgentOutput(context.Background(), agentRecord.ID, `{"streams":[[{"agent":"missing-agent","task":"Do work"}]]}`); err != nil {
		t.Fatalf("update planner output: %v", err)
	}

	agentsRoot := newTestAgentsRoot(t, "branch-setup", "tester")
	t.Setenv("KENNEL_ROOT_DIR", agentsRoot)

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		Config: ProjectConfig{ProjectID: projectRecord.ID, Name: projectRecord.Name},
		State:  ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Plan: &Plan{Streams: []TaskStream{{{Agent: "missing-agent", Task: "Do work"}}}},
		},
	}}, repo)
	m.projectTable.SetCursor(1)
	m.agentTableEntries = []agentTableEntry{{Kind: planRowAgent, AgentIndex: 0, StreamIndex: 0, StepIndex: 0}}
	m.agentTable.SetCursor(0)

	_, err = m.buildSelectedAgentExecution(&m.projects[0])
	if err == nil || !strings.Contains(err.Error(), "agent definition not found") {
		t.Fatalf("error = %v, want missing agent definition", err)
	}
}

func TestFindAgentDefinitionMatchesCanonicalName(t *testing.T) {
	definition, err := findAgentDefinition([]data.AgentDefinition{{Name: "frontend-developer"}}, "Frontend Developer")
	if err != nil {
		t.Fatalf("findAgentDefinition returned error: %v", err)
	}
	if definition.Name != "frontend-developer" {
		t.Fatalf("definition name = %q, want frontend-developer", definition.Name)
	}
}

func TestStoredAgentOutputReturnsEmptyForBlankStoredOutput(t *testing.T) {
	output := storedAgentOutput([]data.Agent{{Name: "tester", Output: "   "}}, "tester")
	if output != "" {
		t.Fatalf("output = %q, want empty string", output)
	}
}
