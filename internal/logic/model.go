package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	repository "MattiasHognas/Kennel/internal/data"
	eventbus "MattiasHognas/Kennel/internal/events"
	"MattiasHognas/Kennel/internal/supervisor"
	"MattiasHognas/Kennel/internal/ui"
	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type ActivityEntry struct {
	Timestamp string
	Text      string
}

type ProjectConfig struct {
	ProjectID    int64
	Name         string
	Workplace    string
	Instructions string
}

type ProjectState struct {
	State agent.AgentState
}

type ProjectRuntime struct {
	Agents           []agent.AgentContract
	AgentIDs         []int64
	Activities       []ActivityEntry
	ActivityDone     <-chan struct{}
	ActivityCancel   context.CancelFunc
	Supervisor       *supervisor.Supervisor
	SupervisorEvents eventbus.EventChan
	SupervisorDone   <-chan struct{}
	CancelCtx        context.CancelFunc
}

type Project struct {
	Config  ProjectConfig
	State   ProjectState
	Runtime ProjectRuntime
}

type viewMode int

const (
	tableViewMode viewMode = iota
	projectEditorViewMode
)

type ActivitySource struct {
	projectIndex int
	agentIndex   int
	channel      eventbus.EventChan
	done         <-chan struct{}
}

type activityMsg struct {
	source ActivitySource
	text   string
}

type supervisorSource struct {
	projectIndex int
	channel      eventbus.EventChan
	done         <-chan struct{}
}

type supervisorSyncMsg struct {
	source supervisorSource
	event  eventbus.SupervisorSyncEvent
}

type supervisorCompletedMsg struct {
	source supervisorSource
}

type Keymap struct {
	quit          key.Binding
	nextTable     key.Binding
	prevTable     key.Binding
	editProject   key.Binding
	startProject  key.Binding
	stopProject   key.Binding
	toggleProject key.Binding
}

type Model struct {
	projectTable  table.Model
	agentTable    table.Model
	activityTable table.Model
	focusedStyles table.Styles
	blurredStyles table.Styles
	focusIndex    int
	projects      []Project
	Sources       []ActivitySource
	windowWidth   int
	windowHeight  int
	keymap        Keymap
	repository    *repository.SQLiteRepository
	mode          viewMode
	projectEditor projectEditor
}

const (
	DefaultProjectWidth  = 28
	DefaultAgentWidth    = 22
	DefaultActivityWidth = 60
	DefaultTableHeight   = 8
	FooterHeight         = 4
	TableGap             = 2
	createProjectRowName = "Create new..."
)

func NewModel(focusedStyles, blurredStyles table.Styles, projects []Project, repository *repository.SQLiteRepository) Model {
	m := Model{
		projectTable:  newProjectTable(blurredStyles),
		agentTable:    newAgentTable(DefaultAgentWidth, blurredStyles),
		activityTable: newActivityTable("Activity", DefaultActivityWidth, blurredStyles),
		focusedStyles: focusedStyles,
		blurredStyles: blurredStyles,
		projects:      projects,
		repository:    repository,
		projectEditor: newProjectEditor(),
		keymap: Keymap{
			quit: key.NewBinding(
				key.WithKeys("esc", "ctrl+c", "q"),
				key.WithHelp("esc/q", "quit"),
			),
			nextTable: key.NewBinding(
				key.WithKeys("tab", "right", "l"),
				key.WithHelp("tab/right", "next table"),
			),
			prevTable: key.NewBinding(
				key.WithKeys("shift+tab", "left", "h"),
				key.WithHelp("shift+tab/left", "prev table"),
			),
			editProject: key.NewBinding(
				key.WithKeys("enter"),
				key.WithHelp("enter", "edit project"),
			),
			startProject: key.NewBinding(
				key.WithKeys("s"),
				key.WithHelp("s", "start project"),
			),
			stopProject: key.NewBinding(
				key.WithKeys("p"),
				key.WithHelp("p", "stop project"),
			),
			toggleProject: key.NewBinding(
				key.WithKeys("space"),
				key.WithHelp("space", "cycle state"),
			),
		},
	}

	m.initializeActivityListeners()
	m.Sources = m.BuildActivitySources()
	m.refreshProjectTable()
	m.refreshSelectedProjectTables()
	m.resizeProjectEditor()
	return m
}

func newProjectTable(styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "State", Width: 10},
			{Title: "Projects", Width: DefaultProjectWidth - 12},
		}),
		table.WithStyles(styles),
		table.WithWidth(DefaultProjectWidth),
		table.WithHeight(DefaultTableHeight),
	)
}

