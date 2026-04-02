package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
)

type PlanTask struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

type TaskStream []PlanTask

type Plan struct {
	Streams []TaskStream `json:"streams"`
}

type StreamDefinition struct {
	Task string `json:"task"`
}

type StreamPlan struct {
	Streams []StreamDefinition `json:"streams"`
}

type PlanDecision struct {
	Completed bool      `json:"completed"`
	NextTask  *PlanTask `json:"next_task,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

type executionTask struct {
	PlanTask
	InstanceKey string
	ForceRun    bool
}

type executionStream []executionTask

type executionState struct {
	agentMap           map[string]data.AgentDefinition
	agentStateMap      map[string]data.Agent
	agentStateMu       *sync.RWMutex
	completedBeforeRun map[string]struct{}
	executedAgents     map[string]struct{}
	planningAgents     []string
	stepCounter        *int64
	publishedPlan      *Plan
	planMu             *sync.Mutex
	agentLocks         map[string]*sync.Mutex
	agentLocksMu       *sync.Mutex
}

type StreamContext struct {
	StreamID         int
	MainTask         string
	BranchName       string
	ExecutionHistory []ExecutedStep
	PlannerOutputs   []string
}

type ExecutedStep struct {
	Agent   string
	Task    string
	Output  string
	Summary string
}

func ParsePlanOutput(output string) (Plan, error) {
	return parsePlanJSON(extractPlanJSON(output))
}

const (
	branchSetupAgentName    = "branch-setup"
	codeReviewerAgentName   = "code-reviewer"
	plannerAgentName        = "planner"
	generalPurposeAgentName = "general-purpose"
	testerAgentName         = "tester"
)

type Repository interface {
	AddAgentToProject(ctx context.Context, projectID int64, name, instanceKey string) (data.Agent, error)
	AddAgentToStream(ctx context.Context, projectID int64, streamID int, name, instanceKey, branchName string) (data.Agent, error)
	CheckpointSupervisorRun(ctx context.Context, projectID int64, stepIndex int, status, data string) error
	NewActivity(ctx context.Context, projectID int64, agentID sql.NullInt64, text string) (data.Activity, error)
	ReadProject(ctx context.Context, projectID int64) (data.Project, error)
	UpdateAgentOutput(ctx context.Context, agentID int64, output string) error
	UpdateAgentState(ctx context.Context, agentID int64, state string) error
	UpdateProjectState(ctx context.Context, projectID int64, state string) error
}

type ACPClient interface {
	Prompt(ctx context.Context, msg string) (string, error)
	Close() error
}

type ACPFactory func(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error)

type GitRootResolver func(ctx context.Context, workplace string) (string, error)

func DefaultACPFactory(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
	return workers.NewWrapper(ctx, definition, eb, workplace, topic)
}

func DefaultGitRootResolver(ctx context.Context, workplace string) (string, error) {
	resolvedWorkplace, err := filepath.Abs(strings.TrimSpace(workplace))
	if err != nil {
		return "", fmt.Errorf("resolve workplace path: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "-C", resolvedWorkplace, "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", nil
	}

	gitRoot := strings.TrimSpace(string(out))
	if gitRoot == "" {
		return "", fmt.Errorf("git returned an empty repository root for %s", resolvedWorkplace)
	}

	return filepath.Clean(gitRoot), nil
}

type Supervisor struct {
	Repo        Repository
	EventBus    *data.EventBus
	AgentsDir   string
	ProjectID   int64
	ProjectName string
	Workplace   string
	AcpFactory  ACPFactory
	GitRoot     GitRootResolver
	Logger      *data.ProjectLogger
}

func NewSupervisor(repo Repository, eb *data.EventBus, agentsDir string, projectID int64, projectName string, workplace string) *Supervisor {
	return &Supervisor{
		Repo:        repo,
		EventBus:    eb,
		AgentsDir:   agentsDir,
		ProjectID:   projectID,
		ProjectName: projectName,
		Workplace:   workplace,
		AcpFactory:  DefaultACPFactory,
		GitRoot:     DefaultGitRootResolver,
		Logger:      data.NewProjectLogger(agentsDir, projectID, projectName),
	}
}

func (s *Supervisor) RunPlan(ctx context.Context, instructions string, configuredAgents []string) error {
	if s.Logger != nil {
		s.Logger.LogProject("PROJECT_START", fmt.Sprintf("workplace=%s\nconfiguredAgents=%s", s.Workplace, strings.Join(configuredAgents, ", ")))
	}

	proj, err := s.Repo.ReadProject(ctx, s.ProjectID)
	if err != nil {
		return s.failStop(ctx, 0, "read_project_failed", err)
	}

	agentStateMap := make(map[string]data.Agent)
	completedBeforeRun := make(map[string]struct{})
	var agentStateMu sync.RWMutex
	for _, a := range proj.Agents {
		key := a.InstanceKey
		if key == "" {
			key = a.Name
		}
		agentStateMap[key] = a
		if a.State == "completed" {
			completedBeforeRun[key] = struct{}{}
		}
	}

	defs, err := data.LoadAgentDefinitions(s.AgentsDir)
	if err != nil {
		return s.failStop(ctx, -1, "discovery_failed", err)
	}

	agentMap := make(map[string]data.AgentDefinition)
	for _, d := range defs {
		agentMap[d.Name] = d
	}

	registerBuiltinAgents(agentMap)

	planningAgents := availablePlanningAgents(agentMap, configuredAgents)
	streamPlan, plannerErr := s.runInitialPlanner(ctx, instructions, planningAgents, agentMap, agentStateMap, completedBeforeRun, &agentStateMu)
	if plannerErr != nil {
		return plannerErr
	}

	publishedPlan := emptyPublishedPlan(streamPlan)
	s.publishPlan(publishedPlan)

	stepCounter := int64(0)
	planMu := &sync.Mutex{}
	execState := &executionState{
		agentMap:           agentMap,
		agentStateMap:      agentStateMap,
		agentStateMu:       &agentStateMu,
		completedBeforeRun: completedBeforeRun,
		executedAgents:     make(map[string]struct{}),
		planningAgents:     planningAgents,
		stepCounter:        &stepCounter,
		publishedPlan:      &publishedPlan,
		planMu:             planMu,
		agentLocks:         make(map[string]*sync.Mutex),
		agentLocksMu:       &sync.Mutex{},
	}

	if err := s.executeStreamPlan(ctx, streamPlan, execState); err != nil {
		return s.failStop(ctx, -1, "stream_execution_failed", err)
	}

	if s.Logger != nil {
		s.Logger.LogProject("PROJECT_COMPLETE", "supervisor run completed")
	}

	return nil
}

func (s *Supervisor) runInitialPlanner(ctx context.Context, instructions string, planningAgents []string, agentMap map[string]data.AgentDefinition, agentStateMap map[string]data.Agent, completedBeforeRun map[string]struct{}, agentStateMu *sync.RWMutex) (StreamPlan, error) {
	plannerTask := PlanTask{Agent: plannerAgentName, Task: "Create parallel work streams for the project instructions."}
	plannerDef, ok := agentMap[plannerTask.Agent]
	if !ok {
		return StreamPlan{}, s.failStop(ctx, 0, "planning_validation_failed", fmt.Errorf("planner agent definition missing"))
	}

	plannerRec, found := agentStateMap[plannerTask.Agent]
	if !found {
		var err error
		plannerRec, err = s.Repo.AddAgentToProject(ctx, s.ProjectID, plannerTask.Agent, plannerTask.Agent)
		if err != nil {
			return StreamPlan{}, s.failStop(ctx, 0, "planning_validation_failed", fmt.Errorf("add agent %s: %w", plannerTask.Agent, err))
		}
		agentStateMap[plannerTask.Agent] = plannerRec
		s.logAgentCreated(plannerRec.Name)
		s.publishSync(plannerRec, plannerRec.State, "")
	}

	prompt := buildInitialPlannerPrompt(instructions, planningAgents)
	output, err := s.runPromptedAgent(ctx, plannerRec, plannerTask.Agent, plannerTask.Task, prompt, plannerDef, nil)
	if err != nil {
		return StreamPlan{}, s.failAgentAndStop(ctx, plannerRec, 0, "planning_failed", err)
	}

	agentStateMu.Lock()
	agentStateMap[plannerTask.Agent] = refreshAgentCompletion(agentStateMap[plannerTask.Agent], output)
	completedBeforeRun[plannerTask.Agent] = struct{}{}
	agentStateMu.Unlock()

	streamPlan, err := parseStreamPlanJSON(extractPlanJSON(output))
	if err != nil {
		return StreamPlan{}, s.failAgentAndStop(ctx, plannerRec, 0, "planning_json_parse_failed", err)
	}
	if err := normalizeStreamPlan(&streamPlan); err != nil {
		return StreamPlan{}, s.failAgentAndStop(ctx, plannerRec, 0, "planning_validation_failed", err)
	}

	return streamPlan, nil
}

func (s *Supervisor) executeStreamPlan(ctx context.Context, streamPlan StreamPlan, state *executionState) error {
	g, gCtx := errgroup.WithContext(ctx)
	for streamIndex, streamDef := range streamPlan.Streams {
		streamIndex := streamIndex
		streamDef := streamDef
		g.Go(func() error {
			return s.executeStream(gCtx, streamIndex, streamDef, state)
		})
	}
	return g.Wait()
}

func (s *Supervisor) executeStream(ctx context.Context, streamIndex int, streamDef StreamDefinition, state *executionState) error {
	if err := s.validateWorkplaceGitRoot(ctx); err != nil {
		branchRec, recErr := s.ensureStreamAgentRecord(ctx, branchSetupAgentName, branchSetupInstanceKey(streamIndex), streamIndex, "", state)
		if recErr == nil {
			return s.failAgentAndStop(ctx, branchRec, streamIndex+1, "workplace_validation_failed", err)
		}
		return err
	}

	branchName, setupOut, err := s.runBranchSetupForStream(ctx, streamIndex, streamDef.Task, state)
	if err != nil {
		return err
	}

	streamCtx := &StreamContext{
		StreamID:   streamIndex,
		MainTask:   streamDef.Task,
		BranchName: branchName,
	}

	setupMeta, cleanedSetupOut, parseErr := ParseAgentOutput(setupOut)
	if parseErr == nil {
		if strings.TrimSpace(setupMeta.BranchName) != "" {
			streamCtx.BranchName = setupMeta.BranchName
		}
		streamCtx.ExecutionHistory = append(streamCtx.ExecutionHistory, ExecutedStep{
			Agent:   branchSetupAgentName,
			Task:    "Initialize branch context for this stream.",
			Output:  cleanedSetupOut,
			Summary: setupMeta.Summary,
		})
	} else if strings.TrimSpace(setupOut) != "" {
		streamCtx.ExecutionHistory = append(streamCtx.ExecutionHistory, ExecutedStep{
			Agent:   branchSetupAgentName,
			Task:    "Initialize branch context for this stream.",
			Output:  strings.TrimSpace(setupOut),
			Summary: summarizeOutput(setupOut),
		})
	}

	const maxPlannerIterations = 50
	for plannerStep := 0; plannerStep < maxPlannerIterations; plannerStep++ {
		decision, err := s.runPlannerDecision(ctx, streamCtx, plannerStep, state)
		if err != nil {
			return err
		}
		if decision.Completed {
			return nil
		}
		if decision.NextTask == nil {
			return fmt.Errorf("planner stream %d returned no next task", streamIndex)
		}
		tempPlan := Plan{Streams: []TaskStream{{*decision.NextTask}}}
		if err := resolvePlanAgents(&tempPlan, state.agentMap); err != nil {
			return err
		}

		plannedTask := tempPlan.Streams[0][0]
		if err := s.publishPlannedStep(streamIndex, plannedTask, state); err != nil {
			return err
		}

		stepIndex := countPlannedWorkSteps(streamCtx)
		instanceKey := planStepInstanceKey(streamIndex, stepIndex)
		if _, err := s.ensureStreamAgentRecord(ctx, plannedTask.Agent, instanceKey, streamIndex, streamCtx.BranchName, state); err != nil {
			return err
		}

		taskPrompt := BuildPlannerContext(streamCtx.MainTask, latestExecutedStep(streamCtx), streamCtx)
		out, err := s.executeTask(ctx, executionTask{
			PlanTask:    plannedTask,
			InstanceKey: instanceKey,
		}, taskPrompt, state)
		if err != nil {
			return err
		}

		meta, cleanedOutput, parseErr := ParseAgentOutput(out)
		if parseErr != nil {
			meta = AgentOutputMeta{
				Summary:          summarizeOutput(out),
				CompletionStatus: "partial",
			}
			cleanedOutput = strings.TrimSpace(out)
		}
		streamCtx.ExecutionHistory = append(streamCtx.ExecutionHistory, ExecutedStep{
			Agent:   plannedTask.Agent,
			Task:    plannedTask.Task,
			Output:  cleanedOutput,
			Summary: meta.Summary,
		})
	}

	return fmt.Errorf("stream %d exceeded planner iteration limit", streamIndex)
}

func normalizePlan(plan *Plan) error {
	for streamIdx := range plan.Streams {
		for taskIdx := range plan.Streams[streamIdx] {
			plan.Streams[streamIdx][taskIdx].Agent = strings.TrimSpace(plan.Streams[streamIdx][taskIdx].Agent)
			plan.Streams[streamIdx][taskIdx].Task = strings.TrimSpace(plan.Streams[streamIdx][taskIdx].Task)

			if plan.Streams[streamIdx][taskIdx].Agent == "" {
				return fmt.Errorf("plan stream %d task %d has empty agent", streamIdx, taskIdx)
			}
			if plan.Streams[streamIdx][taskIdx].Task == "" {
				return fmt.Errorf("plan stream %d task %d has empty task", streamIdx, taskIdx)
			}
		}
	}

	return nil
}

func resolvePlanAgents(plan *Plan, agentMap map[string]data.AgentDefinition) error {
	aliases := make(map[string]string, len(agentMap))
	for name := range agentMap {
		canonicalName := CanonicalAgentName(name)
		if canonicalName == "" {
			continue
		}
		if existing, found := aliases[canonicalName]; found && existing != name {
			return fmt.Errorf("agent name alias conflict between %s and %s", existing, name)
		}
		aliases[canonicalName] = name
	}

	for streamIdx := range plan.Streams {
		for taskIdx := range plan.Streams[streamIdx] {
			resolvedName, ok := aliases[CanonicalAgentName(plan.Streams[streamIdx][taskIdx].Agent)]
			if !ok {
				return fmt.Errorf("agent %s not found", plan.Streams[streamIdx][taskIdx].Agent)
			}
			plan.Streams[streamIdx][taskIdx].Agent = resolvedName
		}
	}

	return nil
}

func CanonicalAgentName(name string) string {
	replacer := strings.NewReplacer("-", " ", "_", " ")
	parts := strings.Fields(replacer.Replace(strings.ToLower(strings.TrimSpace(name))))
	return strings.Join(parts, "-")
}

func registerBuiltinAgents(agentMap map[string]data.AgentDefinition) {
	if _, ok := agentMap[plannerAgentName]; !ok {
		agentMap[plannerAgentName] = builtinAgentDefinition(plannerAgentName)
	}
	if _, ok := agentMap[generalPurposeAgentName]; !ok {
		agentMap[generalPurposeAgentName] = builtinAgentDefinition(generalPurposeAgentName)
	}
}

func builtinAgentDefinition(name string) data.AgentDefinition {
	return data.AgentDefinition{
		Name:         name,
		LaunchConfig: data.LaunchConfig{Binary: "copilot", Args: []string{"--acp"}},
	}
}

func availablePlanningAgents(agentMap map[string]data.AgentDefinition, configuredAgents []string) []string {
	selected := make([]string, 0, len(agentMap))
	seen := make(map[string]struct{}, len(agentMap))

	add := func(name string) {
		if name == "" || name == plannerAgentName || name == branchSetupAgentName || name == generalPurposeAgentName {
			return
		}
		if _, ok := agentMap[name]; !ok {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		selected = append(selected, name)
	}

	for _, name := range configuredAgents {
		canonicalName := CanonicalAgentName(name)
		for agentName := range agentMap {
			if CanonicalAgentName(agentName) == canonicalName {
				add(agentName)
				break
			}
		}
	}

	for name := range agentMap {
		add(name)
	}

	sort.Strings(selected)
	return selected
}

func buildInitialPlannerPrompt(instructions string, planningAgents []string) string {
	return fmt.Sprintf(`Create a JSON object containing a "streams" array.

Each item in "streams" must be an object with a single "task" string describing one independent high-level work stream.

Instructions: %s

Available agents for later detailed steps: %s

Keep the work streams high level. Do not assign an agent yet. The planner will decide the next single step for each stream later.
Return only JSON or a Markdown JSON block.`, strings.TrimSpace(instructions), strings.Join(planningAgents, ", "))
}

func buildPlannerDecisionPrompt(streamCtx *StreamContext, planningAgents []string) string {
	return strings.Join([]string{
		BuildPlannerContext(streamCtx.MainTask, latestExecutedStep(streamCtx), streamCtx),
		fmt.Sprintf("Available agents for the next step: %s", strings.Join(planningAgents, ", ")),
		`Return a JSON object with either:
{"completed": true, "reason": "why the stream is done"}

or

{"completed": false, "reason": "why this is the next step", "next_task": {"agent": "agent-name", "task": "single concrete next step"}}`,
		"Plan only the very next step.",
	}, "\n\n")
}

func emptyPublishedPlan(streamPlan StreamPlan) Plan {
	plan := Plan{Streams: make([]TaskStream, len(streamPlan.Streams))}
	for i := range plan.Streams {
		plan.Streams[i] = TaskStream{}
	}
	return plan
}

func normalizeStreamPlan(plan *StreamPlan) error {
	for index := range plan.Streams {
		plan.Streams[index].Task = strings.TrimSpace(plan.Streams[index].Task)
		if plan.Streams[index].Task == "" {
			return fmt.Errorf("stream %d has empty task", index)
		}
	}
	return nil
}

func parseStreamPlanJSON(rawJSON string) (StreamPlan, error) {
	var plan StreamPlan
	if err := json.Unmarshal([]byte(rawJSON), &plan); err != nil {
		return StreamPlan{}, err
	}
	return plan, nil
}

func parsePlanDecision(output string) (PlanDecision, error) {
	var decision PlanDecision
	if err := json.Unmarshal([]byte(extractPlanJSON(output)), &decision); err != nil {
		return PlanDecision{}, err
	}
	if !decision.Completed {
		if decision.NextTask == nil {
			return PlanDecision{}, fmt.Errorf("planner decision missing next task")
		}
		decision.NextTask.Agent = strings.TrimSpace(decision.NextTask.Agent)
		decision.NextTask.Task = strings.TrimSpace(decision.NextTask.Task)
		if decision.NextTask.Agent == "" || decision.NextTask.Task == "" {
			return PlanDecision{}, fmt.Errorf("planner decision next task must include agent and task")
		}
	}
	return decision, nil
}

func refreshAgentCompletion(agent data.Agent, output string) data.Agent {
	agent.Output = output
	agent.State = "completed"
	return agent
}

func latestExecutedStep(streamCtx *StreamContext) *ExecutedStep {
	if streamCtx == nil || len(streamCtx.ExecutionHistory) == 0 {
		return nil
	}
	last := streamCtx.ExecutionHistory[len(streamCtx.ExecutionHistory)-1]
	return &last
}

func countPlannedWorkSteps(streamCtx *StreamContext) int {
	if streamCtx == nil {
		return 0
	}
	count := 0
	for _, step := range streamCtx.ExecutionHistory {
		if CanonicalAgentName(step.Agent) == branchSetupAgentName {
			continue
		}
		count++
	}
	return count
}

func branchSetupInstanceKey(streamIndex int) string {
	return fmt.Sprintf("branch-setup:s%d", streamIndex)
}

func plannerStepInstanceKey(streamIndex, plannerStep int) string {
	return fmt.Sprintf("planner:s%d:p%d", streamIndex, plannerStep)
}

func (s *Supervisor) ensureStreamAgentRecord(ctx context.Context, agentName, instanceKey string, streamIndex int, branchName string, state *executionState) (data.Agent, error) {
	state.agentStateMu.RLock()
	agentRec, found := state.agentStateMap[instanceKey]
	state.agentStateMu.RUnlock()
	if found {
		return agentRec, nil
	}

	agentRec, err := s.Repo.AddAgentToStream(ctx, s.ProjectID, streamIndex, agentName, instanceKey, branchName)
	if err != nil {
		return data.Agent{}, fmt.Errorf("add agent %s: %w", agentName, err)
	}

	state.agentStateMu.Lock()
	state.agentStateMap[instanceKey] = agentRec
	state.agentStateMu.Unlock()
	s.logAgentCreated(agentName)
	s.publishSync(agentRec, agentRec.State, "")
	return agentRec, nil
}

func (s *Supervisor) runPlannerDecision(ctx context.Context, streamCtx *StreamContext, plannerStep int, state *executionState) (PlanDecision, error) {
	plannerDef, ok := state.agentMap[plannerAgentName]
	if !ok {
		return PlanDecision{}, fmt.Errorf("planner agent definition missing")
	}

	instanceKey := plannerStepInstanceKey(streamCtx.StreamID, plannerStep)
	agentRec, err := s.ensureStreamAgentRecord(ctx, plannerAgentName, instanceKey, streamCtx.StreamID, streamCtx.BranchName, state)
	if err != nil {
		return PlanDecision{}, err
	}

	task := "Decide the next single step for this stream."
	prompt := buildPlannerDecisionPrompt(streamCtx, state.planningAgents)
	output, err := s.runPromptedAgent(ctx, agentRec, plannerAgentName, task, prompt, plannerDef, state)
	if err != nil {
		return PlanDecision{}, fmt.Errorf("planner step failed for stream %d: %w", streamCtx.StreamID, err)
	}

	state.agentStateMu.Lock()
	state.agentStateMap[instanceKey] = refreshAgentCompletion(agentRec, output)
	state.executedAgents[instanceKey] = struct{}{}
	state.agentStateMu.Unlock()

	streamCtx.PlannerOutputs = append(streamCtx.PlannerOutputs, output)

	decision, err := parsePlanDecision(output)
	if err != nil {
		return PlanDecision{}, err
	}
	if !decision.Completed {
		tempPlan := Plan{Streams: []TaskStream{{*decision.NextTask}}}
		if err := resolvePlanAgents(&tempPlan, state.agentMap); err != nil {
			return PlanDecision{}, err
		}
		resolvedTask := tempPlan.Streams[0][0]
		decision.NextTask = &resolvedTask
	}
	return decision, nil
}

func (s *Supervisor) runBranchSetupForStream(ctx context.Context, streamIndex int, mainTask string, state *executionState) (string, string, error) {
	def, ok := state.agentMap[branchSetupAgentName]
	if !ok {
		return "", "", fmt.Errorf("branch-setup agent definition missing")
	}

	branchName := s.streamBranchName(streamIndex)
	instanceKey := branchSetupInstanceKey(streamIndex)
	agentRec, err := s.ensureStreamAgentRecord(ctx, branchSetupAgentName, instanceKey, streamIndex, branchName, state)
	if err != nil {
		return "", "", err
	}

	task := "Initialize branch context for this stream."
	prompt := s.buildBranchSetupPrompt(streamIndex, task, mainTask, def.PromptContext.PreviousOutput)
	output, err := s.runPromptedAgent(ctx, agentRec, branchSetupAgentName, task, prompt, def, state)
	if err != nil {
		return "", "", err
	}

	meta, _, parseErr := ParseAgentOutput(output)
	if parseErr == nil {
		if parsedBranchName := strings.TrimSpace(meta.BranchName); parsedBranchName != "" && parsedBranchName != branchName {
			branchName = parsedBranchName
			agentRec, err = s.ensureStreamAgentRecord(ctx, branchSetupAgentName, instanceKey, streamIndex, branchName, state)
			if err != nil {
				return "", "", err
			}
		}
	}

	state.agentStateMu.Lock()
	state.agentStateMap[instanceKey] = refreshAgentCompletion(agentRec, output)
	state.executedAgents[instanceKey] = struct{}{}
	state.agentStateMu.Unlock()
	return branchName, output, nil
}

func (s *Supervisor) runPromptedAgent(ctx context.Context, agentRec data.Agent, agentName, task, prompt string, def data.AgentDefinition, state *executionState) (string, error) {
	if err := s.markAgentRunning(ctx, agentRec, task); err != nil {
		return "", err
	}

	wrapper, err := s.AcpFactory(ctx, def, s.EventBus, s.Workplace, agentName)
	if err != nil {
		if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
			s.reportAgentError(agentName, "Failed to persist agent failure: %v", failErr)
		}
		return "", fmt.Errorf("launch_failed for %s: %w", agentName, err)
	}
	defer wrapper.Close()
	s.attachACPLogger(wrapper)

	s.logAgentInput(agentName, prompt)
	out, err := wrapper.Prompt(ctx, prompt)
	if err != nil {
		if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
			s.reportAgentError(agentName, "Failed to persist agent failure: %v", failErr)
		}
		return "", fmt.Errorf("execution_failed for %s: %w", agentName, err)
	}
	s.logAgentOutput(agentName, out)

	if err := s.completeAgent(ctx, agentRec, out); err != nil {
		s.reportAgentError(agentName, "Failed to persist agent completion: %v", err)
	}

	if state != nil {
		checkpointIndex := int(atomic.AddInt64(state.stepCounter, 1))
		if saveErr := s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, checkpointIndex, "completed", out); saveErr != nil {
			return "", fmt.Errorf("checkpoint after %s: %w", agentName, saveErr)
		}
	}
	return out, nil
}

func (s *Supervisor) publishPlannedStep(streamIndex int, task PlanTask, state *executionState) error {
	state.planMu.Lock()
	defer state.planMu.Unlock()

	if streamIndex < 0 || streamIndex >= len(state.publishedPlan.Streams) {
		return fmt.Errorf("stream index %d out of range", streamIndex)
	}
	state.publishedPlan.Streams[streamIndex] = append(state.publishedPlan.Streams[streamIndex], task)
	s.publishPlan(clonePlan(*state.publishedPlan))
	return nil
}

func (s *Supervisor) prepareExecutionStreams(ctx context.Context, plan Plan, streamOffset int, agentMap map[string]data.AgentDefinition, agentStateMap map[string]data.Agent, forceRun bool) ([]executionStream, error) {
	for _, stream := range plan.Streams {
		for _, task := range stream {
			if _, ok := agentMap[task.Agent]; !ok {
				return nil, fmt.Errorf("agent %s not found", task.Agent)
			}
		}
	}

	streams := make([]executionStream, 0, len(plan.Streams))
	for localStreamIdx, stream := range plan.Streams {
		globalStreamIdx := streamOffset + localStreamIdx
		execution := make(executionStream, 0, len(stream))
		for stepIdx, task := range stream {
			instanceKey := planStepInstanceKey(globalStreamIdx, stepIdx)

			if _, found := agentStateMap[instanceKey]; !found {
				agentRec, err := s.Repo.AddAgentToProject(ctx, s.ProjectID, task.Agent, instanceKey)
				if err != nil {
					return nil, fmt.Errorf("add agent %s: %w", task.Agent, err)
				}
				agentStateMap[instanceKey] = agentRec
				s.logAgentCreated(task.Agent)
				s.publishSync(agentRec, agentRec.State, "")
			}

			execution = append(execution, executionTask{
				PlanTask:    task,
				InstanceKey: instanceKey,
				ForceRun:    forceRun,
			})
		}
		streams = append(streams, execution)
	}
	return streams, nil
}

func planStepInstanceKey(streamIndex, stepIndex int) string {
	return fmt.Sprintf("s%d:t%d", streamIndex, stepIndex)
}

func collectRequiredAgents(plan Plan) []string {
	requiredAgents := []string{"planner", "branch-setup"}
	seen := map[string]struct{}{
		plannerAgentName:     {},
		branchSetupAgentName: {},
	}

	for _, stream := range plan.Streams {
		for _, task := range stream {
			if _, ok := seen[task.Agent]; ok {
				continue
			}
			seen[task.Agent] = struct{}{}
			requiredAgents = append(requiredAgents, task.Agent)
		}
	}

	return requiredAgents
}

func (s *Supervisor) executePlan(ctx context.Context, streams []executionStream, initialPrompt string, state *executionState) error {
	g, gCtx := errgroup.WithContext(ctx)

	for _, stream := range streams {
		stream := stream
		g.Go(func() error {
			currentPrompt := initialPrompt
			for _, step := range stream {
				var err error
				currentPrompt, err = s.executeTask(gCtx, step, currentPrompt, state)
				if err != nil {
					return err
				}
			}
			return nil
		})
	}

	return g.Wait()
}

func (s *Supervisor) executeTask(ctx context.Context, step executionTask, currentPrompt string, state *executionState) (string, error) {
	agentLock := getAgentLock(state, step.InstanceKey)
	agentLock.Lock()
	defer agentLock.Unlock()

	def, ok := state.agentMap[step.Agent]
	if !ok {
		return "", fmt.Errorf("agent %s not found", step.Agent)
	}

	state.agentStateMu.RLock()
	agentRec, found := state.agentStateMap[step.InstanceKey]
	_, completedBeforeRun := state.completedBeforeRun[step.InstanceKey]
	_, executedThisRun := state.executedAgents[step.InstanceKey]
	state.agentStateMu.RUnlock()
	if !found {
		return "", fmt.Errorf("agent %s not found in project state", step.Agent)
	}

	if agentRec.State == "completed" && !step.ForceRun && completedBeforeRun && !executedThisRun {
		s.logAgentActivity(step.Agent, "reused completed output")
		return agentRec.Output, nil
	}

	if err := s.markAgentRunning(ctx, agentRec, step.Task); err != nil {
		if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
			s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
		}
		return "", fmt.Errorf("execution_failed for %s: %w", step.Agent, err)
	}

	agentRec.State = "running"
	state.agentStateMu.Lock()
	state.agentStateMap[step.InstanceKey] = agentRec
	state.agentStateMu.Unlock()

	wrapper, err := s.AcpFactory(ctx, def, s.EventBus, s.Workplace, step.Agent)
	if err != nil {
		if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
			s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
		}
		return "", fmt.Errorf("launch_failed for %s: %w", step.Agent, err)
	}
	s.attachACPLogger(wrapper)
	defer wrapper.Close()

	promptContext := buildAgentTaskPrompt(step.Agent, step.Task, currentPrompt, def.PromptContext.PreviousOutput, state.planningAgents)
	s.logAgentInput(step.Agent, promptContext)
	out, err := wrapper.Prompt(ctx, promptContext)
	if err != nil {
		if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
			s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
		}
		return "", fmt.Errorf("execution_failed for %s: %w", step.Agent, err)
	}
	s.logAgentOutput(step.Agent, out)

	if err := s.completeAgent(ctx, agentRec, out); err != nil {
		s.reportAgentError(step.Agent, "Failed to persist agent completion: %v", err)
	} else {
		agentRec.Output = out
		agentRec.State = "completed"
		state.agentStateMu.Lock()
		state.agentStateMap[step.InstanceKey] = agentRec
		state.executedAgents[step.InstanceKey] = struct{}{}
		state.agentStateMu.Unlock()
	}

	checkpointIndex := int(atomic.AddInt64(state.stepCounter, 1))
	if saveErr := s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, checkpointIndex, "completed", out); saveErr != nil {
		return "", fmt.Errorf("checkpoint after %s: %w", step.Agent, saveErr)
	}

	return out, nil
}

func appendPublishedPlan(state *executionState, followUpPlan Plan) Plan {
	state.planMu.Lock()
	defer state.planMu.Unlock()

	state.publishedPlan.Streams = append(state.publishedPlan.Streams, followUpPlan.Streams...)
	return clonePlan(*state.publishedPlan)
}

func getAgentLock(state *executionState, agentName string) *sync.Mutex {
	state.agentLocksMu.Lock()
	defer state.agentLocksMu.Unlock()

	if mu, ok := state.agentLocks[agentName]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	state.agentLocks[agentName] = mu
	return mu
}

func clonePlan(plan Plan) Plan {
	cloned := Plan{Streams: make([]TaskStream, len(plan.Streams))}
	for index, stream := range plan.Streams {
		cloned.Streams[index] = append(TaskStream(nil), stream...)
	}
	return cloned
}

func buildTaskPrompt(task string, previousOutput string, includePreviousOutput bool) string {
	if !includePreviousOutput || strings.TrimSpace(previousOutput) == "" {
		return fmt.Sprintf("Task: %s", task)
	}

	return fmt.Sprintf("Task: %s\n\nPrevious context/output: %s", task, previousOutput)
}

func buildAgentTaskPrompt(agentName, task, previousOutput string, includePreviousOutput bool, planningAgents []string) string {
	prompt := buildTaskPrompt(task, previousOutput, includePreviousOutput)
	if CanonicalAgentName(agentName) == plannerAgentName {
		return prompt
	}

	sections := []string{
		prompt,
		"End your response with a final JSON code block containing planner-facing metadata.",
		"Use this schema: ```json\n{\"summary\":\"brief result summary\",\"branch_name\":\"optional branch name\",\"files_modified\":[\"optional/file\"],\"tests_run\":{\"passed\":0,\"failed\":0,\"skipped\":0,\"failures\":[]},\"issues\":[{\"type\":\"bug|security|style|performance\",\"severity\":\"critical|high|medium|low\",\"description\":\"issue summary\",\"location\":\"optional file:line\"}],\"recommendations\":[\"optional next consideration\"],\"completion_status\":\"full|partial|blocked\"}\n```",
		"Keep all human-readable details before the final JSON block.",
	}

	return strings.Join(sections, "\n\n")
}

