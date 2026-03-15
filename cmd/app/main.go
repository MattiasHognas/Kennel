package main

import (
	"fmt"
	"os"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	defaultTableWidth  = 36
	defaultTableHeight = 6
)

func main() {

	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

type keymap struct {
	save key.Binding
	quit key.Binding
}

type model struct {
	tables []table.Model
	keymap keymap
}

func (m model) Init() tea.Cmd {
	return nil
}

func initialModel() model {
	style := lipgloss.NewStyle()
	style.Background(lipgloss.Color("#282828")).
		Foreground(lipgloss.Color("#ebdbb2")).
		Padding(1, 2)
	styles := table.Styles{
		Header: style.Bold(true),
		Cell:   style,
	}

	table1 := table.New(
		table.WithColumns([]table.Column{
			{Title: "ID", Width: 10},
			{Title: "Name", Width: 20},
		}),
		table.WithRows([]table.Row{
			{"1", "Alice"},
			{"2", "Bob"},
		}),
		table.WithStyles(styles),
		table.WithFocused(true),
		table.WithWidth(defaultTableWidth),
		table.WithHeight(defaultTableHeight),
	)
	tables := []table.Model{
		table1,
	}

	return model{
		tables: tables,
		keymap: keymap{
			quit: key.NewBinding(
				key.WithKeys("esc", "ctrl+c", "q"),
				key.WithHelp("esc/q", "quit"),
			),
		},
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resizeTables(msg.Width, msg.Height)
		return m, nil
	case tea.KeyPressMsg:
		if key.Matches(msg, m.keymap.quit) {
			return m, tea.Quit
		}
	}

	return m.updateTables(msg)
}

func (m model) updateTables(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	for i := range m.tables {
		m.tables[i], cmd = m.tables[i].Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, m.tableViews()...),
		"",
		"Press esc, q, or ctrl+c to quit.",
	)
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m model) tableViews() []string {
	var views []string
	for i := range m.tables {
		views = append(views, m.tables[i].View())
	}
	return views
}

func (m *model) resizeTables(width, height int) {
	if len(m.tables) == 0 {
		return
	}

	tableWidth := max(defaultTableWidth, width/len(m.tables))
	tableHeight := max(defaultTableHeight, height-3)

	for i := range m.tables {
		m.tables[i].SetWidth(tableWidth)
		m.tables[i].SetHeight(tableHeight)
	}
}

// func runagents() {

// agent1 := agent.NewAgent()
// agent2 := agent.NewAgent()

// fmt.Printf("Agent1 state: %v\n", agent1.State())
// fmt.Printf("Agent2 state: %v\n", agent2.State())

// channel1 := agent1.Run()

// fmt.Printf("Agent1 state after run: %v\n", agent1.State())

// go func() {
// 	for event := range channel1 {
// 		fmt.Printf("Received event: %v\n", event.Payload)
// 	}
// }()

// channel2 := agent2.Run()

// fmt.Printf("Agent2 state after run: %v\n", agent2.State())

// go func() {
// 	for event := range channel2 {
// 		fmt.Printf("Received event: %v\n", event.Payload)
// 	}
// }()

// fmt.Printf("Waiting for agents to publish events...\n")

// // wating 20 seconds
// time.Sleep(time.Second * 20)

// fmt.Printf("Stopping agents...\n")

// agent1.Stop()
// close(channel1)

// agent2.Stop()
// close(channel2)
// }
