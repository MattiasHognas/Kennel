package model

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	repository "MattiasHognas/Kennel/internal/data"
	eventbus "MattiasHognas/Kennel/internal/events"
	"MattiasHognas/Kennel/internal/ui/table"
	agent "MattiasHognas/Kennel/internal/workers"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type ActivityEntry struct {
	Timestamp string
	Text      string
}

type Project struct {
	ProjectID    int64
	Name         string
	Workplace    string
	Instructions string
	State        agent.AgentState
	Agents       []agent.AgentContract
	AgentIDs     []int64
	Activities   []ActivityEntry
}

type viewMode int

const (
	tableViewMode viewMode = iota
	projectEditorViewMode
)

type projectEditor struct {
	nameInput         textinput.Model
	workplaceInput    textinput.Model
	instructionsInput textarea.Model
	focusIndex        int
	errorMessage      string
	projectIndex      int
}

type ActivitySource struct {
	projectIndex int
	agentIndex   int
	channel      eventbus.EventChan
}

type activityMsg struct {
	source ActivitySource
	text   string
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

	m.Sources = m.BuildActivitySources()
	m.refreshProjectTable()
	m.refreshSelectedProjectTables()
	m.resizeProjectEditor()
	return m
}

func newProjectEditor() projectEditor {
	nameInput := textinput.New()
	nameInput.Placeholder = "Project name"
	nameInput.CharLimit = 160
	nameInput.SetWidth(DefaultActivityWidth)

	workplaceInput := textinput.New()
	workplaceInput.Placeholder = `C:\path\to\workspace`
	workplaceInput.CharLimit = 512
	workplaceInput.SetWidth(DefaultActivityWidth)

	instructionsInput := textarea.New()
	instructionsInput.Placeholder = "Add project instructions"
	instructionsInput.CharLimit = 4000
	instructionsInput.SetWidth(DefaultActivityWidth)
	instructionsInput.SetHeight(6)
	instructionsInput.ShowLineNumbers = false
	instructionsInput.Prompt = ""

	return projectEditor{
		nameInput:         nameInput,
		workplaceInput:    workplaceInput,
		instructionsInput: instructionsInput,
		projectIndex:      -1,
	}
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

func waitForActivity(source ActivitySource) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-source.channel
		if !ok {
			return nil
		}
		return activityMsg{source: source, text: fmt.Sprint(event.Payload)}
	}
}

func (m Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.Sources))
	for _, source := range m.Sources {
		cmds = append(cmds, waitForActivity(source))
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
		if project := m.selectedProject(); project != nil && len(project.Agents) > 0 {
			m.agentTable.SetCursor(min(previousAgent, len(project.Agents)-1))
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
		"Activities come from agents and are shown for the currently selected project.",
	)

	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) tableViews() []string {
	spacedTable := lipgloss.NewStyle().MarginRight(TableGap)
	return []string{
		spacedTable.Render(m.projectTable.View()),
		spacedTable.Render(m.agentTable.View()),
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
		rows = append(rows, table.Row{project.State.String(), project.Name})
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

	agentRows := make([]table.Row, 0, len(project.Agents))
	for _, agentInstance := range project.Agents {
		agentRows = append(agentRows, table.Row{agentInstance.State().String(), agentInstance.Name()})
	}
	if len(agentRows) == 0 {
		agentRows = append(agentRows, table.Row{"-", "No agents"})
	}
	m.agentTable.SetRows(agentRows)

	activityRows := make([]table.Row, 0, len(project.Activities))
	for i := len(project.Activities) - 1; i >= 0; i-- {
		activityRows = append(activityRows, table.Row{project.Activities[i].Timestamp, project.Activities[i].Text})
	}
	if len(activityRows) == 0 {
		activityRows = append(activityRows, table.Row{"-", "No activity yet"})
	}
	m.activityTable.SetRows(activityRows)
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
	return fmt.Sprintf("%s (%s)", project.Name, project.State)
}

func (m *Model) selectedProjectWorkplaceSummary() string {
	if m.isCreateProjectSelected() {
		return "not set"
	}

	project := m.selectedProject()
	if project == nil || strings.TrimSpace(project.Workplace) == "" {
		return "not set"
	}
	return project.Workplace
}

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

func (m *Model) selectedAgentIndex() int {
	project := m.selectedProject()
	if project == nil || len(project.Agents) == 0 {
		return -1
	}
	index := m.agentTable.Cursor()
	if index < 0 || index >= len(project.Agents) {
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
	return project.Agents[index]
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
		} else {
			m.startSelectedProject()
		}
		return false, true, nil
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
		} else {
			m.cycleSelectedProjectState()
		}
		return false, true, nil
	default:
		return false, false, nil
	}
}

