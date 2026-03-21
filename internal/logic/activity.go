package model

import (
	eventbus "MattiasHognas/Kennel/internal/events"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

const supervisorPollInterval = 100 * time.Millisecond

func waitForActivity(source ActivitySource) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-source.channel
		if !ok {
			return nil
		}
		var text string
		switch p := event.Payload.(type) {
		case eventbus.WorkerMessageEvent:
			text = p.Chunk
		case eventbus.WorkerCancellationEvent:
			text = p.Reason
		case eventbus.WorkerCompletionEvent:
			text = p.Result
		case eventbus.PlanUpdateEvent:
			text = "Plan updated"
		case string:
			text = p
		default:
			text = fmt.Sprintf("%v", event.Payload)
		}
		return activityMsg{source: source, text: text}
	}
}

func (m *Model) BuildActivitySources() []ActivitySource {
	sources := make([]ActivitySource, 0)
	for projectIndex := range m.projects {
		for agentIndex, agentInstance := range m.projects[projectIndex].Runtime.Agents {
			sources = append(sources, ActivitySource{
				projectIndex: projectIndex,
				agentIndex:   agentIndex,
				channel:      agentInstance.SubscribeActivity(),
			})
		}
	}
	return sources
}

func (m *Model) recordActivity(source ActivitySource, text string) {
	if source.projectIndex < 0 || source.projectIndex >= len(m.projects) {
		return
	}

	project := &m.projects[source.projectIndex]
	if source.agentIndex < 0 || source.agentIndex >= len(project.Runtime.Agents) {
		return
	}

	activityText := fmt.Sprintf("%s: %s", project.Runtime.Agents[source.agentIndex].Name(), text)
	project.Runtime.Activities = append(project.Runtime.Activities, ActivityEntry{
		Timestamp: time.Now().Format("15:04:05"),
		Text:      activityText,
	})
	m.persistActivity(project, source.agentIndex, activityText)

	m.refreshProjectAndSelection(source.projectIndex)
}

func waitForSupervisorUpdate(source supervisorSource) tea.Cmd {
	return tea.Tick(supervisorPollInterval, func(time.Time) tea.Msg {
		select {
		case _, ok := <-source.channel:
			if !ok {
				return nil
			}
			return supervisorSyncMsg{source: source}
		default:
			return supervisorPollMsg{source: source}
		}
	})
}
