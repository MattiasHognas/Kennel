package logic

import (
	data "MattiasHognas/Kennel/internal/data"
	table "MattiasHognas/Kennel/internal/ui/table"
	workers "MattiasHognas/Kennel/internal/workers"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

type integrationModelOptions struct {
	repo              *data.SQLiteRepository
	projects          []Project
	agentNames        []string
	supervisorFactory supervisorFactory
	supervisorRunner  supervisorRunner
}

func TestKeyboardNavigationSwitchesFocusedTables(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, runtimeProject := newStoredProjectForModel(t, repo, "Navigation Project", "frontend-developer")

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:       repo,
		projects:   []Project{runtimeProject},
		agentNames: []string{"frontend-developer"},
	})
	m.projectTable.SetCursor(1)
	m.refreshSelectedProjectTables()

	if m.focusIndex != 0 {
		t.Fatalf("initial focus = %d, want 0", m.focusIndex)
	}
	assertProjectRow(t, &m, 1, storedProject.Name, workers.Stopped.String())

	sendKey(t, &m, tea.Key{Code: tea.KeyTab})
	if m.focusIndex != 1 {
		t.Fatalf("focus after tab = %d, want 1", m.focusIndex)
	}

	sendKey(t, &m, tea.Key{Code: tea.KeyTab})
	if m.focusIndex != 2 {
		t.Fatalf("focus after second tab = %d, want 2", m.focusIndex)
	}

	sendKey(t, &m, tea.Key{Code: tea.KeyTab})
	if m.focusIndex != 0 {
		t.Fatalf("focus after third tab = %d, want 0", m.focusIndex)
	}

	sendKey(t, &m, tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.focusIndex != 2 {
		t.Fatalf("focus after shift+tab = %d, want 2", m.focusIndex)
	}
}

func TestKeyboardNavigationMovesProjectCursor(t *testing.T) {
	repo := newTestRepository(t)
	first, firstRuntime := newStoredProjectForModel(t, repo, "Project One")
	second, secondRuntime := newStoredProjectForModel(t, repo, "Project Two")
	third, thirdRuntime := newStoredProjectForModel(t, repo, "Project Three")

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:     repo,
		projects: []Project{firstRuntime, secondRuntime, thirdRuntime},
	})

	if m.projectTable.Cursor() != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.projectTable.Cursor())
	}
	assertProjectRow(t, &m, 0, createProjectRowName, "new")

	sendKey(t, &m, tea.Key{Code: tea.KeyDown})
	if m.projectTable.Cursor() != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.projectTable.Cursor())
	}
	assertProjectRow(t, &m, 1, first.Name, workers.Stopped.String())

	sendKey(t, &m, tea.Key{Code: tea.KeyUp})
	if m.projectTable.Cursor() != 0 {
		t.Fatalf("cursor after up = %d, want 0", m.projectTable.Cursor())
	}

	sendKey(t, &m, tea.Key{Code: tea.KeyDown})
	sendKey(t, &m, tea.Key{Code: tea.KeyDown})
	if m.projectTable.Cursor() != 2 {
		t.Fatalf("cursor after two downs = %d, want 2", m.projectTable.Cursor())
	}
	assertProjectRow(t, &m, 2, second.Name, workers.Stopped.String())

	sendKey(t, &m, tea.Key{Code: tea.KeyDown})
	if m.projectTable.Cursor() != 3 {
		t.Fatalf("cursor after third down = %d, want 3", m.projectTable.Cursor())
	}
	assertProjectRow(t, &m, 3, third.Name, workers.Stopped.String())
}