func (m *Model) openSelectedProjectEditor() tea.Cmd {
	m.mode = projectEditorViewMode
	m.projectEditor.errorMessage = ""
	m.projectEditor.projectIndex = m.selectedProjectIndex()
	if m.isCreateProjectSelected() {
		m.projectEditor.nameInput.SetValue("")
		m.projectEditor.workplaceInput.SetValue("")
		m.projectEditor.instructionsInput.SetValue("")
		m.projectEditor.instructionsInput.CursorStart()
		return m.setProjectEditorFocus(0)
	}

	project := m.selectedProject()
	if project == nil {
		return nil
	}

	m.projectEditor.workplaceInput.SetValue(project.Workplace)
	m.projectEditor.workplaceInput.CursorEnd()
	m.projectEditor.nameInput.SetValue(project.Name)
	m.projectEditor.nameInput.CursorEnd()
	m.projectEditor.instructionsInput.SetValue(project.Instructions)
	m.projectEditor.instructionsInput.CursorEnd()
	return m.setProjectEditorFocus(0)
}

func (m *Model) closeSelectedProjectEditor() {
	m.mode = tableViewMode
	m.projectEditor.errorMessage = ""
	m.projectEditor.projectIndex = -1
	m.projectEditor.nameInput.Blur()
	m.projectEditor.workplaceInput.Blur()
	m.projectEditor.instructionsInput.Blur()
}

func (m *Model) setProjectEditorFocus(index int) tea.Cmd {
	m.projectEditor.focusIndex = ((index % 4) + 4) % 4
	if m.projectEditor.focusIndex == 0 {
		m.projectEditor.workplaceInput.Blur()
		m.projectEditor.instructionsInput.Blur()
		return m.projectEditor.nameInput.Focus()
	}
	if m.projectEditor.focusIndex == 1 {
		m.projectEditor.nameInput.Blur()
		m.projectEditor.instructionsInput.Blur()
		return m.projectEditor.workplaceInput.Focus()
	}
	if m.projectEditor.focusIndex == 2 {
		m.projectEditor.nameInput.Blur()
		m.projectEditor.workplaceInput.Blur()
		return m.projectEditor.instructionsInput.Focus()
	}
	m.projectEditor.nameInput.Blur()
	m.projectEditor.workplaceInput.Blur()
	m.projectEditor.instructionsInput.Blur()
	return nil
}

func (m Model) projectEditorView() string {
	title := "Create project"
	if m.projectEditor.projectIndex >= 0 && m.projectEditor.projectIndex < len(m.projects) {
		title = fmt.Sprintf("Edit project: %s", m.projects[m.projectEditor.projectIndex].Name)
	}

	lines := []string{
		title,
		"",
		"Name",
		m.projectEditor.nameInput.View(),
		"",
		"Workplace",
		m.projectEditor.workplaceInput.View(),
		"",
		"Instructions",
		m.projectEditor.instructionsInput.View(),
		"",
		m.projectEditorOKButtonView(),
	}

	if m.projectEditor.errorMessage != "" {
		lines = append(lines, "", m.projectEditor.errorMessage)
	}

	lines = append(lines, "", "tab switches focus, enter saves on OK, esc cancels, click OK to save.")
	return strings.Join(lines, "\n")
}

func (m Model) projectEditorOKButtonView() string {
	button := "[ OK ]"
	if m.projectEditor.focusIndex == 3 {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render(button)
	}
	return lipgloss.NewStyle().Bold(true).Render(button)
}

func (m Model) projectEditorOKButtonBounds() (left int, top int, right int, bottom int) {
	width := lipgloss.Width(m.projectEditorOKButtonView())
	top = 11 + strings.Count(m.projectEditor.instructionsInput.View(), "\n")
	return 0, top, max(0, width-1), top
}

func (m *Model) updateProjectEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		left, top, right, bottom := m.projectEditorOKButtonBounds()
		if mouse.X >= left && mouse.X <= right && mouse.Y >= top && mouse.Y <= bottom {
			return *m, m.saveSelectedProjectEditor()
		}
		return *m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return *m, tea.Quit
		case "esc":
			m.closeSelectedProjectEditor()
			return *m, nil
		case "tab", "shift+tab":
			step := 1
			if msg.String() == "shift+tab" {
				step = -1
			}
			return *m, m.setProjectEditorFocus(m.projectEditor.focusIndex + step)
		case "enter":
			if m.projectEditor.focusIndex == 3 {
				return *m, m.saveSelectedProjectEditor()
			}
			if m.projectEditor.focusIndex == 0 {
				return *m, m.setProjectEditorFocus(1)
			}
			if m.projectEditor.focusIndex == 1 {
				return *m, m.setProjectEditorFocus(2)
			}
		}
	}

	if m.projectEditor.focusIndex == 0 {
		var cmd tea.Cmd
		m.projectEditor.nameInput, cmd = m.projectEditor.nameInput.Update(msg)
		return *m, cmd
	}
	if m.projectEditor.focusIndex == 1 {
		var cmd tea.Cmd
		m.projectEditor.workplaceInput, cmd = m.projectEditor.workplaceInput.Update(msg)
		return *m, cmd
	}
	if m.projectEditor.focusIndex == 2 {
		var cmd tea.Cmd
		m.projectEditor.instructionsInput, cmd = m.projectEditor.instructionsInput.Update(msg)
		return *m, cmd
	}

	return *m, nil
}

