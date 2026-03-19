package model

import (
	"path/filepath"
	"testing"

	repository "MattiasHognas/Kennel/internal/data"

	tea "charm.land/bubbletea/v2"
)

func mustUpdateModel(t *testing.T, m Model, msg tea.Msg) (Model, bool) {
	t.Helper()

	updatedModel, cmd := m.Update(msg)
	if cmd != nil {
		cmd()
	}
	result, ok := updatedModel.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updatedModel)
		return Model{}, false
	}
	return result, true
}

func newTestRepository(t *testing.T) *repository.SQLiteRepository {
	t.Helper()

	repo, err := repository.NewSQLiteRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	t.Cleanup(func() {
		_ = repo.Close()
	})

	return repo
}