func canEmitFollowUpPlan(agentName string) bool {
	switch CanonicalAgentName(agentName) {
	case codeReviewerAgentName, testerAgentName:
		return true
	default:
		return false
	}
}

func (s *Supervisor) validateWorkplaceGitRoot(ctx context.Context) error {
	if s.GitRoot == nil {
		return nil
	}

	resolvedWorkplace, err := filepath.Abs(strings.TrimSpace(s.Workplace))
	if err != nil {
		return fmt.Errorf("resolve workplace path: %w", err)
	}

	gitRoot, err := s.GitRoot(ctx, resolvedWorkplace)
	if err != nil {
		return err
	}
	if strings.TrimSpace(gitRoot) == "" {
		return nil
	}

	if pathsMatch(resolvedWorkplace, gitRoot) {
		return nil
	}

	return fmt.Errorf("workplace %s is inside git repository %s; configure the repository root as the workplace", resolvedWorkplace, gitRoot)
}

func pathsMatch(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (s *Supervisor) buildBranchSetupPrompt(streamIndex int, task, mainTask string, includePreviousOutput bool) string {
	branchName := s.streamBranchName(streamIndex)
	sections := []string{
		fmt.Sprintf("Task: %s", task),
		fmt.Sprintf("Stream id: %d", streamIndex),
		fmt.Sprintf("Project name: %s", strings.TrimSpace(s.ProjectName)),
		fmt.Sprintf("Project slug: %s", branchProjectSlug(s.ProjectName)),
		fmt.Sprintf("Run id: %s", branchRunID(s.Logger)),
		fmt.Sprintf("Suggested branch name: %s", branchName),
		fmt.Sprintf("Main task: %s", strings.TrimSpace(mainTask)),
		fmt.Sprintf("Workplace: %s", strings.TrimSpace(s.Workplace)),
		"End your response with a final JSON code block using the shared metadata schema and include branch_name.",
	}

	if includePreviousOutput {
		sections = append(sections, "Previous context/output is not required for branch setup.")
	}

	return strings.Join(sections, "\n\n")
}

func (s *Supervisor) streamBranchName(streamIndex int) string {
	projectSlug := branchProjectSlug(s.ProjectName)
	runID := branchRunID(s.Logger)
	if runID == "" {
		return fmt.Sprintf("%s/stream-%d", projectSlug, streamIndex)
	}
	return fmt.Sprintf("%s/%s/stream-%d", projectSlug, runID, streamIndex)
}

func branchProjectSlug(projectName string) string {
	slug := sanitizePromptSegment(projectName)
	if slug == "" {
		return "project"
	}
	return slug
}

func branchRunID(logger *data.ProjectLogger) string {
	if logger == nil {
		return ""
	}
	return sanitizePromptSegment(logger.RunID())
}

func sanitizePromptSegment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}

	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		case r == '-', r == '_', r == ' ', r == '.':
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	return strings.Trim(builder.String(), "-")
}

