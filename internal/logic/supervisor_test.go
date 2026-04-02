package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	repository "MattiasHognas/Kennel/internal/data"
	"context"
	"os"
	"os/exec"
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
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "branch-merger", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)

	var (
		topics               []string
		branchSetupMessages  []string
		branchMergerMessages []string
		plannerMessages      []string
		frontendMessages     []string
		plannerStepCount     int
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
		case "branch-merger":
			return &stubACPClient{
				messages: &branchMergerMessages,
				response: "Merged stream branch into main\n\n```json\n{\"summary\":\"Merged stream branch into main\",\"branch_name\":\"main\",\"merge_status\":\"merged\",\"completion_status\":\"full\"}\n```",
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

	if got := strings.Join(topics, ","); got != "planner,branch-setup,planner,frontend-developer,planner,branch-merger" {
		t.Fatalf("ACP topics = %v, want planner,branch-setup,planner,frontend-developer,planner,branch-merger", topics)
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
	if len(branchMergerMessages) != 1 {
		t.Fatalf("branch merger messages = %v, want one", branchMergerMessages)
	}
	if !strings.Contains(branchMergerMessages[0], "Source branch: test-project/run/stream-0") {
		t.Fatalf("branch merger prompt = %q, want source branch", branchMergerMessages[0])
	}
	if !strings.Contains(branchMergerMessages[0], "Execution history:\n1. [branch-setup] Initialize branch context for this stream. => Branch ready\n2. [frontend-developer] Build UI => UI implemented") {
		t.Fatalf("branch merger prompt = %q, want execution history", branchMergerMessages[0])
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "planner", "completed")
	assertAgentState(t, stored.Agents, "branch-setup", "completed")
	assertAgentState(t, stored.Agents, "frontend-developer", "completed")
	assertAgentState(t, stored.Agents, "branch-merger", "completed")
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

func TestRunPlanUsesSeparateWorktreesPerStream(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "branch-merger", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var (
		mu                 sync.Mutex
		workplacesByStream = map[int]map[string]struct{}{}
	)

	recordStreamWorkplace := func(streamID int, workplace string) {
		t.Helper()

		if workplace == project.Workplace {
			t.Fatalf("stream %d workplace = project root %q, want separate worktree", streamID, workplace)
		}
		if _, err := os.Stat(filepath.Join(workplace, ".git")); err != nil {
			t.Fatalf("stream %d workplace %q missing git metadata: %v", streamID, workplace, err)
		}

		mu.Lock()
		defer mu.Unlock()
		if workplacesByStream[streamID] == nil {
			workplacesByStream[streamID] = map[string]struct{}{}
		}
		workplacesByStream[streamID][workplace] = struct{}{}
	}

	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					if workplace != project.Workplace {
						t.Fatalf("initial planner workplace = %q, want %q", workplace, project.Workplace)
					}
					return `{"streams":[{"task":"Build UI stream"},{"task":"Build API stream"}]}`, nil
				}
				streamID := promptStreamID(t, msg)
				recordStreamWorkplace(streamID, workplace)
				if strings.Contains(msg, "Build UI stream") && !strings.Contains(msg, "Last agent: frontend-developer") {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build UI"}}`, nil
				}
				if strings.Contains(msg, "Build API stream") && !strings.Contains(msg, "Last agent: frontend-developer") {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build API"}}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				streamID := promptStreamID(t, msg)
				recordStreamWorkplace(streamID, workplace)
				return "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream\",\"completion_status\":\"full\"}\n```", nil
			}}, nil
		case "frontend-developer":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				streamID := promptStreamID(t, msg)
				recordStreamWorkplace(streamID, workplace)
				return "Implemented work\n\n```json\n{\"summary\":\"Implemented work\",\"completion_status\":\"full\"}\n```", nil
			}}, nil
		case "branch-merger":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				streamID := promptStreamID(t, msg)
				recordStreamWorkplace(streamID, workplace)
				return "Merged stream branch into main\n\n```json\n{\"summary\":\"Merged stream branch into main\",\"branch_name\":\"main\",\"merge_status\":\"merged\",\"completion_status\":\"full\"}\n```", nil
			}}, nil
		default:
			t.Fatalf("unexpected ACP topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if len(workplacesByStream) != 2 {
		t.Fatalf("stream workplaces = %#v, want 2 streams", workplacesByStream)
	}

	var distinct []string
	for streamID, streamWorkplaces := range workplacesByStream {
		if len(streamWorkplaces) != 1 {
			t.Fatalf("stream %d workplaces = %#v, want exactly one worktree path", streamID, streamWorkplaces)
		}
		for workplace := range streamWorkplaces {
			distinct = append(distinct, workplace)
			if _, err := os.Stat(workplace); !os.IsNotExist(err) {
				t.Fatalf("stream %d worktree %q still exists after cleanup; stat err=%v", streamID, workplace, err)
			}
		}
	}
	if distinct[0] == distinct[1] {
		t.Fatalf("distinct stream worktrees = %v, want separate paths", distinct)
	}
}

