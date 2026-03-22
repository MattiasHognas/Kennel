package model

import (
	"context"

	eventbus "MattiasHognas/Kennel/internal/events"
	"MattiasHognas/Kennel/internal/supervisor"
	agent "MattiasHognas/Kennel/internal/workers"

	tea "charm.land/bubbletea/v2"
)

const agentsDir = "C:\\source\\Kennel\\"

func (m *Model) startSelectedProject() tea.Cmd {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State.State == agent.Running || project.State.State == agent.Completed {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	project.Runtime.CancelCtx = cancel
	project.Runtime.SupervisorDone = ctx.Done()

	eb := eventbus.NewEventBus()
	source := supervisorSource{projectIndex: projectIndex, channel: eb.Subscribe(eventbus.SupervisorTopic), done: ctx.Done()}
	sup := supervisor.NewSupervisor(m.repository, eb, agentsDir, project.Config.ProjectID, project.Config.Name, project.Config.Workplace)
	project.Runtime.Supervisor = sup
	project.Runtime.SupervisorEvents = source.channel

	var configuredAgents []string
	for _, agentInstance := range project.Runtime.Agents {
		configuredAgents = append(configuredAgents, agentInstance.Name())
	}

	go func() {
		_ = sup.RunPlan(ctx, project.Config.Instructions, configuredAgents)
	}()

	project.State.State = agent.Running
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)

	return waitForSupervisorUpdate(source)
}

func (m *Model) stopSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State.State == agent.Stopped || project.State.State == agent.Completed {
		return
	}

	if project.Runtime.CancelCtx != nil {
		project.Runtime.CancelCtx()
		project.Runtime.CancelCtx = nil
		project.Runtime.SupervisorDone = nil
	}
	// Cancel and clear any per-project activity listener context to avoid leaking goroutines.
	if project.Runtime.ActivityCancel != nil {
		project.Runtime.ActivityCancel()
		project.Runtime.ActivityCancel = nil
		project.Runtime.ActivityDone = nil
	}
	if project.Runtime.Supervisor != nil {
		if project.Runtime.SupervisorEvents != nil {
			project.Runtime.Supervisor.EventBus.Unsubscribe(eventbus.SupervisorTopic, project.Runtime.SupervisorEvents)
			project.Runtime.SupervisorEvents = nil
		}
		project.Runtime.Supervisor = nil
	}

	// Currently keep old logic to stop individual agents if they have their own routines running outside the supervisor for now.
	for _, agentInstance := range project.Runtime.Agents {
		agentInstance.Stop()
	}

	project.State.State = agent.Stopped
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) completeSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State.State == agent.Completed {
		return
	}

	if project.Runtime.CancelCtx != nil {
		project.Runtime.CancelCtx()
		project.Runtime.CancelCtx = nil
		project.Runtime.SupervisorDone = nil
	}
	if project.Runtime.ActivityCancel != nil {
		project.Runtime.ActivityCancel()
		project.Runtime.ActivityCancel = nil
		project.Runtime.ActivityDone = nil
	}
	if project.Runtime.Supervisor != nil {
		if project.Runtime.SupervisorEvents != nil {
			project.Runtime.Supervisor.EventBus.Unsubscribe(eventbus.SupervisorTopic, project.Runtime.SupervisorEvents)
			project.Runtime.SupervisorEvents = nil
		}
		project.Runtime.Supervisor = nil
	}

	for _, agentInstance := range project.Runtime.Agents {
		agentInstance.Complete()
	}
	project.State.State = agent.Completed
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) startSelectedAgent() {
	project := m.selectedProject()
	agentInstance := m.selectedAgent()
	if project == nil || agentInstance == nil || agentInstance.State() == agent.Running || agentInstance.State() == agent.Completed {
		return
	}

	agentInstance.Run(context.Background())
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(m.selectedProjectIndex())
}

func (m *Model) stopSelectedAgent() {
	project := m.selectedProject()
	agentInstance := m.selectedAgent()
	if project == nil || agentInstance == nil || agentInstance.State() == agent.Stopped || agentInstance.State() == agent.Completed {
		return
	}

	agentInstance.Stop()
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(m.selectedProjectIndex())
}

func (m *Model) completeSelectedAgent() {
	project := m.selectedProject()
	agentInstance := m.selectedAgent()
	if project == nil || agentInstance == nil || agentInstance.State() == agent.Completed {
		return
	}

	agentInstance.Complete()
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(m.selectedProjectIndex())
}

func (m *Model) cycleSelectedProjectState() tea.Cmd {
	project := m.selectedProject()
	if project == nil {
		return nil
	}
	if project.State.State == agent.Completed {
		return nil
	}

	switch project.State.State {
	case agent.Stopped:
		return m.startSelectedProject()
	default:
		m.stopSelectedProject()
		return nil
	}
}

func (m *Model) cycleSelectedAgentState() {
	agentInstance := m.selectedAgent()
	if agentInstance == nil {
		return
	}
	if agentInstance.State() == agent.Completed {
		return
	}

	switch agentInstance.State() {
	case agent.Stopped:
		m.startSelectedAgent()
	default:
		m.stopSelectedAgent()
	}
}
