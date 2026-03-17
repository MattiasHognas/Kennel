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
	workplaceInput    textinput.Model
	instructionsInput textarea.Model
	focusIndex        int
	errorMessage      string
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
				key.WithHelp("space", "toggle project"),
			),
		},
	}

	m.Sources = m.BuildActivitySources()
	m.syncAllProjectStates()
	m.refreshProjectTable()
	m.refreshSelectedProjectTables()
	m.resizeProjectEditor()
	return m
}

func newProjectEditor() projectEditor {
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
		workplaceInput:    workplaceInput,
		instructionsInput: instructionsInput,
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
		"tab/shift+tab switches tables, enter edits the selected project, space toggles it, s starts, p stops.",
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
	rows := make([]table.Row, 0, len(m.projects))
	for _, project := range m.projects {
		rows = append(rows, table.Row{project.State.String(), project.Name})
	}
	m.projectTable.SetRows(rows)
}

func (m *Model) refreshSelectedProjectTables() {
	project := m.selectedProject()
	if project == nil {
		m.agentTable.SetRows([]table.Row{{"-", "No agents"}})
		m.activityTable.SetRows([]table.Row{{"-", "No activity"}})
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
	if len(m.projects) == 0 {
		return -1
	}
	return m.projectTable.Cursor()
}

func (m *Model) selectedProject() *Project {
	index := m.selectedProjectIndex()
	if index < 0 || index >= len(m.projects) {
		return nil
	}
	return &m.projects[index]
}

func (m *Model) selectedProjectSummary() string {
	project := m.selectedProject()
	if project == nil {
		return "none"
	}
	return fmt.Sprintf("%s (%s)", project.Name, project.State)
}

func (m *Model) selectedProjectWorkplaceSummary() string {
	project := m.selectedProject()
	if project == nil || strings.TrimSpace(project.Workplace) == "" {
		return "not set"
	}
	return project.Workplace
}

func (m *Model) startSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State == agent.Running {
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Run()
	}
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) stopSelectedProject() {
	projectIndex := m.selectedProjectIndex()
	project := m.selectedProject()
	if project == nil || project.State == agent.Stopped {
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Stop()
	}
	m.persistProjectAgentStates(project)
	m.refreshProjectAndSelection(projectIndex)
}

func (m *Model) toggleSelectedProject() {
	project := m.selectedProject()
	if project == nil {
		return
	}

	if project.State == agent.Running {
		m.stopSelectedProject()
		return
	}

	m.startSelectedProject()
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
		m.startSelectedProject()
		return false, true, nil
	case key.Matches(msg, m.keymap.stopProject):
		m.stopSelectedProject()
		return false, true, nil
	case key.Matches(msg, m.keymap.toggleProject):
		m.toggleSelectedProject()
		return false, true, nil
	default:
		return false, false, nil
	}
}

func (m *Model) openSelectedProjectEditor() tea.Cmd {
	project := m.selectedProject()
	if project == nil {
		return nil
	}

	m.mode = projectEditorViewMode
	m.projectEditor.errorMessage = ""
	m.projectEditor.workplaceInput.SetValue(project.Workplace)
	m.projectEditor.workplaceInput.CursorEnd()
	m.projectEditor.instructionsInput.SetValue(project.Instructions)
	m.projectEditor.instructionsInput.CursorEnd()
	return m.setProjectEditorFocus(0)
}

func (m *Model) closeSelectedProjectEditor() {
	m.mode = tableViewMode
	m.projectEditor.errorMessage = ""
	m.projectEditor.workplaceInput.Blur()
	m.projectEditor.instructionsInput.Blur()
}

func (m *Model) setProjectEditorFocus(index int) tea.Cmd {
	m.projectEditor.focusIndex = ((index % 3) + 3) % 3
	if m.projectEditor.focusIndex == 0 {
		m.projectEditor.instructionsInput.Blur()
		return m.projectEditor.workplaceInput.Focus()
	}
	if m.projectEditor.focusIndex == 1 {
		m.projectEditor.workplaceInput.Blur()
		return m.projectEditor.instructionsInput.Focus()
	}
	m.projectEditor.workplaceInput.Blur()
	m.projectEditor.instructionsInput.Blur()
	return nil
}

func (m Model) projectEditorView() string {
	project := m.selectedProject()
	if project == nil {
		return "No project selected."
	}

	lines := []string{
		fmt.Sprintf("Edit project: %s", project.Name),
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
	if m.projectEditor.focusIndex == 1 {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render(button)
	}
	return lipgloss.NewStyle().Bold(true).Render(button)
}

func (m Model) projectEditorOKButtonBounds() (left int, top int, right int, bottom int) {
	width := lipgloss.Width(m.projectEditorOKButtonView())
	top = 8 + strings.Count(m.projectEditor.instructionsInput.View(), "\n")
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
			if m.projectEditor.focusIndex == 2 {
				return *m, m.saveSelectedProjectEditor()
			}
			if m.projectEditor.focusIndex == 0 {
				return *m, m.setProjectEditorFocus(1)
			}
		}
	}

	if m.projectEditor.focusIndex == 0 {
		var cmd tea.Cmd
		m.projectEditor.workplaceInput, cmd = m.projectEditor.workplaceInput.Update(msg)
		return *m, cmd
	}
	if m.projectEditor.focusIndex == 1 {
		var cmd tea.Cmd
		m.projectEditor.instructionsInput, cmd = m.projectEditor.instructionsInput.Update(msg)
		return *m, cmd
	}

	return *m, nil
}

func (m *Model) saveSelectedProjectEditor() tea.Cmd {
	project := m.selectedProject()
	if project == nil {
		m.closeSelectedProjectEditor()
		return nil
	}

	workplace := strings.TrimSpace(m.projectEditor.workplaceInput.Value())
	instructions := strings.TrimSpace(m.projectEditor.instructionsInput.Value())
	if m.repository != nil && project.ProjectID > 0 {
		if err := m.repository.UpdateProjectConfiguration(project.ProjectID, workplace, instructions); err != nil {
			m.projectEditor.errorMessage = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
	}

	project.Workplace = workplace
	project.Instructions = instructions
	m.closeSelectedProjectEditor()
	return nil
}

func (m *Model) resizeProjectEditor() {
	inputWidth := max(24, m.windowWidth-2)
	if inputWidth == 24 && m.windowWidth == 0 {
		inputWidth = DefaultActivityWidth
	}
	m.projectEditor.workplaceInput.SetWidth(inputWidth)
	m.projectEditor.instructionsInput.SetWidth(inputWidth)
	m.projectEditor.instructionsInput.SetHeight(6)
}

func (m *Model) syncAllProjectStates() {
	for i := range m.projects {
		m.syncProjectState(i)
	}
}

func (m *Model) refreshProjectAndSelection(projectIndex int) {
	m.syncProjectState(projectIndex)
	m.refreshProjectTable()
	if projectIndex == m.selectedProjectIndex() {
		m.refreshSelectedProjectTables()
	}
}

func (m *Model) syncProjectState(projectIndex int) {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return
	}

	for _, agentInstance := range m.projects[projectIndex].Agents {
		if agentInstance.State() == agent.Running {
			m.projects[projectIndex].State = agent.Running
			return
		}
	}
	m.projects[projectIndex].State = agent.Stopped
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
		m.persistProjectAgentStates(project)
	}
	for i := range m.projects {
		m.syncProjectState(i)
	}
}
