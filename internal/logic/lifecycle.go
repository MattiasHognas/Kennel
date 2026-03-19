package model

import (
	agent "MattiasHognas/Kennel/internal/workers"
)

func (m *Model) startSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State == agent.Running || project.State == agent.Completed {
		return
	}
	if len(project.Agents) == 0 {
		project.State = agent.Running
		m.persistProjectState(project)
		m.refreshProjectAndSelection(projectIndex)
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Run()
	}
	project.State = agent.Running
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) stopSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State == agent.Stopped || project.State == agent.Completed {
		return
	}
	if len(project.Agents) == 0 {
		project.State = agent.Stopped
		m.persistProjectState(project)
		m.refreshProjectAndSelection(projectIndex)
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Stop()
	}
	project.State = agent.Stopped
	m.persistProjectState(project)
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) completeSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State == agent.Completed {
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Complete()
	}
	project.State = agent.Completed
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

	agentInstance.Run()
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

func (m *Model) cycleSelectedProjectState() {
	project := m.selectedProject()
	if project == nil {
		return
	}
	if project.State == agent.Completed {
		return
	}

	switch project.State {
	case agent.Stopped:
		m.startSelectedProject()
	default:
		m.stopSelectedProject()
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