func (s *Supervisor) completeAgent(ctx context.Context, agent data.Agent, output string) error {
	if err := s.Repo.UpdateAgentOutput(ctx, agent.ID, output); err != nil {
		return err
	}
	if err := s.Repo.UpdateAgentState(ctx, agent.ID, "completed"); err != nil {
		return err
	}
	s.logAgentState(agent.Name, "completed")
	s.recordAgentActivity(ctx, agent, "completed")
	s.publishSync(agent, "completed", "completed")

	return nil
}

func (s *Supervisor) markAgentFailed(ctx context.Context, agent data.Agent, cause error) error {
	if err := s.Repo.UpdateAgentState(ctx, agent.ID, "failed"); err != nil {
		return err
	}

	activity := "failed"
	if cause != nil {
		activity = fmt.Sprintf("failed: %v", cause)
	}

	s.logAgentState(agent.Name, "failed")
	s.recordAgentActivity(ctx, agent, activity)
	s.publishSync(agent, "failed", activity)

	return nil
}

func (s *Supervisor) markAgentRunning(ctx context.Context, agent data.Agent, activity string) error {
	if err := s.Repo.UpdateAgentState(ctx, agent.ID, "running"); err != nil {
		return err
	}
	s.logAgentState(agent.Name, "running")
	s.recordAgentActivity(ctx, agent, activity)
	s.publishSync(agent, "running", activity)

	return nil
}

