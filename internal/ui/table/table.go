package table

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Column struct {
	Title string
	Width int
}

type Row []string

type Styles struct {
	Header   lipgloss.Style
	Cell     lipgloss.Style
	Selected lipgloss.Style
}

type Option func(*Model)

type Model struct {
	cols    []Column
	rows    []Row
	styles  Styles
	width   int
	height  int
	cursor  int
	start   int
	focused bool
}

func New(options ...Option) Model {
	m := Model{height: 1}
	for _, option := range options {
		option(&m)
	}
	m.clampCursor()
	m.adjustViewport()
	return m
}

func WithColumns(columns []Column) Option {
	return func(m *Model) {
		m.cols = append([]Column(nil), columns...)
	}
}

func WithRows(rows []Row) Option {
	return func(m *Model) {
		m.rows = cloneRows(rows)
	}
}

func WithStyles(styles Styles) Option {
	return func(m *Model) {
		m.styles = styles
	}
}

func WithWidth(width int) Option {
	return func(m *Model) {
		m.width = width
	}
}

func WithHeight(height int) Option {
	return func(m *Model) {
		m.height = max(1, height)
	}
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.focused {
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		m.SetCursor(m.cursor - 1)
	case "down", "j":
		m.SetCursor(m.cursor + 1)
	case "pgup", "b":
		m.SetCursor(m.cursor - m.bodyHeight())
	case "pgdown", "f":
		m.SetCursor(m.cursor + m.bodyHeight())
	case "home", "g":
		m.SetCursor(0)
	case "end", "G":
		m.SetCursor(len(m.rows) - 1)
	}

	return m, nil
}

func (m Model) View() string {
	lines := []string{m.renderHeader()}
	bodyHeight := m.bodyHeight()
	end := min(len(m.rows), m.start+bodyHeight)

	for rowIndex := m.start; rowIndex < end; rowIndex++ {
		lines = append(lines, m.renderRow(rowIndex))
	}

	for len(lines) < bodyHeight+1 {
		lines = append(lines, m.renderEmptyRow())
	}

	return strings.Join(lines, "\n")
}

func (m Model) Cursor() int {
	if len(m.rows) == 0 {
		return 0
	}
	return m.cursor
}

func (m *Model) SetCursor(cursor int) {
	m.cursor = cursor
	m.clampCursor()
	m.adjustViewport()
}

func (m *Model) SetRows(rows []Row) {
	m.rows = cloneRows(rows)
	m.clampCursor()
	m.adjustViewport()
}

func (m *Model) SetColumns(columns []Column) {
	m.cols = append([]Column(nil), columns...)
}

func (m *Model) SetStyles(styles Styles) {
	m.styles = styles
}

func (m *Model) SetWidth(width int) {
	m.width = width
}

func (m *Model) SetHeight(height int) {
	m.height = max(1, height)
	m.adjustViewport()
}

func (m *Model) Focus() {
	m.focused = true
}

func (m *Model) Blur() {
	m.focused = false
}

func (m Model) SelectedRow() Row {
	if len(m.rows) == 0 || m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return append(Row(nil), m.rows[m.cursor]...)
}

func (m Model) renderHeader() string {
	cells := make([]string, 0, len(m.cols))
	for _, column := range m.cols {
		cells = append(cells, m.styles.Header.Render(m.renderValue(column.Title, column.Width)))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func (m Model) renderRow(rowIndex int) string {
	cells := make([]string, 0, len(m.cols))
	for columnIndex, column := range m.cols {
		value := ""
		if columnIndex < len(m.rows[rowIndex]) {
			value = m.rows[rowIndex][columnIndex]
		}

		cell := m.styles.Cell.Render(m.renderValue(value, column.Width))
		if rowIndex == m.cursor {
			cell = m.styles.Selected.Render(cell)
		}

		cells = append(cells, cell)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func (m Model) renderEmptyRow() string {
	cells := make([]string, 0, len(m.cols))
	for _, column := range m.cols {
		cells = append(cells, m.styles.Cell.Render(m.renderValue("", column.Width)))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func (m Model) renderValue(value string, width int) string {
	if width <= 0 {
		return ""
	}

	truncated := truncate(value, width)
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Render(truncated)
}

func (m *Model) clampCursor() {
	if len(m.rows) == 0 {
		m.cursor = 0
		m.start = 0
		return
	}

	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
}

func (m *Model) adjustViewport() {
	bodyHeight := m.bodyHeight()
	if len(m.rows) <= bodyHeight {
		m.start = 0
		return
	}

	maxStart := len(m.rows) - bodyHeight
	m.start = max(0, min(m.start, maxStart))

	if m.cursor < m.start {
		m.start = m.cursor
	}
	if m.cursor >= m.start+bodyHeight {
		m.start = m.cursor - bodyHeight + 1
	}
	if m.start > maxStart {
		m.start = maxStart
	}
}

func (m Model) bodyHeight() int {
	return max(1, m.height)
}

func cloneRows(rows []Row) []Row {
	cloned := make([]Row, 0, len(rows))
	for _, row := range rows {
		cloned = append(cloned, append(Row(nil), row...))
	}
	return cloned
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
