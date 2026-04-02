package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	repository "MattiasHognas/Kennel/internal/data"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type stubACPClient struct {
	response  string
	messages  *[]string
	err       error
	responder func(context.Context, string) (string, error)
}

func (c *stubACPClient) Prompt(ctx context.Context, msg string) (string, error) {
	if c.messages != nil {
		*c.messages = append(*c.messages, msg)
	}
	if c.responder != nil {
		return c.responder(ctx, msg)
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

func TestRunPlanIterativelyExecutesSingleNextSteps(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	var (
		topics              []string
		branchSetupMessages []string
		plannerMessages     []string
		frontendMessages    []string
		plannerStepCount    int
	)

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)

		switch topic {
		case "planner":
			return &stubACPClient{
				messages: &plannerMessages,
				responder: func(ctx context.Context, msg string) (string, error) {
					if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
						return `{"streams":[{"task":"Build the UI stream"}]}`, nil
					}
					plannerStepCount++
					if plannerStepCount == 1 {
						if !strings.Contains(msg, "Main task: Build the UI stream") {
							t.Fatalf("planner prompt = %q, want main task", msg)
						}
						if !strings.Contains(msg, "Last agent: branch-setup") {
							t.Fatalf("planner prompt = %q, want branch setup context", msg)
						}
						return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build UI"}}`, nil
					}
					if !strings.Contains(msg, "Last agent: frontend-developer") {
						t.Fatalf("planner prompt = %q, want frontend context", msg)
					}
					if !strings.Contains(msg, "Last agent summary: UI implemented") {
						t.Fatalf("planner prompt = %q, want frontend summary", msg)
					}
					return `{"completed":true,"reason":"UI stream done"}`, nil
				},
			}, nil
		case "branch-setup":
			return &stubACPClient{
				messages: &branchSetupMessages,
				response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"test-project/run/stream-0\",\"completion_status\":\"full\"}\n```",
			}, nil
		case "frontend-developer":
			return &stubACPClient{
				messages: &frontendMessages,
				response: "Implemented UI\n\n```json\n{\"summary\":\"UI implemented\",\"files_modified\":[\"ui.go\"],\"completion_status\":\"full\"}\n```",
			}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}
	super.Logger = nil

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if got := strings.Join(topics, ","); got != "planner,branch-setup,planner,frontend-developer,planner" {
		t.Fatalf("ACP topics = %v, want planner,branch-setup,planner,frontend-developer,planner", topics)
	}
	if len(branchSetupMessages) != 1 {
		t.Fatalf("branch setup messages = %v, want one", branchSetupMessages)
	}
	if !strings.Contains(branchSetupMessages[0], "Stream id: 0") || !strings.Contains(branchSetupMessages[0], "Main task: Build the UI stream") {
		t.Fatalf("branch setup prompt = %q, want stream context", branchSetupMessages[0])
	}
	if len(frontendMessages) != 1 {
		t.Fatalf("frontend messages = %v, want one", frontendMessages)
	}
	if !strings.Contains(frontendMessages[0], "Main task: Build the UI stream") {
		t.Fatalf("frontend prompt = %q, want main task context", frontendMessages[0])
	}
	if !strings.Contains(frontendMessages[0], "Last agent: branch-setup") {
		t.Fatalf("frontend prompt = %q, want branch setup context", frontendMessages[0])
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "planner", "completed")
	assertAgentState(t, stored.Agents, "branch-setup", "completed")
	assertAgentState(t, stored.Agents, "frontend-developer", "completed")
}

func TestRunPlanResolvesPlannerAgentNameVariants(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var plannerStepCount int
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build the UI stream"}]}`, nil
				}
				plannerStepCount++
				if plannerStepCount == 1 {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"Frontend Developer","task":"Build UI"}}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream-0\",\"completion_status\":\"full\"}\n```"}, nil
		case "frontend-developer":
			return &stubACPClient{response: "Implemented UI\n\n```json\n{\"summary\":\"UI implemented\",\"completion_status\":\"full\"}\n```"}, nil
		default:
			t.Fatalf("unexpected topic %q", topic)
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
	assertAgentState(t, stored.Agents, "frontend-developer", "completed")
}

func TestRunPlanRejectsUnknownPlannerDecisionAgent(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build the stream"}]}`, nil
				}
				return `{"completed":false,"reason":"Do work","next_task":{"agent":"unknown-agent","task":"Do work"}}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream-0\",\"completion_status\":\"full\"}\n```"}, nil
		default:
			t.Fatalf("unexpected topic %q", topic)
			return nil, nil
		}
	}

	err := super.RunPlan(context.Background(), "ship it", nil)
	if err == nil {
		t.Fatal("RunPlan returned nil error, want invalid-agent failure")
	}
	if !strings.Contains(err.Error(), "agent unknown-agent not found") {
		t.Fatalf("RunPlan error = %v, want unknown agent failure", err)
	}
}

