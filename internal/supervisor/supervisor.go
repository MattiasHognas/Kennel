package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"MattiasHognas/Kennel/internal/acp"
	repository "MattiasHognas/Kennel/internal/data"
	"MattiasHognas/Kennel/internal/discovery"
	eventbus "MattiasHognas/Kennel/internal/events"

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

const (
	branchSetupAgentName    = "branch-setup"
	plannerAgentName        = "planner"
	generalPurposeAgentName = "general-purpose"
)

type Repository interface {
	AddAgentToProject(ctx context.Context, projectID int64, name string) (repository.Agent, error)
	CheckpointSupervisorRun(ctx context.Context, projectID int64, stepIndex int, status, data string) error
	ReadProject(ctx context.Context, projectID int64) (repository.Project, error)
	UpdateAgentOutput(ctx context.Context, agentID int64, output string) error
	UpdateAgentState(ctx context.Context, agentID int64, state string) error
}

type ACPClient interface {
	Prompt(ctx context.Context, msg string) (string, error)
	Close() error
}

type ACPFactory func(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, workplace string, topic string) (ACPClient, error)

func DefaultACPFactory(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, workplace string, topic string) (ACPClient, error) {
	return acp.NewWrapper(ctx, binary, args, eb, workplace, topic)
}

type Supervisor struct {
	Repo        Repository
	EventBus    *eventbus.EventBus
	AgentsDir   string
	ProjectID   int64
	ProjectName string
	Workplace   string
	AcpFactory  ACPFactory
}

func NewSupervisor(repo Repository, eb *eventbus.EventBus, agentsDir string, projectID int64, projectName string, workplace string) *Supervisor {
	return &Supervisor{
		Repo:        repo,
		EventBus:    eb,
		AgentsDir:   agentsDir,
		ProjectID:   projectID,
		ProjectName: projectName,
		Workplace:   workplace,
		AcpFactory:  DefaultACPFactory,
	}
}