func (m *Model) saveSelectedProjectEditor() tea.Cmd {
	name := strings.TrimSpace(m.projectEditor.nameInput.Value())
	workplace := strings.TrimSpace(m.projectEditor.workplaceInput.Value())
	instructions := strings.TrimSpace(m.projectEditor.instructionsInput.Value())
	if name == "" {
		m.projectEditor.errorMessage = "Save failed: project name cannot be empty"
		return nil
	}

	if m.projectEditor.projectIndex < 0 {
		newProject := Project{
			Name:         name,
			Workplace:    workplace,
			Instructions: instructions,
			State:        agent.Stopped,
			Agents:       nil,
			AgentIDs:     nil,
			Activities:   nil,
		}

		if m.repository != nil {
			persistedProject, err := m.repository.CreateProjectConfiguration(name, workplace, instructions)
			if err != nil {
				m.projectEditor.errorMessage = fmt.Sprintf("Save failed: %v", err)
				return nil
			}
			newProject.ProjectID = persistedProject.ID
			newProject.State = parseAgentState(persistedProject.State)
		}

		m.projects = append(m.projects, newProject)
		m.refreshProjectTable()
		m.projectTable.SetCursor(len(m.projects))
		m.refreshSelectedProjectTables()
		m.closeSelectedProjectEditor()
		return nil
	}

	project := m.selectedProject()
	if project == nil {
		m.closeSelectedProjectEditor()
		return nil
	}

	if m.repository != nil && project.ProjectID > 0 {
		if err := m.repository.UpdateProjectConfiguration(project.ProjectID, name, workplace, instructions); err != nil {
			m.projectEditor.errorMessage = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
	}

	project.Name = name
	project.Workplace = workplace
	project.Instructions = instructions
	m.refreshProjectTable()
	m.closeSelectedProjectEditor()
	return nil
}

func (m *Model) resizeProjectEditor() {
	inputWidth := max(24, m.windowWidth-2)
	if inputWidth == 24 && m.windowWidth == 0 {
		inputWidth = DefaultActivityWidth
	}
	m.projectEditor.nameInput.SetWidth(inputWidth)
	m.projectEditor.workplaceInput.SetWidth(inputWidth)
	m.projectEditor.instructionsInput.SetWidth(inputWidth)
	m.projectEditor.instructionsInput.SetHeight(6)
}

func (m *Model) refreshProjectAndSelection(projectIndex int) {
	m.refreshProjectTable()
	if projectIndex == m.selectedProjectIndex() {
		m.refreshSelectedProjectTables()
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
	if m.repository == nil || project == nil || project.ProjectID <= 0 {
		return
	}

	if err := m.repository.UpdateProjectState(project.ProjectID, project.State.String()); err != nil {
		fmt.Fprintf(os.Stderr, "persist project state: %v\n", err)
	}
}

func (m *Model) persistProjectAgentStates(project *Project) {
	if m.repository == nil || project == nil || len(project.AgentIDs) == 0 {
		return
	}

	for i, agentID := range project.AgentIDs {
		if i >= len(project.Agents) || agentID <= 0 {
			continue
		}

		if err := m.repository.UpdateAgentState(agentID, project.Agents[i].State().String()); err != nil {
			fmt.Fprintf(os.Stderr, "persist agent state: %v\n", err)
		}
	}
}

func (m *Model) persistActivity(project *Project, agentIndex int, text string) {
	if m.repository == nil || project == nil || project.ProjectID <= 0 {
		return
	}

	agentID := sql.NullInt64{}
	if agentIndex >= 0 && agentIndex < len(project.AgentIDs) && project.AgentIDs[agentIndex] > 0 {
		agentID = sql.NullInt64{Int64: project.AgentIDs[agentIndex], Valid: true}
	}

	if _, err := m.repository.NewActivity(project.ProjectID, agentID, text); err != nil {
		fmt.Fprintf(os.Stderr, "persist activity: %v\n", err)
	}
}

func (m Model) Shutdown() {
	for i := range m.projects {
		project := &m.projects[i]
		for agentIndex, agentInstance := range project.Agents {
			if agentInstance.State() != agent.Running {
				continue
			}

			agentInstance.Stop()
			activityText := fmt.Sprintf("%s: stopped", agentInstance.Name())
			project.Activities = append(project.Activities, ActivityEntry{
				Timestamp: time.Now().Format("15:04:05"),
				Text:      activityText,
			})
			m.persistActivity(project, agentIndex, activityText)
		}
		if project.State == agent.Running {
			project.State = agent.Stopped
		}
		m.persistProjectState(project)
		m.persistProjectAgentStates(project)
	}
}
