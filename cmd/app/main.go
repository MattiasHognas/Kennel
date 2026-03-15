package main

import (
	"fmt"
	"os"

	"MattiasHognas/Kennel/internal/agent"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	defaultProjectWidth  = 28
	defaultAgentWidth    = 22
	defaultActivityWidth = 60
	defaultTableHeight   = 8
	footerHeight         = 4
)

type project struct {
	name       string
	state      agent.AgentState
	agents     []agent.AgentContract
	activities []string
}

type activitySource struct {
	projectIndex int
	agentIndex   int
	channel      agent.EventChan
}

type activityMsg struct {
	source activitySource
	text   string
}

type keymap struct {
	quit          key.Binding
	nextTable     key.Binding
	prevTable     key.Binding
	startProject  key.Binding
	pauseProject  key.Binding
	toggleProject key.Binding
}

type model struct {
	projectTable  table.Model
	agentTable    table.Model
	activityTable table.Model
	focusIndex    int
	projects      []project
	sources       []activitySource
	windowWidth   int
	windowHeight  int
	keymap        keymap
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func initialModel() model {
	styles := newTableStyles()
	projects := sampleProjects()

	m := model{
		projectTable:  newProjectTable(styles),
		agentTable:    newSingleColumnTable("Agents", defaultAgentWidth, styles),
		activityTable: newSingleColumnTable("Activity", defaultActivityWidth, styles),
		projects:      projects,
		keymap: keymap{
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

	m.sources = m.buildActivitySources()
	m.resizeTables(defaultProjectWidth+defaultAgentWidth+defaultActivityWidth, defaultTableHeight+footerHeight+4)
	m.refreshAllTables()
	m.setFocus(0)

	return m
}

func (m model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.sources))
	for _, source := range m.sources {
		cmds = append(cmds, waitForActivity(source))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		m.resizeTables(msg.Width, msg.Height)
		return m, nil

	case activityMsg:
		m.recordActivity(msg.source, msg.text)
		return m, waitForActivity(msg.source)

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keymap.quit):
			return m, tea.Quit
		case key.Matches(msg, m.keymap.nextTable):
			m.setFocus(m.focusIndex + 1)
			return m, nil
		case key.Matches(msg, m.keymap.prevTable):
			m.setFocus(m.focusIndex - 1)
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

func (m model) updateTables(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if project := m.selectedProject(); project != nil && len(project.agents) > 0 {
			m.agentTable.SetCursor(min(previousAgent, len(project.agents)-1))
		} else {
			m.agentTable.SetCursor(0)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
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

func (m model) tableViews() []string {
	return []string{
		m.projectTable.View(),
		m.agentTable.View(),
		m.activityTable.View(),
	}
}

func (m *model) resizeTables(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	tableHeight := max(defaultTableHeight, height-footerHeight)
	projectWidth := max(defaultProjectWidth, width/4)
	agentWidth := max(defaultAgentWidth, width/5)
	activityWidth := max(defaultActivityWidth, width-projectWidth-agentWidth-4)

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

	m.refreshAllTables()
}

func (m *model) setFocus(index int) {
	m.focusIndex = (index + 3) % 3
	m.projectTable.Blur()
	m.agentTable.Blur()
	m.activityTable.Blur()

	switch m.focusIndex {
	case 0:
		m.projectTable.Focus()
	case 1:
		m.agentTable.Focus()
	default:
		m.activityTable.Focus()
	}
}

func (m *model) refreshAllTables() {
	m.refreshProjectTable()
	m.refreshSelectedProjectTables()
}

func (m *model) refreshProjectTable() {
	rows := make([]table.Row, 0, len(m.projects))
	for i := range m.projects {
		m.syncProjectState(i)
		rows = append(rows, table.Row{m.projects[i].name, m.projects[i].state.String()})
	}
	m.projectTable.SetRows(rows)
}

func (m *model) refreshSelectedProjectTables() {
	project := m.selectedProject()
	if project == nil {
		m.agentTable.SetRows([]table.Row{{"No agents"}})
		m.activityTable.SetRows([]table.Row{{"No activity"}})
		return
	}

	agentRows := make([]table.Row, 0, len(project.agents))
	for _, agentInstance := range project.agents {
		agentRows = append(agentRows, table.Row{fmt.Sprintf("%s [%s]", agentInstance.Name(), agentInstance.State())})
	}
	if len(agentRows) == 0 {
		agentRows = append(agentRows, table.Row{"No agents"})
	}
	m.agentTable.SetRows(agentRows)

	activityRows := make([]table.Row, 0, len(project.activities))
	for i := len(project.activities) - 1; i >= 0; i-- {
		activityRows = append(activityRows, table.Row{project.activities[i]})
	}
	if len(activityRows) == 0 {
		activityRows = append(activityRows, table.Row{"No activity yet"})
	}
	m.activityTable.SetRows(activityRows)
}

func (m *model) buildActivitySources() []activitySource {
	sources := make([]activitySource, 0)
	for projectIndex := range m.projects {
		for agentIndex, agentInstance := range m.projects[projectIndex].agents {
			sources = append(sources, activitySource{
				projectIndex: projectIndex,
				agentIndex:   agentIndex,
				channel:      agentInstance.SubscribeActivity(),
			})
		}
	}
	return sources
}

func (m *model) selectedProjectIndex() int {
	if len(m.projects) == 0 {
		return -1
	}
	return m.projectTable.Cursor()
}

func (m *model) selectedProject() *project {
	index := m.selectedProjectIndex()
	if index < 0 || index >= len(m.projects) {
		return nil
	}
	return &m.projects[index]
}

func (m *model) selectedProjectSummary() string {
	project := m.selectedProject()
	if project == nil {
		return "none"
	}
	return fmt.Sprintf("%s (%s)", project.name, project.state)
}

func (m *model) startSelectedProject() {
	project := m.selectedProject()
	if project == nil || project.state == agent.Running {
		return
	}

	for _, agentInstance := range project.agents {
		agentInstance.Run()
	}
	m.refreshAllTables()
}

func (m *model) pauseSelectedProject() {
	project := m.selectedProject()
	if project == nil || project.state == agent.Paused || project.state == agent.Stopped {
		return
	}

	for _, agentInstance := range project.agents {
		agentInstance.Pause()
	}
	m.refreshAllTables()
}

func (m *model) toggleSelectedProject() {
	project := m.selectedProject()
	if project == nil {
		return
	}

	if project.state == agent.Running {
		m.pauseSelectedProject()
		return
	}

	m.startSelectedProject()
}

func (m *model) recordActivity(source activitySource, text string) {
	if source.projectIndex < 0 || source.projectIndex >= len(m.projects) {
		return
	}

	project := &m.projects[source.projectIndex]
	if source.agentIndex < 0 || source.agentIndex >= len(project.agents) {
		return
	}

	project.activities = append(project.activities, fmt.Sprintf("%s: %s", project.agents[source.agentIndex].Name(), text))
	if len(project.activities) > 100 {
		project.activities = project.activities[len(project.activities)-100:]
	}

	m.syncProjectState(source.projectIndex)
	m.refreshProjectTable()
	if source.projectIndex == m.selectedProjectIndex() {
		m.refreshSelectedProjectTables()
	}
}

func (m *model) syncProjectState(projectIndex int) {
	if projectIndex < 0 || projectIndex >= len(m.projects) {
		return
	}

	state := agent.Stopped
	for _, agentInstance := range m.projects[projectIndex].agents {
		switch agentInstance.State() {
		case agent.Running:
			m.projects[projectIndex].state = agent.Running
			return
		case agent.Paused:
			state = agent.Paused
		}
	}
	m.projects[projectIndex].state = state
}

func newProjectTable(styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{
			{Title: "Projects", Width: defaultProjectWidth - 12},
			{Title: "State", Width: 10},
		}),
		table.WithStyles(styles),
		table.WithWidth(defaultProjectWidth),
		table.WithHeight(defaultTableHeight),
	)
}

func newSingleColumnTable(title string, width int, styles table.Styles) table.Model {
	return table.New(
		table.WithColumns([]table.Column{{Title: title, Width: max(12, width-2)}}),
		table.WithStyles(styles),
		table.WithWidth(width),
		table.WithHeight(defaultTableHeight),
	)
}

func newTableStyles() table.Styles {
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("#ebdbb2")).Padding(0, 1)
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#fbf1c7")).Background(lipgloss.Color("#3c3836")).Padding(0, 1)
	selected := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1d2021")).Background(lipgloss.Color("#8ec07c"))

	return table.Styles{
		Header:   header,
		Cell:     base,
		Selected: selected,
	}
}

func sampleProjects() []project {
	projectDefinitions := []struct {
		name   string
		agents []string
	}{
		{name: "Barky barky", agents: []string{"Planner", "UX", "Frontend Developer", "QA", "DevOps"}},
		{name: "Sniff sniff", agents: []string{"Planner", "Backend Developer", "QA"}},
		{name: "Grr Grr", agents: []string{"Planning", "Backend Developer 1", "Backend Developer 2", "QA", "DevOps"}},
	}

	projects := make([]project, 0, len(projectDefinitions))
	for _, definition := range projectDefinitions {
		agents := make([]agent.AgentContract, 0, len(definition.agents))
		for _, name := range definition.agents {
			agents = append(agents, agent.NewAgent(name))
		}

		projects = append(projects, project{
			name:   definition.name,
			state:  agent.Stopped,
			agents: agents,
		})
	}

	return projects
}

func waitForActivity(source activitySource) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-source.channel
		if !ok {
			return nil
		}
		return activityMsg{source: source, text: fmt.Sprint(event.Payload)}
	}
}