func (s *Supervisor) recordAgentActivity(ctx context.Context, agent data.Agent, activity string) {
	activity = strings.TrimSpace(activity)
	if s.Repo == nil || activity == "" {
		return
	}
	s.logAgentActivity(agent.Name, activity)

	if _, err := s.Repo.NewActivity(ctx, s.ProjectID, sql.NullInt64{Int64: agent.ID, Valid: agent.ID > 0}, fmt.Sprintf("%s: %s", agent.Name, activity)); err != nil {
		s.reportAgentError(agent.Name, "Failed to persist activity: %v", err)
	}
}

func (s *Supervisor) publishSync(agent data.Agent, state, activity string) {
	if s.EventBus == nil {
		return
	}

	s.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.SupervisorSyncEvent{
		ProjectID:   s.ProjectID,
		AgentID:     agent.ID,
		Agent:       agent.Name,
		InstanceKey: agent.InstanceKey,
		State:       state,
		Activity:    activity,
	}})
}

func (s *Supervisor) publishPlan(plan Plan) {
	if s.EventBus == nil {
		return
	}

	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		s.reportProjectError("Failed to marshal plan update: %v", err)
		return
	}

	s.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.PlanUpdateEvent{Plan: string(encodedPlan)}})
}

func extractPlanJSON(output string) string {
	rawJSON := output
	jsonBlockRegex := regexp.MustCompile("(?s)```(?:json)?\n(.*?)\n```")
	if matches := jsonBlockRegex.FindStringSubmatch(rawJSON); len(matches) > 1 {
		return matches[1]
	}

	start := strings.Index(rawJSON, "{")
	end := strings.LastIndex(rawJSON, "}")
	if start != -1 && end != -1 && end > start {
		return rawJSON[start : end+1]
	}

	return rawJSON
}

