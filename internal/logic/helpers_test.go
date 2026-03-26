package model

import (
	"path/filepath"
	"testing"
	"time"

	repository "MattiasHognas/Kennel/internal/data"

	tea "charm.land/bubbletea/v2"
)

func runCmdWithTimeout(t *testing.T, cmd tea.Cmd, timeout time.Duration) tea.Msg {
	t.Helper()

	if cmd == nil {
		return nil
	}

	result := make(chan tea.Msg, 1)
	go func() {
		result <- cmd()
	}()

	select {
	case msg := <-result:
		return msg
	case <-time.After(timeout):
		t.Fatalf("command timed out after %s", timeout)
		return nil
	}
}

func mustUpdateModel(t *testing.T, m Model, msg tea.Msg) (Model, bool) {
	t.Helper()

	updatedModel, cmd := m.Update(msg)
	runCmdWithTimeout(t, cmd, 2*time.Second)
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
