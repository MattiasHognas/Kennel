package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"

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

func ParsePlanOutput(output string) (Plan, error) {
	return parsePlanJSON(extractPlanJSON(output))
}

const (
	branchSetupAgentName    = "branch-setup"
	plannerAgentName        = "planner"
	generalPurposeAgentName = "general-purpose"
)

type Repository interface {
	AddAgentToProject(ctx context.Context, projectID int64, name string) (data.Agent, error)
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

func DefaultACPFactory(ctx context.Context, definition data.AgentDefinition, eb *data.EventBus, workplace string, topic string) (ACPClient, error) {
	return workers.NewWrapper(ctx, definition, eb, workplace, topic)
}

type Supervisor struct {
	Repo        Repository
	EventBus    *data.EventBus
	AgentsDir   string
	ProjectID   int64
	ProjectName string
	Workplace   string
	AcpFactory  ACPFactory
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
	var agentStateMu sync.RWMutex
	for _, a := range proj.Agents {
		agentStateMap[a.Name] = a
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

	plannerTask := PlanTask{Agent: plannerAgentName, Task: "Create an execution plan based on the project instructions."}
	plannerDef, ok := agentMap[plannerTask.Agent]
	if !ok {
		return s.failStop(ctx, 0, "planning_validation_failed", fmt.Errorf("planner agent definition missing"))
	}

	var planOutput string
	plannerRec, plannerFound := agentStateMap[plannerTask.Agent]
	if !plannerFound {
		plannerRec, err = s.Repo.AddAgentToProject(ctx, s.ProjectID, plannerTask.Agent)
		if err != nil {
			return s.failStop(ctx, 0, "planning_validation_failed", fmt.Errorf("add agent %s: %w", plannerTask.Agent, err))
		}
		agentStateMap[plannerTask.Agent] = plannerRec
		s.logAgentCreated(plannerRec.Name)
		s.publishSync(plannerRec, plannerRec.State, "")
	}

	if plannerRec.State != "completed" {
		if err := s.markAgentRunning(ctx, plannerRec, plannerTask.Task); err != nil {
			return s.failAgentAndStop(ctx, plannerRec, 0, "planning_failed", err)
		}
		plannerRec.State = "running"
		agentStateMap[plannerTask.Agent] = plannerRec

		plannerWrapper, err := s.AcpFactory(ctx, plannerDef, s.EventBus, s.Workplace, "planner")
		if err != nil {
			return s.failAgentAndStop(ctx, plannerRec, 0, "planning_launch_failed", err)
		}
		s.attachACPLogger(plannerWrapper)
		defer plannerWrapper.Close()

		planPrompt := fmt.Sprintf(`Create an execution plan based on these instructions: %s

You must output a JSON object containing an array of 'streams', where each stream is an array of tasks that must run sequentially.
Each task must have an 'agent' and a 'task' string. Allow parallel streams.
Use only these exact agent names for plan tasks: %v
Do not use planner, branch-setup, supervisor, or general_purpose unless they appear exactly in the allowed list.
		Ensure the response is purely the JSON or embedded in a Markdown block.`, instructions, planningAgents)

		s.logAgentInput(plannerTask.Agent, planPrompt)
		planOutput, err = plannerWrapper.Prompt(ctx, planPrompt)
		if err != nil {
			return s.failAgentAndStop(ctx, plannerRec, 0, "planning_failed", err)
		}
		s.logAgentOutput(plannerTask.Agent, planOutput)
	} else {
		planOutput = plannerRec.Output
		s.logAgentActivity(plannerTask.Agent, "reused completed output")
	}

	rawJSON := extractPlanJSON(planOutput)
	plan, err := parsePlanJSON(rawJSON)
	if err != nil {
		return s.failAgentAndStop(ctx, plannerRec, 0, "planning_json_parse_failed", err)
	}

	if err := normalizePlan(&plan); err != nil {
		return s.failAgentAndStop(ctx, plannerRec, 0, "planning_validation_failed", err)
	}

	if err := resolvePlanAgents(&plan, agentMap); err != nil {
		return s.failAgentAndStop(ctx, plannerRec, 0, "planning_validation_failed", err)
	}

	if err := s.ensurePlanAgents(ctx, plan, agentMap, agentStateMap); err != nil {
		return s.failAgentAndStop(ctx, plannerRec, 0, "planning_validation_failed", err)
	}
	s.publishPlan(plan)

	if plannerRec.State != "completed" {
		if err := s.completeAgent(ctx, agentStateMap[plannerAgentName], planOutput); err != nil {
			s.reportAgentError(plannerTask.Agent, "Failed to persist planner completion: %v", err)
		} else {
			plannerRec = agentStateMap[plannerAgentName]
			plannerRec.Output = planOutput
			plannerRec.State = "completed"
			agentStateMap[plannerAgentName] = plannerRec
		}
	}

	branchSetupTask := PlanTask{Agent: branchSetupAgentName, Task: "Initialize branch context based on plan."}
	branchSetupDef := agentMap[branchSetupTask.Agent]

	var setupOut string
	branchSetupRec, branchSetupFound := agentStateMap[branchSetupTask.Agent]
	if !branchSetupFound {
		return s.failStop(ctx, 1, "agent_state_not_found", fmt.Errorf("agent state for %s not found", branchSetupTask.Agent))
	}

	if branchSetupRec.State != "completed" {
		if err := s.markAgentRunning(ctx, branchSetupRec, branchSetupTask.Task); err != nil {
			return s.failAgentAndStop(ctx, branchSetupRec, 1, "execution_failed", err)
		}
		branchSetupRec.State = "running"
		agentStateMap[branchSetupTask.Agent] = branchSetupRec

		setupWrapper, err := s.AcpFactory(ctx, branchSetupDef, s.EventBus, s.Workplace, branchSetupTask.Agent)
		if err != nil {
			return s.failAgentAndStop(ctx, branchSetupRec, 1, "launch_failed", err)
		}
		s.attachACPLogger(setupWrapper)

		setupPrompt := buildTaskPrompt(branchSetupTask.Task, rawJSON, branchSetupDef.PromptContext.PreviousOutput)
		s.logAgentInput(branchSetupTask.Agent, setupPrompt)
		setupOut, err = setupWrapper.Prompt(ctx, setupPrompt)
		setupWrapper.Close()
		if err != nil {
			return s.failAgentAndStop(ctx, branchSetupRec, 1, "execution_failed", err)
		}
		s.logAgentOutput(branchSetupTask.Agent, setupOut)

		if err := s.completeAgent(ctx, agentStateMap[branchSetupAgentName], setupOut); err != nil {
			s.reportAgentError(branchSetupTask.Agent, "Failed to persist branch setup completion: %v", err)
		} else {
			branchSetupRec = agentStateMap[branchSetupAgentName]
			branchSetupRec.Output = setupOut
			branchSetupRec.State = "completed"
			agentStateMap[branchSetupAgentName] = branchSetupRec
		}
	} else {
		setupOut = branchSetupRec.Output
		s.logAgentActivity(branchSetupTask.Agent, "reused completed output")
	}

	if saveErr := s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, 1, "completed", setupOut); saveErr != nil {
		return s.failStop(ctx, 1, "checkpoint_failed", fmt.Errorf("checkpoint after branch setup: %w", saveErr))
	}

	g, gCtx := errgroup.WithContext(ctx)

	for streamIdx, stream := range plan.Streams {
		streamIdx := streamIdx
		stream := stream

		g.Go(func() error {
			currentPrompt := setupOut
			for stepIdx, step := range stream {
				def, ok := agentMap[step.Agent]
				if !ok {
					return fmt.Errorf("agent %s not found in stream %d", step.Agent, streamIdx)
				}

				agentStateMu.RLock()
				agentRec, found := agentStateMap[step.Agent]
				agentStateMu.RUnlock()

				if !found {
					return fmt.Errorf("agent %s not found in project state for stream %d", step.Agent, streamIdx)
				}

				if agentRec.State == "completed" {
					currentPrompt = agentRec.Output
					s.logAgentActivity(step.Agent, "reused completed output")
					continue
				}

				if err := s.markAgentRunning(gCtx, agentRec, step.Task); err != nil {
					if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
						s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
					}
					return fmt.Errorf("execution_failed for %s: %w", step.Agent, err)
				}
				agentRec.State = "running"
				agentStateMu.Lock()
				agentStateMap[step.Agent] = agentRec
				agentStateMu.Unlock()

				wrapper, err := s.AcpFactory(gCtx, def, s.EventBus, s.Workplace, step.Agent)
				if err != nil {
					if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
						s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
					}
					return fmt.Errorf("launch_failed for %s: %w", step.Agent, err)
				}
				s.attachACPLogger(wrapper)

				promptContext := buildTaskPrompt(step.Task, currentPrompt, def.PromptContext.PreviousOutput)
				s.logAgentInput(step.Agent, promptContext)
				out, err := wrapper.Prompt(gCtx, promptContext)
				if err != nil {
					wrapper.Close()
					if failErr := s.markAgentFailed(ctx, agentRec, err); failErr != nil {
						s.reportAgentError(step.Agent, "Failed to persist agent failure: %v", failErr)
					}
					return fmt.Errorf("execution_failed for %s: %w", step.Agent, err)
				}
				s.logAgentOutput(step.Agent, out)

				wrapper.Close()
				currentPrompt = out

				if err := s.completeAgent(gCtx, agentRec, currentPrompt); err != nil {
					s.reportAgentError(step.Agent, "Failed to persist agent completion: %v", err)
				} else {
					agentRec.Output = currentPrompt
					agentRec.State = "completed"
					agentStateMu.Lock()
					agentStateMap[step.Agent] = agentRec
					agentStateMu.Unlock()
				}

				if saveErr := s.Repo.CheckpointSupervisorRun(gCtx, s.ProjectID, 2+streamIdx*100+stepIdx, "completed", out); saveErr != nil {
					return fmt.Errorf("checkpoint after stream %d step %d: %w", streamIdx, stepIdx, saveErr)
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return s.failStop(ctx, -1, "stream_execution_failed", err)
	}

	if s.Logger != nil {
		s.Logger.LogProject("PROJECT_COMPLETE", "supervisor run completed")
	}

	return nil
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

func (s *Supervisor) ensurePlanAgents(ctx context.Context, plan Plan, agentMap map[string]data.AgentDefinition, agentStateMap map[string]data.Agent) error {
	requiredAgents := collectRequiredAgents(plan)

	for _, agentName := range requiredAgents {
		if _, ok := agentMap[agentName]; !ok {
			return fmt.Errorf("agent %s not found", agentName)
		}
	}

	for _, agentName := range requiredAgents {
		if _, found := agentStateMap[agentName]; found {
			continue
		}

		agentRec, err := s.Repo.AddAgentToProject(ctx, s.ProjectID, agentName)
		if err != nil {
			return fmt.Errorf("add agent %s: %w", agentName, err)
		}
		agentStateMap[agentName] = agentRec
		s.logAgentCreated(agentName)
		s.publishSync(agentRec, agentRec.State, "")
	}

	return nil
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

func buildTaskPrompt(task string, previousOutput string, includePreviousOutput bool) string {
	if !includePreviousOutput || strings.TrimSpace(previousOutput) == "" {
		return fmt.Sprintf("Task: %s", task)
	}

	return fmt.Sprintf("Task: %s\n\nPrevious context/output: %s", task, previousOutput)
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
		ProjectID: s.ProjectID,
		AgentID:   agent.ID,
		Agent:     agent.Name,
		State:     state,
		Activity:  activity,
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
