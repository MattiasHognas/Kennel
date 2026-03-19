package supervisor

import (
	"context"
	"fmt"

	"MattiasHognas/Kennel/internal/acp"
	"MattiasHognas/Kennel/internal/discovery"
	eventbus "MattiasHognas/Kennel/internal/events"
)

type Repository interface {
	CheckpointSupervisorRun(ctx context.Context, projectID int64, stepIndex int, status, data string) error
}

type ACPClient interface {
	Prompt(ctx context.Context, msg string) (string, error)
	Close() error
}

type ACPFactory func(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, topic string) (ACPClient, error)

func DefaultACPFactory(ctx context.Context, binary string, args []string, eb *eventbus.EventBus, topic string) (ACPClient, error) {
	return acp.NewWrapper(ctx, binary, args, eb, topic)
}

type Supervisor struct {
	Repo        Repository
	EventBus    *eventbus.EventBus
	AgentsDir   string
	ProjectID   int64
	ProjectName string
	AcpFactory  ACPFactory
}

func NewSupervisor(repo Repository, eb *eventbus.EventBus, agentsDir string, projectID int64, projectName string) *Supervisor {
	return &Supervisor{
		Repo:        repo,
		EventBus:    eb,
		AgentsDir:   agentsDir,
		ProjectID:   projectID,
		ProjectName: projectName,
		AcpFactory:  DefaultACPFactory,
	}
}

func (s *Supervisor) RunPlan(ctx context.Context, instructions string, configuredAgents []string) error {
	// 1. Load agent definitions
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

	plannerWrapper, err := s.AcpFactory(ctx, plannerDef.LaunchConfig.Binary, plannerDef.LaunchConfig.Args, s.EventBus, "planner")
	if err != nil {
		return s.failStop(ctx, 0, "planning_launch_failed", err)
	}
	defer plannerWrapper.Close()

	planPrompt := fmt.Sprintf("Create an execution plan based on these instructions: %s", instructions)
	planOutput, err := plannerWrapper.Prompt(ctx, planPrompt)
	if err != nil {
		return s.failStop(ctx, 0, "planning_failed", err)
	}

	runSequence := append([]string{"branch-setup"}, configuredAgents...)
	currentPrompt := planOutput

	for i, stepName := range runSequence {
		def, ok := agentMap[stepName]
		if !ok {
			return s.failStop(ctx, i+1, "agent_not_found", fmt.Errorf("agent %s not found", stepName))
		}

		wrapper, err := s.AcpFactory(ctx, def.LaunchConfig.Binary, def.LaunchConfig.Args, s.EventBus, stepName)
		if err != nil {
			return s.failStop(ctx, i+1, "launch_failed", err)
		}

		out, err := wrapper.Prompt(ctx, currentPrompt)
		if err != nil {
			wrapper.Close()
			return s.failStop(ctx, i+1, "execution_failed", err)
		}

		wrapper.Close()
		currentPrompt = out

		if saveErr := s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, i+1, "completed", out); saveErr != nil {
			return saveErr
		}
	}

	return nil
}

func (s *Supervisor) failStop(ctx context.Context, stepIndex int, status string, originalErr error) error {
	if s.Repo != nil {
		_ = s.Repo.CheckpointSupervisorRun(ctx, s.ProjectID, stepIndex, status, originalErr.Error())
	}
	return fmt.Errorf("fail-stop at step %d: %w", stepIndex, originalErr)
}