func (s *Supervisor) RunPlan(ctx context.Context, instructions string, configuredAgents []string) error {
	proj, err := s.Repo.ReadProject(ctx, s.ProjectID)
	if err != nil {
		return s.failStop(ctx, 0, "read_project_failed", err)
	}

	agentStateMap := make(map[string]repository.Agent)
	for _, a := range proj.Agents {
		agentStateMap[a.Name] = a
	}

	defs, err := discovery.LoadAgentDefinitions(s.AgentsDir)
	if err != nil {
		return s.failStop(ctx, -1, "discovery_failed", err)
	}

	agentMap := make(map[string]discovery.AgentDefinition)
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

	if !plannerFound || plannerRec.State != "completed" {
		plannerWrapper, err := s.AcpFactory(ctx, plannerDef.LaunchConfig.Binary, plannerDef.LaunchConfig.Args, s.EventBus, s.Workplace, "planner")
		if err != nil {
			return s.failStop(ctx, 0, "planning_launch_failed", err)
		}
		defer plannerWrapper.Close()

		planPrompt := fmt.Sprintf(`Create an execution plan based on these instructions: %s

You must output a JSON object containing an array of 'streams', where each stream is an array of tasks that must run sequentially.
Each task must have an 'agent' and a 'task' string. Allow parallel streams.
Use only these exact agent names for plan tasks: %v
Do not use planner, branch-setup, supervisor, or general_purpose unless they appear exactly in the allowed list.
		Ensure the response is purely the JSON or embedded in a Markdown block.`, instructions, planningAgents)

		planOutput, err = plannerWrapper.Prompt(ctx, planPrompt)
		if err != nil {
			return s.failStop(ctx, 0, "planning_failed", err)
		}
	} else {
		planOutput = plannerRec.Output
	}

	rawJSON := planOutput
	jsonBlockRegex := regexp.MustCompile("(?s)```(?:json)?\n(.*?)\n```")
	if matches := jsonBlockRegex.FindStringSubmatch(rawJSON); len(matches) > 1 {
		rawJSON = matches[1]
	} else {
		start := strings.Index(rawJSON, "{")
		end := strings.LastIndex(rawJSON, "}")
		if start != -1 && end != -1 && end > start {
			rawJSON = rawJSON[start : end+1]
		}
	}

	var plan Plan
	if err := json.Unmarshal([]byte(rawJSON), &plan); err != nil {
		return s.failStop(ctx, 0, "planning_json_parse_failed", err)
	}

	if err := normalizePlan(&plan); err != nil {
		return s.failStop(ctx, 0, "planning_validation_failed", err)
	}

	if err := resolvePlanAgents(&plan, agentMap); err != nil {
		return s.failStop(ctx, 0, "planning_validation_failed", err)
	}

	if err := s.ensurePlanAgents(ctx, plan, agentMap, agentStateMap); err != nil {
		return s.failStop(ctx, 0, "planning_validation_failed", err)
	}

	if !plannerFound || plannerRec.State != "completed" {
		if err := s.completeAgent(ctx, agentStateMap[plannerAgentName], planOutput); err != nil {
			log.Printf("Failed to persist planner completion: %v", err)
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
		setupWrapper, err := s.AcpFactory(ctx, branchSetupDef.LaunchConfig.Binary, branchSetupDef.LaunchConfig.Args, s.EventBus, s.Workplace, branchSetupTask.Agent)
		if err != nil {
			return s.failStop(ctx, 1, "launch_failed", err)
		}

		setupOut, err = setupWrapper.Prompt(ctx, fmt.Sprintf("Task: %s\n\nPrevious context/output: %s", branchSetupTask.Task, rawJSON))
		setupWrapper.Close()
		if err != nil {
			return s.failStop(ctx, 1, "execution_failed", err)
		}

		if err := s.completeAgent(ctx, agentStateMap[branchSetupAgentName], setupOut); err != nil {
			log.Printf("Failed to persist branch setup completion: %v", err)
		} else {
			branchSetupRec = agentStateMap[branchSetupAgentName]
			branchSetupRec.Output = setupOut
			branchSetupRec.State = "completed"
			agentStateMap[branchSetupAgentName] = branchSetupRec
		}
	} else {
		setupOut = branchSetupRec.Output
	}

	if saveErr := s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, 1, "completed", setupOut); saveErr != nil {
		return saveErr
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

				agentRec, found := agentStateMap[step.Agent]

				if !found {
					return fmt.Errorf("agent %s not found in project state for stream %d", step.Agent, streamIdx)
				}

				if agentRec.State == "completed" {
					currentPrompt = agentRec.Output
					continue
				}

				wrapper, err := s.AcpFactory(gCtx, def.LaunchConfig.Binary, def.LaunchConfig.Args, s.EventBus, s.Workplace, step.Agent)
				if err != nil {
					return fmt.Errorf("launch_failed for %s: %w", step.Agent, err)
				}

				promptContext := fmt.Sprintf("Task: %s\n\nPrevious context/output: %s", step.Task, currentPrompt)
				out, err := wrapper.Prompt(gCtx, promptContext)
				if err != nil {
					wrapper.Close()
					return fmt.Errorf("execution_failed for %s: %w", step.Agent, err)
				}

				wrapper.Close()
				currentPrompt = out

				if outErr := s.Repo.UpdateAgentOutput(gCtx, agentRec.ID, currentPrompt); outErr != nil {
					log.Printf("Failed to checkpoint agent output: %v", outErr)
				}
				s.Repo.UpdateAgentState(gCtx, agentRec.ID, "completed")

				if saveErr := s.Repo.CheckpointSupervisorRun(gCtx, s.ProjectID, 2+streamIdx*100+stepIdx, "completed", out); saveErr != nil {
					return saveErr
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return s.failStop(ctx, -1, "stream_execution_failed", err)
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

func resolvePlanAgents(plan *Plan, agentMap map[string]discovery.AgentDefinition) error {
	aliases := make(map[string]string, len(agentMap))
	for name := range agentMap {
		canonicalName := canonicalAgentName(name)
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
			resolvedName, ok := aliases[canonicalAgentName(plan.Streams[streamIdx][taskIdx].Agent)]
			if !ok {
				return fmt.Errorf("agent %s not found", plan.Streams[streamIdx][taskIdx].Agent)
			}
			plan.Streams[streamIdx][taskIdx].Agent = resolvedName
		}
	}

	return nil
}

func canonicalAgentName(name string) string {
	replacer := strings.NewReplacer("-", " ", "_", " ")
	parts := strings.Fields(replacer.Replace(strings.ToLower(strings.TrimSpace(name))))
	return strings.Join(parts, "-")
}

func registerBuiltinAgents(agentMap map[string]discovery.AgentDefinition) {
	if _, ok := agentMap[plannerAgentName]; !ok {
		agentMap[plannerAgentName] = builtinAgentDefinition(plannerAgentName)
	}
	if _, ok := agentMap[generalPurposeAgentName]; !ok {
		agentMap[generalPurposeAgentName] = builtinAgentDefinition(generalPurposeAgentName)
	}
}

func builtinAgentDefinition(name string) discovery.AgentDefinition {
	return discovery.AgentDefinition{
		Name:         name,
		LaunchConfig: discovery.LaunchConfig{Binary: "copilot", Args: []string{"--acp"}},
	}
}

func availablePlanningAgents(agentMap map[string]discovery.AgentDefinition, configuredAgents []string) []string {
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
		canonicalName := canonicalAgentName(name)
		for agentName := range agentMap {
			if canonicalAgentName(agentName) == canonicalName {
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

func (s *Supervisor) ensurePlanAgents(ctx context.Context, plan Plan, agentMap map[string]discovery.AgentDefinition, agentStateMap map[string]repository.Agent) error {
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

func (s *Supervisor) completeAgent(ctx context.Context, agent repository.Agent, output string) error {
	if err := s.Repo.UpdateAgentOutput(ctx, agent.ID, output); err != nil {
		return err
	}
	if err := s.Repo.UpdateAgentState(ctx, agent.ID, "completed"); err != nil {
		return err
	}

	return nil
}

func (s *Supervisor) failStop(ctx context.Context, stepIndex int, status string, originalErr error) error {
	if s.Repo != nil {
		_ = s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, stepIndex, status, originalErr.Error())
	}
	return fmt.Errorf("fail-stop at step %d: %w", stepIndex, originalErr)
}
