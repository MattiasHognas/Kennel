package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type AgentExecutor func(ctx context.Context, definition data.AgentDefinition, workplace string, topic string, prompt string, logger *data.ProjectLogger) (string, error)

type agentRunSource struct {
	projectIndex int
	agentIndex   int
	result       <-chan agentRunResult
}

type agentRunResult struct {
	Output string
	Err    error
}

type manualAgentCompletedMsg struct {
	source agentRunSource
	result agentRunResult
}

type plannedAgentExecution struct {
	Definition data.AgentDefinition
	Prompt     string
}

func defaultAgentExecutor(ctx context.Context, definition data.AgentDefinition, workplace string, topic string, prompt string, logger *data.ProjectLogger) (string, error) {
	client, err := DefaultACPFactory(ctx, definition, data.NewEventBus(), workplace, topic)
	if err != nil {
		return "", err
	}
	defer client.Close()

	if loggerAware, ok := client.(interface{ SetLogger(*data.ProjectLogger) }); ok {
		loggerAware.SetLogger(logger)
	}

	if logger != nil {
		logger.LogAgentInput(topic, prompt)
	}

	output, err := client.Prompt(ctx, prompt)
	if err != nil {
		return "", err
	}

	if logger != nil {
		logger.LogAgentOutput(topic, output)
	}

	return output, nil
}

func waitForAgentRun(source agentRunSource) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-source.result
		if !ok {
			return manualAgentCompletedMsg{source: source}
		}
		return manualAgentCompletedMsg{source: source, result: result}
	}
}

func (m Model) handleAgentRunCompleted(msg manualAgentCompletedMsg) (tea.Model, tea.Cmd) {
	if !m.shouldListenForAgentRun(msg.source) {
		return m, nil
	}

	project := &m.projects[msg.source.projectIndex]
	agentIndex := msg.source.agentIndex
	m.clearAgentRun(project, agentIndex)

	if agentIndex < 0 || agentIndex >= len(project.Runtime.Agents) {
		return m, nil
	}

	if msg.result.Err != nil {
		if errors.Is(msg.result.Err, context.Canceled) {
			return m, nil
		}

		project.Runtime.Agents[agentIndex].Fail(msg.result.Err)
		m.persistProjectAgentStates(project)
		m.refreshProjectAndSelection(msg.source.projectIndex)
		return m, nil
	}

	m.persistAgentOutput(project, agentIndex, msg.result.Output)
	project.Runtime.Agents[agentIndex].Complete()
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(msg.source.projectIndex)
	return m, nil
}

func (m *Model) shouldListenForAgentRun(source agentRunSource) bool {
	if source.projectIndex < 0 || source.projectIndex >= len(m.projects) {
		return false
	}
	if source.agentIndex < 0 {
		return false
	}

	project := &m.projects[source.projectIndex]
	if project.Runtime.AgentRunResults == nil {
		return false
	}

	current, ok := project.Runtime.AgentRunResults[source.agentIndex]
	return ok && current == source.result
}

func (m *Model) clearAgentRun(project *Project, agentIndex int) {
	if project == nil || agentIndex < 0 {
		return
	}

	if project.Runtime.AgentRunCancels != nil {
		delete(project.Runtime.AgentRunCancels, agentIndex)
	}
	if project.Runtime.AgentRunResults != nil {
		delete(project.Runtime.AgentRunResults, agentIndex)
	}
}

func (m *Model) cancelAgentRun(project *Project, agentIndex int) {
	if project == nil || agentIndex < 0 {
		return
	}

	if project.Runtime.AgentRunCancels != nil {
		if cancel, ok := project.Runtime.AgentRunCancels[agentIndex]; ok && cancel != nil {
			cancel()
		}
	}
	m.clearAgentRun(project, agentIndex)
}

func (m *Model) cancelProjectAgentRuns(project *Project) {
	if project == nil || len(project.Runtime.AgentRunCancels) == 0 {
		return
	}

	for agentIndex, cancel := range project.Runtime.AgentRunCancels {
		if cancel != nil {
			cancel()
		}
		delete(project.Runtime.AgentRunCancels, agentIndex)
	}
	for agentIndex := range project.Runtime.AgentRunResults {
		delete(project.Runtime.AgentRunResults, agentIndex)
	}
}

func (m *Model) buildSelectedAgentExecution(project *Project) (plannedAgentExecution, error) {
	if project == nil || project.Runtime.Plan == nil || m.repository == nil || project.Config.ProjectID <= 0 {
		return plannedAgentExecution{}, errors.New("planned agent execution unavailable")
	}

	entry := m.selectedAgentTableEntry()
	step, ok := m.selectedPlanTask(entry, project.Runtime.Plan)
	if !ok {
		return plannedAgentExecution{}, errors.New("selected agent is not a planned task")
	}

	definitions, err := data.LoadAgentDefinitions(defaultAgentsDir())
	if err != nil {
		return plannedAgentExecution{}, fmt.Errorf("load agent definitions: %w", err)
	}

	definition, err := findAgentDefinition(definitions, step.Agent)
	if err != nil {
		return plannedAgentExecution{}, err
	}

	storedProject, err := m.repository.ReadProject(context.Background(), project.Config.ProjectID)
	if err != nil {
		return plannedAgentExecution{}, fmt.Errorf("read project state: %w", err)
	}

	previousOutput := promptSeedForPlanStep(project.Runtime.Plan, storedProject.Agents, entry.StreamIndex, entry.StepIndex)
	return plannedAgentExecution{
		Definition: definition,
		Prompt:     buildTaskPrompt(step.Task, previousOutput, definition.PromptContext.PreviousOutput),
	}, nil
}

func (m *Model) selectedPlanTask(entry agentTableEntry, plan *Plan) (PlanTask, bool) {
	if plan == nil || entry.Kind != planRowAgent || entry.StreamIndex < 0 || entry.StepIndex < 0 {
		return PlanTask{}, false
	}
	if entry.StreamIndex >= len(plan.Streams) {
		return PlanTask{}, false
	}
	stream := plan.Streams[entry.StreamIndex]
	if entry.StepIndex >= len(stream) {
		return PlanTask{}, false
	}
	return stream[entry.StepIndex], true
}

func findAgentDefinition(definitions []data.AgentDefinition, agentName string) (data.AgentDefinition, error) {
	canonicalName := CanonicalAgentName(agentName)
	for _, definition := range definitions {
		if CanonicalAgentName(definition.Name) == canonicalName {
			return definition, nil
		}
	}
	return data.AgentDefinition{}, fmt.Errorf("agent definition not found for %s", agentName)
}

func promptSeedForPlanStep(plan *Plan, storedAgents []data.Agent, streamIndex int, stepIndex int) string {
	seed := storedAgentOutput(storedAgents, branchSetupAgentName)
	if plan == nil || streamIndex < 0 || streamIndex >= len(plan.Streams) || stepIndex < 0 {
		return seed
	}

	stream := plan.Streams[streamIndex]
	limit := min(stepIndex, len(stream))
	for index := 0; index < limit; index++ {
		if output := storedAgentOutput(storedAgents, stream[index].Agent); strings.TrimSpace(output) != "" {
			seed = output
		}
	}

	return seed
}

func storedAgentOutput(storedAgents []data.Agent, agentName string) string {
	canonicalName := CanonicalAgentName(agentName)
	for _, storedAgent := range storedAgents {
		if CanonicalAgentName(storedAgent.Name) != canonicalName {
			continue
		}
		if strings.TrimSpace(storedAgent.Output) == "" {
			return ""
		}
		return storedAgent.Output
	}
	return ""
}