func extractFollowUpPlanJSON(output string) (string, bool) {
	jsonBlockRegex := regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	matches := jsonBlockRegex.FindAllStringSubmatch(output, -1)
	var lastCandidate string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		candidate := strings.TrimSpace(m[1])
		if strings.Contains(candidate, `"streams"`) {
			lastCandidate = candidate
		}
	}
	if lastCandidate != "" {
		return lastCandidate, true
	}

	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") && strings.Contains(trimmed, `"streams"`) {
		return trimmed, true
	}

	return "", false
}

func parseFollowUpPlan(agentName, output string) (Plan, bool, error) {
	if !canEmitFollowUpPlan(agentName) {
		return Plan{}, false, nil
	}

	rawJSON, ok := extractFollowUpPlanJSON(output)
	if !ok {
		return Plan{}, false, nil
	}

	plan, err := parsePlanJSON(rawJSON)
	if err != nil {
		return Plan{}, false, err
	}
	if len(plan.Streams) == 0 {
		return Plan{}, false, nil
	}
	if err := normalizePlan(&plan); err != nil {
		return Plan{}, false, err
	}

	return plan, true, nil
}

func parsePlanJSON(rawJSON string) (Plan, error) {
	var plan Plan
	if err := json.Unmarshal([]byte(rawJSON), &plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (s *Supervisor) failStop(ctx context.Context, stepIndex int, status string, originalErr error) error {
	if s.Repo != nil {
		if err := s.Repo.UpdateProjectState(ctx, s.ProjectID, "stopped"); err != nil {
			s.reportProjectError("Failed to persist stopped project state: %v", err)
		}
	}
	if s.Logger != nil {
		s.Logger.LogProject("PROJECT_STATE", "stopped")
	}
	if s.Logger != nil {
		s.Logger.LogProjectError(fmt.Sprintf("step=%d\nstatus=%s\nerror=%v", stepIndex, status, originalErr))
	}
	if s.Repo != nil {
		_ = s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, stepIndex, status, originalErr.Error())
	}
	return fmt.Errorf("fail-stop at step %d: %w", stepIndex, originalErr)
}

func (s *Supervisor) failAgentAndStop(ctx context.Context, agent data.Agent, stepIndex int, status string, originalErr error) error {
	if err := s.markAgentFailed(ctx, agent, originalErr); err != nil {
		s.reportAgentError(agent.Name, "Failed to persist agent failure: %v", err)
	}
	return s.failStop(ctx, stepIndex, status, originalErr)
}

func (s *Supervisor) reportProjectError(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if s.Logger != nil {
		s.Logger.LogProjectError(message)
	}
	log.Printf("%s", message)
}

func (s *Supervisor) reportAgentError(agentName, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if s.Logger != nil {
		s.Logger.LogAgentError(agentName, message)
	}
	log.Printf("%s", message)
}

func (s *Supervisor) logAgentCreated(agentName string) {
	if s.Logger != nil {
		s.Logger.LogAgentCreated(agentName)
	}
}

func (s *Supervisor) logAgentState(agentName, state string) {
	if s.Logger != nil {
		s.Logger.LogAgentState(agentName, state)
	}
}

func (s *Supervisor) logAgentActivity(agentName, activity string) {
	if s.Logger != nil {
		s.Logger.LogAgentActivity(agentName, activity)
	}
}

func (s *Supervisor) logAgentInput(agentName, input string) {
	if s.Logger != nil {
		s.Logger.LogAgentInput(agentName, input)
	}
}

func (s *Supervisor) logAgentOutput(agentName, output string) {
	if s.Logger != nil {
		s.Logger.LogAgentOutput(agentName, output)
	}
}

func (s *Supervisor) attachACPLogger(client ACPClient) {
	if s.Logger == nil || client == nil {
		return
	}
	if loggerAware, ok := client.(interface{ SetLogger(*data.ProjectLogger) }); ok {
		loggerAware.SetLogger(s.Logger)
	}
}
