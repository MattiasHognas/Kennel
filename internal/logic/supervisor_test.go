package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	repository "MattiasHognas/Kennel/internal/data"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type stubACPClient struct {
	response string
	messages *[]string
	err      error
}

func (c *stubACPClient) Prompt(ctx context.Context, msg string) (string, error) {
	if c.messages != nil {
		*c.messages = append(*c.messages, msg)
	}
	if c.err != nil {
		return "", c.err
	}
	return c.response, nil
}

func (c *stubACPClient) Close() error { return nil }

type trackingRepo struct {
	*data.SQLiteRepository
	mu          sync.Mutex
	checkpoints []string
}

func (r *trackingRepo) CheckpointSupervisorRun(ctx context.Context, projectID int64, stepIndex int, status, data string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints = append(r.checkpoints, status)
	return nil
}

func TestRunPlanAddsMissingAgentsAndPersistsPlannerResult(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	syncCh := eb.Subscribe(data.SupervisorTopic)
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	var topics []string
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)

		switch topic {
		case "planner":
			return &stubACPClient{response: `{"streams":[[{"agent":"frontend-developer","task":"Build UI"}]]}`}, nil
		case "branch-setup":
			return &stubACPClient{response: "branch ready"}, nil
		case "frontend-developer":
			return &stubACPClient{response: "frontend done"}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "planner", "completed", `{"streams":[[{"agent":"frontend-developer","task":"Build UI"}]]}`)
	assertAgentState(t, stored.Agents, "branch-setup", "completed", "branch ready")
	assertAgentState(t, stored.Agents, "frontend-developer", "completed", "frontend done")
	assertActivityContains(t, stored.Activities, "planner: completed")
	assertActivityContains(t, stored.Activities, "frontend-developer: completed")

	if strings.Join(topics, ",") != "planner,branch-setup,frontend-developer" {
		t.Fatalf("ACP topics = %v, want planner, branch-setup, frontend-developer", topics)
	}
	if len(tracking.checkpoints) == 0 {
		t.Fatal("expected supervisor checkpoints to be recorded")
	}
	select {
	case <-syncCh:
	default:
		t.Fatal("expected supervisor sync events to be published")
	}
}

func TestRunPlanResolvesAgentNameVariants(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{response: `{"streams":[[{"agent":"Frontend Developer","task":"Build UI"}]]}`}, nil
		case "branch-setup":
			return &stubACPClient{response: "branch ready"}, nil
		case "frontend-developer":
			return &stubACPClient{response: "frontend done"}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "frontend-developer", "completed", "frontend done")
}

func TestRunPlanAcceptsGeneralPurposeFallback(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{response: `{"streams":[[{"agent":"general_purpose","task":"Handle the implementation directly"}]]}`}, nil
		case "branch-setup":
			return &stubACPClient{response: "branch ready"}, nil
		case "general-purpose":
			return &stubACPClient{response: "generic work done"}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", nil); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "general-purpose", "completed", "generic work done")
}

func TestRunPlanRejectsUnknownAgentBeforeExecution(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	var topics []string
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)
		return &stubACPClient{response: `{"streams":[[{"agent":"unknown-agent","task":"Do work"}]]}`}, nil
	}

	err := super.RunPlan(context.Background(), "ship it", nil)
	if err == nil {
		t.Fatal("RunPlan returned nil error, want invalid-agent failure")
	}
	if !strings.Contains(err.Error(), "agent unknown-agent not found") {
		t.Fatalf("RunPlan error = %v, want unknown agent failure", err)
	}
	if strings.Join(topics, ",") != "planner" {
		t.Fatalf("ACP topics = %v, want only planner", topics)
	}

	stored, readErr := repo.ReadProject(context.Background(), project.ID)
	if readErr != nil {
		t.Fatalf("ReadProject returned error: %v", readErr)
	}
	if len(stored.Agents) != 1 {
		t.Fatalf("agent count = %d, want 1 planner after invalid plan", len(stored.Agents))
	}
	if stored.Agents[0].Name != "planner" {
		t.Fatalf("persisted agent = %q, want planner", stored.Agents[0].Name)
	}
	if len(tracking.checkpoints) == 0 || tracking.checkpoints[len(tracking.checkpoints)-1] != "planning_validation_failed" {
		t.Fatalf("checkpoint statuses = %v, want planning_validation_failed", tracking.checkpoints)
	}
}