func TestKeyboardCreateProjectPersistsStoppedProject(t *testing.T) {
	m, repo := newIntegrationModel(t, integrationModelOptions{})

	if !m.isCreateProjectSelected() {
		t.Fatal("expected create row to be selected")
	}

	cmd := sendKey(t, &m, tea.Key{Code: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	if m.mode != projectEditorViewMode {
		t.Fatalf("mode after enter = %v, want %v", m.mode, projectEditorViewMode)
	}

	m.projectEditor.nameInput.SetValue("Keyboard Project")
	m.projectEditor.workplaceInput.SetValue(`/tmp/keyboard-project`)
	m.projectEditor.instructionsInput.SetValue("plan\nbuild\nverify")

	left, top, _, _ := m.projectEditorOKButtonBounds()
	applyMsg(t, &m, tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))

	if m.mode != tableViewMode {
		t.Fatalf("mode after save = %v, want %v", m.mode, tableViewMode)
	}
	if len(m.projects) != 1 {
		t.Fatalf("project count = %d, want 1", len(m.projects))
	}

	assertProjectRow(t, &m, 1, "Keyboard Project", workers.Stopped.String())

	storedProjects, err := repo.ReadProjects(context.Background())
	if err != nil {
		t.Fatalf("read projects: %v", err)
	}
	if len(storedProjects) != 1 {
		t.Fatalf("stored project count = %d, want 1", len(storedProjects))
	}
	if storedProjects[0].Name != "Keyboard Project" {
		t.Fatalf("stored name = %q, want %q", storedProjects[0].Name, "Keyboard Project")
	}
	if storedProjects[0].State != workers.Stopped.String() {
		t.Fatalf("stored state = %q, want %q", storedProjects[0].State, workers.Stopped.String())
	}
}

func TestKeyboardEditProjectPersistsChanges(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, runtimeProject := newStoredProjectForModel(t, repo, "Original Project")

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:     repo,
		projects: []Project{runtimeProject},
	})
	m.projectTable.SetCursor(1)

	cmd := sendKey(t, &m, tea.Key{Code: tea.KeyEnter})
	if cmd != nil {
		cmd()
	}
	if m.mode != projectEditorViewMode {
		t.Fatalf("mode after enter = %v, want %v", m.mode, projectEditorViewMode)
	}

	m.projectEditor.nameInput.SetValue("Updated Project")
	m.projectEditor.workplaceInput.SetValue(`/tmp/updated-project`)
	m.projectEditor.instructionsInput.SetValue("updated\ninstructions")

	left, top, _, _ := m.projectEditorOKButtonBounds()
	applyMsg(t, &m, tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))

	assertProjectRow(t, &m, 1, "Updated Project", workers.Stopped.String())

	persistedProject, err := repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if persistedProject.Name != "Updated Project" {
		t.Fatalf("stored name = %q, want %q", persistedProject.Name, "Updated Project")
	}
	if persistedProject.Workplace != `/tmp/updated-project` {
		t.Fatalf("stored workplace = %q, want %q", persistedProject.Workplace, `/tmp/updated-project`)
	}
}

func TestKeyboardSpaceStartsAndStopsProject(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, runtimeProject := newStoredProjectForModel(t, repo, "Toggle Project", "frontend-developer")
	blocked := make(chan struct{}, 1)

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:             repo,
		projects:         []Project{runtimeProject},
		supervisorRunner: blockingSupervisorRunner(blocked),
	})
	m.projectTable.SetCursor(1)

	cmd := sendKey(t, &m, tea.Key{Code: ' '})
	if cmd == nil {
		t.Fatal("expected start command")
	}
	waitForSignal(t, blocked, "planner prompt")
	assertProjectRow(t, &m, 1, storedProject.Name, workers.Running.String())

	persistedProject, err := repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project after start: %v", err)
	}
	if persistedProject.State != workers.Running.String() {
		t.Fatalf("stored state after start = %q, want %q", persistedProject.State, workers.Running.String())
	}

	runDone := m.projects[0].Runtime.SupervisorDone
	sendKey(t, &m, tea.Key{Code: ' '})
	waitForSignal(t, runDone, "supervisor cancellation")
	assertProjectRow(t, &m, 1, storedProject.Name, workers.Stopped.String())

	persistedProject, err = repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project after stop: %v", err)
	}
	if persistedProject.State != workers.Stopped.String() {
		t.Fatalf("stored state after stop = %q, want %q", persistedProject.State, workers.Stopped.String())
	}
}