func TestRunPlanSurfacesMissingBranchMergerDefinition(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var topics []string
	var plannerStepCount int
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build the UI stream"}]}`, nil
				}
				plannerStepCount++
				if plannerStepCount == 1 {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build UI"}}`, nil
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

	if strings.Join(topics, ",") != "planner,branch-setup,planner,frontend-developer,planner" {
		t.Fatalf("ACP topics = %v, want no branch-merger execution", topics)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}
	assertAgentState(t, stored.Agents, "frontend-developer", "completed")
	for _, agent := range stored.Agents {
		if agent.Name == "branch-merger" {
			t.Fatalf("unexpected branch-merger agent record: %+v", agent)
		}
	}
}

func TestRunPlanToleratesBranchMergerFailure(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "branch-merger", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	var topics []string
	var plannerStepCount int
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build the UI stream"}]}`, nil
				}
				plannerStepCount++
				if plannerStepCount == 1 {
					return `{"completed":false,"reason":"Need implementation","next_task":{"agent":"frontend-developer","task":"Build UI"}}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream-0\",\"completion_status\":\"full\"}\n```"}, nil
		case "frontend-developer":
			return &stubACPClient{response: "Implemented UI\n\n```json\n{\"summary\":\"UI implemented\",\"completion_status\":\"full\"}\n```"}, nil
		case "branch-merger":
			return &stubACPClient{err: context.DeadlineExceeded}, nil
		default:
			t.Fatalf("unexpected topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if strings.Join(topics, ",") != "planner,branch-setup,planner,frontend-developer,planner,branch-merger" {
		t.Fatalf("ACP topics = %v, want branch-merger execution", topics)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}

	assertAgentState(t, stored.Agents, "branch-merger", "failed")
}

func TestRunPlanReusesCompletedBranchMergerOnResume(t *testing.T) {
	repo := newTestRepository(t)
	project := newTestProject(t, repo)
	agentsRoot := newTestAgentsRoot(t, "branch-setup", "branch-merger", "frontend-developer")
	eb := data.NewEventBus()
	tracking := &trackingRepo{SQLiteRepository: repo}
	super := NewSupervisor(tracking, eb, agentsRoot, project.ID, project.Name, project.Workplace)
	super.Logger = nil

	existingMerger, err := repo.AddAgentToStream(context.Background(), project.ID, 0, "branch-merger", branchMergerInstanceKey(0), "stream-0")
	if err != nil {
		t.Fatalf("AddAgentToStream returned error: %v", err)
	}
	existingOutput := "Merged previously\n\n```json\n{\"summary\":\"Merged previously\",\"branch_name\":\"main\",\"merge_status\":\"merged\",\"completion_status\":\"full\"}\n```"
	if err := repo.UpdateAgentOutput(context.Background(), existingMerger.ID, existingOutput); err != nil {
		t.Fatalf("UpdateAgentOutput returned error: %v", err)
	}
	if err := repo.UpdateAgentState(context.Background(), existingMerger.ID, "completed"); err != nil {
		t.Fatalf("UpdateAgentState returned error: %v", err)
	}

	var topics []string
	super.AcpFactory = func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
		topics = append(topics, topic)
		switch topic {
		case "planner":
			return &stubACPClient{responder: func(ctx context.Context, msg string) (string, error) {
				if strings.Contains(msg, `Create a JSON object containing a "streams" array.`) {
					return `{"streams":[{"task":"Build the UI stream"}]}`, nil
				}
				return `{"completed":true,"reason":"Done"}`, nil
			}}, nil
		case "branch-setup":
			return &stubACPClient{response: "Branch ready\n\n```json\n{\"summary\":\"Branch ready\",\"branch_name\":\"stream-0\",\"completion_status\":\"full\"}\n```"}, nil
		case "branch-merger":
			t.Fatal("branch-merger should reuse completed output on resume")
			return nil, nil
		default:
			t.Fatalf("unexpected topic %q", topic)
			return nil, nil
		}
	}

	if err := super.RunPlan(context.Background(), "ship it", []string{"frontend-developer"}); err != nil {
		t.Fatalf("RunPlan returned error: %v", err)
	}

	if strings.Join(topics, ",") != "planner,branch-setup,planner" {
		t.Fatalf("ACP topics = %v, want planner,branch-setup,planner with no branch-merger rerun", topics)
	}

	stored, err := repo.ReadProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ReadProject returned error: %v", err)
	}
	assertAgentState(t, stored.Agents, "branch-merger", "completed", existingOutput)
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

	workplace := t.TempDir()
	initGitRepository(t, workplace)

	project, err := repo.CreateProject(context.Background(), "test-project", workplace, "build something")
	if err != nil {
		t.Fatalf("CreateProject returned error: %v", err)
	}

	return project
}

func initGitRepository(t *testing.T, dir string) {
	t.Helper()

	runGitCommand(t, dir, "init", "-b", "main")
	runGitCommand(t, dir, "config", "user.name", "Test User")
	runGitCommand(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	runGitCommand(t, dir, "add", "README.md")
	runGitCommand(t, dir, "commit", "-m", "initial commit")
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func promptStreamID(t *testing.T, msg string) int {
	t.Helper()

	switch {
	case strings.Contains(msg, "Stream id: 0"):
		return 0
	case strings.Contains(msg, "Stream id: 1"):
		return 1
	default:
		t.Fatalf("prompt %q missing stream id", msg)
		return -1
	}
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
