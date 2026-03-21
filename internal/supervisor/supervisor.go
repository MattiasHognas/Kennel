package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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

type Repository interface {
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

	plannerDef, ok := agentMap["planner"]
	if !ok {
		plannerDef = discovery.AgentDefinition{
			Name:         "planner",
			LaunchConfig: discovery.LaunchConfig{Binary: "copilot", Args: []string{"--acp"}},
		}
	}

	var planOutput string
	plannerRec, foundPlanner := agentStateMap["planner"]
	if foundPlanner && plannerRec.State == "completed" {
		planOutput = plannerRec.Output
	} else {
		plannerWrapper, err := s.AcpFactory(ctx, plannerDef.LaunchConfig.Binary, plannerDef.LaunchConfig.Args, s.EventBus, s.Workplace, "planner")
		if err != nil {
			return s.failStop(ctx, 0, "planning_launch_failed", err)
		}
		defer plannerWrapper.Close()

		planPrompt := fmt.Sprintf(`Create an execution plan based on these instructions: %s

You must output a JSON object containing an array of 'streams', where each stream is an array of tasks that must run sequentially.
Each task must have an 'agent' and a 'task' string. Allow parallel streams.
Available or configured agents: %v
Ensure the response is purely the JSON or embedded in a Markdown block.`, instructions, configuredAgents)

		planOutput, err = plannerWrapper.Prompt(ctx, planPrompt)
		if err != nil {
			return s.failStop(ctx, 0, "planning_failed", err)
		}

		if foundPlanner {
			if outErr := s.Repo.UpdateAgentOutput(ctx, plannerRec.ID, planOutput); outErr != nil {
				log.Printf("Failed to checkpoint planner output: %v", outErr)
			}
			s.Repo.UpdateAgentState(ctx, plannerRec.ID, "completed")
		}
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

	branchSetupTask := PlanTask{Agent: "branch-setup", Task: "Initialize branch context based on plan."}
	setupDef, ok := agentMap[branchSetupTask.Agent]
	if !ok {
		return s.failStop(ctx, 1, "agent_not_found", fmt.Errorf("agent %s not found", branchSetupTask.Agent))
	}

	var setupOut string
	agentRec, found := agentStateMap[branchSetupTask.Agent]
	if !found {
		return s.failStop(ctx, 1, "agent_state_not_found", fmt.Errorf("agent state for %s not found", branchSetupTask.Agent))
	}

	if agentRec.State != "completed" {
		setupWrapper, err := s.AcpFactory(ctx, setupDef.LaunchConfig.Binary, setupDef.LaunchConfig.Args, s.EventBus, s.Workplace, branchSetupTask.Agent)
		if err != nil {
			return s.failStop(ctx, 1, "launch_failed", err)
		}

		setupOut, err = setupWrapper.Prompt(ctx, fmt.Sprintf("Task: %s\n\nPrevious context/output: %s", branchSetupTask.Task, rawJSON))
		setupWrapper.Close()
		if err != nil {
			return s.failStop(ctx, 1, "execution_failed", err)
		}

		if outErr := s.Repo.UpdateAgentOutput(ctx, agentRec.ID, setupOut); outErr != nil {
			log.Printf("Failed to checkpoint agent output: %v", outErr)
		}
		s.Repo.UpdateAgentState(ctx, agentRec.ID, "completed")
	} else {
		setupOut = agentRec.Output
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

func (s *Supervisor) failStop(ctx context.Context, stepIndex int, status string, originalErr error) error {
	if s.Repo != nil {
		_ = s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, stepIndex, status, originalErr.Error())
	}
	return fmt.Errorf("fail-stop at step %d: %w", stepIndex, originalErr)
}