func TestRunPlanOmitsPreviousOutputWhenAgentDisablesPromptContext(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "tester")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	testerDir := filepath.Join(agentsRoot, "agents", "tester")
	if err := os.WriteFile(filepath.Join(testerDir, "agent.json"), []byte(`{
		"promptContext": {"previousOutput": false},
		"permissions": {"git": {"status": false, "diff": false, "history": false}}
	}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	var testerMessages []string
	var plannerStepCount int
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Run focused tests"}]}`, nil
				}
				plannerStepCount++
				if plannerStepCount == 1 {
					return `{"completed":false,"reason":"Need tests","next_task":{"agent":"tester","task":"Run focused tests"}}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream-0\",\"completion_status\":\"full\"}\n```"}, nil
		case "tester":
			if definition.PromptContext.PreviousOutput {
				t.Fatalf("tester prompt context should be disabled by agent config")
			}
			return &stubACPClient{
				messages: &testerMessages,
				response: "Tests passed\n\n```json\n{\"summary\":\"Tests passed\",\"tests_run\":{\"passed\":1,\"failed\":0,\"skipped\":0},\"completion_status\":\"full\"}\n```",
			}, nil
		default:
			t.Fatalf("unexpected topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"tester"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if len(testerMessages) != 1 {
		t.Fatalf("tester messages = %v, want exactly one prompt", testerMessages)
	}
	if !strings.HasPrefix(testerMessages[0], "Task: Run focused tests") {
		t.Fatalf("tester prompt = %q, want task prefix", testerMessages[0])
	}
	if strings.Contains(testerMessages[0], "Previous context/output:") {
		t.Fatalf("tester prompt = %q, want no previous output", testerMessages[0])
	}
}

func TestRunPlanStopsWhenWorkplaceIsNotGitRoot(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var topics []string
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)
		switch topic {
		case "planner":
			return &stubACPClient{response: `{"streams":[{"task":"Build the UI stream"}]}`}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}
	super.GitRoot = func(ctx context.Context, workplace string) (string, error) {
		return filepath.Join(workplace, ".."), nil
	}

	err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"})
	if err == nil {
		t.Fatal("RunPlan returned nil error, want workplace validation failure")
	}
	if !strings.Contains(err.Error(), "configure the repository root as the workplace") {
		t.Fatalf("RunPlan error = %v, want workplace validation failure", err)
	}
	if strings.Join(topics, ",") != "planner" {
		t.Fatalf("ACP topics = %v, want only planner before branch setup", topics)
	}
}

func TestRunPlanAllowsConcurrentInstancesOfSameAgentAcrossStreams(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var (
		concurrent    int32
		maxConcurrent int32
		startCount    int32
		allStarted    = make(chan struct{})
		closeOnce     sync.Once
	)

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build UI stream"},{"task":"Build tests stream"}]}`, nil
				}
				if strings.Contains(msg, "Build UI stream") && !strings.Contains(msg, "Last agent: frontend-developer") {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build UI"}}`, nil
				}
				if strings.Contains(msg, "Build tests stream") && !strings.Contains(msg, "Last agent: frontend-developer") {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build tests"}}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream\",\"completion_status\":\"full\"}\n```"}, nil
		case "frontend-developer":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				current := atomic.AddInt32(&concurrent, 1)
				for {
					observed := atomic.LoadInt32(&maxConcurrent)
					if current <= observed || atomic.CompareAndSwapInt32(&maxConcurrent, observed, current) {
						break
					}
				}
				if atomic.AddInt32(&startCount, 1) >= 2 {
					closeOnce.Do(func() { close(allStarted) })
				}
				<-allStarted
				defer atomic.AddInt32(&concurrent, -1)
				return "Implemented work\n\n```json\n{\"summary\":\"Implemented work\",\"completion_status\":\"full\"}\n```", nil
			}}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}
	if maxConcurrent < 2 {
		t.Fatalf("frontend-developer max concurrent instances = %d, want at least 2", maxConcurrent)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}
	completedCount := 0
	for _, agent := range stored.Agents {
		if agent.Name == "frontend-developer" && agent.State == "completed" {
			completedCount++
		}
	}
	if completedCount != 2 {
		t.Fatalf("completed frontend-developer agent records = %d, want 2", completedCount)
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
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "default.json"), []byte(`{"binary":"copilot","args":["--acp"]}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	for _, agentName := range agentNames {
		agentDir := filepath.Join(agentsDir, agentName)
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatalf("MkdirAll returned error: %v", err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte("# test instructions\n"), 0o644); err != nil {
			t.Fatalf("WriteFile returned error: %v", err)
		}
	}

	return root
}

func assertAgentState(t *testing.T, agents []repository.Agent, name, state string, output ...string) {
	t.Helper()

	for _, agent := range agents {
		if agent.Name != name {
			continue
		}
		if agent.State != state {
			t.Fatalf("agent %q state = %q, want %q", name, agent.State, state)
		}
		if len(output) > 0 && agent.Output != output[0] {
			t.Fatalf("agent %q output = %q, want %q", name, agent.Output, output[0])
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
