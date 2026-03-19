package model

import (
	"fmt"
	"strings"

	"MattiasHognas/Kennel/internal/ui"
	agent "MattiasHognas/Kennel/internal/workers"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type projectEditor struct {
	nameInput         textinput.Model
	workplaceInput    textinput.Model
	instructionsInput textarea.Model
	focusIndex        int
	errorMessage      string
	projectIndex      int
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
		return ui.ButtonActiveStyle.Render(button)
	}
	return ui.ButtonInactiveStyle.Render(button)
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