func TestKeyboardStartAndStopProjectWithKeys(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, runtimeProject := newStoredProjectForModel(t, repo, "Start Stop Project", "frontend-developer")
	blocked := make(chan struct{}, 1)

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:             repo,
		projects:         []Project{runtimeProject},
		supervisorRunner: blockingSupervisorRunner(blocked),
	})
	m.projectTable.SetCursor(1)

	cmd := sendKey(t, &m, tea.Key{Code: 's'})
	if cmd == nil {
		t.Fatal("expected start command")
	}
	waitForSignal(t, blocked, "planner prompt")
	assertProjectRow(t, &m, 1, storedProject.Name, workers.Running.String())

	runDone := m.projects[0].Runtime.SupervisorDone
	sendKey(t, &m, tea.Key{Code: 'p'})
	waitForSignal(t, runDone, "supervisor cancellation")
	assertProjectRow(t, &m, 1, storedProject.Name, workers.Stopped.String())
}

func TestKeyboardQuitAndShutdownStopsRunningAgents(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, err := repo.CreateProject(context.Background(), "Quit Project", `/tmp/quit-project`, "quit flow")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	storedAgent, err := repo.AddAgentToProject(context.Background(), storedProject.ID, "Worker", "")
	if err != nil {
		t.Fatalf("add agent: %v", err)
	}
	if err := repo.UpdateProjectState(context.Background(), storedProject.ID, workers.Running.String()); err != nil {
		t.Fatalf("update project state: %v", err)
	}
	if err := repo.UpdateAgentState(context.Background(), storedAgent.ID, workers.Running.String()); err != nil {
		t.Fatalf("update agent state: %v", err)
	}

	runtimeAgent := workers.NewAgent("Worker")
	runtimeAgent.Hydrate(workers.Running)
	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo: repo,
		projects: []Project{{
			Config: ProjectConfig{
				ProjectID:    storedProject.ID,
				Name:         storedProject.Name,
				Workplace:    storedProject.Workplace,
				Instructions: storedProject.Instructions,
			},
			State: ProjectState{State: workers.Running},
			Runtime: ProjectRuntime{
				Agents:   []workers.AgentContract{runtimeAgent},
				AgentIDs: []int64{storedAgent.ID},
			},
		}},
	})
	m.projectTable.SetCursor(1)

	cmd := sendKey(t, &m, tea.Key{Code: 'q'})
	if cmd == nil {
		t.Fatal("expected quit command")
	}
	msg := runCmdWithTimeout(t, cmd, 2*time.Second)
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("quit message = %T, want tea.QuitMsg", msg)
	}

	m.Shutdown()

	if m.projects[0].State.State != workers.Stopped {
		t.Fatalf("state after shutdown = %s, want %s", m.projects[0].State.State, workers.Stopped)
	}
	if m.projects[0].Runtime.Agents[0].State() != workers.Stopped {
		t.Fatalf("agent state after shutdown = %s, want %s", m.projects[0].Runtime.Agents[0].State(), workers.Stopped)
	}
	assertActivityContainsText(t, m, "Worker: stopped")

	persistedProject, err := repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project after shutdown: %v", err)
	}
	if persistedProject.State != workers.Stopped.String() {
		t.Fatalf("stored project state = %q, want %q", persistedProject.State, workers.Stopped.String())
	}
	if persistedProject.Agents[0].State != workers.Stopped.String() {
		t.Fatalf("stored agent state = %q, want %q", persistedProject.Agents[0].State, workers.Stopped.String())
	}
}

