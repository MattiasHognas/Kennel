package model

import (
	eventbus "MattiasHognas/Kennel/internal/events"
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func waitForActivity(source ActivitySource) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-source.done:
			return nil
		case event, ok := <-source.channel:
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
			case eventbus.WorkerFailureEvent:
				if p.Error != nil {
					text = p.Error.Error()
				} else {
					text = "failed"
				}
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
}

func (m *Model) BuildActivitySources() []ActivitySource {
	sources := make([]ActivitySource, 0)
	for projectIndex := range m.projects {
		m.ensureActivityListener(projectIndex)
		for agentIndex, agentInstance := range m.projects[projectIndex].Runtime.Agents {
			sources = append(sources, ActivitySource{
				projectIndex: projectIndex,
				agentIndex:   agentIndex,
				channel:      agentInstance.SubscribeActivity(),
				done:         m.projects[projectIndex].Runtime.ActivityDone,
			})
		}
	}
	return sources
}

func (m *Model) BuildSupervisorSources() []supervisorSource {
	sources := make([]supervisorSource, 0)
	for projectIndex := range m.projects {
		channel := m.projects[projectIndex].Runtime.SupervisorEvents
		if channel == nil {
			continue
		}

		sources = append(sources, supervisorSource{
			projectIndex: projectIndex,
			channel:      channel,
			done:         m.projects[projectIndex].Runtime.SupervisorDone,
			result:       m.projects[projectIndex].Runtime.SupervisorResult,
		})
	}
	return sources
}

func (m *Model) initializeActivityListeners() {
	for projectIndex := range m.projects {
		m.ensureActivityListener(projectIndex)
	}
}

func (m *Model) ensureActivityListener(projectIndex int) {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return
	}

	project := &m.projects[projectIndex]
	if project.Runtime.ActivityDone != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	project.Runtime.ActivityCancel = cancel
	project.Runtime.ActivityDone = ctx.Done()
}

func (m *Model) resetActivitySourcesForProject(projectIndex int) []ActivitySource {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return nil
	}

	project := &m.projects[projectIndex]
	ctx, cancel := context.WithCancel(context.Background())
	project.Runtime.ActivityCancel = cancel
	project.Runtime.ActivityDone = ctx.Done()

	m.Sources = m.BuildActivitySources()

	sources := make([]ActivitySource, 0, len(project.Runtime.Agents))
	for _, source := range m.Sources {
		if source.projectIndex == projectIndex {
			sources = append(sources, source)
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
	return func() tea.Msg {
		select {
		case err, ok := <-source.result:
			if !ok {
				return supervisorCompletedMsg{source: source}
			}
			return supervisorCompletedMsg{source: source, err: err}
		case <-source.done:
			if source.result != nil {
				select {
				case err, ok := <-source.result:
					if ok {
						return supervisorCompletedMsg{source: source, err: err}
					}
				default:
				}
			}
			return supervisorCompletedMsg{source: source}
		case event, ok := <-source.channel:
			if !ok {
				return nil
			}

			if planEvent, ok := event.Payload.(eventbus.PlanUpdateEvent); ok {
				return supervisorPlanMsg{source: source, plan: planEvent.Plan}
			}

			syncEvent, ok := event.Payload.(eventbus.SupervisorSyncEvent)
			if !ok {
				return supervisorSyncMsg{source: source}
			}

			return supervisorSyncMsg{source: source, event: syncEvent}
		}
	}
}
