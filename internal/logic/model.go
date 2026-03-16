package model

import (
	eventbus "MattiasHognas/Kennel/internal/events"
	agent "MattiasHognas/Kennel/internal/workers"

	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Project struct {
	Name       string
	State      agent.AgentState
	Agents     []agent.AgentContract
	Activities []string
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
	startProject  key.Binding
	pauseProject  key.Binding
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
}

const (
	DefaultProjectWidth  = 28
	DefaultAgentWidth    = 22
	DefaultActivityWidth = 60
	DefaultTableHeight   = 8
	FooterHeight         = 4
)

func NewModel(focusedStyles, blurredStyles table.Styles, projects []Project) Model {
	return Model{
		projectTable:  newProjectTable(blurredStyles),
		agentTable:    newSingleColumnTable("Agents", DefaultAgentWidth, blurredStyles),
		activityTable: newSingleColumnTable("Activity", DefaultActivityWidth, blurredStyles),
		focusedStyles: focusedStyles,
		blurredStyles: blurredStyles,
		projects:      projects,
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
			startProject: key.NewBinding(
				key.WithKeys("s"),
				key.WithHelp("s", "start project"),
			),
			pauseProject: key.NewBinding(
				key.WithKeys("p"),
				key.WithHelp("p", "pause project"),
			),
			toggleProject: key.NewBinding(
				key.WithKeys("enter", "space"),
				key.WithHelp("enter", "toggle project"),
			),
		},
	}
}

func newProjectTable(styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Projects", Width: DefaultProjectWidth - 12},
			{Title: "State", Width: 10},
		}),
		table.WithStyles(styles),
		table.WithWidth(DefaultProjectWidth),
		table.WithHeight(DefaultTableHeight),
	)
}

func newSingleColumnTable(title string, width int, styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{{Title: title, Width: max(12, width-2)}}),
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

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keymap.quit):
			return m, tea.Quit
		case key.Matches(msg, m.keymap.nextTable):
			m.SetFocus(m.focusIndex + 1)
			return m, nil
		case key.Matches(msg, m.keymap.prevTable):
			m.SetFocus(m.focusIndex - 1)
			return m, nil
		case key.Matches(msg, m.keymap.startProject):
			m.startSelectedProject()
			return m, nil
		case key.Matches(msg, m.keymap.pauseProject):
			m.pauseSelectedProject()
			return m, nil
		case key.Matches(msg, m.keymap.toggleProject):
			m.toggleSelectedProject()
			return m, nil
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
	header := fmt.Sprintf("Selected project: %s", m.selectedProjectSummary())
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, m.tableViews()...),
		"",
		"tab/shift+tab switches tables, s starts, p pauses, enter toggles the selected project.",
		"Activities come from agents and are shown for the currently selected project.",
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m Model) tableViews() []string {
	return []string{
		m.projectTable.View(),
		m.agentTable.View(),
		m.activityTable.View(),
	}
}

func (m *Model) ResizeTables(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	tableHeight := max(DefaultTableHeight, height-FooterHeight)
	projectWidth := max(DefaultProjectWidth, width/4)
	agentWidth := max(DefaultAgentWidth, width/5)
	activityWidth := max(DefaultActivityWidth, width-projectWidth-agentWidth-4)

	if projectWidth+agentWidth+activityWidth > width {
		activityWidth = max(32, width-projectWidth-agentWidth-2)
	}

	m.projectTable.SetWidth(projectWidth)
	m.projectTable.SetHeight(tableHeight)
	m.projectTable.SetColumns([]table.Column{
		{Title: "Projects", Width: max(16, projectWidth-12)},
		{Title: "State", Width: 10},
	})

	m.agentTable.SetWidth(agentWidth)
	m.agentTable.SetHeight(tableHeight)
	m.agentTable.SetColumns([]table.Column{{Title: "Agents", Width: max(12, agentWidth-2)}})

	m.activityTable.SetWidth(activityWidth)
	m.activityTable.SetHeight(tableHeight)
	m.activityTable.SetColumns([]table.Column{{Title: "Activity", Width: max(24, activityWidth-2)}})

	m.RefreshAllTables()
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

func (m *Model) RefreshAllTables() {
	m.refreshProjectTable()
	m.refreshSelectedProjectTables()
}

func (m *Model) refreshProjectTable() {
	rows := make([]table.Row, 0, len(m.projects))
	for i := range m.projects {
		m.syncProjectState(i)
		rows = append(rows, table.Row{m.projects[i].Name, m.projects[i].State.String()})
	}
	m.projectTable.SetRows(rows)
}

func (m *Model) refreshSelectedProjectTables() {
	project := m.selectedProject()
	if project == nil {
		m.agentTable.SetRows([]table.Row{{"No agents"}})
		m.activityTable.SetRows([]table.Row{{"No activity"}})
		return
	}

	agentRows := make([]table.Row, 0, len(project.Agents))
	for _, agentInstance := range project.Agents {
		agentRows = append(agentRows, table.Row{fmt.Sprintf("%s [%s]", agentInstance.Name(), agentInstance.State())})
	}
	if len(agentRows) == 0 {
		agentRows = append(agentRows, table.Row{"No agents"})
	}
	m.agentTable.SetRows(agentRows)

	activityRows := make([]table.Row, 0, len(project.Activities))
	for i := len(project.Activities) - 1; i >= 0; i-- {
		activityRows = append(activityRows, table.Row{project.Activities[i]})
	}
	if len(activityRows) == 0 {
		activityRows = append(activityRows, table.Row{"No activity yet"})
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

func (m *Model) startSelectedProject() {
	project := m.selectedProject()
	if project == nil || project.State == agent.Running {
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Run()
	}
	m.RefreshAllTables()
}

func (m *Model) pauseSelectedProject() {
	project := m.selectedProject()
	if project == nil || project.State == agent.Paused || project.State == agent.Stopped {
		return
	}

	for _, agentInstance := range project.Agents {
		agentInstance.Pause()
	}
	m.RefreshAllTables()
}

func (m *Model) toggleSelectedProject() {
	project := m.selectedProject()
	if project == nil {
		return
	}

	if project.State == agent.Running {
		m.pauseSelectedProject()
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

	project.Activities = append(project.Activities, fmt.Sprintf("%s: %s", project.Agents[source.agentIndex].Name(), text))
	if len(project.Activities) > 100 {
		project.Activities = project.Activities[len(project.Activities)-100:]
	}

	m.syncProjectState(source.projectIndex)
	m.refreshProjectTable()
	if source.projectIndex == m.selectedProjectIndex() {
		m.refreshSelectedProjectTables()
	}
}

func (m *Model) syncProjectState(projectIndex int) {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return
	}

	state := agent.Stopped
	for _, agentInstance := range m.projects[projectIndex].Agents {
		switch agentInstance.State() {
		case agent.Running:
			m.projects[projectIndex].State = agent.Running
			return
		case agent.Paused:
			state = agent.Paused
		}
	}
	m.projects[projectIndex].State = state
}