func TestKeyboardStartProjectRunsPlannerAndShowsStreams(t *testing.T) {
	repo := newTestRepository(t)
	storedProject, runtimeProject := newStoredProjectForModel(t, repo, "Planner Project", "frontend-developer")
	supervisorRunner, advance := planningSupervisorRunner(t, repo, storedProject, runtimeProject)

	m, _ := newIntegrationModel(t, integrationModelOptions{
		repo:             repo,
		projects:         []Project{runtimeProject},
		supervisorRunner: supervisorRunner,
	})
	m.projectTable.SetCursor(1)

	cmd := sendKey(t, &m, tea.Key{Code: 's'})
	if cmd == nil {
		t.Fatal("expected start command")
	}

	pumpSupervisorUpdates(t, &m, 0, 16, advance)

	if m.projects[0].State.State != workers.Completed {
		t.Fatalf("project state = %s, want %s", m.projects[0].State.State, workers.Completed)
	}
	if m.projects[0].Runtime.Plan == nil {
		t.Fatal("expected plan to be populated")
	}
	if len(m.projects[0].Runtime.Plan.Streams) != 1 || len(m.projects[0].Runtime.Plan.Streams[0]) != 1 {
		t.Fatalf("plan streams = %#v, want one stream with one task", m.projects[0].Runtime.Plan.Streams)
	}
	if task := m.projects[0].Runtime.Plan.Streams[0][0]; task.Agent != "frontend-developer" || task.Task != "Build UI" {
		t.Fatalf("planned task = %#v, want frontend-developer/Build UI", task)
	}

	streamRowIndex := m.rowIndexForStreamIndex(0)
	if streamRowIndex < 0 {
		t.Fatal("expected stream row to be present")
	}
	frontendIndex := findRuntimeAgentIndexByName(m.projects[0], "frontend-developer")
	if frontendIndex < 0 {
		t.Fatal("expected frontend-developer runtime agent")
	}
	stepRowIndex := m.rowIndexForAgentIndex(frontendIndex)
	if stepRowIndex < 0 {
		t.Fatal("expected planned step row to be present")
	}

	assertAgentRow(t, &m, streamRowIndex, "Stream 1 (1 tasks)", "")
	assertAgentRow(t, &m, stepRowIndex, "frontend-developer - Build UI", workers.Completed.String())

	persistedProject, err := repo.ReadProject(context.Background(), storedProject.ID)
	if err != nil {
		t.Fatalf("read project after planner run: %v", err)
	}
	if persistedProject.State != workers.Completed.String() {
		t.Fatalf("stored project state = %q, want %q", persistedProject.State, workers.Completed.String())
	}
	assertAgentState(t, persistedProject.Agents, "planner", workers.Completed.String())
	assertAgentState(t, persistedProject.Agents, "branch-setup", workers.Completed.String())
	assertAgentState(t, persistedProject.Agents, "frontend-developer", workers.Completed.String())
	assertAgentState(t, persistedProject.Agents, "branch-merger", workers.Completed.String())
	assertActivityContains(t, persistedProject.Activities, "branch-merger: completed")
}

func newIntegrationModel(t *testing.T, opts integrationModelOptions) (Model, *data.SQLiteRepository) {
	t.Helper()

	repo := opts.repo
	if repo == nil {
		repo = newTestRepository(t)
	}
	if len(opts.agentNames) > 0 {
		t.Setenv("KENNEL_ROOT_DIR", newTestAgentsRoot(t, opts.agentNames...))
	}

	m := NewModel(table.Styles{}, table.Styles{}, opts.projects, repo)
	m.ResizeTables(DefaultProjectWidth+DefaultAgentWidth+DefaultActivityWidth, DefaultTableHeight+FooterHeight+4)
	m.SetFocus(0)
	m.supervisorFactory = opts.supervisorFactory
	m.supervisorRunner = opts.supervisorRunner
	return m, repo
}

func newStoredProjectForModel(t *testing.T, repo *data.SQLiteRepository, name string, agentNames ...string) (data.Project, Project) {
	t.Helper()

	storedProject, err := repo.CreateProject(context.Background(), name, `/tmp/`+strings.ReplaceAll(strings.ToLower(name), " ", "-"), "build something")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	runtimeAgents := make([]workers.AgentContract, 0, len(agentNames))
	agentIDs := make([]int64, 0, len(agentNames))
	for _, agentName := range agentNames {
		storedAgent, err := repo.AddAgentToProject(context.Background(), storedProject.ID, agentName, "")
		if err != nil {
			t.Fatalf("add agent %q: %v", agentName, err)
		}
		runtimeAgent := workers.NewAgent(agentName)
		runtimeAgents = append(runtimeAgents, runtimeAgent)
		agentIDs = append(agentIDs, storedAgent.ID)
	}

	return storedProject, Project{
		Config: ProjectConfig{
			ProjectID:    storedProject.ID,
			Name:         storedProject.Name,
			Workplace:    storedProject.Workplace,
			Instructions: storedProject.Instructions,
		},
		State: ProjectState{State: workers.Stopped},
		Runtime: ProjectRuntime{
			Agents:   runtimeAgents,
			AgentIDs: agentIDs,
		},
	}
}

