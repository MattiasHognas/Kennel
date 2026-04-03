package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

func defaultAgentsDir() string {
	if override := os.Getenv("KENNEL_ROOT_DIR"); override != "" {
		return override
	}

	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		agentsPath := filepath.Join(exeDir, "agents")
		if info, statErr := os.Stat(agentsPath); statErr == nil && info.IsDir() {
			return exeDir
		}
	}

	if wd, err := os.Getwd(); err == nil {
		return wd
	}

	return "."
}

func (m *Model) startSelectedProject() tea.Cmd {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State.State == workers.Running || project.State.State == workers.Completed {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	runResult := make(chan error, 1)
	project.Runtime.CancelCtx = cancel
	project.Runtime.SupervisorDone = runDone
	project.Runtime.SupervisorResult = runResult

	eb := data.NewEventBus()
	source := supervisorSource{projectIndex: projectIndex, channel: eb.Subscribe(data.SupervisorTopic), done: runDone, result: runResult}
	supervisorFactory := m.supervisorFactory
	if supervisorFactory == nil {
		supervisorFactory = NewSupervisor
	}
	sup := supervisorFactory(m.repository, eb, defaultAgentsDir(), project.Config.ProjectID, project.Config.Name, project.Config.Workplace)
	sup.EventBus = eb
	project.Runtime.Supervisor = sup
	project.Runtime.Logger = sup.Logger
	project.Runtime.SupervisorEvents = source.channel

	var configuredAgents []string
	for _, agentInstance := range project.Runtime.Agents {
		configuredAgents = append(configuredAgents, agentInstance.Name())
	}

	supervisorRunner := m.supervisorRunner
	if supervisorRunner == nil {
		supervisorRunner = func(ctx context.Context, supervisor *Supervisor, instructions string, configuredAgents []string) error {
			return supervisor.RunPlan(ctx, instructions, configuredAgents)
		}
	}

	go func() {
		defer close(runDone)
		runResult <- supervisorRunner(ctx, sup, project.Config.Instructions, configuredAgents)
		close(runResult)
	}()

	project.State.State = workers.Running
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)

	return waitForSupervisorUpdate(source)
}

func (m *Model) clearProjectSupervisor(project *Project) {
	if project == nil {
		return
	}

	if project.Runtime.CancelCtx != nil {
		project.Runtime.CancelCtx()
		project.Runtime.CancelCtx = nil
	}
	project.Runtime.SupervisorDone = nil
	project.Runtime.SupervisorResult = nil

	if project.Runtime.SupervisorEvents != nil {
		if project.Runtime.Supervisor != nil {
			project.Runtime.Supervisor.EventBus.Unsubscribe(data.SupervisorTopic, project.Runtime.SupervisorEvents)
		}
	}
	project.Runtime.SupervisorEvents = nil
	project.Runtime.Supervisor = nil
}

func (m *Model) failProjectAtIndex(projectIndex int) []ActivitySource {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return nil
	}

	project := &m.projects[projectIndex]
	m.cancelProjectAgentRuns(project)
	m.clearProjectSupervisor(project)

	if m.repository != nil && project.Config.ProjectID > 0 {
		return m.syncProjectFromRepository(projectIndex)
	}

	project.State.State = workers.Stopped
	m.refreshProjectAndSelection(projectIndex)
	return nil
}

