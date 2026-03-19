package model

import (
	"testing"

	"MattiasHognas/Kennel/internal/ui/table"

	tea "charm.land/bubbletea/v2"
)

func TestEnterOpensProjectEditorAndClickingOKPersistsWorkplace(t *testing.T) {
	repo := newTestRepository(t)

	storedProject, err := repo.CreateProject("Configured Project")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	m := NewModel(table.Styles{}, table.Styles{}, []Project{{
		ProjectID: storedProject.ID,
		Name:      storedProject.Name,
	}}, repo)
	m.SetFocus(0)
	m.projectTable.SetCursor(1)

	openedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		cmd()
	}
	updatedModel, ok := openedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", openedModel)
	}
	if updatedModel.mode != projectEditorViewMode {
		t.Fatalf("mode = %v, want projectEditorViewMode", updatedModel.mode)
	}

	updatedModel.projectEditor.nameInput.SetValue("Configured Project Updated")
	updatedModel.projectEditor.workplaceInput.SetValue(`C:\work\kennel`)
	updatedModel.projectEditor.instructionsInput.SetValue("step one\nstep two")
	left, top, _, _ := updatedModel.projectEditorOKButtonBounds()
	clickedModel, saveCmd := updatedModel.Update(tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))
	if saveCmd != nil {
		saveCmd()
	}
	updatedModel, ok = clickedModel.(Model)
	if !ok {
		t.Fatalf("clicked model type = %T, want Model", clickedModel)
	}
	if updatedModel.mode != tableViewMode {
		t.Fatalf("mode after save = %v, want tableViewMode", updatedModel.mode)
	}
	if updatedModel.projects[0].Name != "Configured Project Updated" {
		t.Fatalf("model name = %q, want %q", updatedModel.projects[0].Name, "Configured Project Updated")
	}
	if updatedModel.projects[0].Workplace != `C:\work\kennel` {
		t.Fatalf("model workplace = %q, want %q", updatedModel.projects[0].Workplace, `C:\work\kennel`)
	}
	if updatedModel.projects[0].Instructions != "step one\nstep two" {
		t.Fatalf("model instructions = %q, want %q", updatedModel.projects[0].Instructions, "step one\nstep two")
	}

	persistedProject, err := repo.ReadProject(storedProject.ID)
	if err != nil {
		t.Fatalf("read project after save: %v", err)
	}
	if persistedProject.Name != "Configured Project Updated" {
		t.Fatalf("stored name = %q, want %q", persistedProject.Name, "Configured Project Updated")
	}
	if persistedProject.Workplace != `C:\work\kennel` {
		t.Fatalf("stored workplace = %q, want %q", persistedProject.Workplace, `C:\work\kennel`)
	}
	if persistedProject.Instructions != "step one\nstep two" {
		t.Fatalf("stored instructions = %q, want %q", persistedProject.Instructions, "step one\nstep two")
	}
}

func TestCreateNewRowOpensEditorAndCreatesProject(t *testing.T) {
	repo := newTestRepository(t)

	m := NewModel(table.Styles{}, table.Styles{}, nil, repo)
	m.SetFocus(0)

	if !m.isCreateProjectSelected() {
		t.Fatalf("create row should be selected by default")
	}

	openedModel, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil {
		cmd()
	}
	updatedModel, ok := openedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", openedModel)
	}
	if updatedModel.mode != projectEditorViewMode {
		t.Fatalf("mode = %v, want projectEditorViewMode", updatedModel.mode)
	}

	updatedModel.projectEditor.nameInput.SetValue("New Project")
	updatedModel.projectEditor.workplaceInput.SetValue(`C:\new\project`)
	updatedModel.projectEditor.instructionsInput.SetValue("do this\nand that")
	left, top, _, _ := updatedModel.projectEditorOKButtonBounds()
	clickedModel, saveCmd := updatedModel.Update(tea.MouseClickMsg(tea.Mouse{X: left + 1, Y: top}))
	if saveCmd != nil {
		saveCmd()
	}
	updatedModel, ok = clickedModel.(Model)
	if !ok {
		t.Fatalf("clicked model type = %T, want Model", clickedModel)
	}
	if len(updatedModel.projects) != 1 {
		t.Fatalf("project count = %d, want 1", len(updatedModel.projects))
	}
	if updatedModel.projects[0].Name != "New Project" {
		t.Fatalf("model name = %q, want %q", updatedModel.projects[0].Name, "New Project")
	}
	if updatedModel.projectTable.Cursor() != 1 {
		t.Fatalf("project cursor = %d, want 1", updatedModel.projectTable.Cursor())
	}

	projects, err := repo.ReadProjects()
	if err != nil {
		t.Fatalf("read projects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("stored project count = %d, want 1", len(projects))
	}
	if projects[0].Name != "New Project" {
		t.Fatalf("stored name = %q, want %q", projects[0].Name, "New Project")
	}
	if projects[0].Workplace != `C:\new\project` {
		t.Fatalf("stored workplace = %q, want %q", projects[0].Workplace, `C:\new\project`)
	}
	if projects[0].Instructions != "do this\nand that" {
		t.Fatalf("stored instructions = %q, want %q", projects[0].Instructions, "do this\nand that")
	}
}