func applyMsg(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()

	updatedModel, cmd := m.Update(msg)
	updated, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
	}
	*m = updated
	return cmd
}

func sendKey(t *testing.T, m *Model, key tea.Key) tea.Cmd {
	t.Helper()
	return applyMsg(t, m, tea.KeyPressMsg(key))
}

func pumpSupervisorUpdates(t *testing.T, m *Model, projectIndex int, maxSteps int, advance chan<- struct{}) {
	t.Helper()

	for step := 0; step < maxSteps; step++ {
		project := m.projects[projectIndex]
		if project.Runtime.SupervisorEvents == nil || project.Runtime.SupervisorDone == nil {
			return
		}

		source := supervisorSource{
			projectIndex: projectIndex,
			channel:      project.Runtime.SupervisorEvents,
			done:         project.Runtime.SupervisorDone,
			result:       project.Runtime.SupervisorResult,
		}
		msg := runCmdWithTimeout(t, waitForSupervisorUpdate(source), 2*time.Second)
		applyMsg(t, m, msg)
		if advance != nil {
			select {
			case advance <- struct{}{}:
			default:
			}
		}
		if _, ok := msg.(supervisorCompletedMsg); ok {
			return
		}
	}

	t.Fatalf("supervisor updates exceeded %d steps", maxSteps)
}

func blockingSupervisorRunner(blocked chan<- struct{}) supervisorRunner {
	return func(ctx context.Context, supervisor *Supervisor, instructions string, configuredAgents []string) error {
		select {
		case blocked <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
}

func planningSupervisorRunner(t *testing.T, repo *data.SQLiteRepository, storedProject data.Project, runtimeProject Project) (supervisorRunner, chan struct{}) {
	t.Helper()

	advance := make(chan struct{}, 1)
	return func(ctx context.Context, supervisor *Supervisor, instructions string, configuredAgents []string) error {
		planJSON := `{"streams":[[{"agent":"frontend-developer","task":"Build UI"}]]}`

		planner, err := repo.AddAgentToProject(ctx, storedProject.ID, "planner", "")
		if err != nil {
			return err
		}
		if err := repo.UpdateAgentOutput(ctx, planner.ID, planJSON); err != nil {
			return err
		}
		if err := repo.UpdateAgentState(ctx, planner.ID, workers.Completed.String()); err != nil {
			return err
		}
		if _, err := repo.NewActivity(ctx, storedProject.ID, nullAgentID(planner.ID), completedActivityText("planner")); err != nil {
			return err
		}
		supervisor.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.PlanUpdateEvent{Plan: planJSON}})
		supervisor.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.SupervisorSyncEvent{
			ProjectID: storedProject.ID, AgentID: planner.ID, Agent: "planner", State: workers.Completed.String(), Activity: "completed",
		}})
		if err := waitForAdvance(ctx, advance); err != nil {
			return err
		}

		branchSetup, err := repo.AddAgentToStream(ctx, storedProject.ID, 0, "branch-setup", branchSetupInstanceKey(0), "planner-project/run/stream-0")
		if err != nil {
			return err
		}
		if err := repo.UpdateAgentOutput(ctx, branchSetup.ID, "Branch ready"); err != nil {
			return err
		}
		if err := repo.UpdateAgentState(ctx, branchSetup.ID, workers.Completed.String()); err != nil {
			return err
		}
		if _, err := repo.NewActivity(ctx, storedProject.ID, nullAgentID(branchSetup.ID), completedActivityText("branch-setup")); err != nil {
			return err
		}
		supervisor.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.SupervisorSyncEvent{
			ProjectID: storedProject.ID, AgentID: branchSetup.ID, Agent: "branch-setup", InstanceKey: branchSetupInstanceKey(0), State: workers.Completed.String(), Activity: "completed",
		}})
		if err := waitForAdvance(ctx, advance); err != nil {
			return err
		}

		frontendIndex := findRuntimeAgentIndexByName(runtimeProject, "frontend-developer")
		if frontendIndex < 0 || frontendIndex >= len(runtimeProject.Runtime.AgentIDs) {
			return context.Canceled
		}
		frontendID := runtimeProject.Runtime.AgentIDs[frontendIndex]
		if err := repo.UpdateAgentOutput(ctx, frontendID, "Implemented UI"); err != nil {
			return err
		}
		if err := repo.UpdateAgentState(ctx, frontendID, workers.Completed.String()); err != nil {
			return err
		}
		if _, err := repo.NewActivity(ctx, storedProject.ID, nullAgentID(frontendID), completedActivityText("frontend-developer")); err != nil {
			return err
		}
		supervisor.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.SupervisorSyncEvent{
			ProjectID: storedProject.ID, AgentID: frontendID, Agent: "frontend-developer", State: workers.Completed.String(), Activity: "completed",
		}})
		if err := waitForAdvance(ctx, advance); err != nil {
			return err
		}

		branchMerger, err := repo.AddAgentToStream(ctx, storedProject.ID, 0, "branch-merger", branchMergerInstanceKey(0), "planner-project/run/stream-0")
		if err != nil {
			return err
		}
		if err := repo.UpdateAgentOutput(ctx, branchMerger.ID, "Merged stream branch into main"); err != nil {
			return err
		}
		if err := repo.UpdateAgentState(ctx, branchMerger.ID, workers.Completed.String()); err != nil {
			return err
		}
		if _, err := repo.NewActivity(ctx, storedProject.ID, nullAgentID(branchMerger.ID), completedActivityText("branch-merger")); err != nil {
			return err
		}
		supervisor.EventBus.Publish(data.SupervisorTopic, data.Event{Payload: data.SupervisorSyncEvent{
			ProjectID: storedProject.ID, AgentID: branchMerger.ID, Agent: "branch-merger", InstanceKey: branchMergerInstanceKey(0), State: workers.Completed.String(), Activity: "completed",
		}})
		if err := waitForAdvance(ctx, advance); err != nil {
			return err
		}

		return nil
	}, advance
}