func newActivityTable(title string, width int, styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Time", Width: 10},
			{Title: title, Width: max(12, width-2-12)},
		}),
		table.WithStyles(styles),
		table.WithWidth(width),
		table.WithHeight(DefaultTableHeight),
	)
}

func newAgentTable(width int, styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "State", Width: 10},
			{Title: "Agents", Width: max(12, width-2-12)},
		}),
		table.WithStyles(styles),
		table.WithWidth(width),
		table.WithHeight(DefaultTableHeight),
	)
}

func (m Model) Init() tea.Cmd {
	supervisorSources := m.BuildSupervisorSources()
	cmds := make([]tea.Cmd, 0, len(m.Sources)+len(supervisorSources))
	for _, source := range m.Sources {
		cmds = append(cmds, waitForActivity(source))
	}
	for _, source := range supervisorSources {
		cmds = append(cmds, waitForSupervisorUpdate(source))
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		m.ResizeTables(msg.Width, msg.Height)
		return m, nil

	case activityMsg:
		m.recordActivity(msg.source, msg.text)
		return m, waitForActivity(msg.source)

	case supervisorCompletedMsg:
		if m.shouldListenForSupervisor(msg.source) {
			m.completeProjectAtIndex(msg.source.projectIndex)
		}
		return m, nil

	case supervisorSyncMsg:
		if !m.shouldListenForSupervisor(msg.source) {
			return m, nil
		}
		if msg.event.ProjectID > 0 {
			project := &m.projects[msg.source.projectIndex]
			if project.Config.ProjectID > 0 && project.Config.ProjectID != msg.event.ProjectID {
				return m, waitForSupervisorUpdate(msg.source)
			}
		}
		activitySources := m.applySupervisorSync(msg.source, msg.event)
		cmds := make([]tea.Cmd, 0, len(activitySources)+1)
		cmds = append(cmds, waitForSupervisorUpdate(msg.source))
		for _, source := range activitySources {
			cmds = append(cmds, waitForActivity(source))
		}
		return m, tea.Batch(cmds...)
	}

	if m.mode == projectEditorViewMode {
		return m.updateProjectEditor(msg)
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if shouldStop, handled, cmd := m.handleKeyPress(keyMsg); handled {
			if shouldStop {
				return m, tea.Quit
			}
			return m, cmd
		}
	}

	return m.updateTables(msg)
}

