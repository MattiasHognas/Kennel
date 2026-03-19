package model

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func waitForActivity(source ActivitySource) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-source.channel
		if !ok {
			return nil
		}
		return activityMsg{source: source, text: fmt.Sprint(event.Payload)}
	}
}

func (m *Model) BuildActivitySources() []ActivitySource {
	sources := make([]ActivitySource, 0)
	for projectIndex := range m.projects {
		for agentIndex, agentInstance := range m.projects[projectIndex].Agents {
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
	if source.agentIndex < 0 || source.agentIndex >= len(project.Agents) {
		return
	}

	activityText := fmt.Sprintf("%s: %s", project.Agents[source.agentIndex].Name(), text)
	project.Activities = append(project.Activities, ActivityEntry{
		Timestamp: time.Now().Format("15:04:05"),
		Text:      activityText,
	})
	m.persistActivity(project, source.agentIndex, activityText)

	m.refreshProjectAndSelection(source.projectIndex)
}