func TestRunPlanOmitsPreviousOutputWhenAgentDisablesPromptContext(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "tester")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	var testerMessages []string
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{response: `{"streams":[[{"agent":"tester","task":"Run focused tests"}]]}`}, nil
		case "branch-setup":
			return &stubACPClient{response: "branch ready"}, nil
		case "tester":
			if definition.PromptContext.PreviousOutput {
				t.Fatalf("tester prompt context should be disabled by agent config")
			}
			return &stubACPClient{response: "tests done", messages: &testerMessages}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	testerDir := filepath.Join(agentsRoot, "agents", "tester")
	if err := os.WriteFile(filepath.Join(testerDir, "agent.json"), []byte(`{
		"promptContext": {"previousOutput": false},
		"permissions": {"git": {"status": false, "diff": false, "history": false}}
	}`), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"tester"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if len(testerMessages) != 1 {
		t.Fatalf("tester messages = %v, want exactly one prompt", testerMessages)
	}
	if testerMessages[0] != "Task: Run focused tests" {
		t.Fatalf("tester prompt = %q, want task-only prompt", testerMessages[0])
	}
}

func TestRunPlanMarksPlannerFailedAndStopsProjectOnLaunchFailure(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t)
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		if topic != "planner" {
			t.Fatalf("unexpected ACP topic %q", topic)
		}
		return &stubACPClient{err: fmt.Errorf("planner launch failed")}, nil
	}

	err := super.RunPlan(context.Background(), "ship it", nil)
	if err == nil {
		t.Fatal("RunPlan returned nil error, want planner failure")
	}

	stored, readErr := repo.ReadProject(context.Background(), project.ID)
	if readErr != nil {
		t.Fatalf("ReadProject returned error: %v", readErr)
	}
	if stored.State != "stopped" {
		t.Fatalf("project state = %q, want stopped", stored.State)
	}
	assertAgentState(t, stored.Agents, "planner", "failed", "")
	assertActivityContains(t, stored.Activities, "planner: failed: planner launch failed")
	if len(tracking.checkpoints) == 0 || tracking.checkpoints[len(tracking.checkpoints)-1] != "planning_failed" {
		t.Fatalf("checkpoint statuses = %v, want planning_failed", tracking.checkpoints)
	}
}

func newTestRepository(t *testing.T) *data.SQLiteRepository {
	t.Helper()

	repo, err := data.NewSQLiteRepository(filepath.Join(t.TempDir(), "supervisor.db"))
	if err != nil {
		t.Fatalf("NewSQLiteRepository returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	return repo
}

func newTestProject(t *testing.T, repo *data.SQLiteRepository) data.Project {
	t.Helper()

	project, err := repo.CreateProject(context.Background(), "test-project", t.TempDir(), "build something")
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	return project
}

func newTestAgentsRoot(t *testing.T, agentNames ...string) string {
	t.Helper()

	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "default.json"), []byte(`{"binary":"copilot","args":["--acp"]}`), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	for _, agentName := range agentNames {
		agentDir := filepath.Join(agentsDir, agentName)
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			t.Fatalf("MkdirAll returned error: %v", err)
		}
		instructionsPath := filepath.Join(agentDir, "instructions.md")
		if err := os.WriteFile(instructionsPath, []byte("# test instructions\n"), 0644); err != nil {
			t.Fatalf("WriteFile returned error: %v", err)
		}
	}

	return root
}

func assertAgentState(t *testing.T, agents []repository.Agent, name, state, output string) {
	t.Helper()

	for _, agent := range agents {
		if agent.Name != name {
			continue
		}
		if agent.State != state {
			t.Fatalf("agent %q state = %q, want %q", name, agent.State, state)
		}
		if agent.Output != output {
			t.Fatalf("agent %q output = %q, want %q", name, agent.Output, output)
		}
		return
	}

	t.Fatalf("agent %q not found in project", name)
}

func assertActivityContains(t *testing.T, activities []repository.Activity, want string) {
	t.Helper()

	for _, activity := range activities {
		if activity.Text == want {
			return
		}
	}

	t.Fatalf("activity %q not found in %#v", want, activities)
}