func waitForSignal(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()

	if done == nil {
		t.Fatalf("%s channel is nil", label)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func assertProjectRow(t *testing.T, m *Model, rowIndex int, expectedName string, expectedState string) {
	t.Helper()

	previous := m.projectTable.Cursor()
	m.projectTable.SetCursor(rowIndex)
	row := m.projectTable.SelectedRow()
	m.projectTable.SetCursor(previous)
	if len(row) != 2 {
		t.Fatalf("project row %d = %#v, want 2 columns", rowIndex, row)
	}
	if row[0] != expectedState || row[1] != expectedName {
		t.Fatalf("project row %d = %#v, want [%q %q]", rowIndex, row, expectedState, expectedName)
	}
}

func assertAgentRow(t *testing.T, m *Model, rowIndex int, expectedLabelContains string, expectedState string) {
	t.Helper()

	previous := m.agentTable.Cursor()
	m.agentTable.SetCursor(rowIndex)
	row := m.agentTable.SelectedRow()
	m.agentTable.SetCursor(previous)
	if len(row) != 2 {
		t.Fatalf("agent row %d = %#v, want 2 columns", rowIndex, row)
	}
	if expectedState != "" && row[0] != expectedState {
		t.Fatalf("agent row %d state = %q, want %q", rowIndex, row[0], expectedState)
	}
	if !strings.Contains(row[1], expectedLabelContains) {
		t.Fatalf("agent row %d label = %q, want to contain %q", rowIndex, row[1], expectedLabelContains)
	}
}

func assertActivityContainsText(t *testing.T, m Model, substring string) {
	t.Helper()

	for _, project := range m.projects {
		for _, activity := range project.Runtime.Activities {
			if strings.Contains(activity.Text, substring) {
				return
			}
		}
	}

	t.Fatalf("activity containing %q not found", substring)
}

func findRuntimeAgentIndexByName(project Project, name string) int {
	for index, agent := range project.Runtime.Agents {
		if agent.Name() == name {
			return index
		}
	}
	return -1
}

func waitForAdvance(ctx context.Context, advance <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-advance:
		return nil
	}
}

func nullAgentID(agentID int64) sql.NullInt64 {
	return sql.NullInt64{Int64: agentID, Valid: agentID > 0}
}

func completedActivityText(agentName string) string {
	return agentName + ": completed"
}
