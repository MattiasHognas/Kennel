package model

import (
	"fmt"
	"strings"

	repository "MattiasHognas/Kennel/internal/data"
	"MattiasHognas/Kennel/internal/supervisor"
	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"
)

const nonSelectableAgentIndex = -1

type planRowKind int

const (
	planRowNone planRowKind = iota
	planRowAgent
	planRowStream
)

type agentTableEntry struct {
	Kind        planRowKind
	AgentIndex  int
	StreamIndex int
}

type runtimeAgentEntry struct {
	AgentIndex int
	Name       string
	State      string
}

func RestorePlanFromStoredAgents(agents []repository.Agent) *supervisor.Plan {
	for _, storedAgent := range agents {
		if supervisor.CanonicalAgentName(storedAgent.Name) != "planner" {
			continue
		}
		if strings.TrimSpace(storedAgent.Output) == "" {
			continue
		}

		plan, err := supervisor.ParsePlanOutput(storedAgent.Output)
		if err != nil {
			return nil
		}
		return &plan
	}

	return nil
}

func buildAgentTableRows(agents []agent.AgentContract, plan *supervisor.Plan, collapsedStreams map[int]bool) ([]table.Row, []agentTableEntry) {
	if plan == nil || len(plan.Streams) == 0 {
		return buildFlatAgentTableRows(agents)
	}

	lookup := buildRuntimeAgentLookup(agents)
	plannedAgents := collectPlannedAgents(plan)
	rows, rowEntries := buildUnplannedAgentRows(agents, plannedAgents)

	for streamIndex, stream := range plan.Streams {
		collapsed := collapsedStreams[streamIndex]
		toggle := "[-]"
		if collapsed {
			toggle = "[+]"
		}
		rows = append(rows, table.Row{"", fmt.Sprintf("%s Stream %d (%d tasks)", toggle, streamIndex+1, len(stream))})
		rowEntries = append(rowEntries, agentTableEntry{Kind: planRowStream, AgentIndex: nonSelectableAgentIndex, StreamIndex: streamIndex})

		if collapsed {
			continue
		}

		for stepIndex, step := range stream {
			runtimeEntry, found := lookup[supervisor.CanonicalAgentName(step.Agent)]
			rowState := "-"
			displayName := strings.TrimSpace(step.Agent)
			agentIndex := nonSelectableAgentIndex
			if found {
				rowState = runtimeEntry.State
				displayName = runtimeEntry.Name
				agentIndex = runtimeEntry.AgentIndex
			}

			label := fmt.Sprintf("%d. %s", stepIndex+1, displayName)
			if task := strings.TrimSpace(step.Task); task != "" {
				label = fmt.Sprintf("%s - %s", label, task)
			}

			rows = append(rows, table.Row{rowState, indentLabel(1, label)})
			rowEntries = append(rowEntries, agentTableEntry{Kind: planRowAgent, AgentIndex: agentIndex, StreamIndex: streamIndex})
		}
	}

	return rows, rowEntries
}

func buildFlatAgentTableRows(agents []agent.AgentContract) ([]table.Row, []agentTableEntry) {
	rows := make([]table.Row, 0, len(agents))
	rowEntries := make([]agentTableEntry, 0, len(agents))
	for index, agentInstance := range agents {
		rows = append(rows, table.Row{agentInstance.State().String(), agentInstance.Name()})
		rowEntries = append(rowEntries, agentTableEntry{Kind: planRowAgent, AgentIndex: index, StreamIndex: -1})
	}
	return rows, rowEntries
}

func buildRuntimeAgentLookup(agents []agent.AgentContract) map[string]runtimeAgentEntry {
	lookup := make(map[string]runtimeAgentEntry, len(agents))
	for index, agentInstance := range agents {
		canonicalName := supervisor.CanonicalAgentName(agentInstance.Name())
		if canonicalName == "" {
			continue
		}
		if _, exists := lookup[canonicalName]; exists {
			continue
		}

		lookup[canonicalName] = runtimeAgentEntry{
			AgentIndex: index,
			Name:       agentInstance.Name(),
			State:      agentInstance.State().String(),
		}
	}
	return lookup
}

func collectPlannedAgents(plan *supervisor.Plan) map[string]struct{} {
	plannedAgents := make(map[string]struct{})
	if plan == nil {
		return plannedAgents
	}

	for _, stream := range plan.Streams {
		for _, step := range stream {
			canonicalName := supervisor.CanonicalAgentName(step.Agent)
			if canonicalName == "" {
				continue
			}
			plannedAgents[canonicalName] = struct{}{}
		}
	}

	return plannedAgents
}

func buildUnplannedAgentRows(agents []agent.AgentContract, plannedAgents map[string]struct{}) ([]table.Row, []agentTableEntry) {
	rows := make([]table.Row, 0, len(agents))
	rowEntries := make([]agentTableEntry, 0, len(agents))
	for index, agentInstance := range agents {
		if _, planned := plannedAgents[supervisor.CanonicalAgentName(agentInstance.Name())]; planned {
			continue
		}

		rows = append(rows, table.Row{agentInstance.State().String(), agentInstance.Name()})
		rowEntries = append(rowEntries, agentTableEntry{Kind: planRowAgent, AgentIndex: index, StreamIndex: -1})
	}

	return rows, rowEntries
}

func indentLabel(depth int, label string) string {
	if depth <= 0 {
		return label
	}
	return strings.Repeat("  ", depth) + label
}

func (m *Model) rowIndexForAgentIndex(agentIndex int) int {
	for rowIndex, entry := range m.agentTableEntries {
		if entry.Kind == planRowAgent && entry.AgentIndex == agentIndex {
			return rowIndex
		}
	}
	return -1
}

func (m *Model) rowIndexForStreamIndex(streamIndex int) int {
	for rowIndex, entry := range m.agentTableEntries {
		if entry.Kind == planRowStream && entry.StreamIndex == streamIndex {
			return rowIndex
		}
	}
	return -1
}

func (m *Model) selectedAgentTableEntry() agentTableEntry {
	rowIndex := m.agentTable.Cursor()
	if rowIndex < 0 || rowIndex >= len(m.agentTableEntries) {
		return agentTableEntry{Kind: planRowNone, AgentIndex: nonSelectableAgentIndex, StreamIndex: -1}
	}
	return m.agentTableEntries[rowIndex]
}

func (m *Model) setAgentTableCursorForEntry(entry agentTableEntry) {
	if entry.Kind == planRowStream {
		if rowIndex := m.rowIndexForStreamIndex(entry.StreamIndex); rowIndex >= 0 {
			m.agentTable.SetCursor(rowIndex)
			return
		}
	}

	if entry.Kind == planRowAgent {
		if rowIndex := m.rowIndexForAgentIndex(entry.AgentIndex); rowIndex >= 0 {
			m.agentTable.SetCursor(rowIndex)
			return
		}
	}

	if rowIndex := m.rowIndexForAgentIndex(entry.AgentIndex); rowIndex >= 0 {
		m.agentTable.SetCursor(rowIndex)
		return
	}

	m.ensurePlanTableCursor()
}

func (m *Model) ensurePlanTableCursor() {
	if len(m.agentTableEntries) == 0 {
		m.agentTable.SetCursor(0)
		return
	}

	cursor := m.agentTable.Cursor()
	if cursor >= 0 && cursor < len(m.agentTableEntries) {
		return
	}

	m.agentTable.SetCursor(0)
}