func (m Model) updateTables(msg tea.Msg) (tea.Model, tea.Cmd) {
	previousProject := m.selectedProjectIndex()
	previousAgent := m.agentTable.Cursor()

	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.projectTable, cmd = m.projectTable.Update(msg)
	cmds = append(cmds, cmd)
	m.agentTable, cmd = m.agentTable.Update(msg)
	cmds = append(cmds, cmd)
	m.activityTable, cmd = m.activityTable.Update(msg)
	cmds = append(cmds, cmd)

	if m.selectedProjectIndex() != previousProject {
		m.refreshSelectedProjectTables()
		if project := m.selectedProject(); project != nil && len(project.Runtime.Agents) > 0 {
			m.agentTable.SetCursor(min(previousAgent, len(project.Runtime.Agents)-1))
		} else {
			m.agentTable.SetCursor(0)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() tea.View {
	if m.mode == projectEditorViewMode {
		v := tea.NewView(m.projectEditorView())
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	header := fmt.Sprintf("Selected project: %s", m.selectedProjectSummary())
	workplace := fmt.Sprintf("Workplace: %s", m.selectedProjectWorkplaceSummary())
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		workplace,
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, m.tableViews()...),
		"",
		"tab/shift+tab switches tables, enter edits the selected project, space cycles state, s starts, p stops.",
		"Activities come from agents and supervisor updates for the currently selected project.",
	)

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) tableViews() []string {

	return []string{
		ui.SpacedTableStyle.Render(m.projectTable.View()),
		ui.SpacedTableStyle.Render(m.agentTable.View()),
		m.activityTable.View(),
	}
}

func (m *Model) ResizeTables(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	tableHeight := max(DefaultTableHeight, height-FooterHeight)
	availableWidth := max(width-(TableGap*2), DefaultProjectWidth+DefaultAgentWidth+32)
	projectWidth := max(DefaultProjectWidth, availableWidth/4)
	agentWidth := max(DefaultAgentWidth, availableWidth/5)
	activityWidth := max(DefaultActivityWidth, availableWidth-projectWidth-agentWidth)

	if projectWidth+agentWidth+activityWidth > availableWidth {
		activityWidth = max(32, availableWidth-projectWidth-agentWidth)
	}

	m.projectTable.SetWidth(projectWidth)
	m.projectTable.SetHeight(tableHeight)
	m.projectTable.SetColumns([]table.Column{
		{Title: "State", Width: 10},
		{Title: "Projects", Width: max(16, projectWidth-12)},
	})

	m.agentTable.SetWidth(agentWidth)
	m.agentTable.SetHeight(tableHeight)
	m.agentTable.SetColumns([]table.Column{
		{Title: "State", Width: 10},
		{Title: "Agents", Width: max(12, agentWidth-2-12)},
	})

	m.activityTable.SetWidth(activityWidth)
	m.activityTable.SetHeight(tableHeight)
	m.activityTable.SetColumns([]table.Column{
		{Title: "Time", Width: 10},
		{Title: "Activity", Width: max(24, activityWidth-2-12)},
	})

	m.resizeProjectEditor()
}

func (m *Model) SetFocus(index int) {
	m.focusIndex = (index + 3) % 3
	m.projectTable.SetStyles(m.blurredStyles)
	m.agentTable.SetStyles(m.blurredStyles)
	m.activityTable.SetStyles(m.blurredStyles)
	m.projectTable.Blur()
	m.agentTable.Blur()
	m.activityTable.Blur()

	switch m.focusIndex {
	case 0:
		m.projectTable.SetStyles(m.focusedStyles)
		m.projectTable.Focus()
	case 1:
		m.agentTable.SetStyles(m.focusedStyles)
		m.agentTable.Focus()
	default:
		m.activityTable.SetStyles(m.focusedStyles)
		m.activityTable.Focus()
	}
}

func (m *Model) refreshProjectTable() {
	rows := make([]table.Row, 0, len(m.projects)+1)
	rows = append(rows, table.Row{"new", createProjectRowName})
	for _, project := range m.projects {
		rows = append(rows, table.Row{project.State.State.String(), project.Config.Name})
	}
	m.projectTable.SetRows(rows)
}

func (m *Model) refreshSelectedProjectTables() {
	if m.isCreateProjectSelected() {
		m.agentTable.SetRows([]table.Row{{"", ""}})
		m.activityTable.SetRows([]table.Row{{"", ""}})
		return
	}

	project := m.selectedProject()
	if project == nil {
		m.agentTable.SetRows([]table.Row{{"", ""}})
		m.activityTable.SetRows([]table.Row{{"", ""}})
		return
	}

	agentRows := make([]table.Row, 0, len(project.Runtime.Agents))
	for _, agentInstance := range project.Runtime.Agents {
		agentRows = append(agentRows, table.Row{agentInstance.State().String(), agentInstance.Name()})
	}
	if len(agentRows) == 0 {
		agentRows = append(agentRows, table.Row{"-", "No agents"})
	}
	m.agentTable.SetRows(agentRows)

	activityRows := make([]table.Row, 0, len(project.Runtime.Activities))
	for i := len(project.Runtime.Activities) - 1; i >= 0; i-- {
		activityRows = append(activityRows, table.Row{project.Runtime.Activities[i].Timestamp, project.Runtime.Activities[i].Text})
	}
	if len(activityRows) == 0 {
		activityRows = append(activityRows, table.Row{"-", "No activity yet"})
	}
	m.activityTable.SetRows(activityRows)
}

func (m *Model) selectedProjectIndex() int {
	if len(m.projects) == 0 || m.projectTable.Cursor() <= 0 {
		return -1
	}
	index := m.projectTable.Cursor() - 1
	if index >= len(m.projects) {
		return -1
	}
	return index
}

func (m *Model) isCreateProjectSelected() bool {
	return m.projectTable.Cursor() == 0
}

func (m *Model) selectedProject() *Project {
	index := m.selectedProjectIndex()
	if index < 0 || index >= len(m.projects) {
		return nil
	}
	return &m.projects[index]
}

func (m *Model) selectedProjectSummary() string {
	if m.isCreateProjectSelected() {
		return createProjectRowName
	}

	project := m.selectedProject()
	if project == nil {
		return "none"
	}
	return fmt.Sprintf("%s (%s)", project.Config.Name, project.State)
}

func (m *Model) selectedProjectWorkplaceSummary() string {
	if m.isCreateProjectSelected() {
		return "not set"
	}

	project := m.selectedProject()
	if project == nil || strings.TrimSpace(project.Config.Workplace) == "" {
		return "not set"
	}
	return project.Config.Workplace
}

func (m *Model) selectedAgentIndex() int {
	project := m.selectedProject()
	if project == nil || len(project.Runtime.Agents) == 0 {
		return -1
	}
	index := m.agentTable.Cursor()
	if index < 0 || index >= len(project.Runtime.Agents) {
		return -1
	}
	return index
}

func (m *Model) selectedAgent() agent.AgentContract {
	project := m.selectedProject()
	index := m.selectedAgentIndex()
	if project == nil || index < 0 {
		return nil
	}
	return project.Runtime.Agents[index]
}

func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (shouldQuit bool, handled bool, cmd tea.Cmd) {
	switch {
	case key.Matches(msg, m.keymap.quit):
		return true, true, nil
	case key.Matches(msg, m.keymap.nextTable):
		m.SetFocus(m.focusIndex + 1)
		return false, true, nil
	case key.Matches(msg, m.keymap.prevTable):
		m.SetFocus(m.focusIndex - 1)
		return false, true, nil
	case key.Matches(msg, m.keymap.editProject):
		if m.focusIndex == 0 {
			return false, true, m.openSelectedProjectEditor()
		}
		return false, false, nil
	case key.Matches(msg, m.keymap.startProject):
		if m.focusIndex == 1 {
			m.startSelectedAgent()
			return false, true, nil
		} else {
			return false, true, m.startSelectedProject()
		}
	case key.Matches(msg, m.keymap.stopProject):
		if m.focusIndex == 1 {
			m.stopSelectedAgent()
		} else {
			m.stopSelectedProject()
		}
		return false, true, nil
	case key.Matches(msg, m.keymap.toggleProject):
		if m.focusIndex == 1 {
			m.cycleSelectedAgentState()
			return false, true, nil
		} else {
			return false, true, m.cycleSelectedProjectState()
		}
	default:
		return false, false, nil
	}
}

func parseAgentState(state string) agent.AgentState {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case agent.Running.String():
		return agent.Running
	case agent.Completed.String():
		return agent.Completed
	default:
		return agent.Stopped
	}
}

func (m *Model) persistProjectState(project *Project) {
	if m.repository == nil || project == nil || project.Config.ProjectID <= 0 {
		return
	}

	if err := m.repository.UpdateProjectState(context.Background(), project.Config.ProjectID, project.State.State.String()); err != nil {
		fmt.Fprintf(os.Stderr, "persist project state: %v\n", err)
	}
}

func (m *Model) persistProjectAgentStates(project *Project) {
	if m.repository == nil || project == nil || len(project.Runtime.AgentIDs) == 0 {
		return
	}

	for i, agentID := range project.Runtime.AgentIDs {
		if i >= len(project.Runtime.Agents) || agentID <= 0 {
			continue
		}

		if err := m.repository.UpdateAgentState(context.Background(), agentID, project.Runtime.Agents[i].State().String()); err != nil {
			fmt.Fprintf(os.Stderr, "persist agent state: %v\n", err)
		}
	}
}

func (m *Model) persistActivity(project *Project, agentIndex int, text string) {
	if m.repository == nil || project == nil || project.Config.ProjectID <= 0 {
		return
	}

	agentID := sql.NullInt64{}
	if agentIndex >= 0 && agentIndex < len(project.Runtime.AgentIDs) && project.Runtime.AgentIDs[agentIndex] > 0 {
		agentID = sql.NullInt64{Int64: project.Runtime.AgentIDs[agentIndex], Valid: true}
	}

	if _, err := m.repository.NewActivity(context.Background(), project.Config.ProjectID, agentID, text); err != nil {
		fmt.Fprintf(os.Stderr, "persist activity: %v\n", err)
	}
}

func (m *Model) shouldListenForSupervisor(source supervisorSource) bool {
	if source.projectIndex < 0 || source.projectIndex >= len(m.projects) {
		return false
	}

	project := m.projects[source.projectIndex]
	return project.Runtime.SupervisorEvents == source.channel && project.Runtime.SupervisorDone == source.done
}

func (m *Model) applySupervisorSync(source supervisorSource, syncEvent eventbus.SupervisorSyncEvent) []ActivitySource {
	if source.projectIndex < 0 || source.projectIndex >= len(m.projects) {
		return nil
	}

	project := &m.projects[source.projectIndex]
	agentName := strings.TrimSpace(syncEvent.Agent)
	state := parseAgentState(syncEvent.State)
	agentIndex := -1

	if syncEvent.AgentID > 0 {
		for index, agentID := range project.Runtime.AgentIDs {
			if agentID == syncEvent.AgentID {
				agentIndex = index
				break
			}
		}
	}

	if agentIndex == -1 && agentName != "" {
		for index, agentInstance := range project.Runtime.Agents {
			if agentInstance.Name() == agentName {
				agentIndex = index
				break
			}
		}
	}

	var activitySources []ActivitySource
	if agentIndex == -1 && agentName != "" {
		restoredAgent := agent.NewAgent(agentName)
		restoredAgent.Hydrate(state)
		project.Runtime.Agents = append(project.Runtime.Agents, restoredAgent)
		project.Runtime.AgentIDs = append(project.Runtime.AgentIDs, syncEvent.AgentID)
		agentIndex = len(project.Runtime.Agents) - 1
		activitySources = m.resetActivitySourcesForProject(source.projectIndex)
	} else if agentIndex >= 0 {
		project.Runtime.Agents[agentIndex].Hydrate(state)
		if syncEvent.AgentID > 0 {
			for len(project.Runtime.AgentIDs) <= agentIndex {
				project.Runtime.AgentIDs = append(project.Runtime.AgentIDs, 0)
			}
			project.Runtime.AgentIDs[agentIndex] = syncEvent.AgentID
		}
	}

	if state == agent.Running && project.State.State == agent.Stopped {
		project.State.State = agent.Running
	}

	if activity := strings.TrimSpace(syncEvent.Activity); activity != "" && agentIndex >= 0 {
		project.Runtime.Activities = append(project.Runtime.Activities, ActivityEntry{
			Timestamp: time.Now().Format("15:04:05"),
			Text:      fmt.Sprintf("%s: %s", project.Runtime.Agents[agentIndex].Name(), activity),
		})
	}

	m.refreshProjectAndSelection(source.projectIndex)
	return activitySources
}

func (m *Model) syncProjectFromRepository(projectIndex int) []ActivitySource {
	if m.repository == nil || projectIndex < 0 || projectIndex >= len(m.projects) {
		return nil
	}

	project := &m.projects[projectIndex]
	if project.Config.ProjectID <= 0 {
		return nil
	}

	storedProject, err := m.repository.ReadProject(context.Background(), project.Config.ProjectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync project from repository: %v\n", err)
		return nil
	}

	if project.Runtime.ActivityCancel != nil {
		project.Runtime.ActivityCancel()
	}
	preservedSupervisor := project.Runtime.Supervisor
	preservedSupervisorEvents := project.Runtime.SupervisorEvents
	preservedSupervisorDone := project.Runtime.SupervisorDone
	preservedCancel := project.Runtime.CancelCtx

	agents := make([]agent.AgentContract, 0, len(storedProject.Agents))
	agentIDs := make([]int64, 0, len(storedProject.Agents))
	for _, storedAgent := range storedProject.Agents {
		agents = append(agents, restorePersistedAgent(storedAgent.Name, storedAgent.State))
		agentIDs = append(agentIDs, storedAgent.ID)
	}

	activities := make([]ActivityEntry, 0, len(storedProject.Activities))
	for _, activity := range storedProject.Activities {
		activities = append(activities, ActivityEntry{
			Timestamp: activity.CreatedAt.Format("15:04:05"),
			Text:      activity.Text,
		})
	}

	project.Config.Name = storedProject.Name
	project.Config.Workplace = storedProject.Workplace
	project.Config.Instructions = storedProject.Instructions
	project.State.State = parseAgentState(storedProject.State)
	project.Runtime = ProjectRuntime{
		Agents:           agents,
		AgentIDs:         agentIDs,
		Activities:       activities,
		Supervisor:       preservedSupervisor,
		SupervisorEvents: preservedSupervisorEvents,
		SupervisorDone:   preservedSupervisorDone,
		CancelCtx:        preservedCancel,
	}
	sources := m.resetActivitySourcesForProject(projectIndex)

	m.refreshProjectAndSelection(projectIndex)
	return sources
}

func restorePersistedAgent(name string, persistedState string) agent.AgentContract {
	a := agent.NewAgent(name)
	a.Hydrate(parseAgentState(persistedState))
	return a
}

func (m Model) Shutdown() {
	for i := range m.projects {
		project := &m.projects[i]
		if project.Runtime.ActivityCancel != nil {
			project.Runtime.ActivityCancel()
			project.Runtime.ActivityCancel = nil
			project.Runtime.ActivityDone = nil
		}
		for agentIndex, agentInstance := range project.Runtime.Agents {
			if agentInstance.State() != agent.Running {
				continue
			}

			agentInstance.Stop()
			activityText := fmt.Sprintf("%s: stopped", agentInstance.Name())
			project.Runtime.Activities = append(project.Runtime.Activities, ActivityEntry{
				Timestamp: time.Now().Format("15:04:05"),
				Text:      activityText,
			})
			m.persistActivity(project, agentIndex, activityText)
		}
		if project.State.State == agent.Running {
			project.State.State = agent.Stopped
		}
		m.persistProjectState(project)
		m.persistProjectAgentStates(project)
	}
}
