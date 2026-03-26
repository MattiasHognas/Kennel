package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	table "MattiasHognas/Kennel/internal/ui/table"
	workers "MattiasHognas/Kennel/internal/workers"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPlanTableRendersStreamsAndTogglesCollapse(t *testing.T) {
	planner := workers.NewAgent("planner")
	backend := workers.NewAgent("backend-developer")
	tester := workers.NewAgent("tester")

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		State: ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Agents: []workers.AgentContract{planner, backend, tester},
			Plan: &Plan{Streams: []TaskStream{{
				{Agent: "backend-developer", Task: "Build API"},
				{Agent: "tester", Task: "Run tests"},
			}}},
		},
	}}, nil)
	m.ResizeTables(220, 18)
	m.projectTable.SetCursor(1)
	m.refreshSelectedProjectTables()
	m.SetFocus(1)

	if got := m.selectedAgentIndex(); got != 0 {
		t.Fatalf("selected agent index = %d, want 0 for planner", got)
	}

	view := m.agentTable.View()
	for _, fragment := range []string{"planner", "[-] Stream 1 (2 tasks)", "  1. backend-developer", "  2. tester - Run tests"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("agent table view missing %q:\n%s", fragment, view)
		}
	}

	m.agentTable.SetCursor(1)
	if got := m.selectedStreamIndex(); got != 0 {
		t.Fatalf("selected stream index = %d, want 0", got)
	}

	updatedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		t.Fatalf("unexpected command after collapsing stream: %#v", cmd)
	}
	collapsed, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}

	collapsedView := collapsed.agentTable.View()
	if !strings.Contains(collapsedView, "[+] Stream 1 (2 tasks)") {
		t.Fatalf("collapsed plan view missing collapsed stream header:\n%s", collapsedView)
	}
	if strings.Contains(collapsedView, "backend-developer - Build API") || strings.Contains(collapsedView, "tester - Run tests") {
		t.Fatalf("collapsed plan view still shows stream tasks:\n%s", collapsedView)
	}
	if got := collapsed.selectedStreamIndex(); got != 0 {
		t.Fatalf("selected stream after collapse = %d, want 0", got)
	}

	collapsed.agentTable.SetCursor(0)
	if got := collapsed.selectedAgentIndex(); got != 0 {
		t.Fatalf("selected agent index on planner row = %d, want 0", got)
	}

	selectedRow := collapsed.agentTable.SelectedRow()
	if len(selectedRow) < 2 || !strings.Contains(selectedRow[1], "planner") {
		t.Fatalf("selected row = %#v, want planner row", selectedRow)
	}
}

func TestRestorePlanFromStoredAgentsParsesPlannerOutput(t *testing.T) {
	plan := RestorePlanFromStoredAgents([]data.Agent{{
		Name:   "planner",
		Output: "```json\n{\"streams\":[[{\"agent\":\"tester\",\"task\":\"Run tests\"}]]}\n```",
	}})

	if plan == nil {
		t.Fatal("expected stored planner output to restore a plan")
	}
	if len(plan.Streams) != 1 || len(plan.Streams[0]) != 1 {
		t.Fatalf("restored plan = %#v, want one stream with one task", plan)
	}
	if plan.Streams[0][0].Agent != "tester" || plan.Streams[0][0].Task != "Run tests" {
		t.Fatalf("restored task = %#v, want tester/Run tests", plan.Streams[0][0])
	}
}