func (m *Model) stopSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State.State == workers.Stopped || project.State.State == workers.Completed {
		return
	}

	if project.Runtime.CancelCtx != nil {
		project.Runtime.CancelCtx()
		project.Runtime.CancelCtx = nil
	}
	m.cancelProjectAgentRuns(project)
	project.Runtime.SupervisorDone = nil
	project.Runtime.SupervisorResult = nil

	if project.Runtime.ActivityCancel != nil {
		project.Runtime.ActivityCancel()
		project.Runtime.ActivityCancel = nil
		project.Runtime.ActivityDone = nil
	}

	if project.Runtime.Supervisor != nil {
		if project.Runtime.SupervisorEvents != nil {
			project.Runtime.Supervisor.EventBus.Unsubscribe(data.SupervisorTopic, project.Runtime.SupervisorEvents)
			project.Runtime.SupervisorEvents = nil
		}
		project.Runtime.Supervisor = nil
	}

	for _, agentInstance := range project.Runtime.Agents {
		agentInstance.Stop()
	}

	project.State.State = workers.Stopped
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) completeProjectAtIndex(projectIndex int) {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return
	}

	project := &m.projects[projectIndex]
	if project.State.State == workers.Completed {
		return
	}

	if project.Runtime.CancelCtx != nil {
		project.Runtime.CancelCtx()
		project.Runtime.CancelCtx = nil
	}
	m.cancelProjectAgentRuns(project)
	project.Runtime.SupervisorDone = nil
	project.Runtime.SupervisorResult = nil
	if project.Runtime.ActivityCancel != nil {
		project.Runtime.ActivityCancel()
		project.Runtime.ActivityCancel = nil
		project.Runtime.ActivityDone = nil
	}
	if project.Runtime.Supervisor != nil {
		if project.Runtime.SupervisorEvents != nil {
			project.Runtime.Supervisor.EventBus.Unsubscribe(data.SupervisorTopic, project.Runtime.SupervisorEvents)
			project.Runtime.SupervisorEvents = nil
		}
		project.Runtime.Supervisor = nil
	}

	for _, agentInstance := range project.Runtime.Agents {
		agentInstance.Complete()
	}
	project.State.State = workers.Completed
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) startSelectedAgent() tea.Cmd {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	agentInstance := m.selectedAgent()
	if project == nil || agentInstance == nil || agentInstance.State() == workers.Running || agentInstance.State() == workers.Completed {
		return nil
	}

	agentIndex := m.selectedAgentIndex()
	if project.Runtime.AgentRunResults != nil {
		if _, alreadyRunning := project.Runtime.AgentRunResults[agentIndex]; alreadyRunning {
			return nil
		}
	}

	execution, err := m.buildSelectedAgentExecution(project)
	if err != nil {
		agentInstance.Run(context.Background())
		m.persistProjectAgentStates(project)
		m.refreshProjectAndSelection(projectIndex)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan agentRunResult, 1)
	if project.Runtime.AgentRunCancels == nil {
		project.Runtime.AgentRunCancels = make(map[int]context.CancelFunc)
	}
	if project.Runtime.AgentRunResults == nil {
		project.Runtime.AgentRunResults = make(map[int]chan agentRunResult)
	}
	project.Runtime.AgentRunCancels[agentIndex] = cancel
	project.Runtime.AgentRunResults[agentIndex] = result

	agentInstance.Run(context.Background())
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)

	executor := m.agentExecutor
	if executor == nil {
		executor = defaultAgentExecutor
	}
	logger := m.ensureProjectLogger(project)
	workplace := project.Config.Workplace
	agentName := agentInstance.Name()
	go func() {
		defer close(result)
		output, runErr := executor(ctx, execution.Definition, workplace, agentName, execution.Prompt, logger)
		result <- agentRunResult{Output: output, Err: runErr}
	}()

	return waitForAgentRun(agentRunSource{projectIndex: projectIndex, agentIndex: agentIndex, result: result})
}

func (m *Model) stopSelectedAgent() {
	project := m.selectedProject()
	agentInstance := m.selectedAgent()
	if project == nil || agentInstance == nil || agentInstance.State() == workers.Stopped || agentInstance.State() == workers.Completed {
		return
	}

	m.cancelAgentRun(project, m.selectedAgentIndex())
	agentInstance.Stop()
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(m.selectedProjectIndex())
}

func (m *Model) cycleSelectedProjectState() tea.Cmd {
	project := m.selectedProject()
	if project == nil {
		return nil
	}
	if project.State.State == workers.Completed {
		return nil
	}

	switch project.State.State {
	case workers.Stopped:
		return m.startSelectedProject()
	default:
		m.stopSelectedProject()
		return nil
	}
}

func (m *Model) cycleSelectedAgentState() tea.Cmd {
	agentInstance := m.selectedAgent()
	if agentInstance == nil {
		return nil
	}
	if agentInstance.State() == workers.Completed {
		return nil
	}

	switch agentInstance.State() {
	case workers.Stopped, workers.Failed:
		return m.startSelectedAgent()
	default:
		m.stopSelectedAgent()
		return nil
	}
}
